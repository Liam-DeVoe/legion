package testwait

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// eventuallyChildEnv tells the re-executed test binary to run the child tests below, which skip in
// an ordinary run. The parent names the child it wants with -test.run.
const eventuallyChildEnv = "TESTWAIT_EVENTUALLY_CHILD"

// eventuallyChildTimeout is the -test.timeout of the child whose wait gives up: 35 s, so the wait
// gives up 5 s in, 30 s before the deadline. Eventually's minute, clamped to that deadline less a
// second, gave up 34 s in, with a line of its own.
const eventuallyChildTimeout = "35s"

// Eventually returns once its condition holds, asking it until then and no more.
func TestEventuallyReturnsOnceItsConditionHolds(t *testing.T) {
	asks := 0
	Eventually(t, "the third answer", func() bool { asks++; return asks == 3 })
	if asks != 3 {
		t.Fatalf("Eventually asked its condition %d times, want 3: the third answer is the first that held", asks)
	}
}

// Under a short -timeout Eventually gives up 30 s before the deadline, before Go's alarm: it
// writes its line to stderr and fails the test with the same text, as Receive does.
func TestEventuallyGivesUpTheMarginBeforeTheDeadline(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestEventuallyGivesUpChild$", "-test.timeout="+eventuallyChildTimeout, "-test.v")
	child.Env = append(os.Environ(), eventuallyChildEnv+"=1")
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	if err := child.Run(); err == nil {
		t.Fatalf("the child was meant to fail; it passed:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	if strings.Contains(stdout.String()+stderr.String(), "panic: test timed out") {
		t.Fatalf("the child's wait ran into the binary's deadline rather than giving up before it:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	begins := "testwait: TestEventuallyGivesUpChild: gave up waiting for a condition that never holds after "
	ends := ", 30s before the test binary's deadline"
	var line string
	for _, written := range strings.Split(stderr.String(), "\n") {
		if strings.HasPrefix(written, begins) && strings.HasSuffix(written, ends) {
			line = written
		}
	}
	if line == "" {
		t.Fatalf("the child wrote no stderr line %q…%q; its stderr:\n%s", begins, ends, &stderr)
	}
	if !strings.Contains(stdout.String(), line+"\n") {
		t.Fatalf("the child failed no test with %q; its output:\n%s", line, &stdout)
	}
}

// Under -timeout 0 there is no deadline: Eventually asks until its condition holds, however many
// false answers come first.
func TestUnderNoTimeoutEventuallyHasNoEnd(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestEventuallyNoDeadlineChild$", "-test.timeout=0", "-test.v")
	child.Env = append(os.Environ(), eventuallyChildEnv+"=1")
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--- PASS: TestEventuallyNoDeadlineChild (") {
		t.Fatalf("under -timeout 0 the child did not pass: %v\n%s", err, output)
	}
}

// TestEventuallyGivesUpChild is the child of TestEventuallyGivesUpTheMarginBeforeTheDeadline, run
// under -timeout 35s, so its wait gives up 5 s in. It runs only when the parent re-executes this
// binary.
func TestEventuallyGivesUpChild(t *testing.T) {
	if os.Getenv(eventuallyChildEnv) == "" {
		t.Skip(eventuallyChildEnv + " is not set: this test is the child of TestEventuallyGivesUpTheMarginBeforeTheDeadline")
	}
	Eventually(t, "a condition that never holds", func() bool { return false })
}

// TestEventuallyNoDeadlineChild is the child of TestUnderNoTimeoutEventuallyHasNoEnd, run under
// -timeout 0: the condition is false twice and true the third time, and a wait that gave up on a
// false answer fails it. It runs only when the parent re-executes this binary.
func TestEventuallyNoDeadlineChild(t *testing.T) {
	if os.Getenv(eventuallyChildEnv) == "" {
		t.Skip(eventuallyChildEnv + " is not set: this test is the child of TestUnderNoTimeoutEventuallyHasNoEnd")
	}
	asks := 0
	Eventually(t, "the third answer", func() bool { asks++; return asks == 3 })
	if asks != 3 {
		t.Errorf("under -timeout 0 Eventually asked its condition %d times, want 3: it returned before the third answer, the first that held", asks)
	}
}
