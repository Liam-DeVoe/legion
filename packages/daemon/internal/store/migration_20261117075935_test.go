package store

import (
	"context"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// recordTurnVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const recordTurnVersion = 20261117075935

// The record of the turn that confirmed a relaunch record is kept on the claim's own row
// (supervise.Claim.RecordTurn), because the end of that turn, and the confirmation of the record
// sent again after a cut, can each be a daemon restart away: the restarted daemon decides from
// the row alone whether the turn ending is the record's and what it takes back. The round trip
// is the column through putClaim and scanClaim. A row an earlier build of this release wrote also
// holds the start of the record's turn (`at`), which nothing reads any more: it still reads back
// as the record it holds, or a daemon rolled onto this build could not read its claims. The
// decode refuses a record no confirmation writes, and the migration gives every row it finds none.
func TestARelaunchRecordsTurnIsKeptOnTheClaimRow(t *testing.T) {
	t.Run("the record round-trips with the claim, and its absence is null", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		want := tmuxClaim("legion-LEGION-209-recordturn")
		want.RecordTurn = &supervise.RecordTurn{
			Base:     supervise.TurnCounts{Deaths: 1, PodEndsNotOurs: 2},
			TakeBack: supervise.TurnCounts{Deaths: 1, PodEndsNotOurs: 1},
		}
		if err := store.PutClaim(ctx, want); err != nil {
			t.Fatalf("put the claim with its record turn: %v", err)
		}
		read := onlyClaim(t, store)
		turn := read.RecordTurn
		if turn == nil {
			t.Fatal("the claim read back holds no record turn, want the one written")
		}
		if turn.Base != want.RecordTurn.Base || turn.TakeBack != want.RecordTurn.TakeBack {
			t.Errorf("the record turn read back holds base %+v and take-back %+v, want %+v and %+v",
				turn.Base, turn.TakeBack, want.RecordTurn.Base, want.RecordTurn.TakeBack)
		}

		// A claim that holds none writes null, not an empty record.
		want.RecordTurn = nil
		if err := store.PutClaim(ctx, want); err != nil {
			t.Fatalf("put the claim with no record turn: %v", err)
		}
		if read := onlyClaim(t, store); read.RecordTurn != nil {
			t.Errorf("the claim read back holds the record turn %+v, want none", read.RecordTurn)
		}
		var null bool
		if err := store.Pool().QueryRow(ctx, "select record_turn is null from claims where token = $1",
			string(want.Token)).Scan(&null); err != nil {
			t.Fatalf("read the column: %v", err)
		}
		if !null {
			t.Error("a claim that holds no record turn wrote one, want null")
		}
	})

	t.Run("a record written with its turn's start still reads back", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		c := tmuxClaim("legion-LEGION-209-recordturn")
		if err := store.PutClaim(ctx, c); err != nil {
			t.Fatalf("put the claim: %v", err)
		}
		const stored = `{"at": "2026-11-17T07:59:35.123456Z", "base": {"deaths": 1, "podEndsNotOurs": 2}, "takeBack": {"deaths": 1, "podEndsNotOurs": 1}}`
		if _, err := store.Pool().Exec(ctx, "update claims set record_turn = $2::jsonb where token = $1",
			string(c.Token), stored); err != nil {
			t.Fatalf("write the record turn %s: %v", stored, err)
		}
		want := supervise.RecordTurn{
			Base:     supervise.TurnCounts{Deaths: 1, PodEndsNotOurs: 2},
			TakeBack: supervise.TurnCounts{Deaths: 1, PodEndsNotOurs: 1},
		}
		if read := onlyClaim(t, store); read.RecordTurn == nil || *read.RecordTurn != want {
			t.Errorf("the record turn %s read back as %+v, want %+v", stored, read.RecordTurn, want)
		}
	})

	t.Run("the decode refuses a record no confirmation writes", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		c := tmuxClaim("legion-LEGION-209-recordturn")
		if err := store.PutClaim(ctx, c); err != nil {
			t.Fatalf("put the claim: %v", err)
		}
		for _, stored := range []string{
			`{"base": {"deaths": -1, "podEndsNotOurs": 0}, "takeBack": {"deaths": 0, "podEndsNotOurs": 0}}`,
			`{"base": {"deaths": 0, "podEndsNotOurs": 0}, "takeBack": {"deaths": 0, "podEndsNotOurs": -1}}`,
			`{"base": {"deaths": 0, "podEndsNotOurs": 3}, "takeBack": {"deaths": 0, "podEndsNotOurs": 2}}`,
			`{"base": {"deaths": 3, "podEndsNotOurs": 0}, "takeBack": {"deaths": 2, "podEndsNotOurs": 0}}`,
		} {
			if _, err := store.Pool().Exec(ctx, "update claims set record_turn = $2::jsonb where token = $1",
				string(c.Token), stored); err != nil {
				t.Fatalf("write the record turn %s: %v", stored, err)
			}
			if claims, err := store.Claims(ctx); err == nil || !strings.Contains(err.Error(), string(c.Token)) {
				t.Errorf("reading the record turn %s answered %+v, %v; want a refusal naming the claim", stored, claims, err)
			}
		}
	})

	t.Run("the migration gives every stored claim no record turn", func(t *testing.T) {
		ctx := context.Background()
		store := emptyStore(t)
		migrateThrough(t, store, recordTurnVersion-1)

		const token = "legion-LEGION-211-implementer"
		if _, err := store.Pool().Exec(ctx, `insert into claims (token, project, tree, issue, role, generation,
			session, session_file, state, failure, launch_failures, prompt_failures, prompt_retires, uncertain_streak)
			values ($1, 'legion', 'LEGION-208', 'LEGION-208', 'implementer', 1, '', '', 'idle', '', 0, 0, 0, 0)`, token); err != nil {
			t.Fatalf("record %s before the migration: %v", token, err)
		}
		_, err := store.Pool().Exec(ctx, `update claims set record_turn = '{}'::jsonb`)
		if err == nil || !strings.Contains(err.Error(), "record_turn") {
			t.Fatalf("writing record_turn before the migration = %v, want the column to be unknown", err)
		}

		if _, err := store.Migrate(ctx); err != nil {
			t.Fatalf("migrate through the record turn: %v", err)
		}

		var null bool
		if err := store.Pool().QueryRow(ctx, "select record_turn is null from claims where token = $1", token).Scan(&null); err != nil {
			t.Fatalf("read the column after the migration: %v", err)
		}
		if !null {
			t.Error("the stored claim holds a record turn after the migration, want null")
		}
		if read := onlyClaim(t, store); read.RecordTurn != nil {
			t.Errorf("the stored claim reads the record turn %+v after the migration, want none", read.RecordTurn)
		}
	})
}
