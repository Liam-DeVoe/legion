package workflow

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// besideTheFix is LEGION-208 in reviewing on `implementer tester reviewer`, its review step
// started at fixHead and fix 1 running beside it for k-ci failed there (startedFix), with the
// session the reviewer's agent registered recorded on the reviewer's claim: the session a
// review-App review's Legion footer names to be the reviewer's own (byReviewer).
func besideTheFix(t *testing.T) *fixRig {
	t.Helper()
	r := startedFix(t, intake.FixJobRead{Name: "unit", URL: fixPage})
	seedClaimSession(t, r.pool, reviewClaim, "ses-reviewer")
	return r
}

// reviewOf applies review id of LEGION-208's pull request in state, as the reviewer's own session
// submits it through the review App: its Legion footer names that session, head as the head it
// read, and the entry the reviewer's row stands at (roundReviewBody). It is submitted an hour after
// the engine's clock, so after any completion of the reviewer's: the reviewer's answer to a round
// its completion left undecided (reviewersAnswer).
func (r *fixRig) reviewOf(id int64, state, head string) {
	r.t.Helper()
	r.apply(fmt.Sprintf("review-%d", id), intake.PullRequestReview{Repo: statesRepo, Number: 42, ID: id, State: state, CommitID: head,
		Author: testReviewApp, Body: roundReviewBody(r.t, r.pool, "ses-reviewer", "reviewed", head), SubmittedAt: stepClock.Add(time.Hour)})
}

// fixPushes applies the fix's push of head onto the branch of LEGION-208's task from fixHead, the
// head its check failed at: the push GitHub delivers, then the synchronize that moves the pull
// request's head.
func (r *fixRig) fixPushes(head string) {
	r.t.Helper()
	r.apply("fix-push", intake.Push{Repo: statesRepo, Branch: "legion/LEGION-208", Before: fixHead, After: head, Pusher: "legion-implementer[bot]"})
	r.apply("fix-sync", intake.PullRequestSynchronized{Repo: statesRepo, Number: 42, Branch: "legion/LEGION-208", Before: fixHead, HeadSHA: head})
}

// reviewerCompletes applies the reviewer's completion of its round, naming its handoff record.
func (r *fixRig) reviewerCompletes() {
	r.t.Helper()
	r.apply("review-done", intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim,
		Summary: "reviewed", HandoffRecord: reviewRecord, HandoffVersion: 1})
}

// assertApprovalOfXDecided fails unless the reviewer's approval of fixHead decided its round: the
// reviewer's row records it with no refusal, the commit the reviewer read is fixHead, and the run
// of the review's step, the task's third, ended passed.
func (r *fixRig) assertApprovalOfXDecided() {
	r.t.Helper()
	if decision := reviewerDecision(r.t, r.pool); decision == nil || decision.State != "approved" || decision.Head != fixHead || decision.Refused != "" {
		r.t.Fatalf("the reviewer's decision = %+v, want the approval of %s with no refusal", decision, fixHead)
	}
	if got := reviewerRow(r.t, r.pool).ReviewedHead; got != fixHead {
		r.t.Fatalf("the reviewer's row reads the reviewed commit %q, want %s, the commit its review step started at", got, fixHead)
	}
	r.assertRunEnded(3, 1, ResultPassed, 0)
}

// assertLeftTheReview fails unless LEGION-208's task has gone on past its review.
func (r *fixRig) assertLeftTheReview() {
	r.t.Helper()
	if got := issueOf(r.t, r.pool, r.engine, "LEGION-208").Phase; got == phase.Reviewing {
		r.t.Fatalf("LEGION-208 stands in %s, want its task gone on past its review", got)
	}
}

// assertRefusedAt fails unless a review whose footer names named decided nothing while the pull
// request stands at headTwo: the reviewer's row records no decision and keeps the refusal for the
// stuck notice, the issue stays in reviewing, and the review's run has not ended.
func (r *fixRig) assertRefusedAt(named string) {
	r.t.Helper()
	want := "footer names " + named + ", not the head " + headTwo
	if decision := reviewerDecision(r.t, r.pool); decision == nil || decision.State != "" || !strings.HasPrefix(decision.Refused, want) {
		r.t.Fatalf("the reviewer's decision = %+v, want none, refused as %q", decision, want)
	}
	assertPhase(r.t, r.pool, phase.Reviewing)
	if end := r.runEnd(3, 1); end != nil {
		r.t.Fatalf("the review's run ended %+v, want it running", end)
	}
}

// A review step that started at X ends with the review of X, also after the fix beside it pushed
// Y: the reviewer's approval whose Legion footer names X decides its round though the pull request
// stands at Y, the commit the reviewer read stays X for the next review, and the review's run ends
// passed. After the fix ended with new commits, the way after it then runs the steps after the fix
// again at Y, the tester's first. While the fix runs, the task goes on past its review.
func TestAReviewOfXDecidesBesideTheFix(t *testing.T) {
	r := besideTheFix(t)
	r.pushed(headTwo)
	r.apply("fix-done", fixCompletion(headTwo))

	r.reviewOf(1, "approved", fixHead)

	r.assertApprovalOfXDecided()
	assertPhase(t, r.pool, phase.Testing)

	t.Run("while the fix runs", func(t *testing.T) {
		r := besideTheFix(t)
		r.pushed(headTwo)

		r.reviewOf(1, "approved", fixHead)

		r.assertApprovalOfXDecided()
		r.assertLeftTheReview()
	})
}

// A push of the fix moves the pull request's head from X to Y while the review runs, and only the
// fix pushes while it runs, so the push re-asks no round of the reviewer. The reviewer completed
// its round with no deciding review, and the architect was told once that the round is stuck; the
// fix's push of Y tells nothing more, where a head moved by the work under review would tell the
// architect to ask the reviewer for a review of Y. The reviewer's approval of X then decides its
// step. An approval of X that waits on GitHub's answer for X when the fix pushes is decided by
// that push, and nobody is told the round is stuck.
func TestAPushOfTheFixReasksNoReview(t *testing.T) {
	r := besideTheFix(t)
	r.reviewerCompletes()
	if told := reviewStuckNotices(t, r.pool); len(told) != 1 {
		t.Fatalf("after the reviewer completed undecided the architect was told %+v, want one review-stuck notice", told)
	}

	r.fixPushes(headTwo)

	if told := reviewStuckNotices(t, r.pool); len(told) != 1 {
		t.Fatalf("after the fix's push the architect was told %+v, want only the notice of the completion: the fix's push re-asks no review", told)
	}
	r.apply("fix-done", fixCompletion(headTwo))
	r.reviewOf(1, "approved", fixHead)
	r.assertApprovalOfXDecided()
	assertPhase(t, r.pool, phase.Testing)

	t.Run("an approval of X that waits on GitHub's answer", func(t *testing.T) {
		r := besideTheFix(t)
		seedPR(t, r.pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: statesRepo, Number: 42,
			Branch: "legion/LEGION-208", HeadSHA: fixHead, Failing: []string{}, MergeState: record.MergeStateBlocked})
		r.reviewOf(1, "approved", fixHead)
		r.reviewerCompletes()
		assertPhase(t, r.pool, phase.Reviewing)

		r.fixPushes(headTwo)

		if told := reviewStuckNotices(t, r.pool); len(told) != 0 {
			t.Fatalf("after the fix's push the architect was told %+v, want nothing: the push is the fix's", told)
		}
		r.assertApprovalOfXDecided()
		r.assertLeftTheReview()
	})
}

// The control, kept: beside the fix only the commit the review step started at decides beside the
// pull request's head. A review whose footer names Z, neither X nor Y, is refused as today, and the
// issue stays in reviewing with the refusal kept for the stuck notice; and with no fix beside the
// review, a review of X is refused at Y, as today.
func TestAReviewOfAnotherHeadStillWaits(t *testing.T) {
	r := besideTheFix(t)
	r.pushed(headTwo)

	r.reviewOf(1, "approved", headThree)

	r.assertRefusedAt(headThree)

	t.Run("a review of X with no fix beside it is refused at Y", func(t *testing.T) {
		r := reviewing(t)
		seedClaimSession(t, r.pool, reviewClaim, "ses-reviewer")
		r.pushed(headTwo)

		r.reviewOf(1, "approved", fixHead)

		r.assertRefusedAt(fixHead)
	})
}

// mergeStateAtX records GitHub's merge state for LEGION-208's pull request at fixHead, with one
// check run concluded there, so an approval standing on fixHead is judged on that state alone.
func (r *fixRig) mergeStateAtX(state record.MergeState) {
	r.t.Helper()
	seedPR(r.t, r.pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: statesRepo, Number: 42,
		Branch: "legion/LEGION-208", HeadSHA: fixHead, Failing: []string{}, Cancelled: []string{},
		Mergeability: record.MergeabilityMergeable, MergeState: state,
		CheckedHead: fixHead, CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 1, Head: fixHead, Conclusion: record.RunSucceeded}}})
}

// While a fix runs beside the review, the reviewer's approval of X, the head GitHub reports
// `blocked`, ends its round and the task goes on past its review, with no notice of a stuck round:
// a check the fix owns is what blocks the head, and the task's end waits for each check a CI job
// runs. The controls, kept: with no fix the same approval stays in reviewing and the architect is
// told the round is stuck, as the base does; and a head GitHub reports `behind` keeps the wait
// beside a fix, since the fix does not cause it.
func TestAnApprovalOfABlockedHeadBesideARunningFixEndsItsRound(t *testing.T) {
	r := besideTheFix(t)
	r.mergeStateAtX(record.MergeStateBlocked)

	r.reviewOf(1, "approved", fixHead)

	r.assertLeftTheReview()
	if told := reviewStuckNotices(t, r.pool); len(told) != 0 {
		t.Fatalf("the architect was told %+v, want nothing: the blocked head beside a fix ends the round", told)
	}

	t.Run("with no fix the round stays stuck and tells", func(t *testing.T) {
		r := reviewing(t)
		seedClaimSession(t, r.pool, reviewClaim, "ses-reviewer")
		r.mergeStateAtX(record.MergeStateBlocked)
		r.reviewOf(1, "approved", fixHead)
		assertPhase(t, r.pool, phase.Reviewing)
		if told := reviewStuckNotices(t, r.pool); len(told) != 1 {
			t.Fatalf("the architect was told %+v, want one review-stuck notice", told)
		}
	})

	t.Run("a head behind its base keeps the wait beside a fix", func(t *testing.T) {
		r := besideTheFix(t)
		r.mergeStateAtX(record.MergeStateBehind)
		r.reviewOf(1, "approved", fixHead)
		assertPhase(t, r.pool, phase.Reviewing)
	})
}
