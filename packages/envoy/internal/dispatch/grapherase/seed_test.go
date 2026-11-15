package grapherase

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// commentNodeKind is what the node and tuple store calls a comment (migration 20261109051232), the
// kind of the node most of this package's tests erase.
const commentNodeKind = "comment"

// seedWriter is who seedAgreeingStore states the blocks' parents by: the backfill's own writer
// (graphcensus's backfillActor), which writes every other fact of the seed.
var seedWriter = model.Actor{Kind: "system", ID: "dispatch-backfill-graph-store"}

// preStoreDocument is one document as a build before the node and tuple store left it: its id,
// its address, and one document_blocks row per block that names no node.
type preStoreDocument struct {
	artifactID, refKey string
}

// seedPreStoreDocument writes issue CORE-1 with one document at version 1 and a document_blocks
// row for each of blockIDs, each naming no node, which is what the backfill reads. The version
// carries markdown, so migration 0122's trigger gives the document its document_block_versions
// row, as a version gives one in production.
func seedPreStoreDocument(t *testing.T, database *store.Store, blockIDs ...string) preStoreDocument {
	t.Helper()
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('CORE', 'Core', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank) values
			('CORE-1', 'CORE', 1, 'One', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("seed issue CORE-1: %v", err)
	}
	var document preStoreDocument
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ('CORE-1', 'CORE', 'spec', 'spec.md', 'doc', false, '{"kind":"user","id":"alice"}')
		returning id::text, ref_key
	`).Scan(&document.artifactID, &document.refKey); err != nil {
		t.Fatalf("create the document: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1, 1, 'Written before the store existed.', '[{"kind":"user","id":"alice"}]')
	`, document.artifactID); err != nil {
		t.Fatalf("write version 1 of the document: %v", err)
	}
	for _, blockID := range blockIDs {
		if _, err := database.Pool.Exec(ctx, `
			insert into document_blocks (artifact_id, block_id, node_id, type, text_sha256, excerpt, changed_version, changed_at)
			values ($1::uuid, $2, $3, 'paragraph', '', $4, 1, now())
		`, document.artifactID, blockID, document.refKey+"#"+blockID, "Block "+blockID+"."); err != nil {
			t.Fatalf("write block %s of the document: %v", blockID, err)
		}
	}
	var owed bool
	if err := database.Pool.QueryRow(ctx, `
		select exists (select 1 from document_block_versions where artifact_id = $1)
	`, document.artifactID).Scan(&owed); err != nil || !owed {
		t.Fatalf("the document has no document_block_versions row to hold (err %v), so this test would hold nothing", err)
	}
	return document
}

// agreeingStore is a database whose store and projections agree on every kind the store census
// reads rows of: one document of two blocks, the inner one inside the outer one, one block
// reference out of the inner block, and one comment that the document owns and is anchored to,
// written by a person. outer and inner are the two blocks' node addresses, and comment the
// comment's id.
type agreeingStore struct {
	document              preStoreDocument
	outer, inner, comment string
}

// seedAgreeingStore writes the rows as a build before the store existed wrote them, runs the
// backfill over them (graphcensus.Backfill), and states each block's parent with the tuples and
// the column the block index's reconciliation writes (refs' reconcileBlockParents): the outer
// block's part_of to the document, the inner block's to the outer block, and the inner row's
// parent_block_id beside it. It is graphcensus's own seed of the same name, but for the census
// graphcensus takes after it, which is that package's to hold.
func seedAgreeingStore(t *testing.T, database *store.Store) agreeingStore {
	t.Helper()
	ctx := context.Background()
	document := seedPreStoreDocument(t, database, "outer", "inner")
	seeded := agreeingStore{
		document: document,
		outer:    document.refKey + "#outer",
		inner:    document.refKey + "#inner",
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into block_refs (artifact_id, block_id, from_node, to_kind, to_id, kind, created_version)
		values ($1::uuid, 'inner', $2, 'issue', 'CORE-1', 'mentions', 1)
	`, document.artifactID, seeded.inner); err != nil {
		t.Fatalf("write the inner block's reference to CORE-1: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		insert into comments (artifact_id, author, body, anchor)
		values ($1::uuid, '{"kind":"user","id":"alice"}', 'A remark on the spec.', jsonb_build_object('artifact_id', $1::text))
		returning id::text
	`, document.artifactID).Scan(&seeded.comment); err != nil {
		t.Fatalf("write the comment on the document: %v", err)
	}
	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill the store: %v", err)
	}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the parents' write: %v", err)
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	parent := func(address, toKind, toID string) graphstore.Tuple {
		return graphstore.Tuple{
			FromKind: refs.BlockNodeKind, FromID: address, Kind: graphstore.TuplePartOf,
			ToKind: toKind, ToID: toID, At: now, Who: seedWriter, SourceEvent: graphstore.NoEvent, Asserted: true,
		}
	}
	if err := graphstore.Write(ctx, tx, nil, []graphstore.Tuple{
		parent(seeded.outer, "artifact", document.refKey),
		parent(seeded.inner, refs.BlockNodeKind, seeded.outer),
	}); err != nil {
		t.Fatalf("state the blocks' parents: %v", err)
	}
	if tag, err := tx.Exec(ctx, `
		update document_blocks set parent_block_id = 'outer' where artifact_id = $1::uuid and block_id = 'inner'
	`, document.artifactID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("project the inner block's parent: %d rows, %v", tag.RowsAffected(), err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the parents' write: %v", err)
	}
	return seeded
}

// The two documents the owner-order test seeds, one on each of two issues, whose ids sort the
// other way round from their owner keys: issue:ACME-1 sorts before issue:CORE-1, and CORE-1's
// document's id before ACME-1's.
const (
	coreDocumentID = "00000000-0000-4000-8000-0000000000d1"
	acmeDocumentID = "00000000-0000-4000-8000-0000000000d2"
)

// seedIssueDocument writes project, its issue <project>-1, and one document of that issue under id
// at version 1 holding markdown. The version carries markdown, so migration 0122's trigger gives
// the document its document_block_versions row, as a version gives one in production.
func seedIssueDocument(t *testing.T, database *store.Store, project, id, markdown string) preStoreDocument {
	t.Helper()
	ctx := context.Background()
	issue := project + "-1"
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ($1, $1, 'alice')
	`, project); err != nil {
		t.Fatalf("seed project %s: %v", project, err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into issues (key, project_key, number, title, created_by, rank)
		values ($1, $2, 1, 'One', '{"kind":"user","id":"alice"}', 'U')
	`, issue, project); err != nil {
		t.Fatalf("seed issue %s: %v", issue, err)
	}
	document := preStoreDocument{artifactID: id}
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (id, issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ($1::uuid, $2, $3, 'spec', 'spec.md', 'doc', false, '{"kind":"user","id":"alice"}')
		returning ref_key
	`, id, issue, project).Scan(&document.refKey); err != nil {
		t.Fatalf("create %s's document: %v", issue, err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1::uuid, 1, $2, '[{"kind":"user","id":"alice"}]')
	`, id, markdown); err != nil {
		t.Fatalf("write version 1 of %s: %v", document.refKey, err)
	}
	return document
}

// seedPreStoreBlock writes block blockID of document as a document_blocks row naming no node,
// which is what the backfill reads.
func seedPreStoreBlock(t *testing.T, database *store.Store, document preStoreDocument, blockID string) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `
		insert into document_blocks (artifact_id, block_id, node_id, type, text_sha256, excerpt, changed_version, changed_at)
		values ($1::uuid, $2, $3, 'paragraph', '', $4, 1, now())
	`, document.artifactID, blockID, document.refKey+"#"+blockID, "Block "+blockID+"."); err != nil {
		t.Fatalf("write block %s of %s: %v", blockID, document.refKey, err)
	}
}
