package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The read that comes to serve no repository logs its serving-none line once it has published the
// mapping, whether or not the narrowing of GitHub intake's subscription after it succeeds. The
// daemon serves none, and refuses every issue for it, from the publication on, so a line written
// only at the end of a read whose last step failed would leave the log silent for as long as
// JetStream refuses, and say nothing at all of a period that ends with a read serving one again.
func TestTheServingNoneLineIsLoggedWhenTheReadsSubscriptionNarrowingFails(t *testing.T) {
	var narrowings atomic.Int64
	w, client, _, logged, _ := servingNoneLogRig(t, &installations{}, func(w *workflowRuntime) {
		w.setRepositories = func(ctx context.Context, repositories []ghrepo.Repository) error {
			if len(repositories) == 0 {
				narrowings.Add(1)
				return errors.New("JetStream refused the consumer update")
			}
			return w.consumers.SetRepositories(ctx, repositories)
		}
	})
	wantServingNone(t, logged, "at boot, serving both")
	client.set()
	w.requestMappingRead()
	testwait.Eventually(t, "the narrowing to no repository tried and refused", func() bool { return narrowings.Load() > 0 })
	if served(w, "acme/widgets") || served(w, "zeta/tools") {
		t.Fatal("a repository is still served once the publication of the empty mapping committed")
	}
	wantServingNone(t, logged, "once the publication committed, before its narrowing", api.NoRepositoryMapped("LEGION"))
	testwait.Eventually(t, "a retry of the read whose narrowing failed", func() bool { return narrowings.Load() > 1 })
	wantServingNone(t, logged, "while every narrowing is refused", api.NoRepositoryMapped("LEGION"))
}

// abortingStore is a record store whose abortAt'th live-record snapshot (Issues, counted from 1)
// answers the real snapshot and then aborts its transaction with a failing statement, so the
// transaction's COMMIT fails (pgx.ErrTxCommitRollback) after whatever the caller does in memory
// once the snapshot is read. Zero aborts none.
type abortingStore struct {
	record.Store
	abortAt atomic.Int64
	calls   atomic.Int64
}

func (s *abortingStore) Issues(ctx context.Context, tx pgx.Tx) ([]record.Issue, error) {
	issues, err := s.Store.Issues(ctx, tx)
	if n := s.calls.Add(1); err == nil && n == s.abortAt.Load() {
		_, _ = tx.Exec(ctx, "select 1/0")
	}
	return issues, err
}

// A read whose publication transaction has set the mapping in memory, and whose COMMIT then fails,
// still logs the serving-none line when that mapping serves none: the daemon serves none, and
// refuses every issue for it, from the set on. The read unmaps the last repositories; its first
// snapshot is the heard superset's (step 1) and its second the publication's, which is aborted.
func TestTheServingNoneLineIsLoggedWhenThePublicationsCommitFails(t *testing.T) {
	w, client, _, logged, _ := servingNoneLogRig(t, &installations{})
	wantServingNone(t, logged, "at boot, serving both")
	store := &abortingStore{Store: w.records}
	w.records = store
	client.set()
	store.abortAt.Store(store.calls.Load() + 2)
	if err := w.refreshMapping(context.Background()); !errors.Is(err, pgx.ErrTxCommitRollback) {
		t.Fatalf("the read whose publication was aborted = %v, want its COMMIT refused (%v)", err, pgx.ErrTxCommitRollback)
	}
	if served(w, "acme/widgets") || served(w, "zeta/tools") {
		t.Fatal("a repository is still served: the publication's set did not run before its COMMIT failed")
	}
	wantServingNone(t, logged, "after the publication's COMMIT failed", api.NoRepositoryMapped("LEGION"))
}

// The daemon comes to serve no repository with the same repositories mapped: its one repository,
// kept on its last answer while its installation read fails, then answers that an App is not
// installed on it. That is a transition into serving none, and the refresher's backoff starts again
// at 30 s, as the serving-none line's retryIn says, rather than keeping the wait the failed reads
// grew to (up to 10 min), which would leave an App installed a minute later unfound for ten.
func TestComingToServeNoneWithTheSameRepositoriesMappedStartsTheBackoffAgainAt30s(t *testing.T) {
	clock := newRecheckWaits()
	w, _, installs, _ := mappingRigWith(t, &installations{}, func(w *workflowRuntime) {
		w.dispatch.(*mappingDispatch).set(dispatch.RepoProject{Repo: "acme/widgets", Project: "LEGION", Default: true})
		w.recheckAfter = clock.after
	})
	if !served(w, "acme/widgets") {
		t.Fatal("acme/widgets not served at boot")
	}
	installs.setFailing(errors.New("GitHub answered 502"), "implement acme/widgets")
	w.requestMappingRead()
	before := grownToCap(t, clock, "acme/widgets is kept on its last answer")
	if !served(w, "acme/widgets") {
		t.Fatal("acme/widgets not kept on its last answer while its read fails")
	}
	installs.setFailing(nil, "implement acme/widgets")
	installs.setMissing(true, "review acme/widgets")
	clock.tick <- time.Now()
	testwait.Eventually(t, "acme/widgets left out and the refresher waiting again", func() bool {
		_, refusal, leftOut, _ := w.repositories.lookup("acme/widgets")
		return leftOut && refusal == "the review App is not installed on acme/widgets" && len(clock.recorded()) > before
	})
	if got := clock.recorded()[before]; got != 30*time.Second {
		t.Fatalf("the first wait after the daemon came to serve none = %s, want 30s: the backoff starts again on the transition", got)
	}
}

// A read that serves a repository again, out of serving none, keeps the backoff it has for what
// still waits: only a change of the repositories mapped and a transition into serving none start it
// again at 30 s. Two repositories left out for now through a GitHub outage grow the backoff to its
// cap; once one of them answers and is served, the other is read again at that cap, as the outage
// read it before, and not from 30 s.
func TestServingARepositoryAgainKeepsTheBackoffForWhatStillWaits(t *testing.T) {
	clock := newRecheckWaits()
	installs := &installations{}
	installs.setFailing(errors.New("GitHub answered 502"), "implement acme/widgets", "implement zeta/tools")
	w, _, installs, _ := mappingRigWith(t, installs, func(w *workflowRuntime) { w.recheckAfter = clock.after })
	if served(w, "acme/widgets") || served(w, "zeta/tools") {
		t.Fatal("a repository served at boot while both installation reads fail")
	}
	before := grownToCap(t, clock, "both are left out for now")
	installs.setFailing(nil, "implement acme/widgets")
	clock.tick <- time.Now()
	testwait.Eventually(t, "acme/widgets served and the refresher waiting again", func() bool {
		return served(w, "acme/widgets") && len(clock.recorded()) > before
	})
	if got := clock.recorded()[before]; got != 10*time.Minute {
		t.Fatalf("the first wait after acme/widgets was served again = %s, want the 10 min it had grown to: serving one again restarts no backoff", got)
	}
}

// A child an architect releases while the project maps no repository is refused with that cause,
// and the architect's notice tells it to map the project's repositories, as the child's own note
// does: Dispatch refuses setting an issue to a repository its project does not map, so "set its
// repository" is no remedy there.
func TestAChildReleasedWhileTheProjectMapsNoRepositoryTellsTheArchitectToMapOne(t *testing.T) {
	w, client, _, _ := mappingRigWith(t, &installations{}, func(w *workflowRuntime) {
		w.recheckAfter = func(time.Duration) <-chan time.Time { return nil }
	})
	rigHandlers(w)
	const root, child = "LEGION-1600", "LEGION-1601"
	liveRoot(t, w, client, root)
	client.set()
	read := w.requestMappingRead()
	testwait.Eventually(t, "the read of the empty mapping", func() bool { return w.mappingApplied.Load() >= read })
	before := len(treeNotices(t, w, root))
	deliver(t, w, client, issue(child, "acme/widgets", "todo", false, root, 1))
	if got := recordedIssue(t, w, child); got != nil {
		t.Fatalf("%s recorded as %+v while the project maps no repository", child, got)
	}
	notices := treeNotices(t, w, root)[before:]
	if len(notices) != 1 || !strings.Contains(notices[0], api.NoRepositoryMapped("LEGION")) ||
		!strings.Contains(notices[0], "Map the project's repositories in Dispatch (Settings → Repositories → Projects), one of them its default, then release it again") ||
		strings.Contains(notices[0], "dispatch issue-update") {
		t.Fatalf("notices on %s after %s = %q, want one naming the empty mapping and mapping a repository as the remedy", root, child, notices)
	}
}

// The boot's naming of the mapping comes to serve no repository when every repository it accepted
// is still refused for GitHub's rate limit after the boot waited the limit out: it logs the one
// serving-none line, its cause every repository left out and its error the first read that failed.
func TestABootWhoseEveryRepositoryStaysRateLimitedLogsTheServingNoneLine(t *testing.T) {
	github := newLimitedGitHub(t, limitedBoth, func(w http.ResponseWriter, _ string, _ int) bool {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
		return true
	})
	logs := &syncBuffer{}
	w, _, _, _ := mappingRigWith(t, &installations{}, limitedRig(t, github, logs), func(w *workflowRuntime) {
		w.recheckAfter = func(time.Duration) <-chan time.Time { return nil }
	})
	if served(w, "acme/widgets") || served(w, "zeta/tools") {
		t.Fatal("a repository is served while GitHub rate-limits both")
	}
	errorLines := loggedLines(t, logs.String(), `"level":"ERROR"`)
	if len(errorLines) != 1 || errorLines[0]["msg"] != servingNoneLine || errorLines[0]["cause"] != noRepositoryServed("LEGION") || errorLines[0]["error"] == nil {
		t.Fatalf("ERROR lines = %v, want one serving-none line, every repository left out, naming the failed read", errorLines)
	}
}
