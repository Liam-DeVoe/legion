package admit

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The first implementing round after a wait on the children asks the implementer to merge the
// default branch forward and to end its forward-merge comment with the full-round line
// (dispatch://INFRA-3320 R12). The round can be started again before it is done, and every task
// that starts it again asks for the merge, once, until the implementer completes that round
// (workflow's settleForwardMerge) or the pull request merges. The failed claim and the
// architect's retry are TestTheReturnsForwardMergeSurvivesAFailedImplementerAndTheRetry; the
// cases here are the other ways the round is started again, and its end.

// fmReturned builds a parent back from awaiting_children: its tester failed its head while an open
// child held the start of implementing that fail raised, and the child's close took the parent
// back to implementing, whose first implementer task asks for the forward merge once. It answers
// the pool, the admission and that first implementer start.
func fmReturned(t *testing.T) (*pgxpool.Pool, *Admission, record.SuperviseRequest) {
	t.Helper()
	failed := intake.HandoffComplete{Generation: 1, Issue: spParent, Role: claim.RoleTester, Claim: "tester",
		Summary: "the empty list panics", Verdict: "fail", Commit: "head-42", HandoffRecord: "test-1", HandoffVersion: 1}
	pool, admission, _ := pnMovedParent(t, phase.Testing, nil, "api", failed, phase.Implementing, "the tester completed testing")
	if back := pnChildDone(t, pool, admission); back.Phase != phase.Implementing {
		t.Fatalf("the parent after its child's close = phase %s, want implementing", back.Phase)
	}
	first := pnWorkerStart(t, pool, spParent, claim.RoleImplementer)
	fmAsksOnce(t, "the return's implementer task", first.Task)
	return pool, admission, first
}

// fmAsksOnce fails unless task asks for the forward merge and names the full-round line once.
func fmAsksOnce(t *testing.T, what, task string) {
	t.Helper()
	if !strings.Contains(task, pnForwardMerge) || strings.Count(task, spMergeLine) != 1 {
		t.Fatalf("%s = %q, want it to ask for the forward merge once, ending with the line %s", what, task, spMergeLine)
	}
}

// The operator's `legion claims retry --fresh-session` of the implementer the return started
// restarts its phase through intake.RestartPhase, the ask for a new session riding the fact (api's
// restartWorkedPhase, for a claim that failed or an operator stopped, and for one that comes back
// in a new session owing no task). The new session has read none of the return's task, and no
// implementer has merged the children's work forward yet, so the restarted task asks for the
// merge, once, and the record still owes it.
func TestAFreshSessionRetryOfTheReturnsImplementerStillAsksForTheForwardMerge(t *testing.T) {
	pool, admission, first := fmReturned(t)

	pwApplied(t, pool, admission, "api", "fresh-session", intake.RestartPhase{Issue: spParent, Role: claim.RoleImplementer, FreshSession: true})

	restarted := pnWorkerStart(t, pool, spParent, claim.RoleImplementer)
	if restarted.Task == first.Task || !restarted.FreshSession || !restarted.ResendEntry || restarted.Entry != first.Entry {
		t.Fatalf("the newest implementer start = %+v, want the restart's own, in a new session, resending the return's entry %d", restarted, first.Entry)
	}
	if !strings.Contains(restarted.Task, "Reason: the operator retried this phase's worker") {
		t.Fatalf("the restarted implementer's task = %q, want it to name the operator's retry", restarted.Task)
	}
	fmAsksOnce(t, "the restarted implementer's task", restarted.Task)
	if got := issue(t, pool, spParent); got.Phase != phase.Implementing || !got.ForwardMergeOwed {
		t.Fatalf("the parent after the restart = phase %s, merge owed %t; want implementing, the merge still owed", got.Phase, got.ForwardMergeOwed)
	}
}

// A member of a tree waited on its own child and came back to implementing, and its start waited
// for the slot the return's line gave it. Before that start reached the implementer, a person
// moved the tree's root to the backlog: the tree lingers, and the same fact's pass finishes the
// member's start undelivered (finishFencedStarts). The person's todo then re-admits the tree, and
// its promotion starts the member's implementer again on the resume task of its phase
// (startMidPhaseMembers, workflow.Engine.StartPromotedWorker), which raises the entry the
// re-admission cleared the task of. No implementer has merged the children's work forward yet, so
// the promoted task asks for the merge, once, and the record still owes it.
func TestAReadmittedTreesPromotedImplementerStillAsksForTheForwardMerge(t *testing.T) {
	const root, member, grandchild = "LEGION-208", "LEGION-209", "LEGION-210"
	pool := migratedPool(t)
	admission := pnAdmission(t, pool, 2)
	spWaiting(t, pool, root, phase.Implementing, "Continue "+root+". Issue: "+root+". Phase: implementing.",
		"a review asked for changes, while these Dispatch children of "+root+" were open: "+member+" (in_progress)", nil)
	parent, waited := root, fixedNow.Add(-2*time.Hour)
	held := record.ChildrenWait{Start: phase.Implementing, Task: "Continue " + member + ". Issue: " + member + ". Phase: implementing.",
		Reason: "a review asked for changes, while these Dispatch children of " + member + " were open: " + grandchild + " (todo)", Since: waited}
	putIssue(t, pool, record.Issue{Key: member, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: testProject,
		Title: member, Tree: root, Parent: &parent, Phase: phase.AwaitingChildren, Generation: 1, Status: "in_progress",
		DispatchStatus: "in_progress", Rank: "B", LastDispatchSeq: 1, ChildrenWait: &held, ForwardMergeOwed: true, ChildrenWaitedAt: &waited})
	pwPullRequest(t, pool, member, record.PullRequestOpen)
	seedMirrorChild(t, pool, member, "in_progress", root)
	seedMirrorChild(t, pool, grandchild, "todo", member)
	putWorkerClaim(t, pool, root, member, claim.RoleImplementer, 1, supervise.StateSuspended, supervise.SuspendedByDaemon)

	spMirror(t, pool, grandchild, "done", member, 2)
	spPass(t, pool, admission)
	if back := issue(t, pool, member); back.Phase != phase.Implementing {
		t.Fatalf("%s after its child is done = phase %s, want implementing", member, back.Phase)
	}
	fmAsksOnce(t, "the return's implementer task", pnWorkerStart(t, pool, member, claim.RoleImplementer).Task)

	apply(t, pool, admission, "moved-to-backlog", intake.DispatchIssue{Key: root, ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated",
		Status: "backlog", Title: root, Rank: root, HandedOver: true}, pnLingering())
	if got := issue(t, pool, root); got.Phase != phase.Done || !got.Lingers() {
		t.Fatalf("the root on the backlog = phase %s, linger %v; want done, lingering", got.Phase, got.LingerUntil)
	}
	if starts := pwWorkerStarts(t, pool, member); len(starts) != 0 {
		t.Fatalf("the starts of %s after the move to the backlog = %+v, want none: the pass finishes a start of a lingering tree undelivered", member, starts)
	}

	apply(t, pool, admission, "readmitted", intake.DispatchIssue{Key: root, ResolvedRepo: "acme/widgets", Seq: 3, Type: "issue.updated",
		Status: "todo", Title: root, Rank: root, HandedOver: true}, pnLingering())

	if got := issue(t, pool, root); got.Generation != 2 || got.Phase != phase.Admitted {
		t.Fatalf("the re-admitted root = generation %d, phase %s; want generation 2 at admitted", got.Generation, got.Phase)
	}
	promoted := pnWorkerStart(t, pool, member, claim.RoleImplementer)
	if !promoted.ResumeTask || promoted.Phase != phase.Implementing || !strings.Contains(promoted.Task, "Resume the existing phase work.") {
		t.Fatalf("the newest implementer start of %s = %+v, want the promotion's, on implementing's resume task", member, promoted)
	}
	fmAsksOnce(t, "the promoted implementer's task", promoted.Task)
	if got := issue(t, pool, member); got.Phase != phase.Implementing || !got.ForwardMergeOwed {
		t.Fatalf("%s after the re-admission = phase %s, merge owed %t; want implementing, the merge still owed", member, got.Phase, got.ForwardMergeOwed)
	}
}

// The implementer's completion of the first implementing round after the wait is what settles
// the forward merge: the record owes it through that round, and owes none once the implementer
// completes, so the next implementing round, after the tester fails the merged head, asks for no
// forward merge and no full test round.
func TestTheImplementersCompletionSettlesTheForwardMerge(t *testing.T) {
	pool, admission, _ := fmReturned(t)
	if got := issue(t, pool, spParent); !got.ForwardMergeOwed {
		t.Fatalf("the parent after the return = merge owed %t, want it owed until its implementer completes the round", got.ForwardMergeOwed)
	}

	pwApplied(t, pool, admission, "api", "implemented", intake.HandoffComplete{Generation: 1, Issue: spParent, Role: claim.RoleImplementer,
		Claim: "implementer", Summary: "merged main forward", Commit: "head-43", HandoffRecord: "implement-2", HandoffVersion: 2})
	if got := issue(t, pool, spParent); got.Phase != phase.Testing || got.ForwardMergeOwed {
		t.Fatalf("the parent after its implementer's completion = phase %s, merge owed %t; want testing, nothing owed", got.Phase, got.ForwardMergeOwed)
	}

	pwApplied(t, pool, admission, "api", "failed-again", intake.HandoffComplete{Generation: 1, Issue: spParent, Role: claim.RoleTester,
		Claim: "tester", Summary: "the empty list still panics", Verdict: "fail", Commit: "head-43", HandoffRecord: "test-2", HandoffVersion: 2})
	if got := issue(t, pool, spParent); got.Phase != phase.Implementing || got.ForwardMergeOwed {
		t.Fatalf("the parent after the tester's second fail = phase %s, merge owed %t; want implementing, nothing owed", got.Phase, got.ForwardMergeOwed)
	}
	next := pnWorkerStart(t, pool, spParent, claim.RoleImplementer).Task
	if !strings.Contains(next, "Tester's verdict: fail.") || strings.Contains(next, pnForwardMerge) || strings.Contains(next, spMergeLine) {
		t.Fatalf("the implementer's task after the tester's second fail = %q, want the tester's fail and no forward-merge request", next)
	}
}
