package testwait

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// remainingChildEnv tells the re-executed test binary to run the child tests below, which skip in
// an ordinary run. The parent names the child it wants with -test.run.
const remainingChildEnv = "TESTWAIT_REMAINING_CHILD"

// remainingCallingEnv names the file TestRemainingWithinTheMarginChild writes just before it calls
// Remaining: the mark that it reached the call before its binary's alarm.
const remainingCallingEnv = "TESTWAIT_REMAINING_CALLING"

// remainingChildTimeout is the -test.timeout of the child that starts within the margin: 10 s,
// less than giveUpMargin, so its first call of Remaining has less than the margin left.
const remainingChildTimeout = 10 * time.Second

// Remaining is the time until the test binary's deadline less giveUpMargin: read just before the
// call that time is no less than Remaining's answer, and read just after it no more. The reads and
// the answer come from one monotonic clock, so the bounds need no tolerance.
func TestRemainingIsTheTimeToTheDeadlineLessTheMargin(t *testing.T) {
	deadline, ok := t.Deadline()
	if !ok {
		t.Skip("the binary runs under -timeout 0, so there is no deadline to measure; TestUnderNoTimeoutRemainingHasNoEnd covers that")
	}
	most := time.Until(deadline) - giveUpMargin
	got := Remaining(t)
	least := time.Until(deadline) - giveUpMargin
	if got < least || got > most {
		t.Fatalf("Remaining = %s, want between %s and %s: the time to the deadline %s less %s",
			got, least, most, deadline.Format(time.RFC3339Nano), giveUpMargin)
	}
}

// Under -timeout 0 the test binary has no deadline, and Remaining is a span no wait reaches.
func TestUnderNoTimeoutRemainingHasNoEnd(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestRemainingNoDeadlineChild$", "-test.timeout=0", "-test.v")
	child.Env = append(os.Environ(), remainingChildEnv+"=1")
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--- PASS: TestRemainingNoDeadlineChild (") {
		t.Fatalf("under -timeout 0 the child did not pass: %v\n%s", err, output)
	}
}

// TestRemainingNoDeadlineChild is the child of TestUnderNoTimeoutRemainingHasNoEnd, run under
// -timeout 0: Remaining is unboundedRemaining, and a deadline that far off is still after now, as
// a read deadline or a timer built from it needs. It runs only when the parent re-executes this
// binary.
func TestRemainingNoDeadlineChild(t *testing.T) {
	if os.Getenv(remainingChildEnv) == "" {
		t.Skip(remainingChildEnv + " is not set: this test is the child of TestUnderNoTimeoutRemainingHasNoEnd")
	}
	if deadline, ok := t.Deadline(); ok {
		t.Fatalf("the child has the deadline %s; it was meant to run under -timeout 0", deadline.Format(time.RFC3339Nano))
	}
	got := Remaining(t)
	if got != unboundedRemaining {
		t.Fatalf("under -timeout 0 Remaining = %s, want %s, a span no wait reaches", got, unboundedRemaining)
	}
	if now := time.Now(); !now.Add(4 * got).After(now) {
		t.Fatalf("now plus four times Remaining's %s is %s, not after now %s", got, now.Add(4*got), now)
	}
}

// unboundedRemaining survives the small factors its callers multiply it by (a consumer's handler
// limit is intake.WindowsPerLimit ack waits): every product up to seven times it is still
// positive. 1 << 62 times 4 wraps an int64 to 0, which is what this row exists to catch.
func TestUnboundedRemainingStaysPositiveTimesASmallFactor(t *testing.T) {
	for factor := time.Duration(1); factor <= 7; factor++ {
		if product := factor * unboundedRemaining; product <= 0 {
			t.Errorf("%d times unboundedRemaining (%d ns) is %s, want a positive span", factor, int64(unboundedRemaining), product)
		}
	}
}

// Called with less than the margin left before the deadline, Remaining fails the test by name, on
// one line naming the deadline and the margin, and returns nothing a wait could run on: the child
// runs under -timeout 10s, and the line it logs after Remaining returns never comes.
//
// The child's alarm is counted from its start, so a loaded machine can hold the child past it
// before the child reaches its call of Remaining, and that run, which the alarm ended, shows
// nothing of Remaining. The child marks that it reached the call (remainingCallingEnv) just before
// it calls, and only a run that made the mark is judged: a run whose alarm came first is run
// again until a run of the right order comes, within a minute. A run with no mark that its alarm
// did not end cannot have happened, and fails at once.
func TestRemainingWithinTheMarginFailsTheTestByName(t *testing.T) {
	within := Bounded(t, time.Minute, remainingChildTimeout+5*time.Second)
	until := time.Now().Add(within)
	for runs := 1; ; runs++ {
		calling := filepath.Join(t.TempDir(), "calling")
		child := exec.Command(os.Args[0], "-test.run=^TestRemainingWithinTheMarginChild$", "-test.timeout="+remainingChildTimeout.String(), "-test.v")
		child.Env = append(os.Environ(), remainingChildEnv+"=1", remainingCallingEnv+"="+calling)
		output, err := child.CombinedOutput()
		text := string(output)
		if _, statErr := os.Stat(calling); statErr != nil {
			if !strings.Contains(text, "panic: test timed out") {
				t.Fatalf("run %d: the child never reached its call of Remaining, and its alarm did not end it (%v):\n%s", runs, err, text)
			}
			if time.Now().Before(until) {
				t.Logf("run %d: the child's alarm came before it reached its call of Remaining; run again", runs)
				continue
			}
			t.Fatalf("no run of the right order in %d runs over %s: in each, the child's alarm came before it reached its call of Remaining", runs, within)
		}
		if err == nil || !strings.Contains(text, "--- FAIL: TestRemainingWithinTheMarginChild (") {
			t.Fatalf("run %d: the child under -timeout %s was meant to fail; it did not (%v):\n%s", runs, remainingChildTimeout, err, text)
		}
		if strings.Contains(text, "panic: test timed out") {
			t.Fatalf("run %d: the child ran into the binary's deadline rather than failing at once:\n%s", runs, text)
		}
		if strings.Contains(text, "Remaining returned") {
			t.Fatalf("run %d: Remaining returned within the margin rather than failing the test:\n%s", runs, text)
		}
		var named string
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "testwait.Remaining: the test binary's deadline ") {
				named = line
			}
		}
		if named == "" || !strings.Contains(named, "less than the 30s margin") || !strings.Contains(named, "remaining_test.go:") {
			t.Fatalf("run %d: the child failed with no line from Remaining naming the deadline and the 30s margin at its caller's line:\n%s", runs, text)
		}
		return
	}
}

// TestRemainingWithinTheMarginChild is the child of TestRemainingWithinTheMarginFailsTheTestByName,
// run under -timeout 10s, so less than the margin is left at once. It runs only when the parent
// re-executes this binary.
func TestRemainingWithinTheMarginChild(t *testing.T) {
	if os.Getenv(remainingChildEnv) == "" {
		t.Skip(remainingChildEnv + " is not set: this test is the child of TestRemainingWithinTheMarginFailsTheTestByName")
	}
	if err := os.WriteFile(os.Getenv(remainingCallingEnv), nil, 0o600); err != nil {
		t.Fatalf("mark the call of Remaining: %v", err)
	}
	got := Remaining(t)
	t.Logf("Remaining returned %s", got)
}
