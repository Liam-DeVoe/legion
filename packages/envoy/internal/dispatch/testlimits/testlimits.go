// Package testlimits states, once, how many connections a Dispatch test suite needs from the
// Postgres it runs against, so that no CI lane, no pod's test server and no test holds a number of
// its own.
//
// Three places read it. .github/scripts/pg-connection-ceiling.sh raises a CI lane's server to
// ServerConnections plus that server's superuser reserve, through
// internal/dispatch/store/cmd/connections, so every lane runs the Dispatch processes it starts at
// production's pool sizes. packages/daemon/docker/legion-testpg starts a pod's test server at that
// size, as a literal that testlimits_test.go holds to ServerConnections. And every start of a
// Dispatch process in cmd/dispatch's tests is checked against MaxProcessesAtOnce and against the
// server it meets (cmd/dispatch/connection_ceiling_test.go): a server smaller than
// ServerConnections gives each process a connection budget its pools scale to (PoolBudget), and a
// server too small for even the smallest pools refuses the start by name, rather than as a 53300
// refusal inside a room, a lease or a socket.
package testlimits

import (
	"fmt"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// MaxProcessesAtOnce is the most Dispatch server processes one test binary runs at once against
// one Postgres. The count is the binary's, not one test's: one `go test` binary is what one lane's
// server serves, and parallel root tests in it share that server. A process counts from its start
// until it exits, which is what it holds connections for. cmd/dispatch/connection_ceiling_test.go
// refuses the start of one more process than this and names this constant.
//
// It is the limit the guard enforces, measured with that same count over the cmd/dispatch test
// binaries whose rows ran: the twobuild binary reaches 4, in TestTwoBuildsAnswerEachOthersCLI, and
// so do the binaries with no tag and with multiprocess, in
// TestEveryProcessAHundredConnectionServerRunsStaysWithinItsBudget, which runs this many on purpose;
// their other rows reach 3. The memory binary's measured 1 is a floor, not its peak: in a gVisor
// pod each of its rows fails at /proc/<pid>/clear_refs after its first start, and its code bounds it
// at two, a writer and a reader (cmd/dispatch/memory_test.go,
// TestTheHeaviestStoredDocumentsStayWithinTheMemoryBound). A binary that comes to run more is
// refused by name, and this number is raised to what it runs.
const MaxProcessesAtOnce = 4

// RequiredConnections is what MaxProcessesAtOnce Dispatch processes may open together at
// production's pool sizes.
const RequiredConnections = MaxProcessesAtOnce * store.ConnectionsPerProcess

// SuiteHeadroom is the connections a test suite opens outside any Dispatch process: each
// package's own pool and the harness's reads. Nothing counts these at run time. A lane whose
// Postgres log shows a 53300 refusal that no guard refused first needs more.
const SuiteHeadroom = 20

// ServerConnections is what a test server must allow beyond its superuser reserve
// (superuser_reserved_connections) for every Dispatch process a suite starts to run at production's
// pool sizes. A smaller server scales them (PoolBudget).
const ServerConnections = RequiredConnections + SuiteHeadroom

// MinServerConnections is the fewest connections a test server must allow beyond its superuser
// reserve for a suite to run at all: MaxProcessesAtOnce processes with every pool at its smallest
// (store.MinBudget), beside SuiteHeadroom.
func MinServerConnections() int { return MaxProcessesAtOnce*store.MinBudget() + SuiteHeadroom }

// PoolBudget is how many connections each Dispatch process a test starts may open on a server that
// allows maxConnections with reserved of them kept for superusers. It is zero on a server that
// allows ServerConnections beyond its reserve, as every CI lane's does: there every process runs
// production's pool sizes. On a smaller server, such as a pod's that allows Postgres's default of
// 100, it is that server's share for one of MaxProcessesAtOnce processes beside SuiteHeadroom,
// (maxConnections - reserved - SuiteHeadroom) / MaxProcessesAtOnce rounded down, which the test
// harness hands each process it starts (DISPATCH_TEST_POOL_BUDGET) and the process scales its pools
// to (store.OpenWithin). A share below store.MinBudget is refused, and the error names the
// max_connections the server needs.
func PoolBudget(maxConnections, reserved int) (int, error) {
	if maxConnections-reserved >= ServerConnections {
		return 0, nil
	}
	budget := (maxConnections - reserved - SuiteHeadroom) / MaxProcessesAtOnce
	if budget >= store.MinBudget() {
		return budget, nil
	}
	return 0, fmt.Errorf("a Postgres that allows %d connections with %d of them reserved for superusers leaves each "+
		"of testlimits.MaxProcessesAtOnce=%d Dispatch processes %d beside the %d the suite opens itself "+
		"(testlimits.SuiteHeadroom), and one process needs %d with every pool at its smallest (store.MinBudget). "+
		"Restart the server with -c max_connections=%d or more (testlimits.MinServerConnections plus the reserve). "+
		"From %d (testlimits.ServerConnections plus the reserve), as every CI lane's server allows "+
		"(.github/scripts/pg-connection-ceiling.sh), every process runs production's pool sizes; below that, "+
		"each runs pools scaled to its share",
		maxConnections, reserved, MaxProcessesAtOnce, max(budget, 0), SuiteHeadroom, store.MinBudget(),
		MinServerConnections()+reserved, ServerConnections+reserved)
}
