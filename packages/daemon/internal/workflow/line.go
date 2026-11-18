package workflow

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/legion/daemon/internal/claim"
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
	// Text is the text of the task's block (record.PlanTask.BlockText), which a step's worker is
	// told (standingPart); "" where the run has no plan rows.
	Text string
	// Steps are the task's steps, by place from 1; nil where the run has no plan rows.
	Steps []record.PlanStep
	// Line is the phases the task walks: one for each step, then retro and merging.
	Line []phase.Phase
	// State and Place are where the task stands (record.PlanTask); zero where the run has no
	// plan rows, whose place is the issue's phase alone.
	State string
	Place record.TaskPlace
	// Version is the version of the plan in force (record.StoredPlan.Version): where the plan came
	// from, its document and its number, which the run's plan node names (planNode); zero where
	// the run has no plan rows.
	Version record.PlanVersion
}

// walk is the walk of issue's run: its plan rows, or today's line with Stored false. Each step
// runs as the phase of its role (plan.RunsAs). A run whose rows hold no task or more than one is
// an error: no plan this release takes writes one (plan.Validate, plan.Supported).
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
	line := plan.Line(named)
	return Walk{Stored: true, Task: task.Task, Text: task.BlockText, Steps: steps, Line: line, State: task.State, Place: task.Place,
		Version: stored.Version}, nil
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

// MovesBack says whether row moves w's task back by the task's own order. A stored walk compares
// the row's two places (placeRank): an earlier step by its place, every step before the retro, the
// retro before the merge, so a move from step 3 back to step 2 goes back though both run as step.
// A run with no plan rows walks today's line, so this is phase.MovesBack of the row's two phases
// for it, as it always was. A task in another order than today's so counts its move back from
// testing to reviewing (the steps `implementer reviewer tester`), and not the review's approval
// from reviewing to testing, which moves it on.
func (w Walk) MovesBack(row Row) bool {
	if !w.Stored {
		return phase.MovesBack(row.From, row.To)
	}
	return row.ToPlace.Kind != "" && placeRank(row.ToPlace) < placeRank(row.FromPlace)
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

// moveTask is the one writer of where a task stands (C8): it moves w's task to place in state
// (record.Store.MoveTask), the place of the row that moves it (a move to done passes the place the
// task keeps), and sets issue.Phase to the phase that place derives (issuePhaseOf), which must be
// to, and issue.StepRole to the role of the step at that place ("" at the retro, the merge or no
// act), in the caller's transaction and before the caller's write of issue
// (record.Store.PutIssue), so the two never disagree in the store and a caller that reads the
// issue after the move reads the role a fresh read gives. w then stands where the task does, for
// whatever the caller reads of it after the move. A run with no plan rows has no task to move, and
// issue.Phase is to, as it always was. A move to where the task already stands writes nothing. The
// caller clears or keeps issue.Hold itself.
//
// A write of issues.phase that does not move the task keeps its place and says so where it is
// made: a hold, which overlays held on the derived phase (claimFailed); the retry of a hold, which
// puts back Hold.From, the derived phase (retryPhase); a root's wait on a person before its first
// step (enterAwaiting); a root the operator's tree holds, whose task never started
// (leaveUnderOperator); and an adopted member parked at a new run (moveIntoTree).
func (e *Engine) moveTask(ctx context.Context, tx pgx.Tx, issue *record.Issue, w *Walk, to phase.Phase, place record.TaskPlace, state string) error {
	if !w.Stored {
		issue.Phase = to
		return nil
	}
	if derived := issuePhaseOf(*w, place, state); derived != to {
		return fmt.Errorf("move task %s of %s to %s at %+v: that place of its line %v runs as %q", w.Task, issue.Key, to, place, w.Line, derived)
	}
	if place != w.Place || state != w.State {
		if err := e.store.MoveTask(ctx, tx, issue.Key, issue.Generation, w.Task, place, state); err != nil {
			return err
		}
		w.Place, w.State = place, state
	}
	issue.Phase = to
	issue.StepRole = ""
	if place.Kind == record.PlaceStep && place.Position >= 1 && place.Position <= len(w.Steps) {
		issue.StepRole = claim.Role(w.Steps[place.Position-1].Role)
	}
	return nil
}

// row is the row of w's table that moves an issue standing in from on trigger, into target where
// a target is named (the zero place names none), whose guard admits snapshot. A stored walk takes
// the row out of the place its task stands at (Row.FromPlace) and into target (Row.ToPlace), so
// two steps that run as one phase take their own rows; its table holds a return from each step
// to each step before it (StoredTableOf), so a failed step returns its work to the step its plan
// reads (R7: returnTarget, or the step a completion names), which is an earlier step of the
// task's line but not always implementing. A walk with no plan rows reads no place of a row: its
// target is the phase that place derives on today's line.
func (w Walk) row(from phase.Phase, trigger TriggerKind, target record.TaskPlace, snapshot Snapshot) (Row, bool) {
	named := target != (record.TaskPlace{})
	for _, row := range w.table() {
		if row.From != from || row.Trigger != trigger || (row.Guard != nil && !row.Guard(snapshot)) {
			continue
		}
		if w.Stored && (row.FromPlace != w.Place || named && row.ToPlace != target) {
			continue
		}
		if !w.Stored && named && row.To != issuePhaseOf(w, target, record.TaskRunning) {
			continue
		}
		return row, true
	}
	return Row{}, false
}

// returnsWork says whether trigger is a failed step returning its task's work to an earlier step
// (R7): the tester's fail, a review's request for changes and the fail of a step that runs as
// step. A backward move names its own target, which a backward row of the line serves, and a
// conflicting head goes back to implementing whatever the plan says (TriggerConflict).
func returnsWork(trigger TriggerKind) bool {
	return trigger == TriggerTesterFailed || trigger == TriggerReviewRejected || trigger == TriggerStepFailed
}

// failsIn says whether trigger is the return of a step that runs as p (stepRows): the one fail a
// worker of p reports.
func failsIn(p phase.Phase, trigger TriggerKind) bool {
	for _, step := range stepRows {
		if step.from == p && step.trigger == trigger && step.to != "" {
			return true
		}
	}
	return false
}

// returnTarget is the place the failed step of w's task, standing at w.Place, returns the work to
// when the completion names none (R7): the first step before it whose recorded type is
// implementation (record.PlanStep.Type). ok is false where no step before it has that type: the
// task then stops (stopUnreturned). A step of a role the deployment defines may stand before its
// task's implementer, and a stored type that no plan gives may stand anywhere; each reaches that
// answer.
func (w Walk) returnTarget() (record.TaskPlace, bool) {
	if w.Place.Kind != record.PlaceStep || w.Place.Position < 1 || w.Place.Position > len(w.Steps) {
		return record.TaskPlace{}, false
	}
	for i, step := range w.Steps[:w.Place.Position-1] {
		if step.Type == string(plan.TypeImplementation) {
			return record.TaskPlace{Kind: record.PlaceStep, Position: i + 1}, true
		}
	}
	return record.TaskPlace{}, false
}

// returnAct is what names a step to go back to: a failed step's report (failReport) or a move back
// (backwardMove). Again is what the caller sends again, and Verb what the step at its place did.
type returnAct struct{ Again, Verb string }

var (
	failReport   = returnAct{Again: "the fail", Verb: "failed"}
	backwardMove = returnAct{Again: "the move", Verb: "moves back"}
)

// returnStep is the one judge of a named place: the place of step n of w's task, which the report
// or the move of the step standing at at names as the one its task returns to (R7, returnTo), or
// why it is refused, which changes nothing: an issue with no plan rows has no step to name, a
// place past the task's last step names no step of it, and the step at at itself or a later one is
// not earlier (the spec's words: a report that names a step of another task, or a step that is not
// earlier). The sentences take the route's shape for a returnTo it refuses before the workflow
// reads one (api's INVALID_RETURN_TO). An at that is no step of the task's line is an error: the
// route takes returnTo only of a step's worker, which stands at a step.
func returnStep(issue record.Issue, w Walk, at record.TaskPlace, n int, act returnAct) (record.TaskPlace, string, error) {
	if !w.Stored {
		return record.TaskPlace{}, fmt.Sprintf("returnTo is %d, and %s has no plan of tasks in Legion's record, so it has no step to name; send %s again without returnTo; nothing was recorded",
			n, issue.Key, act.Again), nil
	}
	if at.Kind != record.PlaceStep || at.Position < 1 || at.Position > len(w.Steps) {
		return record.TaskPlace{}, "", fmt.Errorf("judge returnTo %d of %s: %+v is no step of the line %v of its task %s", n, issue.Key, at, w.Line, w.Task)
	}
	role := w.Steps[at.Position-1].Role
	switch {
	case n > len(w.Steps):
		return record.TaskPlace{}, fmt.Sprintf("returnTo is %d, and task %s of %s has steps 1 to %d, so it names no step of the task whose step %d (%s) %s; send %s again naming an earlier step of that task, or without returnTo; nothing was recorded",
			n, w.Task, issue.Key, len(w.Steps), at.Position, role, act.Verb, act.Again), nil
	case n >= at.Position:
		return record.TaskPlace{}, fmt.Sprintf("returnTo is %d, and step %d (%s) of task %s of %s is not before step %d (%s), the step that %s; send %s again naming an earlier step, or without returnTo; nothing was recorded",
			n, n, w.Steps[n-1].Role, w.Task, issue.Key, at.Position, role, act.Verb, act.Again), nil
	}
	return record.TaskPlace{Kind: record.PlaceStep, Position: n}, "", nil
}

// placeOfRole is the place of the step of w's task whose role is role, ok false where none is: a
// role runs at most one step of a task.
func (w Walk) placeOfRole(role claim.Role) (record.TaskPlace, bool) {
	for i, step := range w.Steps {
		if claim.Role(step.Role) == role {
			return record.TaskPlace{Kind: record.PlaceStep, Position: i + 1}, true
		}
	}
	return record.TaskPlace{}, false
}

// backwardPlace is the place a move back of w's task to the phase to reaches, ok false where none
// serves it: for a walk with no plan rows the place of to on today's line, whose phases each stand
// once (PlaceOf); for a stored walk the one place before w.Place whose step, retro or merge runs
// as to. Where no earlier place runs as to, or more than one does (two steps that run as step), no
// backward row serves the move.
func (w Walk) backwardPlace(to phase.Phase) (record.TaskPlace, bool) {
	if !w.Stored {
		return w.PlaceOf(to)
	}
	var found []record.TaskPlace
	for at, p := range w.Line {
		if place := placeAt(w.Line, at); p == to && placeRank(place) < placeRank(w.Place) {
			found = append(found, place)
		}
	}
	if len(found) != 1 {
		return record.TaskPlace{}, false
	}
	return found[0], true
}

// table is the table of w's line: Table for a run with no plan rows, which is TableOf(phase.Order)
// built once, and the table of the task's line with its places otherwise (StoredTableOf).
func (w Walk) table() []Row {
	if !w.Stored {
		return Table
	}
	return StoredTableOf(w.Line)
}
