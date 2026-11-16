package admit

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// The first implementing round after a wait on the children asks the implementer to merge the
// default branch forward and to end its forward-merge comment with the full-round line
// (dispatch://INFRA-3320 R12). That round is the round the implementer works, not the first
// start row the return writes: when the implementer's claim fails before it takes that start,
// the daemon holds the issue for it, and the architect's retry of the held implementing starts
// the round again. The retried task still asks for the forward merge, since nothing has merged
// the children's work into the branch yet, and the tester reads the line to run a full round.
func TestTheReturnsForwardMergeSurvivesAFailedImplementerAndTheRetry(t *testing.T) {
	failed := intake.HandoffComplete{Generation: 1, Issue: spParent, Role: claim.RoleTester, Claim: "tester",
		Summary: "the empty list panics", Verdict: "fail", Commit: "head-42", HandoffRecord: "test-1", HandoffVersion: 1}
	pool, admission, _ := pnMovedParent(t, phase.Testing, nil, "api", failed, phase.Implementing, "the tester completed testing")

	back := pnChildDone(t, pool, admission)
	if back.Phase != phase.Implementing {
		t.Fatalf("the parent after its child's close = phase %s, want implementing", back.Phase)
	}
	first := pnWorkerStart(t, pool, spParent, claim.RoleImplementer)
	if !strings.Contains(first.Task, pnForwardMerge) || !strings.Contains(first.Task, spMergeLine) {
		t.Fatalf("the return's implementer task = %q, want the forward-merge request", first.Task)
	}

	// The implementer's claim fails before it takes the return's start: the daemon holds the
	// issue for it, from implementing.
	pwApplied(t, pool, admission, "supervisor", "implementer-failed", intake.ClaimFailed{Issue: spParent, Role: claim.RoleImplementer,
		Reason: "the launch budget ran out"})
	if got := issue(t, pool, spParent); got.Phase != phase.Held || got.Hold == nil || got.Hold.From != phase.Implementing {
		t.Fatalf("the parent after its implementer's claim failed = phase %s, hold %+v; want held from implementing", got.Phase, got.Hold)
	}

	// The architect retries the held implementing. No open child stands under the parent, so the
	// retry starts the implementer, and its task is the first implementing round after the wait.
	pwApplied(t, pool, admission, "api", "retry", intake.RetryOrEscalate{Issue: spParent, Decision: intake.RetryDecision,
		Reason: "the runner is back"})
	got := issue(t, pool, spParent)
	if got.Phase != phase.Implementing {
		t.Fatalf("the parent after the retry = phase %s, want implementing", got.Phase)
	}
	retried := pnWorkerStart(t, pool, spParent, claim.RoleImplementer)
	if retried.Task == first.Task {
		t.Fatalf("the retry wrote no implementer start of its own; the newest is still the return's %q", first.Task)
	}
	for _, want := range []string{"Reason: the runner is back", pnForwardMerge, spMergeLine} {
		if !strings.Contains(retried.Task, want) {
			t.Fatalf("the retried implementer's task = %q, want it to carry %q: no implementer has been asked to merge %s's children forward yet "+
				"(record forward merge owed %t)", retried.Task, want, spParent, got.ForwardMergeOwed)
		}
	}
}
