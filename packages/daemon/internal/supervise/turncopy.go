package supervise

// The copy each turn's end owes (R1 of dispatch://INFRA-1117), as the machine meets it: it starts
// the copy from the turn's end itself, and a stop held for that end waits for the copy before it
// stops the process the copy asks, never longer than the copy's budget or the stop's own deadline.

import (
	"context"
	"time"
)

// turnCopy is the copy of the issue's workspace that the end of the claim's last turn started
// (Deps.CopyTurnEnd), while the machine waits to hear it end (Machine.turnCopy).
type turnCopy struct {
	// seq is the copy's number, which its end carries back (TurnEndCopied.Copy).
	seq uint64
	// due is when the copy's own budget runs out, counted from the turn's end that started it.
	due time.Time
	// stopWaits is whether a held stop waits for the copy (waitForTurnCopy). TimerTurnCopy is
	// armed at due while it does.
	stopWaits bool
}

// copyTurnEnd starts the copy the end of the claim's turn owes, at every turn's end the machine
// records (turnEnded, blindTurnEnded), before anything that end goes on to decide can stop the
// process. The daemon is handed the claim as it stands at the end, so its copy asks the shim of
// the role whose turn ended, in the process recorded for it, and needs no read of the claims to
// find a live role after the fact (Deps.CopyTurnEnd). The daemon decides which claims it copies:
// it starts none for the controller, for a claim of no issue, or where it bundles no workspace,
// and the machine then has no copy to wait for.
//
// It returns at once, and nothing of the turn waits for the copy: a turn's end with no stop held
// goes on to idle and to the next task while the copy is built (R2). What the copy is is only
// what a stop held for this end waits on (waitForTurnCopy). A stop already waiting for an earlier
// turn's copy — a turn that ended while the stop waited, one the daemon never saw begin among
// them — waits for this copy instead, under this copy's budget: it holds the later end, and the
// daemon takes it behind the earlier one (internal/daemon's runningCopies). A turn's end that
// started no copy leaves the copy before it, if one is still being taken, as what a stop waits for.
func (m *Machine) copyTurnEnd() {
	if m.deps.CopyTurnEnd == nil {
		return
	}
	m.turnCopies++
	budget, started := m.deps.CopyTurnEnd(m.current(), TurnEndCopied{Claim: m.claim.Token, Copy: m.turnCopies})
	if !started {
		return
	}
	waits := m.turnCopy != nil && m.turnCopy.stopWaits
	m.turnCopy = &turnCopy{seq: m.turnCopies, due: m.deps.Clock.Now().Add(budget)}
	if waits {
		m.turnCopy.stopWaits = true
		m.arm(TimerTurnCopy, budget, "")
	}
}

// waitForTurnCopy is how a stop held for a turn's end waits for the copy that end started: it
// reports whether the stop waits, and arms the wait's bound where it does. Every such stop asks
// it before it runs — the turn's own end (turnEnded), the end of a compaction (compactEnded), and
// the prompt's result a pause's hold waited for (stopAtResult) — because the stop takes the
// process whose shim the copy is asking, and a copy cut off by it leaves the turn's end in no
// copy at all once the role is stopped.
//
// The wait is bounded by the smaller of the copy's own budget, counted from the turn's end
// (TimerTurnCopy at turnCopy.due), and what is left of the stop's own deadline: the operator's
// stop timeout, or a deadline the daemon named (RequestSuspend.Deadline), which stays armed on
// TimerSuspend and stops the claim there (suspendAtTimeout). A pause's stop names none. So the
// wait never makes a stop late. Whichever comes first of the copy's end (TurnEndCopied) and those
// two bounds runs the stop, once: the stop lets the process go, and the wait with it (letGo). A
// copy that fails, is refused or runs out of its budget is logged by the daemon as any copy is,
// and its end runs the stop all the same.
//
// The claim waits in the state the end gives it back in, ready or idle, holding the stop: it is
// handed nothing (sendPending), and a suspension that reaches it meanwhile is held with this one
// (suspend, holdRequest). The machine never waits on its own goroutine: the wait is this mark and
// a timer, and the copy's end is an event of its own. No copy still being taken — the daemon
// started none, or its end has come back — waits nothing, and the stop runs at once.
func (m *Machine) waitForTurnCopy() bool {
	c := m.turnCopy
	if c == nil {
		return false
	}
	c.stopWaits = true
	m.arm(TimerTurnCopy, max(0, c.due.Sub(m.deps.Clock.Now())), "")
	m.log.Info("supervise: the held stop waits for the copy of the issue's workspace that the turn's end started",
		"copy", c.seq, "until", c.due, "by", m.held.By, "deadline", m.held.Deadline)
	return true
}

// stopWaitsForTurnCopy is whether the claim's held stop is waiting for the copy its turn's end
// started (waitForTurnCopy).
func (m *Machine) stopWaitsForTurnCopy() bool {
	return m.held != nil && m.turnCopy != nil && m.turnCopy.stopWaits
}

// turnCopyEnded is the end of the copy the claim's last turn's end started (TurnEndCopied),
// whatever the copy came to: the held stop that waited for it runs now (endTurnCopyWait).
func turnCopyEnded(m *Machine, ctx context.Context, _ Event) error {
	return m.endTurnCopyWait(ctx, false)
}

// turnCopyBound is the copy's budget running out before its end came back (TimerTurnCopy): the
// held stop that waited for it runs without it (endTurnCopyWait), and the copy's end, when it
// comes, decides nothing (fence).
func turnCopyBound(m *Machine, ctx context.Context, _ Event) error {
	return m.endTurnCopyWait(ctx, true)
}

// endTurnCopyWait ends the machine's wait for the copy its last turn's end started, at the copy's
// end or at its bound, and runs the held stop that waited for it. Nothing else of the claim moves.
// A copy no stop waits for ends with nothing run. Neither does one whose claim is in a turn again
// — the agent started one of its own while the stop waited — since stopping the process now would
// cut that turn off: the held stop runs at that turn's end, after the copy that end starts, as a
// stop held for a turn always does (holdSuspension). A stop the runtime refuses stays held and is
// tried again at the probe interval (suspendHeld).
func (m *Machine) endTurnCopyWait(ctx context.Context, bound bool) error {
	c := m.turnCopy
	m.turnCopy = nil
	m.disarm(TimerTurnCopy)
	if c == nil || !c.stopWaits || m.held == nil {
		return nil
	}
	if state := m.claim.State; state != StateReady && state != StateIdle {
		m.log.Info("supervise: the copy of the turn's end is over and the claim is in a turn again; the held stop waits for that turn's end", "copy", c.seq)
		return nil
	}
	if bound {
		m.log.Warn("supervise: the copy of the turn's end did not end within its budget; the held stop runs without it", "copy", c.seq, "due", c.due, "by", m.held.By)
	} else {
		m.log.Info("supervise: the copy of the turn's end is over; the held stop runs now", "copy", c.seq, "by", m.held.By)
	}
	return m.suspendHeld(ctx)
}
