package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// stepWork is the role whose step works phase p, as a step of a plan names it (the part before a
// step's `:key`), and the type of that work; ok is false for a phase no step of a plan works.
func stepWork(p phase.Phase) (role stepRole, typ stepType, ok bool) {
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
// its count of starts is kept under, whose Place is its place in the task (from 1 for a step, 0 for
// a retro or a merge, which no plan names, so the kind tells those two apart), its role and its
// type ("" for a retro or a merge, whose role stepOfWalk names for every task). A working phase's
// step is the first step of steps whose role works it, at one plus its index. A phase no step of
// steps works, and a phase that runs no step — admitted, awaiting a person, held, done — answers
// ok false.
func stepIn(steps []plan.Step, p phase.Phase) (place record.StepPlace, role stepRole, typ stepType, ok bool) {
	switch p {
	case phase.Retro, phase.Merging:
		act, _ := Walk{}.PlaceOf(p)
		role, typ, ok = stepOfWalk(Walk{}, act)
		return record.StepPlace{Task: defaultTask, Kind: act.Kind}, role, typ, ok
	}
	role, typ, ok = stepWork(p)
	if !ok {
		return record.StepPlace{}, "", "", false
	}
	for i, named := range steps {
		if stepRole(named.Role) == role {
			return record.StepPlace{Task: defaultTask, Kind: "step", Place: i + 1}, role, typ, true
		}
	}
	return record.StepPlace{}, "", "", false
}

// planRecordNamespace is the namespace of every plan record's id.
var planRecordNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("legion-plan-record"))

// PlanID is the id of the plan one run of an issue carries out: a version 5 uuid of the issue and
// its run (record.Issue.Generation), so a write of the plan sent again lands on the same plan and
// the next run writes a plan of its own.
func PlanID(issue string, generation uint64) string {
	return uuid.NewSHA1(planRecordNamespace, []byte(fmt.Sprintf("%s/%d", issue, generation))).String()
}

// planNode is the plan node of issue's run: the plan w walks, as Dispatch records it under
// the run's one id (PlanID). Its source is the version in force's (record.PlanVersion.Source):
// owner, with the document and the version of it Legion took, or default, the project's default
// steps, with neither. Its task is the run's one task, with its steps in order, each a role or
// `role:key` (plan.Step.String). The node is written at the start of the work and again, whole, at
// each later version Legion takes (planGiven): Dispatch replaces all four fields for a version
// above the one it holds. A run with no plan rows walks today's line, and its node is the default
// steps of a project that names none (config.Project.RunsByDefault), as the release before wrote.
func planNode(issue record.Issue, w Walk) record.PlanRecord {
	node := record.PlanRecord{ID: PlanID(issue.Key, issue.Generation), Generation: issue.Generation, Source: record.PlanSourceDefault}
	if !w.Stored {
		node.Tasks = []record.PlanRecordTask{{ID: defaultTask, Steps: stepWords(config.Project{}.RunsByDefault())}}
		return node
	}
	if w.Version.Source == record.PlanSourceOwner {
		node.Source, node.PlanDocument = record.PlanSourceOwner, &record.PlanDocument{ArtifactID: w.Version.ArtifactID, Version: w.Version.Version}
	}
	steps := make([]plan.Step, len(w.Steps))
	for i, step := range w.Steps {
		steps[i] = plan.Step{Role: step.Role, Key: step.AgentKey}
	}
	node.Tasks = []record.PlanRecordTask{{ID: w.Task, Steps: stepWords(steps)}}
	return node
}

// stepWords is steps as a plan writes them, each `role` or `role:key`.
func stepWords(steps []plan.Step) []string {
	words := make([]string, len(steps))
	for i, step := range steps {
		words[i] = step.String()
	}
	return words
}

// passOverEarlierOwners deletes each plan of issue that an earlier owner gave, before its first
// step reads its plan, so the walk never takes one, and answers the versions it
// passed over. An earlier owner's plan is the version in force of a generation whose giver is no
// architect of the issue's tree now (record.Store's PlansOfEarlierOwners, record.ArchitectOfTree,
// the rule the routes hold every architect's call to): the owner of an issue changes where an
// orphan is admitted again as a root of its own and where an adoption moves a member to another
// tree, and the plan the earlier owner gave is not the new owner's to run. The rows go in this
// transaction, so the next first step finds none. An issue with no plan rows, or only its
// owners', is read and nothing is written.
func (e *Engine) passOverEarlierOwners(ctx context.Context, tx pgx.Tx, issue record.Issue) ([]record.PlanVersion, error) {
	earlier, err := e.store.PlansOfEarlierOwners(ctx, tx, issue)
	if err != nil {
		return nil, err
	}
	for _, version := range earlier {
		if err := e.store.DeletePlan(ctx, tx, issue.Key, version.Generation); err != nil {
			return nil, err
		}
	}
	return earlier, nil
}

// planFirstStep settles the plan the first step of issue's run walks, in the transaction that
// starts it (gateOpened) and before the transition reads it (walk), so each run that starts its
// work in this release has plan rows. A plan an earlier owner gave is passed over first
// (passOverEarlierOwners), so nothing below carries one. A run whose generation then holds plan
// rows, the owner's own or a hand-over's carried plan, keeps them. One whose generation holds none
// takes the newest plan its owner gave, of any generation, generation 0 among them, which holds a
// plan given before the daemon recorded the issue (record.Store's OwnersNewestPlan), carried from
// its start (CarryPlan without carried): each task waits at no act, and the transition starts the
// first step of its line. Where the owner gave none, the run takes the project's default steps
// (defaultRows). Nothing is passed over in silence, and the plan the run takes is the one the
// telling names (tellPassedOver).
func (e *Engine) planFirstStep(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
	earlier, err := e.passOverEarlierOwners(ctx, tx, issue)
	if err != nil {
		return err
	}
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil {
		return err
	}
	if stored == nil {
		if stored, err = e.store.OwnersNewestPlan(ctx, tx, issue); err != nil {
			return err
		}
	}
	switch {
	case stored == nil:
		version, tasks, steps := e.defaultRows(issue)
		if err := e.store.PutPlan(ctx, tx, version, tasks, steps); err != nil {
			return err
		}
		stored = &record.StoredPlan{Version: version}
	case stored.Version.Generation != issue.Generation:
		if err := e.store.CarryPlan(ctx, tx, issue.Key, stored.Version.Generation, issue.Generation, false); err != nil {
			return err
		}
	}
	return e.tellPassedOver(ctx, tx, issue, earlier, stored.Version)
}

// defaultRows is the plan of issue's run whose owner gave none: version 0 of the source
// default, given now, with no document and no giver, and one task, default, whose block names no
// steps and so runs the project's default steps (Config.DefaultSteps), waiting at no act.
func (e *Engine) defaultRows(issue record.Issue) (record.PlanVersion, []record.PlanTask, []record.PlanStep) {
	version := record.PlanVersion{Issue: issue.Key, Generation: issue.Generation, Source: record.PlanSourceDefault, GivenAt: e.now()}
	task := record.PlanTask{Issue: issue.Key, Generation: issue.Generation, Task: defaultTask, BlockText: "The project's default steps.",
		StepsByDefault: true, State: record.TaskWaiting}
	return version, []record.PlanTask{task}, planSteps(issue.Key, issue.Generation, defaultTask, e.cfg.DefaultSteps)
}

// planSteps is steps as the rows of the steps of task of issue's plan at generation: each at its
// place from 1, with its agent key and the type of its role.
func planSteps(issue string, generation uint64, task string, steps []plan.Step) []record.PlanStep {
	rows := make([]record.PlanStep, len(steps))
	for i, step := range steps {
		rows[i] = record.PlanStep{Issue: issue, Generation: generation, Task: task, Position: i + 1, Role: step.Role,
			Type: string(builtinDeployment.Roles[step.Role].Type), AgentKey: step.Key}
	}
	return rows
}

// tellPassedOver says what the first step of issue passed over, the plans of earlier owners
// (passOverEarlierOwners): one log line for each version, naming the plan the first step runs
// (runs, the version in force of its generation: the owner's, or the default steps), and one
// plan-not-taken notice to the issue's architect. Nothing passed over tells nothing.
func (e *Engine) tellPassedOver(ctx context.Context, tx pgx.Tx, issue record.Issue, earlier []record.PlanVersion, runs record.PlanVersion) error {
	if len(earlier) == 0 {
		return nil
	}
	ran, told := "default steps", "the default steps"
	if runs.Source == record.PlanSourceOwner {
		ran = describeVersion(runs)
		told = ran
	}
	passed := make([]string, len(earlier))
	for i, version := range earlier {
		passed[i] = describeVersion(version)
		e.logOnCommit(ctx, "workflow: a plan of an earlier owner is not taken", "issue", issue.Key, "generation", version.Generation,
			"version", version.Version, "document", version.ArtifactID, "giver", version.GivenBy, "runs", ran)
	}
	return e.notice(ctx, tx, issue.Key, record.Notice{Kind: "plan-not-taken", Role: claim.RoleArchitect, Reason: fmt.Sprintf(
		"The first step of %s took no plan of an earlier owner: %s. It runs %s. To run a plan it did not take, give it again with register_plan as your own.",
		issue.Key, strings.Join(passed, "; "), told)})
}

// describeVersion names an owner's version of a plan as a log line and a notice name it: its
// version, its document and who gave it.
func describeVersion(version record.PlanVersion) string {
	return fmt.Sprintf("version %d of document %s, given by %s", version.Version, version.ArtifactID, version.GivenBy)
}

// rateRetro writes the rating the retro of issue's run gave the plan onto that retro's record, in
// the transaction of the completion that ended the retro: a step record row whose id is the id of
// the retro's latest run (StepID, from the count of its starts, which the retro's end left as it
// was), at the retro's place of the run's walk (stepAt: its task's retro for a run with plan rows,
// the task default's for one with none), carrying every identity field the run's start and end
// carry, the agent its start named among them, so the write makes the record where neither has
// landed yet, and the rating and its reason. It carries no moment and no result, which are the
// end's. Dispatch sets each field once, so the end and the rating merge in either order. A record
// key that is no issue key writes nothing (recordsSteps).
func (e *Engine) rateRetro(ctx context.Context, tx pgx.Tx, issue record.Issue, rating intake.PlanRating) error {
	if !recordsSteps(issue) {
		return nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return err
	}
	place, role, typ, _ := stepAt(w, phase.Retro)
	start, err := e.store.StepStarts(ctx, tx, issue.Key, place)
	if err != nil {
		return err
	}
	write := stepWrite(issue, place, role, typ, start.Agent, start.Starts)
	write.PlanRating = &record.PlanRating{Rating: rating.Score, Reason: rating.Reason}
	return e.enqueue(ctx, tx, issue.Key, write)
}

// planHold says whether issue is held by its plan rather than by a failure: its owner stopped its
// task (record.HoldTaskStopped), or, as a parent whose plan has no acceptance step, it waits for
// the issues beneath it in place of its retro (record.HoldForChildren). No path that resumes the
// work of a held issue resumes such a hold: a later version of the plan or a person's todo on a
// root ends a task's stop (restartTask, or waitForFirstStep for a task stopped before its first
// step), and the last issue beneath the parent to leave starts its retro. An issue with no plan rows
// is never held so.
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

// pastReport is the run that a completion of a phase its issue no longer stands in reports, which
// pastReportOf decides once: a run of a phase the daemon moved the issue on from, the run of the
// step that the stop of its task ended, or no run that handoff answers.
type pastReport int

const (
	// noRunReported is a completion of no run: handoff refuses it as one of a phase the issue
	// does not stand in (NotCurrentPhase).
	noRunReported pastReport = iota
	// leftPhaseReported is the first report of a run of a phase the daemon moved the issue on from
	// without it (leftItsPhase), whatever event moved it, which handoff records on the role's row
	// and writes onto the ended step (reportEndedStep), moving nothing.
	leftPhaseReported
	// stoppedStepReported is a completion of the phase the issue is held from because its task
	// was stopped there (record.HoldTaskStopped).
	stoppedStepReported
)

// pastReportOf is the run that role's completion of reported, a phase issue no longer stands in,
// reports. A held issue stands where no role works, so no role has left its phase there
// (leftItsPhase), and a run of a left phase is never also the run a stop ended.
func (e *Engine) pastReportOf(ctx context.Context, tx pgx.Tx, issue record.Issue, fact intake.HandoffComplete, reported phase.Phase) (pastReport, error) {
	left, err := e.leftItsPhase(ctx, tx, issue, fact.Role, fact.Claim)
	switch {
	case err != nil:
		return noRunReported, err
	case left:
		return leftPhaseReported, nil
	case planHold(issue) && issue.Hold.Reason == record.HoldTaskStopped && issue.Hold.From == reported:
		return stoppedStepReported, nil
	}
	return noRunReported, nil
}

// lateReport is the refusal of fact, role's report of reported, a phase issue no longer stands in,
// where issue's run has plan rows and a stop of its task ended the run the report is of. past is
// the run the report is of (pastReportOf). The report of the phase a stop holds the issue from is
// late where it is of the run the stop ended (ranUnreported); its first sentence is the reason the
// task stores (LateReportOfStoppedTask). Every other report gets nil: the first report of a run an
// event ended, whatever step the task stands at now, which handoff takes as it takes one on an
// issue with no plan rows (leftPhaseReported); one of no run; one of a run whose plan rows are
// gone; one of the retro or the merge; one of a task whose issue left the workflow
// (HANDOFF_NOT_CURRENT_PHASE); and one of a step other than the one the stop ended. The refusal
// writes nothing itself: the caller answers it after the lingering tree's and keeps the report's
// handoff record (spendLateHandoff).
func (e *Engine) lateReport(ctx context.Context, tx pgx.Tx, issue record.Issue, fact intake.HandoffComplete, reported phase.Phase, past pastReport) (*intake.Refusal, error) {
	if past != stoppedStepReported {
		return nil, nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil || !w.Stored {
		return nil, err
	}
	at, _ := w.PlaceOf(reported)
	if at.Kind != record.PlaceStep {
		return nil, nil
	}
	ran, err := e.ranUnreported(ctx, tx, issue, fact.Role, fact.Claim)
	if err != nil || !ran {
		return nil, err
	}
	// The walk carries where the task stands, not why it was stopped: the reason is read from the
	// task's own row, the one source of it (record.Store.StopTask).
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil {
		return nil, err
	}
	for _, task := range stored.Tasks {
		if task.Task == w.Task {
			return intake.LateReportOfStoppedTask(issue.Key, w.Task, task.StoppedReason, at.Position, w.Steps[at.Position-1].Role, fact.HandoffRecord != ""), nil
		}
	}
	return nil, fmt.Errorf("late report of %s at generation %d: its plan holds no task %s", issue.Key, issue.Generation, w.Task)
}

// spendLateHandoff keeps the handoff record fact names, a late report lateReport refused, as its
// role's last one (record.PhaseRow.LastHandoff), and writes nothing else: the issue, its task and
// the rest of the row stand as they were, and no movement is marked. The record counts as used, so
// the step's next run is refused it (HANDOFF_NOT_NEW) and writes a handoff of its own, as the
// refusal tells the agent. A report that names no record writes nothing.
func (e *Engine) spendLateHandoff(ctx context.Context, tx pgx.Tx, fact intake.HandoffComplete) error {
	if fact.HandoffRecord == "" {
		return nil
	}
	row, err := e.phaseRow(ctx, tx, fact.Issue, fact.Role)
	if err != nil || row.LastHandoff == fact.HandoffRecord {
		return err
	}
	row.LastHandoff = fact.HandoffRecord
	return e.store.PutPhase(ctx, tx, row)
}

// stopTask stops w's task of issue where it stands, in the caller's transaction: the
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
	if err := e.WriteIssue(ctx, tx, issue); err != nil {
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
// before it of the type implementation to take its work back (returnTarget), as stop_task
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

// taskStopped is the owner's stop_task of one task of a recorded issue: the task
// stops where it stands, in the fact's own transaction, as stopTask stops it, its running step
// ending stopped, with the reason that its owner stopped it. The architect the issue's notices
// reach is told in a task-stopped notice that names the architect that made the call
// (Notice.StoppedBy): the daemon's notice executor publishes it only where that is another
// claim, since the caller is answered by its own call.
//
// An issue the record does not hold, or one with no plan rows, has no task to stop, and a task
// the plan does not hold is none either (TaskNotFound, which lists the plan's tasks only where it
// has some): an issue with no plan rows changes nothing. A stop of a task already stopped
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

// restartTask starts w's task of issue again at its first step, in the
// caller's transaction, for reason, which that step's worker is told. It runs on a person's todo
// on a root held for the stopped task (releaseHeldRoot), and for a later version of the plan that
// changes the stopped task or replaces it with a task of another id, where the work of the stopped
// task ran: it stood at an act of its line (record.TaskPlace.Started). A task stopped before its
// first step went out takes the way into that step instead (waitForFirstStep). issue is the record
// the caller writes, with any observation it keeps, and w is the walk of the task to start, which
// the caller read: the stopped task, or the new task of a later version, waiting at no act.
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
	if status != issue.DispatchStatus {
		if err := e.enqueue(ctx, tx, issue.Key, record.OrdinaryStatusWrite(status, issue.DispatchStatus, "")); err != nil {
			return nil, err
		}
	}
	if err := e.moveTask(ctx, tx, &issue, &w, first, record.TaskRunning); err != nil {
		return nil, err
	}
	issue.Hold, issue.Status = nil, status
	if err := e.WriteIssue(ctx, tx, issue); err != nil {
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

// waitForFirstStep ends the stop of w's task of issue, a task stopped before its first step went
// out, which stands at no act (record.TaskPlace.Started), in the caller's transaction, for reason,
// which its log line names. It runs where restartTask runs for a task whose work ran. Nothing of the
// task ran, so it goes back the way a task that was never stopped goes into its first step: it waits
// at no act again with its stop's reason gone (moveTask), the hold is cleared, and the issue stands
// where the hold is from, admitted, or awaiting a person for a root that waits on one before its
// first step (enterAwaiting). Nothing starts here: the design gate, the waiting line and admission
// decide when the first step starts (StartFirstPhase), and no count is raised, since no run of the
// task ended. Neither phase has a Dispatch status of its own (PhaseStatus), so none is written. The
// issue leaves held, which is a movement (the rule in dispatchIssue). The act is task_released,
// which the log line names.
func (e *Engine) waitForFirstStep(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, reason string) error {
	from := record.StandsAt(issue)
	if err := e.moveTask(ctx, tx, &issue, &w, phase.Admitted, record.TaskWaiting); err != nil {
		return err
	}
	issue.Phase, issue.Hold = from, nil
	if err := e.WriteIssue(ctx, tx, issue); err != nil {
		return err
	}
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
		return err
	}
	e.logOnCommit(ctx, "workflow: a task stopped before its first step waits for that step again", "issue", issue.Key, "tree", issue.Tree,
		"task", w.Task, "act", "task_released", "to", from, "reason", reason)
	return nil
}

// planGiven is the plan an owner gave with register_plan, stored at the
// generation the route gave it at: the issue's own, or 0 for an issue the daemon had not recorded,
// whose plan waits there for the issue's first step. A generation's first plan is taken where its
// task has not started: an issue the daemon has not recorded, and one standing before its first
// step (beforeItsWork). Each task waits at no act, and the opening of the gate starts the first
// step of its line (walk). An issue recorded with no plan rows whose work started ran before
// Legion took plans, and walks today's line to its end: its plan is refused.
//
// A later version is compared, task by task, with the version in force as its owner gave it
// (heldPlan, plan.ChangedTasks). A version of another document than the one in force is refused,
// but over the default steps, and so is a version at or below it (laterVersionRefusal). A task
// the version changes or removes must wait or be stopped: a running task is refused, one reason
// for each, and so is a task that is done or left the workflow. A version taken is stored as the
// version in force (givenRows): each task it leaves as it was keeps its steps and where it stands,
// each task it changes takes the steps its block now names, and each task it removes is deleted.
// Where it changes the stopped task of an issue held for that task, the stop ends. Where the work
// of the stopped task ran, which the place it stood at says (record.TaskPlace.Started), the task
// the plan now holds starts again at its first step (restartTask), and its worker is told which
// version did it. A task stopped before its first step went out waits for that step again, as a
// task never stopped does (waitForFirstStep). A version taken after the work started writes the
// run's plan node again, whole (planNode): the start wrote it at the version then in force, and the
// node names the plan the run walks now. A version taken before the work starts writes none, since
// the start writes the version in force.
//
// A plan given at a generation the issue has left, or in a lingering tree, is refused too. Each
// refusal writes nothing and leaves the fact unspent, so the same registration, whose id names the
// issue, the generation, the document and the version (api's registerPlan), applies when it is
// given again: a version refused while its task ran is taken after stop_task.
func (e *Engine) planGiven(ctx context.Context, tx pgx.Tx, fact intake.PlanGiven) (intake.Result, error) {
	refuse := func(refusal *intake.Refusal) (intake.Result, error) {
		return intake.Result{Refusal: refusal, Unspent: true}, nil
	}
	issue, err := e.store.Issue(ctx, tx, fact.Issue)
	if err != nil {
		return intake.Result{}, err
	}
	// The route gives generation 0 to an issue it found unrecorded. The daemon may record it
	// before the fact applies; that plan still waits at generation 0 for the first step.
	recorded := issue != nil && fact.Generation != 0
	if recorded && fact.Generation != issue.Generation {
		return refuse(intake.PlanStaleGeneration(fact.Issue, fact.Generation, issue.Generation))
	}
	stored, err := e.store.Plan(ctx, tx, fact.Issue, fact.Generation)
	if err != nil {
		return intake.Result{}, err
	}
	changed := []string{}
	if stored == nil && recorded && !beforeItsWork(*issue) {
		return refuse(intake.PlanRefused([]plan.Refusal{intake.StartedBeforePlans(issue.Key)}))
	}
	if stored != nil {
		if refusal := laterVersionRefusal(fact.Issue, stored.Version, fact.Plan); refusal != nil {
			return refuse(refusal)
		}
		changed = plan.ChangedTasks(heldPlan(stored), fact.Plan)
		var running []plan.Refusal
		for _, id := range changed {
			at := slices.IndexFunc(stored.Tasks, func(task record.PlanTask) bool { return task.Task == id })
			if at < 0 {
				continue
			}
			switch stored.Tasks[at].State {
			case record.TaskRunning:
				running = append(running, intake.RunningTaskChanged(id))
			case record.TaskDone:
				return refuse(intake.PlanOfDoneTask(fact.Issue, id))
			case record.TaskLeft:
				return refuse(intake.PlanOfLeftTask(fact.Issue, id))
			}
		}
		if len(running) > 0 {
			return refuse(intake.PlanRefused(running))
		}
	}
	if issue != nil {
		lingers, err := record.TreeLingers(ctx, e.store, tx, issue.Tree)
		if err != nil {
			return intake.Result{}, err
		}
		if lingers {
			result := refusedLingering(*issue, fmt.Sprintf("version %d of the owner's plan", fact.Plan.Version))
			result.Unspent = true
			return result, nil
		}
	}

	version, tasks, steps := givenRows(fact, stored, changed, e.cfg.DefaultSteps, e.now())
	var removed, stopped []string
	// started says whether the run's work started: a task of the version in force stood at an act of
	// its line, read before this version changes, replaces or removes it. A first plan is taken only
	// before the work (beforeItsWork), so without stored rows it did not.
	started := false
	if stored != nil {
		for _, task := range stored.Tasks {
			if !slices.ContainsFunc(fact.Plan.Tasks, func(given plan.Task) bool { return given.ID == task.Task }) {
				removed = append(removed, task.Task)
			}
			if task.State == record.TaskStopped && slices.Contains(changed, task.Task) {
				stopped = append(stopped, task.Task)
			}
			started = started || task.Place.Started()
		}
	}
	if len(removed) > 0 {
		if err := e.store.DeleteTasks(ctx, tx, fact.Issue, fact.Generation, removed); err != nil {
			return intake.Result{}, err
		}
	}
	if err := e.store.PutPlan(ctx, tx, version, tasks, steps); err != nil {
		return intake.Result{}, err
	}
	e.logOnCommit(ctx, "workflow: an owner's plan is taken", "issue", fact.Issue, "generation", fact.Generation, "version", fact.Plan.Version,
		"document", fact.Plan.ArtifactID, "given_by", fact.GivenBy, "changed", changed)
	if !recorded {
		return intake.Result{}, nil
	}
	ends := len(stopped) > 0 && planHold(*issue) && issue.Hold.Reason == record.HoldTaskStopped
	if !started && !ends {
		return intake.Result{}, nil
	}
	w, err := e.walk(ctx, tx, *issue)
	if err != nil {
		return intake.Result{}, err
	}
	if started && recordsSteps(*issue) {
		if err := e.enqueue(ctx, tx, issue.Key, planNode(*issue, w)); err != nil {
			return intake.Result{}, err
		}
	}
	if !ends {
		return intake.Result{}, nil
	}
	reason := fmt.Sprintf("the owner gave version %d of the plan while task %s was stopped", fact.Plan.Version, strings.Join(stopped, ", "))
	if !started {
		return intake.Result{}, e.waitForFirstStep(ctx, tx, *issue, w, reason)
	}
	refusal, err := e.restartTask(ctx, tx, *issue, w, reason)
	if err != nil {
		return intake.Result{}, err
	}
	if refusal != nil {
		// The version wrote its rows already, and restartTask refuses only a task that is done or
		// left the workflow, which no task held stopped is.
		return intake.Result{}, fmt.Errorf("start task %s of %s again after version %d of its plan: %s", w.Task, issue.Key, fact.Plan.Version, refusal.Message)
	}
	return intake.Result{}, nil
}

// beforeItsWork says whether issue stands before its first step: admitted, or a root waiting
// there on a person (enterAwaiting). Its task has not started, so a first plan takes it.
func beforeItsWork(issue record.Issue) bool {
	return issue.Phase == phase.Admitted || issue.Phase == phase.AwaitingPerson
}

// laterVersionRefusal is the refusal of next, given for issue over held, the version in force:
// a version of another document (PlanDocumentChanged), but over the project's default steps,
// which no document wrote, and a version at or below held (PlanVersionOld). nil for a later
// version of the same document.
func laterVersionRefusal(issue string, held record.PlanVersion, next plan.Plan) *intake.Refusal {
	if held.Source != record.PlanSourceDefault && next.ArtifactID != held.ArtifactID {
		return intake.PlanDocumentChanged(issue, held.ArtifactID)
	}
	if next.Version <= held.Version {
		return intake.PlanVersionOld(issue, held.Version, next.Version)
	}
	return nil
}

// heldPlan is the plan in force of stored as its owner's blocks gave it, which a later version is
// compared with (plan.ChangedTasks): each task's block, with the steps its block names. Those are
// its stored steps, but for a block that named none (record.PlanTask.StepsByDefault), whose stored
// steps are the default steps it runs, which a block naming them would differ from.
func heldPlan(stored *record.StoredPlan) plan.Plan {
	held := plan.Plan{ArtifactID: stored.Version.ArtifactID, Version: stored.Version.Version}
	for _, task := range stored.Tasks {
		block := plan.Task{ID: task.Task, Needs: task.Needs, PartOf: task.PartOf, Owner: task.OwnerRole, Merges: task.Merges, Text: task.BlockText}
		if !task.StepsByDefault {
			for _, step := range stored.Steps[task.Task] {
				block.Steps = append(block.Steps, plan.Step{Role: step.Role, Key: step.AgentKey})
			}
		}
		held.Tasks = append(held.Tasks, block)
	}
	return held
}

// givenRows is the rows of fact's version, given at now over stored, the plan in force (nil for
// none), of which changed are the tasks it changes, adds or removes. Each task takes the
// version's block, and a task new to the generation waits at no act (PutPlan keeps where a task
// the generation holds stands). A task the version leaves as it was keeps its stored steps, the
// commit a review of each read included; any other task takes the steps its block names (StepsOf),
// or, for a block that names none, defaults, the project's default steps (Config.DefaultSteps),
// each with the type of its role (planSteps).
func givenRows(fact intake.PlanGiven, stored *record.StoredPlan, changed []string, defaults []plan.Step, now time.Time) (record.PlanVersion, []record.PlanTask, []record.PlanStep) {
	version := record.PlanVersion{Issue: fact.Issue, Generation: fact.Generation, Version: fact.Plan.Version, ArtifactID: fact.Plan.ArtifactID,
		Source: record.PlanSourceOwner, GivenBy: fact.GivenBy, GivenAt: now}
	var tasks []record.PlanTask
	var steps []record.PlanStep
	for _, given := range fact.Plan.Tasks {
		tasks = append(tasks, record.PlanTask{Issue: fact.Issue, Generation: fact.Generation, Task: given.ID, PlanVersion: fact.Plan.Version,
			BlockText: given.Text, Needs: given.Needs, PartOf: given.PartOf, OwnerRole: given.Owner, Merges: given.Merges,
			StepsByDefault: !given.NamesSteps() && given.Owner == "", State: record.TaskWaiting})
		if stored != nil && !slices.Contains(changed, given.ID) {
			steps = append(steps, stored.Steps[given.ID]...)
			continue
		}
		steps = append(steps, planSteps(fact.Issue, fact.Generation, given.ID, plan.StepsOf(given, defaults))...)
	}
	return version, tasks, steps
}
