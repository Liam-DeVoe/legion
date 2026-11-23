package store_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/processes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/storesmove"
)

// rowQuerier is what each opener hands out a connection through: a pool of this package's, a
// pgxpool.Pool or one connection.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// writerMark reads store.StoresMovedWriterSetting on a connection of q: its value, or "null"
// where the session holds no such setting.
func writerMark(t *testing.T, q rowQuerier, opener string) string {
	t.Helper()
	var setting *string
	if err := q.QueryRow(context.Background(), `select current_setting($1, true)`, store.StoresMovedWriterSetting).Scan(&setting); err != nil {
		t.Fatalf("read %s on a connection of %s: %v", store.StoresMovedWriterSetting, opener, err)
	}
	if setting == nil {
		return "null"
	}
	return *setting
}

// requireMarked fails unless a connection of q, which opener opened, reads
// store.StoresMovedWriterSetting as on.
func requireMarked(t *testing.T, q rowQuerier, opener string) {
	t.Helper()
	if got := writerMark(t, q, opener); got != "on" {
		t.Errorf("a connection of %s reads %s = %s, want on: every connection this build opens to Dispatch's database carries the writer mark (store.markWriter)",
			opener, store.StoresMovedWriterSetting, got)
		return
	}
	t.Logf("a connection of %s reads %s = on", opener, store.StoresMovedWriterSetting)
}

// TestEveryConnectionOfThePoolCarriesTheStoresMovedWriterMark holds every opener of a connection
// to Dispatch's database to the startup parameter dispatch.stores_moved_writer=on
// (store.StoresMovedWriterSetting), which is how the triggers the move at boot leaves on
// graph_nodes for the release before tell this build's writes from that release's
// (dispatch://INFRA-1098's R32). Each opener answers on for current_setting(…, true) on a
// connection of its own:
//
//   - the shared pool of store.Open;
//   - each pool Pool.separate opens from the shared pool's configuration: the document rooms',
//     their appends', the listener's (Listener and Dedicated), the loop leases', the outbox's, the
//     compaction's, the display tree fill's, the event horizon's and the health probe's;
//   - the registry pool processes.Register opens from the same configuration, which it hands no
//     caller, so a trigger of the test's own on server_processes records the setting of the
//     session that inserts the process's row;
//   - store.Connect's one connection, which graphcensus's store census, grapherase, the census's
//     snapshots of what only docs reads (cmd/dispatch's inCensusSnapshot) and store.CheckVersions
//     each open;
//   - the pre-deploy census's one connection, whose census file of the test's own reads the
//     setting and counts 1 where it is not on.
//
// pgmigrate's lock watch dials only while a migration waits on a lock, from a copy of the
// migration connection's configuration, which is the shared pool's; the test does not make a
// migration wait. A connection that carries no startup parameter, pgx's own, reads null, so the
// read is no constant.
//
// The other half of the check reads the triggers the move at boot leaves on graph_nodes for the
// release before (graphstore.StoresMovedTriggers): once storesmove.Move has swapped the database's
// four tables for the views, a step row inserted on a connection of the shared pool, which carries
// the mark, takes no visibility or state tuple from graph_nodes_workflow_state, while the same
// insert on pgx's own connection, which carries none, takes one of each
// (requireTheTriggersPassOverMarkedWrites).
func TestEveryConnectionOfThePoolCarriesTheStoresMovedWriterMark(t *testing.T) {
	ctx := context.Background()
	database := store.OpenEmptyTestStore(t)
	// The registry's table is a migration's, so the database is migrated; nothing else here
	// reads the schema.
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool := database.Pool
	databaseURL := pool.Config().ConnString()

	unmarked, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect with pgx's own configuration: %v", err)
	}
	defer unmarked.Close(context.Background())
	if got := writerMark(t, unmarked, "pgx.Connect"); got != "null" {
		t.Fatalf("a connection pgx opens with no startup parameter reads %s = %s, want null: the database itself sets it, so no opener's on proves anything",
			store.StoresMovedWriterSetting, got)
	}

	requireMarked(t, pool, "the shared pool (store.Open)")
	for _, separate := range []struct {
		opener string
		open   func() (rowQuerier, error)
	}{
		{"the document rooms' pool (Pool.Rooms)", func() (rowQuerier, error) { return pool.Rooms() }},
		{"the room appends' pool (Pool.RoomAppends)", func() (rowQuerier, error) { return pool.RoomAppends() }},
		{"the outbox's pool (Pool.Outbox)", func() (rowQuerier, error) { return pool.Outbox() }},
		{"the compaction's pool (Pool.Compaction)", func() (rowQuerier, error) { return pool.Compaction() }},
		{"the display tree fill's pool (Pool.TextTrees)", func() (rowQuerier, error) { return pool.TextTrees() }},
		{"the event horizon's pool (Pool.Horizon)", func() (rowQuerier, error) { return pool.Horizon() }},
		{"the health probe's pool", func() (rowQuerier, error) { return pool.HealthPool() }},
		{"the loop leases' pool (Pool.Leases)", func() (rowQuerier, error) {
			leases, err := pool.Leases()
			if err != nil {
				return nil, err
			}
			return leases.For("stores-moved-writer-mark")
		}},
	} {
		opened, err := separate.open()
		if err != nil {
			t.Fatalf("open %s: %v", separate.opener, err)
		}
		requireMarked(t, opened, separate.opener)
	}
	listener, err := pool.Listener(ctx)
	if err != nil {
		t.Fatalf("open the listener's connection: %v", err)
	}
	defer listener.Close(context.Background())
	requireMarked(t, listener, "the listener's pool (Pool.Listener)")
	dedicated, err := pool.Dedicated(ctx, "stores-moved-writer-mark")
	if err != nil {
		t.Fatalf("open a dedicated connection: %v", err)
	}
	defer dedicated.Close(context.Background())
	requireMarked(t, dedicated, "the listener's pool (Pool.Dedicated)")

	requireTheRegistryPoolMarked(t, ctx, pool)

	conn, err := store.Connect(ctx, databaseURL, "stores-moved-writer-mark")
	if err != nil {
		t.Fatalf("connect through store.Connect: %v", err)
	}
	defer conn.Close(context.Background())
	requireMarked(t, conn, "store.Connect")

	requireTheCensusConnectionMarked(t, ctx)
	requireTheTriggersPassOverMarkedWrites(t, ctx, database, unmarked)
}

// requireTheTriggersPassOverMarkedWrites moves the stores of database (storesmove.Move), whose four
// tables hold no row, so the swap creates the two triggers, then inserts one step row on a
// connection of the shared pool, which carries the writer mark, and one on unmarked, which carries
// none, each as the release before's putNode inserts one. It fails unless the marked row takes no
// visibility or state tuple and the unmarked row takes one of each.
func requireTheTriggersPassOverMarkedWrites(t *testing.T, ctx context.Context, database *store.Store, unmarked *pgx.Conn) {
	t.Helper()
	if _, err := storesmove.Move(ctx, database.Pool.Config().ConnString(), storesmove.Options{}); err != nil {
		t.Fatalf("move the stores, which creates the triggers: %v", err)
	}
	const insert = `
		insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
		values ('step', $1, 'step', 'project', 'live', '{}', now(), '{"kind":"session","id":"session-0123456789abcdef"}', 0)`
	stated := func(id string) map[string]int {
		rows, err := database.Pool.Query(ctx, `select kind from graph_tuples where from_kind = 'step' and from_id = $1`, id)
		if err != nil {
			t.Fatalf("read the tuples of %s: %v", id, err)
		}
		kinds, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read the tuples of %s: %v", id, err)
		}
		counted := map[string]int{}
		for _, kind := range kinds {
			counted[kind]++
		}
		return counted
	}
	const markedRun, unmarkedRun = "0f1e2d3c-0000-4000-8000-00000000aa01", "0f1e2d3c-0000-4000-8000-00000000aa02"
	if _, err := database.Pool.Exec(ctx, insert, markedRun); err != nil {
		t.Fatalf("insert a step row on the shared pool: %v", err)
	}
	if _, err := unmarked.Exec(ctx, insert, unmarkedRun); err != nil {
		t.Fatalf("insert a step row with no mark: %v", err)
	}
	if got := stated(markedRun); len(got) != 0 {
		t.Errorf("a step row the shared pool inserts takes the tuples %v from the triggers, want none: they pass over a marked write", got)
	}
	if got := stated(unmarkedRun); got["visibility"] != 1 || got["state"] != 1 || len(got) != 2 {
		t.Errorf("a step row a connection with no mark inserts takes the tuples %v from the triggers, want one visibility and one state", got)
	}
	t.Logf("a step row the shared pool inserts takes no tuple from the triggers, and one with no mark takes %v", stated(unmarkedRun))
}

// requireTheRegistryPoolMarked registers a process through processes.Register, whose pool of one
// connection writes its server_processes row, and fails unless the session that inserted the row
// read the writer mark as on. A trigger of the test's own records that session's setting.
func requireTheRegistryPoolMarked(t *testing.T, ctx context.Context, pool *store.Pool) {
	t.Helper()
	for _, statement := range []string{
		`create table writer_marks_seen (setting text)`,
		`create function writer_mark_seen() returns trigger language plpgsql as $$
		begin
		  insert into writer_marks_seen (setting) values (coalesce(current_setting('` + store.StoresMovedWriterSetting + `', true), 'null'));
		  return null;
		end $$`,
		`create trigger writer_mark_seen after insert on server_processes for each row execute function writer_mark_seen()`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("record the setting of the session that registers a process (%s): %v", statement, err)
		}
	}
	registration, err := processes.Register(ctx, pool, processes.Instance{SigningKeys: []string{"stores-moved-writer-mark"}})
	if err != nil {
		t.Fatalf("register a process: %v", err)
	}
	if err := registration.Deregister(ctx); err != nil {
		t.Fatalf("deregister the process: %v", err)
	}
	var seen []string
	if err := pool.QueryRow(ctx, `select coalesce(array_agg(setting), '{}') from writer_marks_seen`).Scan(&seen); err != nil {
		t.Fatalf("read the setting the registry's session held: %v", err)
	}
	if len(seen) != 1 || seen[0] != "on" {
		t.Errorf("the session that inserted the process's row read %s as %v, want one row of on: the registry pool processes.Register opens copies the shared pool's configuration, the writer mark among it",
			store.StoresMovedWriterSetting, seen)
		return
	}
	t.Logf("a connection of the registry pool (processes.Register) reads %s = on", store.StoresMovedWriterSetting)
}

// requireTheCensusConnectionMarked takes the pre-deploy census over a database that records one
// version, 1, and a set whose version 2 is pending, of the test's own, whose census counts 1 where
// the census's connection does not read the writer mark as on, and fails unless it counts 0. A
// database that records no version is a fresh one, of which the census reads nothing.
func requireTheCensusConnectionMarked(t *testing.T, ctx context.Context) {
	t.Helper()
	database := store.OpenEmptyTestStore(t)
	if _, err := database.Pool.Exec(ctx, `create table schema_migrations (version bigint primary key); insert into schema_migrations (version) values (1)`); err != nil {
		t.Fatalf("record version 1: %v", err)
	}
	set := fstest.MapFS{
		"migrations/0001_base.up.sql":        {Data: []byte("select 1")},
		"migrations/0002_writer_mark.up.sql": {Data: []byte("select 1")},
		"migrations/0002_writer_mark.census.sql": {Data: []byte("-- 1 where the census's own connection lacks the writer mark\n" +
			"select (current_setting('" + store.StoresMovedWriterSetting + "', true) is distinct from 'on')::int")},
	}
	report, err := store.CensusOf(ctx, database.Pool.Config().ConnString(), set)
	if err != nil {
		t.Fatalf("take the census: %v", err)
	}
	if len(report.Pending) != 1 || report.Pending[0].Count == nil {
		t.Fatalf("the census answered %d pending migrations, want the one whose census reads the writer mark, with its count", len(report.Pending))
	}
	if count := *report.Pending[0].Count; count != 0 {
		t.Errorf("the census's own connection does not read %s as on (its census counted %d): store.Census connects through connConfig, which marks the connection",
			store.StoresMovedWriterSetting, count)
		return
	}
	t.Logf("the pre-deploy census's connection (store.Census) reads %s = on", store.StoresMovedWriterSetting)
}
