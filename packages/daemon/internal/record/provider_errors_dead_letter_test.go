package record

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/sjawhar/envoy/listenerwire"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// deadLetterClaim records the claim of LEGION-208's implementer, its runs through run 2 told, and
// answers its token.
func deadLetterClaim(t *testing.T, tx pgx.Tx) claim.Token {
	t.Helper()
	project, err := claim.ProjectToken("LEGION")
	if err != nil {
		t.Fatal(err)
	}
	token, err := claim.NewToken(project, "LEGION-208", claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `insert into claims (token, project, tree, issue, role, generation, session,
		session_file, state, launch_failures, prompt_failures, prompt_retires, uncertain_streak,
		provider_error_run, provider_error_run_id, provider_errors_told_run)
		values ($1, $2, 'LEGION-208', 'LEGION-208', 'implementer', 1, 'ses', '', 'idle', 0, 0, 0, 0, 0, 2, 2)`,
		string(token), project); err != nil {
		t.Fatalf("record the claim: %v", err)
	}
	return token
}

// owedRunsOf is each run the claim of token owes, oldest first.
func owedRunsOf(t *testing.T, tx pgx.Tx, token claim.Token) []uint64 {
	t.Helper()
	rows, err := tx.Query(context.Background(), "select run from provider_errors_owed where claim_token = $1 order by run", string(token))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var runs []uint64
	for rows.Next() {
		var run int64
		if err := rows.Scan(&run); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, uint64(run))
	}
	return runs
}

// A dead letter of a recorded copy of a provider-errors notice owes again each run of the copy no
// later copy tells, once for each copy, in the run's own words; one whose dead_letter field says
// the copy may have reached the session that held the role counts as told and owes nothing; a
// repeat of either changes nothing; and a dead letter of a copy no row records owes nothing.
func TestADeadLetterOwesAgainOnlyTheRunsItsCopyAloneTold(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	records := NewStore()
	inTx(t, st, func(tx pgx.Tx) {
		token := deadLetterClaim(t, tx)
		must(t, records.RecordProviderErrorsCopy(ctx, tx, "copy-a", ToldRuns{Claim: token, Runs: []ToldRun{{Run: 1, Reason: "run one"}, {Run: 2, Reason: "run two"}}}))
		must(t, records.RecordProviderErrorsCopy(ctx, tx, "copy-b", ToldRuns{Claim: token, Runs: []ToldRun{{Run: 2, Reason: "run two"}}}))
		must(t, records.RecordProviderErrorsCopy(ctx, tx, "copy-c", ToldRuns{Claim: token, Runs: []ToldRun{{Run: 1, Reason: "run one"}}}))
		// A copy recorded again under its event id keeps its first record.
		must(t, records.RecordProviderErrorsCopy(ctx, tx, "copy-c", ToldRuns{Claim: token, Runs: []ToldRun{{Run: 9, Reason: "run nine"}}}))

		applied, err := records.ProviderErrorsDeadLetter(ctx, tx, "copy-a", listenerwire.DeadLetterExpired)
		if err != nil {
			t.Fatal(err)
		}
		// Run 1 is told by copy-c and run 2 by copy-b, both recorded after copy-a.
		if applied.Outcome != DeadLetterToldLater || len(applied.Runs) != 0 || applied.Claim != token {
			t.Fatalf("the dead letter of copy-a applied %+v, want told_later: copies after it tell both its runs", applied)
		}
		applied, err = records.ProviderErrorsDeadLetter(ctx, tx, "copy-b", listenerwire.DeadLetterExpiredUnconfirmed)
		if err != nil {
			t.Fatal(err)
		}
		if applied.Outcome != DeadLetterUnconfirmed {
			t.Fatalf("the unconfirmed dead letter of copy-b applied %+v, want unconfirmed, which counts as told", applied)
		}
		if owed := owedRunsOf(t, tx, token); len(owed) != 0 {
			t.Fatalf("the claim owes runs %v after an unconfirmed dead letter, want none", owed)
		}
		applied, err = records.ProviderErrorsDeadLetter(ctx, tx, "copy-c", listenerwire.DeadLetterDeliveryMode)
		if err != nil {
			t.Fatal(err)
		}
		if applied.Outcome != DeadLetterOwedAgain || !slices.Equal(applied.Runs, []uint64{1}) {
			t.Fatalf("the dead letter of copy-c applied %+v, want run 1 owed again: no copy after it tells it", applied)
		}
		var reason string
		if err := tx.QueryRow(ctx, "select reason from provider_errors_owed where claim_token = $1 and run = 1", string(token)).Scan(&reason); err != nil || reason != "run one" {
			t.Fatalf("the owed run 1 reads %q (%v), want its own words", reason, err)
		}
		for _, again := range []string{"copy-a", "copy-b", "copy-c"} {
			applied, err := records.ProviderErrorsDeadLetter(ctx, tx, again, listenerwire.DeadLetterExpired)
			if err != nil {
				t.Fatal(err)
			}
			if applied.Outcome != DeadLetterRepeated {
				t.Fatalf("a second dead letter of %s applied %+v, want repeated", again, applied)
			}
		}
		if owed := owedRunsOf(t, tx, token); !slices.Equal(owed, []uint64{1}) {
			t.Fatalf("the claim owes runs %v, want run 1 alone", owed)
		}
		applied, err = records.ProviderErrorsDeadLetter(ctx, tx, "copy-nobody-recorded", listenerwire.DeadLetterExpired)
		if err != nil {
			t.Fatal(err)
		}
		if applied.Outcome != DeadLetterUnknown {
			t.Fatalf("the dead letter of an unrecorded copy applied %+v, want unknown", applied)
		}
	})
}

// A copy of a claim no row holds records nothing, and one whose claim's row goes goes with it.
func TestACopyOfAGoneClaimRecordsNothing(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	records := NewStore()
	inTx(t, st, func(tx pgx.Tx) {
		must(t, records.RecordProviderErrorsCopy(ctx, tx, "copy-gone", ToldRuns{Claim: "legion-legion-legion-999-implementer", Runs: []ToldRun{{Run: 1, Reason: "run one"}}}))
		token := deadLetterClaim(t, tx)
		must(t, records.RecordProviderErrorsCopy(ctx, tx, "copy-kept", ToldRuns{Claim: token, Runs: []ToldRun{{Run: 1, Reason: "run one"}}}))
		if _, err := tx.Exec(ctx, "delete from claims where token = $1", string(token)); err != nil {
			t.Fatal(err)
		}
		var copies int
		if err := tx.QueryRow(ctx, "select count(*) from provider_errors_copies").Scan(&copies); err != nil || copies != 0 {
			t.Fatalf("provider_errors_copies holds %d rows (%v), want none", copies, err)
		}
	})
}
