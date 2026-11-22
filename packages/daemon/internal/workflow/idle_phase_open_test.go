package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
)

// A phase reminder is armed in the claim of the worker of the issue's open phase that serves the
// issue's current generation (idlePhaseOpen), and at step that worker is the role of the running
// step, which the idle view reads with the issue (pods.IdleView.StepRole) and no phase names. The
// generation is held for both: a checker serving an earlier run is not the worker of this one. At
// a phase of Legion's the phase names the worker, as before.
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

// Two claims of one issue can hold the tester's role: key a's agent, which the tester's phases
// row names on its step (pods.IdleView.Workers), and the tester's own claim. Only the claim the
// row names works the phase, so a reminder due on the tester's own claim holds no pod
// (IdleHoldReminder); a role whose row names no claim works its phase in each claim of its role.
func TestAPhaseReminderIsArmedOnlyForTheClaimTheStepNames(t *testing.T) {
	const keyed, own = claim.Token("legion-legion-legion-209-implementer"), claim.Token("legion-legion-legion-209-tester")
	named := pods.IdleView{Phase: phase.Testing, Generation: 3, Workers: map[claim.Role]claim.Token{claim.RoleTester: keyed}}
	unnamed := pods.IdleView{Phase: phase.Testing, Generation: 3}
	for _, c := range []struct {
		name  string
		view  pods.IdleView
		claim pods.IdleClaim
		want  bool
	}{
		{"key a's agent, which the row names", named, pods.IdleClaim{Token: keyed, Role: claim.RoleTester, ServingGeneration: 3}, true},
		{"the tester's own claim, which works no step", named, pods.IdleClaim{Token: own, Role: claim.RoleTester, ServingGeneration: 3}, false},
		{"the tester's own claim where the row names none", unnamed, pods.IdleClaim{Token: own, Role: claim.RoleTester, ServingGeneration: 3}, true},
	} {
		if got := idlePhaseOpen(c.view, c.claim); got != c.want {
			t.Errorf("%s: idlePhaseOpen = %t, want %t", c.name, got, c.want)
		}
	}
}
