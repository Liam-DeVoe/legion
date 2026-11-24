package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/embedqueue"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The lines the coverage counts loop logs (embedqueue/counts.go), as a server's text log writes
// them.
const (
	coverageFilledLine     = `msg="dispatch embedqueue: coverage counts filled"`
	coverageRecountedLine  = `msg="dispatch embedqueue: coverage counts recounted"`
	coverageFillFailedLine = `level=ERROR msg="dispatch embedqueue: coverage counts fill failed; it runs again later"`
)

// coverageOracleSource is the file that holds mainCoverageQuery, the coverage statement before
// the kept counts, which embedqueue's tests keep as the oracle of the report's answers.
const coverageOracleSource = "../../internal/dispatch/embedqueue/coverage_oracle_test.go"

// mainCoverageQuery reads embedqueue's oracle (mainCoverageQuery in coverageOracleSource) from its
// source, so the server's answers are held to the one frozen text the package's own tests hold
// the report to, and not to a second copy: Go shares no test code across packages.
func mainCoverageQuery(t *testing.T) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), coverageOracleSource, nil, 0)
	if err != nil {
		t.Fatalf("parse the coverage oracle's source: %v", err)
	}
	for _, declaration := range file.Decls {
		constants, ok := declaration.(*ast.GenDecl)
		if !ok || constants.Tok != token.CONST {
			continue
		}
		for _, spec := range constants.Specs {
			value := spec.(*ast.ValueSpec)
			for position, name := range value.Names {
				if name.Name != "mainCoverageQuery" {
					continue
				}
				literal, ok := value.Values[position].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("mainCoverageQuery in %s is no string literal", coverageOracleSource)
				}
				text, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("read mainCoverageQuery's text: %v", err)
				}
				return text
			}
		}
	}
	t.Fatalf("%s holds no mainCoverageQuery", coverageOracleSource)
	return ""
}

// coverageDatabase is a database of the test's own holding an item of the issue and window kinds
// and a component, written with their triggers, and no fill of the coverage counts: done_at unset.
func coverageDatabase(t *testing.T) *store.Store {
	t.Helper()
	database := storetest.Open(t)
	for _, statement := range []string{
		`insert into projects (key, name, default_assignee) values ('COV', 'Coverage', 'alice')`,
		`insert into issues (key, project_key, number, title, created_by, rank)
		 select 'COV-' || g, 'COV', g, 'An issue the coverage counts count ' || g, '{"kind":"user","id":"alice"}', 'A'
		 from generate_series(1, 3) g`,
		`select embeddings_enqueue_window('document_block', 'document_block/cov#b' || g, 'A window the coverage counts count ' || g, array['b1'])
		 from generate_series(1, 2) g`,
		`insert into architecture_sources (project_key, id, repo, branch, installation_id, created_by)
		 values ('COV', 1, 'cov/arch', 'main', 1, '{"kind":"system","id":"test"}')`,
		`insert into architecture_snapshots (id, project_key, commit, files) values (910, 'COV', 'c1', '{}')`,
		`insert into components (project_key, id, title, prose, snapshot_id, source_id) values ('COV', 'counted', 'Counted', '', 910, 1)`,
	} {
		if _, err := database.Pool.Exec(context.Background(), statement); err != nil {
			t.Fatalf("seed the coverage corpus: %v\n%s", err, statement)
		}
	}
	return database
}

// coverageHold is a lock the test takes on its own connection that a fill or a recount waits on
// where it runs and the route's read does not take: components in access exclusive mode, which
// the not-indexed group's statement reads. It answers the holding transaction; committing it ends
// the hold.
func coverageHold(t *testing.T, database *store.Store) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	hold, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the hold: %v", err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if _, err := hold.Exec(ctx, `lock table components in access exclusive mode`); err != nil {
		t.Fatalf("hold components: %v", err)
	}
	return hold
}

// endHold commits hold.
func endHold(t *testing.T, hold pgx.Tx) {
	t.Helper()
	if err := hold.Commit(context.Background()); err != nil {
		t.Fatalf("end the hold: %v", err)
	}
}

// requireHeld fails the test unless a session of the server waits on hold within 30 s: the fill
// or the recount it holds.
func requireHeld(t *testing.T, hold pgx.Tx, what string) {
	t.Helper()
	if storetest.WaitForLockWaiters(t, hold, 1, 30*time.Second) < 1 {
		t.Fatalf("the server's %s never waited on the hold", what)
	}
}

// coverageServer is a process a coverage test started, with the Host its requests name: empty for
// the address they reach, and the dashboard origin's host for a dev sign-in server, which answers a
// request naming any other 421 HOST_MISMATCH.
type coverageServer struct {
	*dispatchProcess
	host string
}

// coverage is what GET /api/v1/search/coverage answers now, asked with the agent token, which the
// route admits, failing the test when no answer comes within 30 s, three of the route's bounds.
func (s coverageServer) coverage(t *testing.T) httpAnswer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url("/api/v1/search/coverage"), nil)
	if err != nil {
		t.Fatalf("build the coverage request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+dispatchTestAgentToken)
	if s.host != "" {
		request.Host = s.host
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET /api/v1/search/coverage: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the coverage answer: %v", err)
	}
	return httpAnswer{status: response.StatusCode, header: response.Header, body: body}
}

// requireCoverageRefused fails the test unless the route answers 503 with code, its message holding
// says.
func requireCoverageRefused(t *testing.T, server coverageServer, code, says string) {
	t.Helper()
	answer := server.coverage(t)
	if answer.status != http.StatusServiceUnavailable || answer.code() != code || !strings.Contains(string(answer.body), says) {
		t.Fatalf("the coverage route answers %s, want 503 %s saying %q", answer, code, says)
	}
}

// requireHealthy fails the test unless /healthz answers 200 at once: the server serves.
func requireHealthy(t *testing.T, p *dispatchProcess) {
	t.Helper()
	if answer := p.send(t, http.MethodGet, "/healthz", "", nil); answer.status != http.StatusOK {
		t.Fatalf("/healthz answers %s, want 200: the server keeps serving", answer)
	}
}

// coverageDifference compares report, the route's answer, with main's statement (mainCoverageQuery)
// read now for the report's identity, and answers how they differ: "" when every kind's items,
// current, failed and lacking and every not-indexed count are the same and each oldest lacking age is
// within 2 s of main's (the two read a moment apart).
func coverageDifference(t *testing.T, database *store.Store, oracle string, report model.SearchCoverage) string {
	t.Helper()
	identity := ""
	if report.Identity != nil {
		identity = *report.Identity
	}
	rows, err := database.Pool.Query(context.Background(), oracle, identity)
	if err != nil {
		t.Fatalf("run main's coverage statement: %v", err)
	}
	defer rows.Close()
	kinds := map[string]model.SearchCoverageCounts{
		"issue": report.Kinds.Issue, "document_block": report.Kinds.DocumentBlock, "comment_block": report.Kinds.CommentBlock,
		"message_block": report.Kinds.MessageBlock, "ask_part": report.Kinds.AskPart,
	}
	notIndexed := map[string]int64{}
	for _, kind := range report.NotIndexed {
		notIndexed[kind.Kind] = kind.Items
	}
	var differences []string
	for rows.Next() {
		var kind string
		var indexed bool
		var items, current, failed int64
		var oldest *int64
		if err := rows.Scan(&kind, &indexed, &items, &current, &failed, &oldest); err != nil {
			t.Fatalf("read main's coverage statement: %v", err)
		}
		if !indexed {
			if notIndexed[kind] != items {
				differences = append(differences, fmt.Sprintf("%s: the route counts %d, main's statement %d", kind, notIndexed[kind], items))
			}
			continue
		}
		got := kinds[kind]
		ages := got.OldestLackingSeconds == nil && oldest == nil ||
			got.OldestLackingSeconds != nil && oldest != nil && max(*got.OldestLackingSeconds-max(*oldest, 0), max(*oldest, 0)-*got.OldestLackingSeconds) <= 2
		if got.Items != items || got.Current != current || got.Failed != failed || got.Lacking != items-current-failed || !ages {
			differences = append(differences, fmt.Sprintf("%s: the route answers %+v, main's statement items %d, current %d, failed %d, oldest %v",
				kind, got, items, current, failed, oldest))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read main's coverage statement: %v", err)
	}
	return strings.Join(differences, "\n")
}

// requireCoverageIsMains fails the test unless the route answers 200 with the kept counts, and
// those equal main's statement's (coverageDifference) within 30 s: an embedder beside the counts
// keeps writing until its queue is drained, and each read of the route and main's statement is a
// moment of its own.
func requireCoverageIsMains(t *testing.T, server coverageServer, database *store.Store) {
	t.Helper()
	oracle := mainCoverageQuery(t)
	deadline := time.Now().Add(30 * time.Second)
	for {
		answer := server.coverage(t)
		if answer.status != http.StatusOK {
			t.Fatalf("the coverage route answers %s, want 200 with the kept counts", answer)
		}
		var report model.SearchCoverage
		if err := json.Unmarshal(answer.body, &report); err != nil {
			t.Fatalf("read the coverage route's answer: %v\n%s", err, answer.body)
		}
		difference := coverageDifference(t, database, oracle, report)
		if difference == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the route's kept counts differ from main's statement's:\n%s", difference)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// filled reports whether search_coverage_filled holds its row: a fill has ended.
func filled(t *testing.T, database *store.Store) bool {
	t.Helper()
	var done bool
	if err := database.Pool.QueryRow(context.Background(), `select exists (select 1 from search_coverage_filled)`).Scan(&done); err != nil {
		t.Fatalf("read search_coverage_filled: %v", err)
	}
	return done
}

// devEmbedderOptions is a process that embeds meaning search's text with the dev embedder
// (DISPATCH_DEV_EMBEDDER=1), which only a dev sign-in server, signing its own cookies, may run.
var devEmbedderOptions = dispatchOptions{Env: map[string]string{
	"DISPATCH_DEV_SIGNIN":              "1",
	"DISPATCH_DEV_EMBEDDER":            "1",
	"DISPATCH_IDENTITY":                "cookie",
	"DISPATCH_IDENTITY_HEADER_TRUSTED": "",
	"DISPATCH_INSECURE_COOKIE":         "1",
	"DISPATCH_SERVER_URL":              "http://127.0.0.1",
}}

// TestTheServerFillsTheCoverageCountsAfterItListens holds R7k: a server started on counts no
// fill has filled (done_at unset), its fill held where it runs, listens - its listening line
// names since_start_ms - and serves: /healthz answers, and the coverage route answers 503
// SEARCH_COVERAGE_NOT_FILLED. Once the hold ends the log holds `coverage counts filled` and the
// route answers the kept counts, equal to main's statement's. With no embedder and with the dev
// embedder.
func TestTheServerFillsTheCoverageCountsAfterItListens(t *testing.T) {
	for _, run := range []struct {
		name, host string
		options    dispatchOptions
	}{{"no embedder", "", dispatchOptions{}}, {"the dev embedder", "127.0.0.1", devEmbedderOptions}} {
		t.Run(run.name, func(t *testing.T) {
			database := coverageDatabase(t)
			hold := coverageHold(t, database)
			process := startDispatchProcessWith(t, database.Pool.Config().ConnString(), run.options)
			server := coverageServer{process, run.host}
			process.waitHealthy(t)
			if !process.logged(process.listeningLine(), "since_start_ms=") {
				t.Errorf("the listening line names no since_start_ms:\n%s", process.Output.String())
			}
			requireHeld(t, hold, "fill")
			requireHealthy(t, process)
			requireCoverageRefused(t, server, "SEARCH_COVERAGE_NOT_FILLED", "the search coverage counts are not filled yet; try again later")
			if process.logged(coverageFilledLine) || filled(t, database) {
				t.Fatal("the fill ended while it was held")
			}

			endHold(t, hold)
			process.waitForLog(t, 30*time.Second, coverageFilledLine)
			requireCoverageIsMains(t, server, database)
		})
	}
}

// TestAFillCutOffByAStopFillsAgainAtTheNextStart holds R7l: a server stopped while its fill is
// held leaves done_at unset, and the next start, with nothing held, fills again and logs
// `coverage counts filled`; the route then answers the kept counts, equal to main's statement's.
func TestAFillCutOffByAStopFillsAgainAtTheNextStart(t *testing.T) {
	database := coverageDatabase(t)
	hold := coverageHold(t, database)
	first := startDispatchProcess(t, database.Pool.Config().ConnString())
	first.waitHealthy(t)
	requireHeld(t, hold, "fill")
	first.Terminate(t)
	first.WaitExit(t, "SIGTERM")
	if first.logged(coverageFilledLine) || filled(t, database) {
		t.Fatalf("a fill cut off by a stop recorded its end:\n%s", first.Output.String())
	}
	endHold(t, hold)

	second := startDispatchProcess(t, database.Pool.Config().ConnString())
	second.waitHealthy(t)
	second.waitForLog(t, 30*time.Second, coverageFilledLine)
	if !filled(t, database) {
		t.Fatal("the next start's fill ended and left done_at unset")
	}
	requireCoverageIsMains(t, coverageServer{dispatchProcess: second}, database)
}

// TestAFilledStartListensBeforeItsRecountEnds holds R7m: a server started on counts a fill has
// filled, its recount held where it runs, listens, and /healthz and the route answer at once,
// the route 200 with the kept counts; once the hold ends the log holds `coverage counts
// recounted`, it never holds `coverage counts filled`, and the kept counts equal main's statement's
// (which reads components, and so is read once the hold ends).
func TestAFilledStartListensBeforeItsRecountEnds(t *testing.T) {
	database := coverageDatabase(t)
	if err := embedqueue.FillCoverage(context.Background(), database); err != nil {
		t.Fatalf("fill the counts before the start: %v", err)
	}
	hold := coverageHold(t, database)
	process := startDispatchProcess(t, database.Pool.Config().ConnString())
	server := coverageServer{dispatchProcess: process}
	process.waitHealthy(t)
	requireHeld(t, hold, "recount")
	requireHealthy(t, process)
	if answer := server.coverage(t); answer.status != http.StatusOK {
		t.Fatalf("while the recount is held the coverage route answers %s, want 200 with the kept counts", answer)
	}
	if process.logged(coverageRecountedLine) {
		t.Fatal("the recount ended while it was held")
	}

	endHold(t, hold)
	process.waitForLog(t, 30*time.Second, coverageRecountedLine)
	if process.logged(coverageFilledLine) {
		t.Errorf("a start on filled counts filled them:\n%s", process.Output.String())
	}
	requireCoverageIsMains(t, server, database)
}

// TestAFailedFillKeepsServingAndRefusesUntilAFillEnds holds R7n for a fill that fails
// (DISPATCH_TEST_COVERAGE_FILL_FAILURES=1, before its first statement): the server keeps serving,
// logs the failure at ERROR, and answers 503 SEARCH_COVERAGE_NOT_FILLED while the fill after it is
// held; once that one ends the route answers the kept counts, equal to main's statement's.
func TestAFailedFillKeepsServingAndRefusesUntilAFillEnds(t *testing.T) {
	database := coverageDatabase(t)
	hold := coverageHold(t, database)
	process := startDispatchProcessWith(t, database.Pool.Config().ConnString(), dispatchOptions{Env: map[string]string{
		"DISPATCH_TEST_COVERAGE_FILL_FAILURES": "1",
	}})
	process.waitHealthy(t)
	process.waitForLog(t, 30*time.Second, coverageFillFailedLine, "fill was made to fail")
	requireHealthy(t, process)
	requireCoverageRefused(t, coverageServer{dispatchProcess: process}, "SEARCH_COVERAGE_NOT_FILLED", "not filled yet")

	endHold(t, hold)
	process.waitForLog(t, 30*time.Second, coverageFilledLine)
	requireCoverageIsMains(t, coverageServer{dispatchProcess: process}, database)
}

// TestAFillPastItsTimeoutIsAFailedFill holds R7n for a fill past its timeout
// (DISPATCH_TEST_COVERAGE_FILL_TIMEOUT=1s, the fill held for 3 s): the server keeps serving, logs
// the failure at ERROR as a timeout, and answers 503 SEARCH_COVERAGE_NOT_FILLED until a later fill
// ends; then the route answers the kept counts, equal to main's statement's.
func TestAFillPastItsTimeoutIsAFailedFill(t *testing.T) {
	database := coverageDatabase(t)
	hold := coverageHold(t, database)
	process := startDispatchProcessWith(t, database.Pool.Config().ConnString(), dispatchOptions{Env: map[string]string{
		"DISPATCH_TEST_COVERAGE_FILL_TIMEOUT": "1s",
	}})
	process.waitHealthy(t)
	held := time.Now()
	process.waitForLog(t, 30*time.Second, coverageFillFailedLine, "timed_out=true")
	requireHealthy(t, process)
	requireCoverageRefused(t, coverageServer{dispatchProcess: process}, "SEARCH_COVERAGE_NOT_FILLED", "not filled yet")
	time.Sleep(time.Until(held.Add(3 * time.Second)))

	endHold(t, hold)
	process.waitForLog(t, 60*time.Second, coverageFilledLine)
	requireCoverageIsMains(t, coverageServer{dispatchProcess: process}, database)
}

// TestTheCoverageRouteAnswersFoldBehindPastTheBound holds R7j's route: against a server whose
// counts are filled, with the fold held by the counts lock on the test's own connection
// (pg_advisory_lock, which the fold's transaction lock waits on) and 100,001 delta rows unfolded,
// the route answers 503 SEARCH_COVERAGE_FOLD_BEHIND naming the bound of 100,000.
func TestTheCoverageRouteAnswersFoldBehindPastTheBound(t *testing.T) {
	ctx := context.Background()
	database := coverageDatabase(t)
	process := startDispatchProcess(t, database.Pool.Config().ConnString())
	process.waitHealthy(t)
	process.waitForLog(t, 30*time.Second, coverageFilledLine)
	conn, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire the connection that holds the counts lock: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `select pg_advisory_lock(hashtext('search-coverage-counts'))`); err != nil {
		t.Fatalf("hold the counts lock: %v", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `select pg_advisory_unlock_all()`) }()
	if _, err := conn.Exec(ctx, `
		insert into search_coverage_deltas (kind, model, matched, dead, items)
		select 'component', '', false, false, case when g = 100001 then 0 when g % 2 = 0 then 1 else -1 end
		from generate_series(1, 100001) g`); err != nil {
		t.Fatalf("leave 100,001 delta rows unfolded: %v", err)
	}
	requireCoverageRefused(t, coverageServer{dispatchProcess: process}, "SEARCH_COVERAGE_FOLD_BEHIND", "more than 100,000 rows of writes")
}

// TestTheCoverageRouteAnswersItsTimeoutAt503 holds R7p's server row: a server whose route's
// statement is bound to 200 ms (DISPATCH_TEST_COVERAGE_STATEMENT_TIMEOUT), with
// search_coverage_counts held in access exclusive mode by the test once its counts are filled,
// answers the route 503 SEARCH_COVERAGE_TIMEOUT naming 200ms.
func TestTheCoverageRouteAnswersItsTimeoutAt503(t *testing.T) {
	ctx := context.Background()
	database := coverageDatabase(t)
	process := startDispatchProcessWith(t, database.Pool.Config().ConnString(), dispatchOptions{Env: map[string]string{
		"DISPATCH_TEST_COVERAGE_STATEMENT_TIMEOUT": "200ms",
	}})
	process.waitHealthy(t)
	process.waitForLog(t, 30*time.Second, coverageFilledLine)
	hold, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the hold: %v", err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err := hold.Exec(ctx, `lock table search_coverage_counts in access exclusive mode`); err != nil {
		t.Fatalf("hold search_coverage_counts: %v", err)
	}
	requireCoverageRefused(t, coverageServer{dispatchProcess: process}, "SEARCH_COVERAGE_TIMEOUT", "200ms bound")
}
