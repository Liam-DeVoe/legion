package workflow

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// view is the copy of issue read for task of its run's plan, and the walk of that task: the copy
// stands in the task's own phase and hold (taskStanding), not the issue's, and names the task
// (record.Issue.ViewTask), so a handler of a fact that concerns one step reads it once and then
// runs as it runs on an issue of one task. An issue whose plan holds one task, or none, is its own
// view and walks as it always did, whatever task is named: its walk is its run's (walk). A task a
// plan of several does not hold is an error.
func (e *Engine) view(ctx context.Context, tx pgx.Tx, issue record.Issue, task string) (record.Issue, Walk, error) {
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil {
		return record.Issue{}, Walk{}, err
	}
	view, w, ok, err := viewOn(issue, stored, task)
	if err == nil && !ok {
		err = fmt.Errorf("read %s at generation %d for task %s: its plan holds no such task", issue.Key, issue.Generation, task)
	}
	return view, w, err
}

// viewOn is view on stored, the plan of issue's run, which the caller read: ok is false where its
// plan holds several tasks and none is task.
func viewOn(issue record.Issue, stored *record.StoredPlan, task string) (record.Issue, Walk, bool, error) {
	issue.ViewTask = ""
	if stored == nil || len(stored.Tasks) <= 1 {
		if stored == nil {
			return issue, todaysWalk(), true, nil
		}
		w, err := walkIn(issue, stored)
		return issue, w, err == nil, err
	}
	at := slices.IndexFunc(stored.Tasks, func(row record.PlanTask) bool { return row.Task == task })
	if at < 0 {
		return issue, Walk{}, false, nil
	}
	w := walkOfTask(issue.Key, stored, at, mergingTask(stored))
	issue.ViewTask = task
	issue.Phase, issue.Hold = taskStanding(w)
	return issue, w, true, nil
}

// viewsOf is the copy of issue read for each task of its run's plan, in task id order, with the
// walk of that task: a reader that reads every step of the issue reads each of them. An issue
// whose plan holds one task, or none, is its one view.
func (e *Engine) viewsOf(ctx context.Context, tx pgx.Tx, issue record.Issue) ([]record.Issue, []Walk, error) {
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil {
		return nil, nil, err
	}
	if stored == nil || len(stored.Tasks) <= 1 {
		view, w, _, err := viewOn(issue, stored, "")
		if err != nil {
			return nil, nil, err
		}
		return []record.Issue{view}, []Walk{w}, nil
	}
	views, walks := make([]record.Issue, len(stored.Tasks)), make([]Walk, len(stored.Tasks))
	for at, task := range stored.Tasks {
		if views[at], walks[at], _, err = viewOn(issue, stored, task.Task); err != nil {
			return nil, nil, err
		}
	}
	return views, walks, nil
}

// putIssue writes issue, the issue itself or a copy of it read for one of its tasks (view), and
// answers the issue as written, which a caller that then asks where the issue stands reads. An
// issue whose plan holds one task, or none, is written as it is given (writeIssueOn's check). One
// whose plan holds several stands where its tasks put it (issuePhaseOfTasks): a copy read for a
// task is written as the issue in that phase and hold, with the Dispatch status of that phase
// where it has one, and read for none; and the issue itself, read for none, is refused where its
// phase or hold is not theirs, since issues.phase is derived from its tasks and from nothing else.
// A root that waits on a person before its first step stands in awaiting_person over the admitted
// its tasks derive.
func (e *Engine) putIssue(ctx context.Context, tx pgx.Tx, issue record.Issue) (record.Issue, error) {
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil {
		return record.Issue{}, err
	}
	if stored == nil || len(stored.Tasks) <= 1 {
		issue.ViewTask = ""
		w := todaysWalk()
		if stored != nil {
			if w, err = walkIn(issue, stored); err != nil {
				return record.Issue{}, err
			}
		}
		return issue, e.writeIssueOn(ctx, tx, issue, w)
	}
	at, hold := issuePhaseOfTasks(walksOf(issue.Key, stored))
	if at == phase.Admitted && issue.Phase == phase.AwaitingPerson {
		at = phase.AwaitingPerson
	}
	if issue.ViewTask == "" {
		if issue.Phase != at || !sameHold(issue.Hold, hold) {
			return record.Issue{}, fmt.Errorf("put %s at generation %d: it stands in %s (hold %+v), and its %d tasks put it in %s (hold %+v)",
				issue.Key, issue.Generation, issue.Phase, issue.Hold, len(stored.Tasks), at, hold)
		}
		return issue, e.store.PutIssue(ctx, tx, issue)
	}
	issue.ViewTask, issue.Phase, issue.Hold = "", at, hold
	if status := PhaseStatus(at); status != "" && status != issue.Status {
		if err := e.status(ctx, tx, issue, status, ""); err != nil {
			return record.Issue{}, err
		}
		issue.Status = status
	}
	if issue.CarriesRun() {
		issue.TakenBack = true
	}
	return issue, e.store.PutIssue(ctx, tx, issue)
}

// sameHold says whether a and b are one hold: none, or one from the same phase for the same reason.
func sameHold(a, b *record.Hold) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
