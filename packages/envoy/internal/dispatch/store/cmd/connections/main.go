// Command connections prints testlimits.ServerConnections: the connections a Dispatch test lane's
// Postgres must allow beyond its superuser reserve (superuser_reserved_connections) for every
// Dispatch process its tests start to run at production's pool sizes.
//
// .github/scripts/pg-connection-ceiling.sh runs it to size a CI lane's server from the code, and
// adds only that server's own superuser reserve, so no workflow holds the number. A lane so sized
// tests the pools production runs; a smaller server would have the tests scale them
// (testlimits.PoolBudget).
package main

import (
	"fmt"

	"github.com/sjawhar/envoy/internal/dispatch/testlimits"
)

func main() {
	fmt.Println(testlimits.ServerConnections)
}
