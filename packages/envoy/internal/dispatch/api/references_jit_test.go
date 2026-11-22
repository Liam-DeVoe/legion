package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The issue references read runs with JIT off whatever its session starts with (inClosureRead):
// stock Postgres starts every session at jit = on, under which JIT's compilation alone once took
// the read past its bound. GET /api/v1/issues/{key}/references is served by a handler whose store
// is a pool of one connection whose sessions start at jit = on, so the session the route reads in
// and the session read after it are the one connection's, and a tracer on that pool records each
// statement the connection sends, in order. The route answers 200; the statements of its closure,
// each starting with refs.ClosureTag, run in one transaction, whose first statement after its
// begin is the closure's set local jit = off, before any other statement of the closure; and after
// the read the same session reads jit on, so the setting was the transaction's alone. The negative
// controls: inClosureRead without its set local jit = off, under which the transaction's first
// statement after its begin is the budget's set_config of statement_timeout; and the route running
// refs.Closure on the pool rather than inside inClosureRead, under which the closure's statements
// run in no transaction.
func TestTheIssueReferencesReadRunsWithJITOff(t *testing.T) {
	handler, database, deps := newTestServer(t, testServerOptions{})
	createReferenceAPIProject(t, handler, "CORE")
	issue := createReferenceAPIIssue(t, handler, "CORE")
	sent := &backendStatements{}
	jitOn := storetest.Reopened(t, database, func(config *pgxpool.Config) {
		config.MaxConns = 1
		config.MinConns = 0
		config.MinIdleConns = 0
		config.ConnConfig.RuntimeParams["jit"] = "on"
		config.ConnConfig.Tracer = sent
	})
	readDeps := deps
	readDeps.Store = jitOn
	routes := http.NewServeMux()
	// Registered after the one-connection store, so this server's wait runs before that store
	// closes.
	t.Cleanup(Register(routes, t.Context(), readDeps).Wait)
	if response := issueReferencesRequest(t, routes, issue.Key, ""); response.Code != http.StatusOK {
		t.Fatalf("references of %s on a session that starts at jit = on: status=%d body=%s, want 200", issue.Key, response.Code, response.Body.String())
	}
	readBackend := closureTransactionBackend(t, sent.all())
	var jit string
	var backend int32
	if err := jitOn.Pool.QueryRow(context.Background(), "select current_setting('jit'), pg_backend_pid()").Scan(&jit, &backend); err != nil {
		t.Fatalf("read jit on the pool after the read: %v", err)
	}
	if uint32(backend) != readBackend {
		t.Fatalf("the session after the read is backend %d, want the read's own, backend %d", backend, readBackend)
	}
	if jit != "on" {
		t.Errorf("jit on the read's own session after the read reads %q, want on, the value its sessions start with", jit)
	}
}

// closureTransactionBackend requires of sent, every statement a pool's connections sent in order,
// that the statements of the closure, each starting with refs.ClosureTag, ran on one backend in
// one transaction, and that the first statement of that transaction after its begin is the
// closure's set local jit = off. It answers that backend.
func closureTransactionBackend(t *testing.T, sent []backendStatement) uint32 {
	t.Helper()
	const jitOff = refs.ClosureTag + "set local jit = off"
	isClosure := func(statement backendStatement) bool { return strings.HasPrefix(statement.sql, refs.ClosureTag) }
	first := slices.IndexFunc(sent, isClosure)
	if first < 0 {
		t.Fatalf("the route sent no statement of the closure, none starting with refs.ClosureTag:\n%s", backendStatementLines(sent))
	}
	backend := sent[first].backend
	begun, closureBegun := -1, -1
	for index, statement := range sent {
		closure := isClosure(statement)
		if statement.backend != backend {
			if closure {
				t.Fatalf("statement %d, of the closure, ran on backend %d, and the closure's first on backend %d:\n%s", index, statement.backend, backend, backendStatementLines(sent))
			}
			continue
		}
		switch {
		case strings.HasPrefix(statement.sql, "begin"):
			begun = index
		case statement.sql == "commit" || statement.sql == "rollback":
			begun = -1
		case !closure:
		case begun < 0:
			t.Fatalf("statement %d, of the closure, ran in no transaction: the route ran refs.Closure outside inClosureRead's transaction, where nothing sets jit off for it:\n%s", index, backendStatementLines(sent))
		case closureBegun < 0:
			closureBegun = begun
		case begun != closureBegun:
			t.Fatalf("statement %d, of the closure, ran in the transaction begun at statement %d, and the closure's first in the one begun at statement %d, want one transaction:\n%s", index, begun, closureBegun, backendStatementLines(sent))
		}
	}
	next := closureBegun + 1
	for sent[next].backend != backend {
		next++
	}
	if sent[next].sql != jitOff {
		t.Fatalf("statement %d, the first of the closure's transaction after its begin at statement %d, is %q, want %q, before any other statement of the closure:\n%s", next, closureBegun, sent[next].sql, jitOff, backendStatementLines(sent))
	}
	return backend
}

// backendStatement is one statement a connection sent: the backend it ran on and its SQL.
type backendStatement struct {
	backend uint32
	sql     string
}

// backendStatementLines is sent one to a line, each with its index and backend and its SQL cut to
// 120 bytes, for a failure to show.
func backendStatementLines(sent []backendStatement) string {
	lines := make([]string, len(sent))
	for index, statement := range sent {
		sql := strconv.Quote(statement.sql)
		if len(sql) > 120 {
			sql = sql[:120] + "..."
		}
		lines[index] = fmt.Sprintf("%d backend %d: %s", index, statement.backend, sql)
	}
	return shownLines(lines)
}

// backendStatements records each statement the connections of the pool it traces send, with the
// backend each ran on, in the order they started (pgx.QueryTracer).
type backendStatements struct {
	mu   sync.Mutex
	sent []backendStatement
}

func (b *backendStatements) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, backendStatement{backend: conn.PgConn().PID(), sql: data.SQL})
	return ctx
}

func (b *backendStatements) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// all is every statement b has recorded, in the order they started.
func (b *backendStatements) all() []backendStatement {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.sent)
}
