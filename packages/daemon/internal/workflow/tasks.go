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

// partsStart says whether the parts of task, a task of stored, may start: it runs at its parts,
// where its sub-owner acts, and its step waits for nothing, so the join of its needs has ended
// (record.StepWaitJoin) and each part starts from the joined work of those needs. A task that
// waits, is stopped or held, waits for its join, or stands at its report holds its parts back.
func partsStart(stored *record.StoredPlan, task string) bool {
	row, ok := stored.TaskOf(task)
	return ok && row.State == record.TaskRunning && row.Place.Kind == record.PlaceParts && row.StepWait == record.StepWaitNone
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
// moves each task that waits and each of whose needs is done, in task id order, to the place its
// start puts it (Walk.startPlace): its first step, or the parts of a task with a sub-owner, where
// its sub-owner acts. The task runs there (moveTask, which writes that its step waits for its
// join); startReady writes the issue (putScope) and starts the step's role through Engine.start,
// the start of every step, which gives the task its branch and joins its needs (startTaskCopy). A
// part of a task (record.PlanTask.PartOf) starts only while that task runs at its parts and waits
// for nothing (partsStart), so a part never starts before the join of its task's needs ended: the
// end of that join starts it (taskJoined). Where a task started at its parts with no wait, its
// parts that wait start in the same call. It answers the issue as written. A plan of one task, or
// none, starts nothing here: its one task starts at the gate's opening as before (transition),
// and so the issue is answered as given. It runs at the first step of the work (gateOpened), at
// each move of a task to done (transition), at each version of the plan taken after the work
// started (afterVersion), after a stopped task starts again or waits (startAtFirstStep, through
// afterVersion), and at the end of the join of a task at its parts (taskJoined). No setting bounds
// how many tasks run at one time: the plan bounds it with each task's needs and parts.
func (e *Engine) startReady(ctx context.Context, tx pgx.Tx, issue record.Issue) (record.Issue, error) {
	written := issue
	for {
		stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
		if err != nil || stored == nil || len(stored.Tasks) <= 1 {
			return written, err
		}
		var ready []string
		for _, row := range stored.Tasks {
			if row.State == record.TaskWaiting && needsDone(stored, row) && (row.PartOf == "" || partsStart(stored, row.PartOf)) {
				ready = append(ready, row.Task)
			}
		}
		// atParts says whether a task of ready started at its parts, whose own parts can start
		// once it waits for nothing: the plan is read again for them.
		atParts := false
		for _, id := range ready {
			s, err := e.ScopeOf(ctx, tx, written, id)
			if err != nil {
				return record.Issue{}, err
			}
			// The step's text names the pull request of its own task (taskPullRequest), none for a
			// task that has not opened one.
			pr, err := e.taskPullRequest(ctx, tx, s)
			if err != nil {
				return record.Issue{}, err
			}
			to, first, ok := s.walk.startPlace()
			if !ok {
				return record.Issue{}, fmt.Errorf("start task %s of %s: its line %v holds no first step", id, issue.Key, s.walk.Line)
			}
			if err := e.moveTask(ctx, tx, &s, to, first, record.TaskRunning); err != nil {
				return record.Issue{}, err
			}
			s.issue.Hold = nil
			if written, err = e.putScope(ctx, tx, &s); err != nil {
				return record.Issue{}, err
			}
			if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
				return record.Issue{}, err
			}
			e.logOnCommit(ctx, "workflow: a task whose needs are done starts", "issue", issue.Key, "tree", issue.Tree, "task", id,
				"needs", s.walk.Needs, "to", to, "place", placeWords(first), "issuePhase", written.Phase)
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
			atParts = atParts || first.Kind == record.PlaceParts
		}
		if !atParts {
			return written, nil
		}
	}
}
