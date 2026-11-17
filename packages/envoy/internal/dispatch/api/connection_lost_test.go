package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// insertEditBody is the body of an edit that appends a paragraph, which changes the document, so
// the edit writes a version and reads its two reports in its transaction (editArtifact).
func insertEditBody(markdown string) map[string]any {
	return map[string]any{
		"ops":   []map[string]string{{"op": "insert", "after": "end", "markdown": markdown}},
		"actor": sessionActor(),
	}
}

// INFRA-592's ten-times freeze, reproduced: the database stops answering as a document edit rolls
// back the savepoint of its second report, the open approval request's
// (movedApprovalRequestAdvice), and the rollback's own bound (store.RollbackContext) passes five
// seconds later. pgconn ends the rollback by closing the connection. The rollback's error was
// dropped, so the edit went on to its settlement record, which pgconn refused `conn closed`, and
// the edit was answered 500 INTERNAL ("record the document's pending settlement: conn closed",
// 7,755 ms, five seconds after the freeze began). The edit is answered 503 DATABASE_BUSY with
// Retry-After instead, at the rollback that failed: nothing of it committed, so its client may
// send it again.
//
// The socket is frozen, not stalled: its reads answer nothing until pgx puts the rollback's own
// deadline on it (storetest.NewFrozenSocket), so the bound that ends the call is the one that
// ended it in the freeze.
func TestAnEditWhoseReportRollbackRunsOutItsBoundIsRefusedDatabaseBusy(t *testing.T) {
	socket := storetest.NewFrozenSocket(store.RequestsPool)
	logs := captureDebugLog(t)
	handler, _, _ := newTestServer(t, testServerOptions{dialRequestPool: socket.Dial, settle: time.Hour})
	issue := createInteractionIssue(t, handler, "TEST", "The database stops answering a report's rollback", "Before.")

	// sp_2 is the edit's second report's savepoint: the first report opens sp_1.
	socket.StallAfterSent(storetest.SimpleQuery("rollback to savepoint sp_2"))
	started := time.Now()
	response := sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits",
		insertEditBody("Written as the database stops answering."))
	waited := time.Since(started)

	if !socket.Fired() {
		t.Fatal("the socket never froze, so the report's rollback was answered as any other")
	}
	if waited < store.RollbackTimeout {
		t.Fatalf("answered after %v, before the rollback's own bound of %v passed", waited, store.RollbackTimeout)
	}
	if failed := logs.Lines("API handler failed"); failed != "" {
		t.Fatalf("the edit was logged as a fault of the server's:\n%s", failed)
	}
	assertRetryable(t, response.Result(), http.StatusServiceUnavailable, admission.CodeDatabaseBusy)
	busy := logs.Lines("request found the database busy")
	if !strings.Contains(busy, "moved_approval_request report") || !strings.Contains(busy, "rollback's bound passed") {
		t.Fatalf("logged %q, want the line to name the report's rollback and the bound it ran out", busy)
	}
}

// A write whose transaction's connection is closed under it before its COMMIT committed nothing:
// Postgres rolls back the transaction of a backend that ends. It is answered 503 DATABASE_BUSY
// with Retry-After (store.ConnectionLost), and never 500 INTERNAL, which told the client nothing
// to do and the operator that a handler had failed. The edit's backend is ended with
// pg_terminate_backend from inside the edit's transaction, at its first report: the report's read
// meets Postgres's FATAL, and pgconn refuses every later call on the connection `conn closed`
// (pgconn.ErrConnClosed), the report's rollback among them.
func TestAnEditWhoseBackendIsTerminatedBeforeItsCommitIsRefusedDatabaseBusy(t *testing.T) {
	logs := captureDebugLog(t)
	var terminated atomic.Bool
	var backend int32
	// The hook terminates the backend through the test's own handle on the store, which the
	// server is built with below.
	var database *store.Store
	handler, opened, _, _ := newRegisteredTestServer(t, testServerOptions{
		settle: time.Hour,
		adviceQueryHook: func(ctx context.Context, q queryer, query string) error {
			if query != "superseded_approval" || terminated.Swap(true) {
				return nil
			}
			if err := q.QueryRow(ctx, `select pg_backend_pid()`).Scan(&backend); err != nil {
				t.Errorf("read the edit's backend: %v", err)
				return nil
			}
			var ended bool
			if err := database.Pool.QueryRow(context.Background(), `select pg_terminate_backend($1, 5000)`, backend).Scan(&ended); err != nil || !ended {
				t.Errorf("terminate the edit's backend %d: ended=%v err=%v", backend, ended, err)
			}
			return nil
		},
	})
	database = opened
	issue := createInteractionIssue(t, handler, "TEST", "The edit's backend is terminated", "Before.")

	response := sessionRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits",
		insertEditBody("Written as its backend is terminated."))

	if !terminated.Load() {
		t.Fatal("the edit never reached its first report, so its backend was never terminated")
	}
	if failed := logs.Lines("API handler failed"); failed != "" {
		t.Fatalf("the edit was logged as a fault of the server's:\n%s", failed)
	}
	assertRetryable(t, response.Result(), http.StatusServiceUnavailable, admission.CodeDatabaseBusy)
	if busy := logs.Lines("request found the database busy"); !strings.Contains(busy, "conn closed") {
		t.Fatalf("logged %q, want the line to name the closed connection", busy)
	}
	var versions int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) from artifact_versions where artifact_id = $1
	`, issue.PrimaryArtifactID).Scan(&versions); err != nil {
		t.Fatalf("count the document's versions: %v", err)
	}
	if versions != 1 {
		t.Fatalf("the document holds %d versions, want the seeded one alone: the refused edit committed", versions)
	}
}

// A write whose connection ends under its COMMIT may have committed: the server sent the COMMIT,
// and nothing it holds says whether Postgres applied it before the session ended. It stays 500
// COMMIT_OUTCOME_UNKNOWN with no Retry-After (store.ErrCommitTimeout) and the ERROR line naming
// the unknown outcome, never the retryable 503 a connection lost before the COMMIT is answered
// with: a client that sent it again could write twice. The COMMIT is held in a deferred trigger
// on doc_updates, which every edit appends to, and its backend is ended with pg_terminate_backend
// while it waits there.
func TestAnEditWhoseBackendIsTerminatedDuringItsCommitIsAnOutcomeNobodyKnows(t *testing.T) {
	logs := captureDebugLog(t)
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	exec := func(what, sql string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	exec("create the commit gate", `create table tester_commit_gate (armed boolean)`)
	exec("create the slow trigger's function", `create function tester_slow_commit() returns trigger language plpgsql as $$
		begin
			if exists (select 1 from tester_commit_gate) then perform pg_sleep(30); end if;
			return null;
		end $$`)
	exec("create the slow trigger", `create constraint trigger tester_slow_commit after insert on doc_updates
		deferrable initially deferred for each row execute function tester_slow_commit()`)
	issue := createInteractionIssue(t, handler, "TEST", "The edit's backend is terminated at its COMMIT", "Before.")
	exec("arm the commit gate", `insert into tester_commit_gate values (true)`)
	// Disarmed however the test ends, so the document service's shutdown appends nothing slowly.
	t.Cleanup(func() { _, _ = database.Pool.Exec(context.Background(), `delete from tester_commit_gate`) })

	data, err := json.Marshal(insertEditBody("Written as its COMMIT's backend is terminated."))
	if err != nil {
		t.Fatalf("encode the edit: %v", err)
	}
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/edits", bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer agent-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		answered <- response
	}()
	var backend int32
	pollUntil(t, 10*time.Second, "the edit's COMMIT waits in the slow trigger", func() bool {
		return database.Pool.QueryRow(context.Background(), `
			select pid from pg_stat_activity
			where query = 'commit' and wait_event = 'PgSleep' and application_name = $1
		`, "dispatch-"+store.RequestsPool).Scan(&backend) == nil
	})
	var ended bool
	if err := database.Pool.QueryRow(context.Background(), `select pg_terminate_backend($1, 5000)`, backend).Scan(&ended); err != nil || !ended {
		t.Fatalf("terminate the COMMIT's backend %d: ended=%v err=%v", backend, ended, err)
	}
	exec("disarm the commit gate", `delete from tester_commit_gate`)
	response := awaitResponse(t, answered)

	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"COMMIT_OUTCOME_UNKNOWN"`) {
		t.Fatalf("status=%d body=%s, want 500 COMMIT_OUTCOME_UNKNOWN", response.Code, response.Body.String())
	}
	if retry := response.Result().Header.Get("Retry-After"); retry != "" {
		t.Fatalf("Retry-After = %q, want none: the commit may have landed", retry)
	}
	if busy := logs.Lines("request found the database busy"); busy != "" {
		t.Fatalf("the edit whose COMMIT lost its connection was offered as retryable:\n%s", busy)
	}
	unknown := logs.Lines("its outcome is unknown")
	if !strings.Contains(unknown, "level=ERROR") || !strings.Contains(unknown, "the connection ended under the COMMIT") {
		t.Fatalf("logged %q, want an ERROR line naming the COMMIT whose connection ended", unknown)
	}
}
