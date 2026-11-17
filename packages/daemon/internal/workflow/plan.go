package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// DefaultSteps is the steps of the one task of an issue whose owner gave no plan, in order, each a
// role or `role:key`: the one place they are stated. The plan each run writes when its work starts
// names them (defaultPlan), and the place and the role of the step a phase runs are read from them
// (stepOf). It answers a fresh copy each time, so no caller's change to its slice reaches another
// caller: Go has no constant of a slice, and a package variable would be state each caller shares.
func DefaultSteps() []string {
	return []string{"planner", "implementer", "tester", "reviewer", "acceptance:owner"}
}

// stepWork is the role whose step works phase p, as a step of a plan names it (the part before a
// step's `:key`), and the type of that work; ok is false for a phase no step of a plan works.
func stepWork(p phase.Phase) (role, typ string, ok bool) {
	switch p {
	case phase.Planning:
		return "planner", "planning", true
	case phase.Implementing:
		return "implementer", "implementation", true
	case phase.Testing:
		return "tester", "testing", true
	case phase.Reviewing:
		return "reviewer", "review", true
	case phase.Accepting:
		return "acceptance", "review", true
	}
	return "", "", false
}

// stepIn is the step phase p runs as a step of the task default whose steps are steps: the place
// its count of starts is kept under, its place in the task (from 1 for a step, 0 for a retro or a
// merge, which no plan names, so the kind tells those two apart), its role and its type ("" for a
// retro or a merge). A working phase's step is the first step of steps whose role works it, at one
// plus its index. A phase no step of steps works, and a phase that runs no step — admitted,
// awaiting a person, held, done — answers ok false.
func stepIn(steps []string, p phase.Phase) (place record.StepPlace, step int, role, typ string, ok bool) {
	switch p {
	case phase.Retro:
		return record.StepPlace{Task: defaultTask, Kind: "retro"}, 0, "implementer", "", true
	case phase.Merging:
		return record.StepPlace{Task: defaultTask, Kind: "merge"}, 0, "merger", "", true
	}
	role, typ, ok = stepWork(p)
	if !ok {
		return record.StepPlace{}, 0, "", "", false
	}
	for i, named := range steps {
		if stepRole, _, _ := strings.Cut(named, ":"); stepRole == role {
			return record.StepPlace{Task: defaultTask, Kind: "step", Place: i + 1}, i + 1, role, typ, true
		}
	}
	return record.StepPlace{}, 0, "", "", false
}

// planRecordNamespace is the namespace of every plan record's id.
var planRecordNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("legion-plan-record"))

// PlanID is the id of the plan one run of an issue carries out: a version 5 uuid of the issue and
// its run (record.Issue.Generation), so a write of the plan sent again lands on the same plan and
// the next run writes a plan of its own.
func PlanID(issue string, generation uint64) string {
	return uuid.NewSHA1(planRecordNamespace, []byte(fmt.Sprintf("%s/%d", issue, generation))).String()
}

// defaultPlan is the plan of issue's run whose owner gave none: the source default, and one task,
// default, whose steps are DefaultSteps.
func defaultPlan(issue record.Issue) record.PlanRecord {
	return record.PlanRecord{ID: PlanID(issue.Key, issue.Generation), Generation: issue.Generation, Source: "default",
		Tasks: []record.PlanRecordTask{{ID: defaultTask, Steps: DefaultSteps()}}}
}

// rateRetro writes the rating the retro of issue's run gave the plan onto that retro's record, in
// the transaction of the completion that ended the retro: a step record row whose id is the id of
// the retro's latest run (StepID, from the count of its starts, which the retro's end left as it
// was), carrying every identity field the run's start and end carry, the agent its start named
// among them, so the write makes the record where neither has landed yet, and the rating and its
// reason. It carries no moment and no result, which are the end's. Dispatch sets each field once,
// so the end and the rating merge in either order. A record key that is no issue key writes nothing
// (recordsSteps).
func (e *Engine) rateRetro(ctx context.Context, tx pgx.Tx, issue record.Issue, rating int, reason string) error {
	place, step, role, typ, _ := stepOf(phase.Retro)
	if !recordsSteps(issue) {
		return nil
	}
	start, err := e.store.StepStarts(ctx, tx, issue.Key, place)
	if err != nil {
		return err
	}
	write := stepWrite(issue, place, step, role, typ, start.Agent, start.Starts)
	write.Rating, write.RatingReason = new(rating), new(reason)
	return e.enqueue(ctx, tx, issue.Key, write)
}

// planHold says whether issue is held by its plan rather than by a failure: its owner stopped its
// task (record.HoldTaskStopped), or, as a parent whose plan has no acceptance step, it waits for
// the issues beneath it in place of its retro (record.HoldForChildren). No path that resumes the
// work of a held issue resumes such a hold: a later version of the plan or a person's todo on a
// root starts a stopped task again (restartTask), and the last issue beneath the parent to leave
// starts its retro. An issue with no plan rows is never held so (R20).
func planHold(issue record.Issue) bool {
	return issue.Phase == phase.Held && issue.Hold != nil &&
		(issue.Hold.Reason == record.HoldTaskStopped || issue.Hold.Reason == record.HoldForChildren)
}

// planHoldRefusal is the answer to a release of issue's hold — the architect's retry or
// escalation (retryOrEscalate), the operator's restart (restartPhase) — where its plan holds it
// (planHold), and nil for any other hold. A retry would start the role of the phase the hold is
// from and an escalation would overwrite the reason, so each is refused with what starts the work
// again: for a stopped task a later version of its plan (RetryOfStoppedTask), for a parent held
// for its children the last of them to leave (RetryOfHeldParent, naming each one still open).
func (e *Engine) planHoldRefusal(ctx context.Context, tx pgx.Tx, issue record.Issue) (*intake.Refusal, error) {
	if !planHold(issue) {
		return nil, nil
	}
	if issue.Hold.Reason == record.HoldForChildren {
		beneath, err := e.openBeneath(ctx, tx, issue)
		if err != nil {
			return nil, err
		}
		return intake.RetryOfHeldParent(issue.Key, beneath), nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return nil, err
	}
	return intake.RetryOfStoppedTask(issue.Key, w.Task), nil
}

// stopTask stops w's task of issue where it stands, in the caller's transaction (R16; C2-1): the
// step the issue stands in ends as end says (endStep), the task keeps its place and becomes
// stopped with why (record.Store.StopTask), and the issue is held from the phase that place
// derives for the stopped task (record.HoldTaskStopped), as a failed claim's hold is written
// (claimFailed). The hold keeps the task's place (moveTask) and the slot of the phase it is from
// (record.KeepsSlot reads record.StandsAt), and it writes no Dispatch status, since held has none
// (PhaseStatus). The process of the step's role is stopped with its session kept (the op
// step_stop, whose source is the daemon's, said as its reason), except for accepting, whose
// architect is the owner itself and runs no process of Legion's for the step, and a phase no role
// works. The task's state is the caller's to have read: a task done or left is no task to stop
// (StopTask refuses it).
func (e *Engine) stopTask(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, why, said string, end StepEnd) error {
	from := record.StandsAt(issue)
	if err := e.endStep(ctx, tx, issue, end); err != nil {
		return err
	}
	if err := e.store.StopTask(ctx, tx, issue.Key, issue.Generation, w.Task, why); err != nil {
		return err
	}
	issue.Phase, issue.Hold = phase.Held, &record.Hold{From: from, Reason: record.HoldTaskStopped}
	if err := e.store.PutIssue(ctx, tx, issue); err != nil {
		return err
	}
	// The stop takes the issue into held, which is a phase change and so a movement (the rule in
	// dispatchIssue), as a failed claim's hold is.
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
		return err
	}
	e.logOnCommit(ctx, "workflow: a task is stopped", "issue", issue.Key, "tree", issue.Tree, "task", w.Task, "from", from, "reason", why)
	role := phase.RoleFor(from)
	if role == "" || role == claim.RoleArchitect {
		return nil
	}
	return e.enqueue(ctx, tx, issue.Key, record.SuperviseRequest{Op: "step_stop", Tree: issue.Tree, Role: role, Generation: issue.Generation, Reason: said})
}

// stopUnreturned stops w's task of issue, whose step standing in issue.Phase failed with no step
// before it of the type implementation to take its work back (R7, returnTarget), as stop_task
// stops it (stopTask), its step ending as end says; and tells the task's owner once, in a notice
// of kind task-stopped whose reason names the task and the step. No return happened, so no pass,
// pair, round or hook is counted.
func (e *Engine) stopUnreturned(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, end StepEnd) error {
	place, _ := w.PlaceOf(issue.Phase)
	why := fmt.Sprintf("step %d (%s) failed and no earlier step of the type implementation can take the work", place.Position, w.Steps[place.Position-1].Role)
	said := fmt.Sprintf("task %s of %s was stopped: %s", w.Task, issue.Key, why)
	if err := e.stopTask(ctx, tx, issue, w, why, said, end); err != nil {
		return err
	}
	return e.notice(ctx, tx, issue.Key, record.Notice{Kind: "task-stopped", Role: claim.Role(w.Steps[place.Position-1].Role), Phase: issue.Phase, Reason: said})
}

// taskStopped is the owner's stop_task of one task of a recorded issue (R16; C2-1): the task
// stops where it stands, in the fact's own transaction, as stopTask stops it, its running step
// ending stopped, with the reason that its owner stopped it. The architect the issue's notices
// reach is told in a task-stopped notice that names the architect that made the call
// (Notice.StoppedBy): the daemon's notice executor publishes it only where that is another
// claim, since the caller is answered by its own call.
//
// An issue the record does not hold, or one with no plan rows, has no task to stop, and a task
// the plan does not hold is none either (TaskNotFound, which lists the plan's tasks only where it
// has some): an issue with no plan rows changes nothing (R20). A stop of a task already stopped
// changes nothing and is no refusal, so a retry of a stop already applied finds it stopped. A
// task that is done, or left the workflow with its issue before it was done, has nothing left to
// stop (TaskDone, TaskLeft): each is refused here, before the store's stop, which refuses both
// states itself and would fail the whole fact, and before the linger a root's leave begins, since
// the task's own state is the truer answer. A stop in a lingering tree changes nothing and says
// so.
func (e *Engine) taskStopped(ctx context.Context, tx pgx.Tx, fact intake.TaskStopped) (intake.Result, error) {
	issue, err := e.store.Issue(ctx, tx, fact.Issue)
	if err != nil {
		return intake.Result{}, err
	}
	if issue == nil {
		return intake.Result{Refusal: intake.TaskNotFound(fact.Issue, fact.Task, nil)}, nil
	}
	w, err := e.walk(ctx, tx, *issue)
	if err != nil {
		return intake.Result{}, err
	}
	switch {
	case !w.Stored:
		return intake.Result{Refusal: intake.TaskNotFound(issue.Key, fact.Task, nil)}, nil
	case fact.Task != w.Task:
		return intake.Result{Refusal: intake.TaskNotFound(issue.Key, fact.Task, []string{w.Task})}, nil
	case w.State == record.TaskStopped:
		return intake.Result{}, nil
	case w.State == record.TaskDone:
		return intake.Result{Refusal: intake.TaskDone(issue.Key, w.Task)}, nil
	case w.State == record.TaskLeft:
		return intake.Result{Refusal: intake.TaskLeft(issue.Key, w.Task)}, nil
	}
	lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree)
	if err != nil {
		return intake.Result{}, err
	}
	if lingers {
		return refusedLingering(*issue, fmt.Sprintf("the owner's stop of task %s", w.Task)), nil
	}
	from := record.StandsAt(*issue)
	why := fmt.Sprintf("task %s of %s was stopped by its owner", w.Task, issue.Key)
	if err := e.stopTask(ctx, tx, *issue, w, why, why, stepStopped); err != nil {
		return intake.Result{}, err
	}
	return intake.Result{}, e.notice(ctx, tx, issue.Key, record.Notice{Kind: "task-stopped", Role: phase.RoleFor(from), Phase: from,
		StoppedBy: fact.By, Reason: why + ", the architect " + string(fact.By)})
}

// restartTask starts w's task of issue again at its first step (R16; C2-1, C2-2), in the
// caller's transaction, for reason, which that step's worker is told. It runs on a person's todo
// on a root held for the stopped task (releaseHeldRoot), and for a later version of the plan that
// changes the stopped task or replaces it with a task of another id. issue is the record the
// caller writes, with any observation it keeps, and w is the walk of the task to start, which the
// caller read: the stopped task, or the new task of a later version, waiting at no act.
//
// The hold is cleared, the task runs at step 1 with its stop's reason gone (moveTask), and the
// issue stands in the phase of step 1. The Dispatch status of that phase is queued over the
// status Dispatch shows, not through Engine.status: a hold changes no status, so the record still
// reads the status of the phase the hold is from (releaseHeldRoot says the same of a release).
// Step 1's role is started at a raised entry (start), which writes the start of its step, and its
// entry counts as a move back, since it asks for the work again from the first step. The step the
// issue stood in ended when the task stopped (stopTask), so none is ended here. A task that ran
// raises its pass, so that a completion of this run takes a key of its own (the completion key
// names the task and its pass), and no pair, round or hook is counted, since no step failed; a
// new task keeps its counts of zero. The act is task_restarted, which the log line names: no row
// of the table moves a held issue.
//
// A task that is done, or left the workflow with its issue, has nothing left to start, and the
// store's move refuses neither state: each is refused here, before anything is written
// (RestartOfDoneTask, RestartOfLeftTask).
func (e *Engine) restartTask(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, reason string) (*intake.Refusal, error) {
	switch w.State {
	case record.TaskDone:
		return intake.RestartOfDoneTask(issue.Key, w.Task), nil
	case record.TaskLeft:
		return intake.RestartOfLeftTask(issue.Key, w.Task), nil
	}
	steps := w.steps()
	if !w.Stored || len(steps) == 0 {
		return nil, fmt.Errorf("restart task %s of %s: its line %v holds no first step", w.Task, issue.Key, w.Line)
	}
	if w.State != record.TaskWaiting {
		counts, err := e.store.ReturnCounts(ctx, tx, issue.Key, issue.Generation, w.Task)
		if err != nil {
			return nil, err
		}
		counts.Pass++
		if err := e.store.PutReturnCounts(ctx, tx, issue.Key, issue.Generation, w.Task, counts); err != nil {
			return nil, err
		}
	}
	from, first := record.StandsAt(issue), steps[0]
	status := PhaseStatus(first)
	if status != "" && status != issue.DispatchStatus {
		if err := e.enqueue(ctx, tx, issue.Key, record.OrdinaryStatusWrite(status, issue.DispatchStatus, "")); err != nil {
			return nil, err
		}
	}
	if err := e.moveTask(ctx, tx, &issue, &w, first, record.TaskRunning); err != nil {
		return nil, err
	}
	issue.Hold = nil
	if status != "" {
		issue.Status = status
	}
	if err := e.store.PutIssue(ctx, tx, issue); err != nil {
		return nil, err
	}
	// The restart takes the issue out of held into a phase, which is a movement (the rule in
	// dispatchIssue).
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
		return nil, err
	}
	e.logOnCommit(ctx, "workflow: a stopped task starts again at its first step", "issue", issue.Key, "tree", issue.Tree, "task", w.Task,
		"act", "task_restarted", "from", from, "to", first, "reason", reason)
	role := phase.RoleFor(first)
	if err := e.clearHandoff(ctx, tx, issue.Key, role); err != nil {
		return nil, err
	}
	pr, err := e.store.PullRequest(ctx, tx, issue.Key)
	if err != nil {
		return nil, err
	}
	return nil, e.start(ctx, tx, issue, role, func(at Standing) string {
		return task(issue, at, record.PhaseRow{}, pr, "", reason)
	}, true)
}
