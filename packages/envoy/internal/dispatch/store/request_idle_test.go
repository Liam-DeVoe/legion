package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// idleBoundReader is what readIdleBound asks: the request pool, a wrapped pool and a separate
// pool all answer a one-row query the same way.
type idleBoundReader interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readIdleBound is idle_in_transaction_session_timeout as the connection q hands out carries it.
// pg_settings reports the value in its own unit, which the unit column names, so the reading is a
// number rather than Postgres's formatted "70s".
func readIdleBound(t *testing.T, ctx context.Context, q idleBoundReader) time.Duration {
	t.Helper()
	var milliseconds int64
	var unit string
	if err := q.QueryRow(ctx, `
		select setting::bigint, unit from pg_settings where name = $1
	`, idleInTransactionParam).Scan(&milliseconds, &unit); err != nil {
		t.Fatalf("read %s: %v", idleInTransactionParam, err)
	}
	if unit != "ms" {
		t.Fatalf("%s is reported in %q, want milliseconds", idleInTransactionParam, unit)
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// The connections of every pool whose transactions take the locks a request's write waits on, and
// none of which sits idle longer by design, carry the bound this package computes - one cold read,
// the rollback that ends the transaction after it, and a margin (RequestIdleInTransactionTimeout):
// the request pool's, the background loops', the outbox's and the rooms' appends'. The pools that
// take nothing a request's write waits on carry none, wrapped (Live, TextTrees) and separate
// (Rooms, Leases) alike: a room load, a live reader, a lease and the display tree fill may sit in
// a transaction for as long as their own work takes. The settlements and compaction pools carry
// none either; their writes bound their own transactions (docs' rows hold those).
func TestTheIdleBoundIsOnTheRequestBackgroundOutboxAndRoomAppendsConnections(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()

	background, err := database.Pool.Background()
	if err != nil {
		t.Fatalf("open the background pool: %v", err)
	}
	outbox, err := database.Pool.Outbox()
	if err != nil {
		t.Fatalf("open the outbox pool: %v", err)
	}
	roomAppends, err := database.Pool.RoomAppends()
	if err != nil {
		t.Fatalf("open the room-appends pool: %v", err)
	}
	rooms, err := database.Pool.Rooms()
	if err != nil {
		t.Fatalf("open the rooms pool: %v", err)
	}
	live, err := database.Pool.Live()
	if err != nil {
		t.Fatalf("open the live pool: %v", err)
	}
	leaseCallers, err := database.Pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	leases, err := leaseCallers.For("the idle bound's reading")
	if err != nil {
		t.Fatalf("reserve a connection of the lease pool: %v", err)
	}
	textTrees, err := database.Pool.TextTrees()
	if err != nil {
		t.Fatalf("open the text-trees pool: %v", err)
	}

	for _, bounded := range []struct {
		pool string
		q    idleBoundReader
	}{
		{RequestsPool, database.Pool},
		{BackgroundPool, background},
		{OutboxPool, outbox},
		{RoomAppendsPool, roomAppends},
	} {
		if got := readIdleBound(t, ctx, bounded.q); got != RequestIdleInTransactionTimeout {
			t.Errorf("the %s pool's connection carries %s = %v, want the computed %v (%v cold read + %v rollback + margin)",
				bounded.pool, idleInTransactionParam, got, RequestIdleInTransactionTimeout, ColdReadBound, RollbackTimeout)
		}
	}
	for _, unbounded := range []struct {
		pool string
		q    idleBoundReader
	}{
		{RoomsPool, rooms},
		{LivePool, live},
		{LeasesPool, leases},
		{TextTreesPool, textTrees},
	} {
		if got := readIdleBound(t, ctx, unbounded.q); got != 0 {
			t.Errorf("the %s pool's connection carries %s = %v, want none", unbounded.pool, idleInTransactionParam, got)
		}
	}
}

// Postgres ends a request transaction left idle past that bound, so an orphaned one - a
// transaction whose pgx connection was abandoned, whose close reached Postgres late or never -
// cannot hold the rows it locked and the event horizon until Postgres notices the socket is gone.
// A rooms connection's transaction idle just as long keeps its transaction.
//
// The bound is shortened here (shortenIdleBound) rather than waited out, so the test takes
// seconds and not the seventy production runs.
func TestPostgresEndsARequestTransactionLeftIdlePastItsBound(t *testing.T) {
	const bound = 300 * time.Millisecond
	shortenIdleBound(t, bound)
	database := openTestStore(t)
	ctx := context.Background()

	request, err := database.Pool.Begin(WithTransactionTracking(ctx))
	if err != nil {
		t.Fatalf("begin the request transaction: %v", err)
	}
	defer request.Rollback(ctx)
	requestPID := backendPID(t, ctx, request)
	load, loadPID := beginRoomsTransaction(t, ctx, database)
	defer load.Rollback(ctx)

	// Both sit idle in their transactions well past the bound.
	time.Sleep(10 * bound)

	requireEndedByPostgres(t, ctx, database, "request", request, requestPID, bound)
	requireKept(t, ctx, database, "rooms", load, loadPID)
}

// Postgres ends a background-pool transaction left idle past that same bound. A background writer
// takes the issue rows and the document locks a request's write waits on, and no request's write
// sets a lock_timeout, so an orphaned background transaction would hold them until Postgres
// noticed its socket was gone. A rooms connection's transaction idle just as long keeps its
// transaction.
func TestPostgresEndsABackgroundTransactionLeftIdlePastItsBound(t *testing.T) {
	const bound = 300 * time.Millisecond
	shortenIdleBound(t, bound)
	database := openTestStore(t)
	ctx := context.Background()

	background, err := database.Pool.Background()
	if err != nil {
		t.Fatalf("open the background pool: %v", err)
	}
	loop, err := background.Begin(WithTransactionTracking(ctx))
	if err != nil {
		t.Fatalf("begin the background transaction: %v", err)
	}
	defer loop.Rollback(ctx)
	loopPID := backendPID(t, ctx, loop)
	load, loadPID := beginRoomsTransaction(t, ctx, database)
	defer load.Rollback(ctx)

	time.Sleep(10 * bound)

	requireEndedByPostgres(t, ctx, database, "background", loop, loopPID, bound)
	requireKept(t, ctx, database, "rooms", load, loadPID)
}

// shortenIdleBound has the pools opened from here on carry bound in place of production's
// seventy seconds (requestIdleInTransaction), until t ends. A pool takes the bound as it opens
// (configurePool), so it is shortened before the store opens.
func shortenIdleBound(t *testing.T, bound time.Duration) {
	t.Helper()
	production := requestIdleInTransaction
	requestIdleInTransaction = bound
	t.Cleanup(func() { requestIdleInTransaction = production })
}

// beginRoomsTransaction opens a transaction on a connection of the rooms pool, which carries no
// idle bound, and answers it with its backend pid.
func beginRoomsTransaction(t *testing.T, ctx context.Context, database *Store) (pgx.Tx, int32) {
	t.Helper()
	rooms, err := database.Pool.Rooms()
	if err != nil {
		t.Fatalf("open the rooms pool: %v", err)
	}
	load, err := rooms.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the rooms transaction: %v", err)
	}
	return load, backendPID(t, ctx, load)
}

// requireEndedByPostgres requires the pool's transaction tx, on backend pid, left idle past bound,
// to have been ended by Postgres: its next statement refused with SQLSTATE 25P03 and its session
// gone from pg_stat_activity.
func requireEndedByPostgres(t *testing.T, ctx context.Context, database *Store, pool string, tx pgx.Tx, pid int32, bound time.Duration) {
	t.Helper()
	_, err := tx.Exec(ctx, "select 1")
	if err == nil {
		t.Errorf("a %s transaction idle %v past its %v bound still runs statements, want it ended by Postgres",
			pool, 9*bound, bound)
	} else if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code != "25P03" {
		t.Errorf("a %s transaction idle past its bound answered SQLSTATE %s (%s), want 25P03", pool, pgErr.Code, pgErr.Message)
	}
	if alive(t, ctx, database.Pool, pid) {
		t.Errorf("the %s connection (pid %d) is still in pg_stat_activity, want its session ended", pool, pid)
	}
}

// requireKept requires the pool's transaction tx, on backend pid, to be open still.
func requireKept(t *testing.T, ctx context.Context, database *Store, pool string, tx pgx.Tx, pid int32) {
	t.Helper()
	if _, err := tx.Exec(ctx, "select 1"); err != nil {
		t.Errorf("a %s transaction idle just as long: %v, want it still open", pool, err)
	}
	if !alive(t, ctx, database.Pool, pid) {
		t.Errorf("the %s connection (pid %d) is gone from pg_stat_activity, want it untouched", pool, pid)
	}
}

// backendPID is the Postgres backend tx runs on.
func backendPID(t *testing.T, ctx context.Context, tx pgx.Tx) int32 {
	t.Helper()
	var pid int32
	if err := tx.QueryRow(ctx, "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("read the transaction's backend pid: %v", err)
	}
	return pid
}

// alive reports whether pid still holds a session, read on a connection of its own.
func alive(t *testing.T, ctx context.Context, q idleBoundReader, pid int32) bool {
	t.Helper()
	var open bool
	if err := q.QueryRow(ctx, "select exists(select 1 from pg_stat_activity where pid = $1)", pid).Scan(&open); err != nil {
		t.Fatalf("read pg_stat_activity for pid %d: %v", pid, err)
	}
	return open
}
