package daemon

import (
	"context"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
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
