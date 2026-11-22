package supervise

// The work hold (`gates.hold: work`) as one claim's machine keeps it. Its stop is a suspension like
// any other (RequestSuspend, By SuspendedByHold): held for the agent's turn and cut at a deadline
// the outbox stores (holdSuspension, suspendAtTimeout), and for a ready or idle claim held for the
// turn its close message starts (RequestSuspend.TurnAfter). What is the hold's own is here: the
// one check that holds every request that could start a turn of a claim the hold stopped
// (workHoldWaits, read by the fence through each event's door), and the hold's end. Each decision
// that may make the hold's stop reads whether the tree's work hold row stands (readHoldForStop,
// Store.WorkHoldStands). The read that finds the row gone drops a stop not yet made, and answers
// the hold's stop asked now ErrWorkHoldEnded. No decision holds the row after that read, so no
// decision's runtime call holds back the opening, which waits for the row under the lock every
// fact takes. Once the stop is written, the machine reads the row again (holdReadAfterStop). The
// opening takes that row for update before it reads the tree's claims. So either the opening reads
// the stop and starts the claim, or the read after the stop finds the row gone and the daemon
// starts the claim (Deps.WorkHoldEndedUnderStop). The opening's start drops a stop not yet made
// too, and starts a claim the hold stopped with one task (EndHold). The stop's own outbox row
// drops a stop not yet made where the row meets a tree that no longer holds work (DropHoldStop).

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// workHoldWaits is the work hold's answer to ev, and nil where it has none: a claim the hold
// suspended answers every request that could start or resume a turn of its agent a wait
// (ErrHeldByWorkHold) — a start's resume by rule, a task, a wake, a CI settlement's or a hook's
// task, the boot's relaunch, a pause's resume and the resume of a launch it owed, the controller
// keeper's resume or retry, a compaction, a ready — except the operator's own resume and retry,
// which start it (turnDoor). Each request answers it before its row is reached, so no row of the
// table has to know the hold, and a request added later is held by the door it declares. The
// claim is left exactly as it was: the task it kept stays kept, and the turn-starting request is
// asked again by whoever holds it (the outbox's backoff) or is a request the hold's end makes
// moot, since that end starts the claim itself (EndHold).
//
// A claim the hold's stop is only held over — in a turn, a compaction, idle waiting for the turn
// its message starts, or a launch held until its ready — is not answered here: its stop is held
// (Machine.held), so nothing is sent to it (sendPending), and while the read of the tree's work
// hold row finds the hold standing (readHoldForStop), its row runs the stop where the stop is due.
// A start of such a claim waits all the same (StartedBy).
func (m *Machine) workHoldWaits(ev Event) error {
	if ev.door() != ruleDoor || !m.claim.WorkHeld() {
		return nil
	}
	name, ok := requestName(ev)
	if !ok {
		name = string(kindOf(ev))
	}
	m.log.Info("supervise: the work hold holds the claim; the request waits for the hold's end", "request", name)
	return &RefusedError{State: m.claim.State, Request: name, Err: ErrHeldByWorkHold}
}

// EndHold is the work hold's end for this claim, and the one door the hold's opening takes into
// the claim; it is no event of the table, as StartedBy is none, because no request of a rule may
// lift the hold. The outbox calls it once the claim's tree no longer holds work, before the start
// it is running against a claim the hold holds goes on as an ordinary start.
//
//   - A stop the hold asked for and has not made — held for a turn, a compaction, a launch's ready,
//     or an idle claim's next turn — is not made: it is dropped, and a task it held back is sent
//     where it may go now (dropHoldStop). EndHold reports false: the claim runs on, and nothing
//     of task is used. A decision of the claim whose read of the tree's work hold row finds the
//     row gone drops such a stop too (readHoldForStop), so EndHold finds one only where no
//     decision came between the opening and the start.
//   - A claim the hold stopped is started again, paced as any start by rule is, with one task,
//     never two: task, the opening start's, whose text starts with the opening's sentences
//     (opening). Where the claim kept a task of its own that no turn has taken — a released
//     worker's pending task, a task the hold's stop held back, a task its cut turn was working —
//     and task carries no newer work than it, the one task is that kept task's work behind the
//     opening's sentences instead (openingTask), and the kept delivery is retired in its place.
//     The one task is queued under task's id and row, so the start's own delivery of task, made
//     once this returns, queues nothing more (given). EndHold reports true. With task nil the
//     claim is started with whatever it kept.
//   - Any other claim — one an operator's suspension or the daemon's own took over, one the hold
//     never reached — is left as it is, and EndHold reports false.
//
// A claim the hold stopped that records a session it does not hold is refused its start before
// anything of it changes (resumeRefusal), as a resume, a resume by rule and a delivery that
// resumes are: a RefusedError naming the forget-session to run, the claim left stopped by the hold
// with the task it kept, and nothing launched or charged, where a relaunch would be refused at
// every try until the claim failed. The outbox asks again on its backoff.
//
// A start the lifecycle refuses (Store.AdmitClaim, a reserved cleanup) leaves the claim suspended
// by the hold with the one task queued, and the outbox asks again: the task is not composed a
// second time, since its row is already the claim's (given). Nor is a task queued that the claim
// was given already, and the task it kept is then kept as it is.
//
// EndHold takes the machine's lock outside Handle, so it first makes the claim write the machine
// owes, as Handle does at the head of every decision (repairBeforeTheHold). Where that write is
// still owed, the store does not hold the claim the machine holds, so EndHold answers an error
// and changes nothing, and the outbox asks again on its backoff.
func (m *Machine) EndHold(ctx context.Context, opening string, task *RequestDeliver) (bool, error) {
	m.mu.Lock()
	defer m.unlock()
	if err := m.repairBeforeTheHold(ctx, "work hold's end"); err != nil {
		return false, err
	}
	if dropped, err := m.dropHoldStop(ctx); dropped || err != nil {
		return false, err
	}
	if !m.claim.WorkHeld() {
		return false, nil
	}
	if err := m.resumeRefusal(ctx, "work hold's end"); err != nil {
		return false, err
	}
	if task != nil {
		if one, kept := m.openingTask(opening, *task); !m.given(one) {
			if p := m.claim.Pending; p != nil {
				m.log.Info("supervise: the work hold's opening replaces the task the claim kept with its one task",
					"kept", p.ID, "keptWork", kept, "delivery", one.ID)
				if err := m.dropPending(ctx); err != nil {
					return false, fmt.Errorf("retire the task %s kept before the work hold's opening: %w", m.claim.Token, err)
				}
			}
			if err := m.queue(ctx, one); err != nil {
				return false, fmt.Errorf("queue the work hold's opening task of %s: %w", m.claim.Token, err)
			}
		}
	}
	m.log.Info("supervise: the work hold ended; the claim it stopped starts again", "delivery", pendingID(m.claim.Pending))
	if err := m.revive(ctx, false, nil); err != nil {
		return false, err
	}
	return true, nil
}

// DropHoldStop is the work hold's end for a claim whose stop row meets a tree that no longer
// holds work (the outbox's holdStopUnmade), decided under the claim's lock in one call. It is the
// stop row's end of the drop a decision makes itself when its read of the tree's work hold row
// finds the row gone (readHoldForStop). A stop the hold asked for and has not made, which no
// decision has dropped yet, is dropped, as EndHold drops it, and dropped is true. Nothing is
// started: a claim the hold has already stopped is left stopped, and stopped is true, so the
// caller has the hold's opening start it with the opening's sentences where no opening start
// covers it. The opening may have read such a claim before its stop's write: the decision lets
// the row go once it has read it (readHoldForStop), and a read may fail. The read of the row
// after the stop's write starts such a claim (holdReadAfterStop). This is the backstop for a read
// that failed or missed its deadline. Any other claim is left as it is, and both are false.
//
// DropHoldStop takes the machine's lock outside Handle, so it first makes the claim write the
// machine owes, as Handle does at the head of every decision (repairBeforeTheHold). The caller
// applies a fact that reads the claims from the store, so a stop whose own write failed must be
// in the store before stopped reports it. Where the write is still owed after the repair,
// DropHoldStop answers an error, drops nothing and reports nothing, and the outbox row asks again
// on its backoff.
func (m *Machine) DropHoldStop(ctx context.Context) (dropped, stopped bool, err error) {
	m.mu.Lock()
	defer m.unlock()
	if err := m.repairBeforeTheHold(ctx, "work hold stop's drop"); err != nil {
		return false, false, err
	}
	if dropped, err := m.dropHoldStop(ctx); dropped || err != nil {
		return dropped, false, err
	}
	return false, m.claim.WorkHeld(), nil
}

// errClaimWriteOwed is the hold's end met by a claim write the database has not taken, which the
// repair could not make either (repairBeforeTheHold).
var errClaimWriteOwed = errors.New("a claim write the database has not taken is still owed")

// repairBeforeTheHold makes the claim write the machine owes (repair) before EndHold or
// DropHoldStop reads the claim, as Handle does at the head of every decision: both take the
// machine's lock outside Handle. A write the database refused for good drops the mark, as the
// repair's rule does, and the caller goes on. A write still owed (Machine.owedWrite) means the
// store does not hold the claim the machine holds, and a fact the caller applies would read the
// claim the store holds. So it answers errClaimWriteOwed, and the caller changes nothing. request
// names what the caller was asked to do, for the error.
func (m *Machine) repairBeforeTheHold(ctx context.Context, request string) error {
	m.repair(ctx)
	if m.owedWrite == nil {
		return nil
	}
	return fmt.Errorf("supervise: the %s of %s waits: %w", request, m.claim.Token, errClaimWriteOwed)
}

// dropHoldStop drops a stop the work hold asked for and has not made — held for a turn, a
// compaction, a launch's ready, or an idle claim's next turn — and sends a task it held back where
// it may go now. It reports whether there was one. A project pause's hold whose pause has ended is
// dropped first, as at the start of every decision (dropEndedPauseHold).
func (m *Machine) dropHoldStop(ctx context.Context) (bool, error) {
	m.dropEndedPauseHold()
	if m.held == nil || m.held.By != SuspendedByHold {
		return false, nil
	}
	m.dropUnmadeHoldStop()
	return true, m.sendPending(ctx)
}

// readHoldForStop reads whether the tree's work hold row stands (readWorkHold,
// Store.WorkHoldStands) for a decision that may make the work hold's stop: a decision of a
// claim the hold's stop is held over (Machine.held, By SuspendedByHold), or one of the hold's stop
// asked now (RequestSuspend, By SuspendedByHold). Handle calls it after the claim's lock, before
// the fence. The opening takes the same row for update before it reads the tree's claims, and
// deletes the row in that same transaction (workflow's tellAndEndWorkHold). So a read asked while
// an opening holds the row waits for the opening's commit. Where the opening has committed, the
// read finds no row:
//
//   - A stop held over the claim is not made: it is dropped (dropUnmadeHoldStop), and dropped is
//     true, so the decision sends a task the stop held back once its row has run. A turn's end
//     leaves the claim idle, a compaction's end gives the claim back, the hold's own deadline cuts
//     nothing, and a launch's ready goes ahead.
//   - The hold's stop asked now is not made: err is ErrWorkHoldEnded. Where the stop's row posts
//     that stop again while it is held over the claim, the held stop is dropped as above, and the
//     task it held back is sent. Where the claim already stands stopped by the hold, an earlier
//     attempt of the row made the stop, and the read of the row after the stop did not run or
//     failed (holdReadAfterStop). The opening may have read that claim live, so the daemon is told
//     that the hold has ended under the stop (holdEndedUnderStop), and the daemon starts the
//     claim. That fact reads the claims from the store, so where the stop's own claim write is
//     still owed — Handle's repair failed again — the daemon is not told, and err wraps
//     errClaimWriteOwed instead, not ErrWorkHoldEnded: the row asks again, meets the open gate,
//     and DropHoldStop makes the write before it reports the claim stopped.
//
// The decision holds no lock on the row after this read. The opening waits for the row under the
// lock every fact takes (intake.SerializeFacts), so a decision that held the row across a call of
// the runtime — a process stop, a tree's or an issue's close — would hold every fact of the daemon
// for as long as that call ran, whatever source asked for it. So the opening can commit after this
// read and before the decision writes the stop. That is the one order the read leaves: a stop
// decided in the instant before an opening commits is made, and the claim is started at once with
// the opening's task. Each write of the hold's stop is followed by a read of the row in a
// transaction of its own (holdReadAfterStop). That read waits for an opening in flight, and where
// it finds the row gone, the daemon starts the claim (Deps.WorkHoldEndedUnderStop).
//
// The wait for the read ends at the claim-write deadline (Deps.claimWrite), as a claim write's
// wait does. A read that fails — the store failed, or the deadline passed — keeps a stop held over
// the claim held, with a warning, and the decision goes on: a stop it makes is read after as every
// stop is, and the stop's outbox row asks again on its backoff (DropHoldStop, the outbox's
// holdStopUnmade). The hold's stop asked now answers that error instead, so its outbox row asks
// again.
//
// A tool step and a model's answer read nothing, whatever the claim holds. Each kind's row in
// every state records what the agent did and nothing else: toolStepped writes the claim's steps
// and answered keeps the model in memory, in every live state, and a claim with no process ignores
// both. Neither moves the claim, sends a task or fires a held suspension, so neither decision can
// make the hold's stop, and a read for each step of a turn would cost the store a transaction for
// nothing. Any other decision reads nothing.
func (m *Machine) readHoldForStop(ctx context.Context, ev Event) (dropped bool, err error) {
	switch ev.(type) {
	case StreamToolStep, StreamAnswered:
		return false, nil
	}
	request, stopping := ev.(RequestSuspend)
	stopping = stopping && request.By == SuspendedByHold
	held := m.held != nil && m.held.By == SuspendedByHold
	if !stopping && !held {
		return false, nil
	}
	stands, err := m.readWorkHold(ctx)
	if err != nil {
		if held {
			m.log.Warn("supervise: the work hold's record did not answer; the hold's stop stays held, and the stop's outbox row drops it if the hold has ended",
				"tree", m.claim.Tree, "reason", m.held.Reason, "error", err)
		}
		if stopping {
			return false, fmt.Errorf("read the work hold of %s for the stop of %s: %w", m.claim.Tree, m.claim.Token, err)
		}
		return false, nil
	}
	if stands {
		return false, nil
	}
	if held {
		m.dropUnmadeHoldStop()
		dropped = true
	}
	if stopping {
		if m.claim.WorkHeld() {
			if m.owedWrite != nil {
				return dropped, fmt.Errorf("supervise: the work hold of %s has ended under the stop of %s, whose claim write is still owed: %w",
					m.claim.Tree, m.claim.Token, errClaimWriteOwed)
			}
			m.holdEndedUnderStop(request.HoldVersion)
		}
		err = ErrWorkHoldEnded
	}
	return dropped, err
}

// readWorkHold is whether the work hold of the claim's tree stands (Store.WorkHoldStands), read
// under the claim-write deadline (Deps.claimWrite), as a claim write's wait is bounded. Both reads
// of the row go through it: the decision's (readHoldForStop) and the read after the hold's stop
// (holdReadAfterStop).
func (m *Machine) readWorkHold(ctx context.Context) (bool, error) {
	reading, cancel := context.WithTimeout(ctx, m.deps.claimWrite())
	defer cancel()
	return m.deps.Store.WorkHoldStands(reading, m.claim.Tree)
}

// holdReadAfterStop reads the tree's work hold row again once the work hold's stop (request, By
// SuspendedByHold) is written: a read for share in a transaction of its own, under the claim-write
// deadline (readWorkHold). The opening takes that row for update before it reads the tree's
// claims, and deletes it in that same transaction. So this read waits for an opening in flight, or
// it reads the row while no opening holds it:
//
//   - The row stands: nothing more. An opening after this read reads the claims after the stop,
//     which is committed, and starts the claim itself.
//   - The row is gone: the opening has committed, and it may have read the claim before the
//     stop's write and started nothing of it. The daemon is told (holdEndedUnderStop), and it
//     starts each claim the hold stopped that no opening start covers.
//   - The read fails or misses its deadline: a warning, and the error is the decision's. The
//     stop's outbox row asks again: a held stop's row is asked again until the claim answers nil,
//     and the stop asked now answers this error. The row's next attempt meets the open gate and
//     starts the claim (the outbox's holdStopUnmade), or it posts the stop again: a row that still
//     stands changes nothing (suspendedAgain), and a row the opening deleted tells the daemon
//     (readHoldForStop).
//
// A stop whose own claim write failed is not read: the decision answers the write's error, the
// write is owed (repair), and the row's next attempt makes it before it reads the claim (Handle,
// DropHoldStop). A suspension by any other source reads nothing.
func (m *Machine) holdReadAfterStop(ctx context.Context, request RequestSuspend) error {
	if request.By != SuspendedByHold {
		return nil
	}
	stands, err := m.readWorkHold(ctx)
	if err != nil {
		m.log.Warn("supervise: the work hold's record did not answer after the hold's stop was written; the stop's outbox row starts the claim if the hold has ended",
			"tree", m.claim.Tree, "reason", request.Reason, "error", err)
		return fmt.Errorf("read the work hold of %s after the stop of %s: %w", m.claim.Tree, m.claim.Token, err)
	}
	if !stands {
		m.holdEndedUnderStop(request.HoldVersion)
	}
	return nil
}

// holdEndedUnderStop tells the daemon that the work hold of the claim's tree has ended while the
// claim stands stopped by the hold (Deps.WorkHoldEndedUnderStop), with version, the version that
// closed the gate as the hold's stop named it. The daemon applies a fact at once, and that fact
// waits on the lock every fact takes. The decision holds no lock on the row then
// (readHoldForStop).
func (m *Machine) holdEndedUnderStop(version int) {
	m.log.Info("supervise: the work hold has ended and the claim stands stopped by it; the daemon starts the claim",
		"tree", m.claim.Tree, "version", version)
	if m.deps.WorkHoldEndedUnderStop != nil {
		m.deps.WorkHoldEndedUnderStop(m.current(), version)
	}
}

// dropUnmadeHoldStop drops the work hold's held stop, which is not made, and logs it.
func (m *Machine) dropUnmadeHoldStop() {
	m.log.Info("supervise: the work hold ended before its stop was made; the stop is not made",
		"reason", m.held.Reason, "deadline", m.held.Deadline, "launch", m.heldLaunch)
	m.dropHeld()
}

// OpeningTask is the one task the work hold's opening gives a claim it stopped: opening, the
// opening's sentences, a blank line, and work. A start that carries the opening's sentences apart
// (record.SuperviseRequest.Opening) carries a task that begins with OpeningTask(opening, ""), and
// the record refuses any other.
func OpeningTask(opening, work string) string { return opening + "\n\n" + work }

// OpeningWork is task's work: task with the opening's sentences it begins with (OpeningTask(opening,
// "")) cut off its front. A task with no opening, or a task that does not begin with the opening's
// sentences, is its whole text.
func OpeningWork(task, opening string) string {
	if opening == "" {
		return task
	}
	return strings.TrimPrefix(task, OpeningTask(opening, ""))
}

// openingTask is the one task the hold's opening gives a claim it stopped (EndHold): task, the
// opening start's own, unless the claim kept a task of its own that no turn has taken and task
// carries no newer work than it. Then it is the kept task's work behind the opening's sentences:
// its phase, run, entry, kept decisions, check look and review round, under task's id and row, a
// resend of its entry (RequestDeliver.ResendEntry), so the row decides it and the entry the claim
// was given stands. kept says which of the two it is. The daemon's own delivery of a relaunch's
// record is no work, and is never kept: its sentence goes in front of whatever task follows it
// (Delivery.message).
//
// The kept task's work is its text with the sentences of an earlier opening taken off its front
// (Delivery.Work): a claim an earlier opening started and a later close stopped before any turn
// took that opening's task keeps the earlier opening's task. The one task carries the newest
// opening's sentences once, then that work, and it is marked with those sentences
// (RequestDeliver.Opening), so the next opening reads it the same way.
//
// Newer work is a task of a later run than the kept one's, or of a later entry of the same run,
// that is no resend: a transition or an owed entry written for the role while the gate was closed,
// whose start the opening's folded into, so the role is given the newest entry's task. A start of
// no entry, the phase's resume task the boot builds, carries none.
func (m *Machine) openingTask(opening string, task RequestDeliver) (one RequestDeliver, kept bool) {
	p := m.claim.Pending
	if p == nil || !p.ConfirmedAt.IsZero() || p.Relaunch() {
		return task, false
	}
	if task.Entry > 0 && !task.ResendEntry && (task.Generation > p.Generation || (task.Generation == p.Generation && task.Entry > p.Entry)) {
		return task, false
	}
	one = task
	one.Task, one.Opening = p.Work(), ""
	if opening != "" {
		one.Task, one.Opening = OpeningTask(opening, one.Task), opening
	}
	one.Phase, one.Generation, one.Entry, one.ResendEntry = p.Phase, p.Generation, p.Entry, true
	one.Kept, one.CheckLook, one.RoundNamed = p.Kept, p.CheckLook, p.RoundNamed
	return one, true
}
