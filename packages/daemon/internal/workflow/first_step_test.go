package workflow

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// todaysDefaultSteps is the steps of a project that names no default_steps, as the walk stores
// them: today's line up to the owner's acceptance, each with the type of its role.
var todaysDefaultSteps = []string{"planner/planning", "implementer/implementation", "tester/testing", "reviewer/review", "acceptance:owner/review"}

// engineRunningByDefault is returnEngine for a project whose default_steps are words
// (config.Project.RunsByDefault, which the daemon wires as Config.DefaultSteps).
func engineRunningByDefault(t *testing.T, words ...string) *Engine {
	t.Helper()
	steps, err := plan.ParseSteps(strings.Join(words, " "))
	if err != nil {
		t.Fatal(err)
	}
	engine := returnEngine()
	engine.cfg.DefaultSteps = steps
	return engine
}

// assertDefaultPlan fails unless key's run at generation holds the plan of the default steps
// alone (R5): version 0 of the source default, with no document and no giver, and one task,
// default, whose block text says so and whose steps are steps, run by default.
func assertDefaultPlan(t *testing.T, stored *record.StoredPlan, key string, generation uint64, steps []string) {
	t.Helper()
	if stored == nil {
		t.Fatalf("%s at generation %d holds no plan rows, want the default steps", key, generation)
	}
	if v := stored.Version; v.Issue != key || v.Generation != generation || v.Version != 0 || v.Source != record.PlanSourceDefault ||
		v.ArtifactID != "" || v.GivenBy != "" {
		t.Fatalf("the version in force of %s = %+v, want version 0 of the source default with no document and no giver", key, v)
	}
	if len(stored.Tasks) != 1 {
		t.Fatalf("the tasks of %s = %+v, want the one task default", key, stored.Tasks)
	}
	if task := stored.Tasks[0]; task.Task != plan.DefaultTask || task.PlanVersion != 0 || task.BlockText != "The project's default steps." ||
		!task.StepsByDefault {
		t.Fatalf("the one task of %s = %+v, want default of version 0, its text the project's default steps, run by default", key, task)
	}
	if got := stepsOfTask(stored, plan.DefaultTask); !reflect.DeepEqual(got, steps) {
		t.Fatalf("the steps of default = %v, want %v", got, steps)
	}
}

// An issue whose owner gives no plan stores the project's default steps at the start of its work
// (R5; StartFirstPhase's gateOpened): one version 0 of the source default, one task default, the
// project's default_steps, and the first of them running, which is planning for a project that
// names none and implementing for `default_steps: [implementer, reviewer]`, whose run starts no
// planner. An owner's plan whose one block names no steps stores the project's default steps under
// the block's id, and the first step writes no default plan beside it. The twin (R20): an issue
// that started its work before the release holds no plan rows, and a first step on it writes none
// and moves nothing.
func TestAnIssueWithNoPlanStoresTheDefaultSteps(t *testing.T) {
	t.Run("a project that names no default_steps stores today's line and starts planning", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)

		firstStep(t, pool, engine, root.Key)

		now := planStandingOf(t, pool, engine, root.Key, root.Generation)
		assertDefaultPlan(t, now.plan, root.Key, root.Generation, todaysDefaultSteps)
		want := walkStanding{phase.Planning, record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning, []string{"0 default"}, todaysDefaultSteps}
		if got := walkStandingOf(t, pool, engine, root.Key, root.Generation); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after its first step stands at %+v, want %+v", root.Key, got, want)
		}
		if planners := startsOf(t, pool, root.Key, claim.RolePlanner); len(planners) != 1 {
			t.Fatalf("the planner's starts = %+v, want one", planners)
		}
	})

	t.Run("default_steps implementer reviewer starts implementing, and no planner", func(t *testing.T) {
		pool := migratedPool(t)
		engine := engineRunningByDefault(t, "implementer", "reviewer")
		root := stepRoot(t, pool, phase.Admitted)

		firstStep(t, pool, engine, root.Key)

		steps := []string{"implementer/implementation", "reviewer/review"}
		assertDefaultPlan(t, planStandingOf(t, pool, engine, root.Key, root.Generation).plan, root.Key, root.Generation, steps)
		want := walkStanding{phase.Implementing, record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning, []string{"0 default"}, steps}
		if got := walkStandingOf(t, pool, engine, root.Key, root.Generation); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after its first step stands at %+v, want %+v", root.Key, got, want)
		}
		if planners := startsOf(t, pool, root.Key, claim.RolePlanner); len(planners) != 0 {
			t.Fatalf("the planner's starts = %+v, want none: the project's default steps name no planner", planners)
		}
		if implementers := startsOf(t, pool, root.Key, claim.RoleImplementer); len(implementers) != 1 || implementers[0].Phase != phase.Implementing {
			t.Fatalf("the implementer's starts = %+v, want one, of implementing", implementers)
		}
	})

	t.Run("an owner's block that names no steps stores the project's default steps under its id", func(t *testing.T) {
		pool := migratedPool(t)
		engine := engineRunningByDefault(t, "implementer", "reviewer")
		root := stepRoot(t, pool, phase.Admitted)
		mustTake(t, "version 1", applyFacts(t, pool, engine)(planID(root.Key, root.Generation, 1),
			ownersPlan(t, root.Key, root.Generation, 1, givenTask(t, "Build the widget."))))

		firstStep(t, pool, engine, root.Key)

		now := planStandingOf(t, pool, engine, root.Key, root.Generation)
		if task := now.task(t); task.Task != "a" || !task.StepsByDefault {
			t.Fatalf("the one task = %+v, want a, whose block named no steps", task)
		}
		want := walkStanding{phase.Implementing, record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning, []string{"1 owner"},
			[]string{"implementer/implementation", "reviewer/review"}}
		if got := walkStandingOf(t, pool, engine, root.Key, root.Generation); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after its first step stands at %+v, want %+v: the owner's task on the project's default steps, and no default plan", root.Key, got, want)
		}
	})

	t.Run("the twin: an issue that started before the release holds no rows, and a first step writes none", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Implementing)

		firstStep(t, pool, engine, root.Key)

		if got := walkStandingOf(t, pool, engine, root.Key, root.Generation); !reflect.DeepEqual(got, walkStanding{phase: phase.Implementing}) {
			t.Fatalf("%s, which started before the release, stands at %+v after a first step, want implementing with no plan rows", root.Key, got)
		}
		if got := planRowsOf(t, pool, root.Key); got != [3]int{} {
			t.Fatalf("the plan rows of %s = %v, want none", root.Key, got)
		}
	})
}

// An issue admitted in this release that its owner gave no plan holds the rows of the task
// default from its first step, so a plan its owner gives later is judged as a later version over
// them (R16): while default runs it is refused with the running task's text, never with the text
// of an issue that started its work before Legion took plans; once the owner stops default, the
// same version is taken and its task starts. The twin (R20): an issue that started its work before
// the release, with no plan rows, is refused with that earlier release's text.
func TestAnIssueOfThisReleaseJudgesALaterPlanAsALaterVersion(t *testing.T) {
	t.Run("the default task running refuses it, and stopped takes it", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		apply := applyFacts(t, pool, engine)
		root := stepRoot(t, pool, phase.Admitted)
		key, gen := root.Key, root.Generation
		firstStep(t, pool, engine, key)
		id, version1 := planID(key, gen, 1), ownersPlan(t, key, gen, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))

		before := planStandingOf(t, pool, engine, key, gen)
		reason := "task default is running: stop it with stop_task before you change it."
		assertPlanRefusal(t, pool, engine, key, gen, id, apply(id, version1), &intake.Refusal{Status: 409, Code: "PLAN_REFUSED",
			Message: "Legion refused this plan and recorded nothing: " + reason, Reasons: []plan.Refusal{{Task: plan.DefaultTask, Reason: reason}}}, before)

		if result := apply("stop", ownersStop(t, key, plan.DefaultTask)); result.Refusal != nil {
			t.Fatalf("the owner's stop of default was refused: %+v", result.Refusal)
		}
		mustTake(t, "version 1 given again after the stop", apply(id, version1))

		want := walkStanding{phase.Implementing, record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning, []string{"0 default", "1 owner"},
			[]string{"implementer/implementation", "reviewer/review"}}
		if got := walkStandingOf(t, pool, engine, key, gen); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after version 1 stands at %+v, want %+v", key, got, want)
		}
	})

	t.Run("the twin: an issue that started before the release is refused with that release's text", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Implementing)
		before := planStandingOf(t, pool, engine, root.Key, root.Generation)
		id := planID(root.Key, root.Generation, 1)
		assertPlanRefusal(t, pool, engine, root.Key, root.Generation, id,
			applyFacts(t, pool, engine)(id, ownersPlan(t, root.Key, root.Generation, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))),
			intake.PlanRefused([]plan.Refusal{intake.StartedBeforePlans(root.Key)}), before)
	})
}
