package supervise

// The copy each turn's end owes (R1 of dispatch://INFRA-1117), as the machine meets it: it starts
// the copy from the turn's end itself, and a stop held for that end, or asked while the copy is
// still being taken, waits for the copy before it stops the process the copy asks, never longer
// than the copy's budget or the stop's own deadline.

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
// it starts none for the controller or for a claim of no issue, and none where it bundles no
// workspace, keeps no store, is stopping or has not attached its workflow yet, and the machine
// then has no copy to wait for.
//
// It returns at once, and nothing of the turn waits for the copy: a turn's end with no stop held
// goes on to idle and to the next task while the copy is built (R2). What the copy is is only
// what a stop held for this end, or asked while the copy is taken, waits on (waitForTurnCopy,
// suspend). A stop already waiting for an earlier turn's copy — a turn that ended while the stop
// waited, one the daemon never saw begin among them — waits for this copy instead, under this
// copy's budget: it holds the later end, and the daemon takes it behind the earlier one
// (internal/daemon's runningCopies). A turn's end that started no copy leaves the copy before it,
// if one is still being taken, as what a stop waits for.
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

// waitForTurnCopy is how a stop waits for the copy a turn's end started: it reports whether the
// stop waits, and arms the wait's bound where it does. Every stop held for a turn's end asks it
// before it runs — the turn's own end (turnEnded), the end of a compaction (compactEnded), and the
// answer of the prompt a pause's hold waited for (stopAfterAnswer, stopAtResult) — and so does a
// suspension that reaches a ready or idle claim while the copy of its last turn's end is still
// being taken and its due has not come (turnCopyInBudget), though nothing held it for that end
// (suspend): the stop takes the process whose shim the copy is asking, and a copy cut off by it
// leaves the turn's end in no copy at all once the role is stopped.
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
// (suspend, holdRequest). A suspension that may not be held (RequestSuspend.NeverHold) waits for no
// copy: its asker read the claim in no turn and is owed the stop at once. The machine never waits
// on its own goroutine: the wait is this mark and a timer, and the copy's end is an event of its
// own. No copy still being taken — the daemon started none, or its end has come back — waits
// nothing, and the stop runs at once. Nor does a copy past its due (turnCopyInBudget): a held
// stop's wait for the copy ends there (turnCopyHolds), so a stop whose wait would begin past it —
// a pause's stop at the prompt's answer, a compaction's end, a turn's end — runs in that same
// decision, as it runs where no copy is taken, rather than on a bound that has already passed.
func (m *Machine) waitForTurnCopy() bool {
	c := m.turnCopy
	if c == nil || !m.turnCopyInBudget() {
		return false
	}
	c.stopWaits = true
	m.arm(TimerTurnCopy, max(0, c.due.Sub(m.deps.Clock.Now())), "")
	m.log.Info("supervise: the held stop waits for the copy of the issue's workspace that the turn's end started",
		"copy", c.seq, "until", c.due, "by", m.held.By, "deadline", m.held.Deadline)
	return true
}

// turnCopyInBudget is the copy's half of the rule turnCopyHolds states: the copy of the issue's
// workspace that the claim's last turn's end started (Machine.turnCopy) is still being taken, and
// the clock is before the copy's due, its budget counted from the turn's end. A suspension that
// reaches a ready or idle claim asks it alone, since it is what decides whether a stop is held
// for the copy at all (suspend).
func (m *Machine) turnCopyInBudget() bool {
	return m.turnCopy != nil && m.deps.Clock.Now().Before(m.turnCopy.due)
}

// turnCopyHolds is whether a stop held over the claim is held, now, for the copy of the issue's
// workspace that its last turn's end started: a stop is held, and that copy is still being taken
// within its budget (turnCopyInBudget). It is the one rule for when a held stop's wait for a
// turn's end copy ends: at the copy's reported end (TurnEndCopied) or at its due, whichever comes
// first. The rule holds whether or not the stop has begun to wait for the copy
// (turnCopy.stopWaits). A pause's stop held for the prompt's answer begins that wait only after
// the answer's own decisions have run (stopAfterAnswer), so a guard that asked whether the wait
// had begun would let those decisions cut the copy. And a wait that has not begun arms no bound,
// so a guard that waited for the copy's report alone would wait past the budget for as long as the
// daemon took to report the copy; a wait that has begun is ended at the due by its own bound
// (TimerTurnCopy, endTurnCopyWait).
//
// It is the precondition of every path that stops or replaces the claim's process while it holds
// a stop (endHeld, suspendHeld, fail, replaceNow): such a path asks it first, and while it holds
// leaves the stop to what runs it, because the stop would take the process whose shim the copy is
// asking and leave the turn's end in no copy once the role is stopped. Once it no longer holds,
// each path does exactly what it does where no copy is being taken. The sites that ask it are a
// prompt failure at the limit (chargePrompt), a provider error that holds the issue
// (providerErrored), a run of refused turns at the limit (turnOutcome), the stall backstop
// (backstopStalledTurn), a stale-plugin relaunch (refreshPlugin), a registration on another plugin
// contract (staleRegistration), a stale address (judge), the agent's ready said again (reready),
// and the turn of a wake parked over the process (wakeNow). The registration alone keeps no
// process: its plugin exits on the refusal, the copy fails and owes a retry, and what the guard
// keeps is that the exit meets the held stop (died, endHeld) rather than a relaunch charged a
// launch failure.
//
// What runs the held stop is apart from these guards. A stop whose wait has begun runs at the
// copy's end or at the due (endTurnCopyWait). One whose wait has not begun begins it once what it
// is held for is over — the end of the turn it was held for (turnEnded), which is where a busy
// answer leaves it, or, for a pause's stop, the prompt's answer (stopAfterAnswer, which states
// that rule) — and runs then the same way; past the due it waits for nothing and runs in that
// decision (waitForTurnCopy). Where code needs whether the wait has begun, it reads the mark
// itself: a new suspension, which begins the wait where no held stop has begun it (suspend); the
// held stop's deadline, whose line says which bound ran out (suspendAtTimeout); and the copy's own
// start and end, which carry the mark to a later copy and run the waiting stop (copyTurnEnd,
// endTurnCopyWait).
//
// The turn-copy tests of this package (turncopy_*_test.go, pause_noturn_result_test.go) hold each
// site in a daemon's stop that waits for the copy and in a pause's stop whose wait has not begun,
// and each site past the due as well: the seven guards of turncopy_past_due_test.go, the stall
// backstop and the parked wake, besides a new suspension and a pause's stop at the prompt's answer
// past the due. k9's wait control (dispatch://INFRA-1117) holds the wait.
func (m *Machine) turnCopyHolds() bool {
	return m.held != nil && m.turnCopyInBudget()
}

// heldStopWaitsAtEnd is the wait of a stop held for the end of a turn or of a compaction for the
// copy a turn's end started (waitForTurnCopy), made where that end gives the claim back in the
// state back names (turnEnded, compactEnded): it reports whether the stop waits, and where it does
// the claim is given back in that state, holding the stop, and the state is written and reported
// to the daemon as a durable transition (durableWrite), as the end's own idle is. The error is
// that write's.
func (m *Machine) heldStopWaitsAtEnd(ctx context.Context, back ClaimState) (bool, error) {
	if !m.waitForTurnCopy() {
		return false, nil
	}
	m.claim.State = back
	return true, m.durableWrite(ctx, back, m.persist)
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
	write, line := m.log.Info, "supervise: the copy of the turn's end is over; the held stop runs now"
	if bound {
		write, line = m.log.Warn, "supervise: the copy of the turn's end did not end within its budget; the held stop runs without it"
	}
	write(line, "copy", c.seq, "due", c.due, "by", m.held.By)
	return m.suspendHeld(ctx)
}
