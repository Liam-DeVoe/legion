package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// reviewedSHA is the head the round's recorded reviewer approved in reviewing, which the pull
// request has since moved off: headSHA is its head now.
const reviewedSHA = "c0de00000000000000000000000000000000ee01"

// reviewVerdict posts req to the review verdict route.
func (h *harness) reviewVerdict(req ReviewVerdictRequest) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.request(http.MethodPost, "/legion/v1/review-verdict", req, nil)
}

// seedRecordedReviewer records reviewer as the claim whose completion ended key's review round, on
// the reviewer's phases row, as the workflow records that completion (workflow's handoff): its
// review record, the head it reviewed, its approve and the round's approval of that head.
func seedRecordedReviewer(t *testing.T, h *harness, key string, reviewer claim.Token) {
	t.Helper()
	err := h.store.Tx(context.Background(), func(tx pgx.Tx) error {
		return record.NewStore().PutPhase(context.Background(), tx, record.PhaseRow{Issue: key, Role: claim.RoleReviewer, Claim: reviewer,
			HandoffCommit: reviewedSHA, HandoffRecord: "review-1", HandoffVersion: 1, Summary: "reviewed",
			Decision: &record.ReviewDecision{State: "approved", Head: reviewedSHA}})
	})
	if err != nil {
		t.Fatalf("seed the recorded reviewer of %s: %v", key, err)
	}
}

// pastReviewIssue is LEGION-208 standing at at with its pull request at headSHA, its review round
// ended by its live reviewer's completion at reviewedSHA: the head moved past review.
func pastReviewIssue(t *testing.T, at phase.Phase) (*harness, *factRecorder, liveClaim) {
	t.Helper()
	h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	seedIssueAt(t, h, "LEGION-208", at)
	seedPullRequest(t, h, "LEGION-208", headSHA)
	reviewer := newLiveClaim(t, h, "LEGION-208", claim.RoleReviewer)
	seedRecordedReviewer(t, h, "LEGION-208", reviewer.token)
	return h, facts, reviewer
}

// newVerdictWritingHarness is the daemon's routes over a real Postgres with the workflow deciding
// every fact they apply, as newWorkflowHandoffHarness serves them, and a writer of legion-verdict
// that appends the head of each write the workflow asks for to asked, once the fact's transaction
// commits (workflow.Config.WriteVerdict). The write itself is the daemon's (writeVerdict), which
// its own TestAHeadThatMovesPastReviewAsksTheRecordedReviewer drives against a fake GitHub.
func newVerdictWritingHarness(t *testing.T, asked *[]string) *harness {
	t.Helper()
	h := newHarness(t)
	records := record.NewStore()
	engine := workflow.New(records, workflow.Config{Project: testProject, Repositories: testRepositories, Steps: config.DesignGateRootIssues.Steps(),
		ReviewRoundCap: 3, WriteVerdict: func(_, _ string, _ int, head string) { *asked = append(*asked, head) }},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.handoffs = &statusRecorder{}
	h.handler = NewServer("127.0.0.1", 8437, Options{
		Steps:      config.DesignGateRootIssues.Steps(),
		Supervisor: h.supervisor, BootTokens: h.tokens, Project: testProject, OperatorToken: testOperatorToken,
		Controller: h.store, Grants: credential.New(nil), Pool: h.store.Pool(), Record: records,
		Handlers: []intake.Handler{engine}, Dispatch: h.handoffs, Tokens: handoffTokens{}, GitHubAPI: newHandoffGitHub(t).url,
	}).Handler
	return h
}

// seedTesterPass records the tester's pass of key at commit, on its phases row.
func seedTesterPass(t *testing.T, h *harness, key, commit string) {
	t.Helper()
	err := h.store.Tx(context.Background(), func(tx pgx.Tx) error {
		return record.NewStore().PutPhase(context.Background(), tx, record.PhaseRow{Issue: key, Role: claim.RoleTester, Claim: "test-claim",
			HandoffCommit: commit, HandoffRecord: "test-1", HandoffVersion: 1, Summary: "tested"})
	})
	if err != nil {
		t.Fatalf("seed the tester's pass of %s: %v", key, err)
	}
}

// storedVerdict is the verdict the store holds on acme/widgets#42 at head, the one the writer of
// legion-verdict reads, and whether it holds one.
func storedVerdict(t *testing.T, h *harness, head string) (store.Verdict, bool) {
	t.Helper()
	var given store.Verdict
	var found bool
	if err := h.store.Tx(context.Background(), func(tx pgx.Tx) (err error) {
		given, found, err = store.VerdictTx(context.Background(), tx, ghrepo.MustParse("acme/widgets"), 42, head)
		return err
	}); err != nil {
		t.Fatalf("read the verdict at %s: %v", head, err)
	}
	return given, found
}

// The verdict past review. With the issue in accepting, retro or merging, no review round is
// open, and the round's recorded reviewer sends its verdict on the head the pull request moved
// to with `POST /legion/v1/review-verdict`. The route takes it only from that reviewer's claim
// (VERDICT_CALLER_FORBIDDEN for an implementer's, a tester's or another reviewer session's) and
// only at the head the daemon's record holds (VERDICT_HEAD_NOT_CURRENT), each refusal writing
// nothing. A call it takes queues one intake.ReviewVerdict fact, with the verdict, the head and
// the testedCommit as sent, and writes no handoff and no completion: the reviewer's row and the
// issue's phase stand as they were. Through the workflow, the fact records the round's decision at
// that head and asks for the write of legion-verdict there, and moves the issue back to testing
// only where an approve carries no tester's pass (the recording case below).
func TestReviewVerdictPastReviewTakesOnlyTheRecordedReviewer(t *testing.T) {
	for _, at := range []phase.Phase{phase.Accepting, phase.Retro, phase.Merging} {
		t.Run(string(at), func(t *testing.T) {
			h, facts, reviewer := pastReviewIssue(t, at)
			before := phaseRowOf(t, h, "LEGION-208", claim.RoleReviewer)
			implementer := newLiveClaim(t, h, "LEGION-208", claim.RoleImplementer)
			tester := newLiveClaim(t, h, "LEGION-208", claim.RoleTester)

			for _, caller := range []liveClaim{implementer, tester} {
				message := refused(t, h.reviewVerdict(ReviewVerdictRequest{GrantID: caller.grant(t), Verdict: "approve", Commit: headSHA,
					TestedCommit: testedSHA, Summary: "read the merge"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
				if !strings.Contains(message, "nothing was recorded") {
					t.Fatalf("the refusal of the %s's call says %q, want it to say nothing was recorded", caller.token, message)
				}
			}
			message := refused(t, h.reviewVerdict(ReviewVerdictRequest{GrantID: reviewer.grant(t), Verdict: "approve", Commit: reviewedSHA,
				Summary: "read the merge"}), http.StatusConflict, "VERDICT_HEAD_NOT_CURRENT")
			for _, want := range []string{reviewedSHA, headSHA, "nothing was recorded"} {
				if !strings.Contains(message, want) {
					t.Fatalf("the refusal of a verdict at another head says %q, want it to say %q", message, want)
				}
			}
			if got := facts.recorded(); len(got) != 0 {
				t.Fatalf("facts after the refusals = %#v, want none applied", got)
			}

			for i, sent := range []ReviewVerdictRequest{
				{Verdict: "approve", Commit: headSHA, TestedCommit: testedSHA, Summary: "read the merge, which changes no code that runs"},
				{Verdict: "changes", Commit: headSHA, Summary: "the merge breaks the parser"},
			} {
				sent.GrantID = reviewer.grant(t)
				if recorder := h.reviewVerdict(sent); recorder.Code != http.StatusOK {
					t.Fatalf("the recorded reviewer's %s = %d: %s", sent.Verdict, recorder.Code, recorder.Body)
				}
				got := facts.recorded()
				want := intake.ReviewVerdict{Issue: "LEGION-208", Claim: reviewer.token, Verdict: sent.Verdict, Commit: headSHA,
					TestedCommit: sent.TestedCommit, Summary: sent.Summary}
				if len(got) != i+1 || !reflect.DeepEqual(got[i], want) {
					t.Fatalf("facts after the recorded reviewer's %s = %#v, want %d, the last %#v", sent.Verdict, got, i+1, want)
				}
			}
			assertIssuePhase(t, h, at)
			if after := phaseRowOf(t, h, "LEGION-208", claim.RoleReviewer); !reflect.DeepEqual(after, before) {
				t.Fatalf("the reviewer's row after its verdicts = %+v, want it as it stood, %+v: the call writes no handoff", after, before)
			}
		})
	}

	// Another reviewer session: a reviewer claim that is not the claim whose completion ended this
	// issue's round, and the reviewer of an issue whose round no reviewer's completion ended.
	for _, tc := range []struct {
		name     string
		recorded func(reviewer liveClaim) claim.Token
	}{
		{name: "a reviewer the round's record does not name", recorded: func(liveClaim) claim.Token { return "legion-acme-legion-208-reviewer" }},
		{name: "a reviewer of a round no reviewer ended", recorded: func(liveClaim) claim.Token { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
			seedIssueAt(t, h, "LEGION-208", phase.Accepting)
			seedPullRequest(t, h, "LEGION-208", headSHA)
			reviewer := newLiveClaim(t, h, "LEGION-208", claim.RoleReviewer)
			seedRecordedReviewer(t, h, "LEGION-208", tc.recorded(reviewer))

			refused(t, h.reviewVerdict(ReviewVerdictRequest{GrantID: reviewer.grant(t), Verdict: "approve", Commit: headSHA,
				Summary: "read the merge"}), http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN")
			if got := facts.recorded(); len(got) != 0 {
				t.Fatalf("facts after the refusal = %#v, want none applied", got)
			}
		})
	}

	// The round's own reviewer outside the three phases, and with a word or a commit missing.
	for _, tc := range []struct {
		name   string
		at     phase.Phase
		sent   ReviewVerdictRequest
		status int
		code   string
	}{
		{name: "in reviewing, where its completion carries the verdict", at: phase.Reviewing,
			sent: ReviewVerdictRequest{Verdict: "approve", Commit: headSHA, Summary: "reviewed"}, status: http.StatusConflict, code: "VERDICT_NOT_PAST_REVIEW"},
		{name: "the tester's pass", at: phase.Retro,
			sent: ReviewVerdictRequest{Verdict: "pass", Commit: headSHA, Summary: "read the merge"}, status: http.StatusBadRequest, code: "REVIEWER_VERDICT_REQUIRED"},
		{name: "no commit", at: phase.Retro,
			sent: ReviewVerdictRequest{Verdict: "approve", Summary: "read the merge"}, status: http.StatusBadRequest, code: "VERDICT_COMMIT_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, facts, reviewer := pastReviewIssue(t, tc.at)
			tc.sent.GrantID = reviewer.grant(t)
			refused(t, h.reviewVerdict(tc.sent), tc.status, tc.code)
			if got := facts.recorded(); len(got) != 0 {
				t.Fatalf("facts after the refusal = %#v, want none applied", got)
			}
		})
	}

	// The recording case, through the workflow: the recorded reviewer's verdict at the current head
	// is the round's decision there and the verdict the writer of legion-verdict reads, whose write
	// the same fact asks for on its commit, and no handoff is written. An approve carrying the
	// commit of the tester's recorded pass, and a changes, leave the phase as it was; an approve
	// naming no testedCommit carries no pass and moves the issue back to testing, with the tester's
	// pass cleared, so nothing writes success on the head.
	for _, tc := range []struct {
		name  string
		sent  ReviewVerdictRequest
		state string
		moves bool
	}{
		{name: "an approve carrying the tester's pass", sent: ReviewVerdictRequest{Verdict: "approve", Commit: headSHA, TestedCommit: testedSHA,
			Summary: "read the push, which changes no code that runs"}, state: "approved"},
		{name: "a changes", sent: ReviewVerdictRequest{Verdict: "changes", Commit: headSHA, Summary: "the push breaks the parser"}, state: "changes_requested"},
		{name: "an approve naming no testedCommit", sent: ReviewVerdictRequest{Verdict: "approve", Commit: headSHA, Summary: "read the push"},
			state: "approved", moves: true},
	} {
		for _, at := range []phase.Phase{phase.Accepting, phase.Retro, phase.Merging} {
			t.Run("recorded through the workflow, "+string(at)+", "+tc.name, func(t *testing.T) {
				var asked []string
				h := newVerdictWritingHarness(t, &asked)
				seedIssueAt(t, h, "LEGION-208", at)
				seedPullRequest(t, h, "LEGION-208", headSHA)
				seedTesterPass(t, h, "LEGION-208", testedSHA)
				reviewer := newLiveClaim(t, h, "LEGION-208", claim.RoleReviewer)
				seedRecordedReviewer(t, h, "LEGION-208", reviewer.token)
				before := phaseRowOf(t, h, "LEGION-208", claim.RoleReviewer)

				tc.sent.GrantID = reviewer.grant(t)
				if recorder := h.reviewVerdict(tc.sent); recorder.Code != http.StatusOK {
					t.Fatalf("the recorded reviewer's %s = %d: %s", tc.sent.Verdict, recorder.Code, recorder.Body)
				}
				after := phaseRowOf(t, h, "LEGION-208", claim.RoleReviewer)
				if decided := after.Decision; decided == nil || decided.State != tc.state || decided.Head != headSHA || decided.TestedCommit != tc.sent.TestedCommit {
					t.Fatalf("the round's decision = %+v, want %s at %s carrying %q", decided, tc.state, headSHA, tc.sent.TestedCommit)
				}
				after.Decision = before.Decision
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("the reviewer's row after its verdict = %+v, want it as it stood but for the decision, %+v: the call writes no handoff", after, before)
				}
				if given, found := storedVerdict(t, h, headSHA); !found || string(given.Verdict) != tc.sent.Verdict || given.TestedCommit != tc.sent.TestedCommit ||
					given.Session != string(reviewer.token) {
					t.Fatalf("the store's verdict at %s = %+v (found %t), want the reviewer's %s carrying %q", headSHA, given, found, tc.sent.Verdict, tc.sent.TestedCommit)
				}
				if !slices.Equal(asked, []string{headSHA}) {
					t.Fatalf("the workflow asked for writes of legion-verdict on %v, want one on %s in the verdict's own pass", asked, headSHA)
				}
				tester := phaseRowOf(t, h, "LEGION-208", claim.RoleTester)
				if !tc.moves {
					assertIssuePhase(t, h, at)
					if tester.HandoffCommit != testedSHA {
						t.Fatalf("the tester's row = %+v, want its pass at %s kept", tester, testedSHA)
					}
					return
				}
				assertIssuePhase(t, h, phase.Testing)
				if tester.HandoffCommit != "" {
					t.Fatalf("the tester's row = %+v, want its pass cleared, so nothing writes success on %s", tester, headSHA)
				}
			})
		}
	}
}

// The reviewer's two bodies as Go writes them, and the route's answer: its verdict past review,
// and its completion of reviewing carrying testedCommit. contracts' legion-api.test.ts parses each
// under the schema that mirrors it (LegionReviewVerdictRequest, LegionEmptyResponse,
// LegionHandoffCompleteRequest), and the request schemas are strict, so a name Go writes and the
// contract does not read fails there.
func TestReviewerVerdictBodiesGolden(t *testing.T) {
	golden(t, "review-verdict-request.json", ReviewVerdictRequest{GrantID: "grant-for-one-command", Verdict: "approve",
		Commit: headSHA, TestedCommit: testedSHA, Summary: "The merge forward changes no code that runs."})
	golden(t, "review-verdict.json", EmptyResponse{})
	golden(t, "handoff-complete-request-reviewer.json", HandoffCompleteRequest{GrantID: "grant-for-one-command",
		Summary: "Reviewed; no finding stands.", Verdict: "approve", Commit: headSHA, TestedCommit: testedSHA,
		HandoffID: "5b1f0c2e-9d7a-4c3b-8e61-2f4a9d0c7b13", HandoffVersion: 1})
}
