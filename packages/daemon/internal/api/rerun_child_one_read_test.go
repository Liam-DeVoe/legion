package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/credential"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// countingRecords counts the issues a route reads one at a time (Store.Issue).
type countingRecords struct {
	record.Store
	issues atomic.Int64
}

func (c *countingRecords) Issue(ctx context.Context, tx pgx.Tx, key string) (*record.Issue, error) {
	c.issues.Add(1)
	return c.Store.Issue(ctx, tx, key)
}

// rerun_child judges the child from one read of its own record, which authorizes the request, and
// the Dispatch chain it reads beside it (record.Chains), never the tree a record stores: the chain
// carries the accepted issue above the child the refusal names (record.AcceptedAncestorIn).
func TestRerunChildJudgesTheTreeFromOneRead(t *testing.T) {
	h, handlers, statuses := statusHarness(t)
	counting := &countingRecords{Store: record.NewStore()}
	h.handler = NewServer("127.0.0.1", 8437, Options{
		Supervisor: h.supervisor, BootTokens: h.tokens, Project: testProject, DispatchProject: testDispatchProject,
		OperatorToken: testOperatorToken, Controller: h.store, Grants: credential.New(nil), Pool: h.store.Pool(),
		Record: counting, Handlers: handlers, Dispatch: statuses,
	}).Handler
	architect := newLiveClaim(t, h, statusRoot, claim.RoleArchitect)
	if recorder := h.request(http.MethodPost, "/legion/v1/children/park", map[string]any{"grantId": architect.grant(t), "issue": statusChild}, nil); recorder.Code != http.StatusOK {
		t.Fatalf("park = %d %s", recorder.Code, recorder.Body)
	}
	acceptPhase(t, h, statusRoot, phase.Merging)

	issues := counting.issues.Load()
	recorder := h.request(http.MethodPost, "/legion/v1/children/rerun", map[string]any{"grantId": architect.grant(t), "issue": statusChild}, nil)
	refused(t, recorder, http.StatusConflict, "CHILD_UNDER_ACCEPTED_PARENT")
	if read := counting.issues.Load() - issues; read != 1 {
		t.Fatalf("the refused rerun read %d issues one at a time, want 1: the child's own record, which authorizes the request", read)
	}
}
