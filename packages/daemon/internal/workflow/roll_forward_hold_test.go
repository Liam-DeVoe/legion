package workflow

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// dropPlanRows deletes every plan row of key, as the way back of the release whose owner's plan
// picks the steps drops the three tables.
func dropPlanRows(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	for _, table := range []string{"plan_steps", "plan_tasks", "issue_plans"} {
		if _, err := pool.Exec(t.Context(), "delete from "+table+" where issue = $1", key); err != nil {
			t.Fatal(err)
		}
	}
}

// After a roll forward, an issue still held task-stopped or children whose plan rows the way back
// dropped — one a way back run without its update left — is a plain hold: no plan holds a run
// that has no plan rows (planHold). The retry releases a stopped root into the phase it is held
// from, a person's todo on it starts that phase's role, and the last child's leave of a parent held
// for its children applies and starts no retro. Each control is the same issue with its rows
// kept, which its plan holds.
func TestAHoldWhosePlanRowsTheWayBackDroppedIsAPlainHold(t *testing.T) {
	for _, dropped := range []bool{false, true} {
		suffix := ": control, rows kept"
		if dropped {
			suffix = ": rows dropped by the way back"
		}
		t.Run("a retry of a stopped root"+suffix, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stoppedAtTesting(t, pool, engine)
			if dropped {
				dropPlanRows(t, pool, root.Key)
			}
			result := applyFacts(t, pool, engine)("retry", intake.RetryOrEscalate{Issue: root.Key, Decision: intake.RetryDecision, Reason: "go on"})
			got := issueOf(t, pool, engine, root.Key)
			if !dropped {
				if result.Refusal == nil || result.Refusal.Code != "TASK_STOPPED" || got.Phase != phase.Held {
					t.Fatalf("the retry = %+v, %s in %s; want TASK_STOPPED and the hold kept", result.Refusal, root.Key, got.Phase)
				}
				return
			}
			if result.Refusal != nil || got.Phase != phase.Testing {
				t.Fatalf("the retry = %+v, %s in %s; want the plain hold released into testing", result.Refusal, root.Key, got.Phase)
			}
		})

		t.Run("a person's todo on a stopped root"+suffix, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stoppedAtTesting(t, pool, engine)
			if dropped {
				dropPlanRows(t, pool, root.Key)
			}
			statuses := len(issueOutboxOf(t, pool, root.Key, string(record.OutboxKindDispatchStatus)))
			if result := applyFacts(t, pool, engine)("todo", personsTodo(root.Key)); result.Refusal != nil {
				t.Fatalf("the todo = %+v, want it taken", result.Refusal)
			}
			got := issueOf(t, pool, engine, root.Key)
			if !dropped {
				if got.Phase != phase.Implementing {
					t.Fatalf("%s stands in %s after the todo, want the stopped task started again at its first step", root.Key, got.Phase)
				}
				return
			}
			// The release of a plain hold starts the phase's role again and queues that phase's
			// status (releaseHeldRoot's retryPhase); the stop's own path, which takes a task back to
			// its first step, writes none.
			if now := len(issueOutboxOf(t, pool, root.Key, string(record.OutboxKindDispatchStatus))); got.Phase != phase.Testing || got.Hold != nil || now != statuses+1 {
				t.Fatalf("%s stands in %s (hold %+v) with %d status writes after the todo (before %d), want testing released with its status written", root.Key, got.Phase, got.Hold, now, statuses)
			}
		})

		t.Run("the last child's leave of a parent held for its children"+suffix, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			root := stepRoot(t, pool, phase.Admitted)
			seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
			stepChild(t, pool, heldParentChild, phase.Implementing, nil)
			move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
			move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
			approveReview(t, pool, engine)
			if held := issueOf(t, pool, engine, root.Key); !record.HeldForChildren(held) {
				t.Fatalf("premise: %s stands in %s hold %+v, want held for children", root.Key, held.Phase, held.Hold)
			}
			if dropped {
				dropPlanRows(t, pool, root.Key)
			}
			done := intake.DispatchIssue{Key: heldParentChild, ResolvedRepo: "sjawhar/legion", Seq: 2, Type: "issue.updated", Status: "done",
				Title: heldParentChild, Parent: "LEGION-208", Rank: "V"}
			if _, err := intake.ApplyFact(t.Context(), pool, "dispatch", "child-done", done, engine, admissionStub{}); err != nil {
				t.Fatalf("the last child's done fails its fact: %v", err)
			}
			parent := issueOf(t, pool, engine, root.Key)
			if !dropped {
				if parent.Phase != phase.Retro {
					t.Fatalf("%s stands in %s after its last child left, want its retro started", root.Key, parent.Phase)
				}
				return
			}
			if parent.Phase != phase.Held {
				t.Fatalf("%s stands in %s after its last child left, want it still held: a plain hold starts no retro", root.Key, parent.Phase)
			}
		})
	}
}
