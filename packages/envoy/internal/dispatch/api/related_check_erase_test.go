package api_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/grapherase"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// eraseNodeLockStatement is the statement by which a confirmed erase takes its node's row, right
// after its owner locks (grapherase's lockOwnersThenNode).
const eraseNodeLockStatement = "select supertag from graph_nodes where kind = $1 and id = $2 for update"

// R5, R17: a related-item check of a decision block records its terms, the lexemes it picked from
// the block's question, once the edit that added the block has committed, so its record can come
// after an erase of the block has read the block's checks. The record takes the owner lock of the
// block's issue first, which the erase holds until it commits, and writes no row for a block whose
// node graph_erasures names (insertRelatedChecks).
//
// The block is a decision block of a document of issue CORE-1, its question the text of its
// document_blocks row, and the backfill gives it its node. The test holds the block's node row
// while a confirmed erase of it runs, so the erase waits there holding CORE-1's owner lock. It
// then runs the record write of a check of the block, holding the question's lexemes, and reads
// that it waits at an owner lock; it lets the erase go, which commits, and the record write, once
// it ends, has written no related_checks row of the block.
func TestARelatedCheckRecordedBehindAnEraseOfItsBlockWritesNoRow(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	pool := database.Pool
	const question = "Should we ship the quokka7lighthouse release on Friday?"
	if _, err := pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('CORE', 'Core', 'alice');
		insert into issues (key, project_key, number, title, created_by, rank) values
			('CORE-1', 'CORE', 1, 'One', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("seed issue CORE-1: %v", err)
	}
	var document, refKey string
	if err := pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ('CORE-1', 'CORE', 'decisions', 'decisions.md', 'doc', false, '{"kind":"user","id":"alice"}')
		returning id::text, ref_key
	`).Scan(&document, &refKey); err != nil {
		t.Fatalf("create the document: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		values ($1::uuid, 1, 'Written before the store existed.', '[{"kind":"user","id":"alice"}]')
	`, document); err != nil {
		t.Fatalf("write version 1 of the document: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into document_blocks (artifact_id, block_id, node_id, type, text_sha256, excerpt, changed_version, changed_at)
		values ($1::uuid, 'decide', $2, 'paragraph', '', $3, 1, now())
	`, document, refKey+"#decide", question); err != nil {
		t.Fatalf("write the document's block: %v", err)
	}
	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill the store: %v", err)
	}
	node := refKey + "#decide"
	var terms []string
	if err := pool.QueryRow(ctx, `select tsvector_to_array(to_tsvector('english', $1))`, question).Scan(&terms); err != nil {
		t.Fatalf("read the question's lexemes: %v", err)
	}

	url := pool.Config().ConnString()
	listed, err := grapherase.Erase(ctx, url, refs.BlockNodeKind, node, "", 0)
	if err != nil {
		t.Fatalf("erase --node %s:%s without --confirm: %v", refs.BlockNodeKind, node, err)
	}
	hold := lockstep.HoldRow(t, database, &lockstep.Hold{
		Name: "the block's node row", Step: lockstep.StepNode, Kind: refs.BlockNodeKind, ID: node,
	}, `select 1 from graph_nodes where kind = $1 and id = $2 for update`, refs.BlockNodeKind, node)
	var erasure *grapherase.Erasure
	erased := make(chan error, 1)
	go func() {
		var err error
		erasure, err = grapherase.Erase(ctx, url, refs.BlockNodeKind, node, listed.Digest, time.Millisecond)
		erased <- err
	}()
	if pid := storetest.WaitForStatementBehind(t, database, hold.Tx, eraseNodeLockStatement, lockstep.Wait); pid == 0 {
		t.Fatalf("the erase of %s did not queue behind its node row within %s, holding CORE-1's owner lock", node, lockstep.Wait)
	}

	recorded := make(chan struct{})
	go func() {
		api.RecordDecisionBlockCheck(ctx, database, "CORE", "CORE-1", document, "decide", terms)
		close(recorded)
	}()
	if _, early, _ := storetest.AwaitOwnerLockWaiter(t, database, "the record write of the block's check", recorded); early {
		t.Errorf("the record write of the block's check ended without waiting at an owner lock while the erase of %s held CORE-1's", node)
	}
	hold.Release()
	select {
	case err := <-erased:
		if err != nil {
			t.Fatalf("erase --confirm %s: %v", listed.Digest, err)
		}
	case <-time.After(lockstep.Wait):
		t.Fatalf("the erase of %s did not end within %s of its node row's release", node, lockstep.Wait)
	}
	if !erasure.Applied {
		t.Fatalf("the erase of %s removed nothing: %q", node, erasure.Blockers)
	}
	select {
	case <-recorded:
	case <-time.After(lockstep.Wait + time.Minute):
		t.Fatalf("the record write of the block's check did not end")
	}

	var rows, holding int
	if err := pool.QueryRow(ctx, `
		select count(*), count(*) filter (where cardinality(terms) > 0) from related_checks
		where source_kind = 'decision_block' and source_block_id = 'decide' and source_issue_key = 'CORE-1'
	`).Scan(&rows, &holding); err != nil {
		t.Fatalf("read the checks of %s: %v", node, err)
	}
	if holding != 0 {
		t.Errorf("after the erase of %s, %d related_checks row(s) of it hold the question's terms, want none", node, holding)
	}
	if rows != 0 {
		t.Errorf("after the erase of %s, the record write behind it wrote %d related_checks row(s) of it, want none", node, rows)
	}
}
