package workflow

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// drawnInto records key as drawn into variant of experiment, an experiment of unit issue whose
// arm says how its issues are done (plan), as the draw at the issue's first record writes it
// (experiment.DrawAndRecord).
func drawnInto(t *testing.T, pool *pgxpool.Pool, key, experiment, variant, plan string) {
	t.Helper()
	at := time.Date(2026, 11, 17, 19, 7, 25, 0, time.UTC)
	applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := record.NewStore().PutAssignments(ctx, tx, key, []record.Assignment{
			{Experiment: experiment, Unit: "issue", Variant: variant, Mode: "random", DrawnAt: &at, Plan: plan}})
		return err
	})
}

// inPlanningB is how a notice names the group of key in the variant b of the experiment planning,
// whose issues run the project's default steps.
func inPlanningB(key string) string {
	return key + " is in the variant b of the experiment planning, whose issues run the project's default steps."
}

// The first step of an issue whose group runs the project's default steps takes the default steps
// and no plan of its owner: a plan given at generation 0, before the daemon recorded the child and
// drew it, is deleted rather than carried, and the child's architect is told in one plan-not-taken
// notice that names the plan and the group. The same child drawn into the arm whose owner gives
// the plan carries that plan and is told nothing. A child of the group whose owner gave no plan
// runs the default steps and is told nothing either.
func TestAPlanGivenBeforeTheDrawIsPassedOver(t *testing.T) {
	const child = "LEGION-209"
	given := func(t *testing.T, pool *pgxpool.Pool, engine *Engine) {
		t.Helper()
		recordedAt(t, pool, "LEGION-208", "LEGION-208")
		mustTake(t, "version 1 at generation 0", applyFacts(t, pool, engine)(planID(child, 0, 1),
			ownersPlan(t, child, 0, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))))
		recordedAt(t, pool, child, "LEGION-208")
	}

	t.Run("drawn into the default steps, the plan is passed over and told", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		given(t, pool, engine)
		drawnInto(t, pool, child, "planning", "b", "default")

		firstStep(t, pool, engine, child)

		assertDefaultPlan(t, planStandingOf(t, pool, engine, child, 1).plan, child, 1, todaysDefaultSteps)
		want := walkStanding{phase.Planning, record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning, []string{"0 default"}, todaysDefaultSteps}
		if got := walkStandingOf(t, pool, engine, child, 1); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after its first step stands at %+v, want %+v", child, got, want)
		}
		if got := planRowsOf(t, pool, child); got != [3]int{1, 1, len(todaysDefaultSteps)} {
			t.Fatalf("the plan rows of %s = %v, want the default plan alone: the plan of generation 0 deleted", child, got)
		}
		told := []record.Notice{{Kind: "plan-not-taken", Role: claim.RoleArchitect, Reason: "The first step of " + child +
			" took no plan of its owner: version 1 of document " + planArtifact + ", given by " + token(t, "LEGION-208", claim.RoleArchitect) +
			". " + inPlanningB(child)}}
		if got := noticesOf(t, pool, child, "plan-not-taken"); !reflect.DeepEqual(got, told) {
			t.Fatalf("the plan-not-taken notices = %+v, want %+v", got, told)
		}
	})

	t.Run("the control: drawn into the owner's plan, the plan is carried and nothing is told", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		given(t, pool, engine)
		drawnInto(t, pool, child, "planning", "a", "owner")

		firstStep(t, pool, engine, child)

		want := walkStanding{phase.Implementing, record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning, []string{"1 owner"},
			[]string{"implementer/implementation", "reviewer/review"}}
		if got := walkStandingOf(t, pool, engine, child, 1); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after its first step stands at %+v, want %+v, the owner's plan carried", child, got, want)
		}
		if got := noticesOf(t, pool, child, "plan-not-taken"); len(got) != 0 {
			t.Fatalf("the plan-not-taken notices = %+v, want none", got)
		}
	})

	t.Run("drawn into the default steps with no plan given, nothing is told", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		recordedAt(t, pool, "LEGION-208", "LEGION-208")
		recordedAt(t, pool, child, "LEGION-208")
		drawnInto(t, pool, child, "planning", "b", "default")

		firstStep(t, pool, engine, child)

		assertDefaultPlan(t, planStandingOf(t, pool, engine, child, 1).plan, child, 1, todaysDefaultSteps)
		if got := noticesOf(t, pool, child, "plan-not-taken"); len(got) != 0 {
			t.Fatalf("the plan-not-taken notices = %+v, want none: the store held no plan of its owner", got)
		}
	})
}

// An issue whose owner changed keeps the group of its first draw. Where its group runs the
// project's default steps, the plan an earlier owner gave is passed over in the same one notice as
// the owner's own, which names the group and never tells the owner to give the plan again with
// register_plan: that call is answered PLAN_NOT_TAKEN for this issue. Here the architect of
// LEGION-208 gave the plan of LEGION-209 at generation 0, and LEGION-209 was then recorded as a
// root of its own, whose owner is its own architect.
func TestAnEarlierOwnersPlanInTheDefaultGroupIsToldOnce(t *testing.T) {
	const issue = "LEGION-209"
	pool := migratedPool(t)
	engine := returnEngine()
	mustTake(t, "the earlier owner's version 1 at generation 0", applyFacts(t, pool, engine)(planID(issue, 0, 1),
		ownersPlan(t, issue, 0, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))))
	recordedAt(t, pool, issue, issue)
	drawnInto(t, pool, issue, "planning", "b", "default")

	firstStep(t, pool, engine, issue)

	assertDefaultPlan(t, planStandingOf(t, pool, engine, issue, 1).plan, issue, 1, todaysDefaultSteps)
	told := noticesOf(t, pool, issue, "plan-not-taken")
	want := []record.Notice{{Kind: "plan-not-taken", Role: claim.RoleArchitect, Reason: "The first step of " + issue +
		" took no plan of its owner: version 1 of document " + planArtifact + ", given by " + token(t, "LEGION-208", claim.RoleArchitect) +
		". " + inPlanningB(issue)}}
	if !reflect.DeepEqual(told, want) {
		t.Fatalf("the plan-not-taken notices = %+v, want %+v", told, want)
	}
	for _, notice := range told {
		if strings.Contains(notice.Reason, "register_plan") {
			t.Fatalf("the notice %q tells the owner to call register_plan, which this issue's group refuses", notice.Reason)
		}
	}
}

// planNotTakenOfB is the answer to a plan given for key, in the variant b of planning.
func planNotTakenOfB(key string) *intake.Refusal {
	return &intake.Refusal{Status: 409, Code: "PLAN_NOT_TAKEN", Message: inPlanningB(key) + " Legion takes no plan from its owner for this issue."}
}

// drawnForMemory records key's tree as drawn into the arm memory of the `memory` experiment,
// which names no plan: a group of no experiment that sets how an issue is done.
func drawnForMemory(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	at := time.Date(2026, 11, 17, 19, 7, 25, 0, time.UTC)
	applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := record.NewStore().PutAssignments(ctx, tx, key, []record.Assignment{
			{Experiment: "memory", Unit: "tree", Variant: "memory", Mode: "random", DrawnAt: &at}})
		return err
	})
}

// An issue whose group runs the project's default steps takes no plan from its owner:
// register_plan is answered 409 PLAN_NOT_TAKEN with the sentence that names the group, nothing is
// recorded, and the first step runs the project's default steps from planning, under a plan whose
// source is the default. The control is the same issue drawn into the arm whose owner gives the
// plan: the plan is taken and its first step is the implementer's.
func TestTheGroupDefaultRunsTheDefaultSteps(t *testing.T) {
	given := func(t *testing.T, key string, generation uint64) (string, intake.PlanGiven) {
		return planID(key, generation, 1), ownersPlan(t, key, generation, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))
	}

	t.Run("the group default refuses the plan and runs the default steps", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		drawnInto(t, pool, root.Key, "planning", "b", "default")
		before := planStandingOf(t, pool, engine, root.Key, root.Generation)
		id, plan := given(t, root.Key, root.Generation)

		assertPlanRefusal(t, pool, engine, root.Key, root.Generation, id, applyFacts(t, pool, engine)(id, plan), planNotTakenOfB(root.Key), before)
		if got := planRowsOf(t, pool, root.Key); got != [3]int{} {
			t.Fatalf("the plan rows of %s = %v, want none: Legion records nothing when it takes no plan", root.Key, got)
		}

		firstStep(t, pool, engine, root.Key)

		assertDefaultPlan(t, planStandingOf(t, pool, engine, root.Key, root.Generation).plan, root.Key, root.Generation, todaysDefaultSteps)
		want := walkStanding{phase.Planning, record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning, []string{"0 default"}, todaysDefaultSteps}
		if got := walkStandingOf(t, pool, engine, root.Key, root.Generation); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after its first step stands at %+v, want %+v", root.Key, got, want)
		}
	})

	t.Run("the control: the group owner takes the plan and starts implementing", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		drawnInto(t, pool, root.Key, "planning", "a", "owner")
		id, plan := given(t, root.Key, root.Generation)

		mustTake(t, "version 1", applyFacts(t, pool, engine)(id, plan))
		firstStep(t, pool, engine, root.Key)

		assertWalksAt(t, pool, engine, root.Key, phase.Implementing, record.TaskPlace{Kind: record.PlaceStep, Position: 1})
		if got := planStandingOf(t, pool, engine, root.Key, root.Generation).versions; !reflect.DeepEqual(got, []string{"1 owner"}) {
			t.Fatalf("the versions of %s = %v, want the owner's alone", root.Key, got)
		}
	})
}

// The answer PLAN_NOT_TAKEN leaves the registration unspent, as every refusal of a plan does: the
// same registration sent again is answered PLAN_NOT_TAKEN again, never as a registration already
// applied, which the route would answer as the plan taken. Nothing is stored either time.
func TestAPlanNotTakenIsAnsweredTheSameWhenGivenAgain(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	drawnInto(t, pool, root.Key, "planning", "b", "default")
	id := planID(root.Key, root.Generation, 1)
	plan := ownersPlan(t, root.Key, root.Generation, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))
	apply := applyFacts(t, pool, engine)

	for _, attempt := range []string{"the first registration", "the same registration sent again"} {
		result := apply(id, plan)
		if result.Duplicate || !reflect.DeepEqual(result.Refusal, planNotTakenOfB(root.Key)) {
			t.Fatalf("%s = %+v, want the refusal %+v", attempt, result, planNotTakenOfB(root.Key))
		}
		if got := planRowsOf(t, pool, root.Key); got != [3]int{} {
			t.Fatalf("after %s the plan rows of %s = %v, want none", attempt, root.Key, got)
		}
	}
}

// An issue of no group runs as built (R20): an issue the daemon recorded before any experiment
// set how an issue is done holds no row, and one whose rows name no plan — the `memory`
// experiment's alone here — is of no group either. Each takes the plan its owner gives.
func TestAnIssueWithNoGroupRunsAsBuilt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drawn func(t *testing.T, pool *pgxpool.Pool, key string)
	}{
		{"no row", func(*testing.T, *pgxpool.Pool, string) {}},
		{"a row of memory alone", drawnForMemory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stepRoot(t, pool, phase.Admitted)
			tc.drawn(t, pool, root.Key)

			mustTake(t, "version 1", applyFacts(t, pool, engine)(planID(root.Key, root.Generation, 1),
				ownersPlan(t, root.Key, root.Generation, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))))
			firstStep(t, pool, engine, root.Key)

			assertWalksAt(t, pool, engine, root.Key, phase.Implementing, record.TaskPlace{Kind: record.PlaceStep, Position: 1})
		})
	}
}
