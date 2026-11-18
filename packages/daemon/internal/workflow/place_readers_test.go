package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// lateReportOf is lateReport's answer to a completion of role while key stands as the store holds
// it, the role having left the phase reported.
func lateReportOf(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, role claim.Role, reported phase.Phase) *intake.Refusal {
	t.Helper()
	var refusal *intake.Refusal
	applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		issue, err := engine.store.Issue(ctx, tx, key)
		if err != nil {
			return err
		}
		refusal, err = engine.lateReport(ctx, tx, *issue, intake.HandoffComplete{Issue: key, Role: role, Generation: issue.Generation}, reported, true)
		return err
	})
	return refusal
}

// Each reader of where a task stands reads its place, not the first place of its phase: in
// `implementer checker linter` both checker and linter run as step, and with the task moved on to
// step 3 the step it runs is the linter's (stepAt of where it stands), the linter's task says so
// (standingText), and a late completion of the checker is told that its step 2 ended and Legion
// runs step 3. Moved back from step 3 to step 1, a late completion of the linter names step 3, its
// own, and step 1, where the task stands. The known pass: in `implementer reviewer tester`, whose
// phases each stand once, the step of testing is step 3, the tester's.
func TestTheReadersOfWhereATaskStandsReadItsPlace(t *testing.T) {
	types := map[string]string{"checker": "testing", "linter": "testing"}

	t.Run("moved on to step 3", func(t *testing.T) {
		pool := migratedPool(t)
		engine := deploymentEngine()
		root := stepRoot(t, pool, phase.Step)
		seedStepsAt(t, pool, root.Key, root.Generation, 2, types, "implementer", "checker", "linter")
		move(t, pool, engine, root.Key, TriggerStepPassed, record.TaskPlace{}, record.PhaseRow{})

		issue, w := issueAndWalkOf(t, pool, engine, root.Key)
		if _, step, role, typ, ok := stepAt(w, standingIn(phase.Step)); !ok || step != 3 || role != "linter" || typ != "testing" {
			t.Fatalf("stepAt where the task stands = step %d, %s, %s, %t; want step 3, linter, testing", step, role, typ, ok)
		}
		text, err := standingText(issue, nil, w, nil)
		if err != nil || !strings.Contains(string(text), " Step 3 of 3: linter.") {
			t.Fatalf("the linter's standing = %q, %v; want it to say Step 3 of 3: linter.", text, err)
		}
		const ended = "step 2 (checker) of task a of LEGION-208 has ended, and Legion runs step 3 (linter) now."
		if late := lateReportOf(t, pool, engine, root.Key, "checker", phase.Step); late == nil || late.Code != "STEP_ENDED" || !strings.HasPrefix(late.Message, ended) {
			t.Fatalf("the checker's late completion = %+v, want STEP_ENDED beginning %q", late, ended)
		}
	})

	t.Run("moved back from step 3 to step 1", func(t *testing.T) {
		pool := migratedPool(t)
		engine := deploymentEngine()
		root := stepRoot(t, pool, phase.Step)
		seedStepsAt(t, pool, root.Key, root.Generation, 3, types, "implementer", "checker", "linter")
		move(t, pool, engine, root.Key, TriggerBackward, stepPlace(1), record.PhaseRow{})

		const ended = "step 3 (linter) of task a of LEGION-208 has ended, and Legion runs step 1 (implementer) now."
		if late := lateReportOf(t, pool, engine, root.Key, "linter", phase.Step); late == nil || late.Code != "STEP_ENDED" || !strings.HasPrefix(late.Message, ended) {
			t.Fatalf("the linter's late completion = %+v, want STEP_ENDED beginning %q", late, ended)
		}
	})

	t.Run("the known pass: phases that each stand once", func(t *testing.T) {
		pool := migratedPool(t)
		engine := deploymentEngine()
		root := stepRoot(t, pool, phase.Testing)
		seedStepsAt(t, pool, root.Key, root.Generation, 3, nil, "implementer", "reviewer", "tester")

		_, w := issueAndWalkOf(t, pool, engine, root.Key)
		if _, step, role, _, ok := stepAt(w, standingIn(phase.Testing)); !ok || step != 3 || role != "tester" {
			t.Fatalf("stepAt of testing = step %d, %s, %t; want step 3, tester", step, role, ok)
		}
	})
}

// The record of a return names the place the work goes back to, not the first place of its phase
// and not where the task stood: in `implementer checker linter critic` the critic at step 4 moves
// back to step 3, and the end of step 4 is failed, returned to 3. The known pass: in
// `implementer reviewer`, a rejected review ends the reviewer's step failed, returned to 1.
func TestTheRecordOfAReturnNamesThePlaceItReturnsTo(t *testing.T) {
	endOfStep := func(t *testing.T, pool *pgxpool.Pool, key string, step int) record.StepRecord {
		t.Helper()
		var ends []record.StepRecord
		for _, write := range stepWrites(t, pool, key) {
			if isEnd(write) && write.Step == step {
				ends = append(ends, write)
			}
		}
		if len(ends) != 1 {
			t.Fatalf("the ends of step %d of %s = %+v, want one", step, key, ends)
		}
		return ends[0]
	}
	returnedTo := func(end record.StepRecord) int {
		if end.ReturnedTo == nil {
			return 0
		}
		return *end.ReturnedTo
	}

	t.Run("the critic at step 4 moves back to step 3", func(t *testing.T) {
		pool := migratedPool(t)
		engine := deploymentEngine()
		root := stepRoot(t, pool, phase.Step)
		seedStepsAt(t, pool, root.Key, root.Generation, 4, map[string]string{"checker": "testing", "linter": "review", "critic": "review"},
			"implementer", "checker", "linter", "critic")

		move(t, pool, engine, root.Key, TriggerBackward, stepPlace(3), record.PhaseRow{})

		assertWalksAt(t, pool, engine, root.Key, phase.Step, stepPlace(3))
		if end := endOfStep(t, pool, root.Key, 4); resultOf(end) != "failed" || returnedTo(end) != 3 {
			t.Fatalf("the end of step 4 = %s, returned to %d; want failed, returned to 3", resultOf(end), returnedTo(end))
		}
	})

	t.Run("the known pass: a rejected review returns to step 1", func(t *testing.T) {
		pool := migratedPool(t)
		engine := deploymentEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
		reviewRequestsChanges(t, pool, engine, root.Key)

		if end := endOfStep(t, pool, root.Key, 2); resultOf(end) != "failed" || returnedTo(end) != 1 {
			t.Fatalf("the end of step 2 = %s, returned to %d; want failed, returned to 1", resultOf(end), returnedTo(end))
		}
	})
}
