// The work a handler leaves running behind its answer, off its own goroutine (runAfterAnswer),
// and the shutdown's wait for it (awaitAfterAnswer). A handler holds its written response until it
// returns, so work the answer must not wait on - a deregistration's consumer delete
// (deleteAfterAnswer, session_consumer_delete.go) and the filter settle of a registration this
// listener did not answer (settleAfterAnswer, session_filter_fence.go) - runs here. The fields
// this counts and holds off (afterAnswerMu, afterAnswer, afterAnswerOff) stay where the type they
// belong to is declared (sessionDelivery, session_delivery.go).

package main

import "context"

// runAfterAnswer runs work on a goroutine of its own, which the shutdown waits for before it
// drains the session-consumer connection that work runs on (awaitAfterAnswer), and reports
// whether it started it: once the shutdown has stopped starting such work it runs nothing and
// answers false, and the caller logs what it left undone.
func (m *sessionDelivery) runAfterAnswer(work func()) bool {
	m.afterAnswerMu.Lock()
	if m.afterAnswerOff {
		m.afterAnswerMu.Unlock()
		return false
	}
	m.afterAnswer.Add(1)
	m.afterAnswerMu.Unlock()
	go func() {
		defer m.afterAnswer.Done()
		work()
	}()
	return true
}

// awaitAfterAnswer stops starting work behind a handler's answer and waits, within ctx, for the
// work already running (runAfterAnswer). The shutdown calls it once the HTTP server has stopped
// and before it drains the session-consumer connection that work runs on, so a deregistration
// that answered a moment before the stop still frees its session's slot of the stream's consumer
// cap, and a registration it answered 503 still has its consumer's filter set to the row the
// bucket holds. Work still running when ctx ends is left where it is and said so: the
// connection's drain ends it, the sweep deletes the consumer a delete did not, and the session's
// next registration sets the filter a settle did not.
func (m *sessionDelivery) awaitAfterAnswer(ctx context.Context) {
	if m == nil {
		return
	}
	m.afterAnswerMu.Lock()
	m.afterAnswerOff = true
	m.afterAnswerMu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.afterAnswer.Wait()
	}()
	select {
	case <-done:
	case <-ctx.Done():
		m.logger.Warn("work a handler left behind its answer was still running at shutdown: the sweep deletes a consumer a deregistration's delete did not, and the session's next registration sets a filter a settle did not")
	}
}
