package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// PhaseFinished says whether issue's phase is over by its own terminating fact rather than by its
// worker's completion, so no worker of it is started or resumed for it. Merging's fact is the pull
// request being merged: the merger merges, the daemon merges nothing, and a merged root then
// stands in merging until the rest of its tree leaves the workflow (closeMerged), which can be
// days. Accepting's is the architect's sign-off, which moves the issue on in the transaction that
// records it, so an issue standing in accepting is always still waiting for it; every other
// phase ends with its worker's completion, which moves the issue too. A recorded completion
// answers nothing here: the merger's is recorded and moves no issue.
func PhaseFinished(ctx context.Context, store record.Store, tx pgx.Tx, issue record.Issue) (bool, error) {
	if issue.Phase != phase.Merging {
		return false, nil
	}
	pr, err := store.PullRequest(ctx, tx, issue.Key)
	if err != nil || pr == nil {
		return false, err
	}
	return pr.State == record.PullRequestMerged, nil
}

// RoleFor is phase.RoleFor, which is where the role that works a phase is decided
// (dispatch://INFRA-2800 moved it there, because the state projection reads it and
// internal/workflow's own tests import internal/projection). This forwarder is kept under the
// name this package carried it under for one reader that no build of this module reaches:
// packages/pi-legion/scripts/skill-scenarios/daemon-pane.go, which `go run -overlay` builds as a
// main inside the packages/daemon of each checkout a comparison run names, this head's and the
// one it is compared with. That program must therefore compile against both, so it calls the
// name both carry, and this forwards to the one definition rather than restating the table —
// a second table is what would drift.
//
// It goes once no checkout a comparison run can name predates dispatch://INFRA-2800: then
// daemon-pane.go calls phase.RoleFor against every checkout it is built inside, and this module
// holds one exported name for the rule again rather than two a new caller could pick between.
// Until then this name has callers of its own: in this package one test (checks_red_test.go),
// since every reader of the engine reads the role with the issue (record.Issue.RoleAt) or with
// the walk (Walk.RoleAt); and outside it the stalled-tree notice (admit's stalled.go) and test
// code. Each reads the same table as a caller of phase.RoleFor, since that is the one definition
// and this forwards to it.
func RoleFor(p phase.Phase) claim.Role {
	return phase.RoleFor(p)
}

func (e *Engine) phaseFinished(ctx context.Context, tx pgx.Tx, issue record.Issue) (bool, error) {
	return PhaseFinished(ctx, e.store, tx, issue)
}

// PastBound is whether at is more than bound before now. A zero at is no recorded moment, and a
// bound of zero or less is one no deployment can set (config's readMinutes and readHours refuse
// it), so both answer false.
func PastBound(at time.Time, bound time.Duration, now time.Time) bool {
	if at.IsZero() || bound <= 0 {
		return false
	}
	return at.Add(bound).Before(now)
}

// ReportExplainsQuiet is whether a claim's own session's report explains why it takes no turn:
// it found background work of its own running, or its phase reminder is due later than now. The
// launch that replaces a process clears its report (store's putClaim), and a report is written
// only onto the launch whose secret authenticated it (store's PutClaimActivity), so the report
// read is the running process's. A reminder already due explains nothing: it is the timer a
// silence repair backs up.
func ReportExplainsQuiet(background bool, nextReminder time.Time, now time.Time) bool {
	return background || nextReminder.After(now)
}

// SilentPhaseWorker is whether role's claim in run is a phase worker nothing will prompt: a claim
// unpromptedPhaseWorker holds, whose session's report does not explain its quiet
// (ReportExplainsQuiet), and whose agent's last recorded step is past bound (PastBound). A live
// claim holding no task is owed nothing by the daemon, and the timer that carries a worker
// through its phase runs in its own process (packages/pi-shared/src/phase-reminder.ts), so it
// dies with that process.
//
// Terms the code does not show:
//   - a suspension held for the agent's turn is not a term: no record carries it
//     (supervise.Claim.SuspensionHeld), and the outbox refuses the start while it holds.
//   - a finished phase or an accepted completion is not a term: this reads no store, so its
//     callers ask PhaseWorkerOwedTurn as well.
func SilentPhaseWorker(issue, root record.Issue, gate record.TreeGate, role claim.Role, run record.RoleRun,
	bound time.Duration, now time.Time,
) bool {
	if !unpromptedPhaseWorker(issue, root, gate, role, run) {
		return false
	}
	c := run.Claim
	if ReportExplainsQuiet(c.ActivityBackground, c.ActivityNextReminderAt, now) {
		return false
	}
	return PastBound(c.Step.LastStep(), bound, now)
}

// unpromptedPhaseWorker is the half of SilentPhaseWorker that is not about a moment: a live claim
// of the worker of issue's open phase (OpenRoleOf), on an issue at work in a tree that does
// not linger, up and between turns, in no update round, holding no task it still works, with no
// acting stop and no queued start and recorded in root's tree. Ready and idle are both a process
// waiting to be prompted; the role, not the state, tells a fault from health. The last three
// terms are StartFor's, read the other way, so no claim a start of the daemon's is already on
// its way to is named. Admission's repair of a role behind its entry (admit's owedEntryRoles)
// can name the same claim when it is behind the entry its phases row names, and both then send
// that entry's work (repairSilence, SendOwedEntry); whichever writes first leaves a queued start
// the other's rule reads and passes over. BackgroundStalledWorker stands on it too, so the two
// rules agree on which claim they are about.
func unpromptedPhaseWorker(issue, root record.Issue, gate record.TreeGate, role claim.Role, run record.RoleRun) bool {
	if run.Claim == nil {
		return false
	}
	if working := OpenRoleOf(issue); working == "" || working != role {
		return false
	}
	if !record.AtWork(issue, gate) || root.Lingers() {
		return false
	}
	c := run.Claim
	if c.State != supervise.StateReady && c.State != supervise.StateIdle {
		return false
	}
	if c.UpdateRound != "" {
		return false
	}
	if c.Pending != nil && c.Pending.StillWorked(issue.Generation, issue.Phase, c.State) {
		return false
	}
	stop := actingStop(run, root)
	return stop == 0 && c.Tree == root.Key && !queuedStartActs(run, stop, issue.Phase)
}

// PhaseWorkerOwedTurn is whether role's worker of issue's phase is still owed a turn, the half of
// a silence only the store can answer: false for a phase its terminating fact has met
// (PhaseFinished) and for a role whose completion of this phase entry the workflow accepted (the
// phase row's CompletedAt, which the next phase's start clears in clearHandoff). It answers the
// role's phase row too, an empty row naming the issue and role where the record holds none, so
// the silence repair reads it once.
func PhaseWorkerOwedTurn(ctx context.Context, store record.Store, tx pgx.Tx, issue record.Issue, role claim.Role) (record.PhaseRow, bool, error) {
	finished, err := PhaseFinished(ctx, store, tx, issue)
	if err != nil || finished {
		return record.PhaseRow{}, false, err
	}
	rows, err := store.Phases(ctx, tx, issue.Key)
	if err != nil {
		return record.PhaseRow{}, false, err
	}
	row := record.PhaseRow{Issue: issue.Key, Role: role}
	for _, r := range rows {
		if r.Role == role {
			row = r
			break
		}
	}
	return row, row.CompletedAt.IsZero(), nil
}

func (e *Engine) enqueue(ctx context.Context, tx pgx.Tx, issue string, payload record.OutboxPayload) error {
	return e.enqueueAt(ctx, tx, issue, payload, e.now())
}

// enqueueAt enqueues payload for issue due at at: a deadline row (a linger's close, a production
// check's bounds), where enqueue's rows are due now.
func (e *Engine) enqueueAt(ctx context.Context, tx pgx.Tx, issue string, payload record.OutboxPayload, at time.Time) error {
	row, err := record.NewOutboxRow(issue, payload, at)
	if err != nil {
		return err
	}
	return e.store.Enqueue(ctx, tx, row)
}

// status queues the Dispatch write for a status the workflow has just given issue. A status that
// reads as out of the workflow is the daemon's own close — the done a merge writes — and it goes
// as a close row, fenced on the run it ends (record.CloseStatusWrite): the ordinary rule drops a
// row whose issue has left the workflow, which is every close, so a close judged by it would be
// lost on its first ISSUE_CHANGED and leave the issue open in Dispatch for good.
func (e *Engine) status(ctx context.Context, tx pgx.Tx, issue record.Issue, status string) error {
	if status == "" || issue.Status == status {
		return nil
	}
	if record.OutOfWorkflow(status) {
		return e.enqueue(ctx, tx, issue.Key, record.CloseStatusWrite(status, issue.Status, "", issue.Generation))
	}
	return e.enqueue(ctx, tx, issue.Key, record.OrdinaryStatusWrite(status, issue.Status, ""))
}

// notice tells the architect that owns the issue; a notice of no kind (noticeFor's, for a move
// that tells it nothing) enqueues nothing.
func (e *Engine) notice(ctx context.Context, tx pgx.Tx, issue string, notice record.Notice) error {
	if notice.Kind == "" {
		return nil
	}
	return e.enqueue(ctx, tx, issue, notice)
}

// noticeWithController tells the architect that owns the issue and, in an outbox row of its own,
// the project's controller: the two publishes retry apart, so a controller topic that keeps failing never sends
// the architect the same notice again. Every hold is told this way, since the architect answers one
// with a retry or an escalation to the controller, and so is the tree architect's own failed
// claim, since every other notice of the tree reaches that architect.
func (e *Engine) noticeWithController(ctx context.Context, tx pgx.Tx, issue string, notice record.Notice) error {
	if err := e.notice(ctx, tx, issue, notice); err != nil {
		return err
	}
	return e.enqueue(ctx, tx, issue, record.ControllerNotice(notice))
}

// logOnCommit writes one Info line once the fact's transaction commits (intake.OnCommit), so a
// fact rolled back writes nothing and one retried after a failed commit writes it once.
func (e *Engine) logOnCommit(ctx context.Context, msg string, args ...any) {
	intake.OnCommit(ctx, func() { e.log.Info(msg, args...) })
}

// warnOnCommit is logOnCommit at Warn, for a committed fact that stalls a round and tells nobody
// else: a log monitor keys on it where an Info line would drown.
func (e *Engine) warnOnCommit(ctx context.Context, msg string, args ...any) {
	intake.OnCommit(ctx, func() { e.log.Warn(msg, args...) })
}

// refused is a committed 409: the fact changed nothing, and the caller is told why.
func refused(code, message string) intake.Result {
	return intake.Result{Refusal: &intake.Refusal{Status: 409, Code: code, Message: message}}
}

// refusedLingering refuses a worker's request on issue while its tree lingers: linger holds the
// member where it stood (record.TreeLingers), so the request changed nothing.
func refusedLingering(issue record.Issue, request string) intake.Result {
	return refused("TREE_LINGERING", fmt.Sprintf("the tree %s is lingering after it left the workflow, so %s of %s changed nothing", issue.Tree, request, issue.Key))
}

// clearHandoff empties the handoff a role reported for its previous phase — its commit, the
// Dispatch handoff record it named, its verdict and summary, and the review round's decision,
// which ends with the round — keeping its claim, the record it last named (LastHandoff) and the
// review rounds, when a transition starts it on a new phase. A role's recorded handoff is then
// always its current phase's: the implementer's round-1 commit cannot advance round 2, a reviewer
// starting round 2 or a tester starting a new round reads as not completed until it completes
// again (record.PhaseRow.Completed), a merger sent back has no summary until its next one, and no
// round inherits an earlier decision. The architect reports no handoff, so it has none to clear:
// its phases row holds only the entry it was handed work on (EnterArchitect), which no move
// between phases clears.
func (e *Engine) clearHandoff(ctx context.Context, tx pgx.Tx, issue string, role claim.Role) error {
	if role == "" || role == claim.RoleArchitect {
		return nil
	}
	row, err := e.phaseRow(ctx, tx, issue, role)
	if err != nil {
		return err
	}
	// Nothing to clear is nothing to write.
	if handoffOf(row) == (phaseHandoff{}) {
		return nil
	}
	return e.store.PutPhase(ctx, tx, phaseHandoff{}.onto(row))
}

// phaseHandoff is the handoff a role reported for one phase, which is the one list of the fields
// clearHandoff empties. Both halves of that clear are derived from this list: the write is the
// empty handoff put back on the row (onto), and the decision to write compares the handoff the
// row carries with that same empty one, so the fields cleared and the fields the decision stands
// on cannot drift apart. It is a struct of its own, and not record.PhaseRow, because the decision
// is a comparison and a phase row cannot be compared: the row holds the map of its backward moves
// (record.PhaseRow.BackwardMoves), and Go refuses == on a struct type that holds a map whatever
// its values are, two nil maps included.
type phaseHandoff struct {
	commit         string
	dispatchRecord string
	version        int
	verdict        string
	summary        string
	decision       *record.ReviewDecision
	completedAt    time.Time
}

// handoffOf is the handoff row carries.
func handoffOf(row record.PhaseRow) phaseHandoff {
	return phaseHandoff{commit: row.HandoffCommit, dispatchRecord: row.HandoffRecord, version: row.HandoffVersion,
		verdict: row.Verdict, summary: row.Summary, decision: row.Decision, completedAt: row.CompletedAt,
	}
}

// onto is row carrying h, with every other field of the row — its claim, the record it last
// named, its rounds and their counts — left as it stands.
func (h phaseHandoff) onto(row record.PhaseRow) record.PhaseRow {
	row.HandoffCommit, row.HandoffRecord, row.HandoffVersion = h.commit, h.dispatchRecord, h.version
	row.Verdict, row.Summary, row.Decision = h.verdict, h.summary, h.decision
	row.CompletedAt = h.completedAt
	return row
}

// suspend stops role's worker on issue, for reason, which the suspended claim logs.
func (e *Engine) suspend(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, reason string) error {
	return e.enqueue(ctx, tx, issue.Key, record.SuperviseRequest{Op: "suspend", Tree: issue.Tree, Role: role, Generation: issue.Generation, Reason: reason})
}

// testerVerdictLabel labels the tester's verdict a started worker's task carries. The reviewer's
// prompt names it as the tester's result rather than its own (prompts/go/reviewer.md), which a test
// ties to it.
const testerVerdictLabel = "Tester's verdict"

// ReviewedCommitLabel labels the commit a later review round's task names (reviewedCommit.part),
// the head the reviewer's own last deciding review read. The reviewer's prompt and the merge gate
// of skill://legion-worker name it to the reviewer, which a test ties to it, and the tests of the
// packages that read a reviewer's task name it by this constant rather than type it again.
const ReviewedCommitLabel = "Last reviewed commit"

// start starts the phase worker of the issue's current phase, a start stamped with that phase and
// with the entry it raises for that role (enter), whose task is word for word the task the
// worker is sent. Its start carries no mark (startMarks), a fresh session among them: the
// operator's `legion claims retry --fresh-session` reaches a retry of a held phase (restart) and
// never a transition's start. movedBack is whether the move into that phase was a move back to an
// earlier one, which is written on the row beside the entry and is the transition's to read, not
// this function's: the issue record already names the phase it moved into by the time a start
// is made. A phase with no worker, and accepting, whose architect admission started with the
// tree, start nobody.
//
// told composes the start's task from where the work stands (standingPart), which start reads only
// for a worker it starts: a phase that starts nobody is told no task, and done, which holds no
// place in the line of an issue's plan, has no standing to name.
func (e *Engine) start(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, told func(Standing) string, movedBack bool) error {
	if role == "" || role == claim.RoleArchitect {
		return nil
	}
	at, err := e.standingPart(ctx, tx, issue)
	if err != nil {
		return err
	}
	task := told(at)
	entry, err := e.enter(ctx, tx, issue, role, task, movedBack, false)
	if err != nil {
		return err
	}
	return e.started(ctx, tx, issue, role, task, entry, startMarks{})
}

// restart starts the phase worker of a phase entry the issue never left: the architect's or the
// operator's retry of a held phase, which puts the issue back in the phase its worker's claim
// failed in, with a task of its own that names the retry's reason. It hands the role the entry
// that row already names, raising none, because the entry is the one the worker is still on: a
// claim whose task went unconfirmed keeps it (the outbox delivers no second copy of a task its
// claim still works), and a completion of it would otherwise be refused for an entry nothing
// ever delivered, for good.
// Carrying the row's own number rather than nothing keeps the fence live for a worker the retry
// does start afresh: its start names that entry, so a later entry of the same phase still refuses
// this one's completion. A retry told not to resume the claim's recorded session passes that on,
// freshSession, and only the operator's `legion claims retry --fresh-session` does
// (record.SuperviseRequest's FreshSession).
//
// The start is marked a resend (record.SuperviseRequest.ResendEntry), because it hands the role
// the work of the entry its row already names: a claim that confirmed that entry's last task and
// whose turn then ended holds that entry as its given one and no pending delivery, so the entry
// test alone would decline the retry's task and relaunch the worker with no prompt
// (supervise.Machine.queue). Its task is told as start's is, from the standing it reads only for
// a worker it starts.
func (e *Engine) restart(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, told func(Standing) string, freshSession bool) error {
	if role == "" || role == claim.RoleArchitect {
		return nil
	}
	at, err := e.standingPart(ctx, tx, issue)
	if err != nil {
		return err
	}
	row, err := e.phaseRow(ctx, tx, issue.Key, role)
	if err != nil {
		return err
	}
	return e.started(ctx, tx, issue, role, told(at), row.Entry, startMarks{resend: true, freshSession: freshSession})
}

// SendOwedEntry sends role the work the entry of its phases row names, at that entry, for a claim
// that is behind it. row is that row, read by the caller: the admission pass weighs the claim
// against the entry it names and hands it here, so the row the rule was weighed against is the
// row whose work is sent and the rows are read once (admit's owedEntryRoles).
//
// The row's EntryTask is that work: the text the transition composed and wrote in the same
// transaction as the number it raised (enter), so it is sent as it stands.
//
// A row carrying no task is sent the phase's resume task instead (ResumePhaseTask), which says to
// carry the work on rather than beginning it. Two rows are in that state: one the upgrade to the
// entry left with no task, since the release before it recorded none, and one a re-admission
// reset (record.Store.ClearGeneration).
//
// It is exported for admission, which repairs a slot whose worker has no task with it
// (resumeSlottedWorkers) and sweeps the claims left behind their role's entry: every other way
// back into a phase is a transition with a task of its own. Its start carries no mark, which is
// what a sweep's and a ready's start carry; a door that marks its start calls sendOwedEntry.
func (e *Engine) SendOwedEntry(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, row record.PhaseRow) error {
	return e.sendOwedEntry(ctx, tx, issue, role, row, startMarks{})
}

// sendOwedEntry is the body SendOwedEntry, the builder of the boot's resume by rule and of the
// cutover's repair of a merger the `awaiting_merge` cutover left released (resume.go's
// startSuspendedOwner) and the silence repair (silence.go's repairSilence) share, with the marks
// of the door the start comes through (startMarks). A mark names that door and not the task's
// text, so what a claim behind its entry is sent is composed in one place however the start is
// made, and the boot's resume of a claim the daemon suspended goes on telling the outbox whose
// launch budget is at risk.
//
// Every start it writes is a re-assertion (startMarks.reasserts), whichever door it came
// through: the admission pass's sweep of the claims behind their entry, on every pass and on the
// sweep of its own each minute, its repair of a slot whose worker has no task, readyWorkerTask,
// the boot's resume by rule, the cutover's repair and the silence repair. Each of them sends work the record already
// owes the role — the entry and its task were written by the fact that raised the number, and
// that fact marked the issue moved where it wrote them (enter's two callers, start and
// StartPromotedWorker, both come through started as news of the issue) — so the movement is the
// fact that created the entry and not each delivery of it. R3 counts the daemon's own
// re-assertions as no movement (dispatch://INFRA-2560), and these must be among them: the minute
// sweep alone would otherwise reset a stuck tree's quiet time sixty times an hour, and the
// measure would never reach the bound on the stop it most needs to see. It is set here, once,
// rather than by each door, so no door can send an owed entry as news.
func (e *Engine) sendOwedEntry(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, row record.PhaseRow,
	marks startMarks) error {
	task := row.EntryTask
	if task == "" {
		reviewed, err := e.reviewedPartOf(ctx, tx, issue, role, row)
		if err != nil {
			return err
		}
		if task, err = e.resumeTask(ctx, tx, issue, reviewed); err != nil {
			return err
		}
	}
	marks.reasserts = true
	return e.started(ctx, tx, issue, role, task, row.Entry, marks)
}

// StartPromotedWorker starts the phase worker of a mid-phase member a re-admitted tree's
// promotion brings back (admit's startMidPhaseMembers), on the resume task of the phase it stands
// in (ResumePhaseTask).
//
// It raises the entry, where the promotion's other role starts raise none: the promotion is
// handing that role its work again, and the re-admission cleared the task its entry named with
// the rest of the generation (record.Store.ClearTreeGeneration), so the entry the record now
// holds names work nothing would send. The start is marked a resume task, so the outbox still
// hands no second prompt to a claim that holds its phase's task
// (record.SuperviseRequest.ResumeTask). The entry it raises is of no move back: the promotion
// hands the role the phase it already stands in, and moves the issue nowhere.
func (e *Engine) StartPromotedWorker(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role) error {
	reviewed, err := e.reviewedPartFor(ctx, tx, issue, role)
	if err != nil {
		return err
	}
	task, err := e.resumeTask(ctx, tx, issue, reviewed)
	if err != nil {
		return err
	}
	entry, err := e.enter(ctx, tx, issue, role, task, false, false)
	if err != nil {
		return err
	}
	return e.started(ctx, tx, issue, role, task, entry, startMarks{resumeTask: true})
}

// startMarks are the marks a start's request carries beside its entry, which tell the outbox and
// the claim's machine how to read that entry and that task, and how to start the claim
// (record.SuperviseRequest), and the one mark the record reads instead, whether the start is news
// of its issue. The zero value marks none of them, which is a start of the work of an entry the
// claim is behind, resuming the session the claim records, and news of the issue.
type startMarks struct {
	// resend says the start hands the role the work of the entry its phases row already names
	// rather than a new entry, so the claim's given entry does not decline the task
	// (record.SuperviseRequest.ResendEntry). The held phase's retry sets it (restart), because
	// the claim it reaches may hold that very entry as its given one; sendOwedEntry sends the
	// entry its row names too and sets it not, because the claim it reaches is behind that entry
	// and the entry test passes the task on its own.
	resend bool
	// resumeTask says the task is the phase's resume task (ResumePhaseTask), so a claim that
	// already holds a task of this generation and phase is given it once it is ready and the
	// start delivers none (record.SuperviseRequest.ResumeTask). The promotion's start of a
	// mid-phase worker sets it (StartPromotedWorker), and so do the boot's resume, the cutover's
	// repair and the silence repair of a claim level with the entry its phases row names
	// (startSuspendedOwner, which booted and cutoverMergerRepair share, and repairSilence).
	resumeTask bool
	// ruleResume says the start is the boot's start of a claim the daemon itself suspended, so the
	// outbox finishes the row rather than retrying the claim it resumed when that claim spends its
	// launch budget (record.SuperviseRequest.RuleResume). The boot sets it on the two starts it
	// makes of such a claim, and no other start carries it: its resume by rule (booted) and the
	// cutover's one-time repair of a merger the idle release suspended (cutoverMergerRepair), whose
	// starts one builder makes (startSuspendedOwner). The mark names the door the start comes
	// through — the boot's resume of a claim the daemon suspended, where the outbox finishes the
	// row rather than retrying a claim that is spending its launch budget — and not the task's
	// text. So each of the two carries it on the work of the entry its role's phases row names as
	// well as on the phase's resume task (sendOwedEntry), and changing what the task says does not
	// change whose budget is at risk.
	ruleResume bool
	// freshSession says the start must not resume the session the claim records but begin one of
	// its own (record.SuperviseRequest.FreshSession). Only the operator's `legion claims retry
	// --fresh-session` asks for it, which reaches the retry of a held phase alone (restart).
	freshSession bool
	// reasserts says the start sends work the record already owes the role rather than carrying
	// a fact new to the record, so it marks no movement of the issue (reassertion, startOrOwe). It
	// rides on no request: started reads it to tell startOrOwe which movement the start is, and
	// the outbox never sees it. sendOwedEntry sets it on every start it writes, which is where
	// the rule for the sends of an owed entry stands, and the three doors that send a claim level
	// with its entry the phase's resume task set it too, the boot's resume by rule, the cutover's
	// repair of a merger the `awaiting_merge` cutover left stopped and the silence repair
	// (startSuspendedOwner, which booted and cutoverMergerRepair share, and repairSilence), since
	// that resume is the same work sent again. The other three starts are news of the issue and
	// leave it unset: a transition's start and the promotion's start of a mid-phase worker, each
	// raising the entry it hands (enter), and the held phase's retry, which an architect's or an
	// operator's own request brings (restart).
	reasserts bool
}

// started enqueues the start each of its six callers makes, each of which has held role to one
// that works a phase and composed the task itself, at the entry that task is of — 0 for a
// resume that hands the role no new work, which is the boot's resume, the cutover's repair and
// the silence repair of a claim level with the entry its phases row names (startSuspendedOwner,
// which booted and cutoverMergerRepair share, and repairSilence) — and each of which names its
// own marks. It is the one place a start of a phase worker is built, so a reader
// asking what such a row carries reads this request and no other, and a field added here
// reaches every one of those starts. It goes through startOrOwe, so the worker comes back on
// the one row that carries its new task, and that row waits for the issue's admission slot where
// the phase it starts holds one: a transition's start into a phase up to the review puts a
// worker to work on the change, which is exactly what the cap counts. The issue carries the
// phase the transition wrote on it, so the rule reads the phase the start is for. Whether the
// start is news of its issue is marks.reasserts, handed on as the movement startOrOwe marks or
// leaves unmarked.
//
// One of the six is called from outside the engine's own transitions: sendOwedEntry, which
// admission calls through SendOwedEntry for the claims it sweeps and for the slot it repairs
// (admit's owedEntryRoles and resumeSlottedWorkers), which readyWorkerTask calls for a worker
// whose agent has just reported ready while its claim stands below the entry its phases row
// names (catch_up.go), which the boot's resume by rule and the cutover's repair call for a
// suspended claim standing below it (startSuspendedOwner, for booted's claims and for the
// released mergers of cutoverMergerRepair), and which the silence repair calls for a silent claim
// standing below it (repairSilence). A ready worker level with that row is given the phase's
// resume task instead, and that one start is enqueued directly rather than built here, for the
// reason StartOrOwe's own comment gives.
func (e *Engine) started(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, task string, entry int64, marks startMarks) error {
	move := newsOfTheIssue
	if marks.reasserts {
		move = reassertion
	}
	return startOrOwe(ctx, tx, e.store, issue.Key, record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: role, Task: task,
		Generation: issue.Generation, Phase: issue.Phase, Entry: entry, ResendEntry: marks.resend, ResumeTask: marks.resumeTask,
		RuleResume: marks.ruleResume, FreshSession: marks.freshSession}, e.now(), move)
}

// movement says whether a start of a phase worker is news of its issue, which startOrOwe marks
// (record.Store.MarkIssueMoved). It is a type of its own rather than a bare bool because it is
// handed beside the start's own moment and request, where a bare bool says nothing at the call
// site of what it decides.
type movement bool

const (
	// newsOfTheIssue is a start that carries a fact new to the record: a transition putting a
	// worker to work on the entry it raised, the promotion's start of a mid-phase member, which
	// raises one too, and a held phase's retry.
	newsOfTheIssue movement = true
	// reassertion is the daemon sending a phase worker again work the record already owes it:
	// every send of an owed entry (sendOwedEntry) and the phase's resume task sent to a claim
	// level with its entry by the boot's resume by rule, by the cutover's one-time repair
	// (cutoverMergerRepair) and by the silence repair. It is no movement of the issue (startOrOwe
	// says why).
	reassertion movement = false
)

// StartOrOwe writes the start of a phase worker as one outbox row in the caller's own
// transaction: the caller's own request, the task on it. The outbox's start case does the whole
// start on that row — it stops the role elsewhere, re-points a carried claim at this tree,
// provisions the workspace and creates the claim when there is none, records the start against
// the claim, spawns, resumes or retries it by state, and delivers the task — so a worker that
// comes back comes back with its work, rather than spending a turn asking what it is for.
//
// It is exported for the tests of other packages that queue a start the way the engine does
// (internal/admit's slot cases, internal/daemon's outbox cases), so that what those cases write
// is the row production writes. The one production caller is Engine.started, which is where the
// entry a start hands and each of its marks are set, so production code adds no caller here: a
// request composed at this call would carry no entry and no mark. Admission's start of a
// mid-phase worker goes through started too, by way of Engine.StartPromotedWorker (admit's
// startMidPhaseMembers).
//
// A start whose phase holds one of the cap's slots is marked for it, and the outbox holds such
// a row unclaimable while the issue holds no slot (store.go's outboxInOrder). The cap counts
// the issues the daemon has started whose change is still to write, each holding its row from
// its start until its work is past its review, except while it waits on another issue
// (record.KeepsSlot, blockers.waitsOf), so a start claimable at once
// would put a worker to work on an issue holding no slot — where the cap is full that is what
// it would do — and a worker put to work on an issue the cap has not admitted is what a start
// of a phase worker may never be. A transition's start, admission's start of a mid-phase member,
// the boot's resume by rule, the silence repair and the cutover's one-time start of a merger the
// `awaiting_merge` cutover left released (cutoverMergerRepair) all come through here, and all five
// come through Engine.started, so each of them waits where the phase it starts holds a row. The mark
// is this function's and not the caller's: it is the one reading of the phase the start carries
// (record.PhaseKeepsSlot), never a decision of one request. Every caller writes the phase on the
// record before it calls, so the payload's phase is the phase the issue stands in. The row
// carries its task while it waits, so the delivery is made as soon as the slot comes and the
// worker is up.
//
// A start the rule leaves unmarked stands in no waiting line — Store.OwedStarts lists a marked
// row alone, since that line is about slot-waiting — and it is coalesced like every other start
// of a phase worker. Store.EnqueueStart takes out the issue's pending start that names a phase,
// carries that phase's task and waits for what this start waits for, so a retro's or a merger's
// start is written in place of the start the issue already holds rather than beside it: two
// rows serving the phase the issue stands in, at its own generation and at one entry, would
// start its worker twice for one piece of work, and no fence of the outbox's ends the second.
// That pair is what the held phase's retry writes (restart, which sends the entry its row
// already names), while the first start is still queued. The one row is claimable the moment it
// commits, since no slot holds it back, and the outbox's own phase, generation and entry fences
// still end a stale one.
//
// A start that waits for a slot and one that waits for none take each other out nowhere, and
// need not: the mark is read from the phase, so two such rows never name the same phase, the
// issue stands in the newer one's, and the phase fence ends the older. What keeping them apart
// keeps is the waiting line, whose place belongs to the row that waits and is not a past-review
// row's to hand over, and the lane's own order behind a row that waits, a close's suspend among
// it (admit's promotion of a re-admitted tree queues its start behind that suspend, so the
// worker comes back after the stop rather than before it).
//
// A start with no task or no phase is refused here, before the mark. The two guards that would
// otherwise catch it both read the mark or the phase: record.validateOutboxPayload asks a task
// and a phase of a marked row alone, and the outbox's phase fence holds a row that names a phase
// alone (daemon's outbox, "serves a phase the issue has left"). While every start was marked,
// validation refused such a row; an unmarked one would now reach a worker with no work and no
// phase to fence it on. Every start the daemon writes here is built by Engine.started, which
// writes the phase the issue stands in and a task its caller composed, so the refusal rejects no
// start the daemon writes today. The task and the phase are also what tells a phase worker's
// start from the issue's other supervise starts, which is how Store.EnqueueStart finds the one
// row an issue holds for a wait (record's pendingPhaseStart), so a start written without them
// would stand beside that row rather than in its place.
//
// The row is written through Store.EnqueueStart, the one statement that takes out the start the
// issue already owes and writes this one in its place. It matches that row by issue and not by
// role because the row's creation time is the issue's place in the waiting line: an issue that
// already owes a start and has not had its slot keeps that place — what is written is a row of
// its own carrying that creation time — and carries the later transition's task instead, so two
// transitions before the slot start the worker once. The superseded role never starts, which is
// right — the issue has left that phase, so that role never had work to do, while the role the
// later transition names has it. Where there is no such row — the first start, or one the lane
// has already leased because the slot came — the same statement writes the caller's own row,
// which is what the daemon has always done for back-to-back transitions.
//
// No other start of the daemon's comes through here, and none may: the tree architect's start
// (admit's promotion, awaiting_reconcile.go), which waits for no slot of its own, since the row
// the cap counts is its root's, the production check's look (check.go), the pod wake (wake.go),
// the resume of a cancelled idle release (the daemon's outbox), the roles admission brings back
// beside a member's phase worker (admit's startMidPhaseMembers) and the phase's resume task a
// ready worker level with the entry its phases row names is given (catch_up.go's
// readyWorkerTask) are enqueued directly and stay claimable at once. None of the first five puts
// a worker to work on a phase, so none of them is the cap's to hold, and a slot they waited for
// might never be offered. The last is sent to a worker already at work on the phase it is in —
// its agent has just reported ready — so in a phase up to its review the issue holds the row the
// cap counts already or gave it back to wait on another issue, a wait that stops none of its
// work, and past its review it needs none (record.KeepsSlot), so this one has nothing to wait
// for in any case. The work of an entry that same ready worker stands below is sent through
// SendOwedEntry, and that start comes through here with the rest.
func StartOrOwe(ctx context.Context, tx pgx.Tx, store record.Store, issue string, payload record.SuperviseRequest, at time.Time) error {
	return startOrOwe(ctx, tx, store, issue, payload, at, newsOfTheIssue)
}

// startOrOwe is StartOrOwe told whether the start is news of its issue (movement). The starts
// that are not are written through here all the same, so they take the slot mark, the one
// statement that writes a start and the refusal of a start with no task or no phase from the one
// implementation rather than from a second start path that would have to keep in step with this
// one.
func startOrOwe(ctx context.Context, tx pgx.Tx, store record.Store, issue string, payload record.SuperviseRequest, at time.Time,
	move movement,
) error {
	if payload.Task == "" || payload.Phase == "" {
		return fmt.Errorf("the start of a phase worker on %s requires a task and a phase", issue)
	}
	payload.NeedsSlot = record.PhaseKeepsSlot(payload.Phase)
	row, err := record.NewOutboxRow(issue, payload, at)
	if err != nil {
		return err
	}
	// Putting a worker to work is a movement of the issue (the rule in workflow's dispatchIssue),
	// and it is marked here rather than at each caller because this is where every start of a
	// phase worker is written: every one of them is built by Engine.started, which a transition's
	// start, the held phase's retry, admission's start of a mid-phase member, the sends of an
	// owed entry and the three resumes of a claim level with its entry (the boot's, the cutover
	// repair's and the silence repair's) all go through. The moment
	// is the one the caller was handed, the fact's own, not a clock read again here.
	//
	// It is marked whether or not the row written takes out one the issue already owes: an issue
	// that has waited for its slot since its first start and takes a later transition's task has
	// moved, and the row it keeps is only its place in the waiting line.
	//
	// A reassertion is not marked. It is the daemon sending a phase worker work the record
	// already owes it: every send of an owed entry (sendOwedEntry, whose callers are admission's
	// sweep on every pass and each minute, its slot repair, readyWorkerTask, the boot's resume by
	// rule, the cutover's one-time repair (cutoverMergerRepair) and the silence repair), and the
	// phase's resume task the boot's resume, the cutover's repair and the silence repair send a
	// claim level with its entry. A movement is a fact new to the record, and the
	// daemon's own re-assertions are none (dispatch://INFRA-2560, R3): nothing outside the daemon
	// happened, and the work is not new to the record. The fact that made it so is the one that
	// raised the entry, and that fact marked the issue where it raised it: a transition's phase
	// write (Engine.transition) and its start, which comes through here as news, and a
	// promotion's (admit's promoteHolds and readmit) with its start of a mid-phase member. It is
	// the case of the pod wake (wake.go's wakeRow), whose start brings a woken role back to work
	// it already holds and is enqueued directly, so it marks nothing either. The reason is what
	// the mark is for: the quiet time measures a tree that has stopped, and these sends are the
	// daemon reacting to that stop or closing a difference it already knew of, so marking them
	// would make the measure read its own repair as recovery — a stopped tree's clock set back by
	// the minute sweep sixty times an hour, and the stalled-tree notice put off for as long as
	// the daemon kept sending. When a send works, the worker's own claim steps move the tree
	// through the quiet measure (record.QuietTrees), which is where a recovery is read.
	if move == newsOfTheIssue {
		if err := store.MarkIssueMoved(ctx, tx, issue, at); err != nil {
			return err
		}
	}
	return store.EnqueueStart(ctx, tx, row)
}

// enter raises the phase entry this start hands role on issue, writes task as the work of that
// entry and the moment it raised it, and answers the number for the start's payload. All of it is
// the transition's own transaction, so the row names the new entry before the outbox can run that
// start, which is what makes the comparison safe: while a role's turn outlives its phase its claim
// still serves the earlier entry (the machine refuses a second delivery while one is pending), and
// the row already names this one.
//
// Three starts raise one, each handing its role work it was not already on: a transition's start
// of the phase the issue has moved into (start), the promotion's start of a mid-phase worker a
// re-admitted tree brings back (StartPromotedWorker), and a root architect's start of work it was
// not on (EnterArchitect: StartArchitect and ResumeAwaitedRoot). Every other start carries the
// entry its role's phases row already names — the send of an owed entry (SendOwedEntry), the
// sweep's start of a root architect behind its entry (SendArchitectStart), the held phase's retry
// (restart) — or is a start of no entry (record.SuperviseRequest.Entry), so none of them moves
// the row and a worker's in-flight completion is never refused for an entry it was never given.
//
// The number counts the entries this role has been handed on this issue, which is why it is raised
// here: this is the one place a role is handed work it was not on. It needs to be unique among
// those entries and no more, since it is only ever compared with a value the daemon itself wrote,
// never read from a request — a completion carries no identity at all
// (api.HandoffCompleteRequest). Nothing else at hand counts them: the issue's generation is one
// run's whole life, and the round counts only returns to implementing.
//
// The task it writes is the text the worker is sent, word for word, so the record holds exactly
// what the worker is told and a claim left behind the entry can be sent that work again from the
// record alone (SendOwedEntry).
//
// movedBack is written with them: whether the start raising this entry was a move back to an
// earlier phase. Its one reader is the completion route's entry fence, which takes a completion
// of an earlier entry at the head the record holds and refuses it whatever head it names when
// the entry standing now was raised by a move back, since such an entry asks for the phase's
// work to be done again (record.PhaseRow.EntryMovedBack, api/handoff.go). It is written here
// rather than counted anywhere (record.PhaseRow.EntryMovedBack says why no count answers it).
//
// verbatim is whether the sweep's send of a root architect's entry again sends that task as
// written (record.PhaseRow.EntryVerbatim), which only the resume of an answered root says. Every
// raise writes it, so a raise that does not say it clears the mark an earlier entry's resume left.
//
// For an issue with plan rows, a phase worker's raise also writes the step the entry is a run of
// (record.PhaseRow.StepTask, StepPlace): the walk's task and the place of the step the issue stands
// in (stepAt), from 1 for a step and 0 for the retro on the implementer's row and the merge on the
// merger's, so the row's facts are the facts of that step's run. An issue with no plan rows keeps
// no task and place 0 (R20), and the architect's entry is no step of the issue's task.
func (e *Engine) enter(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, task string, movedBack, verbatim bool) (int64, error) {
	row, err := e.phaseRow(ctx, tx, issue.Key, role)
	if err != nil {
		return 0, err
	}
	row.Entry++
	row.EntryTask, row.EntryVerbatim, row.EntryRaisedAt, row.EntryMovedBack = task, verbatim, e.now(), movedBack
	worker := role != claim.RoleArchitect
	var w Walk
	if worker {
		if w, err = e.walk(ctx, tx, issue); err != nil {
			return 0, err
		}
		if _, step, _, _, ok := stepAt(w, standingIn(record.StandsAt(issue))); w.Stored && ok {
			row.StepTask, row.StepPlace = w.Task, step
		}
	}
	if err := e.store.PutPhase(ctx, tx, row); err != nil {
		return 0, err
	}
	// Every entry a phase worker is handed is the start of a run of its step: a transition's start
	// and the promotion's start of a mid-phase member (StartPromotedWorker).
	if worker && recordsSteps(issue) {
		agent, err := stepClaim(issue, issue.Key, role)
		if err != nil {
			return 0, err
		}
		if err := e.startStep(ctx, tx, issue, w, agent); err != nil {
			return 0, err
		}
	}
	return row.Entry, nil
}

// EnterArchitect raises the phase entry a root's architect is handed on root, writes task as the
// work of that entry, whether the sweep sends it again as written (verbatim, enter) and the
// moment it was raised, and answers the number for the start that hands it
// (record.SuperviseRequest.Entry). A root's architect holds a row of the phases record exactly as
// every phase worker does, so what it was handed, what that work is and how long it has stood are
// read from the record rather than from the outbox row that carried it.
//
// It is the one way a root architect's entry is raised, and two starts that hand the architect
// work it was not on make it. StartArchitect does, for admission's promotion of a queued root
// (admit's LineQueuedRoot branch) and for the sweep's raise for an architect whose row names no
// entry, with the derivation's task, which the sweep derives again. ResumeAwaitedRoot does, for
// the resume of a root a person answered, with the task that names the answer, which the sweep
// sends as written. Each composes the architect's task itself and enqueues its start itself,
// because an architect's start waits for no admission slot, so neither writes through start or
// started, both of which are a phase worker's. The rule it is held to is enter's own, which is
// why it is one call and not a second implementation: it is made in the transaction that writes
// that start. The entry is raised by no move back (record.PhaseRow.EntryMovedBack): that mark's
// one reader is the completion route's entry fence, and the architect completes nothing there.
func (e *Engine) EnterArchitect(ctx context.Context, tx pgx.Tx, root record.Issue, task string, verbatim bool) (int64, error) {
	return e.enter(ctx, tx, root, claim.RoleArchitect, task, false, verbatim)
}

// The labels of a started worker's task that a prompt or a skill names to the worker, each the
// text before the colon of one of the task's lines. Every line of the task is built from its
// label here, and TestEachTaskLabelIsOneThePromptsName ties each one to the prompts and the
// skills that tell the worker to read it, so a label renamed on one side alone fails there.
// testerVerdictLabel and ReviewedCommitLabel, above, are two more, tied by tests of their own.
const (
	phaseLabel       = "Phase"
	workingCopyLabel = "Working copy"
	planTaskLabel    = "Task"
	stepsLabel       = "Steps"
	taskTextLabel    = "Task text"
	reportedLabel    = "reported"
	handoffLabel     = "Handoff"
	pullRequestLabel = "Pull request"
	reasonLabel      = "Reason"
)

// task is what a started worker is told. It names the phase the worker starts, which the issue
// record already holds: one role runs several phases (the implementer runs implementing and
// retro), and a resumed session cannot otherwise tell a new phase from its last. at is where the
// worker's work stands (standingPart), placed right after that line.
// The handoff a phase ended with is a Dispatch handoff record on the issue, which the line names by
// its phase word and version with the command that reads that version, the record the daemon
// accepted, whatever was written to the phase since; a commit is named, as `Handoff commit:`,
// whenever the completion reported one: the head the planner's, the implementer's, the tester's or
// the reviewer's record names, and the commit retro and the merger stand on.
// A verdict of one of Legion's roles is always the tester's, since the daemon accepts one from no
// other of them on a phase completion (api/handoff.go, VERDICT_ROLE_FORBIDDEN; the implementer's
// check report is no phase completion and starts nobody), so the line names the tester: the
// reviewer it starts on a pass reads it as the tester's report, never as a field of its own
// completion. A role the deployment defines reports a verdict and writes no handoff record, so its
// report is named by its own step and role, ` Step <i> (<role>) reported: <verdict>.`, with its
// summary after it: <i> is the place of the step the row's entry was raised for (StepPlace).
// reviewed is the commit the reviewer's last deciding review read (reviewedPart), "" for every
// start but a later review round's; the part that names it (reviewedCommit.part) is placed after
// the pull request and before the reason.
func task(issue record.Issue, at Standing, handoff record.PhaseRow, pr *record.PullRequest, reviewed reviewedCommit, reason string) string {
	text := continueLine(issue) + string(at)
	if handoff.HandoffRecord != "" {
		word := handoffWord(handoff.Role)
		text += fmt.Sprintf(" %s: %s version %d (dispatch handoff-read --issue %s --phase %s --version %d).", handoffLabel, word, handoff.HandoffVersion, issue.Key, word, handoff.HandoffVersion)
	}
	if handoff.HandoffCommit != "" {
		text += " Handoff commit: " + handoff.HandoffCommit + "."
	}
	switch {
	case handoff.Verdict == "":
	case handoff.Role != "" && !claim.IsBuiltin(handoff.Role):
		text += fmt.Sprintf(" Step %d (%s) %s: %s.", handoff.StepPlace, handoff.Role, reportedLabel, handoff.Verdict)
		if summary := strings.TrimSpace(handoff.Summary); summary != "" {
			text += " " + sentence(summary)
		}
	default:
		text += " " + testerVerdictLabel + ": " + handoff.Verdict + "."
	}
	if pr != nil && pr.Number != 0 {
		text += fmt.Sprintf(" %s: #%d.", pullRequestLabel, pr.Number)
	}
	text += reviewed.part()
	if reason != "" {
		text += " " + reasonLabel + ": " + sentence(reason)
	}
	return text
}

// sentence is s ended as a sentence: s itself where it already ends with a full stop, a question
// mark or an exclamation mark, as a review's body or a person's reason often does, and s with a
// full stop otherwise, so a reason that ends its own sentence is not given a second stop.
func sentence(s string) string {
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "!") {
		return s
	}
	return s + "."
}

// Standing is the part of a start's task that names where the started worker's work stands, as
// standingPart writes it: the working copy line for every start, and for an issue with plan rows
// the task, the worker's step in it, the task's steps, the task's text and the report of each
// earlier step. It has a type of its own, as reviewedCommit does, so a caller of task and
// ResumePhaseTask passes the one writer's part and no other string in its place. A package outside
// this one gets a value of it from WorkingCopy alone.
type Standing string

// WorkingCopy is the standing of a start of an issue with no plan rows, its working copy line
// alone: ` Working copy: branch legion/<KEY>, directory <dir>.`, with the directory dir gives the
// issue (Config.WorkspaceDir), and ` Working copy: branch legion/<KEY>.` where dir is nil or gives
// none. The branch is the issue's (claim.IssueBranch), which its workspace's bookmark names.
func WorkingCopy(issue record.Issue, dir func(record.Issue) string) Standing {
	where := ""
	if dir != nil {
		where = dir(issue)
	}
	if where == "" {
		return Standing(fmt.Sprintf(" %s: branch %s.", workingCopyLabel, claim.IssueBranch(issue.Key)))
	}
	return Standing(fmt.Sprintf(" %s: branch %s, directory %s.", workingCopyLabel, claim.IssueBranch(issue.Key), where))
}

// standingPart is where the work of a worker started in issue's phase stands, the part of its
// task after the continue line (Standing). Every start names its working copy (WorkingCopy). An
// issue with no plan rows names nothing else, so it is told today's task with that one line, and
// the prompts read a task with no `Task:` line as such an issue's (R20). An issue with plan rows
// names its one task: a step's worker is told ` Task: <id>. Step <i> of <n>: <role>.`, the task's
// steps (stepsLine), ` Task text: ` and the text of the task's block, and the report of each step
// before its own (reportsBefore). The retro and the merge run no step of the task, so their worker
// is told ` Task: <id>.` and the task's steps alone: the merger reads from them whether the task
// has a review step, and each reads the runs of the steps and their reports from Dispatch
// (`dispatch steps`), which keeps every run, the failed ones too.
//
// The phase the worker starts in is issue.Phase, the phase each caller has put the issue in
// before it composes the task. A run with plan rows whose line holds no place in that phase is
// refused: no start of such a run names one, and a task that named none would read as an issue
// with no plan rows.
func (e *Engine) standingPart(ctx context.Context, tx pgx.Tx, issue record.Issue) (Standing, error) {
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return "", err
	}
	var rows []record.PhaseRow
	if place, ok := w.placeIn(issue.Phase); w.Stored && ok && place.Kind == record.PlaceStep {
		if rows, err = e.store.Phases(ctx, tx, issue.Key); err != nil {
			return "", err
		}
	}
	return standingText(issue, e.cfg.WorkspaceDir, w, rows)
}

// standingText is standingPart's text for the worker of issue's phase, from the walk w of its
// run, the directory dir gives its working copy, and its phases rows, which only a step's text reads.
func standingText(issue record.Issue, dir func(record.Issue) string, w Walk, rows []record.PhaseRow) (Standing, error) {
	at := WorkingCopy(issue, dir)
	if !w.Stored {
		return at, nil
	}
	place, ok := w.placeIn(issue.Phase)
	if !ok {
		return "", fmt.Errorf("the task of a start of %s in %s: its task %s has no step, retro or merge in %s", issue.Key, issue.Phase, w.Task, issue.Phase)
	}
	text := string(at) + fmt.Sprintf(" %s: %s.", planTaskLabel, w.Task)
	if place.Kind != record.PlaceStep {
		return Standing(text + stepsLine(w.Steps)), nil
	}
	text += fmt.Sprintf(" Step %d of %d: %s.", place.Position, len(w.Steps), stepWord(w.Steps[place.Position-1]))
	text += stepsLine(w.Steps) + " " + taskTextLabel + ": " + strings.TrimSpace(w.Text)
	return Standing(text + reportsBefore(rows, w, place.Position)), nil
}

// stepWord is step as its plan writes it: its role, and `:<key>` after it where the step names
// the agent that runs it (acceptance:owner).
func stepWord(step record.PlanStep) string {
	if step.AgentKey == "" {
		return step.Role
	}
	return step.Role + ":" + step.AgentKey
}

// stepsLine is ` Steps: 1 <role>, 2 <role>, … then Legion's retro and merge.`: each step of a
// task by its place, in the order it runs, then the retro and the merge Legion runs after them.
func stepsLine(steps []record.PlanStep) string {
	named := make([]string, len(steps))
	for i, step := range steps {
		named[i] = fmt.Sprintf("%d %s", step.Position, stepWord(step))
	}
	return fmt.Sprintf(" %s: %s then Legion's retro and merge.", stepsLabel, strings.Join(named, ", "))
}

// reportsBefore is one ` Step <j> (<role>) reported: <word> version <v>.` for each step of w's
// task before the step at place whose role's phases row holds a handoff record: the record that
// role's completion of that step named, which `dispatch handoff-read --phase <word> --version <v>`
// reads. A role runs at most one step of an issue, so its row holds that step's facts, and each
// step before a running one completed in the run that led to it: a step that runs again after a
// return writes its new record on the row before the step after it starts. A step whose role
// writes no handoff (the owner's acceptance) has no line.
func reportsBefore(rows []record.PhaseRow, w Walk, place int) string {
	var text strings.Builder
	for j := 1; j < place; j++ {
		role := phase.RoleFor(w.Line[j-1])
		for _, row := range rows {
			if row.Role == role && row.HandoffRecord != "" {
				fmt.Fprintf(&text, " Step %d (%s) %s: %s version %d.", j, stepWord(w.Steps[j-1]), reportedLabel, handoffWord(role), row.HandoffVersion)
			}
		}
	}
	return text.String()
}

// reviewedCommit is the commit the reviewer's last deciding review read, as a start's task names
// it (reviewedPart), and "" where the task names none. It holds the commit and not the sentence
// that names it, and part writes that sentence, so no value of the type reaches a task as anything
// but that sentence or nothing. It has a type of its own so task takes it beside the reason, and a
// swap of the two does not compile, nor does a string variable passed for it. No package outside
// this one can name the type, so a caller there can pass ResumePhaseTask only an untyped
// constant, such as the "" of a start that names no commit.
type reviewedCommit string

// part is the part of a start's task that names c, " Last reviewed commit: <sha>."
// (ReviewedCommitLabel), and "" for no commit: the one place the sentence is written.
func (c reviewedCommit) part() string {
	if c == "" {
		return ""
	}
	return " " + ReviewedCommitLabel + ": " + string(c) + "."
}

// reviewedPart is the commit a start's task names as the one the reviewer's own newest deciding
// review read (record.PhaseRow.ReviewedHead, which review writes), so the reviewer can read the
// difference from that commit to the head. It is read from reviewer, the reviewer's phases row:
// the row's commit for the reviewer started in reviewing, and "" in every other case — any other
// role or phase, and the first review of a generation, whose row holds none. It is the one source
// of the part: every start that hands the reviewer a review round builds its task through task or
// ResumePhaseTask, and each takes the commit from here.
func reviewedPart(role claim.Role, at phase.Phase, reviewer record.PhaseRow) reviewedCommit {
	if !startsReviewRound(role, at) {
		return ""
	}
	return reviewedCommit(reviewer.ReviewedHead)
}

// startsReviewRound says whether a start of role in phase at hands the reviewer a review round,
// the one start whose task can name a reviewed commit (reviewedPart).
func startsReviewRound(role claim.Role, at phase.Phase) bool {
	return role == claim.RoleReviewer && at == phase.Reviewing
}

// reviewedPartFor is reviewedPart for a start of role on issue, in the phase the issue stands in,
// whose caller holds no phases row of that role: the transition's start and the promotion's. It
// reads the reviewer's row only for a review round, so no other start reads the record for it. At
// step it reads the commit of the step the task stands at (stepReviewedPart).
func (e *Engine) reviewedPartFor(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role) (reviewedCommit, error) {
	if issue.Phase == phase.Step {
		return e.stepReviewedPart(ctx, tx, issue, role)
	}
	if !startsReviewRound(role, issue.Phase) {
		return "", nil
	}
	reviewer, err := e.phaseRow(ctx, tx, issue.Key, role)
	if err != nil {
		return "", err
	}
	return reviewedPart(role, issue.Phase, reviewer), nil
}

// reviewedPartOf is reviewedPart for a start of role on issue whose caller holds role's phases row
// (row): a resume of the worker of the issue's phase. At step it reads the commit of the step the
// task stands at (stepReviewedPart), so a resumed review step is told the commit its last run read,
// as a resumed reviewer is.
func (e *Engine) reviewedPartOf(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role, row record.PhaseRow) (reviewedCommit, error) {
	if issue.Phase == phase.Step {
		return e.stepReviewedPart(ctx, tx, issue, role)
	}
	return reviewedPart(role, issue.Phase, row), nil
}

// stepReviewedPart is the commit a start of role at step names as the one the newest run of its
// step read (record.PlanStep.ReviewedHead, which recordStepReview writes), so a review step of a
// role the deployment defines can read the difference from that commit to the head, as the
// reviewer does: the commit of the step the issue's task stands at, where that step is role's and
// of the type review, and "" otherwise — a step of another type, and the step's first run of a
// generation, whose row holds none.
func (e *Engine) stepReviewedPart(ctx context.Context, tx pgx.Tx, issue record.Issue, role claim.Role) (reviewedCommit, error) {
	w, err := e.walk(ctx, tx, issue)
	if err != nil {
		return "", err
	}
	step, ok := w.planStepAt(w.Place)
	if !ok || claim.Role(step.Role) != role || step.Type != string(plan.TypeReview) {
		return "", nil
	}
	return reviewedCommit(step.ReviewedHead), nil
}

func continueLine(issue record.Issue) string {
	return fmt.Sprintf("Continue %s. Issue: %s. %s: %s.", issue.Title, issue.Key, phaseLabel, issue.Phase)
}

// handoffWord is the word of the Dispatch handoff record role writes (phase.Handoff's writer), ""
// for a role that writes none.
func handoffWord(role claim.Role) string {
	for _, p := range phase.All {
		if word, writer, ok := phase.Handoff(p); ok && writer == role {
			return word
		}
	}
	return ""
}

// ResumePhaseTask is what a worker started for a phase already under way is told. No handoff
// begins that phase — the run that did is over — so the task names the phase and says to carry
// the work on, which is what a resumed session needs to tell this phase from its last. at is where
// the worker's work stands (standingPart), placed right after the continue line, as a start's
// task places it (task): a worker resumed in a fresh session has no earlier task to read it from.
// reviewed is the commit the reviewer's last deciding review read (reviewedPart), whose part
// (reviewedCommit.part) is placed after that and before the closing sentence, for the same
// reason. It is "" for every other start.
func ResumePhaseTask(issue record.Issue, at Standing, reviewed reviewedCommit) string {
	return continueLine(issue) + string(at) + reviewed.part() + " Resume the existing phase work."
}

// resumeTask is ResumePhaseTask of the worker of issue's phase, standing where the record holds
// its work (standingPart).
func (e *Engine) resumeTask(ctx context.Context, tx pgx.Tx, issue record.Issue, reviewed reviewedCommit) (string, error) {
	at, err := e.standingPart(ctx, tx, issue)
	if err != nil {
		return "", err
	}
	return ResumePhaseTask(issue, at, reviewed), nil
}

// HeldNotice is the notice of issue's hold as its architect answers one, with a retry or an
// escalation: the phase the hold came from, the role that works it, and why it is held. Admission
// tells a carried root's own architect with it, since the hold's first notice went to the
// architect of the tree the root was handed over from.
func HeldNotice(issue record.Issue) record.Notice {
	return record.Notice{Kind: "held", Role: issue.RoleAt(issue.Hold.From), Phase: issue.Hold.From, Reason: string(issue.Hold.Reason)}
}

// ChildReenteredNotice is the notice a re-entered child's tree architect is told, whichever path
// re-enters it: reenterChild for a live tree's reopened child, or admission's
// reenterStrandedChildren for one a reopen declined to re-enter for want of the label until its
// root's own promotion made the tree live.
func ChildReenteredNotice(child, root string) record.Notice {
	return record.Notice{Kind: "child-status", Role: claim.RoleArchitect, Reason: fmt.Sprintf("%s is todo; it runs again under %s", child, root)}
}

// ChildStartedNotice is the notice the tree's architect is told whenever a child of it starts
// its first phase (Engine.StartFirstPhase), its reason naming that phase, the first step of the
// child's plan (planning on today's default steps), and saying why it starts now. waited is every
// issue the child's mirror row still names, in Dispatch's own order, all of them done by now:
// the last of them closed, which is what freed the child.
//
// Empty, the child's own links say nothing about why it starts, and the reason says what is true
// of every child the line starts instead: it reached the front of the waiting line and took a
// slot. That covers both an issue that never named a blocker — which is most of them now that a
// child waits for a slot like any other issue — and one whose blocked-by links a person or the
// architect took off while it waited, which the record cannot tell apart from the first. Naming
// links that were taken off would be a guess, and a reason the architect reads and cannot check
// is worse than a plain one.
//
// A start nobody hears of is the silent state the hold's report exists to remove, so no child
// starts without one. It is the same child-status kind a child's leave and a re-entry are told
// with — the daemon's notice routing sends one on a non-root child to the architect that owns it
// — and not phase-finished: no phase of the child ended, it is beginning its first.
func ChildStartedNotice(child, root string, first phase.Phase, waited []string) record.Notice {
	reason := fmt.Sprintf("%s starts %s under %s: every issue it waited on (%s) is done", child, first, root, strings.Join(waited, ", "))
	if len(waited) == 0 {
		reason = fmt.Sprintf("%s starts %s under %s: it reached the front of the waiting line and took an admission slot", child, first, root)
	}
	return record.Notice{Kind: "child-status", Role: claim.RoleArchitect, Reason: reason}
}

func (e *Engine) lingerAt() time.Time { return e.now().Add(e.cfg.Linger) }
