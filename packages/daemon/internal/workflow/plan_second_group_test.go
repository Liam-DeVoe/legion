package workflow

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// defaultBlock is the block default of a version: its text, and its steps as words (none for a
// block that names no steps).
func defaultBlock(t *testing.T, text string, words ...string) plan.Task {
	t.Helper()
	block := givenTask(t, text, words...)
	block.ID = plan.DefaultTask
	return block
}

// drawnAndPlanning is LEGION-208 drawn into the arm b of planning, whose issues run the project's
// default steps, with its first step started: the task default runs at step 1, the planner.
func drawnAndPlanning(t *testing.T) (*pgxpool.Pool, *Engine, record.Issue) {
	t.Helper()
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	drawnInto(t, pool, root.Key, "planning", "b", "default")
	firstStep(t, pool, engine, root.Key)
	return pool, engine, root
}

// An issue whose group runs the project's default steps takes one kind of plan from its owner: a
// later version that changes nothing but the text of its running task default. The task goes on
// at its step on the default steps, and the answer names that step. Every other version is
// answered PLAN_NOT_TAKEN, left unspent, and changes nothing. So is a version that also adds a
// task, one that changes nothing, one whose block default names steps (another issue gets
// PLAN_REFUSED), one that replaces default, and one below the version in force (another issue
// gets PLAN_VERSION_OLD).
func TestTheGroupDefaultTakesAChangeOfTheTextOfItsRunningTaskAlone(t *testing.T) {
	const was, now = "The project's default steps.", "Start with the changelog entry."
	t.Run("the text of default alone is taken", func(t *testing.T) {
		pool, engine, root := drawnAndPlanning(t)
		key, gen := root.Key, root.Generation
		atPlanner := record.TaskPlace{Kind: record.PlaceStep, Position: 1}
		if got := walkStandingOf(t, pool, engine, key, gen); got.state != record.TaskRunning || got.place != atPlanner {
			t.Fatalf("%s after its first step stands at %+v, want default running at step 1", key, got)
		}

		taken := applyFacts(t, pool, engine)(planID(key, gen, 1), ownersPlan(t, key, gen, 1, defaultBlock(t, now)))
		mustTake(t, "version 1, which changes only the text of default", taken)
		planning := []record.RunningStep{{Task: plan.DefaultTask, Place: atPlanner, Role: claim.RolePlanner}}
		if !reflect.DeepEqual(taken.Running, planning) {
			t.Fatalf("version 1 answered the running steps %+v, want %+v: step 1, the planner", taken.Running, planning)
		}
		want := walkStanding{phase.Planning, atPlanner, record.TaskRunning, []string{"0 default", "1 owner"}, todaysStoredSteps}
		if got := walkStandingOf(t, pool, engine, key, gen); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after version 1 stands at %+v, want %+v: default running at step 1 on the default steps", key, got, want)
		}
		if text := planStandingOf(t, pool, engine, key, gen).task(t).BlockText; text != now {
			t.Fatalf("the text of default after version 1 = %q, want %q", text, now)
		}
	})

	for _, tc := range []struct {
		name string
		// first is a version taken before the one refused, nil for none.
		first []plan.Task
		given []plan.Task
	}{
		{name: "the text of default and a new task", given: []plan.Task{defaultBlock(t, now), {ID: "b", Text: "Write the manual."}}},
		{name: "default as it is", given: []plan.Task{defaultBlock(t, was)}},
		{name: "default naming steps", given: []plan.Task{defaultBlock(t, now, "implementer", "reviewer")}},
		{name: "a task in place of default", given: []plan.Task{givenTask(t, now)}},
		{name: "a version below the one in force", first: []plan.Task{defaultBlock(t, now)}, given: []plan.Task{defaultBlock(t, "Start with the manual.")}},
	} {
		t.Run(tc.name+" is not taken", func(t *testing.T) {
			pool, engine, root := drawnAndPlanning(t)
			key, gen := root.Key, root.Generation
			if tc.first != nil {
				mustTake(t, "version 2, which changes only the text of default", applyFacts(t, pool, engine)(planID(key, gen, 2), ownersPlan(t, key, gen, 2, tc.first...)))
			}
			before := planStandingOf(t, pool, engine, key, gen)
			id := planID(key, gen, 1)
			assertPlanRefusal(t, pool, engine, key, gen, id, applyFacts(t, pool, engine)(id, ownersPlan(t, key, gen, 1, tc.given...)), planNotTakenOfB(key), before)
		})
	}
}
