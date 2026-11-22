package supervise

import (
	"context"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// The bounds and the connection (dispatch://INFRA-2811, decision 8; review handoff v4's N1). Three
// bounds judge the worker by the frames its connection carries: R5's second wait (TimerTurnStart),
// the bound of the wait for a take's report (TimerReport), with nothing held as with work held,
// and R5's first wait over a value the worker reported seeing (TimerTake). Each runs only while
// the connection shows the machine every frame of the worker's turns (viewWhole): a close disarms
// all three (closeView), none fires while no connection stands, and each is armed again whole,
// never from what was left of it, once the view is whole again. A remainder carried across a cut
// could pass before the report of a turn that ran unseen lands, and take back a task whose turn
// ran.
//
// The view is whole again at the agent's answer, on the new connection, to whether it is in a turn
// (get_state), which reaches the machine behind every frame the shim replayed (StreamTurnAnswered,
// answerView), or at a turn start or end frame on that connection, whichever comes first
// (seeView). The daemon asks after every reconnect hello of a claim that holds a pending task,
// whatever the hello says of the shim's backlog (askView). A hello saying the backlog is whole
// does not stand in for the answer: the shim keeps a frame only where its write failed, so a frame
// that sat in the socket's send buffer when the connection broke is lost and never replayed, and
// if it was the start of the take's turn, a bound armed at the hello would pass during that turn.
// The backlog's word decides only what it decided before: the restored anchor (bindRestoredAnchor)
// and the count of the process's turns (readBacklog).
//
// "In a turn" is read as a turn start, over a take awaiting its turn as the take's own turn
// (turnStarted): no bound runs over a turn, and its end arms them whole. "Not in a turn" over a
// claim read in a turn is read as that turn's end (turnEnded), as askTurnOver reads it, so a lost
// end frame leaves no claim working for ever, and the bounds are armed whole from the answer.
//
// The ask is made once on each reconnect hello, never again at a sweep. Its one bound is the
// reconnect deadline (TimerRegistration, reconnectBound), armed at the hello and disarmed by the
// answer or a turn frame: at it the process is relaunched, charged nothing, and the task taken
// back as a cut turn (reconnectDeadline). A later hello is a new connection, which asks once on it
// and arms the deadline again. Where a daemon start's own ask stands — of a claim restored working
// (askFirst, askTurnOver) or holding a take on a stale plugin (askTurn, askRestoredTurn) — its
// answer serves this rule too, and no second ask is made.
//
// A close of a live process whose claim holds a pending task arms the reconnect deadline, whole
// from the close: a hello ends it, and at it the process is probed, and a live one retired and
// relaunched with CauseNeverReconnected, the task taken back as a cut turn. A claim holding no
// pending task arms nothing and is asked nothing, so an idle worker whose shim reconnects late is
// treated as before.
//
// After a daemon start every claim reconnects with a hello, so a machine NewMachine restores with a
// process — launching, shim-connected, registered, ready, idle or working — starts with no
// connection (viewNoHello): a first act that reaches the daemon before the hello over HTTP, a take,
// a report or the agent's ready, arms none of the three bounds.
//
// The agent's ready over HTTP over a view that is not whole keeps the reconnect deadline rather
// than end it, as the ready ends the registration deadline over a whole one (readyView): nothing
// else bounds a shim that never comes back once its agent is ready, so a take it holds would wait
// for ever.
//
// One case is stated, not closed: a connection that closes again before each bound passes
// postpones the bounds, each time to a whole bound after its next hello. The reconnect deadline
// bounds each gap, and nothing bounds how many gaps come.
type connView uint8

const (
	// viewWhole is a connection standing whose frames the machine reads whole: the first hello of
	// the process, or a reconnect hello the agent's answer or a turn frame followed, or a reconnect
	// hello of a claim that held no pending task. The three bounds run.
	viewWhole connView = iota
	// viewNoHello is a claim a daemon start restored with a process — launching, shim-connected,
	// registered, ready, idle or working — its shim's first hello to this daemon still to come
	// (NewMachine).
	viewNoHello
	// viewClosed is a claim whose connection closed, its shim's next hello still to come (closeView).
	viewClosed
	// viewAsked is a reconnect hello over a claim holding a pending task, neither the agent's answer
	// to whether it is in a turn nor a turn frame on that connection come yet (askView).
	viewAsked
)

// viewAskedLine is the line of the ask a reconnect hello makes (askView), at Info, with the task
// pending: the three bounds wait for the agent's answer or a turn frame.
const viewAskedLine = "supervise: the shim reconnected over a pending task; the daemon asks its agent whether it is in a turn, and the bounds on the worker wait for that answer or a turn frame"

// viewUnansweredLine is the line of a reconnect hello's ask the agent did not answer (askView), at
// Warn: the bounds wait for a turn frame, and the reconnect deadline of the hello bounds the wait.
const viewUnansweredLine = "supervise: the agent did not say whether it is in a turn after its shim reconnected; the bounds on the worker wait for a turn frame or the reconnect deadline"

// viewAnsweredLine is the line of the answer that makes the view whole again (answerView), at Info,
// with whether the agent is in a turn.
const viewAnsweredLine = "supervise: the agent said whether it is in a turn after its shim reconnected; the bounds on the worker run again from here"

// viewUnansweredRelaunchLine is the line of the reconnect deadline of a hello no answer or turn
// frame followed (reconnectDeadline), at Warn: the process is relaunched, charged nothing, and its
// task taken back as a cut turn.
const viewUnansweredRelaunchLine = "supervise: the agent did not say whether it is in a turn within the reconnect deadline of its shim's hello; it is relaunched, uncharged, and its task taken back as a cut turn"

// reconnectBound is the reconnect deadline's bound: the registration deadline's base,
// Timeouts.Boot × RegistrationIntervals.
func (m *Machine) reconnectBound() time.Duration {
	return runtime.RegistrationDeadline(m.deps.Timeouts.Boot, m.deps.Timeouts.RegistrationIntervals, 0)
}

// closeView is the claim's connection closing (closed): the view is cut, so the three bounds go at
// the end of this decision (armWaits). A live process whose agent said it is ready and whose claim
// holds a pending task is given the reconnect deadline, whole from the close, unless one stands
// already: the deadline of a hello's ask still unanswered, or of a daemon start's own. A claim not
// ready keeps the registration deadline it runs under, and its ready, with no hello since, keeps a
// reconnect deadline whole from this close (readyView); one holding no pending task is given none.
func (m *Machine) closeView() {
	m.connSeen, m.closedAt = viewClosed, m.deps.Clock.Now()
	if !SaidReady(m.claim.State) || m.claim.Locator == nil || m.claim.Pending == nil {
		return
	}
	if _, armed := m.timers[TimerRegistration]; !armed {
		m.arm(TimerRegistration, m.reconnectBound(), "")
	}
}

// readyView is the agent's ready (ready) read against the claim's connection. Over a whole view
// the ready ends the registration deadline, as it always has. Over a view that is not whole the
// deadline stays as the reconnect deadline (reconnectDeadline), since nothing else bounds a shim
// that never comes back once its agent is ready:
//
//   - a close before the ready (viewClosed) of a live process holding a pending task is given the
//     later of the deadline standing and the close plus the reconnect bound, the deadline closeView
//     gives a ready claim, so a hello inside either finds nothing retired; a close of a claim
//     holding no pending task is given none, as closeView gives none;
//   - any other view not whole — a daemon start's before the shim's first hello (viewNoHello) —
//     keeps the deadline standing, or is given one whole from now where none stands, as a claim the
//     start restored ready is.
func (m *Machine) readyView() {
	switch m.connSeen {
	case viewWhole:
		m.disarm(TimerRegistration)
		return
	case viewClosed:
		if m.claim.Locator == nil || m.claim.Pending == nil {
			m.disarm(TimerRegistration)
			return
		}
		due := m.closedAt.Add(m.reconnectBound())
		if a, armed := m.timers[TimerRegistration]; armed && !a.due.Before(due) {
			return
		}
		m.arm(TimerRegistration, max(due.Sub(m.deps.Clock.Now()), 0), "")
		return
	}
	if _, armed := m.timers[TimerRegistration]; !armed {
		m.arm(TimerRegistration, m.reconnectBound(), "")
	}
}

// askView reads the view a reconnect hello opens (reconnected, reconnectedMidTurn). A claim holding
// no pending task asks nothing, and its view is whole. One holding a task waits for the agent's
// answer or a turn frame (viewAsked): the hello asks once, here, and arms the reconnect deadline
// whole from the hello, unless a daemon start's own ask stands (askTurn, askFirst), whose answer
// serves and whose bound stands as it was. An ask the agent does not answer is not asked again:
// the view waits for a turn frame, or for the deadline.
func (m *Machine) askView(ctx context.Context) {
	if m.claim.Pending == nil {
		m.connSeen = viewWhole
		return
	}
	m.connSeen = viewAsked
	if m.askTurn || m.askFirst {
		return
	}
	m.arm(TimerRegistration, m.reconnectBound(), "")
	m.log.Info(viewAskedLine, "task", m.claim.Pending.Number)
	conn, ok := m.deps.Conns.Conn(m.claim.Token)
	if !ok {
		// The connection closed before the decision that reads its hello: its close is queued
		// behind the hello, and cuts the view again.
		return
	}
	if _, answered := m.streaming(ctx, conn); !answered {
		m.log.Warn(viewUnansweredLine, "task", m.claim.Pending.Number)
	}
}

// seeView is a turn start or end frame (Handle): the connection it came on carries the worker's
// turns, so the view is whole, and the reconnect deadline of a hello's ask goes, unless it is the
// bound of a daemon start's own ask (askTurn), which only its answer ends.
func (m *Machine) seeView() {
	if m.connSeen == viewWhole {
		return
	}
	asked := m.connSeen == viewAsked
	m.connSeen = viewWhole
	if asked && !m.askTurn {
		m.disarm(TimerRegistration)
	}
}

// turnMayHaveRunUnseen is whether the pending take, awaiting its turn (takeAwaitsTurn), may have
// had that turn run with no frame of it reaching the machine: the view is not whole (connSeen) —
// no hello since a daemon start or a close, or since the hello neither the agent's answer nor a
// turn frame — or the take is marked so (Machine.turnUnseen). The record of a let-go reads it
// into the mark and the record alike (recordEnd: letGo, died, displaced), so the take-back of that
// let-go takes the take back as a turn cut off and its next text carries that note, as the
// reconnect deadline takes it back (interrupted; takeBackUnstarted for a retirement, a suspension
// and a held suspension's deadline).
func (m *Machine) turnMayHaveRunUnseen() bool {
	return (m.turnUnseen || m.connSeen != viewWhole) && m.takeAwaitsTurn()
}

// answerView is the agent's answer to whether it is in a turn, over a view a reconnect hello left
// waiting for it (turnAnswered): the view is whole, the reconnect deadline goes, and the answer is
// read against the claim with every frame the shim replayed applied — a turn running over a claim
// not busy is a turn start, the take's own over a take awaiting its turn (turnStarted), and no turn
// over a claim in one is that turn's end (turnEnded). The bounds are armed whole from here at the
// end of this decision, or at the end of the turn the answer started.
func (m *Machine) answerView(ctx context.Context, answer StreamTurnAnswered) error {
	m.connSeen = viewWhole
	m.disarm(TimerRegistration)
	m.log.Info(viewAnsweredLine, "streaming", answer.Streaming)
	switch {
	case answer.Streaming && !m.busy():
		return turnStarted(m, ctx, answer)
	case !answer.Streaming && m.inTurn():
		return turnEnded(m, ctx, answer)
	}
	return nil
}
