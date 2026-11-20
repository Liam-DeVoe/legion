package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

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
// above the one it holds. Both read w with its plan rows: the start's first step writes them
// (planFirstStep), and a version taken is one. A run with no plan rows walks today's line, so its
// node is the task default of todaysSteps: a hand-over's carry of an orphan that ran before plans
// (WritePlan).
func planNode(issue record.Issue, w Walk) record.PlanRecord {
	node := record.PlanRecord{ID: PlanID(issue.Key, issue.Generation), Generation: issue.Generation, Source: record.PlanSourceDefault}
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
	return e.takeFirstPlan(ctx, tx, issue, stored, earlier)
}

// takeFirstPlan writes stored, the plan the first step of issue's run walks, at the run's
// generation, and tells what passed over earlier (tellPassedOver): the project's default steps for
// none (defaultRows), a plan of another generation carried from its start (CarryPlan without
// carried), and the generation's own rows kept as they are. planFirstStep and the stop of a task
// before that step (taskStopped) take the first plan through it.
func (e *Engine) takeFirstPlan(ctx context.Context, tx pgx.Tx, issue record.Issue, stored *record.StoredPlan, earlier []record.PlanVersion) error {
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

// WritePlan queues the plan node of issue's run (planNode), the plan it carries out, read from its
// walk, where the run's work starts: at the run's move out of admitted (transition's
// TriggerGateOpened), after the first step has settled the plan (planFirstStep), and at a
// hand-over's carry, which re-admits an orphan at a new generation in the phase it ran in
// elsewhere (admit's readmit), so that run never leaves admitted, after the carry has written its
// plan rows (record.Store.CarryPlan). A record key that is no issue key writes nothing
// (recordsSteps).
func (e *Engine) WritePlan(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
	if !recordsSteps(issue) {
		return nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return err
	}
	return e.enqueue(ctx, tx, issue.Key, planNode(issue, w))
}

// rateRetro writes the rating the retro of issue's run gave the plan onto that retro's record, in
// the transaction of the completion that ended the retro: a step record row whose id is the id of
// the retro's latest run (StepID, from the count of its starts, which the retro's end left as it
// was), at the retro's place of the run's walk (stepAt: its task's retro for a run with plan rows,
// the task default's for one with none), carrying every identity field the run's start and end
// carry, the agent its start named among them, so the write makes the record where neither has
// landed yet, and the rating and its reason. It carries no moment and no result, which are the
// end's. Dispatch sets each field once, so the end and the rating merge in either order. The
// retro's step comes from stepAt, which every writer of a step's record takes, so a record key that
// is no issue key writes nothing.
func (e *Engine) rateRetro(ctx context.Context, tx pgx.Tx, issue record.Issue, rating record.PlanRating) error {
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return err
	}
	step, recorded := stepAt(w, issue, phase.Retro)
	if !recorded {
		return nil
	}
	start, err := e.store.StepStarts(ctx, tx, issue.Key, step.place)
	if err != nil {
		return err
	}
	write := stepWrite(issue, step.place, step.role, step.typ, start.Agent, start.Starts)
	write.PlanRating = &rating
	return e.enqueue(ctx, tx, issue.Key, write)
}

// planHold says whether issue is held by its plan rather than by a failure: its owner stopped its
// task (record.HoldTaskStopped), or, as a parent whose plan has no acceptance step, it waits for
// the issues beneath it in place of its retro (record.HoldForChildren). No path that resumes the
// work of a held issue resumes such a hold: a later version of the plan or a person's todo on a
// root ends a task's stop (restartTask, or waitForFirstStep for a task stopped before its first
// step), and the parent's retro starts once nothing beneath it is open (releaseHeldParents). stored says whether the
// issue's run has plan rows: no plan holds a run that has none, so a hold of either reason on such
// a run, which the roll-forward of a way back that left its reason finds, is a plain hold that a
// retry releases.
func planHold(issue record.Issue, stored bool) bool {
	return stored && issue.Phase == phase.Held && issue.Hold != nil &&
		(issue.Hold.Reason == record.HoldTaskStopped || issue.Hold.Reason == record.HoldForChildren)
}

// planHoldRefusal is the answer to a release of issue's hold — the architect's retry or
// escalation (retryOrEscalate), the operator's restart (restartPhase) — where its plan holds it
// (planHold), and nil for any other hold. A retry would start the role of the phase the hold is
// from and an escalation would overwrite the reason, so each is refused with what starts the work
// again: for a stopped task a later version of its plan (RetryOfStoppedTask), for a parent held
// for its children the last of them to leave (RetryOfHeldParent, naming each one still open).
func (e *Engine) planHoldRefusal(ctx context.Context, tx pgx.Tx, issue record.Issue) (*intake.Refusal, error) {
	if issue.Phase != phase.Held || issue.Hold == nil {
		return nil, nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil || !planHold(issue, w.Stored) {
		return nil, err
	}
	if issue.Hold.Reason == record.HoldForChildren {
		beneath, err := e.openBeneath(ctx, tx, issue)
		if err != nil {
			return nil, err
		}
		return intake.RetryOfHeldParent(issue.Key, beneath), nil
	}
	return intake.RetryOfStoppedTask(issue.Key, w.Task, w.StoppedReason), nil
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
	case issue.Hold != nil && issue.Hold.Reason == record.HoldTaskStopped && issue.Hold.From == reported:
		w, err := e.walk(ctx, tx, issue)
		if err != nil || !planHold(issue, w.Stored) {
			return noRunReported, err
		}
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

// stopTask stops w's task of issue where it stands, in the caller's transaction, or answers why
// the task cannot stop there (stopRefusal) and writes nothing. Every stop of a task comes here, the
// owner's (taskStopped) and a failed step's with no step to take its work back (stopUnreturned), so
// no stop writes a hold the record cannot hold. A run with no plan rows stands before its first step,
// the one place taskStopped stops such a run: once the stop is judged, the plan that step would walk
// is settled (planFirstStep), the newest plan its owner gave or the project's default steps, and the
// task stops there, standing stopped before its first step (waitForFirstStep brings it back).
//
// The stop itself: the end of the step the issue stands in is named as end says (nameEnd), which
// the fact's settle writes, since the stopped task stands in no step (standingStep reads its plan's
// hold), the task keeps its place and becomes stopped with why (record.Store.StopTask), and the
// issue is held from the phase that place derives for the stopped task (record.HoldTaskStopped), as
// a failed claim's hold is written (claimFailed). The hold keeps the task's place (moveTask) and the
// slot of the phase it is from (record.KeepsSlot reads record.StandsAt), and it writes no Dispatch
// status, since held has none (PhaseStatus). The process of the step's role is stopped with its
// session kept (the op step_stop, whose source is the daemon's, said as its reason), except for
// accepting, whose architect is the owner itself and runs no process of Legion's for the step, and a
// phase no role works. The task's state is the caller's to have read: a task done or left is no task
// to stop (StopTask refuses it).
func (e *Engine) stopTask(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, why, said string, end StepEnd) (*intake.Refusal, error) {
	if refusal := stopRefusal(issue, w.Task); refusal != nil {
		return refusal, nil
	}
	if !w.Stored {
		if err := e.planFirstStep(ctx, tx, issue); err != nil {
			return nil, err
		}
		var err error
		if w, err = e.walk(ctx, tx, issue); err != nil {
			return nil, err
		}
	}
	from := record.StandsAt(issue)
	if step, ok := standingStep(w, issue); ok {
		if err := e.nameEnd(ctx, issue, step, end); err != nil {
			return nil, err
		}
	}
	if err := e.store.StopTask(ctx, tx, issue.Key, issue.Generation, w.Task, why); err != nil {
		return nil, err
	}
	issue.Phase, issue.Hold = phase.Held, &record.Hold{From: from, Reason: record.HoldTaskStopped}
	if err := e.WriteIssue(ctx, tx, issue); err != nil {
		return nil, err
	}
	// The stop takes the issue into held, which is a phase change and so a movement (the rule in
	// dispatchIssue), as a failed claim's hold is.
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
		return nil, err
	}
	e.logOnCommit(ctx, "workflow: a task is stopped", "issue", issue.Key, "tree", issue.Tree, "task", w.Task, "from", from, "reason", why)
	role := phase.RoleFor(from)
	if role == "" || role == claim.RoleArchitect {
		return nil, nil
	}
	return nil, e.enqueue(ctx, tx, issue.Key, record.SuperviseRequest{Op: "step_stop", Tree: issue.Tree, Role: role, Generation: issue.Generation, Reason: said})
}

// stopRefusal is the refusal of a stop of task of issue where issue stands, and nil where the
// stop's hold can stand there. A root that waits on a person before its first step stands in
// awaiting_person, which no hold is from: the issues table refuses such a hold
// (issues_held_from_check), and a wait's columns outside awaiting_person (issues_awaiting_shape).
// So its task cannot stop there, and the owner changes it with a later version of its plan, or
// stops it once the person has answered (TaskAwaitsPerson). Every other place an issue of the
// workflow stands is one a hold is from, held itself among them, since a held issue's place is
// the phase its hold is from (record.StandsAt).
func stopRefusal(issue record.Issue, task string) *intake.Refusal {
	if record.StandsAt(issue) == phase.AwaitingPerson {
		return intake.TaskAwaitsPerson(issue.Key, task)
	}
	return nil
}

// stopUnreturned stops w's task of issue, whose step standing in issue.Phase failed with no step
// before it of the type implementation to take its work back (returnTarget), as stop_task
// stops it (stopTask), its step ending as end says; and tells the task's owner once, in a notice
// of kind task-stopped whose reason names the task and the step. No return happened, so no pass,
// pair, round or hook is counted. A failed step stands in the phase of its role, where a hold
// always stands, so the stop's refusal is an error.
func (e *Engine) stopUnreturned(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, end StepEnd) error {
	place, _ := w.PlaceOf(issue.Phase)
	why := fmt.Sprintf("step %d (%s) failed and no earlier step of the type implementation can take the work", place.Position, w.Steps[place.Position-1].Role)
	said := fmt.Sprintf("task %s of %s was stopped: %s", w.Task, issue.Key, why)
	refusal, err := e.stopTask(ctx, tx, issue, w, why, said, end)
	if err != nil {
		return err
	}
	if refusal != nil {
		return fmt.Errorf("stop task %s of %s after its failed step: %s", w.Task, issue.Key, refusal.Message)
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
// Apply judged the stop already on the record as the fact reads it (requestRefusal): an
// architect that owns the issue no longer, or a stop of a run the issue has left since its route
// read it (TaskStaleGeneration), is refused there, unspent, before this reads anything.
//
// An issue the record does not hold, or one with no plan rows whose work started before Legion
// took plans, has no task to stop, and a task the plan does not hold is none either (TaskNotFound,
// which lists the plan's tasks only where it has some): an issue with no plan rows changes
// nothing. An issue that stands before its first step has no plan rows yet, and its task is one
// that is not done: its task is one of the plan that step would walk, the newest plan its owner
// gave or the project's default steps, and stopTask settles that plan as it stops the task. A task
// that plan does not hold is refused first, naming the plan's tasks, and nothing is written. A
// stop of a task already stopped changes nothing and is no refusal, so a retry of a stop already
// applied finds it stopped. A task that is done, or left the workflow with its issue before it was
// done, has nothing left to stop (TaskDone, TaskLeft): each is refused here, before the store's
// stop, which refuses both states itself and would fail the whole fact, and before the linger a
// root's leave begins, since the task's own state is the truer answer. A stop in a lingering tree
// changes nothing and says so, and so does a stop where its hold cannot stand: a root that waits on
// a person before its first step (stopRefusal).
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
	case !w.Stored && beforeItsWork(*issue):
		// The stop's task is one of the plan the issue's first step would walk: the newest its
		// owner gave, or the task default of the project's default steps.
		waiting, err := e.store.OwnersNewestPlan(ctx, tx, *issue)
		if err != nil {
			return intake.Result{}, err
		}
		tasks := []string{defaultTask}
		if waiting != nil {
			tasks = tasks[:0]
			for _, task := range waiting.Tasks {
				tasks = append(tasks, task.Task)
			}
		}
		if !slices.Contains(tasks, fact.Task) {
			return intake.Result{Refusal: intake.TaskNotFound(issue.Key, fact.Task, tasks)}, nil
		}
		w.Task = fact.Task
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
	refusal, err := e.stopTask(ctx, tx, *issue, w, why, why, stepStopped)
	if err != nil || refusal != nil {
		return intake.Result{Refusal: refusal}, err
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
		return task(issue, at, record.PhaseRow{}, pr, reason)
	}, true)
}

// waitForFirstStep ends the stop of w's task of issue, a task stopped before its first step went
// out, which stands at no act (record.TaskPlace.Started), in the caller's transaction, for reason,
// which its log line names. It runs where restartTask runs for a task whose work ran. Nothing of the
// task ran, so it goes back the way a task that was never stopped goes into its first step: it waits
// at no act again with its stop's reason gone (moveTask), the hold is cleared, and the issue stands
// in admitted, where the hold is from: no stop holds a root that waits on a person (stopRefusal).
// Nothing starts here: the design gate, the waiting line and admission decide when the first step
// starts (StartFirstPhase), and no count is raised, since no run of the task ended. Admitted has no
// Dispatch status of its own (PhaseStatus), so none is written. The issue leaves held, which is a
// movement (the rule in dispatchIssue). The act is task_released, which the log line names.
func (e *Engine) waitForFirstStep(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, reason string) error {
	if err := e.moveTask(ctx, tx, &issue, &w, phase.Admitted, record.TaskWaiting); err != nil {
		return err
	}
	issue.Hold = nil
	if err := e.WriteIssue(ctx, tx, issue); err != nil {
		return err
	}
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
		return err
	}
	e.logOnCommit(ctx, "workflow: a task stopped before its first step waits for that step again", "issue", issue.Key, "tree", issue.Tree,
		"task", w.Task, "act", "task_released", "to", issue.Phase, "reason", reason)
	return nil
}

// beforeItsWork says whether issue stands before its first step: admitted, or a root waiting
// there on a person (enterAwaiting). Its task has not started, so a first plan takes it.
func beforeItsWork(issue record.Issue) bool {
	return issue.Phase == phase.Admitted || issue.Phase == phase.AwaitingPerson
}

// planRatingRefusal is the rule of which completion carries the rating of the plan, judged where
// the retro ends: handoff reads it on the issue as the completion's own transaction reads it, under
// the lock every fact applies under, which is the read its TriggerRetroCompleted ends the retro on.
// The route held what no state of the issue changes: the rating's form (api's planRatingOf) and
// whether the caller must rate (intake.HandoffComplete.MustRate). endsRetro is whether this
// completion ends the retro, and reported the phase it is of, which the refusal names. The
// completion that ends the retro with no rating from a caller that must rate is refused
// PLAN_RATING_REQUIRED, and any other completion that carries one PLAN_RATING_UNEXPECTED; nil is
// no refusal.
func planRatingRefusal(fact intake.HandoffComplete, endsRetro bool, reported phase.Phase) *intake.Refusal {
	what := fmt.Sprintf("this completion of phase %s of %s", reported, fact.Issue)
	switch {
	case endsRetro && fact.PlanRating == nil && fact.MustRate:
		return intake.PlanRatingRequired(what)
	case !endsRetro && fact.PlanRating != nil:
		return intake.PlanRatingUnexpected(what)
	}
	return nil
}
