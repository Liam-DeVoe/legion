package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
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
			HandoffCommit: reviewedSHA, HandoffRecord: "review-1", HandoffVersion: 1, Verdict: "approve", Summary: "reviewed",
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

// The verdict past review. With the issue in accepting, retro or merging, no review round is
// open, and the round's recorded reviewer sends its verdict on the head the pull request moved
// to with `POST /legion/v1/review-verdict`. The route takes it only from that reviewer's claim
// (VERDICT_CALLER_FORBIDDEN for an implementer's, a tester's or another reviewer session's) and
// only at the head the daemon's record holds (VERDICT_HEAD_NOT_CURRENT), each refusal writing
// nothing. A call it takes queues one intake.ReviewVerdict fact, with the verdict, the head and
// the testedCommit as sent, and writes no handoff and no completion: the reviewer's row and the
// issue's phase stand as they were. What the fact records at the head is the workflow's to
// decide, which this route does not reach.
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
