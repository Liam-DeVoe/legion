package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/storesmove"
	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// storesMovedMigration is the migration that writes the supertags of the moved stores' kinds by
// bounded inserts into graph_nodes and graph_tuples.
const storesMovedMigration = "20261123134318_stores_moved.up.sql"

// TestStoresMovedPassesTheCensusAtAOneByteLimit is c-moved-bounded's: the pre-deploy census admits
// dispatch://INFRA-1098's own pending migrations at any size of the two graph tables. Over a
// database of its own, it applies every migration of the store but this issue's own, which the
// rehearsal's one list names (storesmove.OwnMigrations), recording each version, writes one row
// into each of graph_nodes and graph_tuples, and takes the census of every migration of the store
// with the limit at one byte, so the pending migrations are this issue's own and every table that
// holds a row is above the limit. It fails on each refusal of each pending migration, and holds
// the census of the supertag migration to graph_nodes and graph_tuples standing above the limit and
// admitted for bounded writes. The census admits a table for bounded writes only where no earlier
// pending migration touched it with more than bounded writes (changedBy): so a concurrent build of
// an index of graph_nodes versioned before the supertag migration fails it.
func TestStoresMovedPassesTheCensusAtAOneByteLimit(t *testing.T) {
	ctx := context.Background()
	database := store.OpenEmptyTestStore(t)
	own := storesmove.OwnMigrations()
	if err := database.MigrateWithout(ctx, own); err != nil {
		t.Fatalf("apply every migration but this issue's own: %v", err)
	}
	for _, write := range []string{
		`insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
		 values ('issue', 'BOUND-1', 'issue', 'project', 'live', '{}', now(), '{"kind":"user","id":"alice"}', 0)`,
		`insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
		 values ('issue', 'BOUND-1', 'state', 'value', 'live', now(), '{"kind":"user","id":"alice"}', 0, true)`,
	} {
		if _, err := database.Pool.Exec(ctx, write); err != nil {
			t.Fatalf("write a row of the store: %v", err)
		}
	}

	report, err := store.CensusOfEveryMigration(ctx, database.Pool.Config().ConnString(), pgmigrate.CensusOptions{TableLimit: 1})
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	var written strings.Builder
	report.Write(&written)
	t.Logf("the census at a one-byte limit:\n%s", written.String())

	var pending []string
	for _, entry := range report.Pending {
		pending = append(pending, entry.Migration.Name)
		for _, refusal := range entry.Refusals {
			t.Errorf("the census refuses %s: %s", entry.Migration.Name, refusal)
		}
	}
	if !slices.Equal(pending, own) {
		t.Fatalf("the pending migrations are %v, want this issue's own, %v", pending, own)
	}
	for _, entry := range report.Pending {
		if entry.Migration.Name != storesMovedMigration {
			continue
		}
		for _, table := range []string{"graph_nodes", "graph_tuples"} {
			index := slices.IndexFunc(entry.Tables, func(read pgmigrate.TableCensus) bool { return read.Name == table })
			if index < 0 {
				t.Errorf("the census of %s reads no %s", storesMovedMigration, table)
				continue
			}
			read := entry.Tables[index]
			if !read.Exists || read.Bytes <= 1 || read.BoundedWrites == 0 {
				t.Errorf("the census of %s reads %s as %+v, want it standing, above the one-byte limit and admitted for bounded writes",
					storesMovedMigration, table, read)
			}
			for _, line := range strings.Split(written.String(), "\n") {
				if strings.HasPrefix(line, "census: "+storesMovedMigration+" touches "+table+":") {
					t.Logf("%s", line)
				}
			}
		}
	}
}
