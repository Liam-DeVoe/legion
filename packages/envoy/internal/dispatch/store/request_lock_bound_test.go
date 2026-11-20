package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/pgmigrate"
)

// lockBoundReader is what readLockBound asks: a pool, a transaction and a connection all answer a
// one-row query the same way.
type lockBoundReader interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readLockBound is lock_timeout as the session or transaction q runs on carries it; zero is none.
func readLockBound(t *testing.T, ctx context.Context, q lockBoundReader) time.Duration {
	t.Helper()
	var millis int64
	if err := q.QueryRow(ctx, `select setting::bigint from pg_settings where name = 'lock_timeout'`).Scan(&millis); err != nil {
		t.Fatalf("read lock_timeout: %v", err)
	}
	return time.Duration(millis) * time.Millisecond
}

// Every lock wait of the request pool is bounded at the write lock bound (lockWait) unless a
// bounded wait of its own sets another: the pool's connections carry it, so a statement outside a
// transaction waits under it (configurePool), and each transaction the pool opens sets it for
// itself in its BEGIN's message, at the test's bound where its context carries one (Pool.begin).
// No other pool's connections carry it. A bounded wait inside a request transaction gives the
// transaction's own value back, whether its lock was granted or ran out (boundedWaitKeep,
// boundedWaitRestore, leaveBoundedWait). The migration runner's DISCARD ALL, run on a request-pool
// connection before it goes back to the pool (pgmigrate.ResetSession), leaves the bound in place,
// since a startup parameter is the session's reset value. A session that does not carry the bound
// its pool asks for - one a pooler opened without the parameter - is refused at its connect
// (requireLockBound). The one opt-out sets the transaction's own bound to the time its deadline
// leaves, and leaves the write lock bound where its context has no deadline
// (WaitForLocksUntilDeadline).
func TestTheRequestPoolWaitsForALockNoLongerThanTheWriteLockBound(t *testing.T) {
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
	rooms, err := database.Pool.Rooms()
	if err != nil {
		t.Fatalf("open the rooms pool: %v", err)
	}
	if got := readLockBound(t, ctx, database.Pool); got != lockWait {
		t.Errorf("the requests pool's connection carries lock_timeout = %v, want the write lock bound %v", got, lockWait)
	}
	for _, unbounded := range []struct {
		pool string
		q    lockBoundReader
	}{
		{BackgroundPool, background},
		{OutboxPool, outbox},
		{RoomsPool, rooms},
	} {
		if got := readLockBound(t, ctx, unbounded.q); got != 0 {
			t.Errorf("the %s pool's connection carries lock_timeout = %v, want none", unbounded.pool, got)
		}
	}

	plain, err := database.Pool.Begin(WithTransactionTracking(ctx))
	if err != nil {
		t.Fatalf("begin a request transaction: %v", err)
	}
	if got := readLockBound(t, ctx, plain); got != lockWait {
		t.Errorf("a request transaction carries lock_timeout = %v, want the write lock bound %v", got, lockWait)
	}
	if err := WaitForLocksUntilDeadline(ctx, plain); err != nil {
		t.Fatalf("opt a transaction with no deadline out: %v", err)
	}
	if got := readLockBound(t, ctx, plain); got != lockWait {
		t.Errorf("after the opt-out with no deadline the transaction carries lock_timeout = %v, want the write lock bound %v", got, lockWait)
	}
	const deadline = 40 * time.Second
	optOut, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	if err := WaitForLocksUntilDeadline(optOut, plain); err != nil {
		t.Fatalf("opt a transaction out until its deadline: %v", err)
	}
	if got := readLockBound(t, ctx, plain); got <= lockWait || got > deadline {
		t.Errorf("after the opt-out the transaction carries lock_timeout = %v, want the %v its deadline leaves", got, deadline)
	}
	if err := plain.Rollback(ctx); err != nil {
		t.Fatalf("roll the request transaction back: %v", err)
	}

	const transactionBound, waitBound = 1500 * time.Millisecond, 200 * time.Millisecond
	const grantedKey, heldKey = 592620, 592621
	tested := WithLockWaitForTest(WithTransactionTracking(ctx), transactionBound)
	tx, err := database.Pool.BeginTx(tested, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin a request transaction under the test's bound: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var isolation string
	if err := tx.QueryRow(ctx, `select current_setting('transaction_isolation')`).Scan(&isolation); err != nil || isolation != "repeatable read" {
		t.Fatalf("the request transaction's isolation = %q (%v), want the repeatable read it was begun with", isolation, err)
	}
	if got := readLockBound(t, ctx, tx); got != transactionBound {
		t.Fatalf("a request transaction begun under the test's bound carries lock_timeout = %v, want %v", got, transactionBound)
	}
	waiting := WithLockWaitForTest(tested, waitBound)
	if err := LockWithinBound(waiting, tx, `select pg_advisory_xact_lock($1)`, int64(grantedKey)); err != nil {
		t.Fatalf("take a free lock in a bounded wait: %v", err)
	}
	if got := readLockBound(t, ctx, tx); got != transactionBound {
		t.Errorf("after a bounded wait whose lock was granted the transaction carries lock_timeout = %v, want its own %v back", got, transactionBound)
	}
	holder, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holder: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `select pg_advisory_xact_lock($1)`, int64(heldKey)); err != nil {
		t.Fatalf("hold the lock: %v", err)
	}
	started := time.Now()
	err = LockWithinBound(waiting, tx, `select pg_advisory_xact_lock($1)`, int64(heldKey))
	if !errors.Is(err, ErrPoolBusy) {
		t.Fatalf("a bounded wait behind a held lock = %v after %v, want ErrPoolBusy at its bound of %v", err, time.Since(started), waitBound)
	}
	if waited := time.Since(started); waited < waitBound || waited >= transactionBound {
		t.Errorf("the bounded wait was refused after %v, want its own bound of %v and not the transaction's %v", waited, waitBound, transactionBound)
	}
	if got := readLockBound(t, ctx, tx); got != transactionBound {
		t.Errorf("after a bounded wait that ran out the transaction carries lock_timeout = %v, want its own %v back", got, transactionBound)
	}

	reset, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a request connection: %v", err)
	}
	if err := pgmigrate.ResetSession(ctx, reset.Conn()); err != nil {
		reset.Release()
		t.Fatalf("reset the connection's session as the migration runner does: %v", err)
	}
	if got := readLockBound(t, ctx, reset); got != lockWait {
		t.Errorf("after DISCARD ALL the request connection carries lock_timeout = %v, want the write lock bound %v", got, lockWait)
	}
	reset.Release()

	checked, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a request connection: %v", err)
	}
	defer checked.Release()
	if err := requireLockBound(ctx, checked.Conn()); err != nil {
		t.Fatalf("a request connection carrying its bound was refused: %v", err)
	}
	for _, setting := range []string{"0", "20s"} {
		if _, err := checked.Exec(ctx, `select set_config('lock_timeout', $1, false)`, setting); err != nil {
			t.Fatalf("set the session's lock_timeout to %s: %v", setting, err)
		}
		if err := requireLockBound(ctx, checked.Conn()); err == nil {
			t.Errorf("a request connection whose session carries lock_timeout = %s was not refused", setting)
		}
	}
	if _, err := checked.Exec(ctx, `reset lock_timeout`); err != nil {
		t.Fatalf("reset the session's lock_timeout: %v", err)
	}
	if err := requireLockBound(ctx, checked.Conn()); err != nil {
		t.Errorf("a request connection whose lock_timeout was reset to its startup value was refused: %v", err)
	}
}

// A lock_timeout set on the database role - what cmd/dispatch/README.md has a deployment behind a
// pooler that drops startup parameters set for the request pool's sake - reaches a session that
// asks for none, and every pool connects as the one login the connection string names. So every
// pool but the request pool asks for no bound (lockTimeoutNone), a startup parameter that outranks
// the role's setting, and its connections wait for a lock as they did before the request pool had
// a bound; the request pool's carry the write lock bound and are accepted (requireLockBound). The
// setting is the role's in this test's own database alone, so no other test's session meets it.
func TestARoleLockTimeoutReachesNoPoolButTheRequestPool(t *testing.T) {
	ctx := context.Background()
	own := openEmptyTestStore(t)
	const roleSetting = "10s"
	setRoleLockTimeout(t, ctx, own, roleSetting)
	connString := own.Pool.Config().ConnString()
	plain, err := pgx.Connect(ctx, connString)
	if err != nil {
		t.Fatalf("connect with no startup parameter of the store's: %v", err)
	}
	defer func() { _ = plain.Close(ctx) }()
	if got := readLockTimeoutSetting(t, ctx, plain); got != roleSetting {
		t.Fatalf("a session that asks for no lock_timeout carries %q, want the role's %q", got, roleSetting)
	}

	database, err := Open(ctx, connString)
	if err != nil {
		t.Fatalf("open the store under the role's setting: %v", err)
	}
	t.Cleanup(database.Pool.Close)
	if got, want := readLockTimeoutSetting(t, ctx, database.Pool), "10s"; got != want {
		t.Errorf("the %s pool's connection carries lock_timeout = %q, want the write lock bound %q", RequestsPool, got, want)
	}
	pools := []struct {
		name string
		open func() (lockBoundReader, error)
	}{
		{SettlementsPool, func() (lockBoundReader, error) { return database.Pool.Settlements() }},
		{BackgroundPool, func() (lockBoundReader, error) { return database.Pool.Background() }},
		{CompactionPool, func() (lockBoundReader, error) { return database.Pool.Compaction() }},
		{OutboxPool, func() (lockBoundReader, error) { return database.Pool.Outbox() }},
		{RoomsPool, func() (lockBoundReader, error) { return database.Pool.Rooms() }},
		{RoomAppendsPool, func() (lockBoundReader, error) { return database.Pool.RoomAppends() }},
		{LivePool, func() (lockBoundReader, error) { return database.Pool.Live() }},
		{HorizonPool, func() (lockBoundReader, error) { return database.Pool.Horizon() }},
		{TextTreesPool, func() (lockBoundReader, error) { return database.Pool.TextTrees() }},
		{HealthPool, func() (lockBoundReader, error) { return database.Pool.healthPool() }},
		{RegistryPool, func() (lockBoundReader, error) { return database.Pool.OpenRegistry(ctx) }},
		{LeasesPool, func() (lockBoundReader, error) {
			leases, err := database.Pool.Leases()
			if err != nil {
				return nil, err
			}
			return leases.For("the role's lock_timeout reading")
		}},
		{ListenPool, func() (lockBoundReader, error) {
			conn, err := database.Pool.Listener(ctx)
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { _ = conn.Close(ctx) })
			return conn, nil
		}},
	}
	for _, pool := range pools {
		q, err := pool.open()
		if err != nil {
			t.Fatalf("open the %s pool: %v", pool.name, err)
		}
		if got := readLockTimeoutSetting(t, ctx, q); got != lockTimeoutNone {
			t.Errorf("the %s pool's connection carries lock_timeout = %q, want %q, no bound, over the role's %q", pool.name, got, lockTimeoutNone, roleSetting)
		}
	}
}

// The boot's probe of the database (HealthyWithin, which cmd/dispatch's joinRegistry calls with
// its thirty-second bootProbeTimeout) waits for a lock on schema_migrations under that bound
// alone. Behind a pooler that drops startup parameters, the database role's lock_timeout - which
// cmd/dispatch/README.md has such a deployment set for the request pool's sake - reaches the
// health pool's session, and a probe it refused would end the boot. The probe's read-only
// transaction sets no bound for itself (readOnlyWaitingForLocksBegin), so it waits past the
// role's setting until the table is free, and answers the version then. The health pool opens
// without its lock_timeout parameter here (DropLockTimeoutParameterForTest), as such a pooler
// opens it. The version table is one this test makes, since the read needs nothing else, and the
// role's setting is the one of this test's own database alone.
func TestTheBootProbeWaitsForTheVersionTablePastARoleLockTimeout(t *testing.T) {
	ctx := context.Background()
	own := openEmptyTestStore(t)
	const roleSetting, held, bootBound, stored = "1s", 3 * time.Second, 30 * time.Second, 20261120000000
	if _, err := own.Pool.Exec(ctx, `create table schema_migrations (version bigint primary key)`); err != nil {
		t.Fatalf("create the version table: %v", err)
	}
	if _, err := own.Pool.Exec(ctx, `insert into schema_migrations (version) values ($1)`, stored); err != nil {
		t.Fatalf("record a version: %v", err)
	}
	setRoleLockTimeout(t, ctx, own, roleSetting)
	own.Pool.DropLockTimeoutParameterForTest(HealthPool)

	holder := connectForTest(t, ctx, own)
	gate, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("open the transaction holding the version table: %v", err)
	}
	if _, err := gate.Exec(ctx, `lock table schema_migrations in access exclusive mode`); err != nil {
		t.Fatalf("hold the version table: %v", err)
	}

	type answer struct {
		version int
		err     error
	}
	answered := make(chan answer, 1)
	go func() {
		version, err := own.Pool.HealthyWithin(ctx, bootBound)
		answered <- answer{version, err}
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var queued bool
		if err := gate.QueryRow(ctx, `select exists (select from pg_locks
			where not granted and pg_backend_pid() = any(pg_blocking_pids(pid)))`).Scan(&queued); err != nil {
			t.Fatalf("inspect the locks queued behind the version table: %v", err)
		}
		if queued {
			break
		}
		select {
		case got := <-answered:
			t.Fatalf("the probe answered %d, %v without queuing behind the held version table", got.version, got.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the probe did not queue behind the held version table within 10 s")
		}
	}
	select {
	case got := <-answered:
		t.Fatalf("the probe answered %d, %v while the version table was held, before %v and past the role's lock_timeout of %s; want it to wait until the table is free", got.version, got.err, held, roleSetting)
	case <-time.After(held):
	}
	if err := gate.Rollback(ctx); err != nil {
		t.Fatalf("release the version table: %v", err)
	}

	var got answer
	select {
	case got = <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe did not answer within 10 s of the version table being released")
	}
	if got.err != nil || got.version != stored {
		t.Fatalf("the probe answered %d, %v once the version table was free, want %d", got.version, got.err, stored)
	}
	// What the probe waited past is the role's bound: the health pool's session carries it, as a
	// session behind a pooler that drops startup parameters does, and not the parameter's none.
	health, err := own.Pool.healthPool()
	if err != nil {
		t.Fatalf("open the health pool: %v", err)
	}
	if setting := readLockTimeoutSetting(t, ctx, health); setting != roleSetting {
		t.Errorf("the health pool's connection carries lock_timeout = %q, want the role's %q", setting, roleSetting)
	}
}

// setRoleLockTimeout sets lock_timeout to setting on the test's role in own's database alone,
// which is the test's own, so no other test's session meets it, and resets it when the test ends,
// before the database is dropped.
func setRoleLockTimeout(t *testing.T, ctx context.Context, own *Store, setting string) {
	t.Helper()
	if _, err := own.Pool.Exec(ctx, `do $$ begin
		execute format('alter role current_user in database %I set lock_timeout = %L', current_database(), '`+setting+`');
	end $$`); err != nil {
		t.Fatalf("set lock_timeout on the test's role in its own database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := own.Pool.Exec(ctx, `do $$ begin
			execute format('alter role current_user in database %I reset lock_timeout', current_database());
		end $$`); err != nil {
			t.Errorf("reset the role's lock_timeout in the test's database: %v", err)
		}
	})
}

// readLockTimeoutSetting is lock_timeout as the session q runs on shows it (current_setting).
func readLockTimeoutSetting(t *testing.T, ctx context.Context, q lockBoundReader) string {
	t.Helper()
	var setting string
	if err := q.QueryRow(ctx, `select current_setting('lock_timeout')`).Scan(&setting); err != nil {
		t.Fatalf("read lock_timeout: %v", err)
	}
	return setting
}
