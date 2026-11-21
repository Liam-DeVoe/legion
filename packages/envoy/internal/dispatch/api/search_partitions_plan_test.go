package api

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/embed"
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
	seed := hnswSeed(t, hnswAlice, hnswPartitioned)
	hnswAddHidden(t, seed, hnswAlice, 10)
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

// The related check's leg over the other projects (relatedOtherProjectsSQL) plans only the
// partitions of embeddings of the projects its writer sees and the one of no project, as search's
// arms do: alice, who does not see PRIV, has her other-projects statement planned with the meaning
// scan's settings, and its plan names PUB's partition and no partition of PRIV's. The plan is what
// is read, not the statement's statistics, since the leg reads nothing of a partition holding no
// answered question and its statistics would then say nothing of what it walks. The control is the
// same statement planned generically, as a cached statement settles on from its sixth run, which
// cannot read the viewer and names PRIV's partition: a reading blind to the plan would fail there.
func TestTheOtherProjectsLegPlansOnlyThePartitionsItsWriterSees(t *testing.T) {
	seed := hnswSeed(t, hnswAlice, hnswPartitioned)
	alice := model.Actor{Kind: "user", ID: seedAlice, Viewer: &model.Viewer{
		Person: seedAlice, Open: true, Sees: []string{seedPublic}, AnyPrivate: true,
	}}
	arguments := []any{embed.Literal(queryVector()), "ELSEWHERE", seed.deps.Embedder.Identity(),
		searchMeaningFloor, contracts.SearchKindDepth, relatedViewer(alice)}
	planUnder := func(settings []string) string {
		t.Helper()
		ctx := context.Background()
		tx, err := seed.database.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		for _, setting := range settings {
			if _, err := tx.Exec(ctx, setting); err != nil {
				t.Fatalf("%s: %v", setting, err)
			}
		}
		rows, err := tx.Query(ctx, "explain (costs off) "+relatedOtherProjectsSQL, arguments...)
		if err != nil {
			t.Fatalf("explain the other-projects leg: %v", err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("explain the other-projects leg: %v", err)
		}
		return strings.Join(plan, "\n")
	}
	plan := planUnder(searchMeaningScan)
	if !strings.Contains(plan, "embeddings_project_pub") {
		t.Fatalf("the other-projects leg, planned for alice, names no partition of PUB's, so this row cannot tell a pruned plan from one that reads nothing:\n%s", plan)
	}
	if strings.Contains(plan, "embeddings_project_priv") {
		t.Errorf("the other-projects leg, planned for alice, names PRIV's partition, a project she does not see:\n%s", plan)
	}
	generic := slices.DeleteFunc(slices.Clone(searchMeaningScan), func(setting string) bool { return strings.Contains(setting, "plan_cache_mode") })
	generic = append(generic, "set local plan_cache_mode = force_generic_plan")
	if plan := planUnder(generic); !strings.Contains(plan, "embeddings_project_priv") {
		t.Errorf("the other-projects leg planned generically names no partition of PRIV's, so the check above proves nothing:\n%s", plan)
	}
}
