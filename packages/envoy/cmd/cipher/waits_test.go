// packages/envoy/cmd/cipher/waits_test.go
//
// The waits this package's tests share. A test waits for a fact it expects: a line on a terminal,
// a process's end, a file, a request a fake saw. Such a wait has one bound, the moment every such
// wait gives up (waitsGaveUp), and no span of its own. A row whose claim is that code honours a
// deadline of its own, or waits for nothing, times that code against its deadline instead,
// allowing for the time this process was held up meanwhile (requireOnTime, endsByItsDeadline).
package main

import (
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testwait"
)

// waitMargin is how long before the test binary's deadline (go test's -timeout) every wait for a
// fact in these tests gives up, and that moment is the one bound they have (waitsGaveUp). A fact
// that comes ends its wait at once. A fact that never comes fails the test at that moment, with
// the wait's own text, which says what it waited for.
//
// No wait has a span of its own, because a span only says how fast this machine was at that
// moment. On a dev box at a load average of 800 to 1,100 on 288 processors, a 10 s wait for a
// terminal line failed 2 runs of the prompt job family in 25, and one of those tests took 270 s;
// a minute counted from the gaps between polls failed too, when the test process ran and the
// process it waited for was starved.
//
// 30 s is the time a wait that gave up leaves for what must still run before go test's alarm panics
// the binary: the failing test's cleanups (a kill of what it started, and the wait for it), each
// later wait of the test, which then fails at once, and the test's report. The alarm prints no
// running test's log, and without -v a failed subtest's text is printed only once its parent test
// ends, so a margin those steps outlast loses the text that names the wait.
const waitMargin = 30 * time.Second

// untilGiveUp is how long is left before the moment every wait for a fact in these tests gives up:
// the test binary's deadline less waitMargin, the clamp testwait.Bounded gives. The day passed to
// it stands for no bound of the wait's own: Bounded answers what is left before that moment when
// it is sooner, and the day itself where there is no deadline (-timeout 0). It is negative once
// that moment has passed.
func untilGiveUp(t *testing.T) time.Duration {
	return testwait.Bounded(t, 24*time.Hour, waitMargin)
}

// waitsGaveUp reports whether the moment every wait for a fact in these tests gives up has passed
// (untilGiveUp). Under -timeout 0 it never has. The prompt job helper has the deadline
// helperTimeout gives it, so its waits give up before the test's.
func waitsGaveUp(t *testing.T) bool {
	return untilGiveUp(t) <= 0
}

// received is the next value ch receives, and true; or, when the moment every wait for a fact in
// these tests gives up passes first, the zero value and false, and the caller fails the test with
// its own text. A value ch has by then is still taken.
func received[T any](t *testing.T, ch <-chan T) (T, bool) {
	t.Helper()
	giveUp := time.NewTimer(untilGiveUp(t))
	defer giveUp.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-giveUp.C:
		select {
		case v := <-ch:
			return v, true
		default:
			var zero T
			return zero, false
		}
	}
}

// stallTick is how long each sleep of a stallMeter asks for.
const stallTick = 5 * time.Millisecond

// stallMeter adds up how long this process was held up while it runs: a goroutine sleeps
// stallTick at a time and counts how much longer each sleep took than that. A loaded machine, a
// CPU quota or a frozen cgroup holds the whole process up, the code a row times with it, and the
// meter counts that time; code that is late on its own does not move it. Code in another process
// the row started shares the machine, so a machine that holds every process up is counted; one
// that starves that process alone and not this one is not, and only lateSlack covers it.
type stallMeter struct {
	mu       sync.Mutex
	held     time.Duration
	lastWake time.Time
}

// startStallMeter starts a stallMeter, which runs until t ends.
func startStallMeter(t *testing.T) *stallMeter {
	t.Helper()
	m := &stallMeter{lastWake: time.Now()}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTimer(stallTick)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			m.mu.Lock()
			now := time.Now()
			if over := now.Sub(m.lastWake) - stallTick; over > 0 {
				m.held += over
			}
			m.lastWake = now
			m.mu.Unlock()
			tick.Reset(stallTick)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-stopped
	})
	return m
}

// heldUp is how long the meter has counted this process held up so far, the sleep it is in now
// included: when a frozen process wakes, the code it timed can end before the meter's goroutine
// has run again.
func (m *stallMeter) heldUp() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.held
	if over := time.Since(m.lastWake) - stallTick; over > 0 {
		held += over
	}
	return held
}

// lateSlack is how much later than its own deadline code a row times may end, beyond the time this
// process was held up meanwhile (stallMeter). What such code still does once its deadline has
// passed - a cleanup call to a fake in this process, the report of its failure - takes
// milliseconds at the machine's own speed, and the meter counts the time the machine took from it.
// Code whose own deadline ends late, by seconds, is past lateSlack.
const lateSlack = 5 * time.Second

// endsByItsDeadline waits for ch, the end of code started at start under a deadline of its own,
// deadline after start, and answers what ch received. The wait is bounded
// only by the moment every wait for a fact in these tests gives up (received), so a machine that
// held the code up does not fail the row; the row fails where the code was late on its own
// (requireOnTime). what names the code for the failure's text.
func endsByItsDeadline[T any](t *testing.T, meter *stallMeter, what string, start time.Time, deadline time.Duration, ch <-chan T) T {
	t.Helper()
	v, ok := received(t, ch)
	if !ok {
		t.Fatalf("%s had not ended %s after it started, past its %s deadline, %s before the test binary's deadline", what, time.Since(start).Round(time.Millisecond), deadline, waitMargin)
	}
	requireOnTime(t, meter, what, start, deadline)
	return v
}

// requireOnTime fails t where code started at start under a deadline of its own, deadline after
// start (0 for code that should not wait at all), and ended just now, ended later than that
// deadline by more than lateSlack plus the time meter, started before start, counted this process
// held up: that is code whose own deadline came late, or that waited for something it should not.
// It answers how long the code took. what names the code for the failure's text.
func requireOnTime(t *testing.T, meter *stallMeter, what string, start time.Time, deadline time.Duration) time.Duration {
	t.Helper()
	took := time.Since(start)
	held := meter.heldUp()
	if late := took - deadline - held; late > lateSlack {
		t.Fatalf("%s ended %s after it started, past its %s deadline; this process was held up %s of that, so the code was %s late on its own, more than %s",
			what, took.Round(time.Millisecond), deadline, held.Round(time.Millisecond), late.Round(time.Millisecond), lateSlack)
	}
	return took
}
