package testdeadline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// noDeadline is a testing.TB whose test has no deadline, as under `go test -timeout 0`.
type noDeadline struct{ testing.TB }

// fakeDeadline is a testing.TB whose test binary's deadline is at, and whose Errorf and Fatalf
// record their message and return, so a test can read what a helper refused.
type fakeDeadline struct {
	testing.TB
	at      time.Time
	refused []string
}

func (f *fakeDeadline) Deadline() (time.Time, bool) { return f.at, true }
func (f *fakeDeadline) Helper()                     {}
func (f *fakeDeadline) Errorf(format string, args ...any) {
	f.refused = append(f.refused, fmt.Sprintf(format, args...))
}
func (f *fakeDeadline) Fatalf(format string, args ...any) {
	f.refused = append(f.refused, fmt.Sprintf(format, args...))
}

// A call a test only needs answered is given the test's own deadline less Cleanup; with no test
// deadline it has none but its parent's, and its parent's end still ends it.
func TestAnsweredTakesTheTestsDeadlineLessCleanup(t *testing.T) {
	ctx, cancel := Answered(t, context.Background())
	defer cancel()
	got, has := ctx.Deadline()
	if deadline, set := t.Deadline(); set {
		if !has || !got.Equal(deadline.Add(-Cleanup)) {
			t.Errorf("Answered's deadline = %v (set %t), want the test's deadline %v less Cleanup", got, has, deadline)
		}
	} else if has {
		t.Errorf("with no test deadline Answered gave the call the deadline %v", got)
	}
	parent, stop := context.WithCancel(context.Background())
	none, cancelNone := Answered(noDeadline{t}, parent)
	defer cancelNone()
	if deadline, has := none.Deadline(); has {
		t.Errorf("a test with no deadline gave the call the deadline %v, want none", deadline)
	}
	stop()
	if none.Err() == nil {
		t.Error("the parent's end left the call's context live")
	}
}

// A wait for a container the test starts is given what the test's own deadline leaves, less
// Cleanup, and with no test deadline NoBound.
func TestStartupBoundTakesTheTestsDeadlineLessCleanup(t *testing.T) {
	before := time.Now()
	given := StartupBound(t)
	after := time.Now()
	if deadline, set := t.Deadline(); set {
		end := deadline.Add(-Cleanup)
		if given > end.Sub(before) || given < end.Sub(after) {
			t.Errorf("StartupBound = %s, want what the test's deadline leaves less Cleanup, %s to %s", given, end.Sub(after), end.Sub(before))
		}
	} else if given != NoBound {
		t.Errorf("with no test deadline StartupBound = %s, want NoBound", given)
	}
	if none := StartupBound(noDeadline{t}); none != NoBound {
		t.Errorf("a test with no deadline was given %s, want NoBound", none)
	}
}

// A test binary whose -timeout leaves its calls no time after Cleanup is refused once, by name,
// the -timeout, the reserve and the shortfall, however many calls ask: Answered's call gets a
// context already ended and StartupBound's wait the least bound there is. A -timeout that leaves
// time is not refused.
func TestATimeoutTooShortForTheReserveIsRefusedOnce(t *testing.T) {
	start := time.Now()
	short := &fakeDeadline{TB: t, at: start.Add(Cleanup / 2)}
	var once sync.Once
	ctx, cancel := answered(short, context.Background(), start, &once)
	defer cancel()
	again, cancelAgain := answered(short, context.Background(), start, &once)
	defer cancelAgain()
	given := startupBound(short, start, &once)
	if len(short.refused) != 1 {
		t.Fatalf("a -timeout of %s, inside the %s reserve, was refused %d times over three calls (%q), want once", Cleanup/2, Cleanup, len(short.refused), short.refused)
	}
	for _, want := range []string{"testdeadline: the test binary's -timeout, about 44s,", Cleanup.String() + " testdeadline.Cleanup", "44.415s short; run the test with a -timeout"} {
		if !strings.Contains(short.refused[0], want) {
			t.Errorf("the refusal %q does not name %q", short.refused[0], want)
		}
	}
	if ctx.Err() == nil || again.Err() == nil {
		t.Errorf("a refused call's context is live (first %v, second %v)", ctx.Err(), again.Err())
	}
	if given <= 0 {
		t.Errorf("the refused wait was given %s, a bound testcontainers reads as none or as past", given)
	}
	roomy := &fakeDeadline{TB: t, at: start.Add(Cleanup + time.Minute)}
	var roomyOnce sync.Once
	ctx, cancel = answered(roomy, context.Background(), start, &roomyOnce)
	defer cancel()
	if bound := startupBound(roomy, start, &roomyOnce); len(roomy.refused) != 0 || ctx.Err() != nil || bound <= 0 {
		t.Errorf("a -timeout a minute past the reserve was refused (%q), gave an ended context (%v) or a bound of %s", roomy.refused, ctx.Err(), bound)
	}
}

// Late in a long run, when the test binary's deadline leaves a call nothing after Cleanup, nothing
// is refused: Answered's context has already ended, with the deadline's own error, so the call
// fails at its own call site with its own message, and StartupBound's wait gets one nanosecond.
// A refusal there would replace the message of a test that is already failing, on its failure,
// its cleanup or a goroutine of it, with one about the -timeout.
func TestALateCallFailsWithItsOwnErrorAndNoRefusal(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	late := &fakeDeadline{TB: t, at: time.Now().Add(Cleanup / 2)}
	var once sync.Once
	ctx, cancel := answered(late, context.Background(), start, &once)
	defer cancel()
	given := startupBound(late, start, &once)
	if len(late.refused) != 0 {
		t.Errorf("a call late in a run whose -timeout was %s was refused: %q", late.at.Sub(start).Round(time.Second), late.refused)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("the late call's context ended with %v, want its deadline's own error", ctx.Err())
	}
	if given != time.Nanosecond {
		t.Errorf("the late wait was given %s, want one nanosecond", given)
	}
}
