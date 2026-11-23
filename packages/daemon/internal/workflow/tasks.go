package workflow

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// sameHold says whether a and b are one hold: none, or one from the same phase for the same reason.
func sameHold(a, b *record.Hold) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// needsDone says whether each task that task needs is done in stored, the plan of its run. A need
// the plan does not hold is never done: a later version that removes a task another needs is
// refused (plan.Validate).
func needsDone(stored *record.StoredPlan, task record.PlanTask) bool {
	for _, need := range task.Needs {
		if row, ok := stored.TaskOf(need); !ok || row.State != record.TaskDone {
			return false
		}
	}
	return true
}

// lastToFinish says whether the end of s's task ends its issue: each other task of the plan of s's
// run, as the store holds it in tx, is done or left the workflow (record.TaskLeft). That holds for a
// plan of one task, and for a run with no plan rows, whose one walk is the issue's own. It is the one
// statement of "this task's end ends the issue", which two readers ask before they wait for the
// issues beneath: the close of a merge (closeMerged) and the hold before a retro (beneathRetro).
// Every other task's merge, and its retro, waits for nothing beneath.
func (e *Engine) lastToFinish(ctx context.Context, tx pgx.Tx, s TaskScope) (bool, error) {
	if s.plan == nil {
		return true, nil
	}
	stored, err := e.store.Plan(ctx, tx, s.issue.Key, s.issue.Generation)
	if err != nil || stored == nil {
		return true, err
	}
	return !slices.ContainsFunc(stored.Tasks, func(row record.PlanTask) bool {
		return row.Task != s.walk.Task && row.State != record.TaskDone && row.State != record.TaskLeft
	}), nil
}

// tasksWaitingOn is each task of stored, the plan of a run, that needs task, itself or through
// other tasks, and is neither done nor left, in task id order: the tasks a stop of task holds,
// while each other task goes on. None for no plan.
func tasksWaitingOn(stored *record.StoredPlan, task string) []string {
	if stored == nil {
		return nil
	}
	needing := map[string]bool{task: true}
	for grew := true; grew; {
		grew = false
		for _, row := range stored.Tasks {
			if needing[row.Task] {
				continue
			}
			if slices.ContainsFunc(row.Needs, func(need string) bool { return needing[need] }) {
				needing[row.Task], grew = true, true
			}
		}
	}
	var waiting []string
	for _, row := range stored.Tasks {
		if row.Task != task && needing[row.Task] && row.State != record.TaskDone && row.State != record.TaskLeft {
			waiting = append(waiting, row.Task)
		}
	}
	return waiting
}

// startReady is the one place tasks of a plan of several start, in the caller's transaction: it
// moves each task that waits and each of whose needs is done, in task id order, to its first step,
// running (moveTask, which writes that the step waits for its join), writes the issue (putScope)
// and starts the step's role through Engine.start, the start of every step, which gives the task
// its branch and joins its needs (startTaskCopy). It answers the issue as written. A
// plan of one task, or none, starts nothing here: its one task starts at the gate's opening as
// before (transition), and so the issue is answered as given. It runs at the first step of the
// work (gateOpened), at each move of a task to done (transition), at each version of the plan taken
// after the work started (afterVersion), and after a stopped task starts again or waits
// (startAtFirstStep, through afterVersion). No setting bounds how many tasks run at one time: the
// plan bounds it with each task's needs.
func (e *Engine) startReady(ctx context.Context, tx pgx.Tx, issue record.Issue) (record.Issue, error) {
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil || stored == nil || len(stored.Tasks) <= 1 {
		return issue, err
	}
	var ready []string
	for _, row := range stored.Tasks {
		if row.State == record.TaskWaiting && needsDone(stored, row) {
			ready = append(ready, row.Task)
		}
	}
	if len(ready) == 0 {
		return issue, nil
	}
	written := issue
	for _, id := range ready {
		s, err := e.ScopeOf(ctx, tx, written, id)
		if err != nil {
			return record.Issue{}, err
		}
		// The step's text names the pull request of its own task (taskPullRequest), none for a task
		// that has not opened one.
		pr, err := e.taskPullRequest(ctx, tx, s)
		if err != nil {
			return record.Issue{}, err
		}
		steps := s.walk.steps()
		if len(steps) == 0 {
			return record.Issue{}, fmt.Errorf("start task %s of %s: its line %v holds no first step", id, issue.Key, s.walk.Line)
		}
		first := record.TaskPlace{Kind: record.PlaceStep, Position: 1}
		if err := e.moveTask(ctx, tx, &s, steps[0], first, record.TaskRunning); err != nil {
			return record.Issue{}, err
		}
		s.issue.Hold = nil
		if written, err = e.putScope(ctx, tx, &s); err != nil {
			return record.Issue{}, err
		}
		if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
			return record.Issue{}, err
		}
		e.logOnCommit(ctx, "workflow: a task whose needs are done starts at its first step", "issue", issue.Key, "tree", issue.Tree, "task", id,
			"needs", s.walk.Needs, "to", steps[0], "issuePhase", written.Phase)
		role := s.walk.RoleAt(first)
		if err := e.clearHandoff(ctx, tx, issue.Key, role); err != nil {
			return record.Issue{}, err
		}
		view := s.issue
		if err := e.start(ctx, tx, s, role, func(at Standing) string {
			return task(view, at, record.PhaseRow{}, pr, "")
		}, false); err != nil {
			return record.Issue{}, err
		}
	}
	return written, nil
}
