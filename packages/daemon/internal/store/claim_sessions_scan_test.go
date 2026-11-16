package store

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The boot's conflict read must cost what the claims record, not what claim_sessions has gathered
// (review N2 of #4220): SessionConflicts finds each recorded name "through the primary key, never
// the whole table". TestTheBootHoldsAProjectOfProductionSizeInAFewStatements counts the rows a
// statement returns, which a full scan inside the server does not change; this test counts the
// tuples of claim_sessions the server reads (pg_stat_user_tables: seq_tup_read and idx_tup_fetch).
//
// Over the production-size project (7,508 claims, 33,786 names) and 500,000 rows no claim records
// any more, analyzed as autovacuum would, SessionConflicts reads at most twice the names the claims
// record. At 723aa7826a the planner joins the unnested names to the table by a hash join over a
// sequential scan, and the read takes every one of the 533,786 rows.
func TestTheBootsConflictReadReadsOnlyTheRowsClaimsRecord(t *testing.T) {
	ctx := context.Background()
	store := migratedStore(t)
	names := productionClaims*3 + productionClaims/2*3
	if _, err := store.Pool().Exec(ctx, `insert into claims (token, project, tree, issue, role, generation,
		session, session_file, state, failure, launch_failures, prompt_failures, prompt_retires, uncertain_streak,
		abandoned_session)
		select 'legion-LEGION-' || n || '-implementer', 'legion', 'LEGION-' || n, 'LEGION-' || n, 'implementer', 1,
			s, '/state/sessions/--w--/2026-11-16T00-00-00-000Z_' || s || '.jsonl', 'retired', '', 0, 0, 0, 0,
			case when n % 2 = 0 then jsonb_build_object('session', a,
				'sessionFile', '/state/sessions/--w--/2026-11-15T00-00-00-000Z_' || a || '.jsonl',
				'why', 'a retry asked for a new session', 'at', '2026-11-16T00:00:00Z') end
		from (select n, gen_random_uuid()::text as s, gen_random_uuid()::text as a
			from generate_series(1, $1::int) as n) as seeded`, productionClaims); err != nil {
		t.Fatalf("record %d claims: %v", productionClaims, err)
	}
	setup := oneConnStore(t, store)
	if held, err := setup.HoldRecordedSessions(ctx, "legion"); err != nil || held != names {
		t.Fatalf("the first hold held %d names, %v; want %d", held, err, names)
	}
	setup.Close()
	const stale = 500_000
	if _, err := store.Pool().Exec(ctx, `insert into claim_sessions (project, kind, name, claim)
		select 'legion', 'session', 'stale_' || n, 'legion-LEGION-' || (n % $2::int + 1) || '-implementer'
		from generate_series(1, $1::int) as n`, stale, productionClaims); err != nil {
		t.Fatalf("record %d stale holds: %v", stale, err)
	}
	if _, err := store.Pool().Exec(ctx, `analyze claim_sessions`); err != nil {
		t.Fatal(err)
	}

	before := settledTuplesRead(ctx, t, store, -1)
	measured := oneConnStore(t, store)
	conflicts, err := measured.SessionConflicts(ctx, "legion")
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("the conflict read answered %+v, %v; want none", conflicts, err)
	}
	measured.Close() // a backend flushes its statistics as it exits
	read := settledTuplesRead(ctx, t, store, before) - before
	if bound := int64(2 * names); read > bound {
		t.Errorf("the boot's conflict read took %d tuples of claim_sessions for %d recorded names and %d rows no claim records; want at most %d: it reads the whole table, so its cost grows with the holds the cap allows",
			read, names, stale, bound)
	}
	t.Logf("the conflict read took %d tuples of claim_sessions (%d recorded names, %d stale rows)", read, names, stale)
}

// oneConnStore is a Store on store's database with a pool of one connection, so every statement it
// sends runs on one backend, whose statistics are flushed when Close ends it.
func oneConnStore(t *testing.T, store *Store) *Store {
	t.Helper()
	ctx := context.Background()
	var name string
	if err := store.Pool().QueryRow(ctx, `select current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	config, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	raw, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{pool: NewPool(raw)}
}

// settledTuplesRead is claim_sessions' seq_tup_read plus idx_tup_fetch once it stops moving. When
// from is not -1, it first waits for the counters to leave from: a read that took no tuple at all
// cannot be a conflict read of 33,786 names, so counters that never move are no measurement.
func settledTuplesRead(ctx context.Context, t *testing.T, store *Store, from int64) int64 {
	t.Helper()
	read := func() int64 {
		var n int64
		if err := store.Pool().QueryRow(ctx, `select coalesce(seq_tup_read, 0) + coalesce(idx_tup_fetch, 0)
			from pg_stat_user_tables where relname = 'claim_sessions'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	deadline := time.Now().Add(15 * time.Second)
	last := read()
	for from != -1 && last == from {
		if time.Now().After(deadline) {
			t.Fatalf("UNKNOWN: claim_sessions' read counters stayed at %d for 15 s after the conflict read; the statistics were never flushed", last)
		}
		time.Sleep(200 * time.Millisecond)
		last = read()
	}
	for stable := 0; stable < 3; {
		time.Sleep(300 * time.Millisecond)
		next := read()
		if next == last {
			stable++
		} else {
			stable, last = 0, next
		}
		if time.Now().After(deadline) {
			break
		}
	}
	return last
}
