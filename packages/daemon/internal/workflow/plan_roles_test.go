package workflow

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A role the owner's plan defines is kept with the version that defines it, and a step that names
// it is stored with the role's type, the type the plan gives it. While the task whose step names
// the role runs, a later version that gives the role as it was is taken, since the plan in force
// is compared with its roles, and one that changes only that role's prompt changes the task, so it
// is refused, naming the task, and the role as it was stays; after stop_task that version is
// taken, and the role is kept as the new version gives it. So no agent that runs gets another
// prompt.
func TestAPlansRoleIsStoredWithItsTypeAndAChangedRoleIsAChangedTask(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	apply := applyFacts(t, pool, engine)
	root := stepRoot(t, pool, phase.Admitted)
	key, gen := root.Key, root.Generation
	probed := givenTask(t, "Build the widget, then probe it.", "implementer", "prober")
	withProber := func(version int, prompt string) intake.PlanGiven {
		given := ownersPlan(t, key, gen, version, probed)
		given.Plan.Roles = []plan.PlanRole{{Name: "prober", Type: plan.TypeTesting, Model: "m-one", Prompt: prompt}}
		return given
	}
	prober := func(version int, prompt string) []record.PlanRole {
		return []record.PlanRole{{Issue: key, Generation: gen, Role: "prober", PlanVersion: version, Type: "testing", Model: "m-one", Prompt: prompt}}
	}

	mustTake(t, "version 1", apply(planID(key, gen, 1), withProber(1, "Probe.")))
	taken := planStandingOf(t, pool, engine, key, gen)
	if got := stepsOfTask(taken.plan, "a"); !slices.Equal(got, []string{"implementer/implementation", "prober/testing"}) {
		t.Fatalf("the steps of a = %v, want the prober's step stored with the type its plan gives it", got)
	}
	if want := prober(1, "Probe."); !reflect.DeepEqual(taken.plan.Roles, want) {
		t.Fatalf("the roles of version 1 = %+v, want %+v", taken.plan.Roles, want)
	}

	move(t, pool, engine, key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	running := planStandingOf(t, pool, engine, key, gen)
	if task := running.task(t); task.State != record.TaskRunning {
		t.Fatalf("task a after the gate opened = %+v, want it running at the implementer", task)
	}
	mustTake(t, "version 2 with the role as it was", apply(planID(key, gen, 2), withProber(2, "Probe.")))
	kept := planStandingOf(t, pool, engine, key, gen)
	if task := kept.task(t); task.State != record.TaskRunning || task.Place != running.task(t).Place {
		t.Fatalf("task a after version 2 = %+v, want it running where it stood", task)
	}
	if want := prober(2, "Probe."); !reflect.DeepEqual(kept.plan.Roles, want) {
		t.Fatalf("the roles of version 2 = %+v, want %+v", kept.plan.Roles, want)
	}
	id := planID(key, gen, 3)
	assertPlanRefusal(t, pool, engine, key, gen, id, apply(id, withProber(3, "Probe again.")), runningRefusal(), kept)

	if result := apply("stop", ownersStop(t, root, "a")); result.Refusal != nil {
		t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
	}
	mustTake(t, "version 3 after the stop", apply(id, withProber(3, "Probe again.")))
	after := planStandingOf(t, pool, engine, key, gen)
	if want := prober(3, "Probe again."); !reflect.DeepEqual(after.plan.Roles, want) {
		t.Fatalf("the roles of version 3 = %+v, want %+v", after.plan.Roles, want)
	}
}
