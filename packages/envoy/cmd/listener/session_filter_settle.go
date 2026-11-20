// The settle loop: what a fenced filter write could not settle in the attempt its caller made
// under the session's lock is attempted again here, off that caller, until the session's consumer
// filters on what the bucket's newest record and row ask for or the session is gone
// (settleAgain, settleLoop); and the write whose answer did not come back, which is judged again
// once it may have landed (unansweredWrite).

package main

import (
	"log/slog"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
)

// filterSettlePause is how long the settle loop waits before an attempt that follows one that
// did not settle (settleLoop). It is a bound derived from what that attempt waits on, the bus
// answering, and not a number chosen to make anything pass: the attempt before it met a bus that
// did not answer within the fence's budget, or that answered a row the attempt could not keep up
// with. A connection that lost its server tries it again every bus.ReconnectWait, so an attempt
// made sooner meets the same lost connection, and an attempt's first call waits for that
// reconnect itself within its own budget (bus.Client's ensureConnWithContext); against a server
// that answers slowly, the pause is what keeps the loop from adding a read and a write to it every
// round trip. Its cost is that a filter the bus refused stays wrong for up to one pause and one
// attempt after the bus answers again. An attempt that follows a write whose answer came back
// late makes no pause: the bus has just answered (unansweredWrite).
const filterSettlePause = bus.ReconnectWait

// settleRequest is what the settle loop running for one session has been asked to do since its
// last attempt began (settleAgain), under sessionDelivery.settleMu: after, ackFloor and why are
// the latest request's label, the highest floor any asked a create at, and why it was asked;
// pause is set when any request followed an attempt that did not settle; again is set when an
// attempt is still owed.
type settleRequest struct {
	after    string
	ackFloor uint64
	why      string
	pause    bool
	again    bool
}

// settleAgain asks for another attempt at sessionID's consumer filter, after the attempt of a
// fenced write that did not settle it or after a write whose answer came late (fence,
// unansweredWrite): why says which, pause whether the attempt waits filterSettlePause first, and
// ackFloor is the floor a consumer the attempt creates again starts after, as the repair's is
// (repairRegistered). One loop runs per session at a time, on a goroutine of its own the shutdown
// waits for (runAfterAnswer), and a request that comes while it runs is folded into its next
// attempt, so a session whose writes fail many times over has one loop and not one per failure.
// The caller returns at once, holding whatever lock it holds: the loop takes the session's lock
// for each attempt alone.
func (m *sessionDelivery) settleAgain(sessionID, after string, ackFloor uint64, why string, pause bool) {
	m.settleMu.Lock()
	if run, ok := m.settling[sessionID]; ok {
		run.after, run.why = after, why
		run.ackFloor = max(run.ackFloor, ackFloor)
		run.pause = run.pause || pause
		run.again = true
		m.settleMu.Unlock()
		return
	}
	if m.settling == nil {
		m.settling = map[string]*settleRequest{}
	}
	m.settling[sessionID] = &settleRequest{after: after, ackFloor: ackFloor, why: why, pause: pause, again: true}
	m.settleMu.Unlock()
	if !m.runAfterAnswer(func() { m.settleLoop(sessionID) }) {
		m.settleMu.Lock()
		delete(m.settling, sessionID)
		m.settleMu.Unlock()
		m.logger.Warn(settleStoppingLine, slog.String("session_id", sessionID), slog.String("after", after), slog.String("why", why))
	}
}

// settleLoop makes the attempts settleAgain asked for sessionID's consumer filter until one
// settles it with no request left over, the session is gone, or the listener stops.
//
// Each attempt is the repair of a session's consumer (repairPass): it reads the bucket's newest
// record and interest row and makes the consumer what they ask for, under the fence
// (fenceFilterWrite) and on the fence's own budget, under the session's lock, which it releases
// before the pause after it. The repair and not the step the first attempt took for an unsubscribe,
// a settle or a restart (storedFilterStep) because it is the one write that reads the session's
// record: a record the bucket no longer holds is a session that is gone, where the loop stops
// and says so, and a record that asks for consumer delivery of a stream that holds no consumer is
// a session answered 200 that the repair gives its consumer back, at ackFloor's next sequence,
// as the delete's repair does.
//
// It logs a WARN line each time it starts an attempt, naming the session, the attempt, why it
// is made and the pause before it; an INFO line once an attempt settles the filter; and a WARN
// line when it stops for the session being gone or the listener stopping. A listener that stops
// mid-attempt ends that attempt (afterAnswerContext): the session's next registration sets the
// filter of the row it stores.
func (m *sessionDelivery) settleLoop(sessionID string) {
	stop := m.afterAnswerContext()
	for attempt := 1; ; attempt++ {
		m.settleMu.Lock()
		run := m.settling[sessionID]
		if !run.again {
			delete(m.settling, sessionID)
			m.settleMu.Unlock()
			m.logger.Info(settleSettledLine, slog.String("session_id", sessionID), slog.String("after", run.after), slog.Int("attempts", attempt-1))
			return
		}
		after, ackFloor, why := run.after, run.ackFloor, run.why
		pause := time.Duration(0)
		if run.pause {
			pause = filterSettlePause
		}
		run.again, run.pause = false, false
		m.settleMu.Unlock()

		m.logger.Warn(settleAgainLine, slog.String("session_id", sessionID), slog.String("after", after),
			slog.Int("attempt", attempt), slog.String("why", why), slog.String("pause", pause.String()))
		timer := time.NewTimer(pause)
		select {
		case <-timer.C:
		case <-stop.Done():
			timer.Stop()
			m.endSettle(sessionID)
			m.logger.Warn(settleStoppingLine, slog.String("session_id", sessionID), slog.String("after", after), slog.String("why", why))
			return
		}
		unlock := m.locks.lock(sessionID)
		end := fenceFilterWrite(stop, m.logger, m.registry, sessionID, after, m.repairPass(m.sessions, m.registry, sessionID, ackFloor, after))
		unlock()
		switch end {
		case fenceSettled:
			// The top of the loop ends it, unless a request came while this attempt ran.
		case fenceSessionGone:
			m.endSettle(sessionID)
			m.logger.Warn(settleGoneLine, slog.String("session_id", sessionID), slog.String("after", after), slog.Int("attempts", attempt))
			return
		case fenceCancelled:
			m.endSettle(sessionID)
			m.logger.Warn(settleStoppingLine, slog.String("session_id", sessionID), slog.String("after", after), slog.String("why", why))
			return
		default:
			m.settleMu.Lock()
			run.why, run.pause, run.again = end.String(), true, true
			m.settleMu.Unlock()
		}
	}
}

// endSettle forgets the settle loop of sessionID, which is returning, so the next request starts
// another (settleAgain).
func (m *sessionDelivery) endSettle(sessionID string) {
	m.settleMu.Lock()
	delete(m.settling, sessionID)
	m.settleMu.Unlock()
}

// The settle loop's lines (settleAgain, settleLoop).
const (
	settleAgainLine    = "listener settles the filter of a session's consumer again: the attempt before did not leave it on the row the bucket holds"
	settleSettledLine  = "listener settled the filter of a session's consumer on the row the bucket holds"
	settleGoneLine     = "listener stopped settling the filter of a session's consumer: the bucket holds no record of the session, so no consumer of it is read"
	settleStoppingLine = "listener stopped settling the filter of a session's consumer: the listener is stopping; the session's next registration sets it"
)

// unansweredWrite is the listener's handler for a create or an update of a session consumer whose
// caller stopped waiting for its answer (bus.SessionConsumers.OnUnansweredWrite): the server may
// still apply it, and the filter it carries would then stand over whatever this listener set
// since, with nothing to read the consumer again (bus.UnansweredWrite says why the order of the
// two is the server's). It waits for that answer off the caller's goroutine, as work the shutdown
// waits for (runAfterAnswer), and once the answer says the write landed, or the connection it went
// out on was lost with its answer, asks the settle loop for an attempt with no pause before it
// (settleAgain): that attempt reads the consumer after the write, so a filter of a plan nothing
// stored, or of a row the bucket has replaced, is taken back to the row the bucket holds. A
// refused write changed nothing, and the wait ends there. The wait is counted from the moment the
// caller gave up, before that caller returns its error, so a reader waiting for the work behind
// an answer (awaitAfterAnswer) waits for this one and for the attempt it asks for.
func (m *sessionDelivery) unansweredWrite(w *bus.UnansweredWrite) {
	started := m.runAfterAnswer(func() {
		switch end := w.Wait(m.afterAnswerContext().Done()); end {
		case bus.WriteApplied:
			m.settleAgain(w.SessionID, filterAfterUnansweredWrite, 0, "a consumer write whose answer came late landed", false)
		case bus.WriteAnswerLost:
			m.settleAgain(w.SessionID, filterAfterUnansweredWrite, 0, "a consumer write whose answer was lost with its connection may have landed", false)
		}
	})
	if !started {
		w.Release()
	}
}
