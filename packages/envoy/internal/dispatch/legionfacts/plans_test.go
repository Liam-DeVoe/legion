package legionfacts

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/explaintest"
)

// TestReadClosedInReadsTheClosedFactsIndex: ReadClosedIn's statement, as it sends it (wideSelect
// and closedInWhere), reads one project's issues closed in a window through issue_facts_closed
// (migration 20261113082157) and never scans issue_facts, on an analyzed store of five projects of
// 4,000 stored facts each, nine in ten closed, one a day back over a year: enough rows that the
// planner prices a scan of the whole table above the index read.
func TestReadClosedInReadsTheClosedFactsIndex(t *testing.T) {
	ctx := t.Context()
	pool := factsDatabase(t)
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		insert into issue_facts (project_key, issue_key, opened_at, closed_at)
		select project, project || '-' || n, $1::timestamptz - make_interval(days => n % 365 + 10),
		       case when n % 10 <> 0 then $1::timestamptz - make_interval(days => n % 365) end
		from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project, generate_series(1, 4000) as n
		order by n % 365 desc, project
	`, now); err != nil {
		t.Fatalf("seed the facts: %v", err)
	}
	if _, err := pool.Exec(ctx, `analyze issue_facts`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	from := now.AddDate(0, 0, -28)
	closed, err := ReadClosedIn(ctx, pool, "FC", from, now, now)
	if err != nil {
		t.Fatalf("ReadClosedIn: %v", err)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, "explain (format json) "+wideSelect+closedInWhere, "FC", from, now).Scan(&raw); err != nil {
		t.Fatalf("explain ReadClosedIn: %v", err)
	}
	plan := explaintest.OnePlan(t, raw)
	if explaintest.SeqScansRelation(t, plan, "issue_facts") {
		t.Fatalf("ReadClosedIn scans issue_facts sequentially; want a range of issue_facts_closed:\n%s", plan)
	}
	scans := explaintest.IndexScans(t, plan)
	if !slices.ContainsFunc(scans, func(scan explaintest.IndexScan) bool { return scan.Index == "issue_facts_closed" }) {
		t.Fatalf("ReadClosedIn reads %v; want issue_facts_closed:\n%s", scans, plan)
	}
	t.Logf("ReadClosedIn answered %d issues of FC closed in 28 days, through %v", len(closed), scans)
}

// TestTheOtherBuildsRowsReadReadsTheWrittenBuildIndex: the read of the rows another build wrote
// (otherBuildsRowsQuery), which every first page of the build a project's record names makes,
// reaches issue_facts through issue_facts_written_build (migration 20261116151147) and never scans
// the table, on an analyzed store of five projects of 4,000 stored rows each, every project
// recorded under the running build and three rows of one written by another, as a roll's last
// write leaves them.
func TestTheOtherBuildsRowsReadReadsTheWrittenBuildIndex(t *testing.T) {
	ctx := t.Context()
	pool := factsDatabase(t)
	const running = "legion-envoy-running"
	if _, err := pool.Exec(ctx, `
		insert into projects (key, name, default_assignee)
		select project, project, 'alice' from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project;
		insert into issue_facts_computed (project_key, computed_at, computed_rows, computed_build)
		select project, now(), 4000, 'legion-envoy-running' from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project;
		insert into issue_facts (project_key, issue_key, opened_at, written_build)
		select project, project || '-' || n, now() - make_interval(days => n % 365),
		       case when project = 'FC' and n <= 3 then 'legion-envoy-older' else 'legion-envoy-running' end
		from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project, generate_series(1, 4000) as n;
		analyze issue_facts;
		analyze issue_facts_computed;
	`); err != nil {
		t.Fatalf("seed the facts: %v", err)
	}
	other, err := otherBuildsRows(ctx, pool, running)
	if err != nil {
		t.Fatalf("otherBuildsRows: %v", err)
	}
	if want := []string{"FC-1", "FC-2", "FC-3"}; len(other) != 1 || !slices.Equal(other["FC"], want) {
		t.Errorf("the rows another build wrote are %v, want %v of FC alone", other, want)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, "explain (format json) "+otherBuildsRowsQuery, running).Scan(&raw); err != nil {
		t.Fatalf("explain otherBuildsRows: %v", err)
	}
	plan := explaintest.OnePlan(t, raw)
	if explaintest.SeqScansRelation(t, plan, "issue_facts") {
		t.Fatalf("the read of the rows another build wrote scans issue_facts sequentially; want ranges of issue_facts_written_build:\n%s", plan)
	}
	scans := explaintest.IndexScans(t, plan)
	if !slices.ContainsFunc(scans, func(scan explaintest.IndexScan) bool { return scan.Index == "issue_facts_written_build" }) {
		t.Fatalf("the read of the rows another build wrote reads %v; want issue_facts_written_build:\n%s", scans, plan)
	}
	t.Logf("of 20,000 rows of five projects recorded under the running build, the read answered %v through %v", other, scans)
}

// TestTheOtherBuildsFilingsReadReadsTheWrittenBuildIndex: the read of the filings another build
// wrote (otherBuildsFilingsQuery), which the same pages make, reaches issue_filings through
// issue_filings_written_build (migration 20261116151147) and never scans the table, on an analyzed
// store of five projects of 4,000 filings each, every project recorded under the running build and
// three filings of one written by another.
func TestTheOtherBuildsFilingsReadReadsTheWrittenBuildIndex(t *testing.T) {
	ctx := t.Context()
	pool := factsDatabase(t)
	const running = "legion-envoy-running"
	if _, err := pool.Exec(ctx, `
		insert into projects (key, name, default_assignee)
		select project, project, 'alice' from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project;
		insert into issue_facts_computed (project_key, computed_at, computed_rows, computed_build)
		select project, now(), 4000, 'legion-envoy-running' from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project;
		insert into issue_filings (issue_key, project_key, filed_at, filer_role, class, written_build)
		select project || '-' || n, project, now() - make_interval(hours => n), 'architect', 'out_of_tree',
		       case when project = 'FC' and n <= 3 then 'legion-envoy-older' else 'legion-envoy-running' end
		from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project, generate_series(1, 4000) as n;
		analyze issue_filings;
		analyze issue_facts_computed;
	`); err != nil {
		t.Fatalf("seed the filings: %v", err)
	}
	other, err := otherBuildsFilings(ctx, pool, running)
	if err != nil {
		t.Fatalf("otherBuildsFilings: %v", err)
	}
	if want := []string{"FC-1", "FC-2", "FC-3"}; len(other) != 1 || !slices.Equal(other["FC"], want) {
		t.Errorf("the filings another build wrote are %v, want %v of FC alone", other, want)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, "explain (format json) "+otherBuildsFilingsQuery, running).Scan(&raw); err != nil {
		t.Fatalf("explain otherBuildsFilings: %v", err)
	}
	plan := explaintest.OnePlan(t, raw)
	if explaintest.SeqScansRelation(t, plan, "issue_filings") {
		t.Fatalf("the read of the filings another build wrote scans issue_filings sequentially; want ranges of issue_filings_written_build:\n%s", plan)
	}
	scans := explaintest.IndexScans(t, plan)
	if !slices.ContainsFunc(scans, func(scan explaintest.IndexScan) bool { return scan.Index == "issue_filings_written_build" }) {
		t.Fatalf("the read of the filings another build wrote reads %v; want issue_filings_written_build:\n%s", scans, plan)
	}
	t.Logf("of 20,000 filings of five projects recorded under the running build, the read answered %v through %v", other, scans)
}

// TestTheBoardsReadReadsItsRowsByKeyAndByRootAlone: Read, the Legion board's read of the facts,
// answers the rows of the keys it is given, of every issue of the trees it is given and of every
// open issue of the project, and no other row, and its statement (boardSelect and shownWhere)
// reaches issue_facts through the primary key for the keys, through issue_facts_root (migration
// 20261116151147) for the trees and through issue_facts_closed (migration 20261113082157) for the
// open rows, each scan keyed by its own condition, never a scan of the table nor a read of the
// project's whole range filtered after it. The store is five projects of 4,000 stored rows each,
// in trees of ten, one in fifty open, analyzed; the board of FC names 34 cards, 24 of them in the
// 12 trees it shows and 10 whose stored root is another, and a key no row holds. The plan is
// taken twice: with the values, as the planner prices them, and as the generic plan pgx's cached
// statement settles on, which knows no value of the lists, with sequential scans off so it names
// only the indexes the statement can prove.
func TestTheBoardsReadReadsItsRowsByKeyAndByRootAlone(t *testing.T) {
	ctx := t.Context()
	pool := factsDatabase(t)
	if _, err := pool.Exec(ctx, `
		insert into issue_facts (project_key, issue_key, opened_at, closed_at, root_key, tree_depth)
		select project, project || '-' || n, now() - make_interval(days => n % 365 + 1),
		       case when n % 50 <> 0 then now() - make_interval(days => n % 365) end,
		       project || '-' || ((n - 1) / 10 * 10 + 1), case when n % 10 = 1 then 0 else 1 end
		from unnest(array['FA', 'FB', 'FC', 'FD', 'FE']) as project, generate_series(1, 4000) as n;
		analyze issue_facts;
	`); err != nil {
		t.Fatalf("seed the facts: %v", err)
	}
	var roots, keys []string
	want := map[string]bool{}
	for tree := range 12 {
		root := 10*tree*3 + 1
		roots = append(roots, fmt.Sprintf("FC-%d", root))
		for n := root; n < root+10; n++ {
			want[fmt.Sprintf("FC-%d", n)] = true
		}
		keys = append(keys, fmt.Sprintf("FC-%d", root+tree%10), fmt.Sprintf("FC-%d", root+(tree+5)%10))
	}
	for n := 2001; n <= 2010; n++ {
		keys, want[fmt.Sprintf("FC-%d", n)] = append(keys, fmt.Sprintf("FC-%d", n)), true
	}
	keys = append(keys, "FC-9999")
	for n := 50; n <= 4000; n += 50 {
		want[fmt.Sprintf("FC-%d", n)] = true
	}
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	rows, err := Read(ctx, pool, "FC", keys, roots, at)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got, wanted := slices.Sorted(maps.Keys(rows)), slices.Sorted(maps.Keys(want)); !slices.Equal(got, wanted) {
		t.Errorf("Read answered the %d rows %v, want the %d of the keys, the trees and the open rows %v", len(got), got, len(wanted), wanted)
	}

	statement := boardSelect + shownWhere
	judge := func(how string, plan json.RawMessage) []explaintest.IndexScan {
		t.Helper()
		if explaintest.SeqScansRelation(t, plan, "issue_facts") {
			t.Errorf("%s, the board's read scans issue_facts sequentially:\n%s", how, plan)
		}
		scans := explaintest.IndexScans(t, plan)
		for _, index := range []struct{ name, column string }{{"issue_facts_pkey", "issue_key"}, {"issue_facts_root", "root_key"}, {"issue_facts_closed", "closed_at IS NULL"}} {
			if !slices.ContainsFunc(scans, func(scan explaintest.IndexScan) bool {
				return scan.Index == index.name && strings.Contains(scan.Cond, index.column)
			}) {
				t.Errorf("%s, the board's read reads %v; want %s keyed by %s:\n%s", how, scans, index.name, index.column, plan)
			}
		}
		return scans
	}
	var raw []byte
	if err := pool.QueryRow(ctx, "explain (analyze, format json) "+statement, "FC", keys, roots).Scan(&raw); err != nil {
		t.Fatalf("explain the board's read: %v", err)
	}
	custom := judge("with its values", explaintest.OnePlan(t, raw))
	text, err := pool.Query(ctx, "explain (analyze, buffers) "+statement, "FC", keys, roots)
	if err != nil {
		t.Fatalf("explain the board's read as text: %v", err)
	}
	lines, err := pgx.CollectRows(text, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain the board's read as text: %v", err)
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection for the plan: %v", err)
	}
	defer connection.Release()
	generic := judge("under the generic plan", explaintest.GenericPlan(t, ctx, connection, "board_facts", statement,
		[]string{"text", "text[]", "text[]"}, []string{"'FC'", "array['FC-1']", "array['FC-1']"}, "set local enable_seqscan = off"))
	t.Logf("of 20,000 rows of five projects, Read answered %d of FC for %d keys and %d trees, through %v (with its values) and %v (generic); the plan with its values:\n%s",
		len(rows), len(keys), len(roots), custom, generic, strings.Join(lines, "\n"))
}

// TestTheStoredFilingsReadKeepsItsIndexesUnderAGenericPlan: the read of a project's stored filings
// whose filed issue or filer's issue a pass recomputes (storedFilingsOfQuery) reaches issue_filings
// through its primary key and through issue_filings_filer (migration 20261113082155) under the
// generic plan pgx's cached statement settles on, which knows no value of the keys, and never
// scans the table: 6,000 filings of two projects, two in three by a phase worker, are analyzed
// first, and sequential scans are off, so the plan names only the indexes the statement can prove.
func TestTheStoredFilingsReadKeepsItsIndexesUnderAGenericPlan(t *testing.T) {
	ctx := t.Context()
	pool := factsDatabase(t)
	if _, err := pool.Exec(ctx, `
		insert into projects (key, name, default_assignee) values ('FILE', 'Filings', 'alice'), ('FOTH', 'Other', 'alice');
		insert into issue_filings (issue_key, project_key, filed_at, filer_role, filer_issue, class)
		select project || '-' || n, project, now() - make_interval(hours => n),
		       case when n % 3 = 0 then 'controller' else 'architect' end,
		       case when n % 3 = 0 then '' else project || '-' || (n / 10 + 1) end,
		       case when n % 3 = 0 then 'by_the_controller' else 'in_tree_at_design' end
		from unnest(array['FILE', 'FOTH']) as project, generate_series(1, 3000) as n;
		analyze issue_filings;
	`); err != nil {
		t.Fatalf("seed the filings: %v", err)
	}
	keys, err := filingsToCompute(ctx, pool, "FILE", []string{"FILE-7"}, nil)
	if err != nil {
		t.Fatalf("filingsToCompute: %v", err)
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection for the plan: %v", err)
	}
	defer connection.Release()
	plan := explaintest.GenericPlan(t, ctx, connection, "stored_filings_of", storedFilingsOfQuery,
		[]string{"text", "text[]"}, []string{"'FILE'", "array['FILE-7']"}, "set local enable_seqscan = off")
	if explaintest.SeqScansRelation(t, plan, "issue_filings") {
		t.Fatalf("the generic plan of the stored filings read scans issue_filings:\n%s", plan)
	}
	scans := explaintest.IndexScans(t, plan)
	if !slices.ContainsFunc(scans, func(scan explaintest.IndexScan) bool { return scan.Index == "issue_filings_filer" }) {
		t.Fatalf("the generic plan of the stored filings read reads %v; want issue_filings_filer:\n%s", scans, plan)
	}
	t.Logf("FILE-7 is the filed or the filer's issue of %d filings; the generic plan reads %v", len(keys), scans)
}
