package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testlog"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// countingPullStandIn is GitHub's REST API for acme/widgets answering pull request 86 with the
// merge state named, and counting every read of it.
func countingPullStandIn(t *testing.T, state string, reads *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/pulls/86" {
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		w.Write([]byte(`{"number":86,"state":"open","base":{"ref":"main"},"head":{"ref":"legion/CAPTURE-1","sha":"code"},"mergeable":true,"mergeable_state":"` + state + `"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// clearCaptureMergeState puts CAPTURE-1's pull request back to a head GitHub's merge state has
// never been read for, which is where a pull request whose head has just moved stands.
func clearCaptureMergeState(t *testing.T, pool *store.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "update pull_requests set merge_state = '' where issue = 'CAPTURE-1'"); err != nil {
		t.Fatalf("clear the recorded merge state: %v", err)
	}
}

// approvedCapture seeds CAPTURE-1 in reviewing with its reviewer's approval standing on the head
// its pull request is at and no merge state read yet: an approval waiting on GitHub to report the
// head one the issue may advance on (workflow's reviewRound).
func approvedCapture(t *testing.T, pool *store.Pool) {
	t.Helper()
	seedCapture(t, pool, phase.Reviewing, "needs_review",
		record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleReviewer, Claim: "review-claim",
			Decision: &record.ReviewDecision{State: "approved", Head: "code"}})
	clearCaptureMergeState(t, pool)
}

// settledChecksOnCode is the CI settlement for the head CAPTURE-1's pull request stands at: one of
// the facts after which the daemon reads GitHub's merge state when an approval stands.
func settledChecksOnCode() intake.PullRequestChecks {
	return intake.PullRequestChecks{Repo: "acme/widgets", Number: 86, HeadSHA: "code", Generation: 1, Snapshot: "settled-again",
		CheckRuns: []record.AttemptRun{{Name: "pr-checks-result", ID: 3}}, Failing: []string{}, Cancelled: []string{}}
}

// GitHub computes a pull request's merge state lazily: the first read of a head it has not looked
// at answers `unknown` and asks it to compute one. The daemon reads again half a minute later
// while that is the answer, five times in all, and then stops: nothing is queued behind the last
// read, no row waits in the outbox and no pass comes round again. The record is left showing the
// approval standing on a head GitHub has not reported mergeable, which is what the issue's
// architect reads.
func TestTheMergeStateLadderReadsFiveTimesAndThenStops(t *testing.T) {
	pool := isolatedOutboxPool(t)
	approvedCapture(t, pool)
	var reads atomic.Int32
	w := readRuntime(pool, countingPullStandIn(t, "unknown", &reads).URL, quietLogger())
	w.mergeStateRetry = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.readMergeStates(ctx)

	if _, err := intake.ApplyFact(ctx, pool, "github", "checks-on-code", settledChecksOnCode(), w.handlers...); err != nil {
		t.Fatalf("apply the CI settlement: %v", err)
	}
	testwait.Eventually(t, "the ladder read GitHub five times", func() bool { return reads.Load() >= mergeStateLadderReads })

	// Past the fifth `unknown` nothing reads again, however long the ladder's wait is given.
	time.Sleep(20 * w.mergeStateRetry)
	if got := reads.Load(); got != mergeStateLadderReads {
		t.Fatalf("GitHub was read %d times, want exactly %d: the ladder stops after the fifth unknown", got, mergeStateLadderReads)
	}
	w.mergeStateMu.Lock()
	running := len(w.mergeStateRunning)
	w.mergeStateMu.Unlock()
	wanted := w.mergeStates.waiting()
	if wanted != 0 || running != 0 {
		t.Fatalf("%d issues are queued for a read and %d ladders are running, want none standing behind the last read", wanted, running)
	}
	got, pr := capturePhase(t, pool)
	if got != phase.Reviewing || pr.MergeState != record.MergeStateUnknown {
		t.Fatalf("the issue is in %s with merge state %q, want it left in reviewing with GitHub's unknown recorded", got, pr.MergeState)
	}
}

// The read the ladder makes is the one that moves an approved issue on: GitHub answers the state
// clean, the daemon applies it as the fact a webhook would have carried, and the round the
// approval left waiting ends in accepting. One answer is enough, so the ladder reads once.
func TestTheMergeStateReadMovesAnApprovedIssueToAccepting(t *testing.T) {
	pool := isolatedOutboxPool(t)
	approvedCapture(t, pool)
	var reads atomic.Int32
	w := readRuntime(pool, countingPullStandIn(t, "clean", &reads).URL, quietLogger())
	w.mergeStateRetry = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.readMergeStates(ctx)

	if _, err := intake.ApplyFact(ctx, pool, "github", "checks-on-code", settledChecksOnCode(), w.handlers...); err != nil {
		t.Fatalf("apply the CI settlement: %v", err)
	}
	testwait.Eventually(t, "the approval reached accepting", func() bool {
		got, _ := capturePhase(t, pool)
		return got == phase.Accepting
	})
	if got := reads.Load(); got != 1 {
		t.Fatalf("GitHub was read %d times, want one: its first answer was a state the issue may advance on", got)
	}
}

// Nothing is read for an issue no merge state would move: the reviewer has decided nothing, so the
// same settlement leaves the daemon owing GitHub no read at all.
func TestNoMergeStateIsReadWithoutAnApprovalStanding(t *testing.T) {
	pool := isolatedOutboxPool(t)
	seedCapture(t, pool, phase.Reviewing, "needs_review",
		record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleReviewer, Claim: "review-claim"})
	var reads atomic.Int32
	w := readRuntime(pool, countingPullStandIn(t, "clean", &reads).URL, quietLogger())
	w.mergeStateRetry = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.readMergeStates(ctx)

	if _, err := intake.ApplyFact(ctx, pool, "github", "checks-on-code", settledChecksOnCode(), w.handlers...); err != nil {
		t.Fatalf("apply the CI settlement: %v", err)
	}
	time.Sleep(20 * w.mergeStateRetry)
	if got := reads.Load(); got != 0 {
		t.Fatalf("GitHub was read %d times, want none: no approval stands on the head", got)
	}
}

// A fact of an issue whose ladder is already reading asks for a read of its own, and dropping it
// loses the one read that would answer it: the fact - a check that concluded, a push, a review -
// is what changes GitHub's answer, and the running ladder can already be past its last read, as
// it is here, where its first answer is one it ends on. The running ladder climbs again for the
// request instead, and that climb is the read that moves the issue.
func TestAMergeStateRequestArrivingWhileTheLadderReadsIsNotLost(t *testing.T) {
	pool := isolatedOutboxPool(t)
	approvedCapture(t, pool)
	var reads atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/pulls/86" {
			http.NotFound(w, r)
			return
		}
		state := "clean"
		if reads.Add(1) == 1 {
			// The first read is held until the second request has been queued, so the request
			// lands while this ladder is reading rather than before or after it.
			state = "blocked"
			<-release
		}
		w.Write([]byte(`{"number":86,"state":"open","base":{"ref":"main"},"head":{"ref":"legion/CAPTURE-1","sha":"code"},"mergeable":true,"mergeable_state":"` + state + `"}`))
	}))
	t.Cleanup(server.Close)
	w := readRuntime(pool, server.URL, quietLogger())
	w.mergeStateRetry = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(release)
	go w.readMergeStates(ctx)

	if _, err := intake.ApplyFact(ctx, pool, "github", "checks-on-code", settledChecksOnCode(), w.handlers...); err != nil {
		t.Fatalf("apply the CI settlement: %v", err)
	}
	testwait.Eventually(t, "the ladder is reading GitHub", func() bool { return reads.Load() == 1 })

	// The next check of the head concludes while that read is in flight, and asks for a read of
	// its own (workflow's askMergeState).
	later := settledChecksOnCode()
	later.Generation, later.Snapshot = 2, "settled-later"
	later.CheckRuns = []record.AttemptRun{{Name: "pr-checks-result", ID: 4}}
	if _, err := intake.ApplyFact(ctx, pool, "github", "checks-on-code-again", later, w.handlers...); err != nil {
		t.Fatalf("apply the second CI settlement: %v", err)
	}
	testwait.Eventually(t, "the request reached the ladder that is running", func() bool {
		w.mergeStateMu.Lock()
		defer w.mergeStateMu.Unlock()
		return w.mergeStateRunning["CAPTURE-1"]
	})
	release <- struct{}{}

	testwait.Eventually(t, "the approval reached accepting", func() bool {
		got, _ := capturePhase(t, pool)
		return got == phase.Accepting
	})
	if got := reads.Load(); got != 2 {
		t.Fatalf("GitHub was read %d times, want two: the first answer ended one climb, and the request that arrived during it asked for another", got)
	}
	w.mergeStateMu.Lock()
	running := len(w.mergeStateRunning)
	w.mergeStateMu.Unlock()
	if running != 0 {
		t.Fatalf("%d ladders are still marked as running, want none once the climbs are over", running)
	}
}

// mergerMoveGitHub is GitHub's REST API for acme/widgets answering pull request 86 open at head
// code on base main with GitHub's `mergeable` as the literal JSON mergeable and `mergeable_state`
// as state, or with status when status is not 200, and counting every read of it in reads.
func mergerMoveGitHub(t *testing.T, status int, mergeable, state string, reads *atomic.Int32) string {
	t.Helper()
	return githubtest.Server(t, githubtest.Spec{Token: "outbox-token", Handle: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/pulls/86" {
			githubtest.Unexpected(t, w, r)
			return
		}
		reads.Add(1)
		if status != http.StatusOK {
			http.Error(w, `{"message":"Server Error"}`, status)
			return
		}
		w.Write([]byte(`{"number":86,"state":"open","base":{"ref":"main"},"head":{"ref":"legion/CAPTURE-1","sha":"code"},"mergeable":` +
			mergeable + `,"mergeable_state":"` + state + `"}`))
	}})
}

// mergerInMerging seeds CAPTURE-1 in merging with its merger's row, its pull request open at head
// code and last read mergeable, no conflict recorded: where an issue stands when the merge queue
// has taken its pull request out and the daemon's read at the removal recorded nothing.
func mergerInMerging(t *testing.T, pool *store.Pool) {
	t.Helper()
	seedCapture(t, pool, phase.Merging, "retro",
		record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleMerger, Claim: "merge-claim", HandoffCommit: "code", Summary: "READY #86 at code"})
}

// mergerMovesBack applies the merger's move of CAPTURE-1 back to implementing carrying read, as
// the API's route applies it (phaseBackward), failing the test on a refusal.
func mergerMovesBack(t *testing.T, w *workflowRuntime, read *intake.PullRequestMergeability) {
	t.Helper()
	applyFact(t, w.pool, "api", "api:phase/backward:"+t.Name(),
		intake.BackwardMove{Issue: "CAPTURE-1", Requester: claim.RoleMerger, To: phase.Implementing,
			Reason: "GitHub shows the head dirty after the merge queue removed it", Mergeability: read}, w.handlers...)
}

// The merger moves its issue back out of merging when GitHub shows the head dirty after the merge
// queue took the pull request out, and the record then holds no conflict, since the daemon's read
// at the removal records none (mergeQueueRead). So the daemon reads GitHub once for the move
// (mergerMoveRead), the move carries the answer, and the workflow records it before it classifies
// the move: GitHub's CONFLICTING for the recorded head makes the move a conflict round, which
// raises no pair. A read GitHub does not answer is logged and leaves the move applied all the
// same, classified on the record as it stands, so it is counted under merging-implementing.
func TestTheMergersMoveBackCarriesGitHubsAnswerForTheHead(t *testing.T) {
	t.Run("GitHub answers the head conflicting", func(t *testing.T) {
		pool := isolatedOutboxPool(t)
		mergerInMerging(t, pool)
		var reads atomic.Int32
		w := readRuntime(pool, mergerMoveGitHub(t, http.StatusOK, "false", "dirty", &reads), quietLogger())

		opened := time.Now().UTC()
		read := w.mergerMoveRead(context.Background(), "CAPTURE-1")
		if got := reads.Load(); got != 1 {
			t.Fatalf("GitHub was read %d times, want once", got)
		}
		if read == nil {
			t.Fatal("the read answered nothing, want GitHub's answer for the head")
		}
		if read.Repo != "acme/widgets" || read.Number != 86 || read.Base != "main" || read.HeadSHA != "code" ||
			read.Mergeable != record.MergeabilityConflicting || read.MergeState != record.MergeStateDirty ||
			read.ReadAt.Before(opened) || read.ReadAt.After(time.Now().UTC()) {
			t.Fatalf("the read answered %+v, want pull request acme/widgets#86 at head code on main read CONFLICTING and dirty at a moment of the read itself", *read)
		}

		mergerMovesBack(t, w, read)
		if row := rolePhaseRow(t, pool, "CAPTURE-1", claim.RoleImplementer); row.Pass != 1 || len(row.BackwardMoves) != 0 {
			t.Fatalf("the implementer's row counts %d passes and the moves back %v, want the one pass and no pair: the move is a conflict round", row.Pass, row.BackwardMoves)
		}
		got, pr := capturePhase(t, pool)
		if got != phase.Implementing || pr.ConflictingHead != "code" || pr.Mergeability != record.MergeabilityConflicting {
			t.Fatalf("after the move the issue is in %s with %q held conflicting and mergeability %q, want implementing with head code held CONFLICTING",
				got, pr.ConflictingHead, pr.Mergeability)
		}
	})

	t.Run("GitHub does not answer", func(t *testing.T) {
		pool := isolatedOutboxPool(t)
		mergerInMerging(t, pool)
		var reads atomic.Int32
		var logged testlog.Buffer
		w := readRuntime(pool, mergerMoveGitHub(t, http.StatusBadGateway, "", "", &reads), slog.New(slog.NewTextHandler(&logged, nil)))

		read := w.mergerMoveRead(context.Background(), "CAPTURE-1")
		if got := reads.Load(); got != 1 || read != nil {
			t.Fatalf("GitHub was read %d times and the read answered %+v, want one read and no answer", got, read)
		}
		if line := "read whether the head an issue's merger moves back conflicts with its base; the move is applied unread"; !strings.Contains(logged.String(), line) {
			t.Fatalf("the log reads %q, want the failed read logged as %q", logged.String(), line)
		}

		mergerMovesBack(t, w, read)
		got, pr := capturePhase(t, pool)
		if got != phase.Implementing || pr.ConflictingHead != "" {
			t.Fatalf("after the move the issue is in %s with %q held conflicting, want implementing with no head held conflicting", got, pr.ConflictingHead)
		}
		if row := rolePhaseRow(t, pool, "CAPTURE-1", claim.RoleImplementer); row.Pass != 1 || row.BackwardMoves["merging-implementing"] != 1 || len(row.BackwardMoves) != 1 {
			t.Fatalf("the implementer's row counts %d passes and the moves back %v, want the one pass and the move counted under merging-implementing", row.Pass, row.BackwardMoves)
		}
	})
}

// GitHub is read for the merger's move only where its answer can change what the move is. A record
// that already holds the pull request's own head conflicting makes the move a conflict round
// without a read (classify.ConflictRound); an issue that does not stand in merging is no move out
// of merging, which the workflow refuses from the merger in any case; and an issue whose record
// holds no open pull request has no head to read. None of them reads GitHub, and each answers
// nothing, so the move is classified on the record as it stands.
func TestTheMergersMoveBackReadsGitHubOnlyWhereTheAnswerCountsForTheMove(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     phase.Phase
		status string
		// update is what the case writes over the pull request the seed records.
		update string
	}{
		{name: "the record already holds the head conflicting", at: phase.Merging, status: "retro",
			update: "update pull_requests set mergeability = 'CONFLICTING', conflicting_head = 'code' where issue = 'CAPTURE-1'"},
		{name: "the issue stands in reviewing", at: phase.Reviewing, status: "needs_review"},
		{name: "the pull request is closed", at: phase.Merging, status: "retro",
			update: "update pull_requests set state = 'closed' where issue = 'CAPTURE-1'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := isolatedOutboxPool(t)
			seedCapture(t, pool, tc.at, tc.status,
				record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleMerger, Claim: "merge-claim", HandoffCommit: "code", Summary: "READY #86 at code"})
			if tc.update != "" {
				if _, err := pool.Exec(context.Background(), tc.update); err != nil {
					t.Fatalf("write the pull request the case reads: %v", err)
				}
			}
			var reads atomic.Int32
			w := readRuntime(pool, mergerMoveGitHub(t, http.StatusOK, "false", "dirty", &reads), quietLogger())
			if read := w.mergerMoveRead(context.Background(), "CAPTURE-1"); read != nil || reads.Load() != 0 {
				t.Fatalf("the read answered %+v after %d reads of GitHub, want nothing read and no answer", read, reads.Load())
			}
		})
	}
}
