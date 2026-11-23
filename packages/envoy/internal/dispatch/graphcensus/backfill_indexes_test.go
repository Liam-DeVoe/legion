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
