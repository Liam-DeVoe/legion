package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testwait"
)

// The package's own waits for a fact, beside testwait's. Each ends on its fact, and its one bound
// is the test binary's deadline less testwait's margin, never a span of its own: a loaded machine
// that is merely slow passes, and a fact that never comes fails the test by name, with what was
// last read, before go test's alarm.

// waitUntil asks condition every 10 ms until it holds, failing the test with what when it never
// does (testwait.UntilPolling).
func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	testwait.UntilPolling(t, 10*time.Millisecond, what, func() (bool, bool) {
		held := condition()
		return held, held
	})
}

// awaitStill waits until count has not moved for still, reading it every 10 ms, and answers its
// value then: a wait for activity to settle - a hub's recomputes, a pool's acquires, a log's head
// - before a test measures one change alone. still is the length of the calm the test needs, not a
// bound: the wait ends only once count holds still that long, however long the activity runs.
func awaitStill(t *testing.T, what string, still time.Duration, count func() int64) int64 {
	t.Helper()
	last, since := count(), time.Now()
	return testwait.UntilPolling(t, 10*time.Millisecond, fmt.Sprintf("%s to hold still for %s (its last reading)", what, still), func() (int64, bool) {
		if n := count(); n != last {
			last, since = n, time.Now()
		}
		return last, time.Since(since) >= still
	})
}
