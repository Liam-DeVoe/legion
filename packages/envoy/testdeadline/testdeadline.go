// Package testdeadline gives a test's call the deadline the test binary's own -timeout leaves it,
// less the part of that deadline the test keeps for its cleanup. It is the one implementation of
// that rule in this repository: Envoy's tests import it, and so do the daemon's, through the
// daemon module's requirement of this module (packages/daemon/go.mod replaces
// github.com/sjawhar/envoy with ../envoy). Each module's internal/testwait keeps its own polling
// helpers.
package testdeadline

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Cleanup is the part of the test binary's deadline a call bounded by Answered or StartupBound
// leaves for what the test does after the call gives up: its own failure, its t.Cleanup functions
// (a rollback, a pool's close, a database's drop, a container's removal) and the report, so a call
// that never answers fails the test naming the call, and the test still cleans up before the
// binary's own deadline ends it in a panic. It is a reserve, not a timeout: a call bounded by it
// gives up Cleanup before the binary's deadline rather than at it, so that a test whose call does
// not answer names that call rather than ending in the binary's panic, and a -timeout no longer
// than it is refused (refuseShortTimeout). It is the slowest step of that cleanup measured, a
// database's drop, times 1.15:
//   - this dev box (288 CPUs, load average about 600, 2026-11-19 19:33Z): a `drop database` of a
//     test's database took 77.243 s on a PostgreSQL 18 server of the CI image, waiting for every
//     backend to accept its ProcSignalBarrier; 77.243 s x 1.15 = 88.829 s;
//   - a CI runner: each test drop writes its own time into the server's log (testdb.LogDrop), and
//     the slowest drop of any Go job of CI run 44665181416 took 20.188 s (envoy-go-race-docs),
//     under the dev box's.
//
// During an XFS stall of the dev box's disk from 20:15Z the same day, one drop took 357.6 s; a test
// that failed then would end in the binary's panic rather than name its call.
const Cleanup = 88829 * time.Millisecond

// NoBound stands for no bound where a library takes a duration and has no form without one
// (testcontainers' WithStartupTimeout): a century.
const NoBound = 100 * 365 * 24 * time.Hour

// started is when this test binary's package initialisation ran. The testing package reads
// -timeout and sets the deadline from it later, in m.Run, so the deadline less started is the
// -timeout the binary was given plus what package initialisation and a TestMain did before m.Run.
var started = time.Now()

// refused fails one test of the binary, the first that asks, when the binary's -timeout is too
// short; every later call gets its bound without a second refusal.
var refused sync.Once

// deadlineOf is t's test binary's deadline, and whether it has one (`go test -timeout 0` sets
// none).
func deadlineOf(t testing.TB) (time.Time, bool) {
	deadlined, ok := t.(interface{ Deadline() (time.Time, bool) })
	if !ok {
		return time.Time{}, false
	}
	return deadlined.Deadline()
}

// refuseShortTimeout fails t, once for each once, when the binary's -timeout (deadline less start)
// leaves a call no time after Cleanup, naming the -timeout, the reserve and the shortfall. It reads
// the -timeout, not the time left now: late in a long run a call whose deadline has passed fails
// with its own error at its own call site, and nothing replaces that error with this one. It uses
// Errorf, which any goroutine of the test may call, and it is a helper, as Answered and
// StartupBound are, so the line the refusal names is the test's own call of one of them.
func refuseShortTimeout(t testing.TB, deadline, start time.Time, once *sync.Once) {
	t.Helper()
	timeout := deadline.Sub(start)
	if timeout > Cleanup {
		return
	}
	first := false
	once.Do(func() { first = true })
	if !first {
		return
	}
	t.Errorf("testdeadline: the test binary's -timeout, about %s, leaves its calls no time after the %s testdeadline.Cleanup keeps for a test's cleanup: %s short; run the test with a -timeout at least that much longer",
		timeout.Round(time.Second), Cleanup, (Cleanup - timeout).Round(time.Millisecond))
}

// Answered is parent with the deadline a test gives a call it only needs answered — a query, a
// connection, a transaction's begin, a lock it takes to set a scene — whose timing the test does
// not test: the test binary's own deadline less Cleanup. No literal bounds it, so a loaded machine
// on which the call is slow but answers passes, and a call that never answers still fails, Cleanup
// before the test binary's deadline, naming the call. With no test deadline (`go test -timeout 0`)
// the call has none but parent's. Once that deadline has passed, late in a long run, the context
// answered has already ended, and the call fails with its own error. A -timeout too short to leave
// any call time after Cleanup is refused once (refuseShortTimeout). A call whose bound is the
// test's own subject (a lock wait the test expects refused, a latency it claims) keeps its bound
// instead.
func Answered(t testing.TB, parent context.Context) (context.Context, context.CancelFunc) {
	t.Helper()
	return answered(t, parent, started, &refused)
}

func answered(t testing.TB, parent context.Context, start time.Time, once *sync.Once) (context.Context, context.CancelFunc) {
	t.Helper()
	deadline, set := deadlineOf(t)
	if !set {
		return context.WithCancel(parent)
	}
	refuseShortTimeout(t, deadline, start, once)
	return context.WithDeadline(parent, deadline.Add(-Cleanup))
}

// StartupBound is the duration a wait for a container the test starts is given — a testcontainers
// readiness wait, whose own default is 60 s — what the test binary's own deadline leaves less
// Cleanup, so a container that starts slowly on a loaded host is waited for and one that never
// becomes ready fails Cleanup before the test binary's deadline, naming the wait; with no test
// deadline, NoBound. Where nothing is left, late in a long run, it is one nanosecond, spent before
// the strategy starts: the strategy's context ends at once (testcontainers' wait strategies take a
// zero or negative bound the same way), and the wait fails with its own error. A -timeout too
// short to leave any call time after Cleanup is refused once (refuseShortTimeout). The strategy
// reads the container's state on every poll, so a container that exits ends the wait at once all
// the same.
func StartupBound(t testing.TB) time.Duration {
	t.Helper()
	return startupBound(t, started, &refused)
}

func startupBound(t testing.TB, start time.Time, once *sync.Once) time.Duration {
	t.Helper()
	deadline, set := deadlineOf(t)
	if !set {
		return NoBound
	}
	refuseShortTimeout(t, deadline, start, once)
	if left := time.Until(deadline) - Cleanup; left > 0 {
		return left
	}
	return time.Nanosecond
}
