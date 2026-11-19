package sessionproxy

// What the database's own end of a connection does to the proxy's Fault, the record a child's exit
// is read by (launcher's wait): a connection's dial the database refused or never answered, and a
// served connection it reset, are faults until a dial that starts later reaches it.

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// holdEach has every dial from now on wait d before it dials, past any wait a read deadline of a
// test would give it.
func (f *fakeDialer) holdEach(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold = d
}

// dialAt points every dial from now on at addr.
func (f *fakeDialer) dialAt(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addr = addr
}

// heldDial is a dial holdNext holds: entered closes once it has begun, and it dials addr once
// release closes.
type heldDial struct {
	addr             string
	entered, release chan struct{}
}

// holdNext holds the next dial until the returned dial's release closes, then has it dial addr.
func (f *fakeDialer) holdNext(addr string) *heldDial {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = &heldDial{addr: addr, entered: make(chan struct{}), release: make(chan struct{})}
	return f.held
}

// A connection's own dial the database refused, after a start whose proof dial it answered, is
// the proxy's Fault, ErrUnreachable, until a later connection's dial reaches the database; a dial
// refused over anything else, a sign-in the instance refused, is no fault of the store's. Each
// dial is held past any wait a read deadline would give it, and each look at Fault waits for the
// proxy to have finished with that dial: a refused one by the close of its client, which serve
// makes after it records, and one that reached the database by bytes that crossed the proxy.
func TestAConnectionsDialTheDatabaseRefusedIsTheProxysFaultUntilOneReachesIt(t *testing.T) {
	up := newUpstream(t)
	dialer := &fakeDialer{addr: up.listener.Addr().String()}
	socket := socketPath(t)
	proxy := startProxy(context.Background(), t, up, dialer, socket)
	defer proxy.Close()
	dialer.holdEach(750 * time.Millisecond)
	if err := proxy.Fault(); err != nil {
		t.Fatalf("Fault before any connection = %v, want none", err)
	}

	dialer.dialAt(testdb.RefusedAddress(t))
	awaitClosed(t, dialProxy(t, socket), "a connection whose dial the database refused")
	if err := proxy.Fault(); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Fault after a connection whose dial the database refused = %v, want ErrUnreachable", err)
	}

	dialer.dialAt(up.listener.Addr().String())
	client := dialProxy(t, socket)
	writeAll(t, client, "ping")
	if got := readString(t, up.next(t), 4); got != "ping" {
		t.Fatalf("the upstream read %q of the client's ping", got)
	}
	if err := proxy.Fault(); err != nil {
		t.Fatalf("Fault after a connection whose dial reached the database = %v, want none", err)
	}

	dialer.failWith(errors.New("the instance refused the login"))
	awaitClosed(t, dialProxy(t, socket), "a connection whose sign-in the instance refused")
	if err := proxy.Fault(); err != nil {
		t.Fatalf("Fault after a dial refused over the sign-in = %v, want none: the database answered", err)
	}
}

// Fault is the latest event's, each dial's as of its start: a slow dial that fails after a later
// connection's dial reached the database leaves no fault while that later connection is served,
// and a dial that starts after the later one and fails is the fault.
func TestAFaultIsOfTheLatestDialToStartNotTheLatestToEnd(t *testing.T) {
	up := newUpstream(t)
	dialer := &fakeDialer{addr: up.listener.Addr().String()}
	socket := socketPath(t)
	proxy := startProxy(context.Background(), t, up, dialer, socket)
	defer proxy.Close()

	held := dialer.holdNext(testdb.RefusedAddress(t))
	slow := dialProxy(t, socket)
	<-held.entered
	served := dialProxy(t, socket)
	writeAll(t, served, "ping")
	if got := readString(t, up.next(t), 4); got != "ping" {
		t.Fatalf("the upstream read %q of the later client's ping", got)
	}
	close(held.release)
	awaitClosed(t, slow, "the connection whose slow dial the database refused")
	if err := proxy.Fault(); err != nil {
		t.Fatalf("Fault = %v while the later connection, whose dial reached the database, is served; want none", err)
	}

	dialer.dialAt(testdb.RefusedAddress(t))
	awaitClosed(t, dialProxy(t, socket), "a connection whose dial started last and the database refused")
	if err := proxy.Fault(); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Fault after the latest dial was refused = %v, want ErrUnreachable", err)
	}
}

// A served connection the database ends with a reset is the proxy's Fault, as a dial it refused
// is, until a dial that starts later reaches it: a child holding that connection exited on a store
// that stopped answering it. A connection the database ends in order (an end of file), as every
// session that ends ends, and one the proxy itself closes once its drain is over, are none.
func TestAServedConnectionTheDatabaseResetsIsTheProxysFault(t *testing.T) {
	for name, tc := range map[string]struct {
		end   func(t *testing.T, served net.Conn, proxy *Proxy)
		fault bool
	}{
		"the database resets it": {fault: true, end: func(t *testing.T, served net.Conn, _ *Proxy) {
			_ = served.(*net.TCPConn).SetLinger(0)
			_ = served.Close()
		}},
		"control: the database ends it in order": {end: func(t *testing.T, served net.Conn, _ *Proxy) {
			_ = served.Close()
		}},
		"control: the proxy closes it once its drain is over": {end: func(t *testing.T, _ net.Conn, proxy *Proxy) {
			_ = proxy.Close()
		}},
	} {
		t.Run(name, func(t *testing.T) {
			up := newUpstream(t)
			dialer := &fakeDialer{addr: up.listener.Addr().String()}
			socket := socketPath(t)
			proxy := startProxy(context.Background(), t, up, dialer, socket)
			defer proxy.Close()
			client := dialProxy(t, socket)
			writeAll(t, client, "ping")
			served := up.next(t)
			if got := readString(t, served, 4); got != "ping" {
				t.Fatalf("the upstream read %q of the client's ping", got)
			}
			tc.end(t, served, proxy)
			awaitClosed(t, client, "the client of a connection whose upstream ended")
			if got := proxy.Fault(); errors.Is(got, ErrUnreachable) != tc.fault {
				t.Fatalf("Fault after the served connection ended = %v, want ErrUnreachable %t", got, tc.fault)
			}
			if !tc.fault {
				return
			}
			client = dialProxy(t, socket)
			writeAll(t, client, "ping")
			if got := readString(t, up.next(t), 4); got != "ping" {
				t.Fatalf("the upstream read %q of the next client's ping", got)
			}
			if err := proxy.Fault(); err != nil {
				t.Fatalf("Fault after a later dial reached the database = %v, want none", err)
			}
		})
	}
}

// dialProxy is a client of the proxy at socket, closed when the test ends.
func dialProxy(t *testing.T, socket string) net.Conn {
	t.Helper()
	client, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial the proxy: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// awaitClosed waits for the proxy to close client, which serve does once it is done with that
// connection: after it recorded a failed dial, or after both copies of a served connection ended.
// A proxy that does not close it within a bound no dial of a test takes fails the test by name.
func awaitClosed(t *testing.T, client net.Conn, what string) {
	t.Helper()
	if err := client.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set a read deadline: %v", err)
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("the proxy did not close %s: the read answered %v", what, err)
	}
}
