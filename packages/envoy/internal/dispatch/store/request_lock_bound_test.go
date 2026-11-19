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
