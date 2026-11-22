package store

import (
	"context"
	"reflect"
	"testing"
)

// releaseClaimsVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const releaseClaimsVersion = 20261122123244

// 20261122123244 adds issue_releases.suspended_claims, the claims a release's stops suspended. A
// mark the release before wrote takes the default, no claim, and keeps the roles it recorded, which
// the check that the pod went on working reads instead. The way back, the last paragraph of the
// migration's header (migrations.WayBack), drops the column and deletes the version row, so the
// roll forward runs the migration again.
func TestAReleaseMarkRecordsItsClaimsAndTheWayBackDropsThem(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, releaseClaimsVersion-1)
	exec := func(statement string) {
		t.Helper()
		if _, err := store.Pool().Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	exec(`insert into issue_releases (project, issue, generation, role, boundary_row, started_row, first_row, last_row, suspended)
		values ('legion', 'LEGION-208', 1, 'implementer', 2, 0, 1, 3, '{implementer}')`)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the claims of a release: %v", err)
	}
	const mark = `select concat(array_to_string(suspended, ','), '|', array_to_string(suspended_claims, ',')) from issue_releases`
	if got := rowsAsText(t, store, mark); !reflect.DeepEqual(got, []string{"implementer|"}) {
		t.Fatalf("the mark after the migration = %v, want its roles kept and no claim", got)
	}

	exec(wayBackOf(t, releaseClaimsVersion, "the release that records the claims a release suspended"))
	if got := rowsAsText(t, store, `select column_name::text from information_schema.columns
		where table_name = 'issue_releases' and column_name = 'suspended_claims'`); len(got) != 0 {
		t.Fatalf("the columns of issue_releases after the way back hold %v, want no suspended_claims", got)
	}
	if got := rowsAsText(t, store, `select count(*)::text from schema_version where version = 20261122123244`); !reflect.DeepEqual(got, []string{"0"}) {
		t.Fatalf("the version rows of the migration after the way back = %v, want none", got)
	}
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("roll forward: %v", err)
	}
	if got := rowsAsText(t, store, mark); !reflect.DeepEqual(got, []string{"implementer|"}) {
		t.Fatalf("the mark after the roll forward = %v, want its roles kept and no claim", got)
	}
}
