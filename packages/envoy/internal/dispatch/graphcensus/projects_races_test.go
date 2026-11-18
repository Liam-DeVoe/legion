package graphcensus

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

func TestTheObjectBatchDistinguishesReplacedRowsWithTheSameCreationTime(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	seedIdentityIssue(t, database, at, model.Actor{Kind: "user", ID: "alice"})
	first := insertIdentityDocument(t, database, "alice", at)
	insertCreateEvent(t, database, 1, first)
	kind := objectKindOf(t, graphstore.KindArtifact)
	batch, err := readObjectNodes(ctx, database, kind.kind, kind.read, kind.start, backfillBatch)
	if err != nil || len(batch) != 1 {
		t.Fatalf("read the first row: %d node(s), %v; want 1", len(batch), err)
	}
	if _, err := database.Pool.Exec(ctx, `delete from artifacts where id = $1`, first); err != nil {
		t.Fatal(err)
	}
	second := insertIdentityDocument(t, database, "carol", at)
	insertCreateEvent(t, database, 2, second)
	written, err := writeObjectBatch(ctx, database, kind, batch)
	if err != nil {
		t.Fatal(err)
	}
	if written.Nodes != 0 {
		t.Errorf("the stale batch wrote %d node(s) for a different document UUID with the same created_at; want none", written.Nodes)
	}
	if node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, "IDEN-1/notes-md"); err != nil || held {
		t.Errorf("the stale batch left node %+v (held %t, %v), want no node describing the removed row", node, held, err)
	}
}

func TestAStaleCommentBatchDoesNotRestoreAnErasedComment(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	seedIdentityIssue(t, database, at, model.Actor{Kind: "user", ID: "alice"})
	const id = "11111111-1111-4111-8111-111111111111"
	if _, err := database.Pool.Exec(ctx, `
		insert into comments (id, issue_key, author, body, created_at)
		values ($1, 'IDEN-1', '{}', 'A remark.', $2)
	`, id, at); err != nil {
		t.Fatal(err)
	}
	batch, err := readCommentRows(ctx, database, zeroUUID)
	if err != nil || len(batch) != 1 {
		t.Fatalf("read the unmarked comment: %d row(s), %v; want 1", len(batch), err)
	}
	if _, _, err := writeCommentBatch(ctx, database, batch); err != nil {
		t.Fatalf("write the first run's batch: %v", err)
	}
	// The erase takes the issue owner, deletes the stamped row, then erases its node and tuples.
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select 1 from issues where key = 'IDEN-1' for no key update`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select 1 from graph_nodes where kind = 'comment' and id = $1 for update`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from comments where id = $1`, id); err != nil {
			return err
		}
		var seqs []int64
		if err := tx.QueryRow(ctx, `select array_agg(seq) from graph_tuples where from_kind = 'comment' and from_id = $1`, id).Scan(&seqs); err != nil {
			return err
		}
		return graphstore.EraseNode(ctx, tx, graphstore.KindComment, id, seqs)
	}); err != nil {
		t.Fatalf("erase the first run's comment: %v", err)
	}
	if _, _, err := writeCommentBatch(ctx, database, batch); err != nil {
		t.Fatalf("write the second run's cached batch: %v", err)
	}
	if node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindComment, id); err != nil || held {
		t.Errorf("the stale batch restored node %+v (held %t, %v) without its erased comment row; want no node", node, held, err)
	}
}
