package supervise

// The work hold (`gates.hold: work`) as one claim's machine keeps it. Its stop is a suspension like
// any other (RequestSuspend, By SuspendedByHold): held for the agent's turn and cut at a deadline
// the outbox stores (holdSuspension, suspendAtTimeout), and for a ready or idle claim held for the
// turn its close message starts (RequestSuspend.TurnAfter). What is the hold's own is here: the
// one check that holds every request that could start a turn of a claim the hold stopped
// (workHoldWaits, read by the fence through each event's door), and the hold's end. Each decision
// that may make the hold's stop takes the tree's work hold row for share and holds it to its end
// (lockHoldStop). The opening takes that row for update before it reads the tree's claims, so a
// stop made under the lock is committed before the opening reads the claims, or it is not made:
// the lock that finds the row gone drops a stop not yet made, and answers the hold's stop asked
// now ErrWorkHoldEnded. The opening's start drops such a stop too, and starts a claim the hold
// stopped with one task (EndHold). The stop's own outbox row drops it where the row meets a tree
// that no longer holds work (DropHoldStop).

import (
	"context"
	"fmt"
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
// (Machine.held), so nothing is sent to it (sendPending), and while the hold stands, under the
// lock on the tree's work hold row (lockHoldStop), its row runs the stop where the stop is due.
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
//     of task is used. A decision of the claim whose lock on the tree's work hold row finds the
//     row gone drops such a stop too (lockHoldStop), so EndHold finds one only where no decision
//     came between the opening and the start.
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
func (m *Machine) EndHold(ctx context.Context, opening string, task *RequestDeliver) (bool, error) {
	m.mu.Lock()
	defer m.unlock()
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
// stop row's end of the drop a decision makes itself when its lock on the tree's work hold row
// finds the row gone (lockHoldStop). A stop the hold asked for and has not made, which no
// decision has dropped yet, is dropped, as EndHold drops it, and dropped is true. Nothing is
// started: a claim the hold has already stopped is left stopped, and stopped is true, so the
// caller has the hold's opening start it with the opening's sentences. Such a stop was made
// without the lock on the tree's work hold row, so the opening read the claims without waiting
// for it: a stop held over the claim that a decision made after its lock was not granted
// (lockHoldStop), or after its report of a durable state or its launch check ended the lock
// (report, checkLaunch), or a stop whose lock the database ended while its decision ran. Any
// other claim is left as it is, and both are false.
func (m *Machine) DropHoldStop(ctx context.Context) (dropped, stopped bool, err error) {
	m.mu.Lock()
	defer m.unlock()
	if dropped, err := m.dropHoldStop(ctx); dropped || err != nil {
		return dropped, false, err
	}
	return false, m.claim.WorkHeld(), nil
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

// lockHoldStop takes the tree's work hold row for share (Store.LockWorkHold) for a decision that
// may make the work hold's stop: one of a claim the hold's stop is held over (Machine.held, By
// SuspendedByHold), or one of the hold's stop asked now (RequestSuspend, By SuspendedByHold).
// Handle calls it after the claim's lock, before the fence, and ends the lock once the decision's
// act, its writes and its send have run, unless a durable state's report or the launch check ended
// it first. The opening takes the same row for update before it reads the tree's claims, and
// deletes the row in that same transaction (workflow's tellAndEndWorkHold). So either this
// decision commits its stop first, and the opening reads the claim stopped and starts it, or the
// opening commits first, and the lock reads no row:
//
//   - A stop held over the claim is not made: it is dropped (dropUnmadeHoldStop), and dropped is
//     true, so the decision sends a task the stop held back once its row has run. A turn's end
//     leaves the claim idle, a compaction's end gives the claim back, the hold's own deadline cuts
//     nothing, and a launch's ready goes ahead.
//   - The hold's stop asked now is not made: err is ErrWorkHoldEnded. Where the stop's row posts
//     that stop again while it is held over the claim, the held stop is dropped as above, and the
//     task it held back is sent.
//
// The wait for the lock ends at the claim-write deadline (Deps.claimWrite), as a claim write's
// wait does. The machine keeps the lock's release (Machine.holdLock), and the lock lasts until
// the first of three: a durable state's report (report), the launch check (checkLaunch), or the
// end of the decision (Handle, releaseHoldLock). The hold's stop asked now is not made once the
// lock has ended (holdStopUnlocked). A lock that is not granted — the store failed, or the
// deadline passed — keeps a stop held over the claim held, with a warning, and the decision goes
// on without the lock: the stop's outbox row asks again on its backoff, and its drop starts a
// claim this decision stopped without the lock (DropHoldStop, the outbox's holdStopUnmade). The
// hold's stop asked now answers that error instead, so its outbox row asks again rather than make
// a stop under no lock.
//
// A tool step and a model's answer take no lock, whatever the claim holds. Each kind's row in
// every state records what the agent did and nothing else: toolStepped writes the claim's steps
// and answered keeps the model in memory, in every live state, and a claim with no process ignores
// both. Neither moves the claim, sends a task or fires a held suspension, so neither decision can
// make the hold's stop. A tool step's write of the claim's steps has no claim-write deadline
// (putStep), and under the lock it would hold a second connection of the pool for as long as the
// database kept it waiting. Any other decision takes no lock and reads nothing.
func (m *Machine) lockHoldStop(ctx context.Context, ev Event) (dropped bool, err error) {
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
	locking, cancel := context.WithTimeout(ctx, m.deps.claimWrite())
	stands, release, err := m.deps.Store.LockWorkHold(locking, m.claim.Tree)
	cancel()
	if err != nil {
		if held {
			m.log.Warn("supervise: the work hold's record did not answer; the hold's stop stays held, and the stop's outbox row drops it if the hold has ended",
				"tree", m.claim.Tree, "reason", m.held.Reason, "error", err)
		}
		if stopping {
			return false, fmt.Errorf("lock the work hold of %s for the stop of %s: %w", m.claim.Tree, m.claim.Token, err)
		}
		return false, nil
	}
	m.holdLock = release
	if stands {
		return false, nil
	}
	if held {
		m.dropUnmadeHoldStop()
		dropped = true
	}
	if stopping {
		err = ErrWorkHoldEnded
	}
	return dropped, err
}

// holdStopUnlocked is ErrWorkHoldLockEnded where request is the work hold's stop asked now and the
// lock its decision took on the tree's work hold row (lockHoldStop) has ended already, and nil
// otherwise. The lock ends early only at a durable state's report or the launch check (report,
// checkLaunch). No row of RequestSuspend checks a launch or reports a state of its own before its
// stop, so the lock of such a decision ends early only where an earlier write of it landed a write
// a failed decision owed, and the reports owed with it ended the lock (settleOwedWrite). Each row
// of RequestSuspend asks this at the point where it would make the stop at once: before the first
// of the process's stop, the take-back of a dead turn, the takeover of a released claim and the
// write, so a stop it refuses changes nothing of the claim. A stop held for a turn, a compaction or
// a ready is no stop made now, and is not asked. Nor is a stop another source holds in the hold's
// place (suspendRequest), since that stop is not the hold's.
func (m *Machine) holdStopUnlocked(request RequestSuspend) error {
	if request.By != SuspendedByHold || m.holdLock != nil {
		return nil
	}
	m.log.Info("supervise: the lock on the work hold's row ended before the hold's stop was made; the stop waits for its row's next attempt",
		"reason", request.Reason)
	return ErrWorkHoldLockEnded
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

// openingTask is the one task the hold's opening gives a claim it stopped (EndHold): task, the
// opening start's own, unless the claim kept a task of its own that no turn has taken and task
// carries no newer work than it. Then it is the kept task's work behind the opening's sentences:
// its phase, run, entry, kept decisions, check look and review round, under task's id and row, a
// resend of its entry (RequestDeliver.ResendEntry), so the row decides it and the entry the claim
// was given stands. kept says which of the two it is. The daemon's own delivery of a relaunch's
// record is no work, and is never kept: its sentence goes in front of whatever task follows it
// (Delivery.message).
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
	one.Task = p.Task
	if opening != "" {
		one.Task = OpeningTask(opening, p.Task)
	}
	one.Phase, one.Generation, one.Entry, one.ResendEntry = p.Phase, p.Generation, p.Entry, true
	one.Kept, one.CheckLook, one.RoundNamed = p.Kept, p.CheckLook, p.RoundNamed
	return one, true
}
