package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// recordTurnVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const recordTurnVersion = 20261117075935

// The record of the turn that confirmed a relaunch record is kept on the claim's own row
// (supervise.Claim.RecordTurn), because the end of that turn, and the confirmation of the record
// sent again after a cut, can each be a daemon restart away: the restarted daemon decides from
// the row alone whether the turn ending is the record's and what it takes back. The round trip
// is the column through putClaim and scanClaim, and a claim read back must still find its record
// turn at its last turn's start, to the microsecond the timestamptz column keeps, or a restart
// would read the record's turn as a turn of work and end every count. The decode refuses a
// record no confirmation writes, and the migration gives every row it finds none.
func TestARelaunchRecordsTurnIsKeptOnTheClaimRow(t *testing.T) {
	t.Run("the record round-trips with the claim at its turn's start, and its absence is null", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		want := tmuxClaim("legion-LEGION-209-recordturn")
		// A start with nanoseconds, as the daemon's clock gives one: last_turn_start keeps its
		// microseconds alone.
		started := time.Date(2026, 11, 17, 7, 59, 35, 123456789, time.UTC)
		want.LastTurnStart = started
		want.RecordTurn = &supervise.RecordTurn{
			At:       started,
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
		if !turn.At.Equal(read.LastTurnStart) {
			t.Errorf("the record turn read back starts at %s and the claim's last turn at %s, want the same instant: "+
				"the restarted daemon would read the record's turn as a turn of work", turn.At, read.LastTurnStart)
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

	t.Run("the decode refuses a record no confirmation writes", func(t *testing.T) {
		ctx := context.Background()
		store := migratedStore(t)
		c := tmuxClaim("legion-LEGION-209-recordturn")
		if err := store.PutClaim(ctx, c); err != nil {
			t.Fatalf("put the claim: %v", err)
		}
		for _, stored := range []string{
			`{"at": "2026-11-17T07:59:35Z", "base": {"deaths": -1, "podEndsNotOurs": 0}, "takeBack": {"deaths": 0, "podEndsNotOurs": 0}}`,
			`{"at": "2026-11-17T07:59:35Z", "base": {"deaths": 0, "podEndsNotOurs": 0}, "takeBack": {"deaths": 0, "podEndsNotOurs": -1}}`,
			`{"at": "2026-11-17T07:59:35Z", "base": {"deaths": 0, "podEndsNotOurs": 3}, "takeBack": {"deaths": 0, "podEndsNotOurs": 2}}`,
			`{"at": "2026-11-17T07:59:35Z", "base": {"deaths": 3, "podEndsNotOurs": 0}, "takeBack": {"deaths": 2, "podEndsNotOurs": 0}}`,
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
