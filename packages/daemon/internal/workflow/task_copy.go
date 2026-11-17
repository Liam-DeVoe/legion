package workflow

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// startTaskCopy gives task of issue's plan its branch at a start of the task, in the transaction
// that starts it (record.Store.AssignTaskBranch: once, the issue's own branch for the first task of
// the generation to start, else the first give of the task's slug no task of the generation took).
// A task whose branch is not the issue's own, or that needs other tasks, gets one task_branch row
// (record.TaskBranch), which makes its branch on GitHub at the joined work of its needs before any
// start of the issue's roles goes out. The task that holds the issue's own branch and needs none
// queues nothing: the issue_branch row its first step queues makes that branch (gateOpened). A
// task the plan does not hold is refused, naming it.
func (e *Engine) startTaskCopy(ctx context.Context, tx pgx.Tx, issue record.Issue, task string) error {
	branch, err := e.store.AssignTaskBranch(ctx, tx, issue.Key, issue.Generation, task)
	if err != nil {
		return err
	}
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil {
		return err
	}
	at := -1
	if stored != nil {
		at = slices.IndexFunc(stored.Tasks, func(row record.PlanTask) bool { return row.Task == task })
	}
	if at < 0 {
		return fmt.Errorf("start the copy of task %s of %s at generation %d: the plan holds no such task", task, issue.Key, issue.Generation)
	}
	if branch == claim.IssueBranch(issue.Key) && len(stored.Tasks[at].Needs) == 0 {
		return nil
	}
	return e.enqueue(ctx, tx, issue.Key, record.TaskBranch{Generation: issue.Generation, Task: task})
}
