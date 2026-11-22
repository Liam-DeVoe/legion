package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// coderDeployment is Legion's own roles of a step, coder, a role of the deployment of the type
// implementation, and checker, one of the type testing.
func coderDeployment() plan.Deployment {
	roles := plan.BuiltinRoles()
	roles["coder"] = plan.RoleDef{Name: "coder", Type: plan.TypeImplementation}
	roles["checker"] = plan.RoleDef{Name: "checker", Type: plan.TypeTesting}
	return plan.Deployment{Roles: roles}
}

// seedCoderRetro writes the plan `coder checker` of key at generation 1 with its task running at
// its retro, which the coder does: the store reads key's running role at the retro as the coder.
func seedCoderRetro(t *testing.T, h *harness, key string) {
	t.Helper()
	ctx := context.Background()
	err := h.store.Tx(ctx, func(tx pgx.Tx) error {
		records := record.NewStore()
		v := record.PlanVersion{Issue: key, Generation: 1, Version: 2, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner,
			GivenBy: "legion-legion-" + claim.Token(key) + "-architect"}
		task := record.PlanTask{Issue: key, Generation: 1, Task: "a", PlanVersion: 2, BlockText: "Build the widget.", State: record.TaskWaiting}
		steps := []record.PlanStep{
			{Issue: key, Generation: 1, Task: "a", Position: 1, Role: "coder", Type: plan.TypeImplementation},
			{Issue: key, Generation: 1, Task: "a", Position: 2, Role: "checker", Type: plan.TypeTesting},
		}
		if err := records.PutPlan(ctx, tx, v, []record.PlanTask{task}, steps); err != nil {
			return err
		}
		return records.MoveTask(ctx, tx, key, 1, "a", record.TaskPlace{Kind: record.PlaceRetro}, record.TaskRunning)
	})
	if err != nil {
		t.Fatalf("seed the coder's retro of %s: %v", key, err)
	}
}

// The retro of a task is reported by the role of the agent that does it: in `coder checker`,
// coder of the type implementation, the coder's completion at the retro, with its commit and its
// rating and no verdict, is taken as one HandoffComplete of the coder, and the retro ends: the
// issue, which has a pull request, moves on to merging. The same completion naming a verdict is
// refused VERDICT_ROLE_FORBIDDEN, since a retro passes or fails nothing. The implementer, which
// does no step of this task, has no completion of the retro taken, and the issue stays at the
// retro. The control is the judge of the coder's completion by the rule of its step (a role of the
// deployment reports a verdict), which refuses its retro STEP_VERDICT_REQUIRED.
func TestTheRetroOfARoleOfTheDeploymentIsReported(t *testing.T) {
	const key = "LEGION-208"
	for _, tc := range []struct {
		name    string
		role    claim.Role
		verdict string
		code    string
		standAt phase.Phase
	}{
		{name: "the coder, which does the retro", role: "coder", standAt: phase.Merging},
		{name: "the coder naming a verdict", role: "coder", verdict: "pass", code: "VERDICT_ROLE_FORBIDDEN", standAt: phase.Retro},
		{name: "the implementer, which does no step of the task", role: claim.RoleImplementer, standAt: phase.Retro},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, facts := newWorkflowHarness(t, workflow.Config{Project: testProject, Repositories: testRepositories,
				Steps: config.DesignGateRootIssues.Steps(), ReviewRoundCap: 3, Deployment: coderDeployment()})
			seedIssueAt(t, h, key, phase.Retro)
			seedCoderRetro(t, h, key)
			seedPullRequest(t, h, key, headSHA)
			seedEntry(t, h, key, tc.role, 1)
			worker := newLiveClaim(t, h, key, tc.role)
			takeTask(t, h, worker.token, "outbox:77", "retro "+key, 1, 1)
			rating, reason := 4, "the steps fit the work"
			answer := h.complete(HandoffCompleteRequest{GrantID: worker.grant(t), Summary: "retro done", Commit: headSHA, Verdict: tc.verdict,
				PlanRating: &rating, PlanReason: &reason})

			var taken []intake.HandoffComplete
			for _, fact := range facts.recorded() {
				if completed, ok := fact.(intake.HandoffComplete); ok && completed.Role == tc.role {
					taken = append(taken, completed)
				}
			}
			switch {
			case tc.code != "":
				if message := refused(t, answer, http.StatusBadRequest, tc.code); !strings.Contains(message, "the retro of "+key+" reports no verdict") {
					t.Fatalf("the refusal = %q, want it to say the retro reports no verdict", message)
				}
				if len(taken) != 0 {
					t.Fatalf("the %s's refused completion recorded %+v, want nothing", tc.role, taken)
				}
			case tc.standAt == phase.Retro:
				if answer.Code == http.StatusOK {
					t.Fatalf("the %s's completion at the coder's retro answered 200, want it refused", tc.role)
				}
			default:
				completion(t, answer)
				if len(taken) != 1 || taken[0].Commit != headSHA || taken[0].Verdict != "" || taken[0].PlanRating == nil || taken[0].PlanRating.Rating != 4 {
					t.Fatalf("the coder's completions = %+v, want the one of its retro, with its commit and its rating and no verdict", taken)
				}
			}
			assertIssuePhase(t, h, tc.standAt)
		})
	}
}
