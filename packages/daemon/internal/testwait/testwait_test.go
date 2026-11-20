package testwait

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/testdeadline"
)

// childTimeout is the -timeout of the child test binary: it leaves its Eventually about six
// seconds after testdeadline.Cleanup, where a wait of a fixed minute would run on to the minute.
const childTimeout = testdeadline.Cleanup + 6*time.Second

// Eventually waits as long as the test's deadline leaves less testdeadline.Cleanup, not a fixed
// span: in a child binary run with childTimeout, a condition that never holds fails the child,
// naming the wait, at the deadline less the reserve. The child says how far from that instant its
// wait ended; it cannot end before it, and the slack after it is only the scheduler's.
func TestEventuallyWaitsAsLongAsTheTestsDeadlineLeavesLessTheCleanupReserve(t *testing.T) {
	if os.Getenv("TESTWAIT_EVENTUALLY_CHILD") == "1" {
		deadline, ok := t.Deadline()
		if !ok {
			t.Fatal("the child has no deadline; it runs only under its parent's -timeout")
		}
		t.Cleanup(func() {
			t.Logf("the wait ended %s after the deadline less the reserve", time.Since(deadline.Add(-testdeadline.Cleanup)))
		})
		Eventually(t, "a condition that never holds", func() bool { return false })
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.timeout="+childTimeout.String())
	child.Env = append(os.Environ(), "TESTWAIT_EVENTUALLY_CHILD=1")
	out, err := child.CombinedOutput()
	if err == nil {
		t.Fatalf("the child passed, want it to fail naming its wait:\n%s", out)
	}
	if !strings.Contains(string(out), "timed out waiting for a condition that never holds") {
		t.Fatalf("the child did not name its wait:\n%s", out)
	}
	m := regexp.MustCompile(`the wait ended (\S+) after the deadline less the reserve`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("the child did not say when its wait ended:\n%s", out)
	}
	after, err := time.ParseDuration(string(m[1]))
	if err != nil {
		t.Fatalf("read the child's %q: %v", m[1], err)
	}
	if after < -100*time.Millisecond || after > 5*time.Second {
		t.Fatalf("the child's wait ended %s after its deadline less testdeadline.Cleanup, want at that instant (a wait of its own span ends elsewhere):\n%s", after, out)
	}
}
