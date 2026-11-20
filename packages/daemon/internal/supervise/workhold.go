package supervise

// The work hold (`gates.hold: work`, dispatch://INFRA-4528's spec, steps 3 to 5 under `work`, and
// its plan's decisions 14 to 16) as one claim's machine keeps it. Its stop is a suspension like
// any other (RequestSuspend, By SuspendedByHold): held for the agent's turn and cut at a deadline
// the outbox stores (holdSuspension, suspendAtTimeout), and for a ready or idle claim held for the
// turn its close message starts (RequestSuspend.TurnAfter). What is the hold's own is here: the
// one check that holds every request that could start a turn of a claim the hold stopped
// (workHoldWaits, read by the fence through each event's door), and the hold's end, which drops a
// stop not yet made and starts a claim the hold stopped with one task (EndHold).

import (
	"context"
	"fmt"
)

// workHoldWaits is the work hold's answer to ev, and nil where it has none: a claim the hold
// suspended answers every request that could start or resume a turn of its agent a wait
// (ErrHeldByWorkHold) — a start's resume by rule, a task, a wake, a CI settlement's or a hook's
// task, the boot's relaunch, a pause's resume and the resume of a launch it owed, the controller
// keeper's resume or retry, a compaction, a ready — except the operator's own resume and retry,
// which start it (turnDoor; I2 of dispatch://INFRA-4528's plan). Each request answers it before its
// row is reached, so no row of the table has to know the hold, and a request added later is held
// by the door it declares. The claim is left exactly as it was: the task it kept stays kept, and
// the turn-starting request is asked again by whoever holds it (the outbox's backoff) or is a
// request the hold's end makes moot, since that end starts the claim itself (EndHold).
//
// A claim the hold's stop is only held over — in a turn, a compaction, idle waiting for the turn
// its message starts, or a launch held until its ready — is not answered here: its stop is held
// (Machine.held), so nothing is sent to it (sendPending), and its row runs the stop where the
// stop is due. A start of such a claim waits all the same (StartedBy).
func (m *Machine) workHoldWaits(ev Event) error {
	if ev.door() != ruleDoor || m.claim.State != StateSuspended || m.claim.SuspendedBy != SuspendedByHold {
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
//     or an idle claim's next turn — is not made (spec step 3): it is dropped, and a task it held
//     back is sent where it may go now. EndHold reports false: the claim runs on, and nothing of
//     task is used.
//   - A claim the hold stopped is started again, paced as any start by rule is, with one task
//     (decision 18 and point 4 of the orchestrator's hold decisions): task, the opening start's,
//     whose text starts with the opening's sentences (opening). Where the claim kept a task of its
//     own that no turn has taken — a released worker's pending task, a task the hold's stop held
//     back, a task its cut turn was working — and task carries no newer work than it, the one task
//     is that kept task's work behind the opening's sentences instead (openingTask), and the kept
//     delivery is retired in its place. The one task is queued under task's id and row, so the
//     start's own delivery of task, made once this returns, queues nothing more (given). EndHold
//     reports true. With task nil the claim is started with whatever it kept.
//   - Any other claim — one an operator's suspension or the daemon's own took over, one the hold
//     never reached — is left as it is, and EndHold reports false.
//
// A start the lifecycle refuses (Store.AdmitClaim, a reserved cleanup) leaves the claim suspended
// by the hold with the one task queued, and the outbox asks again: the task is not composed a
// second time, since its row is already the claim's (given). Nor is a task queued that the claim
// was given already, and the task it kept is then kept as it is.
func (m *Machine) EndHold(ctx context.Context, opening string, task *RequestDeliver) (bool, error) {
	m.mu.Lock()
	defer m.unlock()
	m.dropEndedPauseHold()
	if m.held != nil && m.held.By == SuspendedByHold {
		m.log.Info("supervise: the work hold ended before its stop was made; the stop is not made",
			"reason", m.held.Reason, "deadline", m.held.Deadline, "launch", m.heldLaunch)
		m.dropHeld()
		return false, m.sendPending(ctx)
	}
	if m.claim.State != StateSuspended || m.claim.SuspendedBy != SuspendedByHold {
		return false, nil
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
// whose start the opening's folded into (decision 18: the newest entry's task). A start of no
// entry, the phase's resume task the boot builds, carries none.
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
		one.Task = opening + "\n\n" + p.Task
	}
	one.Phase, one.Generation, one.Entry, one.ResendEntry = p.Phase, p.Generation, p.Entry, true
	one.Kept, one.CheckLook, one.RoundNamed = p.Kept, p.CheckLook, p.RoundNamed
	return one, true
}
