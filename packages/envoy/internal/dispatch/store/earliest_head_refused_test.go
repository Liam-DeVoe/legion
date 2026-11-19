package store

import (
	"context"
	"strings"
	"testing"
)

// earliestHeadBrokerRevocations is 20261116232217 as the heads of cybermonklabs/legion#4199 from
// cf83238fe9 (2026-11-16T23:38Z) to fe1d01bd2e (2026-11-17T01:37Z) wrote it: one row per address,
// with no issuer, subject or holder_subject, which aa6bb03e14 added at 04:05Z
// (git show cf83238fe9:packages/envoy/internal/dispatch/store/migrations/20261116232217_broker_revocations.up.sql).
const earliestHeadBrokerRevocations = `
create table broker_revocations (
  email text primary key check (email <> '' and email = lower(btrim(email))),
  generation bigint not null default 1,
  recorded_at timestamptz not null default now(),
  tried_at timestamptz,
  done_at timestamptz
);
create function broker_revocations_notify() returns trigger language plpgsql as $$
begin
  perform pg_notify('dispatch_broker_revocations', '');
  return null;
end $$;
create trigger broker_revocations_notify after insert or update of generation on broker_revocations
  for each row execute function broker_revocations_notify();
`

// TestADatabaseOfTheEarliestHeadsShapeIsRefusedAtMigrate: a database one of those heads migrated
// records 20261116232217 as run and holds the table above. Every later migration used to apply
// over it with no error — 20261118152422 adds session_generation `if not exists` and creates a
// guard whose body names issuer, subject and holder_subject, which plpgsql does not resolve until
// the trigger runs — and then every sign-in that replaced an account failed with 42703 and rolled
// back, so Dispatch kept recording the replaced account and the broker was never told. Migrate now
// refuses such a database (20261119064905), naming the window and the reset, as every head before
// 8db739b89a already does at 20261118041916's drop of refuse_agent_token_account_change.
//
// Control, in the test: a database this head migrated from empty takes the same two sign-ins and
// records the replacing account.
func TestADatabaseOfTheEarliestHeadsShapeIsRefusedAtMigrate(t *testing.T) {
	ctx := context.Background()
	const email = "earliest-head@example.com"

	control := migratedEmptyStore(t)
	people := brokerPeople(control)
	signInAll(t, people, email, "account-a", "account-b")
	var sub string
	if err := control.Pool.QueryRow(ctx, `select sub from people where email = $1`, email).Scan(&sub); err != nil || sub != "account-b" {
		t.Fatalf("control: on a database this head migrated, %s records %q, %v; want account-b", email, sub, err)
	}

	database := openEmptyTestStore(t)
	migrateThrough(t, database, 20261116232216)
	if _, err := database.Pool.Exec(ctx, earliestHeadBrokerRevocations); err != nil {
		t.Fatalf("make the earliest head's broker_revocations: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `insert into schema_migrations (version) values (20261116232217)`); err != nil {
		t.Fatalf("record 20261116232217 as run: %v", err)
	}
	err := database.Migrate(ctx)
	if err == nil {
		t.Fatalf("Migrate over the earliest head's broker_revocations answered no error; want it refused, naming the reset")
	}
	for _, want := range []string{"broker_revocations holds no issuer column", "Reset this database and the secrets broker's database together"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Migrate refused with %v; want a message holding %q", err, want)
		}
	}
	// The refusal stands for the whole of Migrate: the sign-in that used to fail with 42703 never
	// runs, because a server whose Migrate refused does not start.

	// The same two databases in a schema whose name needs quotes. Pasted together, the name folds
	// Dispatch to dispatch: Postgres then answers 3F000 (schema "dispatch" does not exist) at this
	// migration, on the earliest shape and on a healthy database alike, so every deployment whose
	// schema needs quotes would fail to migrate. The migration builds the name with format %I.
	inQuotedSchema := func(t *testing.T) *Store {
		t.Helper()
		base := openEmptyTestStore(t)
		var name string
		if err := base.Pool.QueryRow(ctx, `select current_database()`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if _, err := base.Pool.Exec(ctx, `create schema "Dispatch"; alter database "`+name+`" set search_path = "Dispatch"`); err != nil {
			t.Fatalf("give the database a schema whose name needs quotes: %v", err)
		}
		s, err := Open(ctx, base.Pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Pool.Close)
		if schema := tablesSchema(t, s); schema != "Dispatch" {
			t.Fatalf("a new connection's current schema is %q, want Dispatch", schema)
		}
		return s
	}
	healthy := inQuotedSchema(t)
	if err := healthy.Migrate(ctx); err != nil {
		t.Errorf("Migrate on an empty database in the schema Dispatch answered %v; want it migrated", err)
	}
	capitals := inQuotedSchema(t)
	migrateThrough(t, capitals, 20261116232216)
	for _, statement := range []string{earliestHeadBrokerRevocations, `insert into schema_migrations (version) values (20261116232217)`} {
		if _, err := capitals.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("make the earliest head's shape in the schema Dispatch: %v", err)
		}
	}
	if err := capitals.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "broker_revocations holds no issuer column") {
		t.Errorf("Migrate over the earliest head's broker_revocations in the schema Dispatch answered %v; want the same refusal as in an unquoted schema", err)
	}
}
