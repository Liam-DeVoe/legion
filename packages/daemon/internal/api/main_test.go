package api

import (
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/testpg"
)

// TestMain takes out of the process every variable that could lead the git, gh and bash this
// package's tests run to a repository, remote or credential not the test's own
// (fixtureenv.MustGuard). The LEGION_E2E_*_APP_* inputs the App lib reads (real_github_test.go)
// need no entry: fixtureenv's own testInput keeps every LEGION_TEST_ and LEGION_E2E name by prefix.
// Nothing in this package re-executes the test binary, so there is no helper-child branch before
// the guard. It then drops the migrated template testStore clones its databases from
// (testpg.Main), which wraps the run: the guard decides the environment the tests and the
// template's migration both run in.
func TestMain(m *testing.M) {
	fixtureenv.MustGuard()
	os.Exit(testpg.Main("legion_api_test", m.Run))
}
