package supervise

import (
	"context"
	"fmt"
)

// The wait for a take's report (dispatch://INFRA-2811, decision 8 and rule two of T3). For a claim
// whose plugin names its task's number, only the turn-outcome report naming that number finishes
// the task (endFrameFinishes). A let-go made between the take's turn and that report would take
// with it the report that settles the task, and its take-back would mark as a cut turn a task whose
// turn ran whole, to run again in the next process. So what the machine holds for the take's turn
// waits for that report: a held suspension (Machine.held), a stale-plugin relaunch
// (Machine.staleRelaunch), and a wake whose turn comes meanwhile, which is spent into the held
// suspension (wakeNow, Machine.heldLaunch). And a take whose turn ended waits for that report with
// nothing held, too: no other bound reaches a take whose turn started. This file is that one wait:
// what it is (reportOwed), when new work joins it (reportHolds), its bound (TimerReport), which
// takes it reaches (reportReaches), and its one take-back (reportMissed). Nothing of it is a mark
// of its own: it is read off the take and the claim, so every decision that moves the take — its
// turn's start and end, the move of its turn to a later run, a put-back, a finish, a take-back —
// decides the wait again with it.
//
// The wait has three phases, by where the take's turn is:
//
//   - The take's turn runs (inTurn): the work waits for the turn's end, as work held for any turn
//     does (holdSuspension, holdStaleRelaunch), and no bound of this wait runs. So the wait never
//     takes back a take whose own turn is running: the turn's end begins the next phase, and the
//     bound is armed whole from there (armReportWait). That holds for the take's own turn moved to
//     the run after the one the daemon gave it (notTheTakesTurn), too. A wake whose turn comes
//     meanwhile ends a held suspension as it ends every hold over a running turn (wakeNow,
//     endHeld): rule one of T3 has a wake's turn cut a turn off and take its task back with its
//     note.
//   - The take's turn has ended and its report has not come: the work waits for the report, whose
//     finish runs it (reportedTask, afterTurn), as does the take-back a provider error's report
//     makes. The bound is R5's second bound (Timeouts.TurnStart), armed whole at the end of the
//     take's counted turn, with work held or none, and kept by every decision that leaves the take
//     waiting the same way, work that arrives later included; over a take the bound does not reach
//     with nothing held (reportReaches, below), it is armed at the end of the decision that left
//     work waiting here. Any turn's start disarms it, a turn of the host's own included, and that
//     turn's end arms it whole again. When it passes, the task is taken back with the note of a
//     cut turn (reportMissed), as for a process that dies before its report, and the work runs;
//     with nothing held, the value is written again under the task's number for the worker to
//     take, as a retake, and nothing is charged (reportBoundOver). The bound is no shorter than the
//     producer's own schedule for the report: pi-shared sends it again after every attempt the
//     daemon leaves unanswered, each attempt bounded at 5 minutes (DAEMON_ANSWER_TIMEOUT_MS) and
//     the waits between attempts backing off from 500 ms to 30 s (DAEMON_RESTART_FIRST_DELAY_MS,
//     DAEMON_RESTART_MAX_DELAY_MS), with no limit on attempts, and the Claude bridge as well
//     (claudebridge's taskResendMax, taskAnswerTimeout). The bound's default, 25 minutes (decision
//     13 of dispatch://INFRA-2811), holds four attempts that each run out their 5 minutes, with the
//     waits between them, so a report held up by a slow store or a daemon restart lands inside it.
//   - A report gave the take its turn back (putBack): the run the daemon gave it was not its own,
//     and the take awaits its turn again. The report the work waits for is now that turn's, and the
//     worker may already have started it: pi-legion starts the held text at the run's end, before
//     it sends that run's report, so the start frame can be on its way behind the report. The work
//     goes on waiting, for the take's turn to start, which begins the first phase again, or for R5's
//     second wait, armed whole from the put-back (secondWaitNow), whichever is first. That bound
//     charges the worker and takes the task back as R5 says, and the work runs then
//     (turnStartBoundOver). Run at the put-back, a relaunch would cut off a turn whose start the
//     daemon had not seen yet, and the next process would run the task again.
//
// A held suspension's own deadline (TimerSuspend) bounds it in every phase as well, whichever is
// first (suspendAtTimeout): in the first phase its stop cuts the turn off, in the second the task
// is taken back with the note of a cut turn first (reportMissed), and in the third the stop gives
// the take back unstarted (suspended, takeBackUnstarted).
//
// Work that reaches the claim while no turn runs joins the wait in the second phase, and in the
// third where work already waits there (reportHolds): a suspension is held (suspend,
// holdForReport), a relaunch is left waiting (refreshPlugin, runWake), and a wake whose turn comes
// is spent into the held suspension (wakeNow). A take that awaits its turn with nothing waiting
// behind it is no wait: a let-go takes it back unstarted, as rule one of T3 says, since no turn of
// it ran. The wait is memory only, as what waits in it is: a restart forgets both, their askers ask
// again, and the bound is armed again only once the worker has acted (Machine.awaitsAct), so its
// clock starts no earlier than the boot, and a report the producer sends again across a daemon's
// outage has the whole bound once the daemon is back.
//
// With nothing held the bound exists for a take whose counted turn ended and whose report naming
// its number will never come, though its process lives: its text was dropped — Claude Code never
// took it, or Oh My Pi's sendUserMessage refused it (pi-shared's task-take.ts, hostDropped), and
// the producer goes back to no take — and the turn the daemon counts as the take's reported no
// task. Three orders leave such a take holding that turn, the worker seeing no newer revision to
// take: a start of the host's own that reaches the daemon before the dropped turn's report saying
// the text was held, which makes that report moot (heldUnrun); a start of the host's own after the
// put-back that report made, which is given the take (turnStarted); and, with the count known, a
// no-task report that beats its turn's start frame, which passes over a take whose turn has not
// started (notTheTakesTurn), so that start is then given the take. A turn that ran the text sends
// its report well inside the bound, so this take-back runs a task twice only where a process dies
// before its report, which every let-go takes back alike. Turns that follow one another with every
// idle gap shorter than the bound postpone it to the first idle gap of the whole bound.
//
// A take a daemon start restored from the store is reached only where this daemon sees every turn
// start since the take's counted turn: the store shows the daemon before it handled no turn end
// after the take's accept (anchorRestored), and the first hello after the start said its shim's
// backlog is whole and its agent answered on that connection (bindRestoredAnchor), which together
// are the restored take's bound anchor. Any other restored take is left out while nothing is held:
// its own turn may have started while no daemon served it, its start frame lost, and the bound
// would fire during that turn, which would run the task twice. It runs when the worker hands it,
// and a text dropped in that state ends at the process's next let-go.
//
// The report can come and its finish fail on the store (Machine.finishOwed). It came, so no work
// begins to wait for it any more and no bound takes the task back (reportOwed); but work already
// waiting goes on waiting for that finish, which the end-frame bound makes again until it lands,
// and runs at it (reportAwaited, finishAtTurnsEnd), as at the report's own finish.
//
// What waited runs where the wait ends, once the decision ending it has settled the take
// (afterTurn): the report's finish or its provider error's take-back (reportedTask), the owed
// finish (finishAtTurnsEnd), the wait's bound (reportBoundOver), R5's second bound over a take put
// back (turnStartBoundOver). Each reads whether the held suspension waited (heldWaits) before it
// settles the take, since the settled take says nothing of it any more: a suspension held for a
// turn that was no take's, whose stop the runtime refused at that turn's end, is retried at the
// probe interval (suspendHeld), not at a report.

// reportWaitsLine is the line of work that begins to wait for the report of the take's turn
// (reportOwed), at Info, with the task's number and what waits: "suspension", with the hold's source
// and its deadline (the zero time for none); "relaunch", with the pass that asked for it; or "wake",
// with the wake's kind and the hold's source.
const reportWaitsLine = "supervise: the report of the take's turn is still to come; what is held for that turn waits for it"

// putBackWaitsLine is the line of a put-back that leaves work waiting for the take (putBack), at
// Info, with the task's number: the work waits for the take's own turn to start, or for R5's
// second bound.
const putBackWaitsLine = "supervise: the take awaits its turn again; what is held for it waits for that turn to start, or for the second bound"

// reportMissedLine is the line of the wait's take-back (reportMissed), at Warn, with the task's
// number, the bound that came first, and whether work was held: the task goes back as a cut turn,
// and what waited runs, or, with nothing held, its value is written again for the worker to take.
const reportMissedLine = "supervise: the report of the take's turn did not come by its bound; the task is taken back with the note of a cut turn, and what waited for the report runs or its value is written again"

// reportOwed is whether the report that settles the claim's pending take is still to come: a take
// of the pending task stands, and neither its turn's end frame nor a report's finish the store
// refused settles it (overTurnFinish), so only the report naming its number does — a plugin at or
// above TaskTakeContract (endFrameFinishes). It reads the take alone, in whichever phase its turn
// is, so the end of a turn or of a compaction asks it as the turn ends (turnEnded, compactEnded).
func (m *Machine) reportOwed() bool {
	p := m.claim.Pending
	if p == nil || !p.taken() {
		return false
	}
	_, finishes := m.overTurnFinish()
	return !finishes
}

// reportAwaited is what work already waiting on the take waits for: the report that settles it
// (reportOwed), or the finish that report decided and the store refused (Machine.finishOwed),
// which the end-frame bound makes again. It is false for a plugin below TaskTakeContract, whose end
// frame finishes its task (endFrameFinishes): no work waits on such a take past its turn's end.
func (m *Machine) reportAwaited() bool {
	p := m.claim.Pending
	return p != nil && p.taken() && !m.endFrameFinishes()
}

// workHeld is whether anything waits on the machine for the claim's turn or its take's report: a
// held suspension or a held stale-plugin relaunch.
func (m *Machine) workHeld() bool { return m.held != nil || m.staleRelaunch != nil }

// reportHolds is whether work that reaches the claim now joins the wait for the take's report
// rather than run as the claim stands. No turn runs (work that meets a running turn waits for its
// end anyway: holdSuspension, holdStaleRelaunch), and either work already waits on the take
// (reportAwaited), in any phase, or the take's turn has run and its report is still to come.
func (m *Machine) reportHolds() bool {
	if m.inTurn() {
		return false
	}
	if m.workHeld() {
		return m.reportAwaited()
	}
	return m.reportOwed() && m.claim.Pending.turnStarted()
}

// heldWaits is whether the held suspension waits behind the wait now (reportHolds): held, no turn
// running, and the take's report or that report's owed finish still to land. A decision that ends
// the wait reads it before it settles the take, for afterTurn.
func (m *Machine) heldWaits() bool { return m.held != nil && m.reportHolds() }

// reportBounded is the pending take whose report the wait's own bound runs on, and false for none:
// the take's turn ran and is over, and its report has not come (reportOwed: a report whose finish
// the store refused came, and is no report missed); work waits for that report, or nothing does
// and the bound reaches the take all the same (reportReaches); and the bound is judged — R5's
// second bound set, and the worker's first act after a daemon start made (Machine.awaitsAct), as
// R5's own waits are (secondWaitNow). A take awaiting its turn after a put-back is bounded by R5's
// second wait instead, so no take is under the two bounds at once.
func (m *Machine) reportBounded() (*Delivery, bool) {
	if m.deps.Timeouts.TurnStart <= 0 || m.awaitsAct || !SaidReady(m.claim.State) || m.inTurn() || !m.reportOwed() {
		return nil, false
	}
	p := m.claim.Pending
	if !p.turnStarted() || (!m.workHeld() && !m.reportReaches()) {
		return nil, false
	}
	return p, true
}

// reportReaches is whether the wait's bound reaches the pending take with nothing held: every take
// this daemon accepted, and a take a daemon start restored from the store only where this daemon
// sees every turn start since the take's counted turn — the restored take's anchor stands
// (anchorRestored: the store holds no turn end that daemon handled after the take's accept) and
// is bound to the connection of the first hello after the start (bindRestoredAnchor: that hello
// said its shim's backlog is whole, and the agent answered on it). An anchor that went since — a
// start over a running turn lost the count (turnCount.lose), or the process changed — leaves the
// take out, as one never anchored is: a turn of its own may have started unseen, and the bound
// would fire during it (reportwait.go's doc).
func (m *Machine) reportReaches() bool {
	if !m.restoredTake {
		return true
	}
	a := m.turns.anchor
	return a.restored && a.conn != 0 && a.number == m.claim.Pending.Number && m.turns.generation == m.claim.Generation
}

// armReportWait arms the wait's bound (TimerReport) where the claim is in it (reportBounded) and
// disarms it where it is not. It runs at the end of every decision (armWaits, unlock), so the bound
// is armed whole from the decision that left the take waiting for the report of a turn that is
// over — that turn's end, or, over a take the bound reaches only with work held, the work's
// arrival — and a decision that leaves it waiting on the same take keeps the bound where it was.
// Any turn's start disarms it, the take's own turn moved to a later run and a turn of the host's
// own included, and that turn's end arms it whole again; a put-back disarms it, and R5's second
// wait bounds the take from there.
func (m *Machine) armReportWait() {
	p, waiting := m.reportBounded()
	if !waiting {
		m.disarm(TimerReport)
		return
	}
	if a, armed := m.timers[TimerReport]; !armed || a.deliveryID != p.ID {
		m.arm(TimerReport, m.deps.Timeouts.TurnStart, p.ID)
	}
}

// reportBoundOver is the wait's own bound passing (TimerReport): the report of the take's turn did
// not come within R5's second bound of the decision that left the take waiting for it. The task is
// taken back with the note of a cut turn (reportMissed), and what waited runs as the claim stands
// (afterTurn): the held suspension's stop, or the stale-plugin relaunch, which then takes nothing
// back; with nothing held, the offer of the task (offerPending) writes its value again under its
// number, as the retake it is, and the worker takes it again. Nothing is charged: a worker whose
// text was dropped did nothing wrong. A bound that finds a turn running, the report come or the
// take out of its reach does nothing (reportBounded), so it never takes back a take whose own turn
// is running; that turn's end arms it again. A claim being compacted runs the work at the
// compaction's end (compactEnded). A take-back the store refused is tried again at the probe
// interval.
func reportBoundOver(m *Machine, ctx context.Context, _ Event) error {
	p, waiting := m.reportBounded()
	if !waiting {
		return nil
	}
	heldWaited := m.heldWaits()
	if err := m.reportMissed(ctx, "the wait's bound"); err != nil {
		m.arm(TimerReport, m.deps.Timeouts.Probe, p.ID)
		return err
	}
	if m.compacting != nil {
		return nil
	}
	return m.afterTurn(ctx, heldWaited)
}

// reportMissed is the wait's one take-back: the report of the take's turn did not come by the
// bound by names — the wait's own (reportBoundOver) or the held suspension's deadline
// (suspendAtTimeout). So the task goes back under its number with the note of a cut turn, as for a
// process that dies before its report (interrupted), before what waited runs: a take-back the
// store refused stops nothing, and the caller tries it again at the probe interval. Where the
// process stays, the next write of the value is the retake it is (Machine.rewrite); a let-go
// clears that with the process.
func (m *Machine) reportMissed(ctx context.Context, by string) error {
	p := m.claim.Pending
	m.log.Warn(reportMissedLine, "task", p.Number, "by", by, "held", m.workHeld())
	if err := m.interrupted(ctx, "the report of the task's turn did not come by "+by, ordinaryEnd); err != nil {
		return fmt.Errorf("take back the task of %s, whose report did not come: %w", m.claim.Token, err)
	}
	m.rewrite = valueRewrite{number: p.Number, cause: TaskValueRetake}
	return nil
}
