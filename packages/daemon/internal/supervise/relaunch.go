package supervise

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// RelaunchSentence is what a claim's next process is told of what the last let-go of its process
// ended (EndedWork, dispatch://INFRA-3276 R3), where the sentence stands alone: the delivery of the
// daemon's own that a claim holding no task is given at its ready (queueRelaunch), and a root
// architect's catch-up, which the workflow is handed through the ready's report
// (intake.ClaimReady.Relaunch, workflow's QueueCatchUp). In front of a task the same record is told
// by the same composer, ending in the task's own instruction instead (relaunchSentence,
// Delivery.message).
//
// It reads ended as the let-go recorded it, so the caller asks whether there is anything to tell
// (EndedWork.Tells) before it asks for the words.
func RelaunchSentence(ended EndedWork) string { return relaunchSentence(ended, false) }

// relaunchSentence is the one composer of the sentence a record of a let-go is told in, for the
// sentence alone (RelaunchSentence) and in front of a task (task true: Delivery.message, and
// interruptedTask's record of a turn cut off). Another agent reads it, so it follows ASD-STE100,
// as providerErrorTask does: short sentences, one statement each. In order, it says:
//
//   - why the process ended, in the record's own words (EndedWork.Cause), which read after
//     "because" and quote whatever of them is not the daemon's (endDied, endFailed); for a task
//     whose turn the let-go cut off (EndedWork.TaskTurn) it says first that this turn was
//     interrupted, and Delivery.message hands it a record of the task's turn only in front of
//     the task that let-go took back (EndedWork.inFrontOf);
//   - whether a turn was cut off, and that its tool calls in flight were never answered, or that
//     the agent was in no turn;
//   - for a turn cut off, what of the background work ended: first each kind of it the record
//     names (EndedWork.Kinds), all of which ended with the process, or background work of no
//     named kind — a report older than the turn still names what ran in the process (recordEnd);
//     then, for a record that holds no report of that turn (EndedWork.Reported: the process left
//     none, or its newest report is older than the turn's start), that the daemon has none; then
//     that the subagents and background jobs of that turn are gone, which is said of every turn
//     cut off, since no report shows all that a turn started: Oh My Pi reads its background work
//     every five seconds (pi-legion's SESSION_ACTIVITY_PERIOD_MS), so even a report read after
//     the turn began can miss what it started in its last seconds; and last, for a record that
//     holds no report of the turn and names no background work at all, that every other subagent
//     and background job the process started is gone too;
//   - with no turn cut off, each kind of background work the record names; then, for a record
//     that holds no report of the process's last turn (EndedWork.Reported), that the daemon has
//     no report of the background work of that turn, so every subagent and background job the
//     agent started in that process is gone. That is true of a process that left no report, of
//     one whose only report is older than its last turn, whether that report named background
//     work or nothing (recordEnd), and of a record joined from such a one (joined): what that
//     turn started is unknown, and unknown is said, never read as nothing;
//   - that no result of that work will come, and that the transcript does not say so, since the
//     resumed session shows neither a lost tool call nor a lost subagent as missing
//     (dispatch://INFRA-714 R6);
//   - what to do: check what the work did before doing it again, and for a task, then continue it.
//
// The two statements the sentence of an interrupted task made before this record existed — the
// tool calls in flight never answered, and the subagents and background jobs of that turn gone —
// keep their words, which agents and the daemon's own tests read, and are said of every turn cut
// off, as they were then.
//
// The words of the background work are the three kinds the store keeps (backgroundKind); a kind
// it does not know is not read into the prompt, and a report that named none of them is said as
// background work of no named kind.
func relaunchSentence(ended EndedWork, task bool) string {
	cutTask := task && ended.TaskTurn
	process := "Your previous process"
	if cutTask {
		process = "Your process"
	}
	because := process + " ended, and the daemon recorded no cause for it."
	if ended.Cause != "" {
		because = process + " ended because " + ended.Cause + "."
	}
	// A record of the task's turn with no turn running is one whose let-go's own decision took the
	// task back while the claim was in no turn, as from a turn that ended before its prompt's result
	// came (recordEnd): the task's turn is the one cut off, and it is told as one, as a task's
	// interruption always was (interruptedTask).
	turn := ended.Turn || ended.TaskTurn
	var said []string
	switch {
	case cutTask:
		said = append(said, "Your previous turn on this task was interrupted.", because)
	case turn:
		said = append(said, because, "The turn you were in was interrupted.")
	default:
		said = append(said, because, "You were not in a turn then.")
	}
	// stopped is what the record names of the background work that ended with the process, or ""
	// for a record that names none.
	var stopped string
	switch kinds := backgroundKinds(ended.Kinds); {
	case kinds != "":
		stopped = "Each " + kinds + " that you started stopped with that process."
	case ended.Background:
		stopped = "The background work that you started stopped with that process."
	}
	switch {
	case turn:
		said = append(said, "Your tool calls in flight were never answered.")
		if stopped != "" {
			said = append(said, stopped)
		}
		if ended.Reported {
			said = append(said, "The subagents and background jobs of that turn are gone.")
			break
		}
		said = append(said, "The daemon has no report of the background work of that turn.",
			"So the subagents and background jobs of that turn are gone.")
		if stopped == "" {
			said = append(said, "Every other subagent and background job that you started in that process is gone too.")
		}
	case !ended.Reported:
		// No report speaks of the process's last turn: it left none, its only report is older than
		// that turn and names the work that ran then or nothing (recordEnd), or the record is
		// joined from such a one. What that turn started is unknown.
		if stopped != "" {
			said = append(said, stopped)
		}
		said = append(said, "The daemon has no report of the background work of your last turn.",
			"So every subagent and background job that you started in that process is gone.")
	case stopped != "":
		said = append(said, stopped)
	}
	said = append(said, "No result of that work will come to you.", "Your transcript does not say so.")
	if task {
		said = append(said, "Before you do any of that work again, check what it already did.", "Then continue the task.")
	} else {
		said = append(said, "Before you start any of that work again, check what it already did.")
	}
	return strings.Join(said, " ")
}

// inFrontOf is the record as the sentence in front of task d tells it (Delivery.message). The
// turn of the claim's task the record says was cut off (EndedWork.TaskTurn) is d's own only where
// d is the task the let-go took back from that turn (Delivery.Interrupted, interrupted): a let-go
// that takes nothing back leaves the task a turn confirmed to settle, which retires it, and a
// confirmed task is never sent again. So any other task was never in that turn: the operator's
// retry of a worker a stop cut off in its task's turn is the case, since the stop retires that
// task and the retry gives the claim a new task of its phase (api's retryStart). In front of such
// a task the turn cut off is told as the sentence alone tells it (RelaunchSentence): as the turn
// the agent was in, never as this task's.
func (e EndedWork) inFrontOf(d Delivery) EndedWork {
	if !d.Interrupted {
		e.Turn, e.TaskTurn = e.Turn || e.TaskTurn, false
	}
	return e
}

// backgroundKinds names each kind of background work a report named, in the report's order, for
// the sentence "Each … that you started stopped with that process": "subagent and each background
// job", and "" for a report that named none of the three kinds the store keeps
// (claim.BackgroundWork.KnownKinds).
func backgroundKinds(kinds []string) string {
	var named []string
	for _, kind := range kinds {
		if words := backgroundKind(kind); words != "" {
			named = append(named, words)
		}
	}
	return strings.Join(named, " and each ")
}

// backgroundKind is the words an agent reads for one kind of background work the session reports
// (pi-shared's session-activity): a `task` subagent, an `async` job of the session's own, and a
// background task of a Claude Code worker.
func backgroundKind(kind string) string {
	switch kind {
	case "subagent":
		return "subagent"
	case "async-job":
		return "background job"
	case "claude-background-task":
		return "Claude Code background task"
	}
	return ""
}

// relaunchTaskPrefix starts the delivery id of the delivery of the daemon's own that tells a
// claim holding no task what its relaunch ended (queueRelaunch); a fresh word of its own completes
// it (relaunchTaskID). The prefix is the delivery's whole identity: it is what says the delivery
// carries the record's sentence as its text rather than in front of a task (Delivery.message),
// and that a newer task may take its place while no prompt of it is waiting on a result
// (replaceable). It is kept across every take-back, as an update round's ask keeps its round
// (rotateTaskID).
const relaunchTaskPrefix = "relaunch:"

// relaunchTaskID is the id of one delivery of the daemon's own that tells a relaunch's record.
// Its word is random, as an operator's own delivery id is (queue), for the reason updateTaskID
// gives: the shim answers a prompt whose id it holds from its own record without prompting the
// agent, so each delivery and each take-back needs an id the shim has not seen.
func relaunchTaskID() string { return relaunchTaskPrefix + rand.Text() }

// relaunchTask is whether delivery id is the daemon's own delivery of a relaunch's record.
func relaunchTask(id string) bool { return strings.HasPrefix(id, relaunchTaskPrefix) }

// Relaunch is whether d is the daemon's own delivery of a relaunch's record (queueRelaunch): no
// work of the claim's — no phase, no run, no entry — only the sentence of what the last
// let-go of its process ended. A reader that asks whether a claim holds work it was given asks
// HoldsTask.
func (d Delivery) Relaunch() bool { return relaunchTask(d.ID) }

// HoldsTask is whether pending, the delivery a claim holds (Claim.Pending, or the pending task a
// role run reads, record.RoleClaim.Pending), is a task of the claim's: a delivery, and not the
// daemon's own delivery of a relaunch's record (Delivery.Relaunch), which is no work of the
// claim's. It is the one statement of that rule, and every reader that asks whether a claim still
// owes work it was given asks it: a death's charge (chargeDeath), a retirement's end of the
// counts charged against the task it retires (retirePending), the workflow's owed and
// readyWorkerTask, and api's freshSession and heldTaskNotCurrent, which then tells a nil from a
// relaunch's delivery only to choose its reason text. It takes the pointer the claim holds, nil
// for none, because those readers hold two types of claim and the delivery is the one field they
// share.
func HoldsTask(pending *Delivery) bool { return pending != nil && !pending.Relaunch() }

// RecordTurn is the claim's record of the turn that confirmed the daemon's own relaunch record
// (Delivery.Relaunch), which is what that turn's end reads to decide what it ends of the two counts
// a completed turn ends (turnEndsCounts). confirm writes it at every confirmation of a record and
// clears it at the confirmation of a task (confirmRecordTurn), and every turn start that confirms
// no record clears it (forgetRecordTurn). It is persisted with the claim (store's record_turn), in
// the write that confirms the record, so a daemon restarted at any point between that confirmation
// and the turn's end reads what the daemon before it wrote. A claim holds none (nil) until a turn
// confirms a record, and none is also what says the base is unknown.
type RecordTurn struct {
	// At is the start of the turn that confirmed the record: the claim's LastTurnStart at that
	// confirmation. The turn now ending is that record's exactly while At is not zero and equals
	// Claim.LastTurnStart (Claim.recordsTurn).
	At time.Time `json:"at"`
	// Base is the two counts when that confirmation was made, which the confirmation of the same
	// record sent again measures the charge of the end between them from.
	Base TurnCounts `json:"base"`
	// TakeBack is what the record's turn takes back from the two counts when it ends, each 0 or 1.
	// That end zeroes it in the write that takes it back, so it is taken back once.
	TakeBack TurnCounts `json:"takeBack"`
}

// TurnCounts is the two counts a completed turn ends (turnEndsCounts): the deaths
// (Budgets.Deaths) and the run of pod ends from outside the pod that found the ready agent at work
// (Budgets.PodEndsNotOurs).
type TurnCounts struct {
	Deaths         int `json:"deaths"`
	PodEndsNotOurs int `json:"podEndsNotOurs"`
}

// UnmarshalJSON reads a record turn as the store keeps it (store's record_turn) and refuses one
// no confirmation writes: a negative count, or a take-back above one. A daemon that read such a
// record would take back more than any one end charges.
func (r *RecordTurn) UnmarshalJSON(data []byte) error {
	type stored RecordTurn
	var read stored
	if err := json.Unmarshal(data, &read); err != nil {
		return err
	}
	for _, count := range []int{read.Base.Deaths, read.Base.PodEndsNotOurs, read.TakeBack.Deaths, read.TakeBack.PodEndsNotOurs} {
		if count < 0 {
			return fmt.Errorf("the record turn %s holds a negative count", data)
		}
	}
	if read.TakeBack.Deaths > 1 || read.TakeBack.PodEndsNotOurs > 1 {
		return fmt.Errorf("the record turn %s takes back more than one of a count", data)
	}
	*r = RecordTurn(read)
	return nil
}

// countsOf is the two counts of b that a completed turn ends.
func countsOf(b Budgets) TurnCounts {
	return TurnCounts{Deaths: b.Deaths, PodEndsNotOurs: b.PodEndsNotOurs}
}

// oneAtMost is n clamped to 0..1: what one end can have charged one count.
func oneAtMost(n int) int { return min(max(n, 0), 1) }

// recordsTurn is whether the turn the claim last began is the turn that confirmed a relaunch
// record: the claim holds a record turn whose start is that turn's (RecordTurn.At). It reads the
// persisted claim alone.
func (c Claim) recordsTurn() bool {
	r := c.RecordTurn
	return r != nil && !r.At.IsZero() && r.At.Equal(c.LastTurnStart)
}

// confirmRecordTurn records, at the confirmation of d (confirm), what the turn now confirming it
// takes back at its end (turnEndsCounts, which states the rule). A task's confirmation clears the
// record turn: its turn is the claim's own work. A record's confirmation dates the turn
// (Claim.LastTurnStart) and records the two counts now as its base. Its take-back is zero but for
// a record a let-go took back from an earlier turn of it (Delivery.Interrupted) whose base the
// claim still holds: then each amount is the count now less that base, one at most and never
// below zero, which is what the end that cut the earlier turn charged.
func (m *Machine) confirmRecordTurn(d Delivery) {
	if !d.Relaunch() {
		m.claim.RecordTurn = nil
		return
	}
	now := countsOf(m.claim.Budgets)
	turn := &RecordTurn{At: m.claim.LastTurnStart, Base: now}
	if last := m.claim.RecordTurn; d.Interrupted && last != nil {
		turn.TakeBack = TurnCounts{
			Deaths:         oneAtMost(now.Deaths - last.Base.Deaths),
			PodEndsNotOurs: oneAtMost(now.PodEndsNotOurs - last.Base.PodEndsNotOurs),
		}
	}
	m.claim.RecordTurn = turn
}

// forgetRecordTurn clears the record turn at a turn start that confirms no record: beginTurn's
// start of a turn that is not the pending delivery's, the busy answer that records a turn
// (recordBusyTurn), the agent's word that it is in a turn of its own after its task was dropped
// (answerAskFirstAfterDrop), and a turn Oh My Pi starts under a compaction (turnStartedWhileBusy).
// Such a turn is no record's, and an end that cuts it is counted against it, so the base is
// forgotten: a record re-sent after it takes back nothing (turnEndsCounts), and the claim fails
// sooner.
func (m *Machine) forgetRecordTurn() { m.claim.RecordTurn = nil }

// turnEndsCounts ends, at a turn's end (turnEnded), what that turn ends of the two counts a
// completed turn ends: the deaths (Budgets.Deaths) and the run of pod ends from outside the pod
// that found the ready agent at work (Budgets.PodEndsNotOurs). It is the one statement of that
// rule. completed is false for a turn a compaction aborted, which completed nothing, so such a
// turn takes nothing from the run (turnEnded).
//
//   - A turn of the claim's own work ends both: a task's turn, and a turn that confirmed nothing,
//     such as one a message started.
//   - A turn of the daemon's own relaunch record (queueRelaunch) takes back only what the end that
//     cut that record's earlier turn charged: at most one from each count, never below zero
//     (RecordTurn.TakeBack, which confirmRecordTurn measured). A fresh record's turn takes back
//     nothing. The ends and deaths standing when it completes were counted against a message's
//     turn or a task, not against it, and the record is the daemon's own act, given a claim
//     holding nothing at every relaunch's ready, so a turn completed on it is no evidence that
//     the claim's work gets through. Read as such evidence, it would end both at each relaunch
//     after a message's turn the cluster ended or the process died in, and a claim whose messages
//     keep ending its pod or killing its process would never reach the limit. The turn of a
//     record a let-go took back (interrupted) and the relaunch sent again completes the work the
//     end in its earlier turn cut off, so it takes back that end's charge and no other: a run of
//     ends whose last end cut a record's turn keeps every end before it, and reaches the limit
//     as it would with no record at all.
//
// Nothing the claim holds says what an end charged: in died the take-back of the turn
// (interrupted) runs before the charge (chargeDeath), and an end from outside the pod at the
// limit raises the run and then goes to died, which raises the deaths too. So the charge is
// measured: the confirmation of a record records the two counts (RecordTurn.Base), and the
// confirmation of that record sent again takes the difference from them, one at most of each.
// Two ends are mixed into one difference only where a second end is counted between the cut and
// that confirmation. One is an end in a turn that confirms no record, and every such turn's start
// forgets the base (forgetRecordTurn), so the record then takes back nothing and the claim fails
// sooner. The other is an end while the claim works for a compaction, which starts no turn: the
// cap takes back one of the two, and the other stands. Each confirmation measures from the one
// before it, so a record cut twice and then completed takes back the second end's charge alone,
// and the claim fails sooner there too.
//
// The rule is not that an end inside a record's turn goes uncounted. A claim whose process dies in
// every turn of its record — a session Oh My Pi crashes on at its first prompt — or whose
// Burstable pod evicts itself in every such turn completes no turn, and counting those ends
// against the open record's turn is what bounds it; uncounted, it would be relaunched for ever.
// A parked claim, holding no task, whose ends each land in a turn of its record has each one
// taken back when the record sent again completes, so it is relaunched however often the
// environment ends it (Budgets.Deaths).
//
// The record's retirement ends nothing (retirePending, HoldsTask): it is no task of the claim's.
//
// Whose turn is ending is decided from the persisted claim alone, read here: the turn is the
// record's exactly while the claim's record turn starts where its last turn did
// (Claim.recordsTurn). Every turn start either moves Claim.LastTurnStart or forgets the record
// turn, and the one that confirms a record writes a new one, so no earlier turn's record can be
// read for this one. A task delivered during the record's turn (queue) and a provider error's
// result before its end (providerErrored) change the pending delivery and neither field, and a
// daemon restarted before, after or between them reads both fields from the row, so none of them
// changes the answer.
//
// The record turn's take-back is zeroed here, beside the counts it changes, so the turn's end
// writes both together and a second end of the same turn takes nothing more.
func (m *Machine) turnEndsCounts(completed bool) {
	b := &m.claim.Budgets
	if !m.claim.recordsTurn() {
		b.Deaths = 0
		if completed && b.PodEndsNotOurs != 0 {
			m.log.Info("supervise: a turn completed; the run of pod ends from outside the pod is over", "podEndsNotOurs", b.PodEndsNotOurs)
			b.PodEndsNotOurs = 0
		}
		return
	}
	turn := *m.claim.RecordTurn
	took := TurnCounts{Deaths: min(turn.TakeBack.Deaths, b.Deaths)}
	if completed {
		took.PodEndsNotOurs = min(turn.TakeBack.PodEndsNotOurs, b.PodEndsNotOurs)
	}
	b.Deaths -= took.Deaths
	b.PodEndsNotOurs -= took.PodEndsNotOurs
	turn.TakeBack = TurnCounts{}
	m.claim.RecordTurn = &turn
	m.log.Info("supervise: a turn of the relaunch's record ended; it takes back only what the end that cut its earlier turn charged",
		"tookBackDeaths", took.Deaths, "tookBackPodEndsNotOurs", took.PodEndsNotOurs,
		"deaths", b.Deaths, "podEndsNotOurs", b.PodEndsNotOurs)
}

// queueRelaunch gives a claim that holds no delivery at its ready a delivery of the daemon's own
// that carries the sentence of its untold record alone (RelaunchSentence), so the next prompted
// turn of the new process reads what the last one ended at once, whatever the role: a root
// architect's goal-red-team reading that died while it held nothing is the measured case
// (dispatch://INFRA-3276 R3). A claim that holds a delivery needs none: its task carries the
// sentence in front of it (Delivery.message).
//
// The delivery has no phase, no run, no entry and no look, so nothing holds it back
// (phaseHolds, asksRoot), no working copy is adopted for it (adopts), and its confirmation and
// retirement leave what the claim is serving alone and end no phase (serving, settle). Its id is
// relaunchTaskID's. The record is told when a turn confirms a prompt of it (confirm), as a task's
// would be, and its text is the record as it stands when the prompt goes (Delivery.message).
//
// It is queued by finishReady after the ready's durable write and report, which the workflow reads
// synchronously (Machine.OnDurableState, the daemon's applyTerminal): the workflow's ready reads
// the claim holding nothing, so a phase worker is still queued its resume task
// (workflow's readyWorkerTask), and that task takes this delivery's place while no prompt of it is
// waiting on a result, or waits for its turn to end (queue, replaceable). It is queued again where
// sendPending drops the task a claim held and leaves it holding nothing (relaunchAfterDrop), since
// the task that would have carried the sentence is gone.
//
// One claim is given none: the daemon-launched controller. Its keeper sends its start message at
// every ready and only logs a refusal of it (the daemon's controllerKeeper.ready), so a delivery
// queued here would make the start procedure wait behind a sentence; the start message is a
// delivery, which carries the sentence in front of it instead. A claim whose fresh session has not
// yet told the session it abandoned (Claim.Abandoned) is given one as any other is: a record that
// tells here is one of the fresh session's own processes (forgetSession), and the delivery carries
// the abandoned session's sentence and then the record's (Delivery.message).
func (m *Machine) queueRelaunch(ctx context.Context) error {
	ended := m.claim.EndedWork
	if !ended.Tells() || m.claim.Pending != nil || m.claim.Role == claim.RoleController {
		return nil
	}
	m.log.Info("supervise: the claim holds no task; a delivery of the daemon's own tells its agent what the relaunch ended",
		"cause", ended.Cause, "ended", ended.Generation)
	return m.queue(ctx, RequestDeliver{Claim: m.claim.Token, ID: relaunchTaskID(), Task: RelaunchSentence(ended)})
}
