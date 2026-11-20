package storetest

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/fataltest"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// HoldOn waits for its lock at most its bound and then fails naming the lock's holder, rather
// than waiting until go test's own timeout: here another transaction of the same database holds
// the lock and never lets go. The control's holder lets go well inside the bound, and HoldOn
// takes the lock.
func TestHoldOnFailsNamingTheHolderOfALockThatNeverFrees(t *testing.T) {
	const within = 2 * time.Second
	lock := AdvisoryLock(4815162342)

	t.Run("a holder that never lets go", func(t *testing.T) {
		database := Open(t)
		holder := Hold(t, database, lock, `select 'the holder that never lets go'`)
		conn := connect(t, database)
		started := time.Now()
		failure := fataltest.Failure(t, func(t testing.TB) { holdOn(t, conn, lock, within, "") })
		waited := time.Since(started)
		if failure == "" {
			t.Fatalf("HoldOn took %s while backend %d of its database held it", lock, holder)
		}
		for _, want := range []string{"waiting at most " + within.String(), fmt.Sprintf("backend %d,", holder), "the holder that never lets go"} {
			if !strings.Contains(failure, want) {
				t.Fatalf("HoldOn failed with %q, want %q in it", failure, want)
			}
		}
		if waited < within {
			t.Fatalf("HoldOn failed after %s, before its bound of %s: %s", waited, within, failure)
		}
		t.Logf("HoldOn failed after %s: %s", waited, failure)
	})

	t.Run("control: a holder that lets go in time", func(t *testing.T) {
		database := Open(t)
		holder := connect(t, database)
		HoldOn(t, holder, lock, "")
		conn := connect(t, database)
		let := make(chan struct{})
		time.AfterFunc(within/4, func() {
			defer close(let)
			_ = holder.Close(context.Background())
		})
		failure := fataltest.Failure(t, func(t testing.TB) { holdOn(t, conn, lock, within, "") })
		<-let
		if failure != "" {
			t.Fatalf("HoldOn failed for a lock its holder let go a quarter of its bound in: %s", failure)
		}
	})
}

// HoldOn, the function tests call, waits holdWait for its lock and no longer: on a lock another
// transaction of its database never frees it, it gives the wait up no sooner than holdWait, and
// its failure names that bound. The test reads when HoldOn gave the wait up from HoldOn itself:
// its failure then opens a connection of conn's configuration to read the holders (holdersBeside),
// and conn's configuration, which the test owns, marks that connection's dial. So the test's clock
// is HoldOn's own bound, holdWait, with answerWait beside it for HoldOn to give up a wait it was
// told to end, which waits for no lock; how long the failure's read then takes, inside its own
// bounds, is no part of the wait. The test above runs the same wait with a bound of its own
// (holdOn); this one holds HoldOn to holdWait itself.
func TestHoldOnWaitsHoldWaitAndNoLonger(t *testing.T) {
	lock := AdvisoryLock(4815162343)
	database := Open(t)
	holder := Hold(t, database, lock, `select 'the holder that never lets go'`)
	conn, dialed := connectMarkingDials(t, database)
	started := time.Now()
	recorder, done := fataltest.Start(t, func(t testing.TB) { HoldOn(t, conn, lock, "") })
	select {
	case at := <-dialed:
		if waited := at.Sub(started); waited < holdWait {
			t.Fatalf("HoldOn gave up its wait for %s after %s, before its bound of %s", lock, waited, holdWait)
		}
	case <-done:
		if failure := recorder.Message(); failure != "" {
			t.Fatalf("HoldOn failed without reading the holders of %s: %s", lock, failure)
		}
		t.Fatalf("HoldOn took %s while backend %d of its database held it", lock, holder)
	case <-time.After(holdWait + answerWait):
		t.Fatalf("HoldOn still waits for %s %s after it began, past its bound of %s", lock, time.Since(started).Round(time.Millisecond), holdWait)
	}
	select {
	case <-done:
	case <-fataltest.BeforeTheBinaryDeadline(t):
		t.Fatalf("HoldOn's failure, which gave up its wait for %s, had not returned before the test binary's deadline", lock)
	}
	failure := recorder.Message()
	if want := "waiting at most " + holdWait.String(); !strings.Contains(failure, want) {
		t.Fatalf("HoldOn failed with %q, want %q in it", failure, want)
	}
	t.Logf("HoldOn gave up its wait and failed after %s: %s", time.Since(started).Round(time.Millisecond), failure)
}

// connectMarkingDials opens a connection of its own to database, closed when t ends, whose
// configuration sends the time of each later dial on the channel it answers: the dial of a
// connection a helper opens from conn.Config(), as HoldOn's failure does to read the holders. The
// channel holds one time; a dial past it is not marked.
func connectMarkingDials(t *testing.T, database *store.Store) (*pgx.Conn, <-chan time.Time) {
	t.Helper()
	config := database.Pool.Config().ConnConfig.Copy()
	dial := config.DialFunc
	var armed atomic.Bool
	dialed := make(chan time.Time, 1)
	config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if armed.Load() {
			select {
			case dialed <- time.Now():
			default:
			}
		}
		return dial(ctx, network, address)
	}
	ctx, cancel := context.WithTimeout(t.Context(), answerWait)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to the database, waiting at most %s: %v", answerWait, err)
	}
	t.Cleanup(func() { closeWithin(conn) })
	armed.Store(true)
	return conn, dialed
}

// connectingWith is a store on database's database whose every new connection runs hook once it
// is up, a connection of its pool among them: the connections ConnectInListingOrder opens copy
// that pool's configuration. The pool opens one connection before it answers, so a hook armed
// after it does not act on the connection the pool's own reads take.
func connectingWith(t *testing.T, database *store.Store, hook func(context.Context, *pgconn.PgConn) error) *store.Store {
	t.Helper()
	config := database.Pool.Config()
	config.ConnConfig.AfterConnect = hook
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open a pool on the database: %v", err)
	}
	t.Cleanup(pool.Close)
	hooked := &store.Store{Pool: store.NewPool(pool)}
	if _, err := hooked.Pool.Exec(t.Context(), `select 1`); err != nil {
		t.Fatalf("open the pool's connection: %v", err)
	}
	return hooked
}

// ConnectInListingOrder refuses a connection whose backend has gone from the server's listing,
// naming that backend: the connection is dead, and no order holds on it. Here every connection it
// opens to the first database has its backend ended before the connection is handed to it, as a
// server that ends backends under a test does: pg_terminate_backend with a timeout, from a
// connection of the test's own, answers only once that backend has exited, so the backend is gone
// from the listing before ConnectInListingOrder reads it, however long the server takes to end it.
func TestConnectInListingOrderRefusesAConnectionWhoseBackendIsGone(t *testing.T) {
	var ending atomic.Bool
	var mu sync.Mutex
	var ended []uint32
	database := Open(t)
	first := connectingWith(t, database, func(ctx context.Context, conn *pgconn.PgConn) error {
		if !ending.Load() {
			return nil
		}
		pid := conn.PID()
		var gone bool
		if err := database.Pool.QueryRow(ctx, `select pg_terminate_backend($1, $2)`, int32(pid), answerWait.Milliseconds()).Scan(&gone); err != nil {
			return fmt.Errorf("end backend %d, waiting at most %s for it to exit: %w", pid, answerWait, err)
		}
		if !gone {
			return fmt.Errorf("backend %d had not exited %s after it was told to end", pid, answerWait)
		}
		mu.Lock()
		ended = append(ended, pid)
		mu.Unlock()
		return nil
	})
	second := Open(t)
	ending.Store(true)

	failure := fataltest.Failure(t, func(t testing.TB) { ConnectInListingOrder(t, first, second) })
	if failure == "" {
		t.Fatal("ConnectInListingOrder answered connections to a database whose backends end as they open")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(failure, "is gone from the server's listing") {
		t.Fatalf("ConnectInListingOrder failed with %q, want the gone backend named", failure)
	}
	for _, pid := range ended {
		if strings.Contains(failure, fmt.Sprintf("backend %d,", pid)) {
			t.Logf("ConnectInListingOrder refused the connection: %s", failure)
			return
		}
	}
	t.Fatalf("ConnectInListingOrder failed with %q, which names none of the %d backends that ended: %v", failure, len(ended), ended)
}

// AdvisoryLock counts the one-bigint form of its key alone: a waiter on the two-integer lock
// (3, 7), which pg_locks shows with the classid and objid of the bigint key 3<<32 | 7 and objsubid
// 2, is no waiter on that key, and a waiter on the key itself, objsubid 1, is one. Each waits in a
// database of its own, so each count meets one form alone.
func TestAdvisoryLockCountsItsOwnKeyFormAlone(t *testing.T) {
	key := AdvisoryLock(3<<32 | 7)
	pair := AdvisoryLockPair(3, 7)
	withPair := Open(t)
	Queue(t, withPair, pair)
	if got := WaitersOn(t, withPair, key); got != 0 {
		t.Fatalf("counted %d waiters on %s in a database where one backend waits on %s alone, the two-integer lock of the same halves", got, key, pair)
	}
	withKey := Open(t)
	Queue(t, withKey, key)
	if got := WaitersOn(t, withKey, key); got != 1 {
		t.Fatalf("counted %d waiters on %s in a database where one backend waits on it, want 1", got, key)
	}
}
