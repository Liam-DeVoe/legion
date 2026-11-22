// Package testwait holds the bounded wait the daemon's tests share. A test that drives real
// processes, a real database or a real socket waits for something it cannot make happen on its
// own — a delivery, the transaction that answers it — and the wait is bounded by the test
// binary's own deadline rather than a fixed span: on a loaded machine a step that is merely slow
// is not a failure, and a condition that never holds still fails, naming what it waited for.
// Eventually is the wait a test usually wants; WithinPolling takes a bound and a pace of its
// own; Holds answers rather than fails, for a caller whose failure text is its own; and Bounded
// is the clamp alone, for a wait that holds the rest itself.
package testwait

import (
	"testing"
	"time"
)

// Poll is how often a condition is asked. It is short enough that a test which passes in
// milliseconds still does, and long enough that a waiting test costs a loaded machine little.
const Poll = 10 * time.Millisecond

// Eventually waits until condition holds, asking every Poll. Its one bound is the test binary's
// deadline less margin, as Receive's is, and under -timeout 0 it has none: a condition a loaded
// machine makes slow still passes, and one that never holds fails the test naming what it waited
// for, on the line Receive gives up with (gaveUp). A wait that begins with margin or less of the
// deadline left, as every wait does under a -timeout of 30 s or less, asks once.
func Eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	start := time.Now()
	if !waitUntil(eventuallyEnd(t, start), Poll, condition) {
		gaveUp(t, what, time.Since(start))
	}
}

// eventuallyEnd is when a wait of Eventually that begins at start in t gives up: the test
// binary's deadline less margin, and under -timeout 0, which sets no deadline, a moment no wait
// reaches (unboundedRemaining after start).
func eventuallyEnd(t *testing.T, start time.Time) time.Time {
	if deadline, bounded := t.Deadline(); bounded {
		return deadline.Add(-margin)
	}
	return start.Add(unboundedRemaining)
}

// Bounded is bound, or what is left of the test binary's own deadline less margin when that is
// sooner, so a -timeout smaller than bound still reports the wait rather than the binary's panic.
// A wait with a span or a margin of its own takes those; Eventually and Receive have none.
func Bounded(t *testing.T, bound, margin time.Duration) time.Duration {
	if testDeadline, ok := t.Deadline(); ok {
		if left := time.Until(testDeadline) - margin; left < bound {
			bound = left
		}
	}
	return bound
}

// WithinPolling waits for condition under the caller's own bound and its own pace, with no
// clamp to the test binary's deadline, failing the test with what it waited for when the bound
// passes: a wait whose span is the thing under test says how long it means to wait.
func WithinPolling(t *testing.T, bound, poll time.Duration, what string, condition func() bool) {
	t.Helper()
	if waitUntil(time.Now().Add(bound), poll, condition) {
		return
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Holds reports whether condition held before bound passed, and fails nothing itself: a caller
// that keeps a failure text of its own, prints what it read when it gave up, or carries on
// either way takes the answer rather than the test. The caller gives the pace as well, because
// a question that costs something — a database read, a walk of /proc — or that is itself an
// attempt must not be asked a hundred times a second; Poll is this package's own.
func Holds(bound, poll time.Duration, condition func() bool) bool {
	return waitUntil(time.Now().Add(bound), poll, condition)
}

// waitUntil asks condition every poll until it holds or deadline passes, reporting which. It
// asks once more after the deadline, so a condition that came true during the last sleep is not
// reported false: the alternative loses a race against the thing it waits for, and the cost is
// one more question on the path where the wait has already failed.
func waitUntil(deadline time.Time, poll time.Duration, condition func() bool) bool {
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(poll)
	}
	return condition()
}
