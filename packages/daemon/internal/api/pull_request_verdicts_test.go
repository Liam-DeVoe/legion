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
}

func newSideGitHub(t *testing.T) *sideGitHub {
	t.Helper()
	g := &sideGitHub{pulls: map[int]string{}}
	g.url = githubtest.Server(t, githubtest.Spec{Token: "read-token-for-acme", Handle: func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		number, found := strings.CutPrefix(r.URL.Path, "/repos/acme/widgets/pulls/")
		var n int
		if _, err := fmt.Sscan(number, &n); r.Method != http.MethodGet || !found || err != nil {
			githubtest.Unexpected(t, w, r)
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
	rig := &sideRig{harness: newHarness(t), github: newSideGitHub(t)}
	entries := []config.VerdictRepository{
		{Repository: ghrepo.MustParse("acme/widgets"), SideReviews: true},
		{Repository: ghrepo.MustParse("acme/gadgets"), CIReviewer: "example-ci-review[bot]"},
	}
	rig.handler = NewServer("127.0.0.1", 8437, Options{
		Steps:      config.DesignGateRootIssues.Steps(),
		Supervisor: rig.supervisor, BootTokens: rig.tokens, Project: testProject, DispatchProject: testDispatchProject,
		OperatorToken: testOperatorToken, Controller: rig.store, Grants: credential.New(nil),
		Pool: rig.store.Pool(), Record: record.NewStore(), Tokens: handoffTokens{}, GitHubAPI: rig.github.url,
		Log: slog.New(slog.NewTextHandler(rig.serverLog, nil)),
		VerdictEntry: func(repository ghrepo.Repository) (config.VerdictRepository, bool) {
			at := slices.IndexFunc(entries, func(entry config.VerdictRepository) bool {
				return strings.EqualFold(entry.Repository.String(), repository.String())
			})
			if at < 0 {
				return config.VerdictRepository{}, false
			}
			return entries[at], true
		},
		WriteVerdict: func(repository string, number int, head string) {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			rig.writes = append(rig.writes, fmt.Sprintf("%s#%d@%s", repository, number, head))
		},
	}).Handler
	return rig
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
