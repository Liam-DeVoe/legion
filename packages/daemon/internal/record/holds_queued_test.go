package record

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// A start queued for a role at a phase is the work of the place the issue stands at while a running
// step of that role, not held, stands at that phase (Issue.HoldsQueued). Two tasks of a plan run at
// one time, each at its own phase, and the issue's phase is one of them: the start of the other
// task's step is held all the same, which the four judges of queued work then send. A held step's
// start, and the start of a role at no running step, are not. An issue of one task and one with no
// plan rows read as they did when the rule compared the issue's phase: its one running step stands
// at that phase.
//
// The fault: a start held to the issue's one phase is dropped for each task that stands at another
// (the outbox's "serves a phase or a step the issue has left"), so the second task's step never
// starts. The control is the rule that compares the issue's phase, under which the first case
// fails.
func TestAQueuedStartIsHeldToTheRunningStepOfItsRole(t *testing.T) {
	step := func(task string, position int, role claim.Role, at phase.Phase, held bool) RunningStep {
		return RunningStep{Task: task, Place: TaskPlace{Kind: PlaceStep, Position: position}, Role: role, Phase: at, Agent: claim.Agent(role), Held: held}
	}
	twoTasks := Issue{Key: "LEGION-208", Phase: phase.Implementing, Running: []RunningStep{
		step("a", 2, claim.RoleReviewer, phase.Reviewing, false), step("b", 2, claim.RoleImplementer, phase.Implementing, false)}}
	oneHeld := Issue{Key: "LEGION-208", Phase: phase.Implementing, Running: []RunningStep{
		step("a", 2, claim.RoleReviewer, phase.Reviewing, true), step("b", 2, claim.RoleImplementer, phase.Implementing, false)}}
	oneTask := Issue{Key: "LEGION-208", Phase: phase.Reviewing, Running: []RunningStep{step("a", 2, claim.RoleReviewer, phase.Reviewing, false)}}
	unplanned := Issue{Key: "LEGION-208", Phase: phase.Testing}
	unplanned.Running = RunningOf(unplanned, false, nil)
	heldUnplanned := Issue{Key: "LEGION-208", Phase: phase.Held, Hold: &Hold{From: phase.Testing}}
	heldUnplanned.Running = RunningOf(heldUnplanned, false, nil)
	steps := Issue{Key: "LEGION-208", Phase: phase.Step, Running: []RunningStep{step("a", 1, claim.Role("security"), phase.Step, false)}}
	for _, c := range []struct {
		name   string
		issue  Issue
		role   claim.Role
		queued phase.Phase
		want   bool
	}{
		{"the step of a task at another phase than the issue's", twoTasks, claim.RoleReviewer, phase.Reviewing, true},
		{"the step of the task at the issue's phase", twoTasks, claim.RoleImplementer, phase.Implementing, true},
		{"a role at no running step", twoTasks, claim.RoleTester, phase.Testing, false},
		{"a role queued for a phase its step is not at", twoTasks, claim.RoleImplementer, phase.Reviewing, false},
		{"a held step of the role", oneHeld, claim.RoleReviewer, phase.Reviewing, false},
		{"the other task beside the held step", oneHeld, claim.RoleImplementer, phase.Implementing, true},
		{"the one task's step", oneTask, claim.RoleReviewer, phase.Reviewing, true},
		{"a phase the one task has left", oneTask, claim.RoleImplementer, phase.Implementing, false},
		{"an issue with no plan rows at its phase", unplanned, claim.RoleTester, phase.Testing, true},
		{"an issue with no plan rows past a phase", unplanned, claim.RoleImplementer, phase.Implementing, false},
		{"an issue with no plan rows held", heldUnplanned, claim.RoleTester, phase.Testing, false},
		{"the step of a role of the deployment", steps, claim.Role("security"), phase.Step, true},
		{"the step of another role of the deployment", steps, claim.Role("audit"), phase.Step, false},
	} {
		if got := c.issue.HoldsQueued(c.role, c.queued); got != c.want {
			t.Errorf("%s: HoldsQueued(%s, %s) of an issue at %s running %+v = %v, want %v", c.name, c.role, c.queued, c.issue.Phase, c.issue.Running, got, c.want)
		}
	}
}
