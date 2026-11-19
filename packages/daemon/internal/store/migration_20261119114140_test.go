package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// providerErrorsToldVersion is the version of the migration this file tests, so the test can
// migrate through everything before it whatever lands beside it.
const providerErrorsToldVersion = 20261119114140

// runColumns are the four columns 20261119114140 adds, as one claim's row holds them.
type runColumns struct {
	run, runID, owed, told int64
	reason                 string
}

func readRunColumns(t *testing.T, store *Store, token string) runColumns {
	t.Helper()
	var c runColumns
	if err := store.Pool().QueryRow(context.Background(), `select provider_error_run, provider_error_run_id,
		provider_errors_owed_run, provider_errors_told_run, provider_errors_owed_reason from claims where token = $1`,
		token).Scan(&c.run, &c.runID, &c.owed, &c.told, &c.reason); err != nil {
		t.Fatalf("read the run columns of %s: %v", token, err)
	}
	return c
}

// 20261119114140 keeps on the claim's row the notice a run of six provider-error turns owes the
// project's controller. The migration runs over a database with claims in it: a claim in a run
// at the upgrade gets the first run number, so the run it stands in is numbered above the told
// mark and is told once a turn of it from the sixth on is written; a claim whose run already
// stands at six or more owes run 1's notice at once, since its next turn can be a clean one and
// the earlier release kept no mark of whether it was told; a claim in no run keeps 0. The negative
// control is the column itself, which a write refuses before the migration.
func TestAClaimInARunAtTheUpgradeGetsTheFirstRunNumberWith20261119114140(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, providerErrorsToldVersion-1)
	for token, run := range map[string]int{"legion-LEGION-209-implementer": 4, "legion-LEGION-209-planner": 7, "legion-LEGION-209-tester": 0} {
		if _, err := store.Pool().Exec(ctx, `insert into claims (token, project, tree, issue, role, generation,
			session, session_file, state, launch_failures, prompt_failures, prompt_retires, uncertain_streak, provider_error_run)
			values ($1, 'legion', 'LEGION-208', 'LEGION-209', split_part($1, '-', 4), 1, '', '', 'idle', 0, 0, 0, 0, $2)`,
			token, run); err != nil {
			t.Fatalf("record %s before the migration: %v", token, err)
		}
	}
	if _, err := store.Pool().Exec(ctx, "update claims set provider_error_run_id = 1"); err == nil || !strings.Contains(err.Error(), "provider_error_run_id") {
		t.Fatalf("writing provider_error_run_id before the migration = %v, want the column to be unknown", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the told mark: %v", err)
	}

	if got := readRunColumns(t, store, "legion-LEGION-209-implementer"); got != (runColumns{run: 4, runID: 1}) {
		t.Errorf("the claim in a run of 4 reads %+v after the migration, want its run numbered 1, nothing owed and nothing told", got)
	}
	if got, want := readRunColumns(t, store, "legion-LEGION-209-planner"), (runColumns{run: 7, runID: 1, owed: 1,
		reason: "7 turns in a row ended in a provider error; the daemon keeps the task and sends it again every 10m0s at most"}); got != want {
		t.Errorf("the claim in a run of 7 reads %+v after the migration, want %+v: run 1's notice owed and not told", got, want)
	}
	if got := readRunColumns(t, store, "legion-LEGION-209-tester"); got != (runColumns{}) {
		t.Errorf("the claim in no run reads %+v after the migration, want every column at 0", got)
	}
}

// An earlier release names none of the four columns, so its claim write leaves each of them as it
// stands, and a run it begins would keep the number of the run before it: after a rollback and a
// roll forward that run would read as the run already told. The trigger moves the number for such
// a write, whichever release makes it. The earlier release's upsert sets provider_error_run and no
// column of 20261119114140, so a write here that sets that one column alone is its write, as the
// row's trigger sees it.
//
// The claim's first run was told before the rollback. Under the earlier release a clean turn
// resets the run and the next failed turn begins another, and a write whose reset the store never
// took drops the run below the row's: each begin moves the number, and nothing else does. Back on
// this release, the machine reads the claim with the number the trigger gave it, its next run of
// six owes its notice under that number, which stands above the told mark, and a write of this
// release that moves the number itself is left alone.
func TestARunAnEarlierReleaseBeginsReadsAsANewRunWith20261119114140(t *testing.T) {
	ctx := context.Background()
	store := migratedStore(t)
	c := tmuxClaim("legion-LEGION-212-implementer")
	c.ProviderErrorRun, c.ProviderErrorRunID = 6, 1
	c.ProviderErrorsOwed = supervise.OwedNotice{Run: 1, Reason: "6 turns in a row ended in a provider error (HTTP 529)"}
	if err := store.PutClaim(ctx, c); err != nil {
		t.Fatalf("put the claim whose first run reached six: %v", err)
	}
	token := string(c.Token)
	if _, err := store.Pool().Exec(ctx, "update claims set provider_errors_told_run = 1 where token = $1", token); err != nil {
		t.Fatalf("mark the first run told: %v", err)
	}

	earlierWrite := func(run int) {
		t.Helper()
		if _, err := store.Pool().Exec(ctx, "update claims set provider_error_run = $2 where token = $1", token, run); err != nil {
			t.Fatalf("write the run %d as the earlier release does: %v", run, err)
		}
	}
	for _, step := range []struct {
		run, wantID int
		what        string
	}{
		{0, 1, "the clean turn that ends the first run"},
		{0, 1, "a second write of the run at zero"},
		{1, 2, "the first failed turn of a new run"},
		{2, 2, "the second failed turn of that run"},
		{5, 2, "a climb whose writes in between the store did not take"},
		{1, 3, "a run below the row's, whose reset the store never took"},
	} {
		earlierWrite(step.run)
		if got := readRunColumns(t, store, token); got.runID != int64(step.wantID) || got.owed != 1 || got.told != 1 {
			t.Fatalf("after %s the row reads %+v, want the run numbered %d and the first run's notice owed and told", step.what, got, step.wantID)
		}
	}

	restored := onlyClaim(t, store)
	if restored.ProviderErrorRunID != 3 {
		t.Fatalf("this release reads the claim's run numbered %d, want the 3 the trigger gave it", restored.ProviderErrorRunID)
	}
	restored.ProviderErrorRun = 6
	restored.ProviderErrorsOwed = supervise.OwedNotice{Run: restored.ProviderErrorRunID, Reason: "6 turns in a row ended in a provider error (HTTP 503)"}
	if err := store.PutClaim(ctx, restored); err != nil {
		t.Fatalf("put the run of six that began under the earlier release: %v", err)
	}
	var owed *record.OwedProviderErrors
	if err := store.Tx(ctx, func(tx pgx.Tx) (err error) {
		owed, err = record.NewStore().ProviderErrorsOwed(ctx, tx, restored.Token)
		return err
	}); err != nil {
		t.Fatalf("read what the claim owes: %v", err)
	}
	if owed == nil || owed.Run != 3 || !strings.Contains(owed.Reason, "HTTP 503") {
		t.Fatalf("the claim owes %+v, want the run of six numbered 3 owed and untold: the old mark must not match it", owed)
	}

	restored.ProviderErrorRun, restored.ProviderErrorRunID = 1, 4
	if err := store.PutClaim(ctx, restored); err != nil {
		t.Fatalf("put this release's first turn of the next run: %v", err)
	}
	if got := readRunColumns(t, store, token); got.runID != 4 || got.told != 1 {
		t.Fatalf("after this release's own begin the row reads %+v, want the 4 it wrote and the told mark untouched", got)
	}
}
