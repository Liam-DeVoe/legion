package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
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

// pastTheRun is a span longer than any run of a test binary of this package: longer than the
// longest -timeout a command of this repository gives it (the API race step's 70 minutes,
// .github/workflows/ci-job-envoy-go-race-api.yaml). A test sets a product's window or interval to it
// where the row needs that window never to close during the run (a settle it triggers itself, a
// liveness it does not exercise), so a loaded machine cannot close it before the binary's deadline
// ends the test.
const pastTheRun = 24 * time.Hour

// queuedWaiters is what releaseOnceQueued last read: the backends queued behind the held lock, or
// the error that ended the reading.
type queuedWaiters struct {
	count int
	err   error
}

// releaseOnceQueued runs release from a goroutine of its own once want backends queue behind the
// lock holder's transaction owns (storetest.LockWaiters), and answers a channel that then receives
// what it last read. It is for a row whose own goroutine is held until the calls it raced answer,
// which they do only once the lock is released, so the wait cannot run on the test's goroutine.
// That goroutine calls no t method: at the test binary's deadline less binaryDeadlineMargin
// (binaryDeadlineGuard) it releases with the count it has, and the row's goroutine fails by name
// on a count short of want.
func releaseOnceQueued(t *testing.T, holder pgx.Tx, want int, release func()) <-chan queuedWaiters {
	t.Helper()
	guard := binaryDeadlineGuard(t)
	queued := make(chan queuedWaiters, 1)
	go func() {
		defer release()
		for {
			count, err := storetest.LockWaiters(context.Background(), holder)
			if err != nil || count >= want {
				queued <- queuedWaiters{count: count, err: err}
				return
			}
			select {
			case <-guard:
				queued <- queuedWaiters{count: count}
				return
			default:
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return queued
}
