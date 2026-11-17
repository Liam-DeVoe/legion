package workflow

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// reportOf is role's completion of its step of LEGION-208 at generation 1 under token, naming the
// handoff record handoff ("" names none).
func reportOf(role claim.Role, token claim.Token, handoff string) intake.HandoffComplete {
	return intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: role, Claim: token, Summary: "the late report", HandoffRecord: handoff, HandoffVersion: 1}
}

// reported is what a refused report must leave as it found, or with its one write: the issue, its
// task where it has plan rows, the reporting role's phases row and the number of outbox rows.
type reported struct {
	issue  record.Issue
	task   *record.PlanTask
	row    record.PhaseRow
	outbox int
}

func reportedOf(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, role claim.Role, planned bool) reported {
	t.Helper()
	now := reported{issue: issueOf(t, pool, engine, key), row: roleRowOf(t, pool, engine, key, role), outbox: outboxRows(t, pool)}
	if planned {
		task := taskOf(t, pool, engine, key)
		now.task = &task
	}
	return now
}

// assertLateReport fails unless result is the 409 code whose message is want, and the issue, its
// task and the outbox stand as before, and the role's row as before but for LastHandoff, which
// holds handoff where the report named one (open point 3).
func assertLateReport(t *testing.T, pool *pgxpool.Pool, engine *Engine, role claim.Role, result intake.Result, code, want, handoff string, before reported) {
	t.Helper()
	if result.Refusal == nil || result.Refusal.Status != 409 || result.Refusal.Code != code || result.Refusal.Message != want {
		t.Fatalf("the late report = %+v, want 409 %s %q", result.Refusal, code, want)
	}
	expected := before
	if handoff != "" {
		expected.row.LastHandoff = handoff
	}
	if after := reportedOf(t, pool, engine, before.issue.Key, role, before.task != nil); !reflect.DeepEqual(after, expected) {
		t.Fatalf("the refused late report changed %s:\n want %+v\n got  %+v", before.issue.Key, expected, after)
	}
}

// The tail every refusal of a late report ends with where the report names a handoff record.
const usedTail = " Its handoff record counts as used, so the next run of this step writes a handoff of its own."

// For an issue with plan rows, a report of a step whose run Legion already ended is refused, and
// the answer says why and what to do (R7): the task stands at another step (STEP_ENDED), or the
// task was stopped (TASK_STOPPED, by its owner or because a failed step had no step to return to).
// Each refusal moves nothing and writes only the report's handoff record as the role's last one,
// so the next run of the step cannot complete on it (HANDOFF_NOT_NEW). Neither text names how the
// run ended: the step record Dispatch holds does (dispatch steps). A report of a task whose issue
// left the workflow and a report of another run keep main's answers. An issue with no plan rows
// keeps today's answers (R20): a late reviewer's report is taken and moves nothing, and a held
// issue's tester is told it runs no phase.
func TestALateReportIsRefusedAndSaysWhy(t *testing.T) {
	t.Run("a: a review that requested changes ended the reviewer's step", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "tester", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerTesterPassed, "", record.PhaseRow{})
		reviewer := seedPhaseWorker(t, pool, root.Key, claim.RoleReviewer, "")
		reviewRequestsChanges(t, pool, engine, root.Key)
		assertWalksAt(t, pool, engine, root.Key, phase.Implementing, record.TaskPlace{Kind: record.PlaceStep, Position: 1})
		apply := applyFacts(t, pool, engine)
		const ended = "step 3 (reviewer) of task a of LEGION-208 has ended, and Legion runs step 1 (implementer) now. This report moved nothing. dispatch steps --issue LEGION-208 shows how that run ended."

		before := reportedOf(t, pool, engine, root.Key, claim.RoleReviewer, true)
		assertLateReport(t, pool, engine, claim.RoleReviewer, apply("late", reportOf(claim.RoleReviewer, reviewer, "review-1")),
			"STEP_ENDED", ended+usedTail, "review-1", before)

		// A late report that names no handoff record has none to count as used, and says so by
		// saying nothing of one.
		before = reportedOf(t, pool, engine, root.Key, claim.RoleReviewer, true)
		assertLateReport(t, pool, engine, claim.RoleReviewer, apply("late-bare", reportOf(claim.RoleReviewer, reviewer, "")),
			"STEP_ENDED", ended, "", before)

		// Round 2: the reviewer's completion naming the record of the refused report is refused as
		// a handoff not written anew.
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerTesterPassed, "", record.PhaseRow{})
		assertWalksAt(t, pool, engine, root.Key, phase.Reviewing, record.TaskPlace{Kind: record.PlaceStep, Position: 3})
		result := apply("round-2", reportOf(claim.RoleReviewer, reviewer, "review-1"))
		want := "the reviewer named handoff record review-1 for its previous phase of LEGION-208; write this phase's handoff with `dispatch handoff-write --issue LEGION-208 --phase review --body-file <file>` before completing"
		if result.Refusal == nil || result.Refusal.Code != "HANDOFF_NOT_NEW" || result.Refusal.Message != want {
			t.Fatalf("round 2's completion naming the refused report's record = %+v, want HANDOFF_NOT_NEW %q", result.Refusal, want)
		}
	})

	t.Run("b: the owner's stop ended the tester's step", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		tester := seedPhaseWorker(t, pool, "LEGION-208", claim.RoleTester, "")
		root := stoppedAtTesting(t, pool, engine)
		before := reportedOf(t, pool, engine, root.Key, claim.RoleTester, true)

		result := applyFacts(t, pool, engine)("late", reportOf(claim.RoleTester, tester, "test-1"))

		assertLateReport(t, pool, engine, claim.RoleTester, result, "TASK_STOPPED",
			"task a of LEGION-208 was stopped by its owner. Step 2 (tester) has ended. This report moved nothing. dispatch steps --issue LEGION-208 shows how that run ended."+usedTail,
			"test-1", before)
	})

	t.Run("b: a failed step with no step to return to stopped the task", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "tester", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerTesterPassed, "", record.PhaseRow{})
		if _, err := pool.Exec(t.Context(), "update plan_steps set type = 'review' where issue = $1 and task = 'a' and position = 1", root.Key); err != nil {
			t.Fatalf("store the implementer step's type as review: %v", err)
		}
		reviewer := seedPhaseWorker(t, pool, root.Key, claim.RoleReviewer, "")
		reviewRequestsChanges(t, pool, engine, root.Key)
		if held := issueOf(t, pool, engine, root.Key); held.Hold == nil || *held.Hold != (record.Hold{From: phase.Reviewing, Reason: record.HoldTaskStopped}) {
			t.Fatalf("%s stands in %s with hold %+v, want held from reviewing for the stopped task", root.Key, held.Phase, held.Hold)
		}
		before := reportedOf(t, pool, engine, root.Key, claim.RoleReviewer, true)

		result := applyFacts(t, pool, engine)("late", reportOf(claim.RoleReviewer, reviewer, "review-1"))

		assertLateReport(t, pool, engine, claim.RoleReviewer, result, "TASK_STOPPED",
			"task a of LEGION-208 was stopped: step 3 (reviewer) failed and no earlier step of the type implementation can take the work. Step 3 (reviewer) has ended. This report moved nothing. dispatch steps --issue LEGION-208 shows how that run ended."+usedTail,
			"review-1", before)
	})

	// Main's answers, kept for an issue with plan rows: a report of a task whose issue left the
	// workflow is a report of a phase no role works, and a report of another run is stale.
	t.Run("a task whose issue left the workflow, and another run, keep main's answers", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "tester")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		tester := seedPhaseWorker(t, pool, root.Key, claim.RoleTester, "")
		apply := applyFacts(t, pool, engine)

		stale := reportOf(claim.RoleTester, tester, "test-1")
		stale.Generation = 2
		before := reportedOf(t, pool, engine, root.Key, claim.RoleTester, true)
		assertLateReport(t, pool, engine, claim.RoleTester, apply("another-run", stale), "HANDOFF_STALE_GENERATION",
			"LEGION-208 is on generation 1 and this completion is generation 2's; the run it reports is over", "", before)

		applyInTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
			issue, err := engine.store.Issue(ctx, tx, root.Key)
			if err != nil {
				return err
			}
			return engine.leave(ctx, tx, *issue, "backlog")
		})
		if task := taskOf(t, pool, engine, root.Key); task.State != record.TaskLeft {
			t.Fatalf("task a after its issue left is %s, want left", task.State)
		}
		before = reportedOf(t, pool, engine, root.Key, claim.RoleTester, true)
		assertLateReport(t, pool, engine, claim.RoleTester, apply("left", reportOf(claim.RoleTester, tester, "test-1")), "HANDOFF_NOT_CURRENT_PHASE",
			"the tester does not run phase done of LEGION-208; this completion changed nothing", "", before)
	})

	t.Run("c: the twins with no plan rows answer as today", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Reviewing)
		reviewer := seedPhaseWorker(t, pool, root.Key, claim.RoleReviewer, "")
		reviewRequestsChanges(t, pool, engine, root.Key)
		assertPhase(t, pool, phase.Implementing)

		if result := applyFacts(t, pool, engine)("late", reportOf(claim.RoleReviewer, reviewer, "review-1")); result.Refusal != nil {
			t.Fatalf("the late reviewer's report on an issue with no plan rows = %+v, want it taken as today", result.Refusal)
		}
		assertPhase(t, pool, phase.Implementing)
		if row := roleRowOf(t, pool, engine, root.Key, claim.RoleReviewer); row.HandoffRecord != "review-1" || row.LastHandoff != "review-1" {
			t.Fatalf("the reviewer's row = %+v, want the late report recorded on it as today", row)
		}

		held := migratedPool(t)
		bare := stepRoot(t, held, phase.Held)
		bare.Hold = &record.Hold{From: phase.Testing}
		seedIssue(t, held, bare)
		tester := seedPhaseWorker(t, held, bare.Key, claim.RoleTester, "")
		before := reportedOf(t, held, engine, bare.Key, claim.RoleTester, false)
		assertLateReport(t, held, engine, claim.RoleTester, applyFacts(t, held, engine)("held", reportOf(claim.RoleTester, tester, "test-1")),
			"HANDOFF_NOT_CURRENT_PHASE", "the tester does not run phase held of LEGION-208; this completion changed nothing", "", before)
	})
}
