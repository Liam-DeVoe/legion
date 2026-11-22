package admit

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A promotion brings back the worker of each running step of a member (promotedOwners, the owners
// startMidPhaseMembers starts on their work): a member with task a at the implementer's step and
// task b at the reviewer's, read as the store reads it, has the implementer and the reviewer as
// owners while it stands in implementing, and task c held at the tester's step adds none. A member
// with no plan rows standing in testing has its tester, as before; one held from a step has none,
// since a held step takes no work; and one at accepting has none, since that step is the
// architect's own.
//
// The promotion is held at its selection here; its start of each owner, on the copy of the member
// read for that owner's task (workflow's PromotedView), is
// TestACarriedOrphanOfSeveralTasksStartsEachTasksStep's.
//
// The fault it catches is a promotion that starts one running step of several (its control: the
// owners read as the single role of the member's phase, which brings back the implementer alone).
func TestAPromotionStartsEachRunningStepOfAMember(t *testing.T) {
	pool := migratedPool(t)
	ctx, records := context.Background(), record.NewStore()
	member := func(key string, p phase.Phase) record.Issue {
		return record.Issue{Key: key, Tree: "LEGION-700", Project: testProject, Title: key, Phase: p, Generation: 1,
			Status: "in_progress", Rank: "A", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets"}
	}
	held := member("LEGION-703", phase.Held)
	held.Hold = &record.Hold{From: phase.Implementing, Reason: record.HoldEscalated}
	seeded := []record.Issue{member("LEGION-701", phase.Implementing), member("LEGION-702", phase.Testing), held, member("LEGION-704", phase.Accepting)}
	inTx(t, pool, func(tx pgx.Tx) {
		for _, issue := range seeded {
			if err := records.PutIssue(ctx, tx, issue); err != nil {
				t.Fatalf("seed %s: %v", issue.Key, err)
			}
		}
		project, err := claim.ProjectToken(testProject)
		if err != nil {
			t.Fatal(err)
		}
		architect, err := claim.NewToken(project, "LEGION-701", claim.RoleArchitect)
		if err != nil {
			t.Fatal(err)
		}
		v := record.PlanVersion{Issue: "LEGION-701", Generation: 1, Version: 1, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner,
			GivenBy: architect, GivenAt: fixedNow}
		var tasks []record.PlanTask
		var steps []record.PlanStep
		for i, task := range []string{"a", "b", "c"} {
			tasks = append(tasks, record.PlanTask{Issue: "LEGION-701", Generation: 1, Task: task, PlanVersion: 1, BlockText: "Do " + task + ".",
				State: record.TaskRunning, Place: record.TaskPlace{Kind: record.PlaceStep, Position: i + 1}})
			for position, role := range []string{"implementer", "reviewer", "tester"} {
				steps = append(steps, record.PlanStep{Issue: "LEGION-701", Generation: 1, Task: task, Position: position + 1, Role: role,
					Type: plan.BuiltinRoles()[role].Type})
			}
		}
		if err := records.PutPlan(ctx, tx, v, tasks, steps); err != nil {
			t.Fatalf("seed the plan of LEGION-701: %v", err)
		}
		if err := records.HoldTask(ctx, tx, "LEGION-701", 1, "c", ""); err != nil {
			t.Fatalf("hold task c of LEGION-701: %v", err)
		}
	})

	for key, want := range map[string][]claim.Role{
		"LEGION-701": {claim.RoleImplementer, claim.RoleReviewer},
		"LEGION-702": {claim.RoleTester},
		"LEGION-703": nil,
		"LEGION-704": nil,
	} {
		if got := promotedOwners(issue(t, pool, key)); !slices.Equal(got, want) {
			t.Errorf("the promotion's owners of %s = %v, want %v", key, got, want)
		}
	}
}
