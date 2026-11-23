package workflow

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// wayNone is the way of a fix a stop, a leave or a new generation ended (record.TaskFix.Way): it
// takes none of R7a's three, and no later end of a step takes one for it (afterFix, besideFix).
const wayNone = 4

// dropFixWay is the one act of a stop of a task, a leave of its issue and a new generation of its
// run on the task's fix: the latest fix of w's task of issue whose way is not taken (Way 0), in
// whatever state, takes no way (wayNone) in one write. A fix that reads its job or runs ends
// stopped at the task's head (endFix), so a later read of its job starts nothing and the task's
// next failure may start a fix of its own; where it ran, its claim gets the step_stop the running
// step of a stop gets, said as said ("" for a leave or a new generation, whose claims their own
// acts stop). A fix that ended with its way pending keeps its result and its end. A fix whose way
// is taken is left as it is.
func (e *Engine) dropFixWay(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, said string) error {
	if !w.Stored {
		return nil
	}
	fix, err := e.store.LatestFix(ctx, tx, issue.Key, issue.Generation, w.Task)
	if err != nil || fix == nil || fix.Way != 0 {
		return err
	}
	fix.Way = wayNone
	if fix.State == record.FixEnded {
		return e.store.UpdateFix(ctx, tx, *fix)
	}
	ran := fix.State == record.FixRunning
	pr, err := e.store.PullRequest(ctx, tx, issue.Key)
	if err != nil {
		return err
	}
	_, head := taskHead(pr)
	if err := e.endFix(ctx, tx, issue, w, fix, StepEnd{Result: ResultStopped}, head); err != nil {
		return err
	}
	if !ran || said == "" {
		return nil
	}
	return e.enqueue(ctx, tx, issue.Key, record.SuperviseRequest{Op: "step_stop", Tree: issue.Tree, Role: claim.RoleImplementer,
		Generation: issue.Generation, Reason: said})
}

// dropRunFix is dropFixWay on the walk of issue's run as the store holds it.
func (e *Engine) dropRunFix(ctx context.Context, tx pgx.Tx, issue record.Issue, said string) error {
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return err
	}
	return e.dropFixWay(ctx, tx, issue, w, said)
}
