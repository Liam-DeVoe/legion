package admit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// holdByPlan writes key's plan `implementer tester`, task a at place, and the hold its plan holds
// it in: stopped with why where reason is record.HoldTaskStopped, running where the hold stands in
// place of its retro (record.HoldForChildren).
func holdByPlan(t *testing.T, pool *pgxpool.Pool, key string, from phase.Phase, reason record.HoldReason, place record.TaskPlace) {
	t.Helper()
	inTx(t, pool, func(tx pgx.Tx) {
		ctx, records := context.Background(), record.NewStore()
		held, err := records.Issue(ctx, tx, key)
		if err != nil || held == nil {
			t.Fatalf("read %s: %v", key, err)
		}
		held.Phase, held.Hold = phase.Held, &record.Hold{From: from, Reason: reason}
		project, err := claim.ProjectToken(testProject)
		if err != nil {
			t.Fatal(err)
		}
		architect, err := claim.NewToken(project, key, claim.RoleArchitect)
		if err != nil {
			t.Fatal(err)
		}
		v := record.PlanVersion{Issue: key, Generation: held.Generation, Version: 2, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner, GivenBy: architect, GivenAt: fixedNow}
		task := record.PlanTask{Issue: key, Generation: held.Generation, Task: "a", PlanVersion: 2, BlockText: "Build the widget.", State: record.TaskWaiting}
		steps := []record.PlanStep{
			{Issue: key, Generation: held.Generation, Task: "a", Position: 1, Role: "implementer", Type: "implementation"},
			{Issue: key, Generation: held.Generation, Task: "a", Position: 2, Role: "tester", Type: "testing"},
		}
		for _, put := range []error{
			records.PutIssue(ctx, tx, *held),
			records.PutPlan(ctx, tx, v, []record.PlanTask{task}, steps),
			records.MoveTask(ctx, tx, key, held.Generation, "a", place, record.TaskRunning),
		} {
			if put != nil {
				t.Fatalf("seed the hold of %s: %v", key, put)
			}
		}
		if reason == record.HoldTaskStopped {
			if err := records.StopTask(ctx, tx, key, held.Generation, "a", "task a of "+key+" was stopped by its owner"); err != nil {
				t.Fatalf("stop task a of %s: %v", key, err)
			}
		}
	})
}

// workStarts is how many supervise starts of issue carry a task: the work of a phase, which a
// task-less start of a role that has run (startMidPhaseMembers) does not give.
func workStarts(t *testing.T, pool *pgxpool.Pool, issue string) int {
	t.Helper()
	n := 0
	for _, effect := range effects(t, pool) {
		if request, ok := effect.payload.(record.SuperviseRequest); ok && effect.issue == issue && request.Op == "start" && request.Task != "" {
			n++
		}
	}
	return n
}

// The fifth path that resumes work, a re-admission's promotion (startMidPhaseMembers), read
// against the two holds of an owner's plan (C2-1; workflow's TestAStoppedTaskAndAHeldParentGetNoWork
// reads the other four). A member held in either has no owner, since held has no role
// (phase.RoleFor), so no phase worker of it is started and no start carries it work; a role of it
// that has run comes back with no task, as each held member's does today. Nor does its step start
// again: the stopped task's step ended stopped when its owner stopped it, and it stays so until a
// later version of the plan starts the task again, and a parent held for its children has started
// no retro (workflow's StartStepRecord). The twin is the same tree with no plan rows: its member
// held from testing by a failed claim and its member in retro each start their step again, and the
// one in retro gets its implementer's phase work, as today (R20).
func TestAStoppedTaskAndAHeldParentGetNoWorkFromAPromotion(t *testing.T) {
	t.Run("a stopped task and a parent held for its children", func(t *testing.T) {
		pool, admission, engine := seedFinishingTree(t)
		countStart(t, pool, engine, heldTesting)
		inTx(t, pool, func(tx pgx.Tx) {
			stood := issue(t, pool, heldTesting)
			if err := engine.(*workflow.Engine).EndRunningStep(context.Background(), tx, stood); err != nil {
				t.Fatalf("end the stopped step of %s: %v", heldTesting, err)
			}
		})
		holdByPlan(t, pool, heldTesting, phase.Testing, record.HoldTaskStopped, record.TaskPlace{Kind: record.PlaceStep, Position: 2})
		holdByPlan(t, pool, inRetro, phase.Retro, record.HoldForChildren, record.TaskPlace{Kind: record.PlaceRetro})
		closeFinishingTree(t, pool, admission, engine)
		reopenFinishingTree(t, pool, admission, engine)

		if root := issue(t, pool, finishedRoot); root.Generation != 2 || root.Lingers() {
			t.Fatalf("the reopened root = %#v, want generation 2, no longer lingering: the promotion this case reads did not run", root)
		}
		assertStepWrites(t, pool, heldTesting, start("step", 3, 1, 1), end("stopped", "step", 3, 1, 1))
		assertStepWrites(t, pool, inRetro)
		for _, held := range []struct {
			key    string
			from   phase.Phase
			reason record.HoldReason
		}{{heldTesting, phase.Testing, record.HoldTaskStopped}, {inRetro, phase.Retro, record.HoldForChildren}} {
			if n := workStarts(t, pool, held.key); n != 0 {
				t.Errorf("the promotion wrote %d starts carrying work for %s, want none", n, held.key)
			}
			if got := issue(t, pool, held.key); got.Phase != phase.Held || got.Hold == nil || *got.Hold != (record.Hold{From: held.from, Reason: held.reason}) {
				t.Errorf("%s after the promotion = %s with hold %+v, want held from %s for %s still", held.key, got.Phase, got.Hold, held.from, held.reason)
			}
		}
	})

	t.Run("the same tree with no plan rows, as today", func(t *testing.T) {
		pool, admission, engine := seedFinishingTree(t)
		countStart(t, pool, engine, heldTesting)
		countStart(t, pool, engine, inRetro)
		closeFinishingTree(t, pool, admission, engine)
		reopenFinishingTree(t, pool, admission, engine)

		assertStepWrites(t, pool, heldTesting, start("step", 3, 1, 1), end("stopped", "step", 3, 1, 1), start("step", 3, 1, 2))
		assertStepWrites(t, pool, inRetro, start("retro", 0, 1, 1), end("stopped", "retro", 0, 1, 1), start("retro", 0, 1, 2))
		if n := phaseStarts(t, pool, inRetro, claim.RoleImplementer, phase.Retro); n != 1 {
			t.Fatalf("the promotion started %s's implementer in retro %d times, want once", inRetro, n)
		}
	})
}
