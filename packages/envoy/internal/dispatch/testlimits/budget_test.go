package testlimits

import (
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// A server that allows ServerConnections beyond its reserve gives no budget, so every process runs
// production's pool sizes; a smaller one gives each of MaxProcessesAtOnce processes its share
// beside SuiteHeadroom, rounded down; and one whose share is below the smallest pools is refused,
// naming the max_connections it needs. A Postgres at its default of 100, with the default reserve
// of 3, gives each process (100 - 3 - 20) / 4 = 19.
func TestPoolBudgetIsEachProcessesShareOfTheServer(t *testing.T) {
	const reserve = postgresSuperuserReserve
	for _, row := range []struct {
		name           string
		maxConnections int
		want           int
	}{
		{"a server sized for production's pools", ServerConnections + reserve, 0},
		{"a larger one", ServerConnections + reserve + 100, 0},
		{"one connection short of production's pools", ServerConnections + reserve - 1, (ServerConnections - 1 - SuiteHeadroom) / MaxProcessesAtOnce},
		{"Postgres's default of 100", 100, 19},
		{"the smallest server the suite runs on", MinServerConnections() + reserve, store.MinBudget()},
	} {
		got, err := PoolBudget(row.maxConnections, reserve)
		if err != nil || got != row.want {
			t.Errorf("%s (max_connections=%d): PoolBudget = %d, %v; want %d", row.name, row.maxConnections, got, err, row.want)
		}
	}

	smallest := MinServerConnections() + reserve
	for _, maxConnections := range []int{smallest - 1, 50, 0} {
		_, err := PoolBudget(maxConnections, reserve)
		if err == nil || !strings.Contains(err.Error(), "-c max_connections="+strconv.Itoa(smallest)+" ") {
			t.Errorf("max_connections=%d: PoolBudget's error = %v, want a refusal naming max_connections=%d", maxConnections, err, smallest)
		}
	}
}

// Every budget PoolBudget gives fits the server it was read from: MaxProcessesAtOnce processes at
// that budget, the suite's own SuiteHeadroom and the superuser reserve together are never more
// than the server allows, and each process can scale its pools to it (store.ScaledPoolSizes).
func TestEveryPoolBudgetFitsItsServer(t *testing.T) {
	for _, reserve := range []int{0, postgresSuperuserReserve, 10} {
		for maxConnections := MinServerConnections() + reserve; maxConnections < ServerConnections+reserve; maxConnections++ {
			budget, err := PoolBudget(maxConnections, reserve)
			if err != nil {
				t.Fatalf("max_connections=%d, reserve %d: %v", maxConnections, reserve, err)
			}
			if opened := MaxProcessesAtOnce*budget + SuiteHeadroom + reserve; opened > maxConnections {
				t.Fatalf("max_connections=%d, reserve %d: a budget of %d opens %d with the suite and the reserve",
					maxConnections, reserve, budget, opened)
			}
			// PoolBudget refuses a share below store.MinBudget, the least ScaledPoolSizes takes, so
			// this holds while the two agree on that floor; it is here to fail if they drift apart.
			if _, err := store.ScaledPoolSizes(budget); err != nil {
				t.Fatalf("max_connections=%d, reserve %d: a budget of %d: %v", maxConnections, reserve, budget, err)
			}
		}
	}
}
