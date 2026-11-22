// The whole daemon runs a role an owner's plan defines with a role block: the plan's prompt, the
// plan's model and a step record of the role's type. An issue that ran before a plan could define a
// role goes on.
//
// The first test boots the whole daemon over the rig of the deployment's roles
// (newDeploymentRolesRig, deployment_roles_test.go), whose settings here define no role and list
// the model prober-model: the real store, the real outbox and the real Dispatch of this package's
// tests, each role's agent over the worker stream, the owner's plan given with register_plan.
// GitHub's REST API and the panes are stood in for, each pane answering with the model its launch's
// spec named. The second boots it over the rig of an earlier release's store (newSliceFourRig,
// deployment_roles_test.go), stopped below the migration of the roles a plan defines.
package daemon

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

const (
	// planProber is the role the owner's plan defines, of the type testing, with planProberModel,
	// the one model the deployment lists, and planProberPrompt, the block's text.
	planProber       claim.Role = "prober"
	planProberModel             = "prober-model"
	planProberPrompt            = "# Prober\n\nProbe the change."
)

// planRolesMigration ends the name of the migration of the roles a plan defines (plan_roles). It is
// found by its name rather than its number, as deploymentRolesMigration is.
const planRolesMigration = "_plan_roles.up.sql"

// planWithProber is the text of a plan.md whose task a names `implementer prober`
// (deploymentPlanOf), after a role block of prober of the type testing on model, or with no role
// block where block is false.
func planWithProber(model string, block bool) string {
	text := deploymentPlanOf("implementer", string(planProber))
	if !block {
		return text
	}
	return ":::plan_role{#prober type=\"testing\" model=\"" + model + "\"}\n" + planProberPrompt + "\n:::\n\n" + text
}

// v-own-role. The deployment lists prober-model; the owner's plan defines prober (type testing,
// that model, a prompt) and task a with `implementer prober`. The prober starts after the
// implementer's step passed, with the plan's prompt between Legion's parts and the plan's model; its
// task holds the task block's text and the implementer's report; the read of the issue's steps
// names step 2 prober, type testing, model prober-model, the model that answered. A plan that gives
// prober another model is refused, naming the model and the list.
//
// Control (the check's own): the plan without its role block is refused, naming prober, so no plan
// is in force and the issue's gate opens at planning, which the walk's assertion fails on.
func TestARoleThePlanDefinesRunsWithItsPromptAndModel(t *testing.T) {
	proof := newLiveProofOn(t, workflowNATS(t))
	proof.cfg.Models = []string{planProberModel}
	r := newDeploymentRolesRig(t, proof, startScratchDispatchOn(t, scratchDispatchOptions{}), nil,
		[]string{"Prober probes the change", "Other model is refused", "No role block brings planner"})
	runs, otherModel, noBlock := r.issues[0].key, r.issues[1].key, r.issues[2].key

	r.on(runs)
	if status, body := r.registerPlan(r.document("plan.md", planWithProber(planProberModel, true)), 1); status != http.StatusOK {
		t.Fatalf("register_plan of %s's plan with the role block prober = %d %s, want 200", runs, status, body)
	}
	if err := r.walkImplementerProber(); err != nil {
		t.Fatal(err)
	}
	waitDeploymentSteps(r.stepRecordsRig, "a", true, []deploymentStepWant{
		{kind: "step", step: 1, role: "implementer", typ: "implementation", model: stepRecordsModel},
		{kind: "step", step: 2, role: string(planProber), typ: "testing", model: planProberModel},
		{kind: "retro", role: "implementer"},
		{kind: "merge", role: "merger"},
	})

	r.on(otherModel)
	status, body := r.registerPlan(r.document("plan.md", planWithProber("other-model", true)), 1)
	want := "step 2 of task a names the role prober, whose model other-model the deployment does not list; it lists prober-model."
	if status != http.StatusConflict || !strings.Contains(body, "PLAN_REFUSED") || !strings.Contains(body, want) {
		t.Fatalf("register_plan of prober on other-model = %d %s, want 409 PLAN_REFUSED saying %q", status, body, want)
	}

	t.Run("control: the plan without its role block", func(t *testing.T) {
		r.on(noBlock)
		status, body := r.registerPlan(r.document("plan.md", planWithProber("", false)), 1)
		want := "step 2 of task a names the role prober, which neither the deployment nor the plan defines."
		if status != http.StatusConflict || !strings.Contains(body, "PLAN_REFUSED") || !strings.Contains(body, want) {
			t.Fatalf("register_plan of `implementer prober` with no role block = %d %s, want 409 PLAN_REFUSED saying %q", status, body, want)
		}
		err := r.walkImplementerProber()
		if want := noBlock + "'s gate opened and its first step is planning"; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the walk's assertion on %s, whose plan was refused, says %v; want it to say %q", noBlock, err, want)
		}
	})
}

// R20: an issue that ran before a plan could define a role goes on under this release. The store
// holds every migration below plan_roles, applied by hand (storeOfSliceFour), and the record of one
// issue whose owner's plan `implementer tester reviewer` stands at step 2, the implementer's step
// done and the tester's claim a launch the earlier daemon began (seedSliceFourIssue). This release's
// migrations then run on it, plan_roles among them, as a deploy runs them, and the whole daemon
// boots on it. The tester's pass moves task a to step 3, the reviewer starts with the task of step 3
// of 3, and plan_roles holds no row of the issue: the plan of the release before stays a plan that
// defines no role.
//
// The fault: a daemon that needs a plan_roles row of the plan in force could not read the issue's
// plan, since that plan has none, and the issue would not go on. The control is a mutation of the
// release's code, run by hand: record.Postgres.Plan failing where the generation's plan has no
// plan_roles row; the daemon's boot then fails at its read of the issue's plan, and the test fails
// before the tester's pass.
func TestAnIssueThatRanBeforePlanRolesGoesOn(t *testing.T) {
	r := newSliceFourRig(t, planRolesMigration)
	r.work(claim.RoleTester, "test", testCompletion, phase.Reviewing)
	if kind, position := r.taskPlace(); kind != "step" || position != 3 {
		t.Fatalf("after the tester's pass %s's task a stands at %s %d, want step 3", r.key, kind, position)
	}
	reviewer := r.worker(claim.RoleReviewer)
	if task := reviewer.take().Message; !strings.Contains(task, " Task: a. Step 3 of 3: reviewer.") {
		t.Fatalf("the reviewer's task is %q, want it to stand at step 3 of 3 of task a", task)
	}
	reviewer.endTurn()
	var roles int
	if err := r.st.Pool().QueryRow(context.Background(), `select count(*) from plan_roles where issue = $1`, r.key).Scan(&roles); err != nil {
		t.Fatalf("read the plan_roles rows of %s: %v", r.key, err)
	}
	if roles != 0 {
		t.Fatalf("plan_roles holds %d rows of %s, an issue whose plan defines no role, want none", roles, r.key)
	}
}

// walkImplementerProber is the walk of `implementer prober` on the walk's issue: its gate opens at
// implementing, the implementer works its step, the prober launches with the plan's model and a
// prompt that holds the plan's prompt and Legion's worker part, its task names the issue, the
// task's text and the implementer's report, and its pass runs the retro and the merge to done. The
// error names the first thing that does not hold.
func (r *deploymentRolesRig) walkImplementerProber() error {
	r.t.Helper()
	if first := r.openGate(); first != phase.Implementing {
		return fmt.Errorf("%s's gate opened and its first step is %s, want implementing, the first step of its plan", r.key, first)
	}
	r.work(claim.RoleImplementer, "implement", api.HandoffCompleteRequest{Summary: "implemented", Commit: stepRecordsHead}, phase.Step)
	prober := r.worker(planProber)
	spec := lastLaunch(r.t, r.rt, prober.token)
	if spec.Model != planProberModel {
		return fmt.Errorf("the prober's spawn spec names the model %q, want %s", spec.Model, planProberModel)
	}
	prompt, err := runtime.ComposePrompt(spec.Prompt)
	if err != nil {
		return fmt.Errorf("compose the prober's launch prompt: %w", err)
	}
	for _, want := range []string{planProberPrompt, "SEPARATE-POD"} {
		if !strings.Contains(prompt, want) {
			return fmt.Errorf("the prober's launch prompt lacks %q:\n%s", want, prompt)
		}
	}
	task := takeTask(prober)
	for _, want := range []string{r.key, deploymentTaskText, " Step 1 (implementer) reported:"} {
		if !strings.Contains(task, want) {
			return fmt.Errorf("the prober's task lacks %q:\n%s", want, task)
		}
	}
	r.finish(prober, "", api.HandoffCompleteRequest{Summary: "every probe holds", Commit: stepRecordsHead, Verdict: "pass"}, phase.Retro)
	r.retro()
	r.merge()
	return nil
}
