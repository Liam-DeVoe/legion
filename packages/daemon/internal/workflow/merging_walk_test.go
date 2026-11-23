package workflow

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// exTask is task id of the plan of tasks a to e as these tests seed it (seedTasks): the steps
// `implementer reviewer`, each with an agent of its own (impl-<id>, rev-<id>), the tasks it needs,
// and where it stands.
func exTask(id string, needs []string, state record.TaskState, place record.TaskPlace) planTask {
	return planTask{id: id, steps: []string{"implementer", "reviewer"}, agents: []claim.Agent{claim.Agent("impl-" + id), claim.Agent("rev-" + id)},
		needs: needs, state: state, place: place}
}

// exBlock is the block of task id of that plan as a version gives it, needing needs.
func exBlock(t *testing.T, id string, needs ...string) plan.Task {
	t.Helper()
	return keyed(t, id, "", []string{"implementer", "reviewer"}, needs...)
}

// markMerges writes that the block of task of key's plan at its generation says merges
// (plan_tasks.merges), as a block that names merges="true" stores it.
func markMerges(t *testing.T, pool *pgxpool.Pool, key, task string) {
	t.Helper()
	tag, err := pool.Exec(t.Context(), `update plan_tasks t set merges = true from issues i
		where i.key = t.issue and t.generation = i.generation and t.issue = $1 and t.task = $2`, key, task)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("mark task %s of %s as one that merges: %v (%d rows)", task, key, err, tag.RowsAffected())
	}
}

// walksNow is the walk of each task of key's run as the engine reads it now (Engine.walks), by id.
func walksNow(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string) map[string]Walk {
	t.Helper()
	var walks []Walk
	onStored(t, pool, engine, key, func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
		var err error
		walks, err = engine.walks(ctx, tx, issue)
		return err
	})
	byTask := make(map[string]Walk, len(walks))
	for _, w := range walks {
		byTask[w.Task] = w
	}
	return byTask
}

// actStarts is the count of starts of task's act of kind (its retro or its merge) of key, 0 where
// none started.
func actStarts(t *testing.T, pool *pgxpool.Pool, key, task, kind string) int64 {
	t.Helper()
	var starts int64
	if err := pool.QueryRow(t.Context(), `select coalesce(sum(starts), 0) from step_starts where issue = $1 and task = $2 and kind = $3`,
		key, task, kind).Scan(&starts); err != nil {
		t.Fatalf("read the starts of the %s of task %s of %s: %v", kind, task, key, err)
	}
	return starts
}

// Each task whose work merges by itself walks to its own retro and merge after its steps, and
// every other task ends at its last step: of the tasks a to e, each `implementer reviewer`, where c
// needs b, d needs a and c, e needs d, and b's block says merges, b and e merge (b by its block, e
// as the one task no task needs), and their lines end retro, merging; a, c and d end at their
// review.
//
// The fault it catches is a walk that gives a retro and a merge to the one task no task needs alone,
// so the work of b, which its block says merges, never merges by itself (its control: the walk on
// record.StoredPlan.MergingTask, the revision before, under which e alone merges and b ends at its
// review).
func TestEachTaskThatMergesWalksToItsRetroAndItsMerge(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedTasks(t, pool, root.Key, root.Generation,
		exTask("a", nil, record.TaskWaiting, record.TaskPlace{}), exTask("b", nil, record.TaskWaiting, record.TaskPlace{}),
		exTask("c", []string{"b"}, record.TaskWaiting, record.TaskPlace{}), exTask("d", []string{"a", "c"}, record.TaskWaiting, record.TaskPlace{}),
		exTask("e", []string{"d"}, record.TaskWaiting, record.TaskPlace{}))
	markMerges(t, pool, root.Key, "b")

	walks := walksNow(t, pool, engine, root.Key)
	merges := []phase.Phase{phase.Implementing, phase.Reviewing, phase.Retro, phase.Merging}
	ends := []phase.Phase{phase.Implementing, phase.Reviewing}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		w, want := walks[id], ends
		if id == "b" || id == "e" {
			want = merges
		}
		if w.Merges != (id == "b" || id == "e") || !slices.Equal(w.Line, want) {
			t.Errorf("the walk of task %s merges %t on the line %v, want merges %t on %v", id, w.Merges, w.Line, id == "b" || id == "e", want)
		}
	}
}

// A version that changes whether a task at a step merges is taken, and the task's line follows the
// answer in force when its last step passes. Of the tasks a to e, each `implementer reviewer`, with
// a and b running and c, d and e waiting, version 2 gives e the one need c, so no task needs d: it
// is taken, d waits, and its walk merges. Once a, b and c are done d runs its implementer, and
// version 3 adds f, which needs d: it is taken with no refusal, and d's walk merges no more. d's
// reviewer passes: d is done at its last step with no start of a retro or a merge, and f starts.
//
// The fault it catches is a version refused that Legion can carry out (its control: a refusal of
// each running task whose answer the version changes, which refuses version 3).
func TestAVersionThatAddsANeedOnATaskAtAStepIsTaken(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Implementing)
	seedTasks(t, pool, root.Key, root.Generation,
		exTask("a", nil, record.TaskRunning, atStep(1)), exTask("b", nil, record.TaskRunning, atStep(1)),
		exTask("c", []string{"b"}, record.TaskWaiting, record.TaskPlace{}), exTask("d", []string{"a", "c"}, record.TaskWaiting, record.TaskPlace{}),
		exTask("e", []string{"d"}, record.TaskWaiting, record.TaskPlace{}))
	seedTaskBranch(t, pool, root.Key, "a", "legion/"+root.Key)
	seedTaskBranch(t, pool, root.Key, "b", "legion/"+root.Key+".b")
	apply := applyFacts(t, pool, engine)
	blocks := func(e plan.Task, extra ...plan.Task) []plan.Task {
		return append([]plan.Task{exBlock(t, "a"), exBlock(t, "b"), exBlock(t, "c", "b"), exBlock(t, "d", "a", "c"), e}, extra...)
	}

	mustTake(t, "version 2, which gives e the one need c", apply("version-2", laterVersion(t, root.Key, root.Generation, 2, blocks(exBlock(t, "e", "c"))...)))
	assertTask(t, tasksOf(t, pool, engine, root.Key), "d", record.TaskWaiting, record.TaskPlace{})
	if d := walksNow(t, pool, engine, root.Key)["d"]; !d.Merges {
		t.Fatalf("the walk of d after version 2, in which no task needs it, merges %t on %v, want it to merge", d.Merges, d.Line)
	}

	seedRecord(t, pool, func(tx pgx.Tx) error {
		for _, task := range []string{"a", "b", "c"} {
			if err := seedMove(t.Context(), tx, root.Key, root.Generation, task, atStep(2), record.TaskDone); err != nil {
				return err
			}
		}
		return nil
	})
	seedTaskBranch(t, pool, root.Key, "c", "legion/"+root.Key+".c")
	onStored(t, pool, engine, root.Key, func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
		_, err := engine.startReady(ctx, tx, issue)
		return err
	})
	joinsEnd(t, pool, engine, root.Key)
	assertWaits(t, tasksOf(t, pool, engine, root.Key), "d", atStep(1), record.StepWaitNone)
	seedTaskPR(t, pool, engine, root.Key, "d", 45)

	mustTake(t, "version 3, which adds f needing d while d runs at a step", apply("version-3",
		laterVersion(t, root.Key, root.Generation, 3, blocks(exBlock(t, "e", "c"), exBlock(t, "f", "d"))...)))
	if d := walksNow(t, pool, engine, root.Key)["d"]; d.Merges || !slices.Equal(d.Line, []phase.Phase{phase.Implementing, phase.Reviewing}) {
		t.Fatalf("the walk of d after version 3, in which f needs it, merges %t on %v, want it to end at its review", d.Merges, d.Line)
	}

	taskMoves(t, pool, engine, root.Key, "d", TriggerImplementationReady, "")
	taskMoves(t, pool, engine, root.Key, "d", TriggerReviewApproved, "")
	tasks := tasksOf(t, pool, engine, root.Key)
	assertTask(t, tasks, "d", record.TaskDone, atStep(2))
	if retro, merge := actStarts(t, pool, root.Key, "d", record.PlaceRetro), actStarts(t, pool, root.Key, "d", record.PlaceMerge); retro != 0 || merge != 0 {
		t.Fatalf("task d counts %d starts of its retro and %d of its merge, want none: it no longer merges", retro, merge)
	}
	assertTask(t, tasks, "f", record.TaskRunning, atStep(1))
}

// A version that changes whether the work of a task at its retro or its merge merges is refused:
// of the tasks a to e, each `implementer reviewer`, where e needs c alone, so d merges by itself, d
// stands at its merge, whose merger has started, and a version that adds f, needing d, is refused
// with the one reason of d, and the plan's rows are as they were. The same holds with d held
// before its retro for the issues beneath (record.HoldForChildren), a retro that has not started.
//
// The fault it catches is a version that takes the merge off a task at its merge, so the merger
// merges work the plan no longer merges (its control: mergeChangeRefusals with no case of a
// started retro or merge, which takes the version).
func TestAVersionThatChangesWhetherATaskAtItsMergeMergesIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		place record.TaskPlace
		held  bool
		want  plan.Refusal
	}{
		{"at its merge", record.TaskPlace{Kind: record.PlaceMerge}, false, plan.Refusal{Task: "d", Reason: "task d stands at its merge, which has started, " +
			"so this version cannot change whether its work merges. Keep each need on task d as it is until it is done."}},
		{"held before its retro", record.TaskPlace{Kind: record.PlaceRetro}, true, plan.Refusal{Task: "d", Reason: "task d stands at its retro, which is held, " +
			"so this version cannot change whether its work merges. Keep each need on task d as it is until it is done."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stepRoot(t, pool, phase.Implementing)
			seedTasks(t, pool, root.Key, root.Generation,
				exTask("a", nil, record.TaskDone, atStep(2)), exTask("b", nil, record.TaskDone, atStep(2)),
				exTask("c", []string{"b"}, record.TaskDone, atStep(2)), exTask("d", []string{"a", "c"}, record.TaskRunning, tc.place),
				exTask("e", []string{"c"}, record.TaskRunning, atStep(1)))
			seedTaskBranch(t, pool, root.Key, "d", "legion/"+root.Key+".d")
			seedTaskBranch(t, pool, root.Key, "e", "legion/"+root.Key+".e")
			if tc.held {
				seedRecord(t, pool, func(tx pgx.Tx) error {
					return record.NewStore().HoldTask(t.Context(), tx, root.Key, root.Generation, "d", record.HoldForChildren)
				})
			}
			before := tasksOf(t, pool, engine, root.Key)

			result := applyFacts(t, pool, engine)("version-2", laterVersion(t, root.Key, root.Generation, 2,
				exBlock(t, "a"), exBlock(t, "b"), exBlock(t, "c", "b"), exBlock(t, "d", "a", "c"), exBlock(t, "e", "c"), exBlock(t, "f", "d")))
			want := []plan.Refusal{tc.want}
			if result.Refusal == nil || result.Refusal.Code != "PLAN_REFUSED" || !reflect.DeepEqual(result.Refusal.Reasons, want) {
				t.Fatalf("the version that adds f, needing d %s, = %+v, want PLAN_REFUSED with %+v", tc.name, result, want)
			}
			if after := tasksOf(t, pool, engine, root.Key); !reflect.DeepEqual(after, before) {
				t.Fatalf("the plan's rows after the refused version = %+v, want them as before, %+v", after, before)
			}
		})
	}
}
