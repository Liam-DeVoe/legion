package api

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/benchtime"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// What R3 of dispatch://INFRA-5609 holds a listing request to: no more than twice the shared
// buffers on the generic plan than on the plan made for its arguments. planStableWarmBudget bounds
// the ten warm reads: the plan is forced, so they warm the caches and choose nothing, and a read
// that takes longer than this is already the fault (the merge base's generic plan of the largest
// project's list with moves took minutes a read), so they stop there.
const (
	planStableRatio      = 2.0
	planStableWarmBudget = time.Minute
)

// TestNoListingRequestReadsMoreOnTheGenericPlan holds the listing requests a page sends, beside
// the two TestAPriorityListAnswersWithinItsBoundOnEveryPlan times, to R3: on the generic plan,
// whose one-issue estimate made the listing's joins loop, a request reads at most twice the shared
// buffers it reads on the plan made for its arguments. Buffers are what the plan reads, not how
// fast the machine reads them, so the bound holds on any box.
//
// Two of the requests are on the seed of cmd/dispatch-seed --scale 1 (ten projects, the largest of
// 8,000 issues), which the database DISPATCH_BENCH_DATABASE_URL names holds: the seed takes about
// an hour to write, so the test reads one a build Job wrote rather than writing its own, as
// TestStatementTimings does, and CI, which leaves the variable unset, skips it. They are the
// largest project's list as the project list sends it (`?project=<p>&open=true`), and the same list
// with moves and no `open`, the shape whose generic plan read 279,631,934 buffers against 70,185.
// The third is the priorities page's parked list on the corpus of production's counts
// (seedListCorpus). The test writes nothing to the seeded database but what a test server writes
// when it starts: its two people.
//
// Each request goes through a server whose pool holds one connection under the plan named
// (planCacheStore), ten times to warm it (planStableWarmBudget), and then the statement it sent,
// with the arguments it sent, runs under EXPLAIN (ANALYZE, BUFFERS) on that connection. The test
// prints each request's shared buffers and time on both plans and their ratio, and on a ratio
// above 2 the three nodes of the generic plan that ran the most times.
func TestNoListingRequestReadsMoreOnTheGenericPlan(t *testing.T) {
	benchURL := os.Getenv(benchtime.DatabaseURLEnv)
	if benchURL == "" {
		t.Skip(benchtime.DatabaseURLEnv + " must name a database cmd/dispatch-seed --scale 1 wrote; CI leaves it unset")
	}
	corpus := seedListCorpus(t)
	bench, err := store.Open(context.Background(), benchURL)
	if err != nil {
		t.Fatalf("open %s: %v", benchtime.DatabaseURLEnv, err)
	}
	t.Cleanup(bench.Pool.Close)
	var project string
	var issues, open int
	if err := bench.Pool.QueryRow(context.Background(), `
		select project_key, count(*), count(*) filter (where closed_at is null)
		from issues group by project_key order by count(*) desc, project_key limit 1`).Scan(&project, &issues, &open); err != nil {
		t.Fatalf("read the seed's largest project: %v", err)
	}
	t.Logf("%s; seed: the largest project of %s, %s, holds %d issues, %d open", corpus.String(), benchtime.DatabaseURLEnv, project, issues, open)

	for _, request := range []struct {
		name     string
		database *store.Store
		target   string
	}{
		{"the largest project's list", bench, "/api/v1/issues?project=" + project + "&open=true"},
		{"the largest project's list with moves", bench, "/api/v1/issues?project=" + project + "&moved=true"},
		{"the priorities page's parked list", corpus.database, "/api/v1/issues?advances=any&status=backlog&status=icebox&moved=true&open=true"},
	} {
		t.Run(request.name, func(t *testing.T) {
			custom := readPlanStable(t, request.database, request.target, "force_custom_plan")
			generic := readPlanStable(t, request.database, request.target, "force_generic_plan")
			ratio := generic.buffers / custom.buffers
			t.Logf("%s (%s): %.0f shared buffers in %.1f ms on the generic plan, %.0f in %.1f ms on the plan made for its arguments: %.2fx, at most %.2fx (jit = %s)",
				request.name, request.target, generic.buffers, generic.milliseconds, custom.buffers, custom.milliseconds, ratio, planStableRatio, generic.jit)
			if ratio > planStableRatio {
				t.Errorf("%s reads %.0f shared buffers on the generic plan against %.0f on the plan made for its arguments, %.2fx, above %.2fx; the generic plan's most-run nodes are %s",
					request.target, generic.buffers, custom.buffers, ratio, planStableRatio, strings.Join(generic.busiest, ", "))
			}
		})
	}
}

// planStableReading is one request's statement read under one plan: the top node's shared
// buffers (hit and read), the execution time, the three plan nodes that ran the most times with
// how often, and the connection's jit setting.
type planStableReading struct {
	buffers      float64
	milliseconds float64
	busiest      []string
	jit          string
}

// readPlanStable sends target through a server over database whose pool holds one connection
// under plan_cache_mode = mode, warms it, and explains the listing statement the first read sent,
// with its arguments, on that connection.
func readPlanStable(t *testing.T, database *store.Store, target, mode string) planStableReading {
	t.Helper()
	trace := &speedTrace{}
	handler, served, _, _ := newRegisteredTestServer(t, testServerOptions{store: planCacheStore(t, database, mode, 1), tracer: trace, identity: headerIdentity(database)})
	read := func(ctx context.Context) {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		request.Header.Set("X-Dispatch-User", "alice")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("read %s under %s: status=%d body=%.300s", target, mode, response.Code, response.Body.String())
		}
	}
	trace.take()
	began := time.Now()
	read(context.WithValue(context.Background(), countedRead{}, true))
	sent, _ := trace.take()
	var listing *speedStatement
	for index := range sent {
		if slices.Contains([]string{listIssuesQuery, listPinnedIssuesQuery, listIssuesPageQuery, listPinnedIssuesPageQuery}, sent[index].sql) {
			listing = &sent[index]
		}
	}
	if listing == nil {
		t.Fatalf("%s sent no listing statement the trace saw, of %d statements", target, len(sent))
	}
	for warmed := 1; warmed < priorityListWarmReads && time.Since(began) < planStableWarmBudget; warmed++ {
		read(context.Background())
	}

	ctx := context.Background()
	connection, err := served.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("take the measured connection: %v", err)
	}
	defer connection.Release()
	reading := planStableReading{}
	if err := connection.QueryRow(ctx, "select current_setting('jit')").Scan(&reading.jit); err != nil {
		t.Fatalf("read jit: %v", err)
	}
	var generic, custom int64
	if err := connection.QueryRow(ctx, "select generic_plans, custom_plans from pg_prepared_statements where statement = $1", listing.sql).Scan(&generic, &custom); err != nil {
		t.Fatalf("read how the measured connection planned %s: %v", target, err)
	}
	if (mode == "force_generic_plan") != (custom == 0) || generic+custom == 0 {
		t.Fatalf("the measured connection ran %s's statement under %d generic and %d custom plans, want every run under %s", target, generic, custom, mode)
	}
	if _, err := connection.Exec(ctx, "prepare plan_stable_statement as "+listing.sql); err != nil {
		t.Fatalf("prepare %s's statement: %v", target, err)
	}
	defer func() {
		if _, err := connection.Exec(ctx, "deallocate plan_stable_statement"); err != nil {
			t.Errorf("deallocate %s's statement: %v", target, err)
		}
	}()
	var raw []byte
	if err := connection.QueryRow(ctx, "explain (analyze, buffers, format json) execute plan_stable_statement"+speedLiterals(t, listing.arguments)).Scan(&raw); err != nil {
		t.Fatalf("explain %s's statement under %s: %v", target, mode, err)
	}
	var plans []struct {
		Plan          analyzedNode `json:"Plan"`
		ExecutionTime float64      `json:"Execution Time"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("read %s's plan under %s: %d plans, %v", target, mode, len(plans), err)
	}
	top := plans[0].Plan
	reading.buffers, reading.milliseconds = top.SharedHitBlocks+top.SharedReadBlocks, plans[0].ExecutionTime
	nodes := top.everyNode()
	slices.SortStableFunc(nodes, func(a, b analyzedNode) int { return cmp.Compare(b.Loops, a.Loops) })
	for _, node := range nodes[:min(3, len(nodes))] {
		reading.busiest = append(reading.busiest, fmt.Sprintf("%s (%.0f runs)", node.name(), node.Loops))
	}
	if reading.buffers == 0 {
		t.Fatalf("%s's plan under %s reads no shared buffer: the plan was not read, so nothing was measured", target, mode)
	}
	return reading
}
