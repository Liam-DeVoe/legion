package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// checkOfEachMergeVersion is the version of the migration this file tests, so a test can migrate
// through everything before it whatever lands beside it.
const checkOfEachMergeVersion = 20261117211603

// 20261117211603 keys a production check by its issue and the moment it opened, and gives it the
// run and the task of its merge. A check the release before wrote reads after it with run 0 and
// no task, every other field as that release left it, and the read of the issue's check answers
// it. The two columns are unknown before the migration, which is what shows the fixture is the
// old shape.
func TestTheCheckOfTheReleaseBeforeReadsWithGenerationZeroAndNoTask(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, checkOfEachMergeVersion-1)
	opened := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	for _, seed := range []string{
		`insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
			values ('LEGION-208', 'LEGION-208', 'LEGION', 'root', 'done', 4, 'done', 'A', 1)`,
		`insert into production_checks (issue, project, tree, repo, pr_number, merge_sha, opened_at, bound_at, role, looks, look_open)
			values ('LEGION-208', 'LEGION', 'LEGION-208', 'sjawhar/legion', 1208, 'm1', '2026-11-17T08:00:00Z', '2026-12-01T08:00:00Z', 'tester', 1, true)`,
	} {
		if _, err := store.Pool().Exec(ctx, seed); err != nil {
			t.Fatalf("record before the migration: %v", err)
		}
	}
	for _, column := range []string{"generation = 1", "task = 'a'"} {
		_, err := store.Pool().Exec(ctx, "update production_checks set "+column)
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("update production_checks set %s before the migration = %v, want the column unknown: the fixture is not the old shape", column, err)
		}
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the check of each merge: %v", err)
	}
	var check *record.ProductionCheck
	if err := pgx.BeginFunc(ctx, store.Pool(), func(tx pgx.Tx) error {
		var err error
		check, err = record.NewStore().ProductionCheckOf(ctx, tx, "LEGION-208", time.Time{})
		return err
	}); err != nil {
		t.Fatalf("read the check of LEGION-208 after the migration: %v", err)
	}
	if check == nil || check.Number != 1208 || check.MergeSHA != "m1" || !check.OpenedAt.Equal(opened) || check.Looks != 1 || !check.LookOpen {
		t.Fatalf("the check after the migration = %+v, want the one the release before left", check)
	}
	if check.Generation != 0 || check.Task != "" {
		t.Fatalf("the check after the migration is of run %d and task %q, want run 0 and no task: the release before ran no plan", check.Generation, check.Task)
	}
}
