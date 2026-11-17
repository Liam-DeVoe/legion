package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// rollbackEdge is one edge graph_edges answers, by its ends and its kind.
type rollbackEdge struct{ fromKind, fromID, kind, toKind, toID string }

// viewAnswers is how many rows graph_edges answers edge with, and the created_at of the last.
func viewAnswers(t *testing.T, database *store.Store, edge rollbackEdge) (int, time.Time) {
	t.Helper()
	var rows int
	var at *time.Time
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*), max(created_at) from graph_edges
		where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
	`, edge.fromKind, edge.fromID, edge.kind, edge.toKind, edge.toID).Scan(&rows, &at); err != nil {
		t.Fatalf("read %v out of graph_edges: %v", edge, err)
	}
	if at == nil {
		return rows, time.Time{}
	}
	return rows, *at
}

// outboundAnswers is how many edges of kind GET /api/v1/references?from=<issue> answers to target.
func outboundAnswers(t *testing.T, handler http.Handler, issue, kind, targetKind, target string) int {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/references?from="+url.QueryEscape("dispatch://"+issue), nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the references out of %s: status=%d body=%s", issue, response.Code, response.Body.String())
	}
	answered := 0
	for _, edge := range decodeBody[graphReferencesRead](t, response).Edges {
		if edge.Kind == kind && edge.Node.Kind == targetKind && edge.Node.ID == target {
			answered++
		}
	}
	return answered
}

// TestGraphEdgesKeepsEveryRelationAcrossARollback holds the arms of dispatch://INFRA-1253's split
// of the issue relations (c-view-arms): the arms over issues (child_of), issue_links (blocked_by)
// and artifacts (attached_to) answer the rows that name no node, and the store answers the rest
// while the projection row that names each edge carries its node. A database is seeded as the
// release before this one leaves it: beside a stamped issue link and a stamped parent written
// through this build's routes, a link and a parent that release wrote naming no node, a stamped
// link and a stamped parent that release deleted with no retraction, and a document on an issue it
// wrote naming no node. graph_edges, refs.BacklinkCounts and the outbound references read each
// answer every standing relation once and every deleted one nowhere; graph_edges names the issue
// link blocked_by, as a process of the release before reads it, in its old arm and in the tuple
// arm, and the references read answers it as needs. After this child's backfill each relation the
// release before wrote is answered once, by the store, dated at its row's own created_at, and the
// old arms answer none of them. The cases of the component arms, an artifact's citations and the
// deletion of dispatch://INFRA-1149's four arms are T8b's, T8c's and T9's.
func TestGraphEdgesKeepsEveryRelationAcrossARollback(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
	ctx := context.Background()
	keys := createListedIssues(t, handler, "ROLL", 4)
	parent, target, oldWaiter, oldChild := keys[0], keys[1], keys[2], keys[3]
	create := func(title string, fields map[string]any) model.Issue {
		t.Helper()
		body := map[string]any{"project": "ROLL", "title": title, "force": true}
		for name, value := range fields {
			body[name] = value
		}
		response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", body, "alice")
		if response.Code != http.StatusCreated {
			t.Fatalf("create %q: status=%d body=%s", title, response.Code, response.Body.String())
		}
		return decodeBody[model.Issue](t, response)
	}
	stamped := create("Stamped: waits on the target under the parent", map[string]any{"parent": parent, "needs": []string{target}})
	deletedLink := create("Its link deleted by the release before", map[string]any{"needs": []string{target}})
	deletedParent := create("Its parent cleared by the release before", map[string]any{"parent": parent})

	// What the release before writes and deletes, by SQL, as it writes with no node and deletes
	// with no retraction.
	for _, statement := range []struct {
		what, sql string
		args      []any
	}{
		{"its link, naming no node", `insert into issue_links (issue_key, kind, target_key, created_at) values ($1, 'blocked_by', $2, now() - interval '1 day')`, []any{oldWaiter, target}},
		{"its parent, on a row naming no node", `update issues set parent_key = $2, node_kind = null, node_address = null where key = $1`, []any{oldChild, parent}},
		{"its delete of a stamped link", `delete from issue_links where issue_key = $1 and target_key = $2`, []any{deletedLink.Key, target}},
		{"its clear of a stamped parent", `update issues set parent_key = null where key = $1`, []any{deletedParent.Key}},
	} {
		if _, err := database.Pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("write %s as the release before: %v", statement.what, err)
		}
	}
	var document string
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, created_by, created_at)
		values ($1, 'ROLL', 'old-notes-md', 'old-notes.md', 'doc', '{"kind":"user","id":"alice"}', now() - interval '1 day')
		returning id::text
	`, stamped.Key).Scan(&document); err != nil {
		t.Fatalf("write a document as the release before: %v", err)
	}

	standing := []rollbackEdge{
		{"issue", stamped.Key, "blocked_by", "issue", target},
		{"issue", oldWaiter, "blocked_by", "issue", target},
		{"issue", stamped.Key, "child_of", "issue", parent},
		{"issue", oldChild, "child_of", "issue", parent},
		{"artifact", document, "attached_to", "issue", stamped.Key},
	}
	deleted := []rollbackEdge{
		{"issue", deletedLink.Key, "blocked_by", "issue", target},
		{"issue", deletedParent.Key, "child_of", "issue", parent},
	}
	read := func(stage string) {
		t.Helper()
		for _, edge := range standing {
			if rows, _ := viewAnswers(t, database, edge); rows != 1 {
				t.Errorf("%s graph_edges answers %v %d times, want once", stage, edge, rows)
			}
		}
		for _, edge := range deleted {
			if rows, _ := viewAnswers(t, database, edge); rows != 0 {
				t.Errorf("%s graph_edges answers %v, which the release before deleted, %d times, want none", stage, edge, rows)
			}
		}
		alice := model.Actor{Kind: "user", ID: "alice"}
		counts, err := refs.BacklinkCounts(ctx, database.Pool, alice, "issue", []string{target, parent}, []string{"attached_to"})
		if err != nil {
			t.Fatalf("%s count the backlinks: %v", stage, err)
		}
		// The target is waited on by the stamped issue and the release before's waiter; the parent
		// holds the stamped child and the release before's child. Each holds its own spec too,
		// which the attached_to count reads below.
		if counts[target] != 2 || counts[parent] != 2 {
			t.Errorf("%s the backlink counts are %v, want 2 at %s and 2 at %s", stage, counts, target, parent)
		}
		attached, err := refs.BacklinkCounts(ctx, database.Pool, alice, "issue", []string{stamped.Key}, nil)
		if err != nil {
			t.Fatalf("%s count the attachments: %v", stage, err)
		}
		if attached[stamped.Key] != 2 {
			t.Errorf("%s %s counts %d backlinks, want its spec and the release before's document", stage, stamped.Key, attached[stamped.Key])
		}
		for _, outbound := range []struct {
			issue, kind, targetKind, target string
			want                            int
		}{
			{stamped.Key, model.EdgeKindNeeds, "issue", target, 1},
			{oldWaiter, model.EdgeKindNeeds, "issue", target, 1},
			{deletedLink.Key, model.EdgeKindNeeds, "issue", target, 0},
			{stamped.Key, "child_of", "issue", parent, 1},
			{oldChild, "child_of", "issue", parent, 1},
			{deletedParent.Key, "child_of", "issue", parent, 0},
		} {
			if got := outboundAnswers(t, handler, outbound.issue, outbound.kind, outbound.targetKind, outbound.target); got != outbound.want {
				t.Errorf("%s GET /api/v1/references?from=%s answers %s to %s %d times, want %d", stage, outbound.issue, outbound.kind, outbound.target, got, outbound.want)
			}
		}
		if got := outboundAnswers(t, handler, stamped.Key, "blocked_by", "issue", target); got != 0 {
			t.Errorf("%s the references read answers the wait as blocked_by %d times, want it answered as needs alone", stage, got)
		}
	}
	read("before the backfill:")

	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	read("after the backfill:")
	for _, written := range []struct {
		edge              rollbackEdge
		createdAt, oldArm string
		args              []any
	}{
		{standing[1], `select created_at from issue_links where issue_key = $1 and target_key = $2`,
			`select count(*) from issue_links where issue_key = $1 and target_key = $2 and node_kind is null`, []any{oldWaiter, target}},
		{standing[3], `select created_at from issues where key = $1`,
			`select count(*) from issues where key = $1 and node_kind is null`, []any{oldChild}},
		{standing[4], `select created_at from artifacts where id = $1::uuid`,
			`select count(*) from artifacts where id = $1::uuid and node_kind is null`, []any{document}},
	} {
		var createdAt time.Time
		if err := database.Pool.QueryRow(ctx, written.createdAt, written.args...).Scan(&createdAt); err != nil {
			t.Fatalf("read the row behind %v: %v", written.edge, err)
		}
		if _, at := viewAnswers(t, database, written.edge); !at.Equal(createdAt) {
			t.Errorf("after the backfill graph_edges dates %v at %s, want its row's created_at %s", written.edge, at, createdAt)
		}
		var unmarked int
		if err := database.Pool.QueryRow(ctx, written.oldArm, written.args...).Scan(&unmarked); err != nil {
			t.Fatalf("read the marker behind %v: %v", written.edge, err)
		}
		if unmarked != 0 {
			t.Errorf("after the backfill the row behind %v names no node, so its old arm still answers it", written.edge)
		}
	}
}
