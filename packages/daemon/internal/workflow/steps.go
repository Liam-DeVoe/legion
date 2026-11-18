package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// defaultTask is the one task of today's fixed phases: until an issue carries a plan of its own,
// every step of it is a step of this task.
const defaultTask = "default"

// StepAgent is what the daemon reads of a step's agent where it writes the step's record: the
// session its claim records now, and the model that answered it last in that session as
// `<provider>/<model>`. Either is "" where the daemon reads none, and a record leaves out a field
// read empty, so a later write can still fill it (record.StepRecord).
type StepAgent struct {
	Session string
	Model   string
}

// stepOf is the step a phase runs as a step of the task default (stepIn): the step of a run with
// no plan rows, which walks today's line (stepAt). Its steps are the default steps of a project
// that names none (config.Project.RunsByDefault), never the configured project's: such a run
// walks today's line whatever `default_steps` says, so its places, and the ids of its records,
// stay where the release before put them (R20). A held issue's step is the step of the phase its
// hold is from (record.StandsAt), which the caller reads.
func stepOf(p phase.Phase) (place record.StepPlace, step int, role, typ string, ok bool) {
	return stepIn(config.Project{}.RunsByDefault(), p)
}

// stepOfWalk is the step w's task runs at place, a place of w's line (Walk.PlaceOf): the step's
// place in the task, from 1 for a step and 0 for the retro or the merge, the kind of the place,
// and the role and the type the plan's rows record for the step (record.PlanStep), or the role
// that runs Legion's own retro or merge, which no plan names and which has no type. A step place
// the task's steps do not hold, and a task at no act, run no step.
func stepOfWalk(w Walk, place record.TaskPlace) (step int, kind, role, typ string, ok bool) {
	switch place.Kind {
	case record.PlaceRetro:
		return 0, place.Kind, "implementer", "", true
	case record.PlaceMerge:
		return 0, place.Kind, "merger", "", true
	case record.PlaceStep:
		if place.Position >= 1 && place.Position <= len(w.Steps) {
			named := w.Steps[place.Position-1]
			return place.Position, place.Kind, named.Role, named.Type, true
		}
	}
	return 0, "", "", "", false
}

// stepSite is the place a caller of stepAt asks for, made by standingIn or placeNamed. The zero
// stepSite is no place: stepAt answers ok false for it.
type stepSite struct {
	at    phase.Phase
	place record.TaskPlace
	named bool
}

// standingIn is where the task stands while its issue stands in at (record.StandsAt):
// Walk.placeIn on a stored walk, and today's step of at (stepOf) on a walk with no plan rows.
func standingIn(at phase.Phase) stepSite { return stepSite{at: at} }

// placeNamed is a place the caller names: place on a stored walk; on a walk with no plan rows,
// whose rows carry no place that it reads, today's step of at (stepOf), so an issue that ran
// before plan rows keeps the ids of its records (R20).
func placeNamed(place record.TaskPlace, at phase.Phase) stepSite {
	return stepSite{at: at, place: place, named: true}
}

// stepAt is the step the run whose walk is w runs at site: the place its count of starts is kept
// under, its place in the task, its role and its type. A run with plan rows runs the step of its
// one task at the site's place on the task's line (stepOfWalk), under the task's id, so an owner's
// plan of `implementer reviewer` records its implementer at step 1 of its own task and has no step
// 3, 4 or 5, and of two steps that run as step, the one the site names. A run with none runs
// today's step of the task default (stepOf), so an issue that ran before plan rows keeps the ids
// of its records (R20). No place, a phase the line does not hold, and one that runs no step,
// answer ok false.
func stepAt(w Walk, site stepSite) (place record.StepPlace, step int, role, typ string, ok bool) {
	if site == (stepSite{}) {
		return record.StepPlace{}, 0, "", "", false
	}
	if !w.Stored {
		return stepOf(site.at)
	}
	stands, ok := site.place, site.named
	if !site.named {
		stands, ok = w.placeIn(site.at)
	}
	if !ok {
		return record.StepPlace{}, 0, "", "", false
	}
	if step, _, role, typ, ok = stepOfWalk(w, stands); !ok {
		return record.StepPlace{}, 0, "", "", false
	}
	return stands.StepPlace(w.Task), step, role, typ, true
}

// endOf is how a transition on trigger ends the step it moves the issue out of. A move back a role
// or a report asked for — the tester's fail, a review's request for changes, the fail of a step
// that runs as step, a role's or the architect's backward move — ends it failed; a move back nobody
// asked for, a head that conflicts with its base, ends it stopped. Both return the work to the step
// the issue moves into, which the record names. Every other move ends the step passed and returns
// nowhere.
func endOf(trigger TriggerKind) (result string, returns bool) {
	switch trigger {
	case TriggerTesterFailed, TriggerReviewRejected, TriggerStepFailed, TriggerBackward:
		return "failed", true
	case TriggerConflict:
		return "stopped", true
	}
	return "passed", false
}

// stepRecordNamespace is the namespace of every step record's id.
var stepRecordNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("legion-step-record"))

// StepID is the id of the record of one run of one step: a version 5 uuid of the issue, its run
// (record.Issue.Generation), the task, the kind, the place and the count of starts of that place
// (record.Store.StepStarts). The start of the run raises the count and the end reads it unchanged,
// so the two writes compute one id apart, across a restart too, and the next start of the same
// place, which raises the count again, writes another record.
func StepID(issue string, generation uint64, task, kind string, step int, starts int64) string {
	name := fmt.Sprintf("%s/%d/%s/%s/%d/%d", issue, generation, task, kind, step, starts)
	return uuid.NewSHA1(stepRecordNamespace, []byte(name)).String()
}

// StepEnd is how a step ends: its result (passed, failed or stopped), the step the work returns
// to (nil where it returns nowhere; a retro's or a merge's place is 0) and the handoff record the
// ending role's completion named ("" for none).
type StepEnd struct {
	Result     string
	ReturnedTo *int
	Report     string
}

// stepStopped is the end of a step that no completion and no move of the table ended: the issue
// left the workflow, its tree lingered, or its run ended under it.
var stepStopped = StepEnd{Result: "stopped"}

// startStep writes the start of the step issue stands in (record.StandsAt) on w, the walk of
// issue's run (stepAt), run by agent: one more start counted at its place
// (record.Store.RaiseStepStarts), and a step record row, committed with the start, carrying every
// identity field, the moment it started and the session the daemon reads of agent. The record
// never makes the start wait: the row reaches Dispatch on a lane of its own (record.LaneRecord). A
// phase that runs no step writes nothing, and neither does a record key that is no issue key
// (recordsSteps).
func (e *Engine) startStep(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, agent claim.Token) error {
	place, step, role, typ, ok := stepAt(w, standingIn(record.StandsAt(issue)))
	if !ok || !recordsSteps(issue) {
		return nil
	}
	now := e.now()
	starts, err := e.store.RaiseStepStarts(ctx, tx, issue.Key, place, string(agent), now)
	if err != nil {
		return err
	}
	write := stepWrite(issue, place, step, role, typ, string(agent), starts)
	write.StartedAt = new(now)
	if read := e.readAgent(agent); read.Session != "" {
		write.Session = new(read.Session)
	}
	return e.enqueue(ctx, tx, issue.Key, write)
}

// endStep writes the end of the step before stands in (record.StandsAt, so a held issue ends the
// step of the phase its hold is from) on the walk of before's run (stepAt), before being the issue
// as it stood before the write that ends the step: its run's own plan rows, read here, so a caller
// that opens the next run ends the step of the run it leaves. The record is the one its start
// wrote: the same generation and the count of starts unchanged, and the agent and the moment that
// start wrote, so the two writes agree whichever reaches Dispatch first. One run gets one end: the
// end marks the run ended in the store, in its own transaction (record.Store.EndStepStart), and an
// end of a run already marked writes nothing, so a run two writes end — the linger's and the
// hand-over's of one member — keeps the first end, and Dispatch never takes the later one for it.
// A step running when this release deployed has no count, and its end writes the record with the
// count 0, the agent its start would have named and, for a phase worker, the moment its role's
// entry was raised, and marks that run ended too (record.Store.EndUncountedStep): the route makes
// the node from the end alone. The end carries the moment now, end's fields, and the session and
// the model the daemon reads of the agent now. A phase that runs no step writes nothing, and
// neither does a record key that is no issue key (recordsSteps), nor an issue its plan holds
// (planHold): a stopped task's step ended when its owner stopped it, and a parent held for its
// children started no retro, so a linger, a hand-over or a re-admission over either has no run to
// end, and the end of a step with no count would be written for one that never ran.
func (e *Engine) endStep(ctx context.Context, tx pgx.Tx, before record.Issue, end StepEnd) error {
	if !recordsSteps(before) || planHold(before) {
		return nil
	}
	w, err := e.walk(ctx, tx, before)
	if err != nil {
		return err
	}
	at := record.StandsAt(before)
	place, step, role, typ, ok := stepAt(w, standingIn(at))
	if !ok {
		return nil
	}
	start, open, err := e.store.EndStepStart(ctx, tx, before.Key, place)
	if err != nil {
		return err
	}
	agent, startedAt := claim.Token(start.Agent), start.StartedAt
	if !open {
		if agent, startedAt, err = e.upgradedStart(ctx, tx, before, at); err != nil {
			return err
		}
		first, err := e.store.EndUncountedStep(ctx, tx, before.Key, place, string(agent), startedAt)
		if err != nil || !first {
			return err
		}
	}
	write := stepWrite(before, place, step, role, typ, string(agent), start.Starts)
	if !startedAt.IsZero() {
		write.StartedAt = new(startedAt)
	}
	write.EndedAt, write.Result, write.ReturnedTo = new(e.now()), new(end.Result), end.ReturnedTo
	if end.Report != "" {
		write.Report = new(end.Report)
	}
	read := e.readAgent(agent)
	if read.Session != "" {
		write.Session = new(read.Session)
	}
	if read.Model != "" {
		write.Model = new(read.Model)
	}
	return e.enqueue(ctx, tx, before.Key, write)
}

// recordsSteps says whether issue's steps are recorded: its key is an issue key, which every key
// Dispatch gives is, and so names claims (claim.NewToken) and an issue the route can hold. The
// record has keys that are none only where a test wrote them, and those have no claims either,
// as the promotion's own walk passes over them (admit's startMidPhaseMembers).
func recordsSteps(issue record.Issue) bool { return claim.IsIssueKey(issue.Key) }

// upgradedStart is the agent and the moment of the start of a step at that no start of this
// release counted, which ran under the release before: the agent its start would have named
// (startAgent), and the moment its phase worker's entry was raised. The worker is the role that
// works issue at at (record.Issue.RoleAt), the role of its running step at step. Accepting's
// architect raises no entry, and so the moment is unknown there. A merge whose merger's entry was
// never raised is one GitHub had already made when the issue reached merging, which started no
// merger.
func (e *Engine) upgradedStart(ctx context.Context, tx pgx.Tx, issue record.Issue, at phase.Phase) (claim.Token, time.Time, error) {
	worker := issue.RoleAt(at)
	if worker == claim.RoleArchitect {
		agent, err := e.startAgent(ctx, tx, issue, at)
		return agent, time.Time{}, err
	}
	row, err := e.phaseRow(ctx, tx, issue.Key, worker)
	if err != nil {
		return "", time.Time{}, err
	}
	if at == phase.Merging && row.EntryRaisedAt.IsZero() {
		return "", time.Time{}, nil
	}
	agent, err := stepClaim(issue, issue.Key, worker)
	return agent, row.EntryRaisedAt.UTC(), err
}

// startAgent is the agent a start of the step issue stands in at names: the tree architect for
// accepting (open point 4 of the plan: a sub-architect's own claim is runtime state the record does
// not hold), nobody for a merge GitHub has already made, where no merger starts, and the claim of
// the phase's worker otherwise.
func (e *Engine) startAgent(ctx context.Context, tx pgx.Tx, issue record.Issue, at phase.Phase) (claim.Token, error) {
	switch at {
	case phase.Accepting:
		return stepClaim(issue, issue.Tree, claim.RoleArchitect)
	case phase.Merging:
		standing := issue
		standing.Phase = at
		merged, err := PhaseFinished(ctx, e.store, tx, standing)
		if err != nil || merged {
			return "", err
		}
	}
	return stepClaim(issue, issue.Key, issue.RoleAt(at))
}

// stepClaim is the claim token of role on key, in issue's project.
func stepClaim(issue record.Issue, key string, role claim.Role) (claim.Token, error) {
	project, err := claim.ProjectToken(issue.Project)
	if err != nil {
		return "", err
	}
	return claim.NewToken(project, key, role)
}

// stepWrite is the identity of one run of a step of issue: the fields every write of its record
// carries.
func stepWrite(issue record.Issue, place record.StepPlace, step int, role, typ, agent string, starts int64) record.StepRecord {
	return record.StepRecord{ID: StepID(issue.Key, issue.Generation, place.Task, place.Kind, step, starts), Task: place.Task,
		Step: step, Kind: place.Kind, Role: role, Type: typ, Agent: agent, Generation: issue.Generation}
}

// readAgent is what the daemon reads of agent now (Config.StepAgent): nothing for no agent or no
// reader.
func (e *Engine) readAgent(agent claim.Token) StepAgent {
	if agent == "" || e.cfg.StepAgent == nil {
		return StepAgent{}
	}
	return e.cfg.StepAgent(agent)
}

// endOfTransition is how a transition on trigger ends the step of from on w, the walk of the
// issue's run, where the task stands, with handoff as the completion's row: endOf's result, the
// place of the step at to on w where the work goes back (stepAt; to is the place the transition's
// row moves the task to, or no place for a fail that returns nowhere), and the handoff record
// handoff names when its role is ended, the role whose step the move ends (transition reads it
// from the task's place). A phase that runs no step, admitted among them, whose role is the
// architect's, ends no step and names no report.
func endOfTransition(w Walk, trigger TriggerKind, from phase.Phase, to stepSite, handoff record.PhaseRow, ended claim.Role) StepEnd {
	result, returns := endOf(trigger)
	end := StepEnd{Result: result}
	if _, _, _, _, ok := stepAt(w, standingIn(from)); !ok {
		return end
	}
	if _, step, _, _, ok := stepAt(w, to); returns && ok {
		end.ReturnedTo = new(step)
	}
	if handoff.Role != "" && handoff.Role == ended {
		end.Report = handoff.HandoffRecord
	}
	return end
}

// EndRunningStep ends, stopped, the step before stands in: admission's re-admission of an issue
// whose run ends under it (admit's readmit), and its promotion's end of a member's merge a linger
// left open (admit's startMidPhaseMembers). A run another write ended already gets no second end
// (endStep).
func (e *Engine) EndRunningStep(ctx context.Context, tx pgx.Tx, before record.Issue) error {
	return e.endStep(ctx, tx, before, stepStopped)
}

// StartStepRecord writes the start of the step issue stands in on the walk of its run (stepAt),
// under the agent that step names (startAgent): admission's promotion of a member whose phase
// worker it does not start (admit's startMidPhaseMembers), whose step starts again all the same. A
// step whose latest run is still open goes on, and nothing is written: a member held from merging,
// whose merge step the linger left open (beginLinger) and whose retry goes on with it, or a role
// the promotion found already started for this run (workflow.StartFor). A second start there would
// open a run while the one before it has no end, and the count would leave that one none. A member
// its plan holds starts no step either (planHold): its stopped task's step ended stopped and runs
// again only when a later version of the plan starts the task at its first step, and a parent held
// for its children has started no retro.
func (e *Engine) StartStepRecord(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
	if !recordsSteps(issue) || planHold(issue) {
		return nil
	}
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return err
	}
	at := record.StandsAt(issue)
	place, _, _, _, ok := stepAt(w, standingIn(at))
	if !ok {
		return nil
	}
	latest, err := e.store.StepStarts(ctx, tx, issue.Key, place)
	if err != nil || latest.Starts > 0 && !latest.Ended {
		return err
	}
	agent, err := e.startAgent(ctx, tx, issue, at)
	if err != nil {
		return err
	}
	return e.startStep(ctx, tx, issue, w, agent)
}
