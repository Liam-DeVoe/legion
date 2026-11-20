package fataltest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Failure answers what a helper failed with, by Fatal or Fatalf, and the helper stops there, as it
// would under t.Fatal; a helper that returns answers "". The test itself goes on either way.
func TestFailureAnswersTheFailureAndStopsTheHelperThere(t *testing.T) {
	ranOn := false
	if got := Failure(t, func(t testing.TB) { t.Fatal("no lock for ", 7); ranOn = true }); got != "no lock for 7" {
		t.Fatalf("Failure of a Fatal answered %q, want %q", got, "no lock for 7")
	}
	if got := Failure(t, func(t testing.TB) { t.Fatalf("waited %s", time.Second); ranOn = true }); got != "waited 1s" {
		t.Fatalf("Failure of a Fatalf answered %q, want %q", got, "waited 1s")
	}
	if ranOn {
		t.Fatal("a helper ran on past its Fatal")
	}
	if got := Failure(t, func(testing.TB) {}); got != "" {
		t.Fatalf("Failure of a helper that returned answered %q, want none", got)
	}
}

// Start leaves the wait to its caller: done closes once the helper failed, and Message then reads
// what it failed with.
func TestStartClosesDoneOnceTheHelperFailed(t *testing.T) {
	release := make(chan struct{})
	recorder, done := Start(t, func(t testing.TB) {
		<-release
		t.Fatal("released")
	})
	select {
	case <-done:
		t.Fatal("done closed before the helper failed")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("done did not close within 10s of the helper's failure")
	}
	if got := recorder.Message(); got != "released" {
		t.Fatalf("Message answered %q, want %q", got, "released")
	}
}

// guardChildEnv makes TestBeforeTheBinaryDeadlineFiresItsMarginBeforeTheDeadline, run by itself
// as a child test binary, wait on the guard rather than start children of its own.
const guardChildEnv = "FATALTEST_GUARD_CHILD"

// BeforeTheBinaryDeadline fires its margin before the binary's deadline, never under -timeout 0,
// and fails the test by name when the deadline leaves no more than the margin. The test runs its
// own binary three times as a child, which waits on the guard: under a deadline two seconds past
// the margin the child fails by name about two seconds in, long before the binary's own timeout;
// under -timeout 0 it has no guard; under a deadline of the margin itself it is refused at once.
func TestBeforeTheBinaryDeadlineFiresItsMarginBeforeTheDeadline(t *testing.T) {
	if os.Getenv(guardChildEnv) != "" {
		guard := BeforeTheBinaryDeadline(t)
		if guard == nil {
			t.Log("no guard")
			return
		}
		<-guard
		t.Fatal("the guard fired")
	}
	child := func(timeout time.Duration) (string, time.Duration, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), BinaryDeadlineMargin+time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBeforeTheBinaryDeadlineFiresItsMarginBeforeTheDeadline$", "-test.v", "-test.timeout="+timeout.String())
		cmd.Env = append(os.Environ(), guardChildEnv+"=1")
		started := time.Now()
		out, err := cmd.CombinedOutput()
		return string(out), time.Since(started), err
	}
	failed := func(err error) bool {
		var exit *exec.ExitError
		return errors.As(err, &exit) && exit.ExitCode() == 1
	}

	out, took, err := child(BinaryDeadlineMargin + 2*time.Second)
	if !failed(err) || !strings.Contains(out, "the guard fired") || strings.Contains(out, "panic: test timed out") {
		t.Fatalf("a child whose deadline leaves the margin and 2s exited with %v, want it to fail by the guard:\n%s", err, out)
	}
	if took < 2*time.Second || took >= BinaryDeadlineMargin {
		t.Fatalf("the guard of a child whose deadline leaves the margin and 2s fired after the child ran %s, want about 2s", took)
	}

	if out, _, err := child(0); err != nil || !strings.Contains(out, "no guard") {
		t.Fatalf("a child under -timeout 0 exited with %v, want it to pass with no guard:\n%s", err, out)
	}

	if out, _, err := child(BinaryDeadlineMargin); !failed(err) || !strings.Contains(out, "not more than fataltest.BinaryDeadlineMargin") {
		t.Fatalf("a child whose deadline leaves no more than the margin exited with %v, want it refused by name:\n%s", err, out)
	}
}
