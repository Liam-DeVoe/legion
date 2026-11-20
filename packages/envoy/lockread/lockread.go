// Package lockread is the one reader of lock and session state for tests: every read that test code
// makes of pg_locks, pg_stat_activity, or a function behind them or beside them, goes through the
// sources here, and each keeps its rows to the database the statement runs in. A test's database is
// one of many on its server: storetest clones one per test from one template, the broker's tests and
// the Legion daemon's share a server with them, and every clone holds the same tables under the same
// oids and takes the same advisory keys. pg_locks and pg_stat_activity list every database's locks
// and backends, so a read of either that keeps to no database counts another test's waiter, or
// names another test's holder, as its own.
//
// A read names a source here where it would name the view, with an alias of its own, and adds its
// own conditions: `select count(*) from ` + lockread.Activity + ` a where a.wait_event_type =
// 'Lock'`. No condition a read adds can bring back a row the source left out. internal/textcheck's
// lock_reads_test.go refuses a test file outside this directory that names one of these views or
// functions, and this package's own test runs each source against a second database of the server
// that holds planted waiters and sessions, and requires it to see none of them and to see the same
// waiters in its own database.
//
// Each source reads pg_catalog's view or function by its qualified name, so a search_path cannot
// put another object of the same name in its place.
//
// Postgres reads pg_stat_activity once per transaction and serves that copy until the transaction
// ends, while pg_locks is read anew each time. So the backends of the database, which Locks,
// BlockingPIDs and BackendWAL keep to, are those pg_locks shows holding or waiting for a lock in it,
// read anew, beside those pg_stat_activity shows: a poll inside a transaction that read
// pg_stat_activity before a waiter connected still counts that waiter, which waits on a table of
// the database or holds a lock on one. Activity is pg_stat_activity's rows, and inside one
// transaction it shows its first copy, as the view itself does.
package lockread

// ownDatabase is the oid of the database the statement runs in.
const ownDatabase = `(select d.oid from pg_catalog.pg_database d where d.datname = pg_catalog.current_database())`

// ownBackends is the process ids of the backends of the database the statement runs in: each that
// holds or waits for a lock in it, and each pg_stat_activity lists in it.
const ownBackends = `(select m.pid from pg_catalog.pg_locks m where m.database = ` + ownDatabase +
	` union select b.pid from pg_catalog.pg_stat_activity b where b.datid = ` + ownDatabase + `)`

// Activity is pg_stat_activity's rows of the backends of the database the statement runs in, every
// column of the view. A background process that belongs to no database (the checkpointer, the WAL
// writer) is not one of them.
const Activity = `(select a.* from pg_catalog.pg_stat_activity a where a.datid = ` + ownDatabase + `)`

// Locks is pg_locks' rows of the database the statement runs in, every column of the view: a lock
// on one of its objects or advisory keys (its database column), and every lock a backend of it
// holds or waits for, a transaction id's and a virtual transaction id's among them, which name no
// database, and a shared catalog's, which names database 0. A prepared transaction's locks, which
// no backend holds, count by their database.
const Locks = `(select l.* from pg_catalog.pg_locks l where l.database = ` + ownDatabase +
	` or l.pid in ` + ownBackends + `)`

// BlockingPIDs is pg_blocking_pids of the backend whose process id the SQL expression pid gives,
// kept to the backends of the database the statement runs in: an int[] of the backends that block
// it, in pg_blocking_pids' order. pid is a column or a parameter of the caller's statement, such as
// "a.pid" or "$1".
func BlockingPIDs(pid string) string {
	return `array(select p from unnest(pg_catalog.pg_blocking_pids(` + pid + `)) p where p in ` + ownBackends + `)`
}

// BackendWAL is pg_stat_get_backend_wal's row of the backend whose process id the SQL expression
// pid gives, every column of it, or no row when that backend is not one of the database the
// statement runs in. pid is a column or a parameter of the caller's statement, such as
// "pg_backend_pid()".
func BackendWAL(pid string) string {
	return `(select w.* from pg_catalog.pg_stat_get_backend_wal(` + pid + `) w where (` + pid + `) in ` + ownBackends + `)`
}

// Slots is the backends of the database the statement runs in, each by its process id (pid) and the
// number of its slot on the server (slot), as pg_stat_get_backend_idset numbers the slots. A slot's
// number is the backend's while it lives, whatever backends come and go in the slots before it, and
// pg_stat_activity lists every database's backends in the order of their slots, so two backends of
// two databases are listed in the order of their slot numbers, each read in its own database.
const Slots = `(select pg_catalog.pg_stat_get_backend_pid(s) as pid, s as slot from pg_catalog.pg_stat_get_backend_idset() s` +
	` where pg_catalog.pg_stat_get_backend_dbid(s) = ` + ownDatabase + `)`
