package docs

import (
	"context"
	"io"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// A link the release before this one adds after the backfill marked its document reaches the
// store when this build's reconciliation drains the mark, and the census reaches zero.
//
// The order is the one a rollback gives, where the README calls the backfill safe to run: the
// release before this one serves, the backfill runs and marks each document it named the nodes
// of (store_owed_at), and a person then edits one of those documents through that release, whose
// index pass inserts the block_refs row of the link the version adds naming no node and states
// nothing in the store. Once this build serves again, its reconciliation indexes the marked
// document, states every block's parent and clears the mark. The added link's row must then name
// its block's node and the store must assert that link, or the census refuses with `the row names
// no node` until someone runs the backfill again, which nothing tells anyone to do.
//
// The tester drove exactly that on 2026-11-18 at 696a9727c4fe with legion-envoy-v30.45.0 and
// this build on one database: after this build drained the mark, `census --store` refused with
// one difference, `block_ref CORE-4/drive-md#<block> mentions -> issue CORE-3: the row names no
// node`, and the block's tuples held its part_of and no mentions.
//
// The stand-in for that release is indexAsThePreviousRelease, as in
// TestTheBlockAndTheLinkThePreviousReleaseDroppedAreGoneAfterTheBackfill, run here after the
// backfill rather than before it.
func TestALinkThePreviousReleaseAddsAfterTheBackfillReachesTheStore(t *testing.T) {
	database, service := blockIndexService(t)
	ctx := context.Background()

	const slug = "linked"
	document := createOwedDocument(t, database, service, "CORE-1", "", slug, nestedSpec(slug), "")
	settle(t, database, document)
	// Rows of every other kind the census reads, as TestTheBlockParentCensusReachesZeroAfterTheBackfill
	// seeds them, so the census can pass only on agreement and never refuses an empty kind here.
	if _, err := database.Pool.Exec(ctx, `
		insert into comments (issue_key, author, body) values
			('CORE-1', '{"kind":"user","id":"Alice"}', 'A remark written before the store existed.'),
			('CORE-1', '{"kind":"session","id":"previous-release-link-session"}', 'A session''s remark written before the store existed.');
		update issues set route = 'role:previous-release-link-role' where key = 'CORE-1';
	`); err != nil {
		t.Fatalf("seed the comments and the route: %v", err)
	}
	// The document as a build before the store wrote it: its rows, and nothing of the store.
	drainBlockIndex(t, service, "seed")
	takeTheStoreOff(t, database)
	var refKey string
	if err := database.Pool.QueryRow(ctx, `select ref_key from artifacts where id = $1`, document).Scan(&refKey); err != nil {
		t.Fatalf("read the document's ref_key: %v", err)
	}

	report, err := graphcensus.Backfill(ctx, database, io.Discard)
	if err != nil {
		t.Fatalf("backfill the store: %v", err)
	}
	if blocks := backfilledKind(t, report, refs.BlockNodeKind); blocks.Owed != 1 || !blocks.Complete {
		t.Fatalf("the backfill wrote %#v for the blocks, want the one document marked and the cutover complete", blocks)
	}

	inner := holderOf(t, service, document, "dispatch://CORE-2")
	middle := holderOf(t, service, document, "Middle of "+slug)
	dropped := blockLinkRow{blockID: inner}
	if err := database.Pool.QueryRow(ctx, `
		select kind, to_kind, to_id from block_refs where artifact_id = $1 and block_id = $2
	`, document, inner).Scan(&dropped.kind, &dropped.toKind, &dropped.toID); err != nil {
		t.Fatalf("read the inner paragraph's link: %v", err)
	}
	added := blockLinkRow{blockID: middle, kind: dropped.kind, toKind: dropped.toKind, toID: dropped.toID}

	// The previous release's version 2, written after the backfill: the inner paragraph stops
	// citing CORE-2 and the middle one starts. Its index pass inserts the middle paragraph's link
	// naming no node.
	indexAsThePreviousRelease(t, database, service, document, func(tree *pmdoc.Node) *pmdoc.Node {
		replaceText(tree, "Inner of "+slug+", after dispatch://CORE-2.", "Inner of "+slug+".")
		replaceText(tree, "Middle of "+slug+".", "Middle of "+slug+", after dispatch://CORE-2.")
		return tree
	}, dropped, added)
	if after := holderOf(t, service, document, "dispatch://CORE-2"); after != middle {
		t.Fatalf("the edit moved the mention to block %s, want the middle paragraph %s: the room minted new ids and this test proves nothing", after, middle)
	}
	var marked int
	if err := database.Pool.QueryRow(ctx,
		`select count(*) from document_block_versions where artifact_id = $1 and store_owed_at is not null`, document).Scan(&marked); err != nil || marked != 1 {
		t.Fatalf("the document holds the backfill's mark %d time(s) (%v) after the previous release's version, want it kept", marked, err)
	}

	// This build serves again and drains the mark.
	drainBlockIndex(t, service, "after the previous release's version")

	middleAddress := refKey + "#" + middle
	var named bool
	if err := database.Pool.QueryRow(ctx, `
		select node_kind is not null from block_refs
		where artifact_id = $1 and block_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
	`, document, middle, added.kind, added.toKind, added.toID).Scan(&named); err != nil {
		t.Fatalf("read the middle paragraph's link row: %v", err)
	}
	if !named {
		t.Errorf("the block_refs row of %s %s %s:%s names no node after this build drained the backfill's mark",
			middleAddress, added.kind, added.toKind, added.toID)
	}
	held, err := graphstore.CurrentOut(ctx, database.Pool, refs.BlockNodeKind, middleAddress, []string{added.kind})
	if err != nil {
		t.Fatalf("read the links the store states out of %s: %v", middleAddress, err)
	}
	if len(held) != 1 || held[0].ToKind != added.toKind || held[0].ToID != added.toID {
		t.Errorf("the store states %#v out of %s, want the %s %s:%s the version holds", held, middleAddress, added.kind, added.toKind, added.toID)
	}
	census := takeStoreCensus(t, database)
	for _, kind := range census.Kinds {
		for _, difference := range append(append([]graphcensus.Difference{}, kind.ReportedRows...), kind.ReportedNodes...) {
			t.Errorf("the census reads a difference of kind %s at %s: %s", kind.Kind, difference.At, difference.Cause)
		}
	}
	if census.Refused() {
		t.Fatalf("the census refused with %d difference(s) after this build drained the backfill's mark over a link the previous release added, want zero",
			census.Differences())
	}
}
