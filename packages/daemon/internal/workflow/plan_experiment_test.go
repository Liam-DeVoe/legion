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
