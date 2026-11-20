// Package fataltest runs a test helper that may fail its test with Fatal or Fatalf, and answers
// what the helper failed with rather than failing the test: so a test can require that a helper
// fails, and what its failure says, where the helper's t.Fatal would end the test itself. The
// helper runs on a goroutine of its own over a Recorder of the test, which ends that goroutine at
// the failure as t.Fatal does (runtime.Goexit), so the helper stops where it failed. Failure waits
// for the goroutine; Start leaves the wait to its caller, for a test that bounds it itself, which
// BeforeTheBinaryDeadline can do when nothing but the code under test ends the wait. It is a
// package of the module's root rather than an internal one so that the Legion daemon's module,
// which cannot import envoy's internal packages, shares this one copy.
package fataltest

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Recorder is t with Fatal and Fatalf recorded and their goroutine ended, as t.Fatal does; every
// other method is t's own.
type Recorder struct {
	testing.TB
	message string
}

// Fatal records args, as t.Fatal would print them, and ends the calling goroutine.
func (r *Recorder) Fatal(args ...any) {
	r.message = fmt.Sprint(args...)
	runtime.Goexit()
}

// Fatalf records the formatted failure, as t.Fatalf would print it, and ends the calling goroutine.
func (r *Recorder) Fatalf(format string, args ...any) {
	r.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// Message is what Fatal or Fatalf recorded, "" when neither ran. Read it once the goroutine that
// fails through r has ended (Start's done).
func (r *Recorder) Message() string { return r.message }

// Start runs call over a Recorder of t on a goroutine of its own, and answers the recorder and a
// channel closed once call returned or failed.
func Start(t testing.TB, call func(testing.TB)) (*Recorder, <-chan struct{}) {
	recorder := &Recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(recorder)
	}()
	return recorder, done
}

// Failure runs call as Start does and waits for it to end: what it failed with, or "" when it
// returned.
func Failure(t testing.TB, call func(testing.TB)) string {
	recorder, done := Start(t, call)
	<-done
	return recorder.Message()
}

// BinaryDeadlineMargin is how long before the test binary's deadline BeforeTheBinaryDeadline
// fires: the time a failing test needs to report by name and run its cleanups, which close its
// connections and drop its databases, before go test's own timeout ends the binary.
const BinaryDeadlineMargin = 30 * time.Second

// BeforeTheBinaryDeadline fires BinaryDeadlineMargin before the test binary's deadline, and never
// when the binary runs without one (-timeout 0). A test that waits for a fact only the code under
// test brings about selects on it, so a wait that never ends fails by name rather than as the
// binary's timeout panic. It bounds no measurement. A deadline that leaves no more than the margin
// fails t at once, by name.
func BeforeTheBinaryDeadline(t *testing.T) <-chan time.Time {
	t.Helper()
	deadline, bounded := t.Deadline()
	if !bounded {
		return nil
	}
	left := time.Until(deadline) - BinaryDeadlineMargin
	if left <= 0 {
		t.Fatalf("the test binary's deadline leaves %s, not more than fataltest.BinaryDeadlineMargin (%s): raise -timeout",
			time.Until(deadline).Round(time.Millisecond), BinaryDeadlineMargin)
	}
	timer := time.NewTimer(left)
	t.Cleanup(func() { timer.Stop() })
	return timer.C
}
