package workflow

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// Walk is the line of phases an issue's run walks, and where its task stands on it. A run with
// plan rows (record.Store.Plan at the issue's generation) walks the line of its one task's steps
// (plan.Line): each step runs as the phase of its role, then Legion's own retro and merge. A run
// with none, which is every issue that ran before plan rows existed, walks today's line,
// phase.Order, with Stored false, and its table is Table, row for row (R20). Every behaviour that
// differs from today reads Stored and nothing else.
//
// Line and Steps are read only: an unstored walk's Line is phase.Order itself.
type Walk struct {
	// Stored says whether the run has plan rows.
	Stored bool
	// Task is the id of the run's one task: the plan's, or defaultTask where the run has none,
	// which is the task its steps are recorded under (StepID).
	Task string
	// Steps are the task's steps, by place from 1; nil where the run has no plan rows.
	Steps []record.PlanStep
	// Line is the phases the task walks: one for each step, then retro and merging.
	Line []phase.Phase
	// State and Place are where the task stands (record.PlanTask); zero where the run has no
	// plan rows, whose place is the issue's phase alone.
	State string
	Place record.TaskPlace
}

// builtinDeployment is the roles a step of this release runs as (plan.BuiltinRoles): the one
// Deployment the walk reads a stored step's phase from.
var builtinDeployment = plan.Deployment{Roles: plan.BuiltinRoles()}

// walk is the walk of issue's run: its plan rows, or today's line with Stored false. A run whose
// rows hold no task or more than one, or a step whose role runs as no phase, is an error: no plan
// this release takes writes one (plan.Validate, plan.Supported).
func (e *Engine) walk(ctx context.Context, tx pgx.Tx, issue record.Issue) (Walk, error) {
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil || stored == nil {
		return Walk{Task: defaultTask, Line: phase.Order}, err
	}
	if len(stored.Tasks) != 1 {
		return Walk{}, fmt.Errorf("walk %s at generation %d: its plan holds %d tasks, and this release walks one", issue.Key, issue.Generation, len(stored.Tasks))
	}
	task := stored.Tasks[0]
	steps := stored.Steps[task.Task]
	named := make([]plan.Step, len(steps))
	for i, step := range steps {
		named[i] = plan.Step{Role: step.Role, Key: step.AgentKey}
	}
	line := plan.Line(named, builtinDeployment)
	if at := slices.Index(line, ""); at >= 0 {
		return Walk{}, fmt.Errorf("walk %s at generation %d: step %d of task %s names the role %q, which runs as no phase", issue.Key, issue.Generation, at+1, task.Task, steps[at].Role)
	}
	return Walk{Stored: true, Task: task.Task, Steps: steps, Line: line, State: task.State, Place: task.Place}, nil
}

// PlaceOf is where the task stands while its issue stands in p: the step of the line that runs
// as p, at its place from 1, or the retro or the merge at place 0. A phase the line does not hold
// has no place.
func (w Walk) PlaceOf(p phase.Phase) (record.TaskPlace, bool) {
	switch p {
	case phase.Retro:
		return record.TaskPlace{Kind: record.PlaceRetro}, true
	case phase.Merging:
		return record.TaskPlace{Kind: record.PlaceMerge}, true
	}
	if at := slices.Index(w.steps(), p); at >= 0 {
		return record.TaskPlace{Kind: record.PlaceStep, Position: at + 1}, true
	}
	return record.TaskPlace{}, false
}

// steps is the part of the line each step runs: all of it but the retro and the merge.
func (w Walk) steps() []phase.Phase {
	if len(w.Line) < 2 {
		return nil
	}
	return w.Line[:len(w.Line)-2]
}

// issuePhaseOf is the phase an issue whose one task stands at place in state stands in (A-8): done
// for a task that is done or left the workflow before it was done (record.TaskLeft), whatever its
// place, which is the phase of an issue out of the workflow; the phase of the step at place, retro
// or merging at the task's retro or merge; and admitted for a task at no act, which has not
// started. A task stopped or held keeps its place, and the hold overlays this phase
// (record.Hold.From). A step place the line does not hold derives no phase.
func issuePhaseOf(w Walk, place record.TaskPlace, state string) phase.Phase {
	if state == record.TaskDone || state == record.TaskLeft {
		return phase.Done
	}
	switch place.Kind {
	case record.PlaceRetro:
		return phase.Retro
	case record.PlaceMerge:
		return phase.Merging
	case record.PlaceStep:
		if steps := w.steps(); place.Position >= 1 && place.Position <= len(steps) {
			return steps[place.Position-1]
		}
		return ""
	}
	return phase.Admitted
}

// moveTask is the one writer of where a task stands (C8): it moves w's task to the place of to in
// state (record.Store.MoveTask), keeping its place on a move to done, and sets issue.Phase to the
// phase that place derives (issuePhaseOf), in the caller's transaction and before the caller's
// write of issue (record.Store.PutIssue), so the two never disagree in the store. w then stands
// where the task does, for whatever the caller reads of it after the move. A run with no plan rows
// has no task to move, and issue.Phase is to, as it always was. A move to where the task already
// stands writes nothing. The caller clears or keeps issue.Hold itself.
//
// A write of issues.phase that does not move the task keeps its place and says so where it is
// made: a hold, which overlays held on the derived phase (claimFailed); the retry of a hold, which
// puts back Hold.From, the derived phase (retryPhase); a root's wait on a person before its first
// step (enterAwaiting); a root the operator's tree holds, whose task never started
// (leaveUnderOperator); and an adopted member parked at a new run (moveIntoTree).
func (e *Engine) moveTask(ctx context.Context, tx pgx.Tx, issue *record.Issue, w *Walk, to phase.Phase, state string) error {
	if !w.Stored {
		issue.Phase = to
		return nil
	}
	place := w.Place
	if to != phase.Done {
		var ok bool
		if place, ok = w.PlaceOf(to); !ok {
			return fmt.Errorf("move task %s of %s to %s: its line %v holds no such phase", w.Task, issue.Key, to, w.Line)
		}
	}
	if place != w.Place || state != w.State {
		if err := e.store.MoveTask(ctx, tx, issue.Key, issue.Generation, w.Task, place, state); err != nil {
			return err
		}
		w.Place, w.State = place, state
	}
	issue.Phase = issuePhaseOf(*w, place, state)
	return nil
}

// row is the row of w's table that moves an issue standing in from on trigger, into target where
// a target is named, whose guard admits snapshot.
func (w Walk) row(from phase.Phase, trigger TriggerKind, target phase.Phase, snapshot Snapshot) (Row, bool) {
	for _, row := range w.table() {
		if row.From == from && row.Trigger == trigger && (target == "" || row.To == target) && (row.Guard == nil || row.Guard(snapshot)) {
			return row, true
		}
	}
	return Row{}, false
}

// table is the table of w's line: Table for a run with no plan rows, which is TableOf(phase.Order)
// built once, and the table of the task's line otherwise.
func (w Walk) table() []Row {
	if !w.Stored {
		return Table
	}
	return TableOf(w.Line)
}
