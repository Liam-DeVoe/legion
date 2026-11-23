package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// endMeaningStatementsAt points searchMeaningScan's bound (searchMeaningStatementBound) at `at`
// for one test, so that a meaning statement is ended by the server rather than answered, without
// a test waiting production's fifteen seconds out. The bound it replaces must be in the list: a
// list that holds none is the defect an earlier fix closed (a meaning statement with no bound of
// its own), and a test that quietly added its own bound would pass on a server that holds none.
//
// Swapping a package-level statement is safe only because no test in internal/dispatch/api calls
// t.Parallel, so no other test's meaning query can read the shortened list before the cleanup
// puts it back; a test added here that does call t.Parallel breaks these. It is the same seam,
// under the same constraint, as the claim path's (issue_claim_test.go).
//
// The shortened bound alone does not end a meaning statement. On a test database of a few rows
// the statement itself executes in well under a millisecond (0.14 to 0.53 ms measured on Postgres
// 18); what takes longer is its plan, 9 to 18 ms for each custom plan the server makes. A
// backend that reuses a plan it has already made answers the statement inside the bound: 39
// runs of 40 did with plan_cache_mode = force_generic_plan, and one run of the whole package saw
// a search answered in 7 ms. So each test below also holds the one table every meaning
// statement reads (holdMeaningStatements): the statement waits however fast it would run, and
// the bound is what ends it. Were the bound ever to end one of the two `set local` statements
// it precedes instead, beginMeaningScan would fail with that same cancellation, and each caller
// below reports it the same way, so what these tests assert does not depend on which of the
// transaction's statements the bound ends.
func endMeaningStatementsAt(t *testing.T, at string) {
	t.Helper()
	healthy := searchMeaningScan
	shortened := slices.Clone(searchMeaningScan)
	bounded := false
	for index, setting := range shortened {
		if setting == searchMeaningStatementBound {
			shortened[index] = "set local statement_timeout = '" + at + "'"
			bounded = true
		}
	}
	if !bounded {
		t.Fatalf("searchMeaningScan = %q, want it to hold the server's own bound on a meaning statement (%q)",
			searchMeaningScan, searchMeaningStatementBound)
	}
	searchMeaningScan = shortened
	t.Cleanup(func() { searchMeaningScan = healthy })
}

// holdMeaningStatements locks embeddings, the table every meaning statement reads its vectors
// from, in ACCESS EXCLUSIVE mode on a transaction of the test's own, and answers the function
// that rolls that transaction back; the test's cleanup rolls it back too, should the test end
// first. A meaning statement started while the lock is held waits for it, and Postgres checks a
// statement's statement_timeout while the statement waits for a lock as it does while the
// statement runs, so until the release such a statement can be ended by the server's bound on
// it and never answered. That is what lets these tests assert what a caller makes of a
// statement the server ended without betting that the bound is shorter than the statement
// (endMeaningStatementsAt says why that bet loses).
//
// Nothing else on these tests' paths reads embeddings: search's own key and numnode reads, the
// related-item check's keyword stage and the fill's progress read (embeddingProjectKeysFilled)
// read other tables, so only the meaning statement waits. The lock is taken after the test has
// written its embeddings (processAllPending) and released before the server's cleanups, so
// neither those writes nor the server's own shutdown queue behind it.
func holdMeaningStatements(t *testing.T, database *store.Store) (release func()) {
	t.Helper()
	ctx := context.Background()
	hold, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the transaction that holds embeddings: %v", err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if _, err := hold.Exec(ctx, "lock table embeddings in access exclusive mode"); err != nil {
		t.Fatalf("lock embeddings: %v", err)
	}
	return func() {
		if err := hold.Rollback(ctx); err != nil {
			t.Fatalf("release the lock on embeddings: %v", err)
		}
	}
}

// A meaning query's transaction carries the server's own bound, read from the transaction itself
// rather than from the list that set it: a search whose caller has gone cannot be cancelled while
// the pool is full, since pgx cancels out of band and a CancelRequest needs a connection of its
// own, so the statement has to end itself. The two index settings are read beside it: all three
// reach a query the same way, and a list that lost one of them is read here too.
//
// The bound is local to the transaction, so the connection goes back to the pool without it.
func TestMeaningScanAppliesTheServersBoundToItsTransaction(t *testing.T) {
	t.Parallel()
	_, database, deps := newTestServer(t, testServerOptions{})
	ctx := context.Background()
	tx, err := directServer(deps).beginMeaningScan(ctx)
	if err != nil {
		t.Fatalf("begin the meaning scan: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var timeout, efSearch, iterativeScan string
	if err := tx.QueryRow(ctx, `select current_setting('statement_timeout'), current_setting('hnsw.ef_search'),
		current_setting('hnsw.iterative_scan')`).Scan(&timeout, &efSearch, &iterativeScan); err != nil {
		t.Fatalf("read the settings the meaning transaction applies: %v", err)
	}
	if timeout != "15s" {
		t.Fatalf("statement_timeout = %q in a meaning query's transaction, want 15s: the server's own bound is what "+
			"ends a search whose caller has gone, since pgx cannot deliver a cancellation when the pool is full", timeout)
	}
	if efSearch != "400" || iterativeScan != "relaxed_order" {
		t.Fatalf("hnsw.ef_search = %q and hnsw.iterative_scan = %q, want 400 and relaxed_order", efSearch, iterativeScan)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("roll the meaning transaction back: %v", err)
	}
	var pooled string
	if err := database.Pool.QueryRow(ctx, `show statement_timeout`).Scan(&pooled); err != nil {
		t.Fatalf("read statement_timeout outside a meaning transaction: %v", err)
	}
	if pooled == "15s" {
		t.Fatalf("statement_timeout = %q outside the meaning transaction, want the server's own default: "+
			"`set local` must not follow a pooled connection to the next request", pooled)
	}
}

// A setting the server refuses fails the whole scan's setup, and beginMeaningScan rolls the
// transaction it opened back rather than handing the caller a half-set one or leaving the
// connection it was opened on acquired for good. The settings are one Exec now, so a refused
// entry fails the statement the sound ones were sent in, and the rollback is the only thing
// that returns the connection: the pool's count of acquired connections is what reads it.
//
// The refused entry is a bad duration for statement_timeout, a parameter Postgres itself
// defines: hnsw.ef_search is pgvector's, and a server that has not loaded the module reads it
// as a placeholder and takes any text for it.
//
// It reads the error's text too. The Go side no longer names which setting failed, since there
// is one Exec to name; Postgres's own message still does, and that is what the server logs.
func TestMeaningScanRollsBackWhenASettingFails(t *testing.T) {
	_, database, deps := newTestServer(t, testServerOptions{})
	ctx := context.Background()
	healthy := searchMeaningScan
	searchMeaningScan = []string{searchMeaningStatementBound, `set local statement_timeout = 'not a duration'`}
	t.Cleanup(func() { searchMeaningScan = healthy })

	before := database.Pool.Stat().AcquiredConns()
	tx, err := directServer(deps).beginMeaningScan(ctx)
	if err == nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("begin the meaning scan over a setting the server refuses = no error, want the failure reported")
	}
	if tx != nil {
		t.Fatalf("begin the meaning scan answered a transaction beside its error (%v), want none", err)
	}
	if got := database.Pool.Stat().AcquiredConns(); got != before {
		t.Fatalf("the pool holds %d acquired connections after a failed meaning scan, want %d: the transaction a "+
			"setting failed on is rolled back here, never left holding its connection", got, before)
	}
	if !strings.Contains(err.Error(), "statement_timeout") {
		t.Fatalf("a refused setting answered %v, want Postgres's own message naming statement_timeout: the Go side "+
			"no longer names which setting failed", err)
	}
}

// The search route reports a meaning statement the server ended as the failure of this request it
// is - 500 INTERNAL - and never as an answer. runSearch keeps its keyword-only answer for a query
// whose embedding could not be had; papering a cut-off search over as a degraded result would
// serve a caller a page of keyword hits as though meaning search had run.
//
// The bounded search runs while embeddings is held (holdMeaningStatements), so its meaning
// statement cannot answer before the shortened bound ends it. A server that held no bound on the
// statement would wait for that lock until the test binary's deadline guard fails the case by
// name.
func TestSearchReportsAMeaningStatementTheServerEnded(t *testing.T) {
	const title, spec = "Celestial navigation device maintenance", "Routine upkeep notes."
	embedder := &fakeEmbedder{vectors: map[string][]float32{
		issueEmbeddingText(title, spec): angledVector(0.9, 2),
		"wibbleflorp":                   queryVector(),
	}}
	handler, database, _ := newTestServer(t, testServerOptions{embedder: embedder})
	issue := createInteractionIssue(t, handler, "BOUND", title, spec)
	processAllPending(t, database, embedder)

	// The query shares no word with the issue, so the hit below came from the meaning query
	// alone: this server answered the same search before the bound was shortened, and what the
	// 500 after it reports is the statement the server ended, not a broken fixture.
	healthy := searchResponse(t, handler, "q=wibbleflorp")
	if healthy.Degraded != "" || len(healthy.Results) != 1 || healthy.Results[0].ID != issue.Key {
		t.Fatalf("search before the bound was shortened = %+v, want the issue %s found by meaning", healthy, issue.Key)
	}

	endMeaningStatementsAt(t, "1ms")
	release := holdMeaningStatements(t, database)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=wibbleflorp", nil)
	request.Header.Set("X-Dispatch-User", "alice")
	ended := answeredBeforeTheBinaryDeadline(t, "the search", func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	})
	release()
	if ended.Code != http.StatusInternalServerError || !strings.Contains(ended.Body.String(), `"code":"INTERNAL"`) {
		t.Fatalf("search under a statement the server ends: status=%d body=%s, want 500 INTERNAL", ended.Code, ended.Body.String())
	}
	if strings.Contains(ended.Body.String(), issue.Key) {
		t.Fatalf("search under a statement the server ends answered results: %s", ended.Body.String())
	}
}

// The related-item check reports a meaning statement the server ended the way it already reports
// a meaning search that did not answer: the check is still made, by keywords alone, and
// says why. Before this it would have read the server's own bound as "meaning search failed",
// which is what it says when something broke.
//
// The check answers when the statement is ended rather than at the end of its meaning allowance,
// which is what tells the two apart: the statement was ended, not waited out. Here that
// allowance is an hour (server.meaningBound, with the check's own allowance raised to match),
// and the check runs while embeddings is held (holdMeaningStatements), so its meaning statement
// cannot answer before the shortened bound ends it: a check that answered at all answered
// because the server ended the statement, and one that waited the allowance out fails by name
// before the binary's deadline. The text shares one word with the issue and means the same
// thing, so the keyword stage has the item to fall back on and the meaning stage has a reason
// to be running at all.
func TestRelatedCheckReportsAMeaningStatementTheServerEndedAsATimeout(t *testing.T) {
	const title, spec = "Nightly backups overrun their window", "The backup job runs past six in the morning."
	const text = "Nightly snapshots finish late"
	embedder := &fakeEmbedder{vectors: map[string][]float32{
		issueEmbeddingText(title, spec): angledVector(0.9, 2),
		text:                            queryVector(),
	}}
	handler, database, srv := meaningServer(t, embedder)
	createSuggestionProject(t, handler, "MBOUND")
	key := createSearchIssue(t, handler, "MBOUND", title, spec)
	processAllPending(t, database, embedder)

	// The check searched by meaning on this server before the bound was shortened.
	healthy := srv.relatedCheck(context.Background(), relatedInput{project: "MBOUND", text: text})
	if healthy.Search != "meaning_and_keywords" || healthy.SearchNote != "" {
		t.Fatalf("suggestions = %+v before the bound was shortened, want found by meaning and keywords", healthy)
	}

	const meaningBound = time.Hour
	srv.meaningBound, srv.suggestionTimeout = meaningBound, meaningBound
	endMeaningStatementsAt(t, "1ms")
	release := holdMeaningStatements(t, database)
	suggestions := answeredBeforeTheBinaryDeadline(t, "the check", func() *model.Suggestions {
		return srv.relatedCheck(context.Background(), relatedInput{project: "MBOUND", text: text})
	})
	release()
	if suggestions.Outcome != "found" || suggestions.Search != "keywords_only" ||
		suggestions.SearchNote != "meaning search did not answer within "+meaningBound.String() {
		t.Fatalf("suggestions = %+v, want found by keywords only, the server having ended the meaning statement", suggestions)
	}
	if !slices.Contains(relatedOwnerKeys(suggestions.Related), key) {
		t.Fatalf("related = %+v, want %s from the keyword search", suggestions.Related, key)
	}
}

// backendStatement is one statement a traced pool started and the backend process it ran on
// (conn.PgConn().PID()). storetest.StatementRecorder records the text alone, and which
// transaction a statement ran in is a fact of the backend it ran on.
type backendStatement struct {
	pid uint32
	sql string
}

// backendStatements records every statement started on the pool it traces (storetest.Traced),
// in the order they started, each with its backend.
type backendStatements struct {
	mu   sync.Mutex
	seen []backendStatement
}

func (b *backendStatements) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, backendStatement{pid: conn.PgConn().PID(), sql: data.SQL})
	return ctx
}

func (b *backendStatements) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// statements is a copy of what b has recorded so far.
func (b *backendStatements) statements() []backendStatement {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.seen)
}

// The two settings R1 holds the transaction of every search statement to: the server's own
// bound on the statement, and the interval at which Postgres checks, while the statement runs,
// that its client is still connected.
const (
	searchBoundSetting           = `statement_timeout = '15s'`
	searchConnectionCheckSetting = `client_connection_check_interval = '1s'`
)

// unboundedBecause says why the statement at position at of seen did not run inside a
// transaction that, after its begin, ran a setting statement holding both searchBoundSetting and
// searchConnectionCheckSetting, and is empty when it did. It reads the statements its own
// backend ran before it, newest first: a commit or a rollback met before any begin is a statement
// run outside any transaction, and a begin met before any such setting statement is a
// transaction that set neither. A rollback to a savepoint ends no transaction.
func unboundedBecause(seen []backendStatement, at int) string {
	pid := seen[at].pid
	set := false
	for earlier := at - 1; earlier >= 0; earlier-- {
		if seen[earlier].pid != pid {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(seen[earlier].sql))
		switch {
		case text == "commit" || text == "rollback" || text == "end" || text == "abort":
			return fmt.Sprintf("it ran on backend %d in no transaction: the last one there ended (%s) before it and none began after",
				pid, text)
		case text == "begin" || strings.HasPrefix(text, "begin ") || strings.HasPrefix(text, "start transaction"):
			if !set {
				return fmt.Sprintf("it ran on backend %d in a transaction that ran no statement holding both %q and %q after its begin",
					pid, searchBoundSetting, searchConnectionCheckSetting)
			}
			return ""
		case strings.Contains(text, searchBoundSetting) && strings.Contains(text, searchConnectionCheckSetting):
			set = true
		}
	}
	return fmt.Sprintf("it ran on backend %d, which began no transaction before it", pid)
}

// refusingQueryEmbedder is fakeEmbedder with one query it answers no embedding for, as a model
// service that did not answer: a search for that text is answered by keywords alone and stamped
// degraded, on the same server whose other searches run the meaning statements.
type refusingQueryEmbedder struct {
	*fakeEmbedder
	refused string
}

func (e refusingQueryEmbedder) Embed(ctx context.Context, texts []string, inputType embed.InputType) ([][]float32, error) {
	if inputType == embed.InputQuery && slices.Contains(texts, e.refused) {
		return nil, errors.New("the model service did not answer")
	}
	return e.fakeEmbedder.Embed(ctx, texts, inputType)
}

// TestEverySearchStatementRunsInsideItsBoundedTransaction holds R1 over its class. Each of the
// four statements a search runs - searchQuery and the three meaning statements
// meaningStatementFor picks from - runs inside a transaction that, after its begin, ran a
// setting statement holding both the server's 15 s bound and the 1 s check that the client is
// still connected. It holds that in one search of each shape a search takes: keywords only;
// keywords because no embedder answered; meaning over every project; meaning over one project
// once the embeddings project_key fill is done; meaning joined, for a filtered search; and a
// broadened run of a keyword and of a meaning search, whose second statement is held to the
// same as its first. Which transaction a statement ran in is a fact of the backend it ran on, so
// the tracer records each statement's backend process, and each statement is read against what
// its backend ran before it (unboundedBecause).
//
// Each shape is also held to the statement it names and the number of times it runs it, and to
// the answer that says which shape it was (degraded, broadened), so a fixture that stopped
// reaching a shape fails rather than passing on statements it never ran.
func TestEverySearchStatementRunsInsideItsBoundedTransaction(t *testing.T) {
	t.Parallel()
	// degradedQuery is the one query the embedder refuses. broadenedQuery holds a word no item
	// holds, so no item holds every word of it; its embedding is at cosine 0 to every stored
	// vector (defaultVector), so the meaning list answers nothing for it either, and the search
	// runs again over its rarest words.
	const degradedQuery, broadenedQuery = "lighthouse keeper", "lighthouse zzzabsent"
	embedder := refusingQueryEmbedder{
		fakeEmbedder: &fakeEmbedder{vectors: map[string][]float32{broadenedQuery: queryVector()}},
		refused:      degradedQuery,
	}
	statements := &backendStatements{}
	handler, database, _ := newTestServer(t, testServerOptions{embedder: embedder, tracer: statements})
	issue := createInteractionIssue(t, handler, "BOUND", "Lighthouse keeper roster", "The keeper logs the tide at dawn.")
	searchComment(t, handler, issue.Key, "alice", "The lighthouse keeper logs the tide at dawn.")
	processAllPending(t, database, embedder)
	RefreshEverySearchTermStats(context.Background(), database.Pool)
	recordEmbeddingProjectKeyFill(t, database, true)

	texts := map[string]string{
		searchQuery:              "searchQuery",
		searchQueryMeaning:       "searchQueryMeaning",
		searchQueryMeaningScoped: "searchQueryMeaningScoped",
		searchQueryMeaningJoined: "searchQueryMeaningJoined",
	}
	for _, shape := range []struct {
		name, path          string
		query               url.Values
		statement           string
		runs                int
		degraded, broadened bool
	}{
		{"keyword, keywords only", "/api/v1/search/results",
			url.Values{"q": {"lighthouse"}, "keywords_only": {"true"}}, searchQuery, 1, false, false},
		{"keyword, degraded because no embedder answered", "/api/v1/search",
			url.Values{"q": {degradedQuery}}, searchQuery, 1, true, false},
		{"meaning, every project", "/api/v1/search",
			url.Values{"q": {"lighthouse"}}, searchQueryMeaning, 1, false, false},
		{"meaning, one project with the fill done", "/api/v1/search",
			url.Values{"q": {"lighthouse"}, "project": {"BOUND"}}, searchQueryMeaningScoped, 1, false, false},
		{"meaning, joined for a filtered search", "/api/v1/search/results",
			url.Values{"q": {"lighthouse"}, "author": {"alice"}}, searchQueryMeaningJoined, 1, false, false},
		{"keyword, broadened", "/api/v1/search/results",
			url.Values{"q": {broadenedQuery}, "keywords_only": {"true"}}, searchQuery, 2, false, true},
		{"meaning, broadened", "/api/v1/search",
			url.Values{"q": {broadenedQuery}}, searchQueryMeaning, 2, false, true},
	} {
		t.Run(shape.name, func(t *testing.T) {
			mark := len(statements.statements())
			response := searchResponseOn(t, handler, shape.path, shape.query.Encode())
			if (response.Degraded != "") != shape.degraded || response.Query.Broadened != shape.broadened {
				t.Fatalf("the search answered degraded=%q broadened=%v, want degraded %v and broadened %v: the fixture no longer reaches this shape",
					response.Degraded, response.Query.Broadened, shape.degraded, shape.broadened)
			}
			seen := statements.statements()
			ran := map[string]int{}
			for at := mark; at < len(seen); at++ {
				name, ok := texts[seen[at].sql]
				if !ok {
					continue
				}
				ran[name]++
				if why := unboundedBecause(seen, at); why != "" {
					t.Errorf("%s, run %d of this search: %s", name, ran[name], why)
				}
			}
			if want := map[string]int{texts[shape.statement]: shape.runs}; !maps.Equal(ran, want) {
				t.Fatalf("the search ran %v of the four statements, want %v", ran, want)
			}
		})
	}
}
