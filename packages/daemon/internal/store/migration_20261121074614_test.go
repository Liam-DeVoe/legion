package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/store/migrations"
)

// phaseVerdictGoneVersion is the version of the migration this file tests.
const phaseVerdictGoneVersion = 20261121074614

// 20261121074614 drops phases.verdict and adds phases.states_serial at 0 on every row the release
// before wrote. The way back its header gives (migrations.WayBack), run as an operator runs it
// through psql, adds the verdict column back at its default, so the release before's read and
// write of a phase row name a column that stands, drops states_serial and deletes the migration's
// own row; the roll-forward's Migrate then applies the migration again, and only it.
func TestThePhaseVerdictGoneMigrationAndItsWayBack(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, phaseVerdictGoneVersion-1)
	exec := func(statement string) {
		t.Helper()
		if _, err := store.Pool().Exec(ctx, statement, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("run %q: %v", statement, err)
		}
	}
	exec(`insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
		values ('ACME-1', 'ACME-1', 'ACME', 'root', 'testing', 1, 'testing', 'A', 3)`)
	// The release before's write of a tester's row names the verdict.
	exec(`insert into phases (issue, role, claim, handoff_commit, rounds, verdict, summary)
		values ('ACME-1', 'tester', 'tester-claim', 'head', 0, 'pass', 'tested')`)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the phase verdict's removal: %v", err)
	}
	const columns = `select column_name::text from information_schema.columns where table_schema = current_schema()
		and table_name = 'phases' and column_name in ('verdict', 'states_serial') order by column_name`
	if got, want := rowsAsText(t, store, columns), []string{"states_serial"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the phases columns after the migration = %v, want %v", got, want)
	}
	if got, want := rowsAsText(t, store, `select row(role, states_serial, summary)::text from phases`), []string{"(tester,0,tested)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the tester's row after the migration = %v, want %v: kept, at serial 0", got, want)
	}

	wayBack, err := migrations.WayBack(phaseVerdictGoneVersion)
	if err != nil {
		t.Fatalf("read the way back of %d: %v", phaseVerdictGoneVersion, err)
	}
	exec(wayBack)
	if got, want := rowsAsText(t, store, columns), []string{"verdict"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the phases columns after the way back = %v, want %v", got, want)
	}
	// The release before's write after the way back takes, naming the verdict again.
	exec(`update phases set verdict = 'fail' where issue = 'ACME-1' and role = 'tester'`)
	if got, want := rowsAsText(t, store, `select count(*)::text from schema_version where version = 20261121074614`), []string{"0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the migration's rows after the way back = %v, want %v", got, want)
	}

	applied, err := store.Migrate(ctx)
	if err != nil || applied != 1 {
		t.Fatalf("the roll-forward's migrate applied %d, %v; want the phase verdict's removal alone", applied, err)
	}
	if got, want := rowsAsText(t, store, columns), []string{"states_serial"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the phases columns after the roll-forward = %v, want %v", got, want)
	}
}
