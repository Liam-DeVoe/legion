package storetest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// pg_locks holds every lock of every database on the server, while a lock is taken within one
// database. A test's database is one of many on its server: Open clones one per test from one
// template, and the packages of a run, and runs beside it, test against the same server at once.
// So the same table has the same oid in every clone, every test that files an issue in a project
// of the same key files the same issue key, and the same key names the same advisory lock in
// every database. A read of pg_locks that does not keep to its own database counts another
// test's waiter, or names another test's holder, as its own: the API's upload lock probe once
// named another test's spec settlement as the holder of its issue's owner lock, and failed.
//
// Waiting and Holders are the reads that keep to the database they run in, of a Lock that names
// what they look for. Hold, HoldOn and Queue plant that same Lock in a database a test names,
// another clone of its own (Open) among them, and ConnectInListingOrder opens the connections a
// test needs when the order the server lists holders in matters, so a test shows its read counts
// its own database's lock and not another's.

// thisDatabase keeps a read of pg_locks, aliased l, to the rows of the database it runs in.
const thisDatabase = `l.database = (select oid from pg_database where datname = current_database())`

// Lock is one lock as pg_locks shows it: the predicate over pg_locks, aliased l, that names its
// rows, and how a transaction takes it, so the lock a read looks for and the lock a test plants
// cannot drift apart.
type Lock struct {
	name  string
	match string
	args  []any
	take  func(ctx context.Context, tx pgx.Tx) error
}

func (l Lock) String() string { return l.name }

// RelationLock is a table's lock, by its name: every row pg_locks holds on the table, a lock of
// the relation or of one of its rows, as a test that waits on the table counts them. A
// transaction takes it in access exclusive mode, which every other mode waits behind.
func RelationLock(table string) Lock {
	return Lock{
		name:  "table " + table,
		match: `l.relation = $1::regclass`,
		args:  []any{table},
		take: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "lock table "+pgx.Identifier{table}.Sanitize()+" in access exclusive mode")
			return err
		},
	}
}

// AdvisoryLock is the advisory lock of one bigint key, as pg_advisory_xact_lock(bigint) takes it:
// pg_locks shows the key's high half as classid and its low half as objid.
func AdvisoryLock(key int64) Lock {
	return Lock{
		name:  fmt.Sprintf("advisory lock %d", key),
		match: `l.locktype = 'advisory' and ((l.classid::bigint << 32) | l.objid::bigint) = $1`,
		args:  []any{key},
		take: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1::bigint)`, key)
			return err
		},
	}
}

// AdvisoryLockPair is the advisory lock of two integer keys, as pg_advisory_xact_lock(integer,
// integer) takes it: pg_locks shows the first as classid and the second as objid, each as the
// unsigned 32-bit value an oid carries, with objsubid 2.
func AdvisoryLockPair(key1, key2 int32) Lock {
	return Lock{
		name: fmt.Sprintf("advisory lock (%d, %d)", key1, key2),
		match: `l.locktype = 'advisory' and l.objsubid = 2
			and l.classid = ($1::bigint & 4294967295)::oid and l.objid = ($2::bigint & 4294967295)::oid`,
		args: []any{int64(key1), int64(key2)},
		take: func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1::integer, $2::integer)`, key1, key2)
			return err
		},
	}
}

// OwnerLock is owner's lock (store.LockOwner): the two-key advisory lock whose first key is
// store.OwnerLockNamespace and whose second is the hashtext of the owner's lock key
// (store.Owner.LockKey). Both come from store rather than being spelled again here, so a change
// to either reaches this read instead of leaving it looking for a lock nothing takes any more. A
// transaction takes it through store.LockOwner, as every writer of the owner does.
func OwnerLock(owner store.Owner) Lock {
	return Lock{
		name: fmt.Sprintf("the owner lock of %s %s", owner.Kind, owner.Key),
		match: `l.locktype = 'advisory' and l.objsubid = 2
			and l.classid = ($1::bigint & 4294967295)::oid and l.objid = (hashtext($2)::bigint & 4294967295)::oid`,
		args: []any{int64(store.OwnerLockNamespace), owner.LockKey()},
		take: func(ctx context.Context, tx pgx.Tx) error { return store.LockOwner(ctx, tx, owner) },
	}
}

// Waiting counts the backends of the database pool reads that wait for lock: its ungranted rows
// in pg_locks, of that database alone.
func Waiting(ctx context.Context, pool *store.Pool, lock Lock) (int, error) {
	var waiting int
	if err := pool.QueryRow(ctx, `select count(*) from pg_locks l where not l.granted and `+thisDatabase+` and `+lock.match, lock.args...).Scan(&waiting); err != nil {
		return 0, fmt.Errorf("count the waiters on %s: %w", lock, err)
	}
	return waiting, nil
}

// LockHolder is a backend holding a lock, with what pg_stat_activity shows of it: its state, the
// start of its transaction (nil for none) and its last statement.
type LockHolder struct {
	PID       int32
	State     string
	XactStart *time.Time
	Query     string
}

func (h LockHolder) String() string {
	start := "null"
	if h.XactStart != nil {
		start = h.XactStart.Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("backend %d, state %q, xact_start %s, query %q", h.PID, h.State, start, h.Query)
}

// Holders names every backend of the database pool reads that holds lock, granted, in pid order,
// the reading backend left out. Every one, not the first the server lists: that order is the
// order of the server's backend slots, which no test decides, and a lock held shared has several
// holders. A holder that ended between a caller's refusal and this read is simply not named.
func Holders(ctx context.Context, pool *store.Pool, lock Lock) ([]LockHolder, error) {
	rows, err := pool.Query(ctx, `
		select a.pid, a.state, a.xact_start, a.query
		from pg_locks l join pg_stat_activity a on a.pid = l.pid
		where l.granted and a.pid <> pg_backend_pid() and `+thisDatabase+` and `+lock.match+`
		order by a.pid
	`, lock.args...)
	if err != nil {
		return nil, fmt.Errorf("read the holders of %s: %w", lock, err)
	}
	holders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LockHolder, error) {
		var holder LockHolder
		var state, query *string
		if err := row.Scan(&holder.PID, &state, &holder.XactStart, &query); err != nil {
			return holder, err
		}
		if state != nil {
			holder.State = *state
		}
		if query != nil {
			holder.Query = *query
		}
		return holder, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the holders of %s: %w", lock, err)
	}
	return holders, nil
}

// listingTries bounds how many rounds ConnectInListingOrder opens connections in before it gives
// up.
const listingTries = 64

// Hold takes lock in a transaction on a connection of its own to database and then runs then
// with args in it (HoldOn). It answers the holder's backend.
func Hold(t testing.TB, database *store.Store, lock Lock, then string, args ...any) uint32 {
	t.Helper()
	return HoldOn(t, connect(t, database), lock, then, args...)
}

// HoldOn takes lock in a transaction on conn and then runs then with args in it, so
// pg_stat_activity shows then as the holder's last statement; an empty then runs nothing more.
// then's rows are read and dropped, and it may answer none. The transaction stays open, and the
// lock held, until conn closes, which a connection ConnectInListingOrder opened does when t ends.
// It answers the holder's backend.
func HoldOn(t testing.TB, conn *pgx.Conn, lock Lock, then string, args ...any) uint32 {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the transaction that holds %s: %v", lock, err)
	}
	if err := lock.take(ctx, tx); err != nil {
		t.Fatalf("take %s: %v", lock, err)
	}
	if then != "" {
		rows, err := tx.Query(ctx, then, args...)
		if err != nil {
			t.Fatalf("run %q under %s: %v", then, lock, err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("run %q under %s: %v", then, lock, err)
		}
	}
	return conn.PgConn().PID()
}

// ConnectInListingOrder opens a connection to first and one to second, each closed when t ends,
// such that pg_stat_activity lists first's backend before second's: so a read that names the
// first holder the server lists, as a hash join with pg_stat_activity on its outer side does,
// meets a lock held on the first before one held on the second. That order is the order of the
// server's backend slots, and a new connection takes whichever slot the server hands out next,
// which no test decides and which can run high or low for a while. So it opens one connection to
// each in turn, the two in alternating order, keeps them all open while it looks (a slot closed
// would be handed out again), and answers the earliest-listed connection to first and the
// latest-listed one to second once the first is listed before the second, closing the others. It
// fails t after listingTries rounds.
func ConnectInListingOrder(t testing.TB, first, second *store.Store) (*pgx.Conn, *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	var firsts, seconds []*pgx.Conn
	for round := range listingTries {
		if round%2 == 0 {
			firsts = append(firsts, connect(t, first))
			seconds = append(seconds, connect(t, second))
		} else {
			seconds = append(seconds, connect(t, second))
			firsts = append(firsts, connect(t, first))
		}
		positions, err := listingPositions(ctx, first.Pool, append(slices.Clone(firsts), seconds...))
		if err != nil {
			t.Fatalf("read where the server lists the connections' backends: %v", err)
		}
		early := slices.MinFunc(firsts, func(a, b *pgx.Conn) int { return cmp.Compare(positions[a], positions[b]) })
		late := slices.MaxFunc(seconds, func(a, b *pgx.Conn) int { return cmp.Compare(positions[a], positions[b]) })
		if positions[early] == 0 || positions[late] == 0 || positions[early] >= positions[late] {
			continue
		}
		for _, conn := range append(firsts, seconds...) {
			if conn != early && conn != late {
				_ = conn.Close(ctx)
			}
		}
		return early, late
	}
	t.Fatalf("no connection to the first database of %d opened is listed before one of %d to the second", len(firsts), len(seconds))
	return nil, nil
}

// Queue has a backend of database wait for lock: one connection takes the lock (Hold) and a
// second asks for it, both until t ends. It answers once the second waits, read off that
// backend's own row in pg_locks, so a read that counts waiters wrongly cannot end the wait
// early, and it answers the waiting backend.
func Queue(t testing.TB, database *store.Store, lock Lock) uint32 {
	t.Helper()
	Hold(t, database, lock, "")
	waiter := connect(t, database)
	pid := waiter.PgConn().PID()
	ctx, cancel := context.WithCancel(context.Background())
	asked := make(chan struct{})
	go func() {
		defer close(asked)
		tx, err := waiter.Begin(ctx)
		if err != nil {
			return
		}
		// It returns once the holder's connection closes, or cancel stops the wait.
		_ = lock.take(ctx, tx)
	}()
	// Registered after the waiter's connection, so it runs first: the wait ends before the
	// connection closes under it.
	t.Cleanup(func() {
		cancel()
		<-asked
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := database.Pool.QueryRow(context.Background(), `select exists (select 1 from pg_locks where pid = $1 and not granted)`, int32(pid)).Scan(&waiting); err != nil {
			t.Fatalf("read whether backend %d waits for %s: %v", pid, lock, err)
		}
		if waiting {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend %d never waited for %s", pid, lock)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// connect opens a connection of its own to database, closed when t ends, which rolls back any
// transaction left open on it.
func connect(t testing.TB, database *store.Store) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(context.Background(), database.Pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatalf("connect to the database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// listingPositions is where pg_stat_activity lists each of conns' backends: the order of the
// server's backend slots, which pg_stat_get_activity reads them in, counted from 1. A backend that
// is gone is not in the answer, so its position reads 0.
func listingPositions(ctx context.Context, pool *store.Pool, conns []*pgx.Conn) (map[*pgx.Conn]int64, error) {
	pids := make([]int32, len(conns))
	for i, conn := range conns {
		pids[i] = int32(conn.PgConn().PID())
	}
	rows, err := pool.Query(ctx, `
		with listed as (select pid, row_number() over () as position from pg_stat_get_activity(null))
		select pid, position from listed where pid = any($1)
	`, pids)
	if err != nil {
		return nil, err
	}
	byPID := map[int32]int64{}
	var pid int32
	var position int64
	if _, err := pgx.ForEachRow(rows, []any{&pid, &position}, func() error {
		byPID[pid] = position
		return nil
	}); err != nil {
		return nil, err
	}
	positions := make(map[*pgx.Conn]int64, len(conns))
	for i, conn := range conns {
		positions[conn] = byPID[pids[i]]
	}
	return positions, nil
}
