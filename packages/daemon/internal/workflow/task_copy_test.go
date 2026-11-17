package workflow

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// An owner's plan of one task starts as an issue always did: at its first step the one task holds
// the issue's own branch, legion/<KEY>, which the issue_branch row makes, and no task_branch row is
// queued for it.
func TestTheOneTaskOfAPlanTakesTheIssuesBranchAndQueuesNoRow(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	mustTake(t, "version 1", applyFacts(t, pool, engine)(planID(root.Key, root.Generation, 1),
		ownersPlan(t, root.Key, root.Generation, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))))

	firstStep(t, pool, engine, root.Key)

	if task := planStandingOf(t, pool, engine, root.Key, root.Generation).task(t); task.Branch != "legion/LEGION-208" {
		t.Fatalf("the branch of task %s after the first step = %q, want the issue's own, legion/LEGION-208", task.Task, task.Branch)
	}
	if rows := outboxOf(t, pool, string(record.OutboxKindIssueBranch)); len(rows) != 1 {
		t.Fatalf("the issue_branch rows = %+v, want one", rows)
	}
	if rows := outboxOf(t, pool, string(record.OutboxKindTaskBranch)); len(rows) != 0 {
		t.Fatalf("the task_branch rows = %+v, want none: the issue_branch row makes the one task's branch", rows)
	}
}

// Each task that starts takes its branch, and each that does not hold the issue's own branch, or
// that needs other tasks, queues one task_branch row that names it: the row makes its branch on
// GitHub, at the joined work of its needs, before any step of it starts.
func TestASecondTaskQueuesItsBranchRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		// needs is the needs of each task of the plan, by id.
		needs map[string][]string
		// starts is the tasks that start, in order.
		starts []string
		// branches is the branch each task holds after the starts.
		branches map[string]string
		// rows is the task each task_branch row names, in the order they were queued.
		rows []string
	}{
		{
			name:     "the second task takes the first give of its slug and queues its row",
			needs:    map[string][]string{"a": nil, "b": nil},
			starts:   []string{"a", "b"},
			branches: map[string]string{"a": "legion/LEGION-208", "b": "legion/LEGION-208.b"},
			rows:     []string{"b"},
		},
		{
			name:     "a task that needs another queues its row",
			needs:    map[string][]string{"a": nil, "b": {"a"}},
			starts:   []string{"a", "b"},
			branches: map[string]string{"a": "legion/LEGION-208", "b": "legion/LEGION-208.b"},
			rows:     []string{"b"},
		},
		{
			name:     "the task that holds the issue's branch and needs another queues its row, for the join",
			needs:    map[string][]string{"a": {"b"}, "b": nil},
			starts:   []string{"a"},
			branches: map[string]string{"a": "legion/LEGION-208", "b": ""},
			rows:     []string{"a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stepRoot(t, pool, phase.Admitted)
			version := record.PlanVersion{Issue: root.Key, Generation: root.Generation, Version: 1, Source: record.PlanSourceOwner,
				ArtifactID: planArtifact, GivenAt: engine.now()}
			var tasks []record.PlanTask
			var steps []record.PlanStep
			for _, id := range []string{"a", "b"} {
				tasks = append(tasks, record.PlanTask{Issue: root.Key, Generation: root.Generation, Task: id, PlanVersion: 1,
					BlockText: "Build part " + id + ".", Needs: tc.needs[id], State: record.TaskWaiting})
				steps = append(steps, record.PlanStep{Issue: root.Key, Generation: root.Generation, Task: id, Position: 1,
					Role: "implementer", Type: "implementation"})
			}
			applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
				if err := engine.store.PutPlan(ctx, tx, version, tasks, steps); err != nil {
					return err
				}
				for _, task := range tc.starts {
					if err := engine.startTaskCopy(ctx, tx, root, task); err != nil {
						return err
					}
				}
				return nil
			})

			stored := planStandingOf(t, pool, engine, root.Key, root.Generation).plan
			branches := map[string]string{}
			for _, task := range stored.Tasks {
				branches[task.Task] = task.Branch
			}
			if !reflect.DeepEqual(branches, tc.branches) {
				t.Fatalf("the branch of each task = %v, want %v", branches, tc.branches)
			}
			var named []string
			for _, row := range outboxOf(t, pool, string(record.OutboxKindTaskBranch)) {
				if row.issue != root.Key || row.payload["generation"] != float64(root.Generation) {
					t.Fatalf("a task_branch row = %+v, want one of %s at generation %d", row, root.Key, root.Generation)
				}
				task, _ := row.payload["task"].(string)
				named = append(named, task)
			}
			if !reflect.DeepEqual(named, tc.rows) {
				t.Fatalf("the tasks the task_branch rows name = %v, want %v", named, tc.rows)
			}
		})
	}
}
