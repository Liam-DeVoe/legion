package api

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A meaning statement run as a search runs it reads only the partitions of embeddings of the
// projects its reader sees and the one of no project: alice's statement, in a transaction with the
// meaning scan's own settings (searchMeaningScan), scans PUB's partition and the partition of none
// and nothing of PRIV's. Which partitions it read is read from the transaction's own statistics
// (pg_stat_xact_user_tables) on a connection of its own, which has read nothing before, so the row
// holds what the statement did. The control is the same statement run on the plan a cached
// statement settles on from its sixth run, a generic one, which cannot read the viewer and reads
// PRIV's partition too: that is what the meaning scan's plan_cache_mode is there to stop, and a
// reading blind to a partition would fail there.
func TestAMeaningStatementReadsOnlyThePartitionsItsReaderSees(t *testing.T) {
	seed := hnswSeed(t, hnswAlice, 10, hnswPartitioned)
	alice := model.Actor{Kind: "user", ID: seedAlice, Viewer: &model.Viewer{
		Person: seedAlice, Open: true, Sees: []string{seedPublic}, AnyPrivate: true,
	}}
	arguments := searchMeaningQueryArguments(
		searchParameters{text: hnswQuery, limit: searchDefaultLimit, who: alice}, queryVector(), seed.deps.Embedder.Identity())
	readUnder := func(settings []string) []string {
		t.Helper()
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, seed.database.Pool.Config().ConnString())
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer conn.Close(ctx)
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		for _, setting := range settings {
			if _, err := tx.Exec(ctx, setting); err != nil {
				t.Fatalf("%s: %v", setting, err)
			}
		}
		rows, err := tx.Query(ctx, searchQueryMeaning, arguments...)
		if err != nil {
			t.Fatalf("run alice's meaning statement: %v", err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("run alice's meaning statement: %v", err)
		}
		var read []string
		if err := tx.QueryRow(ctx, `
			select coalesce(array_agg(relname::text order by relname), '{}') from pg_stat_xact_user_tables
			 where relname like 'embeddings\_%' and coalesce(seq_scan, 0) + coalesce(idx_scan, 0) > 0
		`).Scan(&read); err != nil {
			t.Fatalf("read which partitions the statement read: %v", err)
		}
		return read
	}
	if read := readUnder(searchMeaningScan); !slices.Equal(read, []string{"embeddings_no_project", "embeddings_project_pub"}) {
		t.Errorf("alice's meaning statement, run as a search runs it, read %v, want PUB's partition and the one of no project alone", read)
	}
	generic := slices.DeleteFunc(slices.Clone(searchMeaningScan), func(setting string) bool { return strings.Contains(setting, "plan_cache_mode") })
	if len(generic) == len(searchMeaningScan) {
		t.Fatal("searchMeaningScan sets no plan_cache_mode, so a meaning statement is planned without its reader")
	}
	generic = append(generic, "set local plan_cache_mode = force_generic_plan")
	if read := readUnder(generic); !slices.Contains(read, "embeddings_project_priv") {
		t.Errorf("alice's meaning statement run on a generic plan read %v, which leaves out PRIV's partition: the check above proves nothing", read)
	}
}
