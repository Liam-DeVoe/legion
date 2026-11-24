package testwait

import (
	"testing"
	"testing/synctest"
	"time"
)

// This file holds the shared wait's forms for a testing/synctest bubble: Idle, the proof of an
// absence there (the package doc's "Proving an absence") for code that a timer of its own may let
// act late, and Bubbled, the t a wait of this package takes inside a bubble.

// Idle proves an absence in a synctest bubble past a timer of the code's own: d is the caller's
// span, which outlasts that timer, and why names the timer and says why d outlasts it; an empty
// why fails the test, as Span's and Quiet's does. This package holds no span of its own. Idle runs
// on a goroutine of a bubble: outside one, synctest.Wait panics.
//
// It waits for three things in turn. First, every other goroutine of the bubble is durably blocked
// (synctest.Wait). Then d passes on the bubble's clock, which moves only while every goroutine of
// the bubble is durably blocked, and then only to the next moment a timer is due: the timers due
// within d fire in their order, timers armed during d among them, and what each wakes runs until
// it blocks again before the clock moves on. Last, every other goroutine is durably blocked once
// more (synctest.Sleep), so what a timer due at the very end of d woke has run too. Idle returns
// then, with the bubble's clock d past where it found it.
//
// An absence read after Idle proves that the bubble's goroutines, left to themselves for d on the
// bubble's clock with every timer due within it fired, did not make the thing happen. It proves
// nothing past d: a timer of the code's own due later lets the code act after the read, which is
// why the caller names the timer d outlasts. It reaches no goroutine outside the bubble, which
// synctest does not wait for. A goroutine of the bubble blocked where it is not durably blocked (a
// network socket, a system call, a sync.Mutex) holds Idle until it blocks durably or ends.
func Idle(t testing.TB, d time.Duration, why string) {
	t.Helper()
	requireWhy(t, "Idle", why, "which timer of the code's own its span outlasts, and why")
	synctest.Wait()
	synctest.Sleep(d)
}

// Bubbled is t, the *testing.T that synctest.Test hands its function, with the Deadline of outer,
// the test that started the bubble, for a wait of this package to take inside the bubble, where
// t's own Deadline panics. outer's deadline is on the real clock, and the bubble's clock starts at
// midnight UTC 2000-01-01, so a wait in the bubble gives up only once the bubble's clock has run
// decades. A wait that asks at the package's pace (Until, Eventually) asks until its fact comes,
// as under -timeout 0, and one whose fact never comes holds the test until go test's alarm ends
// the binary. A Receive whose value never comes gives up, naming what it waited for, once nothing
// else in the bubble can run, since the bubble's clock then moves to its timer. Under -timeout 0
// outer names no deadline, and no wait in the bubble has a timer.
func Bubbled(t, outer *testing.T) testing.TB {
	return bubbled{T: t, outer: outer}
}

// bubbled is what Bubbled returns: t with outer's Deadline.
type bubbled struct {
	*testing.T
	outer *testing.T
}

func (b bubbled) Deadline() (time.Time, bool) { return b.outer.Deadline() }
