package lockread_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/fataltest"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/lockread"
	"github.com/sjawhar/envoy/lockread/plants"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// proofRead is one read the proof judges: its name, its statement and the values of its
// parameters, and whether it must keep to its own database.
type proofRead struct {
	name, sql string
	args      []any
	keeps     bool
}

// helperReads is every source of lockread read as a statement: each source counted whole, and the
// sources that take a backend's process id read for every backend of the server, the second
// database's included (pg_stat_activity unscoped, which this directory may name), so the source
// alone is what keeps them to their own. Each counts client backends alone where it counts
// backends, so an autovacuum worker that visits the database between two reads changes nothing,
// and Locks leaves out the reading backend's own rows, which are the locks its statement takes and
// so differ between a statement and its restriction.
func helperReads() []proofRead {
	clients := `(select c.pid from pg_catalog.pg_stat_activity c where c.backend_type = 'client backend')`
	return []proofRead{
		{name: "Locks: every row of another client backend", sql: `select count(*) from ` + lockread.Locks + ` l where l.pid in ` + clients + ` and l.pid <> pg_backend_pid()`, keeps: true},
		{name: "Locks: a waiter on a transaction id", sql: `select count(*) from ` + lockread.Locks + ` l where not l.granted and l.locktype = 'transactionid'`, keeps: true},
		{name: "Activity: every client backend", sql: `select count(*) from ` + lockread.Activity + ` a where a.backend_type = 'client backend'`, keeps: true},
		{name: "Activity: a backend waiting on a lock", sql: `select count(*) from ` + lockread.Activity + ` a where a.wait_event_type = 'Lock'`, keeps: true},
		{name: "BlockingPIDs: the blockers of every backend of the server", sql: `select count(*) from pg_catalog.pg_stat_activity a, unnest(` + lockread.BlockingPIDs("a.pid") + `) b`, keeps: true},
		{name: "BackendWAL: every client backend of the server", sql: `select count(*) from pg_catalog.pg_stat_activity a, lateral ` + lockread.BackendWAL("a.pid") + ` w where a.backend_type = 'client backend'`, keeps: true},
		{name: "Slots: every client backend", sql: `select count(*) from ` + lockread.Slots + ` s where s.pid in ` + clients, keeps: true},
	}
}

// ownDatabase is the proof's own statement of the database a statement runs in, written apart
// from lockread's, so the proof does not lean on what it judges.
const ownDatabase = `(select oid from pg_catalog.pg_database where datname = pg_catalog.current_database())`

// restrictions are functions of the reading session's temporary schema, each a view or function
// of lock or session state kept to the rows of the database the session is in, by the proof's own
// statement of it: pg_stat_activity's backends of the database; pg_locks' rows of the database or
// of one of those backends; pg_blocking_pids' blockers among those backends; the backend ids of
// the database; and pg_stat_get_backend_wal's row of one of its backends. restricted puts them in
// place of the names they stand for.
var restrictions = []string{
	`create function pg_temp.proof_activity() returns setof pg_catalog.pg_stat_activity language sql stable as $$
		select * from pg_catalog.pg_stat_activity where datid = ` + ownDatabase + ` $$`,
	`create function pg_temp.proof_locks() returns setof pg_catalog.pg_locks language sql stable as $$
		select * from pg_catalog.pg_locks where database = ` + ownDatabase + `
			or pid in (select pid from pg_catalog.pg_stat_activity where datid = ` + ownDatabase + `) $$`,
	`create function pg_temp.proof_blocking_pids(p integer) returns integer[] language sql stable as $$
		select array(select b from unnest(pg_catalog.pg_blocking_pids(p)) b
			where b in (select pid from pg_catalog.pg_stat_activity where datid = ` + ownDatabase + `)) $$`,
	`create function pg_temp.proof_backend_idset() returns setof integer language sql stable as $$
		select s from pg_catalog.pg_stat_get_backend_idset() s where pg_catalog.pg_stat_get_backend_dbid(s) = ` + ownDatabase + ` $$`,
	`do $$ begin execute format(
		'create function pg_temp.proof_backend_wal(%s) returns setof record language sql stable as %L',
		pg_catalog.pg_get_function_arguments('pg_catalog.pg_stat_get_backend_wal'::regproc),
		'select * from pg_catalog.pg_stat_get_backend_wal($1) where $1 in (select pid from pg_catalog.pg_stat_activity where datid = ` + ownDatabase + `)');
	end $$`,
}

// restrictedName is each name restricted puts a restriction in place of, qualified or not,
// quoted or not, and in any case; restrictedNames is what each becomes. A name of lock or
// session state that is none of them (lockStateName) is left, and restricted refuses the
// statement: the proof cannot judge it.
var (
	restrictedName  = regexp.MustCompile(`(?i)(?:\bpg_catalog\s*\.\s*)?"?\b(pg_stat_activity|pg_locks|pg_blocking_pids|pg_stat_get_backend_idset|pg_stat_get_backend_wal)\b"?`)
	restrictedNames = map[string]string{
		"pg_stat_activity":          "pg_temp.proof_activity()",
		"pg_locks":                  "pg_temp.proof_locks()",
		"pg_blocking_pids":          "pg_temp.proof_blocking_pids",
		"pg_stat_get_backend_idset": "pg_temp.proof_backend_idset",
		"pg_stat_get_backend_wal":   "pg_temp.proof_backend_wal",
	}
	lockStateName = regexp.MustCompile(`(?i)\bpg_(lock_status|safe_snapshot_blocking_pids|prepared_xacts|stat_get_activity|stat_ssl|stat_gssapi|stat_progress_\w+|stat_get_progress_info)\b`)
)

// restricted is sql with every name restrictedName matches put through its restriction, so it
// reads the rows of its own database alone, whatever it says; ok is false when sql names lock or
// session state no restriction stands for.
func restricted(sql string) (string, bool) {
	out := restrictedName.ReplaceAllStringFunc(sql, func(name string) string {
		return restrictedNames[strings.ToLower(restrictedName.FindStringSubmatch(name)[1])]
	})
	return out, !lockStateName.MatchString(out)
}

// answer is what sql answers on conn: each row's values, the rows sorted, one a line.
func answer(ctx context.Context, conn *pgx.Conn, sql string, args ...any) (string, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			rows.Close()
			return "", err
		}
		lines = append(lines, fmt.Sprint(values...))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n"), nil
}

// judged is what the proof found of one read while the second database held its waiters: what it
// answered as written and with every name of lock or session state kept to its own database, and
// whether the two were the same (it kept to its own) in each of tries.
type judged struct {
	read          proofRead
	keeps         bool
	as, ownOnly   string
	firstMismatch int
}

// tries is how many times the proof reads each read and its restriction, one after the other, while
// the second database holds its waiters: a read keeps to its own database only when the two agree
// in every try, and reads another's when they differ in one.
const tries = 3

// judge reads each of reads and its restriction on conn tries times and answers what it found. A
// read the restriction cannot stand for fails t, by name: the proof cannot judge it.
func judge(t *testing.T, conn *pgx.Conn, reads []proofRead) []judged {
	t.Helper()
	ctx := t.Context()
	var found []judged
	for _, read := range reads {
		own, ok := restricted(read.sql)
		if !ok {
			t.Fatalf("UNKNOWN: %s names lock or session state the proof has no restriction for: %s", read.name, read.sql)
		}
		result := judged{read: read, keeps: true}
		for try := range tries {
			as, err := answer(ctx, conn, read.sql, read.args...)
			if err != nil {
				t.Fatalf("%s: %v", read.name, err)
			}
			ownOnly, err := answer(ctx, conn, own, read.args...)
			if err != nil {
				t.Fatalf("%s kept to its own database: %v\n%s", read.name, err, own)
			}
			if try == 0 {
				result.as, result.ownOnly = as, ownOnly
			}
			if as != ownOnly && result.keeps {
				result.keeps, result.as, result.ownOnly, result.firstMismatch = false, as, ownOnly, try+1
			}
		}
		found = append(found, result)
	}
	return found
}

// plantWaiters makes backends of database wait, until t ends: on a table's lock, on two other
// tables' lock, on an advisory lock of one key and on one of two keys (storetest.Queue), and on a
// transaction id, a row another transaction updates. Each waits behind a holder of its own.
func plantWaiters(t *testing.T, database *store.Store) {
	t.Helper()
	for _, lock := range []storetest.Lock{
		storetest.RelationLock("search_term_stats"), storetest.RelationLock("issues"),
		storetest.AdvisoryLock(42), storetest.AdvisoryLockPair(3, 7),
	} {
		storetest.Queue(t, database, lock)
	}
	ctx := t.Context()
	holder := connect(t, database)
	if _, err := holder.Exec(ctx, `begin; update lockread_proof_rows set id = id where id = 1`); err != nil {
		t.Fatalf("hold the proof's row: %v", err)
	}
	waiter := connect(t, database)
	pid := waiter.PgConn().PID()
	waiting, cancel := context.WithCancel(ctx)
	var done sync.WaitGroup
	done.Go(func() { _, _ = waiter.Exec(waiting, `begin; update lockread_proof_rows set id = id where id = 1`) })
	t.Cleanup(func() { cancel(); done.Wait() })
	deadline := fataltest.BeforeTheBinaryDeadline(t)
	for {
		var waits bool
		if err := database.Pool.QueryRow(ctx, `select exists (select from pg_catalog.pg_locks where pid = $1 and not granted and locktype = 'transactionid')`, int32(pid)).Scan(&waits); err != nil {
			t.Fatalf("read whether backend %d waits on the proof's row: %v", pid, err)
		}
		if waits {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("backend %d never waited on the proof's row before the test binary's deadline", pid)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// connect opens a connection of its own to database, closed when t ends.
func connect(t *testing.T, database *store.Store) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, database.Pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatalf("connect to the database: %v", err)
	}
	t.Cleanup(func() {
		closing, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = conn.Close(closing)
	})
	return conn
}

// readerOn is a connection of its own to database with the proof's restrictions in its session,
// and the table the row waiter waits on made in database.
func readerOn(t *testing.T, database *store.Store) *pgx.Conn {
	t.Helper()
	if _, err := database.Pool.Exec(t.Context(), `create table lockread_proof_rows (id integer primary key); insert into lockread_proof_rows values (1)`); err != nil {
		t.Fatalf("make the proof's row: %v", err)
	}
	reader := connect(t, database)
	for _, statement := range restrictions {
		if _, err := reader.Exec(t.Context(), statement); err != nil {
			t.Fatalf("make the proof's restriction: %v\n%s", err, statement)
		}
	}
	return reader
}

// Every read lockread offers keeps to the database it runs in: while a second database of the
// server holds waiters on table, advisory and transaction-id locks, and their holders, each read
// of lockread answers on the first database what it answers with every view and function of lock
// or session state in it kept to that database, in each of three tries. The own-database control:
// once the first database holds the same waiters, each read answers otherwise than it did, so each
// sees its own database's. The plants (package plants), the reads the reviews of this change planted
// against the earlier text check, are judged the same way, as if each were one of lockread's reads:
// every one that reads another database's waiters is named, and every one that keeps to its own
// passes, which is also the control that the second database's waiters are there to be seen.
func TestEveryReadOfLockreadKeepsToItsOwnDatabase(t *testing.T) {
	own := storetest.Open(t)
	other := storetest.Open(t)
	reader := readerOn(t, own)
	readerOn(t, other)

	reads := helperReads()
	for _, plant := range plants.All {
		reads = append(reads, proofRead{name: "plant " + plant.Name + " (" + plant.What + ")", sql: plant.SQL, args: plant.Args, keeps: plant.Keeps})
	}
	quiet := judge(t, reader, reads[:len(helperReads())])

	plantWaiters(t, other)
	found := judge(t, reader, reads)
	named, passed := 0, 0
	for _, result := range found {
		switch {
		case result.read.keeps && !result.keeps:
			t.Errorf("%s reads another database: in try %d it answered %q, and %q kept to its own database\n%s",
				result.read.name, result.firstMismatch, result.as, result.ownOnly, result.read.sql)
		case !result.read.keeps && result.keeps:
			t.Errorf("%s keeps to its own database, but it was planted as a read of another's: it answered %q in each try, and the same kept to its own\n%s",
				result.read.name, result.as, result.read.sql)
		case !result.read.keeps:
			named++
		default:
			passed++
		}
	}
	if named == 0 {
		t.Fatalf("UNKNOWN: no read was named as reading the second database, so its waiters may not be there to see")
	}
	t.Logf("with the second database's waiters planted: %d reads kept to their own database, %d planted reads named as reading another's", passed, named)

	plantWaiters(t, own)
	for i, after := range judge(t, reader, reads[:len(helperReads())]) {
		if !after.keeps {
			t.Errorf("%s reads another database once its own holds waiters: %q, and %q kept to its own", after.read.name, after.as, after.ownOnly)
		}
		if after.as == quiet[i].as {
			t.Errorf("%s answered %q before and after its own database held waiters: it does not see its own", after.read.name, after.as)
		}
	}
}

// The proof's restriction keeps a read to its own database without changing a read that already
// keeps to it, and refuses a read of lock or session state it has no restriction for.
func TestTheProofsRestrictionStandsForEveryNameItReplaces(t *testing.T) {
	for _, row := range []struct{ sql, want string }{
		{`select count(*) from pg_locks where not granted`, `select count(*) from pg_temp.proof_locks() where not granted`},
		{`select count(*) from "PG_LOCKS" l`, `select count(*) from pg_temp.proof_locks() l`},
		{`select count(*) from pg_catalog . pg_stat_activity a, unnest(pg_catalog.pg_blocking_pids(a.pid)) b`, `select count(*) from pg_temp.proof_activity() a, unnest(pg_temp.proof_blocking_pids(a.pid)) b`},
		{`select count(*) from pg_stat_get_backend_idset() s`, `select count(*) from pg_temp.proof_backend_idset() s`},
	} {
		if got, ok := restricted(row.sql); !ok || got != row.want {
			t.Errorf("restricted(%q) = %q, %t; want %q, true", row.sql, got, ok, row.want)
		}
	}
	if _, ok := restricted(`select count(*) from pg_lock_status() l`); ok {
		t.Errorf("restricted passed a read of pg_lock_status, which it has no restriction for")
	}
}
