package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/testlimits"
)

// postgresDefaultMaxConnections is the max_connections a Postgres starts with when nothing sets
// it: what a developer's own server, or a pod's test server started before legion-testpg was sized
// from testlimits, allows.
const postgresDefaultMaxConnections = 100

// budgetSampleEvery is how often the row below reads pg_stat_activity.
const budgetSampleEvery = 100 * time.Millisecond

// testlimits.MaxProcessesAtOnce Dispatch processes, each with its pools scaled to the budget a
// Postgres of the default 100 connections gives it (testlimits.PoolBudget: 19 with the default
// superuser reserve of 3), boot and serve load at once, and none opens more than that budget:
// pg_stat_activity, read every 100 ms from before the first start until the load has ended, never
// counts more connections on one process's database than its budget, nor more on all of them
// together than the 100 less the reserve and the suite's own testlimits.SuiteHeadroom. Each
// process has a database of its own, so a database's count is its process's.
//
// On a server that small the connection guard gives every process that budget itself
// (requireConnectionCeiling); on a larger one, as every CI lane's, the row gives it, so the claim is
// held wherever the suite runs. The load is what reaches each pool: every process boots, starting
// every loop, holds a live socket on the event feed and a document's room open in an editor that
// types into it, and serves many more requests at once than its shared pool has connections -
// issue reads, renames and document reads - each of which must succeed.
func TestEveryProcessAHundredConnectionServerRunsStaysWithinItsBudget(t *testing.T) {
	ctx := context.Background()
	urls := make([]string, testlimits.MaxProcessesAtOnce)
	names := make([]string, testlimits.MaxProcessesAtOnce)
	for i := range urls {
		database := storetest.Open(t)
		urls[i], names[i] = databaseURLOf(t, database), database.Pool.Config().ConnConfig.Database
		// The test reads nothing through it, and an idle connection of its own would count
		// against the process that serves the database.
		database.Pool.Close()
	}
	sampler, err := pgx.Connect(ctx, urls[0])
	if err != nil {
		t.Fatalf("connect the sampler: %v", err)
	}
	t.Cleanup(func() { _ = sampler.Close(context.Background()) })
	reserve := serverInt(t, sampler, "superuser_reserved_connections")
	budget, err := testlimits.PoolBudget(postgresDefaultMaxConnections, reserve)
	if err != nil {
		t.Fatalf("the budget of a %d-connection server: %v", postgresDefaultMaxConnections, err)
	}
	ceiling := postgresDefaultMaxConnections - reserve - testlimits.SuiteHeadroom
	t.Logf("a %d-connection server with %d reserved gives each of %d processes %d connections; %d for all of them",
		postgresDefaultMaxConnections, reserve, testlimits.MaxProcessesAtOnce, budget, ceiling)

	peaks := sampleConnections(t, sampler, names)
	processes := make([]*dispatchProcess, len(urls))
	for i, url := range urls {
		processes[i] = startDispatchProcessWith(t, url, dispatchOptions{
			Env: map[string]string{"DISPATCH_TEST_POOL_BUDGET": strconv.Itoa(budget)},
		})
	}
	for _, process := range processes {
		process.waitHealthy(t)
		process.waitForLog(t, 10*time.Second, "dispatch: pools scaled to the test connection budget", "budget="+strconv.Itoa(budget))
	}

	var failures sync.Map
	var load sync.WaitGroup
	t.Cleanup(load.Wait)
	for i, process := range processes {
		project := fmt.Sprintf("BUDGET%d", i)
		process.createProject(t, project, "Budget")
		keys := make([]string, 4)
		artifacts := make([]string, len(keys))
		for k := range keys {
			keys[k], artifacts[k] = process.createIssueIn(t, project, fmt.Sprintf("Budget %d", k), "before")
		}
		process.openLiveEvents(t)
		editor := process.openEditor(t, artifacts[0], "before\n")
		replaceText(t, editor, "before", "typed")
		const workers, rounds = 16, 8
		for worker := range workers {
			load.Go(func() {
				for round := range rounds {
					key, artifact := keys[(worker+round)%len(keys)], artifacts[(worker+round)%len(artifacts)]
					for _, request := range []struct{ method, path, body string }{
						{http.MethodGet, "/api/v1/issues/" + key, ""},
						{http.MethodPatch, "/api/v1/issues/" + key, fmt.Sprintf(`{"title":"Budget %d %d"}`, worker, round)},
						{http.MethodGet, "/api/v1/artifacts/" + artifact + "/text", ""},
					} {
						answer, err := process.trySend(request.method, request.path, request.body, asUser(dispatchTestLogin))
						if err != nil || answer.status != http.StatusOK {
							failures.Store(fmt.Sprintf("process %d: %s %s", i, request.method, request.path),
								fmt.Sprintf("%d %v %s", answer.status, err, answer.body))
						}
					}
				}
			})
		}
	}
	load.Wait()
	byDatabase, together, samples := peaks()

	failures.Range(func(request, failure any) bool {
		t.Errorf("%s answered %s, want 200", request, failure)
		return true
	})
	t.Logf("%d samples of pg_stat_activity: peak connections by process %v, %d on all of them at once", samples, byDatabase, together)
	if samples < 10 {
		t.Fatalf("pg_stat_activity was read %d times, too few to hold the row to", samples)
	}
	for i, name := range names {
		if byDatabase[i] == 0 {
			t.Errorf("process %d's database %s never showed a connection: the count does not see the process", i, name)
		}
		if byDatabase[i] > budget {
			t.Errorf("process %d opened %d connections to its database at once, over its budget of %d", i, byDatabase[i], budget)
		}
	}
	// Implied by the per-process checks above: PoolBudget gives each process at most ceiling /
	// testlimits.MaxProcessesAtOnce, so the processes together pass ceiling only when one passes
	// its budget, which those checks report. It stays to fail if the two come to disagree.
	if together > ceiling {
		t.Errorf("the processes opened %d connections at once, over the %d a %d-connection server leaves them", together, ceiling, postgresDefaultMaxConnections)
	}
	for i, process := range processes {
		if output := process.Output.String(); strings.Contains(output, "53300") || strings.Contains(output, "remaining connection slots") {
			t.Errorf("process %d met a server out of connections:\n%s", i, output)
		}
	}
}

// sampleConnections reads pg_stat_activity through conn every budgetSampleEvery, counting the
// connections on each of databases, until the returned stop is called; stop answers the highest
// count each database reached, the highest their sum reached in one read, and how many reads were
// made. conn's own connection is left out of the count. The test's cleanup stops the reads too,
// before conn closes, for a test that ends before it calls stop.
func sampleConnections(t *testing.T, conn *pgx.Conn, databases []string) (stop func() (byDatabase []int, together, samples int)) {
	t.Helper()
	done, finished := make(chan struct{}), make(chan struct{})
	byDatabase := make([]int, len(databases))
	var together, samples int
	var failed error
	go func() {
		defer close(finished)
		ticker := time.NewTicker(budgetSampleEvery)
		defer ticker.Stop()
		for {
			counts := map[string]int{}
			rows, err := conn.Query(context.Background(), `
				select datname, count(*) from pg_stat_activity
				where datname = any($1) and pid <> pg_backend_pid()
				group by datname`, databases)
			if err == nil {
				var name string
				var count int
				_, err = pgx.ForEachRow(rows, []any{&name, &count}, func() error {
					counts[name] = count
					return nil
				})
			}
			if err != nil {
				failed = err
				return
			}
			sum := 0
			for i, name := range databases {
				byDatabase[i] = max(byDatabase[i], counts[name])
				sum += counts[name]
			}
			together = max(together, sum)
			samples++
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	halt := sync.OnceFunc(func() {
		close(done)
		<-finished
	})
	t.Cleanup(halt)
	return func() ([]int, int, int) {
		halt()
		if failed != nil {
			t.Fatalf("read pg_stat_activity: %v", failed)
		}
		return byDatabase, together, samples
	}
}
