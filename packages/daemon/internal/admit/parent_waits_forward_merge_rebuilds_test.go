package admit

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
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
