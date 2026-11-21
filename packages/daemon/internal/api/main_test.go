package api

import (
	"fmt"
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/testpg"
)

// TestMain takes out of the process every variable that could lead the git, gh and bash this
// package's tests run to a repository, remote or credential not the test's own
// (fixtureenv.MustGuard).
// Nothing in this package re-executes the test binary, so there is no helper-child branch before
// the guard. It then drops the migrated template testStore clones its databases from
// (testpg.Main), which wraps the run: the guard decides the environment the tests and the
// template's migration both run in.
//
// It ends with the two censuses of the pairs this run made: the answer id each door wrote, held
// to the answer table (routeAnswerProblems), and the type each door decoded a body into, held to
// the table of plugin request types (requestDecodedProblems). They are read here and nowhere
// else because no one case drives every door: the pairs are made by the tests of the routes
// themselves.
func TestMain(m *testing.M) {
	fixtureenv.MustGuard()
	os.Exit(testpg.Main("legion_api_test", func() int { return withRouteCensus(m.Run()) }))
}

// withRouteCensus is code, or 1 where a write of this run made a pair the answer table does not
// name or a decode made a pair the request table does not name. A run of one test holds that
// test's own pairs to the tables and no others, which reports no pair the run never made.
func withRouteCensus(code int) int {
	problems := append(routeAnswerProblems(), requestDecodedProblems()...)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "FAIL: "+problem)
	}
	if len(problems) > 0 {
		return 1
	}
	return code
}
