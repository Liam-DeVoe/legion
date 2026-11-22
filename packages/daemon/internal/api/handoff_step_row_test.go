package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// seedTwoTesterSteps gives key, at generation 1, the owner's plan `implementer tester:a reviewer
// tester:a`, one agent at the tester's two steps, with its task running at place.
func seedTwoTesterSteps(t *testing.T, h *harness, key string, place int) {
	t.Helper()
	ctx, records := context.Background(), record.NewStore()
	if err := h.store.Tx(ctx, func(tx pgx.Tx) error {
		if place == 2 {
			v := record.PlanVersion{Issue: key, Generation: 1, Version: 1, ArtifactID: "plan-" + key, Source: record.PlanSourceOwner,
				GivenBy: "owner", GivenAt: time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)}
			task := record.PlanTask{Issue: key, Generation: 1, Task: "a", PlanVersion: 1, BlockText: "Task a.", State: record.TaskWaiting}
			steps := []record.PlanStep{
				{Issue: key, Generation: 1, Task: "a", Position: 1, Role: "implementer", Type: plan.TypeImplementation},
				{Issue: key, Generation: 1, Task: "a", Position: 2, Role: "tester", AgentKey: "a", Type: plan.TypeTesting},
				{Issue: key, Generation: 1, Task: "a", Position: 3, Role: "reviewer", Type: plan.TypeReview},
				{Issue: key, Generation: 1, Task: "a", Position: 4, Role: "tester", AgentKey: "a", Type: plan.TypeTesting}}
			if err := records.PutPlan(ctx, tx, v, []record.PlanTask{task}, record.NameAgents(nil, nil, steps)); err != nil {
				return err
			}
		}
		return records.MoveTask(ctx, tx, key, 1, "a", record.TaskPlace{Kind: record.PlaceStep, Position: place}, record.TaskRunning)
	}); err != nil {
		t.Fatalf("seed the tester's step %d of %s: %v", place, key, err)
	}
}

// A role that stands at two steps of a task completes each, and the two completions are two facts
// even where they report the same thing in one pass: the key names the place of the step when the
// task names the role at two places. In `implementer tester:a reviewer tester:a`, the tester's
// completions of steps 2 and 4 name one handoff record and the verdict pass in pass 0, with no
// return between; both are applied, and neither is answered HANDOFF_ALREADY_RECORDED, so the
// workflow judges the second (which refuses a record named again, HANDOFF_NOT_NEW). Before, the
// key named no place, the second was taken as the first's duplicate, and the task never moved
// past step 4. A role at one place keeps the key it had (TestTheCompletionKeyNamesTheTaskOfAnIssueWithPlanRows).
func TestTwoCompletionsOfOneRoleInOnePassAreTwoFacts(t *testing.T) {
	h, _ := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
	const key = "LEGION-208"
	seedIssueAt(t, h, key, phase.Testing)
	seedTwoTesterSteps(t, h, key, 2)
	tester := newLiveClaim(t, h, key, claim.RoleTester)
	written := tester.writeHandoff("test", 1)
	request := HandoffCompleteRequest{GrantID: tester.grant(t), Summary: "tests pass", Verdict: "pass", HandoffID: written, HandoffVersion: 1}
	completion(t, h.complete(request))

	seedTwoTesterSteps(t, h, key, 4)
	request.GrantID = tester.grant(t)
	if answer := h.complete(request); answer.Code != http.StatusOK {
		t.Fatalf("the tester's completion of step 4 = %d %s, want it applied as a fact of its own", answer.Code, answer.Body)
	}
	prefix := "handoff:" + key + ":1:1:1:tester:testing:a:0:" + written + ":pass"
	if got, want := completionKeys(t, h, key), []string{prefix + ":p2", prefix + ":p4"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("the completion keys of %s = %q, want %q", key, got, want)
	}
}
