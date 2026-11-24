package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// What a priority's issue list is held to, from R1 of dispatch://INFRA-5609: 9 of 10 reads within
// 200 ms, read 200 times on one connection after ten reads that warm it.
const (
	priorityListBound     = 200 * time.Millisecond
	priorityListReads     = 200
	priorityListWarmReads = 10
	priorityListPage      = 50
	priorityListQuantile  = 0.9
)

// priorityListRequest is one of the two requests R1 holds: the list with no `limit`, as the
// priorities page and a priority's own page send it, which runs the unpaged statement, and its
// first page of 50, which runs the paged one.
type priorityListRequest struct {
	name      string
	query     string
	statement string
	paged     bool
}

var priorityListRequests = []priorityListRequest{
	{name: "the list", statement: listIssuesQuery},
	{name: "a page of 50", query: fmt.Sprintf("&limit=%d", priorityListPage), statement: listIssuesPageQuery, paged: true},
}

// TestAPriorityListAnswersWithinItsBoundOnEveryPlan holds GET /api/v1/issues?advances=<the
// largest priority>, as the priorities page and a priority's own page send it with no `limit`,
// and the same request's first page of 50, to 200 ms at the 90th percentile under each of the two
// plans a connection runs a statement with (speedCustomPlans): the generic plan every run after
// the fifth may take, and the plan made for its arguments. Each plan is forced on the connection
// (plan_cache_mode), so the plan measured is the one named whatever Postgres would choose, and the
// connection runs with JIT off, as production's Cloud SQL does. The corpus is production's counts
// (seedListCorpus), whose largest priority has 598 open issues.
//
// The reads are timed as the server answers them, sign-in and encoding included, on a server
// whose pool holds one connection, and the test reads back from that connection how many times it
// ran the statement under each kind of plan, so a reading of the wrong plan or of several
// connections stops the test. It prints each request's p50 and p90 on each plan, the database's
// time for each read where the server preloads pg_stat_statements, and the plan itself, run under
// EXPLAIN (ANALYZE, BUFFERS).
//
// A plan that cannot meet the bound is not read out to the end: once more reads are over it than
// the 90th percentile allows of 200, the percentile is over it whatever the rest read, and the
// test stops there and names the plan's widest join filter. The statements before
// dispatch://INFRA-5609 took 4.8 s a read for the list and 4.6 s for the page under the generic
// plan on this corpus (build Job infra5609-list-plan-gdwp6, 2026-11-24).
func TestAPriorityListAnswersWithinItsBoundOnEveryPlan(t *testing.T) {
	corpus := seedListCorpus(t)
	t.Log(corpus.String())
	times := openStatementTimes(t, corpus.database)
	for _, plan := range []struct{ name, mode string }{
		{"the generic plan", "force_generic_plan"},
		{"the plan made for its arguments", "force_custom_plan"},
	} {
		t.Run(plan.name, func(t *testing.T) {
			measured := planCacheStore(t, corpus.database, plan.mode, 1)
			handler, _, _, _ := newRegisteredTestServer(t, testServerOptions{store: measured, identity: headerIdentity(corpus.database)})
			for _, request := range priorityListRequests {
				t.Run(request.name, func(t *testing.T) {
					measurePriorityList(t, corpus, times, measured, handler, request, plan.name, plan.mode)
				})
			}
		})
	}
}

// measurePriorityList reads one request of the largest priority's list under one plan, on the
// server over measured, and holds it to the bound.
func measurePriorityList(t *testing.T, corpus listCorpus, times statementTimes, measured *store.Store, handler http.Handler,
	request priorityListRequest, name, mode string) {
	t.Helper()
	largest := corpus.priorities[0]
	target := "/api/v1/issues?advances=" + largest + request.query
	described := request.name + " under " + name

	// What the read answers, before any read is timed: a timing of a wrong answer is worth nothing.
	answer := priorityListRead(t, handler, target)
	if request.paged {
		page := decodeBody[model.IssueSummaryPage](t, answer)
		if len(page.Issues) != priorityListPage || page.Total != listCorpusLargest {
			t.Fatalf("%s answers %d issues of %d, want %d of %d", target, len(page.Issues), page.Total, priorityListPage, listCorpusLargest)
		}
	} else if issues := decodeBody[[]model.IssueSummary](t, answer); len(issues) != listCorpusLargest {
		t.Fatalf("%s answers %d issues, want %d", target, len(issues), listCorpusLargest)
	}
	for range priorityListWarmReads - 1 {
		priorityListRead(t, handler, target)
	}

	// Once more reads than this are over the bound, the 90th percentile of 200 is over it too.
	allowedOver := priorityListReads - int(math.Ceil(priorityListQuantile*priorityListReads))
	before := times.listing(t, corpus.database)
	reads := make([]time.Duration, 0, priorityListReads)
	over := 0
	for len(reads) < priorityListReads && over <= allowedOver {
		began := time.Now()
		priorityListRead(t, handler, target)
		read := time.Since(began)
		reads = append(reads, read)
		if read > priorityListBound {
			over++
		}
	}
	database := times.listing(t, corpus.database).since(before)

	setting := func(key string) string {
		var value string
		if err := measured.Pool.QueryRow(context.Background(), "select current_setting($1)", key).Scan(&value); err != nil {
			t.Fatalf("read %s on the measured connection: %v", key, err)
		}
		return value
	}
	jit, cacheMode := setting("jit"), setting("plan_cache_mode")
	var generic, custom int64
	if err := measured.Pool.QueryRow(context.Background(),
		"select generic_plans, custom_plans from pg_prepared_statements where statement = $1", request.statement).
		Scan(&generic, &custom); err != nil {
		t.Fatalf("read how the measured connection planned %s: %v", request.name, err)
	}
	executions, ran := int64(priorityListWarmReads+len(reads)), custom
	if mode == "force_generic_plan" {
		ran = generic
	}
	if cacheMode != mode || ran < executions || generic+custom != ran {
		t.Fatalf("the measured connection runs plan_cache_mode = %s and ran %s's statement under %d generic and %d custom plans, want %s and all of the %d reads under the plan it names: the reads were not all on that connection under that plan",
			cacheMode, request.name, generic, custom, mode, executions)
	}

	plan := explainPriorityList(t, measured, request, largest)
	t.Logf("EXPLAIN (ANALYZE, BUFFERS) of %s's statement under %s (plan_cache_mode = %s, jit = %s):\n%s", request.name, name, cacheMode, jit, strings.Join(plan, "\n"))
	sorted := slices.Clone(reads)
	slices.Sort(sorted)
	p50, p90 := nearestRank(sorted, 0.5), nearestRank(sorted, priorityListQuantile)
	t.Logf("%s: %d reads of %s on one connection after %d warm ones (plan_cache_mode = %s, jit = %s; the connection ran the statement under %d generic and %d custom plans): p50 %s, p90 %s; database time %s",
		described, len(reads), target, priorityListWarmReads, cacheMode, jit, generic, custom,
		p50.Round(10*time.Microsecond), p90.Round(10*time.Microsecond), database.String(len(reads)))
	if p90 > priorityListBound {
		node, removed := widestJoinFilter(plan)
		t.Errorf("%s answers %s at the 90th percentile, over the bound of %s (%d of %d reads over it); its plan's widest join filter is %q, which threw away %d rows",
			described, p90.Round(time.Millisecond), priorityListBound, over, len(reads), node, removed)
	}
}

// priorityListRead is one GET of target as alice makes it, held to 200 OK.
func priorityListRead(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("X-Dispatch-User", "alice")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("read %s: status=%d body=%s", target, response.Code, response.Body.String())
	}
	return response
}

// planCacheStore opens a pool of at most connections on database whose every connection runs
// under plan_cache_mode = mode with JIT off, as production's Cloud SQL runs, closed when t ends.
func planCacheStore(t *testing.T, database *store.Store, mode string, connections int32) *store.Store {
	t.Helper()
	config := database.Pool.Config()
	config.MaxConns, config.MinConns = connections, 0
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = mode
	config.ConnConfig.RuntimeParams["jit"] = "off"
	opened, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open a pool under plan_cache_mode = %s: %v", mode, err)
	}
	planned := &store.Store{Pool: store.NewPool(opened)}
	t.Cleanup(planned.Pool.Close)
	return planned
}

// explainPriorityList runs the statement request sends, with the arguments listIssues builds for
// it, under EXPLAIN (ANALYZE, BUFFERS) on a connection of database, as a prepared statement so the
// connection's plan_cache_mode decides its plan, and answers the plan's lines.
func explainPriorityList(t *testing.T, database *store.Store, request priorityListRequest, priority string) []string {
	t.Helper()
	ctx := context.Background()
	connection, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("take a connection to explain the listing: %v", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "prepare priority_list_plan as "+request.statement); err != nil {
		t.Fatalf("prepare the listing: %v", err)
	}
	defer func() {
		if _, err := connection.Exec(ctx, "deallocate priority_list_plan"); err != nil {
			t.Errorf("deallocate the listing: %v", err)
		}
	}()
	arguments := []any{
		"", []string{}, "", (*time.Time)(nil), []string{}, false, []int16{}, false,
		(*time.Time)(nil), (*time.Time)(nil), []string{priority}, false, false, false,
	}
	if request.paged {
		arguments = append(arguments, priorityListPage, 0)
	}
	rows, err := connection.Query(ctx, "explain (analyze, buffers) execute priority_list_plan"+speedLiterals(t, arguments))
	if err != nil {
		t.Fatalf("explain the listing: %v", err)
	}
	defer rows.Close()
	plan := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("read the listing's plan: %v", err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil || len(plan) == 0 {
		t.Fatalf("read the listing's plan: %d lines, %v", len(plan), err)
	}
	return plan
}

// widestJoinFilter names the node of a text plan that threw away the most rows by its join
// filter, and how many: the loop of a plan that joins every issue to a term it reads whole.
func widestJoinFilter(plan []string) (string, int64) {
	node, widest, most := "", "none", int64(0)
	for _, line := range plan {
		text := strings.TrimSpace(line)
		if before, _, found := strings.Cut(text, "  (cost="); found {
			node = strings.TrimSpace(strings.TrimPrefix(before, "->"))
		}
		if count, found := strings.CutPrefix(text, "Rows Removed by Join Filter: "); found {
			if removed, err := strconv.ParseInt(count, 10, 64); err == nil && removed > most {
				widest, most = node, removed
			}
		}
	}
	return widest, most
}

// nearestRank is the q quantile of sorted by the nearest-rank rule: the smallest read that at
// least q of the reads are at or under.
func nearestRank(sorted []time.Duration, q float64) time.Duration {
	return sorted[int(math.Ceil(q*float64(len(sorted))))-1]
}

// statementTimes reads the database's own time for the listing from pg_stat_statements, which a
// server preloads by its own configuration: where it does not, the reading says so rather than
// stopping the test, since the bound is held on the reads' own times.
type statementTimes struct {
	missing string
}

// openStatementTimes creates pg_stat_statements in database where the server preloads it.
func openStatementTimes(t *testing.T, database *store.Store) statementTimes {
	t.Helper()
	var libraries string
	if err := database.Pool.QueryRow(context.Background(), "select current_setting('shared_preload_libraries')").Scan(&libraries); err != nil {
		t.Fatalf("read shared_preload_libraries: %v", err)
	}
	if !strings.Contains(libraries, "pg_stat_statements") {
		return statementTimes{missing: "not read: the server does not preload pg_stat_statements"}
	}
	if _, err := database.Pool.Exec(context.Background(), "create extension if not exists pg_stat_statements"); err != nil {
		return statementTimes{missing: fmt.Sprintf("not read: create extension pg_stat_statements: %v", err)}
	}
	return statementTimes{}
}

// statementTime is the listing texts' calls and their total execution time in this database, as
// pg_stat_statements counts them. A listing text is the only statement whose normalized text
// starts as these do, and a reading runs one of them alone between its two counts.
type statementTime struct {
	missing      string
	calls        int64
	milliseconds float64
}

func (s statementTimes) listing(t *testing.T, database *store.Store) statementTime {
	t.Helper()
	if s.missing != "" {
		return statementTime{missing: s.missing}
	}
	var read statementTime
	if err := database.Pool.QueryRow(context.Background(), `
		select coalesce(sum(calls), 0)::bigint, coalesce(sum(total_exec_time), 0)::float8
		from pg_stat_statements
		where dbid = (select oid from pg_database where datname = current_database())
		  and query like 'with recursive advances_stated as (%'`).Scan(&read.calls, &read.milliseconds); err != nil {
		t.Fatalf("read pg_stat_statements: %v", err)
	}
	return read
}

// since is the time the listing ran between two readings.
func (s statementTime) since(before statementTime) statementTime {
	if s.missing != "" {
		return s
	}
	return statementTime{calls: s.calls - before.calls, milliseconds: s.milliseconds - before.milliseconds}
}

// String reports the database's time for each of reads, and says when the calls pg_stat_statements
// counted are not the reads made.
func (s statementTime) String(reads int) string {
	if s.missing != "" {
		return s.missing
	}
	if s.calls != int64(reads) {
		return fmt.Sprintf("not read: pg_stat_statements counted %d calls of the listing for %d reads", s.calls, reads)
	}
	return fmt.Sprintf("%.2f ms a read (pg_stat_statements, %d calls)", s.milliseconds/float64(s.calls), s.calls)
}
