package pgmigrate

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/testpg"
	"github.com/sjawhar/envoy/internal/testwait"
)

// What the watch reports after a sequence of readings. One empty reading of a wait that named its
// holders is the cancellation race between pg_locks and pg_blocking_pids and is held back; two in a
// row mean nobody holding the lock can be named - the holder left, or only a prepared transaction,
// which pg_blocking_pids reports as pid 0, still blocks - and a departed holder must not be named.
// Each row is the sequence that tells one term of the rule from its absence.
func TestObserveNamesOnlyAHolderTheLatestReadingsSupport(t *testing.T) {
	named := func(pid uint32) *LockWait {
		return &LockWait{LockType: "relation", Mode: "AccessExclusiveLock", Object: "messages", Holders: []Session{{PID: pid}}}
	}
	empty := func() *LockWait {
		return &LockWait{LockType: "relation", Mode: "AccessExclusiveLock", Object: "messages"}
	}
	otherLock := &LockWait{LockType: "relation", Mode: "AccessExclusiveLock", Object: "asks"}
	otherMode := &LockWait{LockType: "relation", Mode: "ShareLock", Object: "messages"}
	for _, tc := range []struct {
		name     string
		readings []*LockWait
		want     *LockWait
	}{
		{"one empty reading as the cancellation lands keeps the holder", []*LockWait{named(97), empty()}, named(97)},
		{"two empty readings in a row name nobody", []*LockWait{named(97), empty(), empty()}, empty()},
		{"a holder named after a held-back reading replaces it, and the next empty reading is held back again", []*LockWait{named(97), empty(), named(98), empty()}, named(98)},
		{"a holder that takes over replaces the one before it", []*LockWait{named(97), named(98)}, named(98)},
		{"an empty reading of another lock replaces at once", []*LockWait{named(97), otherLock}, otherLock},
		{"an empty reading of another mode on the same table replaces at once", []*LockWait{named(97), otherMode}, otherMode},
		{"a reading of not waiting ends the run, so the next empty one is held back", []*LockWait{named(97), empty(), nil, empty()}, named(97)},
		{"a first reading that names nobody is recorded, so the refusal names the lock", []*LockWait{empty()}, empty()},
		{"a reading of not waiting after a named one keeps it, and the next empty one is held back", []*LockWait{named(97), nil, empty()}, named(97)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w watch
			for _, reading := range tc.readings {
				w.observe(reading)
			}
			if !reflect.DeepEqual(w.wait, tc.want) {
				t.Errorf("after %d readings the watch reports %+v, want %+v", len(tc.readings), w.wait, tc.want)
			}
		})
	}
}

// stallingForwarder passes bytes between one client and the Postgres server dsn names, on a
// loopback port of its own, until stall stops it: from then on it passes nothing in either
// direction and holds both connections open, so a statement written to it neither completes nor
// fails. That is the transport failure a diagnosis's cleanup must not wait on without a bound.
// Only the cleanup this test registers releases it, by closing every connection, and that runs
// after the test's own checks: a call that came back at all came back while the stall still held.
// Every goroutine the forwarder starts has ended when the test returns.
type stallingForwarder struct {
	// addr is the address to dial in the server's place.
	addr *net.TCPAddr

	mu        sync.Mutex
	stalled   bool
	swallowed bool
	conns     []net.Conn
	closing   bool
	wg        sync.WaitGroup
}

// stall stops the forwarder passing bytes, in both directions, for the rest of the test.
func (f *stallingForwarder) stall() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stalled = true
}

// holding reports whether the stall has taken bytes the client wrote and passed them to nobody:
// that something the client sent is waiting on this transport and waits on it still.
func (f *stallingForwarder) holding() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.swallowed
}

// track holds every connection the forwarder opens: a pump blocked reading one ends only when
// that connection closes, which the test's cleanup does for all of them.
func (f *stallingForwarder) track(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing {
		conn.Close()
		return false
	}
	f.conns = append(f.conns, conn)
	return true
}

// pump passes what it reads from src on to dst until either end stops or the stall begins. The
// chunk the stall meets reaches nobody, and the forwarder records that it swallowed it.
func (f *stallingForwarder) pump(dst, src net.Conn) {
	defer f.wg.Done()
	buf := make([]byte, 16*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			f.mu.Lock()
			halted := f.stalled
			if halted {
				f.swallowed = true
			}
			f.mu.Unlock()
			if halted {
				return
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// startForwarder puts a stallingForwarder in front of the server dsn names and returns it,
// listening and passing everything on until something calls stall.
func startForwarder(t *testing.T, dsn string) *stallingForwarder {
	t.Helper()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the test database's URL: %v", err)
	}
	network, address := pgconn.NetworkAddress(config.Host, config.Port)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &stallingForwarder{addr: listener.Addr().(*net.TCPAddr)}
	t.Cleanup(func() {
		listener.Close()
		f.mu.Lock()
		f.closing = true
		for _, conn := range f.conns {
			conn.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			if !f.track(client) {
				return
			}
			server, err := net.Dial(network, address)
			if err != nil {
				t.Errorf("dial the test database: %v", err)
				client.Close()
				return
			}
			if !f.track(server) {
				return
			}
			f.wg.Add(2)
			go f.pump(server, client)
			go f.pump(client, server)
		}
	}()
	return f
}

// stallAtTheCleanup stalls the forwarder at the first statement run on a context other than the
// one the diagnosis's reads ran on, and holds that statement at its start until the test releases
// it. Every statement of the diagnosis - its transaction, each holder's savepoint, the diagnosis
// query and that savepoint's rollback - runs on the one context diagnoseHolders reads under, and
// the cleanup's rollback is the first that does not, because diagnoseHolders gives the rollback
// and the close a context of their own (diagnosisCleanupTimeout). So the hold begins once the
// diagnosis has been read and before any byte of the cleanup leaves pgx, which traces a statement
// before it writes it, and the stall then swallows what the cleanup sends once released. Nothing
// here reads the wire: what the cleanup sends, and whether pgx sends anything at all, does not
// decide when the forwarder stops.
type stallAtTheCleanup struct {
	forwarder *stallingForwarder
	// release lets the held cleanup statement go on, into the stalled forwarder.
	release chan struct{}

	mu    sync.Mutex
	armed bool
	seen  bool
	reads <-chan struct{}
}

// arm starts the watch, once the connection is up: every statement from here is the diagnosis's
// or its cleanup's, and none is signing in.
func (s *stallAtTheCleanup) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = true
}

func (s *stallAtTheCleanup) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	cleanup := false
	switch {
	case !s.armed:
	case !s.seen:
		s.seen, s.reads = true, ctx.Done()
	case ctx.Done() != s.reads:
		s.forwarder.stall()
		cleanup = true
	}
	s.mu.Unlock()
	if cleanup {
		<-s.release
	}
	return ctx
}

func (s *stallAtTheCleanup) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A diagnosis returns what it read without waiting for its cleanup, and that cleanup ends on its
// own bound when its transport answers never. diagnoseHolders rolls its read-only transaction back
// and closes its connection on a goroutine and a context of their own, bounded by
// diagnosisCleanupTimeout: the tracer holds the cleanup's first statement, so diagnoseHolders
// returning at all is a return made before its cleanup had sent a byte, and a cleanup run in line
// would never let it return. Released, the cleanup's rollback goes into a forwarder that takes it
// and answers never - neither completing nor failing - and the cleanup still ends, by its own
// bound, while the forwarder holds it: the forwarder is released only by its own cleanup, after
// the checks below.
func TestDiagnoseHoldersReturnsWhenTheCleanupCannotFinish(t *testing.T) {
	ctx := context.Background()
	dsn := testpg.URL(t)
	// The session the diagnosis reads. It stays connected, so pg_stat_activity has a row for it.
	holder, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect the holder: %v", err)
	}
	t.Cleanup(func() { holder.Close(context.Background()) })
	var pid uint32
	if err := holder.QueryRow(ctx, "select pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("read the holder's pid: %v", err)
	}

	forwarder := startForwarder(t, dsn)
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the test database's URL: %v", err)
	}
	config.Host, config.Port = forwarder.addr.IP.String(), uint16(forwarder.addr.Port)
	// Everything the diagnosis sends has to cross the forwarder: a fallback dials the address the
	// URL named and would reach the server behind its back, and TLS over a loopback hop buys
	// nothing.
	config.TLSConfig, config.Fallbacks = nil, nil
	tracer := &stallAtTheCleanup{forwarder: forwarder, release: make(chan struct{})}
	config.Tracer = tracer
	// A check below that fails before the release still lets the held cleanup go, so its
	// goroutine ends with the test.
	letGo := sync.OnceFunc(func() { close(tracer.release) })
	t.Cleanup(letGo)
	dial := func(ctx context.Context) (*pgx.Conn, error) {
		conn, err := pgx.ConnectConfig(ctx, config)
		if err == nil {
			tracer.arm()
		}
		return conn, err
	}

	type diagnosis struct {
		read    []string
		cleaned <-chan struct{}
	}
	done := make(chan diagnosis, 1)
	go func() {
		read, cleaned := diagnoseHolders(ctx, dial, []Session{{PID: pid}})
		done <- diagnosis{read: read, cleaned: cleaned}
	}()
	got := testwait.Receive(t, "diagnoseHolders to return while the tracer holds its cleanup's first statement", done)
	want := fmt.Sprintf("diagnosis of pid %d, read when refused: ", pid)
	if len(got.read) != 1 || !strings.HasPrefix(got.read[0], want) {
		t.Fatalf("diagnoseHolders returned %q, want one diagnosis starting %q", got.read, want)
	}
	// The cleanup's rollback is held at its start until the release below, so a cleanup that had
	// ended already would be one that never sent its rollback at all.
	select {
	case <-got.cleaned:
		t.Fatal("diagnoseHolders' cleanup ended before its rollback was let go: it never rolled the diagnosis back")
	default:
	}

	letGo()
	testwait.Receive(t, "the cleanup to end on its own bound, its rollback taken by a transport that answers never", got.cleaned)
	if !forwarder.holding() {
		t.Fatal("the cleanup ended and the forwarder had swallowed nothing: its rollback never met the transport that answers never, so this row says nothing about the cleanup's bound")
	}
}
