package workflow

// One rule says whether the merge of the head the pull request stands at is owed to a role
// (owedAsk): the hold at each start, the completion fence, the landed message and the delivery of
// each row read it, so a worker is held exactly while the merge is owed, and is told to wait only
// if it was held. A merge owed at the head always has its one asked role: a move that ends the ask
// asks the role of the phase it enters where the merge is owed there, and an asked role whose
// claim ended keeps it, the ask sent again at its next start.

import (
	"reflect"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The plan `implementer reviewer tester`: the implementer is asked in reviewing, and a later read
// finds the head mergeable, which clears the record's confirmed conflict and leaves the ask, which
// the implementer can still act on. The approval then starts the tester, which is held, as the
// fence reads it.
func TestATesterStartedAfterAMergeableReadIsHeldWhileTheAskIsOwed(t *testing.T) {
	pool := plannedConflictIssue(t, phase.Reviewing, settledGreen("lint"), "implementer", "reviewer", "tester")
	confirmsConflict(t, pool, &record.BaseRules{Required: []string{"ci"}})
	applyRefusingNothing(t, pool, readsAfter(2*time.Minute), intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main",
		HeadSHA: conflictHead, Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
	if pr := recordedPR(t, pool, "LEGION-208"); pr.ConflictingHead != "" || pr.MergeForward == nil {
		t.Fatalf("after the mergeable read the record holds conflicting head %q and ask %+v, want none and the implementer's ask", pr.ConflictingHead, pr.MergeForward)
	}
	applyRefusingNothing(t, pool, readsAfter(3*time.Minute), intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 1,
		State: "approved", CommitID: conflictHead, HeadSHA: conflictHead, Author: "a-writer", AuthorCanWrite: true, Body: "ship it",
		SubmittedAt: firstConflictRead.Add(3 * time.Minute)})
	if got := issuePhase(t, pool); got != phase.Testing {
		t.Fatalf("after the approval the issue is in %s, want testing", got)
	}
	why := record.ConflictCheckNotGreen
	want := []record.MergeForwardMessage{implementerAsked(phase.Reviewing, why), heldIn(claim.RoleReviewer, phase.Reviewing, why),
		heldIn(claim.RoleTester, phase.Testing, why)}
	if got := mergeForwardMessages(t, pool); !reflect.DeepEqual(got, want) {
		t.Errorf("the merge-forward messages are %+v, want %+v: the tester starts while the implementer's ask is owed", got, want)
	}
	if worker, held := heldWorker(storedIssue(t, pool, "LEGION-208"), recordedPR(t, pool, "LEGION-208")); !held || worker != claim.RoleTester {
		t.Errorf("the fence's held rule names %q (held %t), want the tester", worker, held)
	}
}

// On the default steps the implementer, asked in implementing, moves back into planning, which
// stands before the implementer, where no merge forward stands: nobody is asked again, so the
// implementer is told once that the merge is no longer its work, its ask is delivered no more,
// the planner is not held, and the fence tells no worker to wait.
func TestAnAskOfImplementingThatNoPhaseAsksAgainIsWithdrawn(t *testing.T) {
	pool := seedConflictIssue(t, phase.Implementing, "in_progress", settledGreen("lint"))
	confirmsConflict(t, pool, &record.BaseRules{Required: []string{"ci"}})
	applyRefusingNothing(t, pool, readsAfter(2*time.Minute), intake.BackwardMove{Issue: "LEGION-208", Requester: claim.RoleImplementer,
		To: phase.Planning, Reason: "the plan misses a case"})
	if got := issuePhase(t, pool); got != phase.Planning {
		t.Fatalf("after the move back the issue is in %s, want planning", got)
	}
	why := record.ConflictCheckNotGreen
	want := []record.MergeForwardMessage{implementerAsked(phase.Implementing, why), withdrawnIn(phase.Planning, why)}
	if got := mergeForwardMessages(t, pool); !reflect.DeepEqual(got, want) {
		t.Errorf("the merge-forward messages are %+v, want %+v: the implementer is told once, and the planner is not held", got, want)
	}
	issue, pr := storedIssue(t, pool, "LEGION-208"), recordedPR(t, pool, "LEGION-208")
	if why := leftMergeForwardAt(issue, false, &pr, claim.RoleImplementer, record.MergeForwardAsk, conflictHead); why == "" {
		t.Errorf("the implementer's ask of %s is still delivered in planning, want it ended", conflictHead)
	}
	if worker, held := heldWorker(issue, pr); held {
		t.Errorf("the fence's held rule names %q, want nobody: no worker was held", worker)
	}
}

// On the default steps a conflict confirmed in merging, with a check the base requires not green,
// asks the merger. The merger moves the issue back into testing, where the merger's ask is owed by
// nobody: the implementer is asked in testing, for R0's reason with no rules read at the move, and
// the tester is held.
func TestTheMergersMoveBackAsksTheRoleOfThePhaseItEnters(t *testing.T) {
	pool := seedConflictIssue(t, phase.Merging, "retro", settledGreen("lint"))
	confirmsConflict(t, pool, &record.BaseRules{Required: []string{"ci"}})
	applyRefusingNothing(t, pool, readsAfter(2*time.Minute), intake.BackwardMove{Issue: "LEGION-208", Requester: claim.RoleMerger,
		To: phase.Testing, Reason: "the merge needs a test of the base's change"})
	if got := issuePhase(t, pool); got != phase.Testing {
		t.Fatalf("after the merger's move back the issue is in %s, want testing", got)
	}
	why := record.ConflictRulesUnread
	want := []record.MergeForwardMessage{mergerAsked(), implementerAsked(phase.Testing, why), heldIn(claim.RoleTester, phase.Testing, why)}
	if got := mergeForwardMessages(t, pool); !reflect.DeepEqual(got, want) {
		t.Errorf("the merge-forward messages are %+v, want %+v: the implementer is asked in testing and the tester is held", got, want)
	}
	if mf := recordedPR(t, pool, "LEGION-208").MergeForward; mf == nil || mf.Asked() != claim.RoleImplementer || mf.Phase != phase.Testing {
		t.Errorf("the record's merge forward = %+v, want the implementer's ask in testing", mf)
	}
}

// On the default steps the implementer is asked in testing and the tester is held. The
// implementer's claim fails: the merge is still owed to it, so the tester stays held, the
// implementer's ask is not delivered while its claim has ended, and the architect is told once,
// in the worker-died notice its failure sends. A merge that lands then tells the tester the new
// head.
func TestAMergeOwedToARoleWhoseClaimEndedStillHoldsTheWorker(t *testing.T) {
	pool := seedConflictIssue(t, phase.Testing, "testing", settledGreen("lint"))
	confirmsConflict(t, pool, &record.BaseRules{Required: []string{"ci"}})
	applyRefusingNothing(t, pool, readsAfter(2*time.Minute), intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleImplementer})
	issue, pr := storedIssue(t, pool, "LEGION-208"), recordedPR(t, pool, "LEGION-208")
	if issue.Phase != phase.Testing || pr.MergeForward == nil {
		t.Fatalf("after the implementer's claim failed the issue is in %s and the ask is %+v, want testing and the ask", issue.Phase, pr.MergeForward)
	}
	if !pr.MergeForward.Waits {
		t.Errorf("after the implementer's claim failed the ask is %+v, want it waiting for the implementer's next start", pr.MergeForward)
	}
	if worker, held := heldWorker(issue, pr); !held || worker != claim.RoleTester {
		t.Errorf("the fence's held rule names %q (held %t), want the tester: the merge is still owed", worker, held)
	}
	if why := leftMergeForwardAt(issue, false, &pr, claim.RoleImplementer, record.MergeForwardAsk, conflictHead); why == "" {
		t.Errorf("the implementer's ask is delivered while its claim has ended, want it to wait")
	}
	// The notice is queued on the issue's row, which the outbox routes to the architect that owns
	// the issue, as every notice of the issue is routed; the issue is not held, so no held notice
	// reaches the controller.
	if died := noticesOfKind(t, pool, "LEGION-208", "worker-died"); len(died) != 1 || died[0].Role != claim.RoleImplementer {
		t.Errorf("the worker-died notices of LEGION-208 are %+v, want one, naming the implementer's failure", died)
	}
	if held := noticesOfKind(t, pool, "LEGION-208", "held"); len(held) != 0 {
		t.Errorf("the held notices of LEGION-208 are %+v, want none: the implementer does not work testing", held)
	}
	asked := len(mergeForwardMessages(t, pool))
	landsMergeForward(t, pool)
	if got := toldWho(mergeForwardMessages(t, pool)[asked:]); !reflect.DeepEqual(got, []string{"tester landed"}) {
		t.Errorf("the landing queued %v, want the tester told the new head", got)
	}
}

// On the default steps the merger is asked in merging and its claim fails, which holds the issue:
// the ask waits, undelivered. The architect's retry starts the merger again, and the ask is sent
// to it again, delivered once more.
func TestAnAskThatWaitedIsSentAgainAtItsRolesNextStart(t *testing.T) {
	pool := seedConflictIssue(t, phase.Merging, "retro", settledGreen("lint"))
	confirmsConflict(t, pool, &record.BaseRules{Required: []string{"ci"}})
	applyRefusingNothing(t, pool, readsAfter(2*time.Minute), intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleMerger})
	issue, pr := storedIssue(t, pool, "LEGION-208"), recordedPR(t, pool, "LEGION-208")
	if issue.Phase != phase.Held || pr.MergeForward == nil || !pr.MergeForward.Waits {
		t.Fatalf("after the merger's claim failed the issue is in %s and the ask is %+v, want held and the ask waiting", issue.Phase, pr.MergeForward)
	}
	if why := leftMergeForwardAt(issue, false, &pr, claim.RoleMerger, record.MergeForwardAsk, conflictHead); why == "" {
		t.Errorf("the merger's ask is delivered while its claim has ended, want it to wait")
	}
	applyRefusingNothing(t, pool, readsAfter(3*time.Minute), intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision, Reason: "go on"})
	if got := mergeForwardMessages(t, pool); !reflect.DeepEqual(got, []record.MergeForwardMessage{mergerAsked(), mergerAsked()}) {
		t.Errorf("after the retry the merge-forward messages are %+v, want the merger's ask sent again", got)
	}
	issue, pr = storedIssue(t, pool, "LEGION-208"), recordedPR(t, pool, "LEGION-208")
	if why := leftMergeForwardAt(issue, false, &pr, claim.RoleMerger, record.MergeForwardAsk, conflictHead); why != "" || pr.MergeForward.Waits {
		t.Errorf("after the retry the merger's ask is undelivered (%q) with the record's ask %+v, want it delivered and waiting no more", why, pr.MergeForward)
	}
}

// On the default steps the tester moves back into planning, which stands before the implementer,
// while the implementer's ask made in testing is owed: the planner is held, a merge that lands
// tells it the new head, and its completion at the head the merge replaced is refused.
func TestAPlannerBeforeTheImplementerIsToldAndFencedWhileTheMergeIsOwed(t *testing.T) {
	pool := seedConflictIssue(t, phase.Testing, "testing", settledGreen("lint"))
	confirmsConflict(t, pool, &record.BaseRules{Required: []string{"ci"}})
	applyRefusingNothing(t, pool, readsAfter(2*time.Minute), intake.BackwardMove{Issue: "LEGION-208", Requester: claim.RoleTester,
		To: phase.Planning, Reason: "the plan misses a case"})
	why := record.ConflictCheckNotGreen
	asked := mergeForwardMessages(t, pool)
	if want := []record.MergeForwardMessage{implementerAsked(phase.Testing, why), heldIn(claim.RoleTester, phase.Testing, why),
		heldIn(claim.RolePlanner, phase.Planning, why)}; !reflect.DeepEqual(asked, want) {
		t.Errorf("after the move back the merge-forward messages are %+v, want %+v", asked, want)
	}
	landsMergeForward(t, pool)
	if got := toldWho(mergeForwardMessages(t, pool)[len(asked):]); !reflect.DeepEqual(got, []string{"planner landed"}) {
		t.Errorf("the landing in planning queued %v, want the planner told the new head", got)
	}
	result := applyFacts(t, pool, readsAfter(4*time.Minute))("the planner at the replaced head", intake.HandoffComplete{Generation: 1,
		Issue: "LEGION-208", Role: claim.RolePlanner, Claim: "planner-claim", Summary: "planned", Commit: conflictHead,
		HandoffRecord: "plan-1", HandoffVersion: 1})
	if code := refusalCode(result); code != "HANDOFF_HEAD_MERGED_FORWARD" {
		t.Errorf("the planner's completion at %s, the head the merge replaced, was answered %q, want HANDOFF_HEAD_MERGED_FORWARD", conflictHead, code)
	}
}
