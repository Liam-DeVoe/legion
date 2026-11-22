package api

import (
	"fmt"
	"net/http"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// ReviewerVerdictContract is the daemon contract that gave the reviewer a verdict of its own
// (version.go's 225): its completion of reviewing names approve or changes on the head its review
// record names, with testedCommit where that verdict carries the tester's pass to the head, and
// `POST /legion/v1/review-verdict` takes its verdict on a head that moved past review. A reviewer
// whose plugin declared an earlier contract knows the tester's words alone, and the daemon still
// serves its claim while its turn runs and while its pod is in an update round (staleClaim), so
// its completion is read in those words (reviewerVerdictOf).
const ReviewerVerdictContract = 225

// reviewerVerdictOf is the verdict a reviewer's word sent is, for a claim whose plugin declared
// contract, and the sentence of its refusal REVIEWER_VERDICT_REQUIRED where the word is none of
// the verdict's. The verdict is approve (the head may merge) or changes (the head needs changes).
// A plugin before ReviewerVerdictContract offers its reviewer the tester's words alone, so its
// pass is approve and its fail is changes, and its refusal names those two words as the verdict
// they are taken as: the old reviewer learns the words from this answer, since nothing else it
// reads names a verdict for it. Its pending, a check report's word, is refused as no verdict is.
func reviewerVerdictOf(sent string, contract int) (verdict, refusal string) {
	approve, changes := string(store.VerdictApprove), string(store.VerdictChanges)
	old := contract < ReviewerVerdictContract
	switch {
	case sent == approve || sent == changes:
		return sent, ""
	case old && sent == "pass":
		return approve, ""
	case old && sent == "fail":
		return changes, ""
	}
	named := fmt.Sprintf("not %q", sent)
	if sent == "" {
		named = "and this one names none"
	}
	if !old {
		return "", fmt.Sprintf("a reviewer's verdict on the head it reviewed is approve (the head may merge) or changes (the head needs changes), %s; nothing was recorded", named)
	}
	return "", fmt.Sprintf("a reviewer's completion names its verdict on the head it reviewed, %s: this reviewer's plugin speaks daemon API contract %d, from before the reviewer's verdict, so complete again with verdict pass, which this daemon takes as approve (the head may merge), or fail, which it takes as changes (the head needs changes); nothing was recorded",
		named, contract)
}

// ReviewVerdictRequest is `POST /legion/v1/review-verdict`'s body: the reviewer's verdict, approve
// or changes, on Commit, the head its issue's pull request moved to past review, with
// TestedCommit, the commit of the tester's pass the verdict carries to that head where it states
// that no commit since changes code that runs, and Summary, its two sentences. The `legion` tool's
// review_verdict sends it (packages/pi-shared/src/legion-phase-ops.ts), and contracts'
// LegionReviewVerdictRequest mirrors it.
type ReviewVerdictRequest struct {
	GrantID      string `json:"grantId"`
	Verdict      string `json:"verdict"`
	Commit       string `json:"commit"`
	TestedCommit string `json:"testedCommit,omitempty"`
	Summary      string `json:"summary"`
}

// reviewVerdict is the verdict past review: each time the head of an issue's pull request moves
// while the issue stands past its review (record.PastReview: accepting, retro or merging, or held
// from one of them), and so with no review round open, the round's recorded reviewer sends its
// verdict on the new head here. The call is no completion and writes no handoff: it queues one
// intake.ReviewVerdict fact, which the workflow records as the round's decision at that head, and
// moves no phase but where its testedCommit carries no tester's pass (the workflow's to decide).
// Every refusal is written before the fact, so a refused call records nothing:
//
//   - VERDICT_CALLER_FORBIDDEN (403): the grant is not the claim the reviewer's phases row records
//     as the one whose completion ended the issue's review round (record.PhaseRow.Claim): an
//     implementer's, a tester's, the controller's, or another reviewer session's claim, a round no
//     reviewer's completion ended among them. Only that reviewer read the change the round decided.
//   - REVIEWER_VERDICT_REQUIRED (400): the verdict is neither approve nor changes
//     (reviewerVerdictOf).
//   - VERDICT_COMMIT_REQUIRED (400): the call names no commit.
//   - VERDICT_NOT_PAST_REVIEW (409): the issue does not stand past its review. In reviewing the
//     reviewer's completion carries its verdict, and before review there is no round to decide.
//   - VERDICT_HEAD_NOT_CURRENT (409): the commit is not the head the daemon's record holds for the
//     issue's pull request, or the record holds no pull request. The record's own head and never
//     GitHub's, as the entry fence reads it (behindEntryClause): a verdict on any other head is
//     one on code the pull request does not stand on.
//
// The fact is the request's own (requestFactID): two calls are two verdicts, the later one
// standing, so a reviewer that changes its verdict at one head is heard however often it does.
func (s *server) reviewVerdict(w http.ResponseWriter, r *http.Request) {
	var req ReviewVerdictRequest
	if !s.readBody(w, r, &req, s.redeemIdentityCheck) || !s.requireFailureFields(w, r, field{"grantId", req.GrantID}, field{"summary", req.Summary}) {
		return
	}
	grant, ok := s.redeem(w, r, req.GrantID)
	if !ok {
		return
	}
	if grant.Controller || grant.Role != claim.RoleReviewer {
		who := "the controller"
		if !grant.Controller {
			who = "the " + string(grant.Role)
		}
		s.writeFailure(w, r, http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN", fmt.Sprintf(
			"review_verdict is the verdict of the reviewer whose completion ended the issue's review round, and this call is %s's; nothing was recorded", who))
		return
	}
	if s.pool == nil || s.records == nil {
		s.writeFailure(w, r, http.StatusInternalServerError, "FACTS_UNAVAILABLE", "fact intake is unavailable")
		return
	}
	// No plugin before ReviewerVerdictContract offers this call, so its verdict is read in the
	// reviewer's own two words alone.
	verdict, refusal := reviewerVerdictOf(req.Verdict, ReviewerVerdictContract)
	if refusal != "" {
		s.writeFailure(w, r, http.StatusBadRequest, "REVIEWER_VERDICT_REQUIRED", refusal)
		return
	}
	if req.Commit == "" {
		s.writeFailure(w, r, http.StatusBadRequest, "VERDICT_COMMIT_REQUIRED",
			"a reviewer's verdict past review is a verdict on the pull request's current head, and this call names no commit: send it again with commit set to that head; nothing was recorded")
		return
	}
	position, err := s.handoffPosition(r.Context(), grant.Issue, claim.RoleReviewer)
	if err != nil {
		s.writeFailure(w, r, http.StatusInternalServerError, "RECORD_UNAVAILABLE", "could not read the issue record")
		return
	}
	issue := position.issue
	if issue == nil {
		s.writeFailure(w, r, http.StatusNotFound, "ISSUE_NOT_FOUND", "issue is not recorded")
		return
	}
	if position.completedBy != grant.Claim {
		recorded := "no reviewer's completion ended a review round of " + issue.Key
		if position.completedBy != "" {
			recorded = fmt.Sprintf("the review round of %s was ended by the completion of reviewer claim %s, not of this claim %s", issue.Key, position.completedBy, grant.Claim)
		}
		s.writeFailure(w, r, http.StatusForbidden, "VERDICT_CALLER_FORBIDDEN", fmt.Sprintf(
			"review_verdict is the verdict of the reviewer whose completion ended the issue's review round, and %s; nothing was recorded", recorded))
		return
	}
	if !record.PastReview(*issue) {
		s.writeFailure(w, r, http.StatusConflict, "VERDICT_NOT_PAST_REVIEW", fmt.Sprintf(
			"%s stands in %s: review_verdict is the reviewer's verdict on a head that moved past review, in accepting, retro or merging; in reviewing the reviewer's handoff_complete carries its verdict. Nothing was recorded",
			issue.Key, record.StandsAt(*issue)))
		return
	}
	if pr := position.pullRequest; pr == nil || pr.HeadSHA != req.Commit {
		current := "the daemon records no pull request for " + issue.Key
		if pr != nil {
			current = fmt.Sprintf("the head of %s's pull request #%d is %s", issue.Key, pr.Number, pr.HeadSHA)
		}
		s.writeFailure(w, r, http.StatusConflict, "VERDICT_HEAD_NOT_CURRENT", fmt.Sprintf(
			"this verdict is on %s, and %s: read that head and send the verdict on it; nothing was recorded", req.Commit, current))
		return
	}
	result, err := s.apply(r.Context(), "api", requestFactID("review-verdict"), intake.ReviewVerdict{Issue: issue.Key, Claim: grant.Claim,
		Verdict: verdict, Commit: req.Commit, TestedCommit: req.TestedCommit, Summary: req.Summary})
	if err != nil {
		s.writeFailure(w, r, http.StatusInternalServerError, "FACT_APPLY_FAILED", "could not apply the review verdict's fact")
		return
	}
	if result.Refusal != nil {
		s.writeFailure(w, r, result.Refusal.Status, result.Refusal.Code, result.Refusal.Message)
		return
	}
	s.log.Info("api: recorded a review verdict", "issue", issue.Key, "claim", grant.Claim, "phase", issue.Phase,
		"verdict", verdict, "commit", req.Commit, "testedCommit", req.TestedCommit)
	s.writeAnswer(w, r, AnswerEmptyObject, http.StatusOK, EmptyResponse{})
}
