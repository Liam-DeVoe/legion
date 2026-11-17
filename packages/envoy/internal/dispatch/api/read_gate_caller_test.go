package api

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// tokenLookups counts the reads of a personal token's row (personalTokenActor) sent on a
// recording context (recordingKey): one per verification of a `dsp_` credential, so a test
// counts one request's own and none of the server's background work.
type tokenLookups struct{ sent atomic.Int64 }

func (l *tokenLookups) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	statement := strings.TrimSpace(data.SQL)
	if ctx.Value(recordingKey{}) != nil && strings.HasPrefix(statement, "select") &&
		strings.Contains(statement, "from agent_tokens") && strings.Contains(statement, "where token_hash = $1") {
		l.sent.Add(1)
	}
	return ctx
}

func (l *tokenLookups) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestAGETVerifiesItsCredentialOnce: a GET row whose classes need the viewer is read behind
// withinReadScope, which resolves the caller once and keeps it in the request's context
// (withCallerResolved), so a handler behind it that resolves the caller again - through
// requireAuthenticated, readerOf, artifactReaderOf or actingPerson - reads the kept caller and
// verifies the credential no second time. Each read below is made with a personal token, whose
// verification reads its agent_tokens row, and each must read that row exactly once.
//
// Control: a gate that resolves the viewer without keeping the caller (readerOf alone) verifies
// the token again in each handler that asks, and the count of each read below is 2 or 3.
func TestAGETVerifiesItsCredentialOnce(t *testing.T) {
	lookups := &tokenLookups{}
	seed := seedPrivateProjectWith(t, testServerOptions{store: storetest.TracedStore(t, storetest.Open(t), lookups)})
	issue := "/api/v1/issues/" + seed.issue[seedPublic]
	const unheld = "/api/v1/artifacts/00000000-0000-4000-8000-000000000000"
	for _, read := range []struct {
		target string
		status int
	}{
		{issue, http.StatusOK},
		{issue + "/comments", http.StatusOK},
		{issue + "/asks", http.StatusOK},
		{unheld, http.StatusNotFound},
		{unheld + "/versions/1", http.StatusNotFound},
	} {
		before := lookups.sent.Load()
		response := seed.requestIn(t, context.WithValue(t.Context(), recordingKey{}, true), callerCarolToken, http.MethodGet, read.target, nil)
		if response.Code != read.status {
			t.Fatalf("GET %s with carol's token = %d %s, want %d", read.target, response.Code, response.Body.String(), read.status)
		}
		if verified := lookups.sent.Load() - before; verified != 1 {
			t.Errorf("GET %s read its personal token's row %d times, want once", read.target, verified)
		}
	}
}

// TestWhoamiVerifiesItsCredentialOnce: GET /api/v1/whoami, a row of the caller's own class that no
// gate resolves, verifies the caller once and reads the viewer of a credential that lists
// projects from that one resolution (whoamiPrivateProjects), so a token's row is read once.
//
// Control: a whoamiPrivateProjects that resolves the request's reader afresh (resolveReader)
// verifies the token a second time, and the count is 2.
func TestWhoamiVerifiesItsCredentialOnce(t *testing.T) {
	lookups := &tokenLookups{}
	seed := seedPrivateProjectWith(t, testServerOptions{store: storetest.TracedStore(t, storetest.Open(t), lookups)})
	before := lookups.sent.Load()
	response := seed.requestIn(t, context.WithValue(t.Context(), recordingKey{}, true), callerCarolPrivToken, http.MethodGet, "/api/v1/whoami", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"private_projects":["`+seedPrivate+`"]`) {
		t.Fatalf("GET /api/v1/whoami with carol's token that lists PRIV = %d %s, want 200 naming PRIV private", response.Code, response.Body.String())
	}
	if verified := lookups.sent.Load() - before; verified != 1 {
		t.Errorf("GET /api/v1/whoami read its personal token's row %d times, want once", verified)
	}
}
