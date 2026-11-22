package api

import (
	"context"
	"net/http"
	"strings"
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
			if err := records.PutPlan(ctx, tx, v, []record.PlanTask{task}, record.NameAgents(nil, steps)); err != nil {
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

// seedTesterAt gives key, at generation 1, the owner's plan of task a whose steps are roles, each
// with the type plan.BuiltinRoles gives it and the key a on every tester's step, with the task
// running at place, and the tester's row of each place in entries at that entry, under token, raised
// by a forward move in place order.
func seedTesterAt(t *testing.T, h *harness, key string, roles []string, place int, token claim.Token, entries map[int]int64) {
	t.Helper()
	ctx, records := context.Background(), record.NewStore()
	builtin := plan.BuiltinRoles()
	raised := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	if err := h.store.Tx(ctx, func(tx pgx.Tx) error {
		v := record.PlanVersion{Issue: key, Generation: 1, Version: 1, ArtifactID: "plan-" + key, Source: record.PlanSourceOwner,
			GivenBy: "owner", GivenAt: raised}
		task := record.PlanTask{Issue: key, Generation: 1, Task: "a", PlanVersion: 1, BlockText: "Task a.", State: record.TaskWaiting}
		steps := make([]record.PlanStep, 0, len(roles))
		for i, role := range roles {
			step := record.PlanStep{Issue: key, Generation: 1, Task: "a", Position: i + 1, Role: role, Type: builtin[role].Type}
			if role == string(claim.RoleTester) {
				step.AgentKey = "a"
			}
			steps = append(steps, step)
		}
		if err := records.PutPlan(ctx, tx, v, []record.PlanTask{task}, record.NameAgents(nil, steps)); err != nil {
			return err
		}
		if err := records.MoveTask(ctx, tx, key, 1, "a", record.TaskPlace{Kind: record.PlaceStep, Position: place}, record.TaskRunning); err != nil {
			return err
		}
		for at := range len(roles) + 1 {
			entry, ok := entries[at]
			if !ok {
				continue
			}
			if err := records.PutPhase(ctx, tx, record.PhaseRow{Issue: key, Role: claim.RoleTester, Claim: token, Entry: entry,
				EntryRaisedAt: raised.Add(time.Duration(at) * time.Minute), StepTask: "a", StepPlace: at}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed the tester's steps of %s at step %d: %v", key, place, err)
	}
}

// The route holds a completion to the step its turn was given, as the workflow does. In
// `implementer tester:a reviewer tester:a`, a forward move entered step 4 at the head step 2 left,
// and the agent's turn of step 2, still open on step 2's entry, completes at the pull request's head
// with a record of its own: its entry is at or below step 2's row, another step of the role, so the
// route refuses it HANDOFF_STALE_ENTRY, saying so, and applies no fact. The control is a role at one
// step, `implementer tester reviewer`, whose step a forward move handed again at the same head: the
// same completion is taken, as it was before a role stood at two steps. Without the route's read of
// the other step (record.OtherStepsEntry) the first completion was applied, and only the
// workflow's own fence, behind this route, refused it.
func TestTheRouteRefusesATurnOfARolesOtherStep(t *testing.T) {
	const key = "LEGION-208"
	for _, tc := range []struct {
		name    string
		roles   []string
		place   int
		entries map[int]int64
		refused bool
	}{
		{"the turn of step 2 completing while step 4 runs", []string{"implementer", "tester", "reviewer", "tester"}, 4, map[int]int64{2: 1, 4: 3}, true},
		{"the control: a role at one step handed again at the same head", []string{"implementer", "tester", "reviewer"}, 2, map[int]int64{2: 3}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, facts := newHandoffHarness(t, newHandoffGitHub(t), nil, nil)
			seedIssueAt(t, h, key, phase.Testing)
			seedPullRequest(t, h, key, headSHA)
			tester := newLiveClaim(t, h, key, claim.RoleTester)
			seedTesterAt(t, h, key, tc.roles, tc.place, tester.token, tc.entries)
			takeTask(t, h, tester.token, "outbox:77", "test "+key, 1, 1)

			answer := h.complete(HandoffCompleteRequest{GrantID: tester.grant(t), Summary: "tests pass", Verdict: "pass",
				Commit: headSHA, HandoffID: tester.writeHandoff("test", 1), HandoffVersion: 1})
			if !tc.refused {
				completion(t, answer)
				if got := facts.recorded(); len(got) != 1 {
					t.Fatalf("facts after the completion = %#v, want the one completion applied", got)
				}
				return
			}
			message := refused(t, answer, http.StatusConflict, "HANDOFF_STALE_ENTRY")
			for _, want := range []string{
				"this completion is at commit " + headSHA + " while the pull request the record holds is at head " + headSHA,
				"the entry this completion serves is of another step of this role",
				"nothing was recorded",
			} {
				if !strings.Contains(message, want) {
					t.Fatalf("the refusal = %q, want it to say %q", message, want)
				}
			}
			if got := facts.recorded(); len(got) != 0 {
				t.Fatalf("facts after the refusal = %#v, want none applied: the route refuses before the fact", got)
			}
		})
	}
}
