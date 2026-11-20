package storetest

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// fatalRecorder is t with Fatal recorded and the goroutine ended, as t.Fatal does.
type fatalRecorder struct {
	testing.TB
	message string
}

func (f *fatalRecorder) Fatal(args ...any) { f.message = fmt.Sprint(args...); runtime.Goexit() }
func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// failureOf runs call on a goroutine of its own over t, with t's Fatal recorded, and answers what
// call failed with: "" when it returned.
func failureOf(t *testing.T, call func(testing.TB)) string {
	t.Helper()
	recorder := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(recorder)
	}()
	<-done
	return recorder.message
}

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
		failure := failureOf(t, func(t testing.TB) { holdOn(t, conn, lock, within, "") })
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
		failure := failureOf(t, func(t testing.TB) { holdOn(t, conn, lock, within, "") })
		<-let
		if failure != "" {
			t.Fatalf("HoldOn failed for a lock its holder let go a quarter of its bound in: %s", failure)
		}
	})
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
// opens to the first database ends its own backend a millisecond after it goes idle
// (idle_session_timeout), as a server that ends backends under a test does.
func TestConnectInListingOrderRefusesAConnectionWhoseBackendIsGone(t *testing.T) {
	var ending atomic.Bool
	var mu sync.Mutex
	var ended []uint32
	first := connectingWith(t, Open(t), func(ctx context.Context, conn *pgconn.PgConn) error {
		if !ending.Load() {
			return nil
		}
		mu.Lock()
		ended = append(ended, conn.PID())
		mu.Unlock()
		return conn.Exec(ctx, `set idle_session_timeout = 1`).Close()
	})
	second := Open(t)
	ending.Store(true)

	failure := failureOf(t, func(t testing.TB) { ConnectInListingOrder(t, first, second) })
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
