package duty_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/testdeadline"
)

// leaseStall is how long a test holds back a statement of Exclusive that waits behind another
// session's lock on duty_passes: longer than the five seconds every statement on a lease's own
// connection once had, so a bound of that size ends the statement while the database still
// answers it, and far inside the period the test's loop runs at.
const leaseStall = 7 * time.Second

// An unlock the server answers late, inside leaseCallTimeout, is waited for: the lock is given
// back and the connection returned to its pool.
func TestFreeSessionLockWaitsForAnUnlockAnsweredInsideItsBound(t *testing.T) {
	database := storetest.Open(t)
	leases := leasesOf(t, database)
	conn := heldSessionLock(t, leases, 7005)

	started := time.Now()
	if err := duty.FreeSessionLock(conn, `select pg_advisory_unlock($1) from pg_sleep(3)`, int64(7005)); err != nil {
		t.Fatalf("FreeSessionLock of an unlock answered after three seconds: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 3*time.Second {
		t.Fatalf("FreeSessionLock returned after %v, before its unlock's answer", elapsed)
	}
	requireConnections(t, leases, 1, 1, "a connection whose unlock answered true goes back to its pool")
	requireLockFree(t, database.Pool, 7005)
}

// An unlock on a connection that has gone half-open gets no answer, and fails at leaseCallTimeout,
// not before: its connection is closed, and the session it held goes with it.
func TestFreeSessionLockGivesUpOnAnUnlockThatGetsNoAnswerAtItsBound(t *testing.T) {
	database := storetest.Open(t)
	proxy := newSilencingProxy(t, database)
	leases := proxiedLeases(t, database, proxy)
	conn := heldSessionLock(t, leases, 7006)

	proxy.silence()
	started := time.Now()
	err := duty.FreeSessionLock(conn, `select pg_advisory_unlock($1)`, int64(7006))
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("FreeSessionLock on a half-open connection answered %v, want its deadline", err)
	}
	if elapsed < duty.LeaseCallTimeout {
		t.Fatalf("FreeSessionLock gave up after %v, before its %v bound", elapsed, duty.LeaseCallTimeout)
	}
	requireConnections(t, leases, 0, 0, "a connection whose unlock got no answer is closed")
}

// The period read and the record of a pass wait behind another session's lock on duty_passes, a
// migration's, as long as the loop's period allows: each is held past the five seconds it once
// had, and lands once the lock goes, so the pass runs and its period is recorded.
func TestExclusiveWaitsForThePeriodReadAndTheRecordBehindALock(t *testing.T) {
	ctx, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	database := storetest.Open(t)
	leases := leasesOf(t, database)
	const name, period = "lease-wait-test", time.Hour
	due := time.Now()

	migration := lockingSession(t, ctx, database)
	migration.lock(t)
	var ran atomic.Bool
	done := make(chan bool, 1)
	go func() {
		done <- duty.Exclusive(context.Background(), leases, name, due, period, func(context.Context) {
			ran.Store(true)
			// The record comes next, after the pass: the migration takes the table again.
			migration.lockFromPass()
		})
	}()

	outlastLock(t, ctx, database, done, migration, "the period read", func() bool { return !ran.Load() })
	outlastLock(t, ctx, database, done, migration, "the record", ran.Load)
	select {
	case served := <-done:
		if !served || !ran.Load() {
			t.Fatalf("Exclusive answered %t with the pass run %t, want the pass run once both statements landed", served, ran.Load())
		}
	case <-ctx.Done():
		t.Fatalf("Exclusive did not return by the test's deadline once the lock went: %v", ctx.Err())
	}
	if err := migration.err(); err != nil {
		t.Fatalf("take the lock again from the pass: %v", err)
	}
	var boundary time.Time
	if err := database.Pool.QueryRow(ctx, `select boundary from duty_passes where lease = $1`,
		name+"/"+strconv.FormatInt(period.Nanoseconds(), 10)).Scan(&boundary); err != nil {
		t.Fatalf("the pass's period is not recorded once the record's lock went: %v", err)
	}
	if want := duty.PeriodBoundary(due, period); !boundary.Equal(want) {
		t.Fatalf("the recorded boundary is %v, want %v", boundary, want)
	}
}

// lockSession is a session of its own that takes duty_passes in ACCESS EXCLUSIVE mode, as a
// migration that alters the table does, and gives it back.
type lockSession struct {
	conn *pgx.Conn
	mu   sync.Mutex
	tx   pgx.Tx
	fail error
}

func lockingSession(t *testing.T, ctx context.Context, database *store.Store) *lockSession {
	t.Helper()
	conn, err := pgx.Connect(ctx, database.Pool.Config().ConnString())
	if err != nil {
		t.Fatalf("connect the locking session: %v", err)
	}
	session := &lockSession{conn: conn}
	t.Cleanup(func() {
		session.release()
		_ = conn.Close(context.Background())
	})
	return session
}

// take begins a transaction and locks duty_passes in it.
func (s *lockSession) take() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.conn.Begin(context.Background())
	if err != nil {
		return err
	}
	if _, err := tx.Exec(context.Background(), `lock table duty_passes in access exclusive mode`); err != nil {
		_ = tx.Rollback(context.Background())
		return err
	}
	s.tx = tx
	return nil
}

func (s *lockSession) lock(t *testing.T) {
	t.Helper()
	if err := s.take(); err != nil {
		t.Fatalf("lock duty_passes: %v", err)
	}
}

// lockFromPass takes the lock from the pass's goroutine, keeping a failure for the test's.
func (s *lockSession) lockFromPass() {
	if err := s.take(); err != nil {
		s.mu.Lock()
		s.fail = err
		s.mu.Unlock()
	}
}

func (s *lockSession) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fail
}

// release ends the transaction that holds the lock, if one does.
func (s *lockSession) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx != nil {
		_ = s.tx.Rollback(context.Background())
		s.tx = nil
	}
}

// outlastLock waits until a statement waits on duty_passes' lock while ready holds, holds it there
// for leaseStall more, and releases the lock. Exclusive returning first fails the test, naming
// what: its statement could not answer while the lock stood, so a bound of its own ended it.
func outlastLock(t *testing.T, ctx context.Context, database *store.Store, done <-chan bool, session *lockSession, what string, ready func() bool) {
	t.Helper()
	began := time.Now()
	var blocked time.Time
	for {
		select {
		case served := <-done:
			t.Fatalf("Exclusive returned %t %s after %s began waiting, while the lock it needs still stood: a bound of its own ended %s",
				served, time.Since(began).Round(time.Millisecond), what, what)
		default:
		}
		if blocked.IsZero() {
			var waiting int
			if err := database.Pool.QueryRow(ctx, `select count(*) from pg_locks
				where database = (select oid from pg_database where datname = current_database())
				  and relation = 'duty_passes'::regclass and not granted`).Scan(&waiting); err != nil {
				t.Fatalf("read whether %s waits on the lock: %v", what, err)
			}
			if waiting > 0 && ready() {
				blocked = time.Now()
				t.Logf("%s waits behind the lock %s after the step began", what, blocked.Sub(began).Round(time.Millisecond))
			}
		} else if time.Since(blocked) >= leaseStall {
			session.release()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s never waited on the lock by the test's deadline", what)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// silencingProxy forwards each connection to the database until silence is called; from then on
// the connections it holds forward nothing and neither side is closed, so each stays open and
// silent, as a half-open connection does.
type silencingProxy struct {
	listener net.Listener
	target   string
	closed   chan struct{}
	silent   atomic.Bool
	mu       sync.Mutex
	conns    []net.Conn
}

func newSilencingProxy(t *testing.T, database *store.Store) *silencingProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the proxy: %v", err)
	}
	config := database.Pool.Config().ConnConfig
	proxy := &silencingProxy{listener: listener, target: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), closed: make(chan struct{})}
	go proxy.serve()
	t.Cleanup(proxy.close)
	return proxy
}

func (p *silencingProxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()
		go p.copy(server, client)
		go p.copy(client, server)
	}
}

// copy forwards from to to until the proxy is silenced or closed.
func (p *silencingProxy) copy(to io.Writer, from io.Reader) {
	buffer := make([]byte, 32<<10)
	for {
		n, err := from.Read(buffer)
		if n > 0 {
			if p.silent.Load() {
				<-p.closed
				return
			}
			if _, werr := to.Write(buffer[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *silencingProxy) silence() { p.silent.Store(true) }

func (p *silencingProxy) close() {
	close(p.closed)
	_ = p.listener.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		_ = conn.Close()
	}
}

// proxiedLeases is a lease pool whose connections reach the database through proxy.
func proxiedLeases(t *testing.T, database *store.Store, proxy *silencingProxy) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(database.Pool.Config().ConnString())
	if err != nil {
		t.Fatalf("parse the database's config: %v", err)
	}
	config.MaxConns = 2
	config.ConnConfig.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", proxy.listener.Addr().String())
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open the proxied lease pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
