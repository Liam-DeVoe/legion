package store

import (
	"context"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

const removedReachNoticesVersion = 20261116082001

// A database an earlier build of the release that reports claims out of reach ran against can hold
// outbox rows of the two notice kinds that release dropped, and the table of its project's notice
// marks. The cleanup deletes those rows and that table and nothing else, so a later migration that
// writes outbox_kind_check without those kinds is not refused over a pending row of one. The
// negative control is the database before the cleanup, which holds both rows and the table.
func TestTheCleanupDropsTheRemovedReachNoticesAndNothingElse(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("read the embedded migrations: %v", err)
	}
	var before []migrations.Migration
	for _, migration := range all {
		if migration.Version != removedReachNoticesVersion {
			before = append(before, migration)
		}
	}
	if len(before) == len(all) {
		t.Fatalf("migration %d is not embedded", removedReachNoticesVersion)
	}
	if _, err := store.migrate(ctx, before); err != nil {
		t.Fatalf("migrate every other version: %v", err)
	}
	// The earlier build's database: its kind check admitted the two kinds, and it held the table.
	if _, err := store.pool.Exec(ctx, `alter table outbox drop constraint outbox_kind_check;
		create table reach_project_notices (project text primary key)`); err != nil {
		t.Fatalf("shape the earlier build's database: %v", err)
	}
	for _, kind := range []string{"reach_notice", "reach_controller_notice", "notice"} {
		if _, err := store.pool.Exec(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
			values ($1, 'LEGION-208', '{}', 0, now(), '')`, kind); err != nil {
			t.Fatalf("queue a %s row: %v", kind, err)
		}
	}
	count := func(kinds ...string) int {
		var n int
		if err := store.pool.QueryRow(ctx, `select count(*) from outbox where kind = any($1)`, kinds).Scan(&n); err != nil {
			t.Fatalf("count the rows of %v: %v", kinds, err)
		}
		return n
	}
	table := func() bool {
		var exists bool
		if err := store.pool.QueryRow(ctx, `select to_regclass('reach_project_notices') is not null`).Scan(&exists); err != nil {
			t.Fatalf("look for reach_project_notices: %v", err)
		}
		return exists
	}
	if got := count("reach_notice", "reach_controller_notice"); got != 2 || !table() {
		t.Fatalf("before the cleanup the database holds %d removed notice rows and the table %t, want 2 and the table", got, table())
	}

	applied, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate through the cleanup: %v", err)
	}
	if applied != 1 {
		t.Fatalf("the upgrade applied %d migrations, want the cleanup alone", applied)
	}
	if got := count("reach_notice", "reach_controller_notice"); got != 0 || table() {
		t.Fatalf("after the cleanup the database holds %d removed notice rows and the table %t, want neither", got, table())
	}
	if got := count("notice"); got != 1 {
		t.Fatalf("after the cleanup the database holds %d notice rows, want the one it held", got)
	}
}
