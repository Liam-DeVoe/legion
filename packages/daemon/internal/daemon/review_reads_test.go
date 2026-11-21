package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// reviewReadApp is the one App's bot login in these tests.
const reviewReadApp = "legion-reviewer[bot]"

// listedReview is one review as GitHub's GET /pulls/{number}/reviews lists it: submitted reviews
// carry submitted_at, a pending one (a draft) carries none.
func listedReview(id int64, login, state, commit, submitted string) string {
	when := ""
	if submitted != "" {
		when = fmt.Sprintf(`,"submitted_at":%q`, submitted)
	}
	return fmt.Sprintf(`{"id":%d,"user":{"login":%q},"body":"review %d","state":%q,"commit_id":%q%s}`, id, login, id, state, commit, when)
}

// captureHead is a head CAPTURE-1's pull request is moved to, and pushedHead the head a push
// takes the branch to, which a force push can take it back from.
const (
	captureHead = "1b9f4c2d8e7a6053f41c2b9d8e7a60534f1c2b9d"
	pushedHead  = "7d3e1a5c9b2f48607e3d1a5c9b2f48607e3d1a5c"
)

// captureHeadAt moves CAPTURE-1's pull request to head, the head its checks settled on with it.
func captureHeadAt(t *testing.T, pool *store.Pool, head string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "update pull_requests set head_sha = $1, checked_head = $1 where issue = 'CAPTURE-1'", head); err != nil {
		t.Fatalf("move CAPTURE-1's head to %s: %v", head, err)
	}
}

// reviewsStandIn is GitHub's REST API for acme/widgets as the per-issue read reads it: pull
// request 86 on base main, whose rulesets require pr-checks-result and whose branch protection
// requires nothing, whose reviews are what reviews answers when asked (a JSON array), each read
// counted in reads, and on which a-writer, a person, has write access and an-outsider read access.
func reviewsStandIn(t *testing.T, reviews func() string, reads *atomic.Int64) *httptest.Server {
	t.Helper()
	return reviewsStandInWith(t, reviews, reads, func() string { return "read" }, &atomic.Int64{})
}

// reviewsStandInWith is reviewsStandIn on which an-outsider's permission is what outsider answers
// when asked, each read of it counted in outsiderReads.
func reviewsStandInWith(t *testing.T, reviews func() string, reads *atomic.Int64, outsider func() string, outsiderReads *atomic.Int64) *httptest.Server {
	t.Helper()
	return githubtest.Serve(t, githubtest.Spec{Handle: func(w http.ResponseWriter, r *http.Request) {
		if !outboxAuthorized(w, r) {
			return
		}
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/86":
			w.Write([]byte(`{"number":86,"base":{"ref":"main"},"mergeable":true,"mergeable_state":"clean"}`))
		case "/repos/acme/widgets/pulls/86/reviews":
			reads.Add(1)
			w.Write([]byte(reviews()))
		case "/repos/acme/widgets/rules/branches/main":
			w.Write([]byte(`[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"pr-checks-result"}]}}]`))
		case "/repos/acme/widgets/branches/main":
			w.Write([]byte(`{"name":"main","protected":false}`))
		case "/repos/acme/widgets/collaborators/a-writer/permission":
			w.Write([]byte(`{"permission":"write","user":{"login":"a-writer","type":"User"}}`))
		case "/repos/acme/widgets/collaborators/an-outsider/permission":
			outsiderReads.Add(1)
			w.Write([]byte(`{"permission":"` + outsider() + `","user":{"login":"an-outsider","type":"User"}}`))
		default:
			http.NotFound(w, r)
		}
	}})
}

// reviewReadRuntime is a workflow runtime over pool whose GitHub reads go to api, with the one
// App's login set on the engine and the runtime as a boot sets both.
func reviewReadRuntime(pool *store.Pool, api string) *workflowRuntime {
	engine := workflow.New(record.NewStore(), workflow.Config{Project: "CAPTURE", Repositories: testRepositories, Steps: config.DesignGateRootIssues.Steps(), ReviewRoundCap: 10, AppLogin: reviewReadApp}, quietLogger())
	return &workflowRuntime{
		pool: pool, records: projectRecords{Store: record.NewStore(), project: "CAPTURE"}, engine: engine, handlers: []intake.Handler{engine},
		tokens: outboxTokens{}, dispatchProject: "CAPTURE", bootID: "test-boot", log: quietLogger(), githubAPI: api, appLogin: reviewReadApp, repositories: servedAcme(),
	}
}

// captureReviewClaim is CAPTURE-1's reviewer claim as the daemon names it (claim.NewToken of the
// issue's project, key and role): the claim each of the reviewer's completions carries.
const captureReviewClaim claim.Token = "legion-capture-capture-1-reviewer"

// completedReviewer is CAPTURE-1's reviewer having completed its round naming handoff record
// handoff, with no verdict and no review recorded as deciding it.
func completedReviewer(handoff string) record.PhaseRow {
	return record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleReviewer, Claim: captureReviewClaim, HandoffRecord: handoff, HandoffVersion: 1, LastHandoff: handoff,
		Summary: "reviewed", CompletedAt: time.Date(2026, 10, 8, 22, 16, 6, 0, time.UTC)}
}

// reviewerDecision is the state of CAPTURE-1's recorded review decision, "" for none, and the
// implementer's count of returns to implementing.
func reviewerDecision(t *testing.T, pool *store.Pool) (string, int) {
	t.Helper()
	var state string
	var rounds int
	if err := pool.QueryRow(context.Background(), `select coalesce((select decision->>'state' from phases where issue = 'CAPTURE-1' and role = 'reviewer'), ''),
		(select rounds from phases where issue = 'CAPTURE-1' and role = 'implementer')`).Scan(&state, &rounds); err != nil {
		t.Fatalf("read CAPTURE-1's review decision: %v", err)
	}
	return state, rounds
}

// holdFromReviewing puts CAPTURE-1 in held, held from reviewing.
func holdFromReviewing(t *testing.T, pool *store.Pool) {
	t.Helper()
	records := record.NewStore()
	if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		issue, err := records.Issue(context.Background(), tx, "CAPTURE-1")
		if err != nil {
			return err
		}
		issue.Phase, issue.Hold = phase.Held, &record.Hold{From: phase.Reviewing}
		return records.PutIssue(context.Background(), tx, *issue)
	}); err != nil {
		t.Fatalf("hold CAPTURE-1 from reviewing: %v", err)
	}
}

// The per-issue read reads the pull request's reviews, and no review GitHub lists decides a round:
// the reviewer's verdict reaches the daemon with its completion, a review written with the one
// App's token decides nothing however it arrives, and the commit_id the list answers with is
// GitHub's own and moves with the pull request (workflow's RefusesRound, dispatch://INFRA-1557),
// so a person's listed review decides nothing either, whatever permission GitHub gives the
// account; a person's request for changes refuses the head only by its webhook. A comment, a
// dismissed review and a pending draft decide nothing, as they decide nothing by webhook. So the
// round the reviewer completed with no verdict stays in reviewing with nothing recorded, held or
// not.
func TestNoReviewGitHubListsDecidesARoundOnTheRead(t *testing.T) {
	const (
		early = "2026-10-08T22:15:40Z"
		late  = "2026-10-08T22:15:52Z"
	)
	for _, tc := range []struct {
		name    string
		held    bool
		reviews []string
	}{
		{name: "the App's approval of the green head", reviews: []string{listedReview(5463503611, reviewReadApp, "APPROVED", captureHead, early)}},
		{name: "the App's request for changes", reviews: []string{listedReview(5463503611, reviewReadApp, "CHANGES_REQUESTED", captureHead, early)}},
		{name: "a writer's approval", reviews: []string{listedReview(5463503611, "a-writer", "APPROVED", captureHead, early)}},
		{name: "a writer's request for changes", reviews: []string{listedReview(5463503611, "a-writer", "CHANGES_REQUESTED", captureHead, early)}},
		{name: "an outsider's approval", reviews: []string{listedReview(5463503611, "an-outsider", "APPROVED", captureHead, early)}},
		{name: "a comment, a dismissed approval and a draft", reviews: []string{
			listedReview(5463503609, reviewReadApp, "DISMISSED", captureHead, early),
			listedReview(5463503610, reviewReadApp, "COMMENTED", captureHead, late),
			listedReview(5463503611, reviewReadApp, "PENDING", captureHead, ""),
		}},
		{name: "held from reviewing", held: true, reviews: []string{listedReview(5463503611, "a-writer", "CHANGES_REQUESTED", captureHead, early)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := isolatedOutboxPool(t)
			seedCapture(t, pool, phase.Reviewing, "needs_review", completedReviewer("review-1"))
			captureHeadAt(t, pool, captureHead)
			want := phase.Reviewing
			if tc.held {
				holdFromReviewing(t, pool)
				want = phase.Held
			}
			var reads atomic.Int64
			api := reviewsStandIn(t, func() string { return "[" + strings.Join(tc.reviews, ",") + "]" }, &reads).URL
			readCapture(t, reviewReadRuntime(pool, api))
			gotPhase, pr := capturePhase(t, pool)
			if gotPhase != want || pr.ReviewSeen.ID != 0 {
				t.Fatalf("after the read the issue is in %s with %d as its newest deciding review, want %s and none", gotPhase, pr.ReviewSeen.ID, want)
			}
			if got, rounds := reviewerDecision(t, pool); got != "" || rounds != 0 {
				t.Fatalf("the round's recorded decision = %q with %d rounds, want none and 0", got, rounds)
			}
			if got := reads.Load(); got != 1 {
				t.Fatalf("the read read the pull request's reviews %d times, want once", got)
			}
		})
	}
}

// The read decodes GitHub's own answer, not just the stand-in's shape:
// testdata/legion-smoke-pull-550-reviews.json is GET /repos/sjawhar/legion-smoke/pulls/550/reviews
// as GitHub answered it, formatted, with a review App's comments and its request for changes
// (5480463470) at an earlier head, two other Apps' comments, and that App's approval of the head
// last. The request for changes is the one review there that decides anything
// (intake.PullRequestReview.Decides), so the read judges it and keeps it as refused, and passes
// every other over unjudged: the issue stays in reviewing and the reviewer's row records nothing.
func TestTheReviewsReadDecodesGitHubsOwnAnswer(t *testing.T) {
	const head = "a2cd06879ab30d0e8fbeca82b2970826a2c3b6b3"
	answer, err := os.ReadFile("testdata/legion-smoke-pull-550-reviews.json")
	if err != nil {
		t.Fatal(err)
	}
	pool := isolatedOutboxPool(t)
	seedCapture(t, pool, phase.Reviewing, "needs_review", completedReviewer("review-1"))
	captureHeadAt(t, pool, head)
	var reads atomic.Int64
	w := reviewReadRuntime(pool, reviewsStandIn(t, func() string { return string(answer) }, &reads).URL)
	readCapture(t, w)
	if got, _ := capturePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the read the issue is in %s, want reviewing: no listed review decides", got)
	}
	if state, _ := reviewerDecision(t, pool); state != "" || reads.Load() != 1 {
		t.Fatalf("the reviewer's row records the decision %q after %d reads of the reviews, want none after one", state, reads.Load())
	}
	if !w.refusedReviews[5480463470] || len(w.refusedReviews) != 1 {
		t.Fatalf("refused reviews = %v, want the review App's request for changes 5480463470 alone: the one review there that decides anything, decoded and judged", w.refusedReviews)
	}
}

// Every refusal stands at every head (workflow's RefusesRound turns on no head), so the read keeps
// each request for changes it refuses as refused, and judges it once in this process: the read
// after it logs no second refusal. It reads no author's write access for any of them, a person's
// or the one App's: a listed review is refused for the list (intake.ResolveAuthorCanWrite reads
// none for it), even once GitHub gives its author write access, so an account given write access
// after the read cannot turn a listed review into a decision. An approval is no candidate at all,
// since it decides no round (intake.PullRequestReview.Decides): it is neither judged nor kept.
func TestTheReadKeepsEveryReviewItRefuses(t *testing.T) {
	const passedOver = "this read passes over it and reads the review below it"
	for _, tc := range []struct {
		name   string
		listed string
		kept   bool
	}{
		{name: "a person's listed request for changes", listed: listedReview(50, "an-outsider", "CHANGES_REQUESTED", captureHead, "2026-10-08T22:15:40Z"), kept: true},
		{name: "the App's request for changes", listed: listedReview(50, reviewReadApp, "CHANGES_REQUESTED", captureHead, "2026-10-08T22:15:40Z"), kept: true},
		{name: "a person's listed approval", listed: listedReview(50, "an-outsider", "APPROVED", captureHead, "2026-10-08T22:15:40Z")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := isolatedOutboxPool(t)
			seedCapture(t, pool, phase.Reviewing, "needs_review", completedReviewer("review-1"))
			captureHeadAt(t, pool, captureHead)
			// an-outsider has write access here: what refuses its review is the list alone.
			var reads, permissionReads atomic.Int64
			api := reviewsStandInWith(t, func() string { return "[" + tc.listed + "]" }, &reads,
				func() string { return "write" }, &permissionReads).URL
			var logs bytes.Buffer
			w := reviewReadRuntime(pool, api)
			w.log = slog.New(slog.NewJSONHandler(&logs, nil))
			for range 2 {
				readCapture(t, w)
			}
			if got, pr := capturePhase(t, pool); got != phase.Reviewing || pr.ReviewSeen.ID != 0 {
				t.Fatalf("after two reads the issue is in %s with %d as its newest deciding review, want reviewing and none: the read applies no review GitHub lists",
					got, pr.ReviewSeen.ID)
			}
			if got, _ := reviewerDecision(t, pool); got != "" {
				t.Fatalf("the round's recorded decision = %q, want none", got)
			}
			if reads.Load() != 2 || permissionReads.Load() != 0 {
				t.Fatalf("the reviews were read %d times and the author's write access %d times, want twice and never", reads.Load(), permissionReads.Load())
			}
			want := 0
			if tc.kept {
				want = 1
			}
			if got := strings.Count(logs.String(), passedOver); got != want {
				t.Fatalf("the two reads logged the refusal %d times, want %d: %s", got, want, logs.String())
			}
			if got := w.refusedReviews[50]; got != tc.kept {
				t.Fatalf("the read keeps review 50 as refused: %v, want %v", got, tc.kept)
			}
		})
	}
}

// The case no clock settles: the branch went from the head the record holds to another and a
// force push put it back, so the head the record holds now is a head it held before the review
// was written. GitHub gives that review the pull request's current commit_id
// (dispatch://INFRA-1557), which is that same head. The read follows the move the lost webhooks
// never brought, and passes the approval over: the issue stays in reviewing and the reviewer's row
// holds no decision.
func TestAListedApprovalDecidesNothingAcrossAForcePush(t *testing.T) {
	read := time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC)
	pool := isolatedOutboxPool(t)
	seedCapture(t, pool, phase.Reviewing, "needs_review", completedReviewer("review-1"))
	captureHeadAt(t, pool, pushedHead)
	// GitHub shows the branch back at captureHead, its activity holding the push that took it to
	// pushedHead and the force push that took it back.
	github := &activityGitHub{}
	github.moved(captureHead, read,
		activityEntry("legion/CAPTURE-1", "force_push", pushedHead, captureHead, implementerLogin, read.Add(-time.Minute)),
		activityEntry("legion/CAPTURE-1", "push", captureHead, pushedHead, implementerLogin, read.Add(-2*time.Minute)))
	github.lists("[" + listedReview(51, reviewReadApp, "APPROVED", captureHead, "2026-10-08T22:15:40Z") + "]")
	readCapture(t, reviewReadRuntime(pool, github.serve(t).URL))
	gotPhase, pr := capturePhase(t, pool)
	if pr.HeadSHA != captureHead {
		t.Fatalf("after the read the record is at %q, want the head the force push put back", pr.HeadSHA)
	}
	if gotPhase != phase.Reviewing {
		t.Fatalf("after the read the issue is in %s, want reviewing", gotPhase)
	}
	if state, _ := reviewerDecision(t, pool); state != "" {
		t.Fatalf("the reviewer's row records the decision %q, want none", state)
	}
}

// GitHub's list holds every review a pull request has ever had, and the read passes over each one
// the round's rule refuses and reads the one below it, so a newer refused review hides no older
// one: here every review is refused, and the read judges and keeps each of them, the newest and
// the one below it alike.
func TestTheReadPassesOverEveryRefusedReview(t *testing.T) {
	const (
		early = "2026-10-08T22:15:40Z"
		late  = "2026-10-08T22:15:52Z"
	)
	for _, tc := range []struct {
		name   string
		newest string
	}{
		{name: "a person's listed request for changes", newest: listedReview(61, "a-writer", "CHANGES_REQUESTED", captureHead, late)},
		{name: "the App's request for changes", newest: listedReview(61, reviewReadApp, "CHANGES_REQUESTED", captureHead, late)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := isolatedOutboxPool(t)
			seedCapture(t, pool, phase.Reviewing, "needs_review", completedReviewer("review-1"))
			captureHeadAt(t, pool, captureHead)
			var reads atomic.Int64
			api := reviewsStandIn(t, func() string {
				return "[" + listedReview(60, reviewReadApp, "CHANGES_REQUESTED", captureHead, early) + "," + tc.newest + "]"
			}, &reads).URL
			w := reviewReadRuntime(pool, api)
			readCapture(t, w)
			if got, _ := capturePhase(t, pool); got != phase.Reviewing {
				t.Fatalf("after the read the issue is in %s, want reviewing: no listed review decides", got)
			}
			if !w.refusedReviews[60] || !w.refusedReviews[61] {
				t.Fatalf("refused reviews = %v, want both 60 and 61: the read passed over the newest and judged the one below it", w.refusedReviews)
			}
		})
	}
}

// GitHub's rate limit on the reviews read ends the read there, as it ends the read on any of its
// reads: every later read on the implement App's installation meets the same limit, and the panes
// share it. The stand-in answers the first pull request's own read and refuses its reviews read; a
// second open pull request is recorded, and nothing of either is read after the refusal.
func TestARateLimitOnTheReviewsReadEndsTheRead(t *testing.T) {
	pool := isolatedOutboxPool(t)
	seedCapture(t, pool, phase.Reviewing, "needs_review", completedReviewer("review-1"))
	if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		records := record.NewStore()
		if err := records.PutIssue(context.Background(), tx, record.Issue{Key: "CAPTURE-2", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: "CAPTURE-2", Project: "CAPTURE", Title: "second",
			Phase: phase.Reviewing, Generation: 1, Status: "needs_review", Rank: "V"}); err != nil {
			return err
		}
		return records.PutPullRequest(context.Background(), tx, record.PullRequest{State: record.PullRequestOpen, Issue: "CAPTURE-2",
			Repo: "acme/widgets", Number: 87, Branch: "legion/CAPTURE-2", HeadSHA: "second"})
	}); err != nil {
		t.Fatalf("seed a second open pull request: %v", err)
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/86", "/repos/acme/widgets/pulls/87":
			w.Write([]byte(`{"base":{"ref":"main"},"mergeable":true}`))
		default:
			w.Header().Set("X-RateLimit-Remaining", "0")
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		}
	}))
	t.Cleanup(server.Close)
	readCapture(t, reviewReadRuntime(pool, server.URL))
	if len(paths) != 2 || paths[1] != "/repos/acme/widgets/pulls/"+strings.TrimPrefix(paths[0], "/repos/acme/widgets/pulls/")+"/reviews" {
		t.Fatalf("the read read %q, want one pull request and then its reviews, and nothing after the rate limit", paths)
	}
}
