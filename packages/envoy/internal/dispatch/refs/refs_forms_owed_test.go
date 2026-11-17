package refs

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/text"
)

// formsOwed is every row of the forms queue as "kind id", in kind then id order.
func formsOwed(t *testing.T, q Queryer) []string {
	t.Helper()
	rows, err := q.Query(context.Background(), `
		select kind || ' ' || id::text from refs_forms_owed order by kind, id
	`)
	if err != nil {
		t.Fatalf("read refs_forms_owed: %v", err)
	}
	owed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("scan refs_forms_owed: %v", err)
	}
	return owed
}

// edgesFrom is every refs row of one source as "kind id", sorted.
func edgesFrom(t *testing.T, q Queryer, fromKind, fromID string) []string {
	t.Helper()
	rows, err := q.Query(context.Background(), `
		select to_kind || ' ' || to_id from refs where from_kind = $1 and from_id = $2 order by 1
	`, fromKind, fromID)
	if err != nil {
		t.Fatalf("read the edges of %s %s: %v", fromKind, fromID, err)
	}
	edges, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("scan the edges of %s %s: %v", fromKind, fromID, err)
	}
	return edges
}

// A write this build makes owes the forms queue nothing, the delete of an edge its new text no
// longer cites included: the row's own trigger enqueues the source, that delete owes it again
// (migration 20261117025906), and the write's settle, after both, deletes the row. A bare delete
// of the same edges - which is what an older build's re-index of the text is at the database, its
// scope drain's and its block index's included - leaves the source owed.
func TestAWriteLeavesNothingOwedInTheFormsQueue(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	createReferenceProject(t, tx, "CORE")
	createReferenceIssue(t, tx, "CORE-1", "CORE")
	comment := createReferenceComment(t, tx, "CORE-1")
	if owed := formsOwed(t, tx); !slices.Equal(owed, []string{"comment " + comment}) {
		t.Fatalf("the comment's own write owes %v, want the comment alone", owed)
	}

	write := func(body string) {
		t.Helper()
		if _, err := ReplaceCounted(ctx, tx, NewReader(ctx, tx, "comment", comment, refsGrammarServer), body); err != nil {
			t.Fatalf("index the comment as %q: %v", body, err)
		}
	}
	write("The plan: dispatch://CORE-1/plan.")
	if edges := edgesFrom(t, tx, "comment", comment); !slices.Equal(edges, []string{"issue CORE-1"}) {
		t.Fatalf("the comment's edges are %v, want the tab address's issue", edges)
	}
	if owed := formsOwed(t, tx); len(owed) != 0 {
		t.Fatalf("the queue holds %v after the write indexed its own text, want it empty", owed)
	}
	write("Nothing cited now.")
	if edges := edgesFrom(t, tx, "comment", comment); len(edges) != 0 {
		t.Fatalf("the comment's edges are %v after an edit that cites nothing, want none", edges)
	}
	if owed := formsOwed(t, tx); len(owed) != 0 {
		t.Fatalf("the queue holds %v after a write that deleted an edge, want it empty", owed)
	}

	// A document version is the same: its write deletes the edge its new text drops, and settles.
	document, _ := createReferenceArtifact(t, tx, "CORE-1", "CORE", "spec")
	versionAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for version, markdown := range []string{"See https://dispatch.example/issues/CORE-1/plan.", "Nothing cited now."} {
		if _, err := tx.Exec(ctx, `
			insert into artifact_versions (artifact_id, number, markdown, created_at) values ($1, $2, $3, $4)
		`, document, version+1, markdown, versionAt); err != nil {
			t.Fatalf("write version %d: %v", version+1, err)
		}
		reader := NewReader(ctx, tx, "artifact", document, refsGrammarServer)
		if _, err := ReplaceDocumentCounted(ctx, tx, document, version+1, versionAt,
			text.Refs(reader.Mentions(markdown)), nil, nil); err != nil {
			t.Fatalf("index version %d: %v", version+1, err)
		}
		if owed := formsOwed(t, tx); len(owed) != 0 {
			t.Fatalf("the queue holds %v after version %d indexed its own text, want it empty", owed, version+1)
		}
	}

	// What an older build's re-index deletes is owed until a build that reads these forms indexes
	// the text again.
	insertReference(t, tx, "comment", comment, "issue", "CORE-1")
	insertReference(t, tx, "artifact", document, "issue", "CORE-1")
	if _, err := tx.Exec(ctx, `delete from refs where from_id = any($1)`, []string{comment, document}); err != nil {
		t.Fatalf("delete the edges as an older build's re-index does: %v", err)
	}
	if owed, want := formsOwed(t, tx), []string{"artifact " + document, "comment " + comment}; !slices.Equal(owed, want) {
		t.Fatalf("after a bare delete the queue holds %v, want %v", owed, want)
	}
}

// The forms queue is drained by the one list, settle and re-index pair every queue is
// (refs.ListOwed, refs.ReindexOwed): a comment an older build wrote is re-indexed with this
// build's grammar, so the tab address it writes is an edge, and both queues' rows are settled in
// that transaction. A row a write moves between the listing and the re-index is left owed, with
// nothing written for it.
func TestTheFormsQueueDrainReindexesWithThisBuildsGrammar(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedRefsGrammarIssue(t, database)
	raw := func(body string) string {
		t.Helper()
		var id string
		if err := database.Pool.QueryRow(ctx, `
			insert into comments (issue_key, author, body) values ('CORE-1', '{"kind":"user","id":"alice"}', $1)
			returning id::text
		`, body).Scan(&id); err != nil {
			t.Fatalf("write the comment %q: %v", body, err)
		}
		return id
	}
	comment := raw("The plan: dispatch://CORE-1/plan.")
	listed, err := ListOwed(ctx, database.Pool, RefsFormsOwed, 10)
	if err != nil {
		t.Fatalf("list what the forms queue owes: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != comment || listed[0].Queue != RefsFormsOwed {
		t.Fatalf("owed = %#v, want the comment alone, of the forms queue", listed)
	}

	if indexed, err := ReindexOwed(ctx, database.Pool, listed[0], refsGrammarServer); err != nil || !indexed {
		t.Fatalf("re-index the comment = %v (%v), want it indexed", indexed, err)
	}

	if edges := edgesFrom(t, database.Pool, "comment", comment); !slices.Equal(edges, []string{"issue CORE-1"}) {
		t.Fatalf("the comment's edges are %v, want the tab address's issue", edges)
	}
	if owed := formsOwed(t, database.Pool); len(owed) != 0 {
		t.Fatalf("the forms queue holds %v after the re-index, want it empty", owed)
	}
	if owed := owedRows(t, database.Pool); len(owed) != 0 {
		t.Fatalf("the whole grammar's queue holds %v after the re-index, want it settled with the forms queue", owed)
	}

	moved := raw("The verification tab: dispatch://CORE-1/verification.")
	listed, err = ListOwed(ctx, database.Pool, RefsFormsOwed, 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list what the forms queue owes = %#v (%v), want the second comment", listed, err)
	}
	if _, err := database.Pool.Exec(ctx, `
		update refs_forms_owed set enqueued_at = enqueued_at + interval '1 second' where kind = 'comment' and id = $1
	`, moved); err != nil {
		t.Fatalf("move the comment's enqueued_at: %v", err)
	}
	if indexed, err := ReindexOwed(ctx, database.Pool, listed[0], refsGrammarServer); err != nil || indexed {
		t.Fatalf("re-index a comment a write moved = %v (%v), want it left owed", indexed, err)
	}
	if owed := formsOwed(t, database.Pool); !slices.Equal(owed, []string{"comment " + moved}) {
		t.Fatalf("the forms queue holds %v, want the moved comment still owed", owed)
	}
	if edges := edgesFrom(t, database.Pool, "comment", moved); len(edges) != 0 {
		t.Fatalf("the drain wrote %v from text it left owed, want nothing", edges)
	}
}

// A source that is gone has its edges deleted by the drain and its row settled after that delete,
// which owes the forms queue the source again (migration 20261117025906): it is left owing
// nothing.
func TestTheFormsQueueDrainOfAGoneSourceLeavesNothingOwed(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedRefsGrammarIssue(t, database)
	var comment string
	if err := database.Pool.QueryRow(ctx, `
		insert into comments (issue_key, author, body) values ('CORE-1', '{"kind":"user","id":"alice"}', 'See CORE-1.')
		returning id::text
	`).Scan(&comment); err != nil {
		t.Fatalf("write the comment: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into refs (from_kind, from_id, to_kind, to_id) values ('comment', $1, 'issue', 'CORE-1');
	`, comment); err != nil {
		t.Fatalf("write the comment's edge: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `delete from comments where id = $1`, comment); err != nil {
		t.Fatalf("delete the comment: %v", err)
	}
	listed, err := ListOwed(ctx, database.Pool, RefsFormsOwed, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != comment {
		t.Fatalf("list what the forms queue owes = %#v (%v), want the gone comment", listed, err)
	}

	if indexed, err := ReindexOwed(ctx, database.Pool, listed[0], refsGrammarServer); err != nil || !indexed {
		t.Fatalf("drain the gone comment = %v (%v), want it drained", indexed, err)
	}

	if edges := edgesFrom(t, database.Pool, "comment", comment); len(edges) != 0 {
		t.Fatalf("the gone comment keeps edges %v, want none", edges)
	}
	if owed := formsOwed(t, database.Pool); len(owed) != 0 {
		t.Fatalf("the forms queue holds %v after the drain deleted a gone source's edges, want it empty", owed)
	}
}

// A document's removal deletes its edges and then what it owes every queue, so the delete's own
// debt to the forms queue goes with it; the same delete, bare, leaves the document owed.
func TestForgetDocumentLeavesNothingOwedInTheFormsQueue(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	createReferenceProject(t, tx, "CORE")
	createReferenceIssue(t, tx, "CORE-1", "CORE")
	document, _ := createReferenceArtifact(t, tx, "CORE-1", "CORE", "notes")
	if _, err := tx.Exec(ctx, `insert into artifact_versions (artifact_id, number, markdown) values ($1, 1, 'See CORE-1.')`, document); err != nil {
		t.Fatalf("write version 1: %v", err)
	}
	insertReference(t, tx, "artifact", document, "issue", "CORE-1")

	if err := ForgetDocument(ctx, tx, document); err != nil {
		t.Fatalf("forget the document: %v", err)
	}

	if edges := edgesFrom(t, tx, "artifact", document); len(edges) != 0 {
		t.Fatalf("the removed document keeps edges %v, want none", edges)
	}
	if owed := formsOwed(t, tx); len(owed) != 0 {
		t.Fatalf("the forms queue holds %v after the removal, want it empty", owed)
	}
	if owed := owedRows(t, tx); len(owed) != 0 {
		t.Fatalf("the whole grammar's queue holds %v after the removal, want it empty", owed)
	}
	insertReference(t, tx, "artifact", document, "issue", "CORE-1")
	if _, err := tx.Exec(ctx, `delete from refs where from_kind = 'artifact' and from_id = $1`, document); err != nil {
		t.Fatalf("delete the document's edges bare: %v", err)
	}
	if owed := formsOwed(t, tx); !slices.Equal(owed, []string{"artifact " + document}) {
		t.Fatalf("after a bare delete the forms queue holds %v, want the document owed", owed)
	}
}

// rebuild-refs re-indexes every source with this build's grammar and settles each one's forms row
// after its own delete, and its orphan sweep, whose delete owes the forms queue every gone source
// it sweeps, settles those after: nothing is left owed.
func TestRebuildRefsLeavesNothingOwedInTheFormsQueue(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedRefsGrammarIssue(t, database)
	var comment, gone string
	if err := database.Pool.QueryRow(ctx, `
		insert into comments (issue_key, author, body) values ('CORE-1', '{"kind":"user","id":"alice"}', 'The plan: dispatch://CORE-1/plan.')
		returning id::text
	`).Scan(&comment); err != nil {
		t.Fatalf("write the comment: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&gone); err != nil {
		t.Fatalf("draw a uuid: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into refs (from_kind, from_id, to_kind, to_id) values
			('comment', $1, 'issue', 'CORE-9'),
			('comment', $2, 'issue', 'CORE-1')
	`, comment, gone); err != nil {
		t.Fatalf("write a stale edge and an orphan: %v", err)
	}

	report, err := RebuildAll(ctx, database.Pool, refsGrammarServer)
	if err != nil {
		t.Fatalf("rebuild-refs: %v", err)
	}

	if report.Comments != 1 || report.Orphans != 1 {
		t.Fatalf("rebuild = %#v, want the comment re-indexed and the orphan swept", report)
	}
	if edges := edgesFrom(t, database.Pool, "comment", comment); !slices.Equal(edges, []string{"issue CORE-1"}) {
		t.Fatalf("the comment's edges are %v after rebuild-refs, want the tab address's issue alone", edges)
	}
	if owed := formsOwed(t, database.Pool); len(owed) != 0 {
		t.Fatalf("the forms queue holds %v after rebuild-refs, want it empty", owed)
	}
}
