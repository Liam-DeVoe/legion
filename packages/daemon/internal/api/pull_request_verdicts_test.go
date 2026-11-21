package api

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// The heads of acme/widgets' pull request 7, a pull request no Legion issue owns: sideHead, where
// the controller names its reviewer, and movedHead, where a push then moves it.
const (
	sideHead  = "51de000000000000000000000000000000000a01"
	movedHead = "51de000000000000000000000000000000000a02"
)

// sideGitHub is GitHub's REST API for acme/widgets as the side-review routes read it: each pull
// request GET /pulls/{number} answers, by number, which a test changes between calls. Every read
// must carry acme's read token (handoffTokens).
type sideGitHub struct {
	url   string
	mu    sync.Mutex
	pulls map[int]string
	// failures is the status GitHub answers a pull request's read with in place of the pull
	// request, by number: 429 carries a Retry-After of 30 seconds, as GitHub's rate limit does.
	failures map[int]int
}

func newSideGitHub(t *testing.T) *sideGitHub {
	t.Helper()
	g := &sideGitHub{pulls: map[int]string{}, failures: map[int]int{}}
	g.url = githubtest.Server(t, githubtest.Spec{Token: "read-token-for-acme", Handle: func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		// Any repository of acme answers its pull requests alike, so a call addressed under the name
		// a rename moved a repository to reaches the same pull request.
		_, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/repos/acme/"), "/")
		number, found := strings.CutPrefix(rest, "pulls/")
		var n int
		if _, err := fmt.Sscan(number, &n); r.Method != http.MethodGet || !found || err != nil {
			githubtest.Unexpected(t, w, r)
			return
		}
		if status, failing := g.failures[n]; failing {
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "30")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"refused"}`))
			return
		}
		pull, known := g.pulls[n]
		if !known {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(pull))
	}})
	return g
}

// refuse has GitHub answer pull request number's read with status.
func (g *sideGitHub) refuse(number, status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures[number] = status
}

// liveSession launches a claim of role on issue whose agent registered session and is in a turn:
// a running Legion session, which the controller can name.
func liveSession(t *testing.T, h *harness, issue string, role claim.Role, session string) {
	t.Helper()
	token, boot := h.launchIn(issue, issue, role)
	h.registered(boot, session)
	working(t, h, token, issue)
}

// serve has GitHub answer pull request number in state, on branch ref at head.
func (g *sideGitHub) serve(number int, state, ref, head string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pulls[number] = fmt.Sprintf(`{"number":%d,"state":%q,"base":{"ref":"main"},"head":{"ref":%q,"sha":%q,"repo":{"full_name":"acme/widgets"}}}`,
		number, state, ref, head)
}

// forget has GitHub answer pull request number 404, as for a number it does not have.
func (g *sideGitHub) forget(number int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pulls, number)
}

// sideRig is a daemon whose verdict repositories are acme/widgets, whose entry takes side reviews,
// and acme/gadgets, whose entry names a CI reviewer and takes none; with GitHub at github, the
// server's own log in the harness's serverLog, and each write of legion-verdict a route asked for
// (Options.WriteVerdict) in writes, as repository#number@head.
type sideRig struct {
	*harness
	github *sideGitHub
	mu     sync.Mutex
	writes []string
}

func newSideRig(t *testing.T) *sideRig {
	t.Helper()
	return newSideRigServing(t, func(recorded ghrepo.Repository) ghrepo.Repository { return recorded })
}

// newSideRigServing is newSideRig whose daemon finds the repository GitHub holds a named one under
// with serving (Options.Serving), and reads a verdict entry as the daemon's
// workflowRuntime.verdictEntry does: under the name it is given, then under serving's name for it.
func newSideRigServing(t *testing.T, serving func(ghrepo.Repository) ghrepo.Repository) *sideRig {
	t.Helper()
	rig := &sideRig{harness: newHarness(t), github: newSideGitHub(t)}
	// ses_s and ses_t are running reviewers of the project, whom the controller may name.
	liveSession(t, rig.harness, "LEGION-301", claim.RoleReviewer, "ses_s")
	liveSession(t, rig.harness, "LEGION-302", claim.RoleReviewer, "ses_t")
	entries := []config.VerdictRepository{
		{Repository: ghrepo.MustParse("acme/widgets"), SideReviews: true},
		{Repository: ghrepo.MustParse("acme/gadgets"), CIReviewer: "example-ci-review[bot]"},
	}
	entry := func(repository ghrepo.Repository) (config.VerdictRepository, bool) {
		at := slices.IndexFunc(entries, func(entry config.VerdictRepository) bool {
			return strings.EqualFold(entry.Repository.String(), repository.String())
		})
		if at < 0 {
			return config.VerdictRepository{}, false
		}
		return entries[at], true
	}
	rig.handler = NewServer("127.0.0.1", 8437, Options{
		Steps:      config.DesignGateRootIssues.Steps(),
		Supervisor: rig.supervisor, BootTokens: rig.tokens, Project: testProject, DispatchProject: testDispatchProject,
		OperatorToken: testOperatorToken, Controller: rig.store, Grants: credential.New(nil),
		Pool: rig.store.Pool(), Record: record.NewStore(), Tokens: handoffTokens{}, GitHubAPI: rig.github.url,
		Log: slog.New(slog.NewTextHandler(rig.serverLog, nil)), Serving: serving,
		VerdictEntry: func(repository ghrepo.Repository) (config.VerdictRepository, bool) {
			if found, ok := entry(repository); ok {
				return found, true
			}
			return entry(serving(repository))
		},
		WriteVerdict: func(repository string, number int, head string) {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			rig.writes = append(rig.writes, fmt.Sprintf("%s#%d@%s", repository, number, head))
		},
	}).Handler
	return rig
}

// A side review is taken only where the writer of legion-verdict writes it. The verdict is
// recorded under the name GitHub holds the repository under now, and the writer is asked under
// that name; the writer finds the entry under that name, or under the one GitHub serves it under
// (verdictEntry), and never under the name a rename moved it away from. So a call on acme/widgets,
// whose entry is written under that name, once GitHub holds it as acme/widgets-next, is refused
// before anything is recorded, rather than answered 200 with a verdict no check is ever written
// from. A call under the name a rename moved away from, acme/widgets-old, which GitHub holds as
// acme/widgets, is taken, recorded and written under acme/widgets.
func TestASideReviewIsTakenOnlyWhereTheWriterWritesIt(t *testing.T) {
	t.Run("an entry under the name a rename moved the repository away from", func(t *testing.T) {
		rig := newSideRigServing(t, func(recorded ghrepo.Repository) ghrepo.Repository {
			if strings.EqualFold(recorded.String(), "acme/widgets") {
				return ghrepo.MustParse("acme/widgets-next")
			}
			return recorded
		})
		rig.github.serve(7, "open", "fix/parser", sideHead)
		message := refused(t, rig.nameReviewer(PullRequestReviewerRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets",
			Number: 7, Head: sideHead, Session: "ses_s"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
		if !strings.Contains(message, "acme/widgets-next") {
			t.Fatalf("the refusal says %q, want it to name acme/widgets-next, the name the writer is asked under", message)
		}
		refused(t, rig.relayVerdict(PullRequestVerdictRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
			Head: sideHead, Verdict: "approve", Session: "ses_s"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
		if reviewers, verdicts := rig.sideRows(); len(reviewers) != 0 || len(verdicts) != 0 {
			t.Fatalf("the store records side reviewers %+v and verdicts %+v, want none", reviewers, verdicts)
		}
		if got := rig.written(); len(got) != 0 {
			t.Fatalf("writes of legion-verdict = %v, want none", got)
		}
	})
	t.Run("a call under the name a rename moved the repository away from", func(t *testing.T) {
		rig := newSideRigServing(t, func(recorded ghrepo.Repository) ghrepo.Repository {
			if strings.EqualFold(recorded.String(), "acme/widgets-old") {
				return ghrepo.MustParse("acme/widgets")
			}
			return recorded
		})
		rig.github.serve(7, "open", "fix/parser", sideHead)
		for _, recorder := range []*httptest.ResponseRecorder{
			rig.nameReviewer(PullRequestReviewerRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets-old", Number: 7,
				Head: sideHead, Session: "ses_s"}),
			rig.relayVerdict(PullRequestVerdictRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets-old", Number: 7,
				Head: sideHead, Verdict: "approve", Session: "ses_s"}),
		} {
			if recorder.Code != http.StatusOK {
				t.Fatalf("a call under acme/widgets-old = %d: %s", recorder.Code, recorder.Body)
			}
		}
		if got, want := rig.written(), []string{"acme/widgets#7@" + sideHead}; !slices.Equal(got, want) {
			t.Fatalf("writes of legion-verdict = %v, want %v", got, want)
		}
	})
}

// controllerGrant is a grant the project's controller session minted, ses_controller registered
// as `legion controller start` and the session's registration register it.
func (rig *sideRig) controllerGrant() string {
	rig.t.Helper()
	registration := rig.registeredController(rig.mintedSecret(), "ses_controller")
	recorder := rig.controllerSessionGrant("ses_controller", registration.Secret)
	if recorder.Code != http.StatusOK {
		rig.t.Fatalf("the controller session's grant = %d; body %s", recorder.Code, recorder.Body)
	}
	var grant GrantResponse
	decodeInto(rig.t, recorder, &grant)
	return grant.GrantID
}

func (rig *sideRig) nameReviewer(req PullRequestReviewerRequest) *httptest.ResponseRecorder {
	rig.t.Helper()
	return rig.request(http.MethodPost, "/legion/v1/pull-request-reviewer", req, nil)
}

func (rig *sideRig) relayVerdict(req PullRequestVerdictRequest) *httptest.ResponseRecorder {
	rig.t.Helper()
	return rig.request(http.MethodPost, "/legion/v1/pull-request-verdict", req, nil)
}

// written is each write of legion-verdict the routes asked for so far.
func (rig *sideRig) written() []string {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return slices.Clone(rig.writes)
}

// sideRows is every side reviewer and every verdict the store records, oldest first.
func (rig *sideRig) sideRows() (reviewers []store.SideReviewer, verdicts []store.Verdict) {
	rig.t.Helper()
	err := rig.store.Tx(context.Background(), func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `select repository, number, head, session from side_reviewers order by id`)
		if err != nil {
			return err
		}
		if reviewers, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.SideReviewer, error) {
			var r store.SideReviewer
			var repository string
			err := row.Scan(&repository, &r.Number, &r.Head, &r.Session)
			r.Repository = ghrepo.MustParse(repository)
			return r, err
		}); err != nil {
			return err
		}
		rows, err = tx.Query(context.Background(), `select repository, number, head, issue, verdict, session from verdicts order by id`)
		if err != nil {
			return err
		}
		verdicts, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Verdict, error) {
			var v store.Verdict
			var repository, word string
			err := row.Scan(&repository, &v.Number, &v.Head, &v.Issue, &word, &v.Session)
			v.Repository, v.Verdict = ghrepo.MustParse(repository), store.VerdictWord(word)
			return v, err
		})
		return err
	})
	if err != nil {
		rig.t.Fatalf("read the side reviewers and the verdicts: %v", err)
	}
	return reviewers, verdicts
}

// recordedLines is each line the server logged for an accepted pull request verdict.
func (rig *sideRig) recordedLines() []string {
	return rig.serverLog.lines("api: recorded a pull request verdict")
}

// C30. The controller names session S the reviewer of pull request 7 at its current head; then
// the verdict it relays from S at that head is taken, recorded as a verdict of no issue, logged,
// and asks the writer for legion-verdict on that head. The same verdict relayed from session T,
// which the controller did not name, is refused VERDICT_REVIEWER_NOT_NAMED: it records nothing,
// asks for no write and logs no such line. Once a push moves the pull request to another head,
// S's verdict at the new head, where nobody named S, is refused the same way, and its verdict at
// the old head, which is no longer current, is refused VERDICT_CALLER_FORBIDDEN. A naming from a
// reviewer's claim, not the controller's, is refused VERDICT_CALLER_FORBIDDEN.
func TestPullRequestVerdictTakesOnlyTheNamedReviewer(t *testing.T) {
	rig := newSideRig(t)
	rig.github.serve(7, "open", "fix/parser", sideHead)
	widgets := ghrepo.MustParse("acme/widgets")

	reviewer := newLiveClaim(t, rig.harness, "LEGION-208", claim.RoleReviewer)
	refused(t, rig.nameReviewer(PullRequestReviewerRequest{GrantID: reviewer.grant(t), Repository: "acme/widgets", Number: 7,
		Head: sideHead, Session: "ses_s"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
	if reviewers, _ := rig.sideRows(); len(reviewers) != 0 {
		t.Fatalf("side reviewers after a reviewer claim's naming = %+v, want none", reviewers)
	}

	if recorder := rig.nameReviewer(PullRequestReviewerRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
		Head: sideHead, Session: "ses_s"}); recorder.Code != http.StatusOK {
		t.Fatalf("the controller's naming of ses_s = %d: %s", recorder.Code, recorder.Body)
	}
	if reviewers, _ := rig.sideRows(); len(reviewers) != 1 ||
		reviewers[0] != (store.SideReviewer{Repository: widgets, Number: 7, Head: sideHead, Session: "ses_s"}) {
		t.Fatalf("side reviewers after the naming = %+v, want ses_s at %s alone", reviewers, sideHead)
	}

	message := refused(t, rig.relayVerdict(PullRequestVerdictRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
		Head: sideHead, Verdict: "approve", Session: "ses_t"}), http.StatusForbidden, "VERDICT_REVIEWER_NOT_NAMED")
	for _, want := range []string{"ses_t", "ses_s", "nothing was recorded"} {
		if !strings.Contains(message, want) {
			t.Fatalf("the refusal of ses_t's verdict says %q, want it to say %q", message, want)
		}
	}
	if _, verdicts := rig.sideRows(); len(verdicts) != 0 {
		t.Fatalf("verdicts after ses_t's = %+v, want none", verdicts)
	}
	if got := rig.written(); len(got) != 0 {
		t.Fatalf("writes of legion-verdict after ses_t's verdict = %v, want none", got)
	}
	if got := rig.recordedLines(); len(got) != 0 {
		t.Fatalf("recorded-verdict lines after ses_t's verdict = %q, want none", got)
	}

	if recorder := rig.relayVerdict(PullRequestVerdictRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
		Head: sideHead, Verdict: "approve", Session: "ses_s"}); recorder.Code != http.StatusOK {
		t.Fatalf("ses_s's verdict = %d: %s", recorder.Code, recorder.Body)
	}
	if _, verdicts := rig.sideRows(); len(verdicts) != 1 || verdicts[0] != (store.Verdict{Repository: widgets, Number: 7, Head: sideHead,
		Issue: "", Verdict: store.VerdictApprove, Session: "ses_s"}) {
		t.Fatalf("verdicts after ses_s's = %+v, want its approve at %s, of no issue, alone", verdicts, sideHead)
	}
	if got, want := rig.written(), []string{"acme/widgets#7@" + sideHead}; !slices.Equal(got, want) {
		t.Fatalf("writes of legion-verdict after ses_s's verdict = %v, want %v", got, want)
	}
	lines := rig.recordedLines()
	if len(lines) != 1 {
		t.Fatalf("recorded-verdict lines = %q, want one", lines)
	}
	for _, want := range []string{"repository=acme/widgets", "number=7", "head=" + sideHead, "verdict=approve", "reviewer_session=ses_s"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("the recorded-verdict line %q does not hold %q", lines[0], want)
		}
	}

	rig.github.serve(7, "open", "fix/parser", movedHead)
	refused(t, rig.relayVerdict(PullRequestVerdictRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
		Head: movedHead, Verdict: "approve", Session: "ses_s"}), http.StatusForbidden, "VERDICT_REVIEWER_NOT_NAMED")
	message = refused(t, rig.relayVerdict(PullRequestVerdictRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
		Head: sideHead, Verdict: "approve", Session: "ses_s"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
	for _, want := range []string{sideHead, movedHead, "nothing was recorded"} {
		if !strings.Contains(message, want) {
			t.Fatalf("the refusal of a verdict at the old head says %q, want it to say %q", message, want)
		}
	}
	if _, verdicts := rig.sideRows(); len(verdicts) != 1 {
		t.Fatalf("verdicts after the refusals at the moved head = %+v, want ses_s's first alone", verdicts)
	}
	if got := rig.written(); len(got) != 1 {
		t.Fatalf("writes of legion-verdict after the refusals at the moved head = %v, want the first alone", got)
	}
	if got := rig.recordedLines(); len(got) != 1 {
		t.Fatalf("recorded-verdict lines after the refusals at the moved head = %q, want the first alone", got)
	}

	// A controller grant the operator's bearer minted is no grant of the project's controller
	// claim: both calls are refused, and nothing more is recorded, written or logged.
	t.Run("a controller grant the operator's bearer minted", func(t *testing.T) {
		rig.github.serve(7, "open", "fix/parser", sideHead)
		refused(t, rig.nameReviewer(PullRequestReviewerRequest{GrantID: controllerGrant(t, rig.harness).GrantID, Repository: "acme/widgets",
			Number: 7, Head: sideHead, Session: "ses_t"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
		refused(t, rig.relayVerdict(PullRequestVerdictRequest{GrantID: controllerGrant(t, rig.harness).GrantID, Repository: "acme/widgets",
			Number: 7, Head: sideHead, Verdict: "approve", Session: "ses_s"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
		if reviewers, verdicts := rig.sideRows(); len(reviewers) != 1 || len(verdicts) != 1 {
			t.Fatalf("after the operator's calls the store records side reviewers %+v and verdicts %+v, want the first of each alone", reviewers, verdicts)
		}
		if got := rig.written(); len(got) != 1 {
			t.Fatalf("writes of legion-verdict after the operator's calls = %v, want the first alone", got)
		}
		if got := rig.recordedLines(); len(got) != 1 {
			t.Fatalf("recorded-verdict lines after the operator's calls = %q, want the first alone", got)
		}
	})
}

// The two routes take a call only for an open pull request no Legion issue owns, at its current
// head, on a verdict repository whose entry takes side reviews, from the project's controller's
// own claim; and the verdict route a reviewer's word alone. Every other call is refused, and
// records nothing, asks for no write and logs no recorded verdict.
func TestSideReviewCallsRefuseWhatNoSideReviewDecides(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grant  func(rig *sideRig) string
		pull   func(rig *sideRig)
		repo   string
		head   string
		word   string
		status int
		code   string
	}{
		{name: "a controller grant the operator's bearer minted", grant: func(rig *sideRig) string { return controllerGrant(rig.t, rig.harness).GrantID },
			code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "a repository whose entry names a CI reviewer and takes no side reviews", repo: "acme/gadgets", code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "a repository no verdict entry names", repo: "acme/tools", code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "a pull request on a Legion issue's branch", pull: func(rig *sideRig) { rig.github.serve(7, "open", "legion/ACME-12", sideHead) },
			code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "a pull request the record holds for a Legion issue", pull: func(rig *sideRig) {
			seedIssueAt(rig.t, rig.harness, "LEGION-208", phase.Reviewing)
			if err := rig.store.Tx(context.Background(), func(tx pgx.Tx) error {
				return record.NewStore().PutPullRequest(context.Background(), tx, record.PullRequest{State: record.PullRequestOpen,
					Issue: "LEGION-208", Repo: "acme/widgets", Number: 7, Branch: "legion/LEGION-208", HeadSHA: sideHead})
			}); err != nil {
				rig.t.Fatalf("seed LEGION-208's pull request: %v", err)
			}
		}, code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "a closed pull request", pull: func(rig *sideRig) { rig.github.serve(7, "closed", "fix/parser", sideHead) },
			code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "a pull request GitHub does not have", pull: func(rig *sideRig) { rig.github.forget(7) }, code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "a head that is not the pull request's current head", head: movedHead, code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "the tester's pass for a verdict", word: "pass", status: http.StatusBadRequest, code: "REVIEWER_VERDICT_REQUIRED"},
		{name: "a repository that is not owner/name", repo: "acme", status: http.StatusBadRequest, code: "INVALID_REPOSITORY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newSideRig(t)
			rig.github.serve(7, "open", "fix/parser", sideHead)
			// ses_s is named at the current head, so each refusal below is the case's own.
			if recorder := rig.nameReviewer(PullRequestReviewerRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets",
				Number: 7, Head: sideHead, Session: "ses_s"}); recorder.Code != http.StatusOK {
				t.Fatalf("the controller's naming of ses_s = %d: %s", recorder.Code, recorder.Body)
			}
			if tc.pull != nil {
				tc.pull(rig)
			}
			grant := rig.controllerGrant
			if tc.grant != nil {
				grant = func() string { return tc.grant(rig) }
			}
			repo, head, word := cmp.Or(tc.repo, "acme/widgets"), cmp.Or(tc.head, sideHead), cmp.Or(tc.word, "approve")
			status := cmp.Or(tc.status, http.StatusForbidden)
			refused(t, rig.relayVerdict(PullRequestVerdictRequest{GrantID: grant(), Repository: repo, Number: 7, Head: head,
				Verdict: word, Session: "ses_s"}), status, tc.code)
			if tc.word == "" {
				refused(t, rig.nameReviewer(PullRequestReviewerRequest{GrantID: grant(), Repository: repo, Number: 7, Head: head,
					Session: "ses_u"}), status, tc.code)
			}
			if reviewers, verdicts := rig.sideRows(); len(reviewers) != 1 || len(verdicts) != 0 {
				t.Fatalf("after the refusals the store records side reviewers %+v and verdicts %+v, want the first naming alone and no verdict",
					reviewers, verdicts)
			}
			if got := rig.written(); len(got) != 0 {
				t.Fatalf("writes of legion-verdict after the refusals = %v, want none", got)
			}
			if got := rig.recordedLines(); len(got) != 0 {
				t.Fatalf("recorded-verdict lines after the refusals = %q, want none", got)
			}
		})
	}
}

// The controller names only a running Legion reviewer: a session no claim of the project holds,
// and a running implementer's session, are refused VERDICT_REVIEWER_NOT_LIVE, naming the session,
// and nothing is recorded.
func TestASideReviewerIsARunningReviewerClaim(t *testing.T) {
	rig := newSideRig(t)
	rig.github.serve(7, "open", "fix/parser", sideHead)
	liveSession(t, rig.harness, "LEGION-303", claim.RoleImplementer, "ses_implementer")
	for _, session := range []string{"ses_nobody", "ses_implementer"} {
		message := refused(t, rig.nameReviewer(PullRequestReviewerRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets",
			Number: 7, Head: sideHead, Session: session}), http.StatusConflict, "VERDICT_REVIEWER_NOT_LIVE")
		if !strings.Contains(message, session) || !strings.Contains(message, "nothing was recorded") {
			t.Fatalf("the refusal of %s says %q, want it to name the session and say nothing was recorded", session, message)
		}
	}
	if reviewers, _ := rig.sideRows(); len(reviewers) != 0 {
		t.Fatalf("side reviewers after the refusals = %+v, want none", reviewers)
	}
}

// A new naming of another session at the same head withdraws the reviewer named before: its later
// verdict is refused VERDICT_REVIEWER_NOT_NAMED, and the naming asks the writer again, so the
// verdict it already recorded is decided anew. A first naming, and a naming of the session already
// named, ask the writer for nothing.
func TestANewSideReviewerWithdrawsTheEarlierOne(t *testing.T) {
	rig := newSideRig(t)
	rig.github.serve(7, "open", "fix/parser", sideHead)
	name := func(session string) {
		t.Helper()
		if recorder := rig.nameReviewer(PullRequestReviewerRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
			Head: sideHead, Session: session}); recorder.Code != http.StatusOK {
			t.Fatalf("the controller's naming of %s = %d: %s", session, recorder.Code, recorder.Body)
		}
	}
	verdict := func(session string) *httptest.ResponseRecorder {
		return rig.relayVerdict(PullRequestVerdictRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
			Head: sideHead, Verdict: "approve", Session: session})
	}
	write := "acme/widgets#7@" + sideHead
	name("ses_s")
	if got := rig.written(); len(got) != 0 {
		t.Fatalf("writes after the first naming = %v, want none", got)
	}
	if recorder := verdict("ses_s"); recorder.Code != http.StatusOK {
		t.Fatalf("ses_s's verdict = %d: %s", recorder.Code, recorder.Body)
	}
	name("ses_t")
	if got, want := rig.written(), []string{write, write}; !slices.Equal(got, want) {
		t.Fatalf("writes after ses_t is named = %v, want %v: the naming asks the writer to decide the head again", got, want)
	}
	refused(t, verdict("ses_s"), http.StatusForbidden, "VERDICT_REVIEWER_NOT_NAMED")
	name("ses_t")
	if got := rig.written(); len(got) != 2 {
		t.Fatalf("writes after ses_t is named again = %v, want no new one", got)
	}
}

// The route's read of the pull request names what GitHub answered: a refusal (403) is a 502 that a
// retry does not mend, GitHub's rate limit a 503 carrying its Retry-After, GitHub's own failure a
// 502 to retry, and a 404, which GitHub also answers where the App cannot read the repository, is
// refused VERDICT_CALLER_FORBIDDEN and logged. Each records nothing.
func TestASideReviewNamesWhatGitHubAnswered(t *testing.T) {
	for _, tc := range []struct {
		name, code, retryAfter string
		answered, status       int
	}{
		{name: "a refused read", answered: http.StatusForbidden, status: http.StatusBadGateway, code: "GITHUB_READ_REFUSED"},
		{name: "the rate limit", answered: http.StatusTooManyRequests, status: http.StatusServiceUnavailable, code: "GITHUB_RATE_LIMITED", retryAfter: "30"},
		{name: "GitHub's own failure", answered: http.StatusInternalServerError, status: http.StatusBadGateway, code: "PULL_REQUEST_READ_FAILED"},
		{name: "no pull request the App can read", answered: http.StatusNotFound, status: http.StatusForbidden, code: "VERDICT_CALLER_FORBIDDEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newSideRig(t)
			rig.github.refuse(7, tc.answered)
			recorder := rig.nameReviewer(PullRequestReviewerRequest{GrantID: rig.controllerGrant(), Repository: "acme/widgets", Number: 7,
				Head: sideHead, Session: "ses_s"})
			refused(t, recorder, tc.status, tc.code)
			if got := recorder.Header().Get("Retry-After"); got != tc.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retryAfter)
			}
			if got := rig.serverLog.lines("api: GitHub answered a side review's read of its pull request", "repository=acme/widgets"); len(got) != 1 {
				t.Fatalf("lines logging GitHub's answer = %q, want one", got)
			}
			if reviewers, _ := rig.sideRows(); len(reviewers) != 0 {
				t.Fatalf("side reviewers after the refusal = %+v, want none", reviewers)
			}
		})
	}
}

// The controller's two side-review bodies as Go writes them, and the routes' answers. contracts'
// legion-api.test.ts parses each under the schema that mirrors it (LegionPullRequestReviewerRequest,
// LegionPullRequestVerdictRequest, LegionEmptyResponse), and the request schemas are strict, so a
// name Go writes and the contract does not read fails there.
func TestSideReviewBodiesGolden(t *testing.T) {
	golden(t, "pull-request-reviewer-request.json", PullRequestReviewerRequest{GrantID: "grant-for-one-command",
		Repository: "acme/widgets", Number: 7, Head: sideHead, Session: "ses_reviewer"})
	golden(t, "pull-request-reviewer.json", EmptyResponse{})
	golden(t, "pull-request-verdict-request.json", PullRequestVerdictRequest{GrantID: "grant-for-one-command",
		Repository: "acme/widgets", Number: 7, Head: sideHead, Verdict: "approve", Session: "ses_reviewer"})
	golden(t, "pull-request-verdict.json", EmptyResponse{})
}
