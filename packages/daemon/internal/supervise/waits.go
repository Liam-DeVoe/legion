package supervise

import (
	"context"
	"fmt"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// R5's two waits (dispatch://INFRA-2811). The daemon charges a worker only in a state it sees
// itself, and judges two waits, each with a bound of its own.
//
// The first wait is for a take of a value the worker reported seeing: the claim is ready or idle,
// no hold stands, its latest written revision is the one the worker reported seen, and no take
// came at it. When Timeouts.Take passes the worker is charged (chargePrompt, why naming WaitTake).
// A latest revision nobody reported seeing charges nobody, since what failed is the relay and
// not the worker: the value is written again under the same number once per first bound for as
// long as the claim's agent is up, and the claim is listed as unseen (ClaimView.UnseenTask). Both
// ride the one timer (TimerTake), since both are the first bound over the latest value.
//
// The second wait is for a turn to start after an accepted take. When Timeouts.TurnStart passes
// the worker is charged (why naming WaitTurnStart) and the task taken back under its number; the
// value is written again once it can be taken again, as a retake (offerPending).
//
// A turn's start or end resets the count (resetPromptBudgets); a take does not, so a worker that
// takes after every charge and never starts a turn is still retired at the third. A hold that
// stands charges nobody: a hold that arises while a wait is armed disarms it, and its end arms
// it again from there (armWaits). The timers are memory only, as every timer is: a machine a
// daemon start built arms them again from the record at the worker's first act after that start
// (Machine.awaitsAct), so the time the daemon was not serving counts toward neither bound. And a
// seen report such a machine read from the store, which no worker made to it, charges nobody at
// the first bound: the value is written again instead (Machine.seenFromStore, takeBoundOver).
//
// The first wait over a value the worker reported seeing and the second wait judge the worker by
// the frames its connection carries, so each runs only while that connection shows the machine
// every frame of the worker's turns (connview.go).
// A close disarms both, and neither fires while no connection stands.
// After every reconnect hello of a claim holding a pending task, whatever the hello says of the
// shim's backlog, each is armed again whole only at the agent's answer to whether it is in a turn
// or at a turn start or end frame on the new connection, whichever comes first.
// An answer that the agent is in a turn is the turn starting, the take's own over a take awaiting
// its turn, and no bound runs over a turn.
// A first wait over a value nobody reported seeing is the relay's, charges nobody, and runs on.

// The waits a charge names (chargePrompt): the wait attribute of its line, and the word the why of
// each of R5's two begins with.
const (
	// WaitTake is R5's first wait: a value the worker reported seeing that it asked no take of.
	WaitTake = "take"
	// WaitTurnStart is R5's second wait: an accepted take whose turn did not start.
	WaitTurnStart = "turn-start"
	// WaitAdoption is no wait of R5's: a take whose adoption of the working copy the shim refused,
	// charged at its answer (refusedAdoption).
	WaitAdoption = "adoption"
)

// firstWait is what the first bound waits on: the latest revision the daemon wrote of the claim's
// value, and whether nobody reported seeing it (unseen) or the worker saw it and asked no take.
type firstWait struct {
	unseen   bool
	revision TaskRevision
}

// UnseenTask is a claim's latest value that went a whole first bound with nobody reporting it
// seen, as the claim's published view carries it (ClaimView.UnseenTask): the number it names,
// and when the daemon last wrote it.
type UnseenTask struct {
	Task      uint64
	WrittenAt time.Time
}

// UnseenTaskValue is one claim whose value is unseen, as the state route lists it
// (api.State.UnseenTaskValues): the claim, its issue and role, and its UnseenTask.
type UnseenTaskValue struct {
	Claim     claim.Token
	Issue     string
	Role      claim.Role
	Task      uint64
	WrittenAt time.Time
}

// actOf is whether ev is an act of the claim's worker: what a daemon start's waits wait for
// before they are armed (Machine.awaitsAct), and the end-frame bound such a start left to the
// worker (Machine.finishAtAct). The shim's hello is one, the reconnect hello included, so a worker
// that does nothing else after a daemon start is still charged one bound after its reconnect; so
// are its registration, its ready, a seen report, a take, a turn frame and a turn's report.
func actOf(ev Event) bool {
	switch ev.(type) {
	case StreamHello, StreamTurnStart, StreamTurnEnd, RequestRegister, RequestReady, RequestTaskSeen, RequestTake,
		RequestTurnOutcome:
		return true
	}
	return false
}

// resetPromptBudgets puts the claim's prompt budgets back to zero, the reset a turn's start or end
// makes (R5), and reports whether it changed them, for a caller whose own write would not carry
// them otherwise.
func (m *Machine) resetPromptBudgets() bool {
	b := &m.claim.Budgets
	if b.PromptFailures == 0 && b.PromptRetires == 0 {
		return false
	}
	b.PromptFailures, b.PromptRetires = 0, 0
	return true
}

// armWaits arms each of the two waits the claim is in as this decision left it, and disarms each
// it is not in: it runs at the end of every decision (unlock), so a wait is armed from the
// decision that began it — the seen report, the turn's end that left the worker idle, the hold's
// end, the accepted take, the take's put-back (notTheTakesTurn), the worker's first act after a
// daemon start — and a decision that leaves the claim waiting on the same thing leaves the bound
// where it was. A bound is always armed whole (Timeouts.Take, Timeouts.TurnStart), so the moment
// its clock started is the moment it was armed, which the armed timer records (Machine.timers,
// its due less the bound). The claim is listed as unseen only while its first wait is still on a
// value nobody reported seeing. The bound of the wait for a take's report is kept the same way
// (armReportWait, reportwait.go).
func (m *Machine) armWaits() {
	first, waiting := m.firstWaitNow()
	if !waiting || !first.unseen {
		m.unseenListed = false
	}
	if _, armed := m.timers[TimerTake]; !waiting {
		m.disarm(TimerTake)
		m.firstFor = firstWait{}
	} else if !armed || m.firstFor != first {
		m.firstFor = first
		m.arm(TimerTake, m.deps.Timeouts.Take, m.claim.Pending.ID)
	}
	takenAt, waiting := m.secondWaitNow()
	if _, armed := m.timers[TimerTurnStart]; !waiting {
		m.disarm(TimerTurnStart)
		m.secondFor = time.Time{}
	} else if !armed || !m.secondFor.Equal(takenAt) {
		m.secondFor = takenAt
		m.arm(TimerTurnStart, m.deps.Timeouts.TurnStart, m.claim.Pending.ID)
	}
	m.armReportWait()
}

// firstWaitNow is the first wait the claim is in now, and false for none. Its agent is up — ready,
// idle or working — and it holds a task no take holds, whose value the daemon wrote. A latest
// revision nobody reported seeing is waited on in any of the three states, under a hold or not:
// what is waited for is the relay, which neither the claim's turn nor a hold stops, nor a
// connection cut. A value the worker saw is waited on only while it could take it and the daemon
// sees its frames: ready or idle, no take at that revision, no hold the claim's own state or
// another role's turn puts on the task (claimHold, issueInTurn), and the claim's connection
// showing every frame of its turns (connview.go). The holds that ask the record — a root that has
// no architect to hear its task — are asked when the bound passes (takeBoundOver), as a take asks
// them.
func (m *Machine) firstWaitNow() (firstWait, bool) {
	c := &m.claim
	p := c.Pending
	if m.deps.Timeouts.Take <= 0 || m.awaitsAct || p == nil || p.taken() || c.TaskWrittenRevision.Revision == 0 {
		return firstWait{}, false
	}
	if !SaidReady(c.State) {
		return firstWait{}, false
	}
	written := firstWait{revision: c.TaskWrittenRevision}
	if c.TaskWrittenRevision.after(c.TaskSeenRevision) {
		written.unseen = true
		return written, true
	}
	if c.TaskTakeState != "" || m.connSeen != viewWhole || m.claimHold() != "" {
		return firstWait{}, false
	}
	if _, inTurn := m.adoptionWaits(); inTurn {
		return firstWait{}, false
	}
	return written, true
}

// secondWaitNow is the accepted take the claim's second wait is on, and false for none: a take of
// the pending task accepted and its turn not started (Delivery.awaitsTurn), whose report's finish
// the store did not refuse (takePhase: a take awaiting its turn whose finish is owed is
// waitFinish, which the report that came decides), on a ready or idle claim no hold of its own
// state stands over (claimHold), whose connection shows every frame of its turns (connview.go).
// The time it answers, the take's acceptance (ConfirmedAt), names which take the wait is on and
// nothing more: it is not the clock's start. The wait is disarmed at the end of every decision it
// does not stand through — the take's turn started, a hold stands over it, the claim's own turn's
// in-turn hold among them, or the connection cut — and armed whole again at the end of the
// decision that begins it once more (armWaits). So the second wait's clock starts at the latest of
// the take's acceptance, its put-back (notTheTakesTurn: a report naming no task gives the take
// back the turn the daemon gave a run that was not its own), the end of the last hold that stood
// over it (for the in-turn hold, the end of that turn), the worker's first act after a daemon
// start (awaitsAct), and the point the connection's view is whole again after a cut
// (connview.go); no time inside a hold, inside a run the daemon gave the take, or while the daemon
// could not see the worker's frames is charged (dispatch://INFRA-2811, R5 and decision 8's
// put-back and connection rule). The moment is recorded where the bound is armed, as the first
// wait's restart is (Machine.timers, TimerTurnStart).
//
// One hold does not stop it: work that waits behind a take a report put back, a held suspension
// or a held stale-plugin relaunch (reportwait.go, waitTake). That work is no hold over the take
// but waits on it, for its own turn or for this bound, whichever is first (decision 8), so the
// bound is armed from the put-back as for any take awaiting its turn, and it runs the work once it
// has taken the take back (turnStartBoundOver). Left unarmed, as for a hold, the daemon's own
// suspension, which names no deadline, would wait for a turn the worker may never start. The
// project's pause does not stop it either while such work waits: the pause holds the take, and
// its own stop, held behind the take, is such work, so the hold read under the pause is the
// claim's own (stateHold). Read as the pause, it would leave that stop waiting for a turn the
// worker may never start, and the pause would never stop the claim.
func (m *Machine) secondWaitNow() (time.Time, bool) {
	p := m.claim.Pending
	if m.deps.Timeouts.TurnStart <= 0 || m.awaitsAct || m.connSeen != viewWhole || p == nil || !p.awaitsTurn() ||
		m.takePhase() == waitFinish {
		return time.Time{}, false
	}
	if m.workHeld() && m.reportWaitNow() == waitTake {
		// The hold is the work waiting behind the take (reportwait.go), which this bound bounds.
		switch m.stateHold() {
		case "", HoldSuspension, HoldStalePlugin:
			return p.ConfirmedAt, true
		}
		return time.Time{}, false
	}
	if m.claimHold() != "" {
		return time.Time{}, false
	}
	return p.ConfirmedAt, true
}

// takeBoundOver is the first bound passing (TimerTake). A value nobody reported seeing is written
// again under its number, which gives the relay a new revision to carry, and the claim is listed
// as unseen; nobody is charged. A value the worker saw and asked no take of charges the worker,
// unless what a take would meet now refuses or drops it (takeable): a hold that stands charges
// nobody, and is waited out (armWaits arms the bound again from this decision), and a task the
// record no longer owes is dropped here, as a take would drop it.
//
// A seen report this daemon read from the store, and never received itself, charges nobody
// either (decision 20 of dispatch://INFRA-2811): a take of that revision may have failed on its
// first store write under the daemon that stopped, and that process's worker asks once per
// revision, so it will not ask at it again. The value is written again under its number instead,
// as for a value nobody reported seeing, and the mark goes with the write (seenFromStore,
// putValue): a worker that then reports the new revision seen and leaves it untaken is charged one
// bound after that report, and one that reports nothing holds a value nobody reported seeing.
func takeBoundOver(m *Machine, ctx context.Context, _ Event) error {
	armedFor := m.firstFor
	m.firstFor = firstWait{}
	now, waiting := m.firstWaitNow()
	if !waiting || now != armedFor {
		return nil
	}
	p := m.claim.Pending
	if now.unseen {
		m.unseenListed = true
		m.log.Info("supervise: nobody reported seeing the claim's latest value within the first bound; it is written again under its number, and nobody is charged",
			"task", p.Number, "epoch", now.revision.Epoch, "revision", now.revision.Revision, "bound", m.deps.Timeouts.Take)
		return m.writeValue(ctx, TaskValueUnseen)
	}
	hold, dropped, err := m.takeable(ctx, false)
	switch {
	case err != nil || dropped:
		return err
	case hold != "":
		m.log.Info("supervise: the first bound passed while a hold stands over the task; the worker is not charged",
			"task", p.Number, "hold", hold)
		return nil
	case m.seenFromStore:
		m.log.Info("supervise: the first bound passed over a revision whose seen report this daemon read from the store; it is written again under its number, and nobody is charged",
			"task", p.Number, "epoch", now.revision.Epoch, "revision", now.revision.Revision, "bound", m.deps.Timeouts.Take)
		return m.writeValue(ctx, TaskValueUnseen)
	}
	return m.chargePrompt(ctx, WaitTake, fmt.Sprintf("%s: the worker reported seeing task %d at revision %d and asked no take of it within %s",
		WaitTake, p.Number, now.revision.Revision, m.deps.Timeouts.Take))
}

// turnStartBoundOver is the second bound passing (TimerTurnStart): the take of the pending task
// was accepted and no turn started. The worker is charged, and the task is taken back under its
// number, so the next take of it is decided afresh; where the charge left the process in place,
// the value is written again (offerPending, which logs it as the retake it is), since the worker
// asked at the revision it holds. A charge that retired the process leaves the value to the
// relaunched worker, whose registration reads it. Work that waited behind the take after a report
// put it back — a held suspension, a held stale-plugin relaunch — runs once the take is taken back
// (settleThenAfterTurn, reportwait.go): the bound is that wait's end, as R5 says the second wait
// ends.
func turnStartBoundOver(m *Machine, ctx context.Context, _ Event) error {
	armedFor := m.secondFor
	m.secondFor = time.Time{}
	takenAt, waiting := m.secondWaitNow()
	if !waiting || !takenAt.Equal(armedFor) {
		return nil
	}
	number := m.claim.Pending.Number
	// A charge that retired the process, or whose held stop landed, leaves a claim that is not
	// ready or idle, so the value is written again only where the process stays; and work that
	// waited behind the take runs first, a held suspension instead of the offer.
	return m.settleThenAfterTurn(ctx, func() error {
		m.log.Warn("supervise: no turn started within the second bound after the accepted take; the worker is charged and the task taken back",
			"task", number, "takenAt", takenAt, "bound", m.deps.Timeouts.TurnStart)
		if err := m.chargePrompt(ctx, WaitTurnStart, fmt.Sprintf("%s: the take of task %d was accepted and no turn started within %s",
			WaitTurnStart, number, m.deps.Timeouts.TurnStart)); err != nil {
			return err
		}
		if p := m.claim.Pending; p == nil || p.Number != number || !p.awaitsTurn() {
			return nil
		}
		return m.takeBack(ctx, WaitTurnStart)
	})
}

// reconnectDeadline is the reconnect deadline (connview.go): of a claim a daemon start restored
// with its agent ready, idle or working (NewMachine), or whose connection closed while it held a
// pending task (closeView), or whose agent said it is ready over a connection whose view was not
// whole (readyView), its shim not having said hello since; or of a reconnect hello of a claim
// holding a pending task whose agent has not answered whether it is in a turn, nor sent a turn
// frame, since (askView).
//
// With no hello, it probes and retires as the registration deadline does (registrationDeadline,
// retireNeverReady). A process found gone takes the death path (judge, died). A process found
// alive is retired and relaunched with a charged launch failure, CauseNeverReconnected, since a
// live process whose shim never comes back is held by nothing else: no frame of it reaches the
// daemon, so none of R5's waits can be judged of it. The task its turn was running is taken back
// first, as a replacement takes it (interrupted), so the relaunched worker takes it again under
// its number; a take awaiting its turn goes back as a cut turn (Machine.turnUnseen), since that
// turn may have run while no frame of the claim reached the machine.
//
// A suspension held for the claim's turn ends here instead: no turn's end will reach it from a
// shim that never came back. A relaunch the tree lifecycle refuses, a take-back the store would
// not write and a stop the runtime refuses each keep the process, and the deadline is tried again
// at the probe interval, as the registration deadline's are.
//
// After a hello no answer followed, the process is relaunched, charged nothing, and its task taken
// back as a cut turn (replaceProcess, endUnanswered): the process came back, but none of the three
// bounds can be judged of it until its agent answers or a turn frame comes, so a hung agent's turn
// is cut, and a take awaiting its turn may have had that turn run unseen. A held suspension ends
// the process there instead, as every replacement ends it (replaceNow, endHeld). A take-back the
// store refuses leaves the process as it was, and the deadline is tried again at the probe
// interval.
//
// A claim whose shim did say hello, and whose agent has not said since whether it is in a turn
// for the stale-plugin relaunch that waits for the answer (askTurn, askRestoredTurn), reaches the
// deadline its hello armed again for that ask: a hung agent's turn costs nothing to cut, and the
// daemon relaunches onto the current plugin a worker it can (R10). That relaunch is the
// stale-plugin lane's own (refreshPlugin), charged nothing, as the lane's relaunch is: the
// process did come back, and the relaunch is for its plugin, not for a failure of the claim. A
// gate the lane finds full leaves it to the next sweep's post, which no answer holds back any
// more. The deadline then bounds that ask, not a suspension held over the claim, whose shim came
// back and whose turn's end and take's report reach the machine: a hold over a claim working, or
// one that waits for the report of its take's turn (heldWaits, reportwait.go), is left to that
// turn and that wait, as reready leaves it, and the relaunch waits with it (refreshPlugin's own
// holds). Run here, its stop would cut off the take's own turn, or come before that turn's report,
// and settle would take back as a cut turn a task whose turn ran whole. A hold neither waits for
// runs here, as before the hello. And where a hello of the process's generation did not say its
// shim's backlog is whole, a take still awaiting its turn may have had that turn run unseen, its
// frames dropped from the replay, so the relaunch takes it back as a cut turn rather than
// unstarted, as at the agent's answer of no turn (markRestoredTurnUnseen, turnAnswered).
//
// Such a claim read in a turn whose end its connection cannot show is no case of the lane's: a
// replayed start made it working, its connection broke with that turn's end frame in it, and
// neither an answer nor a turn frame came on the next hello's connection, or no hello came at all
// (connSeen not viewWhole). The lane would hold the relaunch for a turn's end that may never come
// (refreshPlugin, holdStaleRelaunch), and leave the claim working until the stall backstop. So it
// is decided below, as any claim whose view is not whole at this deadline: relaunched, uncharged,
// after a hello no answer followed, or retired as a shim that never reconnected with no hello since
// its close, a held suspension stopping the process in either place, and the task taken back as a
// cut turn (interrupted).
func reconnectDeadline(m *Machine, ctx context.Context, _ Event) error {
	askedTurn := m.askTurn && !m.askedAt.IsZero()
	if askedTurn && m.inTurn() && m.connSeen != viewWhole {
		m.askTurn, m.unansweredAsks, askedTurn = false, 0, false
	}
	helloed := askedTurn || m.connSeen == viewAsked
	if (m.held != nil && !helloed) || m.heldRunsNow() {
		return m.suspendHeld(ctx)
	}
	if askedTurn {
		m.askTurn, m.unansweredAsks = false, 0
		m.log.Warn("supervise: the agent of a claim the daemon's start restored did not say whether it is in a turn within the reconnect deadline of its hello; the stale-plugin lane relaunches it, uncharged",
			"askedAt", m.askedAt)
		m.markRestoredTurnUnseen()
		// This deadline is the bound of the wait for a whole view, so its relaunch does not wait
		// for one (refreshPlugin), and row 26 takes its take back as a cut turn (interrupted).
		m.atReconnectDeadline = true
		defer func() { m.atReconnectDeadline = false }()
		return m.refreshPlugin(ctx, m.askTrigger, false)
	}
	if p := m.claim.Pending; p != nil && p.awaitsTurn() {
		m.turnUnseen = true
	}
	if m.connSeen == viewAsked {
		m.log.Warn(viewUnansweredRelaunchLine, "task", pendingNumber(m.claim.Pending))
		end, err := m.replaceProcess(ctx, "the agent did not say whether it is in a turn by the reconnect deadline of its shim's hello", ordinaryEnd, endUnanswered)
		if end == EndNotMade {
			m.arm(TimerRegistration, m.deps.Timeouts.Probe, "")
		}
		return err
	}
	never := neverReady{
		whose: "shim never reconnected",
		never: "never said hello to the daemon that started",
		line:  "supervise: the shim never reconnected after the daemon started; retired its process",
		cause: CauseNeverReconnected,
	}
	why := "the shim never reconnected after the daemon started"
	if m.connSeen == viewClosed {
		never.never = "never said hello again after its connection closed"
		never.line = "supervise: the shim never reconnected after its connection closed; retired its process"
		why = "the shim never reconnected after its connection closed"
	}
	never.before = func(ctx context.Context) error {
		if err := m.interrupted(ctx, why, ordinaryEnd); err != nil {
			return fmt.Errorf("take the task back: %w", err)
		}
		return nil
	}
	return m.retireNeverReady(ctx, never)
}
