package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
)

// A phase reminder is armed in the claim of the worker of the issue's open phase that serves the
// issue's current generation (idlePhaseOpen): the role of the claim that works the task's place,
// which the idle view reads with the issue (pods.IdleView.PlaceRole), none at a place the owner
// does, and at step the role of the running step (pods.IdleView.StepRole), which no phase names.
// The generation is held for both: a checker serving an earlier run is not the worker of this one.
// Where the view reads no place's role, as in these views, a phase of Legion's names the worker,
// as before.
func TestAPhaseReminderIsArmedForTheDeploymentRoleThatRuns(t *testing.T) {
	const checker claim.Role = "checker"
	atStep := pods.IdleView{Phase: phase.Step, StepRole: checker, Generation: 3}
	atTesting := pods.IdleView{Phase: phase.Testing, StepRole: claim.RoleTester, Generation: 3}
	for _, c := range []struct {
		name  string
		view  pods.IdleView
		claim pods.IdleClaim
		want  bool
	}{
		{"the checker at step, serving the current run", atStep, pods.IdleClaim{Role: checker, ServingGeneration: 3}, true},
		{"the checker at step, serving an earlier run", atStep, pods.IdleClaim{Role: checker, ServingGeneration: 2}, false},
		{"the implementer at step", atStep, pods.IdleClaim{Role: claim.RoleImplementer, ServingGeneration: 3}, false},
		{"the tester at testing, serving the current run", atTesting, pods.IdleClaim{Role: claim.RoleTester, ServingGeneration: 3}, true},
	} {
		if got := idlePhaseOpen(c.view, c.claim); got != c.want {
			t.Errorf("%s: idlePhaseOpen = %t, want %t", c.name, got, c.want)
		}
	}
}
