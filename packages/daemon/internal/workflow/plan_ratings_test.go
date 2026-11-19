package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The place of a phase's step is read from the steps, never stated: the step of each working
// phase is the step of the default steps of a project that names none (config.Project.RunsByDefault)
// whose role works that phase, at one plus its index, under that role. The place logic takes the
// steps as a parameter, so a list without the tester shows that the place is read: the reviewer
// moves up to place 3, and testing runs no step of that list. Nothing global is swapped:
// RunsByDefault answers a fresh copy, so a caller that changes the slice it got changes no later
// answer.
func TestStepOfReadsTheDefaultSteps(t *testing.T) {
	works := map[string]phase.Phase{"planner": phase.Planning, "implementer": phase.Implementing, "tester": phase.Testing,
		"reviewer": phase.Reviewing, "acceptance": phase.Accepting}
	steps := config.Project{}.RunsByDefault()
	if got, want := stepWords(steps), []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}; !slices.Equal(got, want) {
		t.Fatalf("the default steps of a project that names none = %q, want %q", got, want)
	}
	for i, step := range steps {
		at, ok := works[step.Role]
		if !ok {
			t.Fatalf("the default step %q names a role that works no phase", step)
		}
		place, got, _, ok := stepOf(at)
		if want := (record.StepPlace{Task: "default", Kind: "step", Place: i + 1}); !ok || place != want || got != stepRole(step.Role) {
			t.Errorf("stepOf(%s) = %+v, role %q, ok %v; want %+v, role %q", at, place, got, ok, want, step.Role)
		}
	}

	withoutTester := slices.DeleteFunc(config.Project{}.RunsByDefault(), func(step plan.Step) bool { return step.Role == "tester" })
	if place, role, typ, ok := stepIn(withoutTester, phase.Reviewing); !ok || place.Place != 3 || role != "reviewer" || typ != "review" {
		t.Errorf("the reviewing step of %q = %+v, role %q, type %q, ok %v; want place 3 under the reviewer, of type review",
			stepWords(withoutTester), place, role, typ, ok)
	}
	if place, role, _, ok := stepIn(withoutTester, phase.Testing); ok {
		t.Errorf("the testing step of %q = %+v, role %q; want none, since no step of that list is the tester's", stepWords(withoutTester), place, role)
	}
	// A retro and a merge are no step of any list: the kind tells them apart, at place 0.
	for at, kind := range map[phase.Phase]string{phase.Retro: "retro", phase.Merging: "merge"} {
		if place, _, _, ok := stepIn(nil, at); !ok || place != (record.StepPlace{Task: "default", Kind: kind}) {
			t.Errorf("the %s step of no list = %+v, ok %v; want kind %s at place 0", at, place, ok, kind)
		}
	}

	changed := config.Project{}.RunsByDefault()
	changed[0] = plan.Step{Role: "architect"}
	if again := (config.Project{}).RunsByDefault(); again[0].Role != "planner" {
		t.Fatalf("RunsByDefault() after a caller changed its answer = %q, want a fresh copy starting with the planner", stepWords(again))
	}
}

// planWrites is every plan record row issue's outbox holds, in the order they were written.
func planWrites(t *testing.T, pool *pgxpool.Pool, issue string) []record.PlanRecord {
	t.Helper()
	var got []record.PlanRecord
	for _, row := range issueOutboxOf(t, pool, issue, string(record.OutboxKindPlanRecord)) {
		var plan record.PlanRecord
		if err := json.Unmarshal(row.payload, &plan); err != nil {
			t.Fatalf("decode a plan record row of %s: %v", issue, err)
		}
		got = append(got, plan)
	}
	return got
}

// The work of an issue starts at its move out of admitted, through the admission's own start
// (StartFirstPhase), and that move writes the plan the run carries out: one plan record of the
// issue, with the source default and one task, default, whose steps are the default steps of a
// project that names none (config.Project.RunsByDefault). Its id
// is a version 5 uuid of the issue and its run, in the plan records' own namespace, so a write
// sent again lands on the same plan and the next run gets a plan of its own. No later move of the
// run writes another.
//
// The next run is the issue re-admitted, which stands it at admitted at the next generation
// (admit's readmit), and its gate opened by a new engine over the same store, as after a restart,
// through the admission's own start (StartFirstPhase). That run writes a plan of its own beside
// the first, with run 2's generation and run 2's id, and the first stays as it was. A second
// opening of that gate, by one more engine, writes no plan: the issue has left admitted.
func TestTheWorkStartsWithTheDefaultPlan(t *testing.T) {
	pool := migratedPool(t)
	root := stepRoot(t, pool, phase.Admitted)
	engine := stepEngine(nil)
	firstStep(t, pool, engine, root.Key)

	namespace := uuid.NewSHA1(uuid.NameSpaceOID, []byte("legion-plan-record"))
	planOf := func(name string, generation uint64) record.PlanRecord {
		return record.PlanRecord{ID: uuid.NewSHA1(namespace, []byte(name)).String(), Generation: generation, Source: "default",
			Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}}}}
	}
	first := planOf("LEGION-208/1", 1)
	if plans := planWrites(t, pool, root.Key); len(plans) != 1 || !reflect.DeepEqual(plans[0], first) {
		t.Fatalf("the move out of admitted wrote the plans %+v, want the one %+v", plans, first)
	}

	move(t, pool, engine, root.Key, TriggerPlannerCompleted, "", record.PhaseRow{})
	if plans := planWrites(t, pool, root.Key); len(plans) != 1 {
		t.Fatalf("the move out of planning left %d plan records, want the one the start wrote", len(plans))
	}

	readmitted := *issueRecord(t, pool, root.Key)
	readmitted.Phase, readmitted.Generation = phase.Admitted, 2
	seedIssue(t, pool, readmitted)
	openTheGate := func() {
		t.Helper()
		applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
			engine := stepEngine(nil)
			issue, err := engine.store.Issue(ctx, tx, root.Key)
			if err != nil {
				return err
			}
			return engine.StartFirstPhase(ctx, tx, *issue, nil)
		})
	}
	openTheGate()
	second := planOf("LEGION-208/2", 2)
	if plans := planWrites(t, pool, root.Key); !reflect.DeepEqual(plans, []record.PlanRecord{first, second}) {
		t.Fatalf("the gate of run 2 left the plans %+v, want run 1's %+v and run 2's %+v", plans, first, second)
	}
	openTheGate()
	if plans := planWrites(t, pool, root.Key); len(plans) != 2 {
		t.Fatalf("a second opening of run 2's gate left %d plan records, want the two the two runs wrote: %+v", len(plans), plans)
	}
}

// A child's plan is named by the child's own key and run, never its tree's: two children of one
// root, at the same run, each write at the start of their work a plan whose id is a version 5 uuid
// of the child's key and that run, so neither carries the other's id or the root's. Dispatch
// refuses a plan id that belongs to another issue, so a plan named by the tree would be lost.
func TestAChildsPlanIsNamedByItsOwnKey(t *testing.T) {
	pool := migratedPool(t)
	stepRoot(t, pool, phase.Implementing)
	engine := stepEngine(nil)
	namespace := uuid.NewSHA1(uuid.NameSpaceOID, []byte("legion-plan-record"))
	for _, child := range []struct{ key, name string }{{"LEGION-210", "LEGION-210/3"}, {"LEGION-209", "LEGION-209/3"}} {
		stepChild(t, pool, child.key, phase.Admitted, nil)
		move(t, pool, engine, child.key, TriggerGateOpened, "", record.PhaseRow{Issue: child.key, Role: claim.RoleArchitect})
		want := record.PlanRecord{ID: uuid.NewSHA1(namespace, []byte(child.name)).String(), Generation: 3, Source: "default",
			Tasks: []record.PlanRecordTask{{ID: "default", Steps: []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}}}}
		if plans := planWrites(t, pool, child.key); len(plans) != 1 || !reflect.DeepEqual(plans[0], want) {
			t.Errorf("the start of %s's work wrote the plans %+v, want the one %+v, named by %s", child.key, plans, want, child.name)
		}
	}
	if plans := planWrites(t, pool, "LEGION-208"); len(plans) != 0 {
		t.Errorf("the children's starts wrote the plans %+v under their root, want none", plans)
	}
}

// The agent that does the retro rates the plan on the completion that ends the retro, and the
// rating is written onto the record of that retro's run: a step record row of its own, whose id is
// the id of the retro's end, carrying the rating, its reason and every identity field of the run,
// so it makes the record where the retro's start and end have not landed yet, and no moment and no
// result, which are the end's. A retro from a plugin of the release before the rating completes
// with none, and the daemon takes it and writes no rating. The run here started before this release
// wrote plans: its gate opened under the release before, so it has no plan record, and its retro,
// rated or not, writes none either. A plan is written at the start of a run's work alone, so a run
// already working at the deploy finishes with no plan (its read shows the plan null).
func TestTheRetrosRatingReachesItsRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		// rating is the completion's rating of the plan, nil for a retro that gives none.
		rating *intake.PlanRating
	}{
		{name: "a retro that rates the plan", rating: &intake.PlanRating{Score: 2, Reason: "The plan put the tests after the review."}},
		{name: "a retro of a plugin before the rating"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			root := stepRoot(t, pool, phase.Accepting)
			seedPhase(t, pool, record.PhaseRow{Issue: root.Key, Role: claim.RoleImplementer, Claim: "implement-claim"})
			engine := stepEngine(nil)
			move(t, pool, engine, root.Key, TriggerSignOff, "", record.PhaseRow{})
			applyRefusingNothing(t, pool, engine, intake.HandoffComplete{Generation: 1, Issue: root.Key, Role: claim.RoleImplementer,
				Claim: "implement-claim", Summary: "retro done", Commit: "head", PlanRating: tc.rating})
			assertPhase(t, pool, phase.Merging)

			var end *record.StepRecord
			var rated []record.StepRecord
			for _, write := range stepWrites(t, pool, root.Key) {
				if write.Kind == "retro" && isEnd(write) {
					end = &write
				}
				if write.PlanRating != nil {
					rated = append(rated, write)
				}
			}
			if end == nil {
				t.Fatalf("the retro's completion wrote no end of the retro: %+v", stepWrites(t, pool, root.Key))
			}
			if plans := planWrites(t, pool, root.Key); len(plans) != 0 {
				t.Fatalf("the retro of a run that started before this release wrote the plans %+v, want none: a plan is written at the start of a run's work alone",
					plans)
			}
			if tc.rating == nil {
				if len(rated) != 0 {
					t.Fatalf("a retro with no rating wrote the ratings %+v, want none", rated)
				}
				return
			}
			if len(rated) != 1 {
				t.Fatalf("the retro's completion wrote %d ratings (%+v), want one", len(rated), rated)
			}
			rating := rated[0]
			want := record.StepRecord{ID: end.ID, Task: "default", Step: 0, Kind: "retro", Role: "implementer", Type: "",
				Agent: end.Agent, Generation: 1, PlanRating: &record.PlanRating{Rating: tc.rating.Score, Reason: tc.rating.Reason}}
			if !reflect.DeepEqual(rating, want) {
				t.Fatalf("the rating = %+v (rating %v), want %+v on the retro's record %s", rating, deref(rating.PlanRating),
					want, end.ID)
			}
			if wantID := StepID(root.Key, 1, record.StepPlace{Task: "default", Kind: "retro"}, 1); rating.ID != wantID {
				t.Fatalf("the rating's id = %s, want %s, the first run of the retro", rating.ID, wantID)
			}
			if rating.Agent != treeRoleToken(t, root.Key, claim.RoleImplementer) {
				t.Fatalf("the rating names the agent %q, want the implementer's claim, which started the retro", rating.Agent)
			}
		})
	}
}

// A retro runs again in its run when the merge finds the head conflicting with its base and sends
// the work back: the work comes through testing, review and acceptance to the retro once more, and
// that retro's completion rates the plan again. Each rating lands on the record of its own run of
// the retro, the id of that run's end, from the count of the retro's starts, and the first rating
// stays as it was written. The run is the issue's third, so the generation in each id is a number
// neither retro's count of starts is, and each retro is completed by a new engine over the same
// store, as after a restart.
func TestASecondRetroOfARunRatesItsOwnRecord(t *testing.T) {
	pool := migratedPool(t)
	root := stepRoot(t, pool, phase.Accepting)
	root.Generation = 3
	seedIssue(t, pool, root)
	seedPhase(t, pool, record.PhaseRow{Issue: root.Key, Role: claim.RoleImplementer, Claim: "implement-claim"})
	retro := func(rating int, reason string) {
		t.Helper()
		engine := stepEngine(nil)
		move(t, pool, engine, root.Key, TriggerSignOff, "", record.PhaseRow{})
		applyRefusingNothing(t, pool, engine, intake.HandoffComplete{Generation: 3, Issue: root.Key, Role: claim.RoleImplementer,
			Claim: "implement-claim", Summary: "retro done", Commit: "head", PlanRating: &intake.PlanRating{Score: rating, Reason: reason}})
		assertPhase(t, pool, phase.Merging)
	}
	retro(2, "The plan put the tests after the review.")
	engine := stepEngine(nil)
	for _, trigger := range []TriggerKind{TriggerConflict, TriggerImplementationReady, TriggerTesterPassed, TriggerReviewApproved} {
		move(t, pool, engine, root.Key, trigger, "", record.PhaseRow{})
	}
	assertPhase(t, pool, phase.Accepting)
	retro(4, "The plan left the migration to its last step.")

	var ends, rated []record.StepRecord
	for _, write := range stepWrites(t, pool, root.Key) {
		if write.Kind == "retro" && isEnd(write) {
			ends = append(ends, write)
		}
		if write.PlanRating != nil {
			rated = append(rated, write)
		}
	}
	agent := treeRoleToken(t, root.Key, claim.RoleImplementer)
	want := []record.StepRecord{
		{ID: StepID(root.Key, 3, record.StepPlace{Task: "default", Kind: "retro"}, 1), Task: "default", Kind: "retro", Role: "implementer", Agent: agent,
			Generation: 3, PlanRating: &record.PlanRating{Rating: 2, Reason: "The plan put the tests after the review."}},
		{ID: StepID(root.Key, 3, record.StepPlace{Task: "default", Kind: "retro"}, 2), Task: "default", Kind: "retro", Role: "implementer", Agent: agent,
			Generation: 3, PlanRating: &record.PlanRating{Rating: 4, Reason: "The plan left the migration to its last step."}},
	}
	if len(ends) != 2 || len(rated) != 2 {
		t.Fatalf("two retros wrote the retro ends %+v and the ratings %+v, want two of each", ends, rated)
	}
	for i := range want {
		if !reflect.DeepEqual(rated[i], want[i]) || rated[i].ID != ends[i].ID {
			t.Errorf("rating %d = %+v (rating %v), want %+v on the record of retro %d, whose end is %s", i+1, rated[i],
				deref(rated[i].PlanRating), want[i], i+1, ends[i].ID)
		}
	}
}

// A retro already running when this release deployed started under the release before, which
// counted no start, so the retro's place holds no count of starts. Its completion ends it as a
// record of its own at the count 0 (record.Store.EndUncountedStep), under the agent its start
// would have named, the implementer's claim; and the rating the completion carries lands on that
// same record: the id of the run counted 0, the implementer's agent, the rating and its reason. The
// retro writes no plan, which only the start of a run's work writes.
func TestARetroRunningAtTheDeployRatesTheRecordItsEndMakes(t *testing.T) {
	pool := migratedPool(t)
	root := stepRoot(t, pool, phase.Retro)
	seedPhase(t, pool, record.PhaseRow{Issue: root.Key, Role: claim.RoleImplementer, Claim: "implement-claim", Entry: 1,
		EntryRaisedAt: stepClock.Add(-time.Hour)})
	place := record.StepPlace{Task: "default", Kind: "retro"}
	var before record.StepStart
	seedRecord(t, pool, func(tx pgx.Tx) error {
		var err error
		before, err = record.NewStore().StepStarts(t.Context(), tx, root.Key, place)
		return err
	})
	if before != (record.StepStart{}) || len(stepWrites(t, pool, root.Key)) != 0 {
		t.Fatalf("before its completion the retro's place holds %+v and the outbox the step writes %+v, want neither: the case is a retro begun before the deploy",
			before, stepWrites(t, pool, root.Key))
	}

	applyRefusingNothing(t, pool, stepEngine(nil), intake.HandoffComplete{Generation: 1, Issue: root.Key, Role: claim.RoleImplementer,
		Claim: "implement-claim", Summary: "retro done", Commit: "head",
		PlanRating: &intake.PlanRating{Score: 3, Reason: "The plan split the work where the review could see it."}})
	assertPhase(t, pool, phase.Merging)

	id, agent := StepID(root.Key, 1, place, 0), treeRoleToken(t, root.Key, claim.RoleImplementer)
	var ends, rated []record.StepRecord
	for _, write := range stepWrites(t, pool, root.Key) {
		if write.Kind == "retro" && isEnd(write) {
			ends = append(ends, write)
		}
		if write.PlanRating != nil {
			rated = append(rated, write)
		}
	}
	if len(ends) != 1 || ends[0].ID != id || ends[0].Agent != agent {
		t.Fatalf("the retro's completion wrote the retro ends %+v, want one, of the run counted 0 (%s) under the implementer's agent %s", ends, id, agent)
	}
	want := record.StepRecord{ID: id, Task: "default", Step: 0, Kind: "retro", Role: "implementer", Type: "", Agent: agent, Generation: 1,
		PlanRating: &record.PlanRating{Rating: 3, Reason: "The plan split the work where the review could see it."}}
	if len(rated) != 1 || !reflect.DeepEqual(rated[0], want) {
		t.Fatalf("the retro's completion wrote the ratings %+v, want the one %+v (rating %v) on the record its end made", rated, want, deref(want.PlanRating))
	}
	if plans := planWrites(t, pool, root.Key); len(plans) != 0 {
		t.Fatalf("the retro wrote the plans %+v, want none: a plan is written at the start of a run's work alone", plans)
	}
}

// deref is a pointer's value, or "<absent>" for nil, so a failure prints what a field says.
func deref[T any](value *T) any {
	if value == nil {
		return "<absent>"
	}
	return *value
}
