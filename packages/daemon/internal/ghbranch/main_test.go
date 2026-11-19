package ghbranch

import (
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
)

// TestMain takes every LEGION_ variable out of the test process (fixtureenv.MustGuard). Create
// runs the leak guard, which reads LEGION_PRIVATE_NAME_RULES_FILE wherever its caller hands it no
// rules, and every Legion role in a pod holds that pointer to the deployment's own rule file, so a
// test run there would otherwise run the deployment's rules in place of the fixture's
// (leakguardtest), or in place of none.
func TestMain(m *testing.M) {
	fixtureenv.MustGuard()
	os.Exit(m.Run())
}
