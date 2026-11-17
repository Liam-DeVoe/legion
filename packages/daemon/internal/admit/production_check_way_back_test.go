package admit

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// checkOfEachMergeVersion is the version of the migration that keys a production check by its
// issue and the moment it opened (20261117211603_production_check_of_each_merge.up.sql), whose
// header carries the way back this file runs.
const checkOfEachMergeVersion = 20261117211603

// The way back of the check of each merge, read from the migration's header and run as its one
// transaction, leaves the table as the release before reads and writes it: each issue keeps its
// newest check alone, the one that release reads, the key is the issue again, the run and the
// task of a check are gone, and so is the migration's version row. So the roll forward's Migrate
// applies the file again, and the key and the two columns stand again.
func TestTheWayBackOfTheCheckOfEachMergeRestoresTheKeyByIssue(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)
	opened := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	closed := opened.Add(30 * time.Minute)
	twice, once := "LEGION-401", "LEGION-402"
	inTx(t, pool, func(tx pgx.Tx) {
		records := record.NewStore()
		for _, key := range []string{twice, once} {
			if err := records.PutIssue(ctx, tx, record.Issue{Key: key, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: key,
				Project: "LEGION", Title: key, Phase: "done", Generation: 3, Status: "done", Rank: "U"}); err != nil {
				t.Fatalf("record %s: %v", key, err)
			}
		}
		for _, check := range []record.ProductionCheck{
			{Issue: twice, Project: "LEGION", Tree: twice, Repo: "sjawhar/legion", Number: 1, Role: claim.RoleTester, OpenedAt: opened, BoundAt: opened.Add(time.Hour),
				ClosedAt: &closed, Outcome: record.CheckNotRun, Generation: 3, Task: "a"},
			{Issue: twice, Project: "LEGION", Tree: twice, Repo: "sjawhar/legion", Number: 2, Role: claim.RoleTester, OpenedAt: opened.Add(time.Hour), BoundAt: opened.Add(2 * time.Hour),
				Looks: 1, LookOpen: true, Generation: 3, Task: "b"},
			{Issue: once, Project: "LEGION", Tree: once, Repo: "sjawhar/legion", Number: 3, Role: claim.RoleTester, OpenedAt: opened, BoundAt: opened.Add(time.Hour), Looks: 1},
		} {
			if err := records.PutProductionCheck(ctx, tx, check); err != nil {
				t.Fatalf("record the check of #%d: %v", check.Number, err)
			}
		}
	})

	if _, err := pool.Exec(ctx, headerWayBack(t, checkOfEachMergeVersion), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatalf("run the way back of the check of each merge as its one transaction: %v", err)
	}
	rows, err := pool.Query(ctx, `select issue, pr_number from production_checks order by issue`)
	if err != nil {
		t.Fatalf("read the checks after the way back: %v", err)
	}
	type kept struct {
		issue  string
		number int
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (kept, error) {
		var k kept
		err := row.Scan(&k.issue, &k.number)
		return k, err
	})
	if err != nil {
		t.Fatalf("read the checks after the way back: %v", err)
	}
	if want := []kept{{twice, 2}, {once, 3}}; !slices.Equal(got, want) {
		t.Fatalf("the checks after the way back = %+v, want each issue's newest alone: %+v", got, want)
	}
	if key := productionCheckKey(t, pool); !slices.Equal(key, []string{"issue"}) {
		t.Fatalf("the key of production_checks after the way back = %v, want (issue)", key)
	}
	if columns := checkRunColumns(t, pool); len(columns) != 0 {
		t.Fatalf("production_checks after the way back holds %v, want neither generation nor task", columns)
	}
	var recorded bool
	if err := pool.QueryRow(ctx, `select exists (select 1 from schema_version where version = $1)`, checkOfEachMergeVersion).Scan(&recorded); err != nil {
		t.Fatalf("read the version row after the way back: %v", err)
	}
	if recorded {
		t.Fatalf("schema_version still holds %d after the way back, so the roll forward would not apply it again", checkOfEachMergeVersion)
	}

	st, err := store.Open(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatalf("open the record store: %v", err)
	}
	defer st.Close()
	applied, err := st.Migrate(ctx)
	if err != nil || applied != 1 {
		t.Fatalf("the roll forward applied %d migrations, %v; want 1, the check of each merge again", applied, err)
	}
	if key := productionCheckKey(t, pool); !slices.Equal(key, []string{"issue", "opened_at"}) {
		t.Fatalf("the key of production_checks after the roll forward = %v, want (issue, opened_at)", key)
	}
	if columns := checkRunColumns(t, pool); !slices.Equal(columns, []string{"generation", "task"}) {
		t.Fatalf("production_checks after the roll forward holds %v of the two columns, want both", columns)
	}
}

// productionCheckKey is the columns of production_checks' primary key, in key order.
func productionCheckKey(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select a.attname from pg_index i
		join pg_attribute a on a.attrelid = i.indrelid and a.attnum = any(i.indkey)
		where i.indrelid = 'production_checks'::regclass and i.indisprimary
		order by array_position(i.indkey::int2[], a.attnum)`)
	if err != nil {
		t.Fatalf("read the key of production_checks: %v", err)
	}
	key, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the key of production_checks: %v", err)
	}
	return key
}

// checkRunColumns is which of production_checks' generation and task stand, in name order.
func checkRunColumns(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select column_name from information_schema.columns
		where table_schema = current_schema() and table_name = 'production_checks' and column_name in ('generation', 'task')
		order by column_name`)
	if err != nil {
		t.Fatalf("read the columns of production_checks: %v", err)
	}
	columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the columns of production_checks: %v", err)
	}
	return columns
}
