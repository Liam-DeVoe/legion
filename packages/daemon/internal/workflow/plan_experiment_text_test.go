package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
)

// resumeOf is the resume task the worker of key's phase is told now (ResumeTask), the start task
// Legion composes next for the issue's running task.
func resumeOf(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string) string {
	t.Helper()
	var resume string
	factTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
		issue, err := engine.store.Issue(ctx, tx, key)
		if err != nil {
			return err
		}
		resume, err = engine.ResumeTask(ctx, tx, *issue)
		return err
	})
	return resume
}

// An issue whose group takes no plan from its owner (the variant b of planning, which runs the
// project's default steps) takes no version that changes only the text of its running task either:
// the arms stay alike, and a decision for an agent there goes by message. Such a version is
// answered PLAN_NOT_TAKEN and changes nothing, and the next start task of the running task holds
// the text it held before. The control is an issue of the group whose owner gives the plan: the
// same kind of version is taken, and the next start task holds the new text.
func TestTheGroupDefaultTakesNoVersionThatChangesOnlyTheRunningTasksText(t *testing.T) {
	t.Run("the group default", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		drawnInto(t, pool, root.Key, "planning", "b", "default")
		firstStep(t, pool, engine, root.Key)
		before := planStandingOf(t, pool, engine, root.Key, root.Generation)
		stored := roleRowOf(t, pool, engine, root.Key, claim.RolePlanner).EntryTask
		id := planID(root.Key, root.Generation, 1)
		given := ownersPlan(t, root.Key, root.Generation, 1, plan.Task{ID: plan.DefaultTask, Text: "Text two"})

		assertPlanRefusal(t, pool, engine, root.Key, root.Generation, id, applyFacts(t, pool, engine)(id, given), planNotTakenOfB(root.Key), before)
		if after := roleRowOf(t, pool, engine, root.Key, claim.RolePlanner).EntryTask; after != stored {
			t.Fatalf("the planner's stored start task after the refused version = %q, want it as it was: %q", after, stored)
		}
		if resume := resumeOf(t, pool, engine, root.Key); strings.Contains(resume, "Text two") || !strings.Contains(resume, "\nThe project's default steps.\n") {
			t.Fatalf("the next start task of %s = %q, want the default task's text and not the refused version's", root.Key, resume)
		}
	})

	t.Run("the control: the group owner takes it", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		apply := applyFacts(t, pool, engine)
		root := stepRoot(t, pool, phase.Admitted)
		drawnInto(t, pool, root.Key, "planning", "a", "owner")
		mustTake(t, "version 1", apply(planID(root.Key, root.Generation, 1),
			ownersPlan(t, root.Key, root.Generation, 1, givenTask(t, "Text one", "implementer", "reviewer"))))
		firstStep(t, pool, engine, root.Key)

		mustTake(t, "version 2 with the text alone changed", apply(planID(root.Key, root.Generation, 2),
			ownersPlan(t, root.Key, root.Generation, 2, givenTask(t, "Text two", "implementer", "reviewer"))))
		if resume := resumeOf(t, pool, engine, root.Key); !strings.Contains(resume, "\nText two\n") || strings.Contains(resume, "Text one") {
			t.Fatalf("the next start task of %s = %q, want version 2's text and not version 1's", root.Key, resume)
		}
	})
}
