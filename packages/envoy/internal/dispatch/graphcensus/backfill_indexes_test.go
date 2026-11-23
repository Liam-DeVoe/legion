package graphcensus

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestTheBackfillRefusesToRunWithoutItsIndexes holds Backfill to refusing, before it writes
// anything, while any of BackfillIndexes is absent, and to naming it: each index is dropped in
// turn, and the run must fail naming that index and no other, leave the block row it would have
// written naming no node, and record no horizon. Each index is then built again as its migration
// built it. The control is the same run once every index stands: it succeeds and names the
// block's node, so the refusal is the index's absence and nothing else of the database.
func TestTheBackfillRefusesToRunWithoutItsIndexes(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	pool := database.Pool
	document := seedPreStoreDocument(t, database, "only")
	definitions := map[string]string{}
	for _, index := range BackfillIndexes {
		var definition string
		if err := pool.QueryRow(ctx, `select pg_get_indexdef(to_regclass($1))`, index).Scan(&definition); err != nil || definition == "" {
			t.Fatalf("read the definition of %s (%q): %v", index, definition, err)
		}
		definitions[index] = definition
	}
	written := func() (blocks, horizons int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			select (select count(*) from document_blocks where artifact_id = $1::uuid and node_kind is not null),
			       (select count(*) from graph_backfill_horizon)
		`, document.artifactID).Scan(&blocks, &horizons); err != nil {
			t.Fatalf("read what the run wrote: %v", err)
		}
		return blocks, horizons
	}
	for _, index := range BackfillIndexes {
		if _, err := pool.Exec(ctx, "drop index "+index); err != nil {
			t.Fatalf("drop %s: %v", index, err)
		}
		_, err := Backfill(ctx, database, io.Discard)
		switch {
		case err == nil:
			t.Errorf("the backfill ran with %s dropped", index)
		case !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), index):
			t.Errorf("the backfill with %s dropped failed with %q, want a refusal naming it", index, err)
		default:
			for _, other := range BackfillIndexes {
				if other != index && strings.Contains(err.Error(), other) {
					t.Errorf("the refusal with only %s dropped names %s too: %q", index, other, err)
				}
			}
			t.Logf("with %s dropped: %v", index, err)
		}
		if blocks, horizons := written(); blocks != 0 || horizons != 0 {
			t.Errorf("the refused run with %s dropped named %d block node(s) and recorded %d horizon(s), want none", index, blocks, horizons)
		}
		if _, err := pool.Exec(ctx, definitions[index]); err != nil {
			t.Fatalf("build %s again: %v", index, err)
		}
	}
	if _, err := Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("the backfill with every index standing, the control: %v", err)
	}
	if blocks, horizons := written(); blocks != 1 || horizons != 1 {
		t.Errorf("the backfill with every index standing named %d block node(s) and recorded %d horizon(s), want 1 and 1", blocks, horizons)
	}
}

// TestTheBackfillRefusesToRunOverAnInvalidIndex holds Backfill to refusing while one of
// BackfillIndexes stands but is invalid, as a concurrent build that failed leaves it until the
// runner's next run drops it and builds it again: document_blocks_naming_no_node is dropped and a
// concurrent build of that name fails on the table's rows, which leaves an INVALID index of the
// name in the catalog, one the planner never reads. The run must fail naming it, and leave the
// block rows naming no node and record no horizon. The index is then built again as its migration
// built it, and the same run, the control, succeeds and names both blocks' nodes, so the refusal
// is the index's invalidity and nothing else of the database.
func TestTheBackfillRefusesToRunOverAnInvalidIndex(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	pool := database.Pool
	document := seedPreStoreDocument(t, database, "first", "second")
	const index = "document_blocks_naming_no_node"
	var definition string
	if err := pool.QueryRow(ctx, `select pg_get_indexdef(to_regclass($1))`, index).Scan(&definition); err != nil || definition == "" {
		t.Fatalf("read the definition of %s (%q): %v", index, definition, err)
	}
	if _, err := pool.Exec(ctx, "drop index "+index); err != nil {
		t.Fatalf("drop %s: %v", index, err)
	}
	// The two block rows of one document share artifact_id, so a unique build over it fails once
	// its catalog entry is made, and the entry stays, marked invalid.
	if _, err := pool.Exec(ctx, "create unique index concurrently "+index+" on document_blocks (artifact_id)"); err == nil {
		t.Fatalf("the unique build of %s over two blocks of one document succeeded; this test needs it to fail", index)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `select indisvalid from pg_index where indexrelid = to_regclass($1)`, index).Scan(&valid); err != nil || valid {
		t.Fatalf("%s after the failed build: valid %t, %v; want an INVALID index of that name", index, valid, err)
	}
	written := func() (blocks, horizons int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			select (select count(*) from document_blocks where artifact_id = $1::uuid and node_kind is not null),
			       (select count(*) from graph_backfill_horizon)
		`, document.artifactID).Scan(&blocks, &horizons); err != nil {
			t.Fatalf("read what the run wrote: %v", err)
		}
		return blocks, horizons
	}
	_, err := Backfill(ctx, database, io.Discard)
	switch {
	case err == nil:
		t.Errorf("the backfill ran over an INVALID %s", index)
	case !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), index):
		t.Errorf("the backfill over an INVALID %s failed with %q, want a refusal naming it", index, err)
	default:
		t.Logf("over an INVALID %s: %v", index, err)
	}
	if blocks, horizons := written(); blocks != 0 || horizons != 0 {
		t.Errorf("the refused run named %d block node(s) and recorded %d horizon(s), want none", blocks, horizons)
	}
	if _, err := pool.Exec(ctx, "drop index "+index); err != nil {
		t.Fatalf("drop the INVALID %s: %v", index, err)
	}
	if _, err := pool.Exec(ctx, definition); err != nil {
		t.Fatalf("build %s again: %v", index, err)
	}
	if _, err := Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("the backfill with every index valid, the control: %v", err)
	}
	if blocks, horizons := written(); blocks != 2 || horizons != 1 {
		t.Errorf("the backfill with every index valid named %d block node(s) and recorded %d horizon(s), want 2 and 1", blocks, horizons)
	}
}
