package daemon

import (
	"context"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/stream"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// The record of a step names its agent's session and the model that answered it, and the daemon
// reads both off the claim's machine where the engine writes the record (workflow.Config.StepAgent):
// the session the machine published, and the model that answered last in that session. Two claims
// of one issue each read as their own, so a record of one never names the other's session or
// model. A fact the boot applies before attach reads nothing, and so does a claim this daemon does
// not supervise.
func TestTheDaemonReadsAStepsAgentFromItsClaimsMachine(t *testing.T) {
	ctx := context.Background()
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	w := &workflowRuntime{log: quietLogger()}
	tester := mustClaimToken(t, "LEGION-208", claim.RoleTester)
	reviewer := mustClaimToken(t, "LEGION-208", claim.RoleReviewer)
	if got := w.stepAgent(tester); got != (workflow.StepAgent{}) {
		t.Fatalf("the agent read before attach = %+v, want nothing", got)
	}
	w.attach(&supervision{supervisor: sup, log: quietLogger()})
	supervised := func(token claim.Token, role claim.Role, session string) *supervise.Machine {
		t.Helper()
		machine, _, err := sup.Create(ctx, supervise.Claim{Token: token, Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208",
			Role: role, State: supervise.StateWorking, Session: session}, "")
		if err != nil {
			t.Fatalf("create the %s's claim: %v", role, err)
		}
		return machine
	}
	answered := func(machine *supervise.Machine, token claim.Token, model string) {
		t.Helper()
		if err := machine.Handle(ctx, supervise.StreamAnswered{Claim: token, Model: model}); err != nil {
			t.Fatalf("the answer of %s: %v", token, err)
		}
	}
	testerMachine := supervised(tester, claim.RoleTester, "ses_tester")
	if got := w.stepAgent(tester); got != (workflow.StepAgent{Session: "ses_tester"}) {
		t.Fatalf("the agent read before any answer = %+v, want its session and no model", got)
	}
	reviewerMachine := supervised(reviewer, claim.RoleReviewer, "ses_reviewer")
	answered(testerMachine, tester, "second/tester-model")
	answered(reviewerMachine, reviewer, "second/reviewer-model")
	for token, want := range map[claim.Token]workflow.StepAgent{
		tester:   {Session: "ses_tester", Model: "second/tester-model"},
		reviewer: {Session: "ses_reviewer", Model: "second/reviewer-model"},
	} {
		if got := w.stepAgent(token); got != want {
			t.Errorf("the agent read of %s after both answered = %+v, want its own session and model %+v", token, got, want)
		}
	}
	if got := w.stepAgent(mustClaimToken(t, "LEGION-208", claim.RoleImplementer)); got != (workflow.StepAgent{}) {
		t.Fatalf("the agent read of a claim this daemon does not supervise = %+v, want nothing", got)
	}
}

// An answer whose model no store can keep reaches the daemon as an Answered that names no model
// (stream.Conn's answeredBy), and the daemon reads it as the claim's last answer like any other: the
// record of the step names the session and no model, never the model that answered before it.
func TestAnAnswerWithNoModelLeavesTheStepWithNone(t *testing.T) {
	ctx := context.Background()
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	w := &workflowRuntime{log: quietLogger()}
	w.attach(&supervision{supervisor: sup, log: quietLogger()})
	planner := mustClaimToken(t, "LEGION-208", claim.RolePlanner)
	const generation = 3
	machine, _, err := sup.Create(ctx, supervise.Claim{Token: planner, Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208",
		Role: claim.RolePlanner, State: supervise.StateWorking, Session: "ses_planner", Generation: generation}, "")
	if err != nil {
		t.Fatalf("create the planner's claim: %v", err)
	}
	answered := func(ev stream.Answered) {
		t.Helper()
		mapped, err := superviseEvent(ev)
		if err != nil {
			t.Fatalf("map %#v: %v", ev, err)
		}
		if err := machine.Handle(ctx, mapped); err != nil {
			t.Fatalf("the answer %#v: %v", ev, err)
		}
	}
	answered(stream.Answered{Claim: planner, Provider: "first", Model: "first-model", Generation: generation})
	if got := w.stepAgent(planner); got != (workflow.StepAgent{Session: "ses_planner", Model: "first/first-model"}) {
		t.Fatalf("the agent read after the first answer = %+v, want its session and first/first-model", got)
	}
	answered(stream.Answered{Claim: planner, Generation: generation})
	if got := w.stepAgent(planner); got != (workflow.StepAgent{Session: "ses_planner"}) {
		t.Fatalf("the agent read after an answer naming no model = %+v, want its session and no model: first/first-model did not answer last", got)
	}
}
