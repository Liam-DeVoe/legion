package workflow

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// taskBranchRowsOf is the task each task_branch row of the outbox names, oldest first.
func taskBranchRowsOf(t *testing.T, pool *pgxpool.Pool, key string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select payload->>'task' from outbox where kind = 'task_branch' and issue = $1 order by id`, key)
	if err != nil {
		t.Fatalf("read the task_branch rows of %s: %v", key, err)
	}
	named, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the task_branch rows of %s: %v", key, err)
	}
	return named
}

// joinEnds applies the end of the join of task of key (intake.TaskJoined) on the branch and the
// needs its row holds now, with no conflict, as the executor of its task_branch row applies it, and
// fails the test where the fact is not taken.
func joinEnds(t *testing.T, pool *pgxpool.Pool, engine *Engine, key, task string) {
	t.Helper()
	issue := issueOf(t, pool, engine, key)
	row := tasksOf(t, pool, engine, key)[task]
	mustSpend(t, "the end of the join of task "+task, applyFacts(t, pool, engine)("task-joined:"+task,
		intake.TaskJoined{Issue: key, Generation: issue.Generation, Task: task, Branch: row.BranchOf(key), Needs: row.Needs}))
}

// Each start of a task joins its needs at the one place that starts a step (Engine.start): the
// move of a task into running from waiting writes that its step waits for its join, and the
// start queues the task's task_branch row and keeps the wait, so nothing of the step starts until
// the end of the join (intake.TaskJoined) starts it. A task that holds the issue's own branch and
// needs none queues no row, and its step starts at once, as an issue of one task starts today.
//
// The fault it catches is a first step whose text is composed before the join of its needs ended
// (its control: start without the join's check, so d's step starts at once and the assertion
// that it waits fails).
func TestEachStartOfATaskWaitsForItsJoin(t *testing.T) {
	t.Run("the first start of a task with needs waits for its join", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Implementing)
		seedSlot(t, pool, record.Slot{Issue: root.Key, Index: 0})
		seedTasks(t, pool, root.Key, root.Generation,
			planTask{id: "a", steps: []string{"implementer:a"}, agents: []claim.Agent{"impl-a"}, state: record.TaskDone, place: atStep(1)},
			planTask{id: "c", steps: []string{"implementer:c"}, agents: []claim.Agent{"impl-c"}, state: record.TaskDone, place: atStep(1)},
			planTask{id: "d", steps: []string{"implementer:d"}, agents: []claim.Agent{"impl-d"}, needs: []string{"a", "c"}, state: record.TaskWaiting})
		seedTaskBranch(t, pool, root.Key, "a", "legion/"+root.Key)
		seedTaskBranch(t, pool, root.Key, "c", "legion/"+root.Key+".c")

		onStored(t, pool, engine, root.Key, func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
			_, err := engine.startReady(ctx, tx, issue)
			return err
		})

		if rows := taskBranchRowsOf(t, pool, root.Key); !slices.Equal(rows, []string{"d"}) {
			t.Fatalf("the task_branch rows of %s name %v, want d's one", root.Key, rows)
		}
		assertWaits(t, tasksOf(t, pool, engine, root.Key), "d", atStep(1), record.StepWaitJoin)
		if claims := startClaims(t, pool, root.Key, claim.RoleImplementer); slices.Contains(claims, agentClaim(t, root.Key, "impl-d")) {
			t.Fatalf("the implementer's starts of %s name %v, want none of d's agent: its step waits for its join", root.Key, claims)
		}
		assertNothingStarted(t, pool, root.Key, "d", atStep(1))

		branch := tasksOf(t, pool, engine, root.Key)["d"].BranchOf(root.Key)
		mustSpend(t, "the end of d's join", applyFacts(t, pool, engine)("task-joined:d",
			intake.TaskJoined{Issue: root.Key, Generation: root.Generation, Task: "d", Branch: branch, Needs: []string{"a", "c"}}))

		assertWaits(t, tasksOf(t, pool, engine, root.Key), "d", atStep(1), record.StepWaitNone)
		if claims := startClaims(t, pool, root.Key, claim.RoleImplementer); !slices.Contains(claims, agentClaim(t, root.Key, "impl-d")) {
			t.Fatalf("the implementer's starts of %s after d's join name %v, want one of d's agent impl-d", root.Key, claims)
		}
	})
	t.Run("the task that holds the issue's branch and needs none starts at once", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedSlot(t, pool, record.Slot{Issue: root.Key, Index: 0})
		seedTasks(t, pool, root.Key, root.Generation,
			planTask{id: "a", steps: []string{"implementer:a"}, agents: []claim.Agent{"impl-a"}, state: record.TaskWaiting},
			planTask{id: "b", steps: []string{"implementer:b"}, agents: []claim.Agent{"impl-b"}, needs: []string{"a"}, state: record.TaskWaiting})

		firstStepOf(t, pool, engine, root.Key)

		if rows := taskBranchRowsOf(t, pool, root.Key); len(rows) != 0 {
			t.Fatalf("the task_branch rows of %s name %v, want none: a holds the issue's own branch and needs nothing", root.Key, rows)
		}
		assertWaits(t, tasksOf(t, pool, engine, root.Key), "a", atStep(1), record.StepWaitNone)
		if claims := startClaims(t, pool, root.Key, claim.RoleImplementer); !slices.Equal(claims, []claim.Token{agentClaim(t, root.Key, "impl-a")}) {
			t.Fatalf("the implementer's starts of %s name %v, want a's agent impl-a alone", root.Key, claims)
		}
	})
}
