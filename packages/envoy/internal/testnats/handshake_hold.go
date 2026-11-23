package testnats

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/goroutinetest"
	"github.com/sjawhar/envoy/internal/testport"
	"github.com/sjawhar/envoy/internal/testwait"
)

// lockHeldWait is how long a read of a connection's status may take before RequireLockHeld and
// AwaitLockHeld take nats.go's connection lock to be held: with the lock free the read takes
// microseconds.
const lockHeldWait = 200 * time.Millisecond

// HandshakeHold is a Proxy whose relay to the server can hold a reconnect's handshake: Hold drops
// every connection the proxy relays, so a client through it reconnects, and until Release it holds
// the server's answer to the CONNECT of each connection made meanwhile. The server's INFO reaches
// the client and the client's CONNECT reaches the server; nothing more the server sends does, as at
// a hub whose auth service has not answered the CONNECT. nats.go holds its connection lock through a
// reconnect attempt's dial and handshake (v1.53.1 doReconnect, nats.go:3458-3485), so while a
// handshake is held every read of the connection's state waits, until the client's connect timeout
// ends the attempt; the next attempt is held in turn.
//
// The proxy (Cut, Freeze, Restore) carries the client's connections, and the hold's own relay sits
// between it and the server. Restore ends a Cut or a Freeze and leaves a hold in force; Release
// ends the hold.
type HandshakeHold struct {
	*Proxy
	// target is the server's address, which the hold's relay dials.
	target   string
	listener net.Listener

	mu sync.Mutex
	// release is nil while connections are relayed as they are, and open from Hold until Release.
	release chan struct{}
	// held is closed once a CONNECT has reached the server under the hold Hold began.
	held     chan struct{}
	heldOnce *sync.Once
	// attempts counts the CONNECTs that have reached the server under a hold: each is one handshake
	// attempt whose answer the hold keeps.
	attempts atomic.Int64
}

// StartHandshakeHold relays to the NATS server at target, a client URL such as Start returns,
// through a proxy and the hold's own relay, each on a loopback address and port of its own
// (testport.LoopbackIP), until the test ends. URL is the same server through both. The proxy gives
// each client connection a receive buffer of holdSocketBuffer (ProxyReceiveBuffer), so a frozen
// proxy takes little of a client's stream (HoldStalledWrite).
func StartHandshakeHold(t testing.TB, target string) *HandshakeHold {
	t.Helper()
	server, err := url.Parse(target)
	if err != nil || server.Host == "" {
		t.Fatalf("read the NATS URL %q the handshake hold relays to: %v", target, err)
	}
	listener, err := testport.ListenLoopback()
	if err != nil {
		t.Fatalf("listen for the handshake hold: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	h := &HandshakeHold{target: server.Host, listener: listener}
	go h.serve()
	relayed := *server
	relayed.Host = listener.Addr().String()
	h.Proxy = StartProxy(t, relayed.String(), ProxyReceiveBuffer(holdSocketBuffer))
	return h
}

// holdSocketBuffer is the receive buffer of each client connection the hold's proxy accepts, and
// the send buffer HoldStalledWrite gives the client's own socket.
const holdSocketBuffer = 16 << 10

// Hold drops every connection the proxy relays (Cut, then Restore) and holds the answer to the
// CONNECT of each connection made from now until Release. The channel it returns is closed once a
// client's CONNECT has reached the server through the hold, so that client's handshake is waiting
// for the answer the hold keeps.
func (h *HandshakeHold) Hold() <-chan struct{} {
	h.mu.Lock()
	if h.release == nil {
		h.release, h.held, h.heldOnce = make(chan struct{}), make(chan struct{}), new(sync.Once)
	}
	held := h.held
	h.mu.Unlock()
	h.Cut()
	h.Restore()
	return held
}

// HoldReconnect holds (Hold) and waits until conn's reconnect handshake is held: a client's
// CONNECT has reached the server through the hold, and nats.go keeps a read of conn's status
// waiting, so its connection lock is held for that handshake. It fails the test when either has
// not happened within 30 s. A bus client reconnects about a second after its connection drops
// (ReconnectWait).
func (h *HandshakeHold) HoldReconnect(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	select {
	case <-h.Hold():
	case <-time.After(30 * time.Second):
		t.Fatal("no reconnect's CONNECT reached the server through the handshake hold within 30 s")
	}
	AwaitLockHeld(t, conn)
}

// AwaitLockHeld waits until nats.go keeps a read of conn's status waiting, so its connection lock is
// held, failing the test when it is not within 30 s.
func AwaitLockHeld(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for lockFree(conn) {
		if time.Now().After(deadline) {
			t.Fatal("nats.go still answered a read of the connection's status after 30 s: no handshake or write of it holds its lock")
		}
	}
}

// Release ends a Hold: every answer it kept moves on, and connections made from now on are relayed
// as they are.
func (h *HandshakeHold) Release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.release != nil {
		close(h.release)
		h.release = nil
	}
}

// HoldWriteLock holds conn's nats.go connection lock for writing on a connection through the hold
// that stays connected, as a write to a socket whose peer has stopped reading holds it: it freezes
// the proxy (Freeze), so nothing either side sends reaches the other, and takes the lock itself
// (takeConnLock). It returns once it holds the lock, which no reading of how long nats.go takes to
// answer decides: from then every nats.go call on conn that needs the lock - a publish, a flush, a
// close, a read of the status - waits until the func it returns thaws the proxy (Restore) and lets
// the lock go. The test's end calls that func too. When it cannot take the lock it fails the test by
// name without ending its goroutine, which may be a hook's rather than the test's (takeConnLock),
// and returns a func that only thaws the proxy. Since the lock is the row's own, no reset or close
// of the connection's socket lets it go: a row that measures what such a reset ends holds the lock
// with a write of nats.go's own (HoldStalledWrite).
func (h *HandshakeHold) HoldWriteLock(t *testing.T, conn *natsgo.Conn) (release func()) {
	t.Helper()
	h.Freeze()
	t.Cleanup(h.Restore)
	lock, taken := takeConnLock(t, conn)
	if !taken {
		return h.Restore
	}
	release = sync.OnceFunc(func() {
		h.Restore()
		lock.Unlock()
	})
	t.Cleanup(release)
	return release
}

// HoldStalledWrite holds conn's nats.go connection lock with a write of nats.go's own, on a
// connection through the hold that stays connected, as a write to a socket whose peer has stopped
// reading holds it: it freezes the proxy (Freeze) and makes one publish that the frozen proxy's
// network stack cannot take whole, so the write, and with it the lock, lasts until the func it
// returns thaws the proxy (Restore), until the connection's write deadline ends the write
// (FlusherTimeout: nats.go's default of a minute, shorter with bus.WithWriteDeadline), or until a
// reset or a close of the write's socket (bus.Client.Sever) fails it, as it does in production.
// HoldWriteLock takes the lock itself, so only its release lets the lock go.
//
// nats.go v1.55.0 holds its connection lock from the Lock in (*Conn).publish to its Unlock
// (nats.go:4730-4825). A publish whose message passes its buffer's limit writes it to the socket
// inside that, in (*natsWriter).flush, which returns only once the socket has taken every byte. A
// frozen proxy reads the client's socket at most once more, the read in flight as it froze, of up
// to proxyReadBuffer: Proxy.relay calls pass before each read. So the network stack can take no
// more of a write than the client socket's send buffer, the proxy's receive buffer for the
// connection and that one read. A write seen parked is no proof by itself: on gVisor the stack
// handles the segments it was sent late, so a write parks before those buffers are full and goes on
// once the stack has handled them, after the proxy has stopped reading. A loop of 64 KiB publishes
// with the stack's own buffer sizes, returned from at the first publish seen parked in its write,
// let the lock go within lockHeldWait of that return in 14 of 20 runs on a gVisor pod.
//
// So both buffers are small and the one write is far bigger than both. The hold's proxy gives each
// client connection a receive buffer of holdSocketBuffer from its accept (ProxyReceiveBuffer).
// Before it freezes the proxy, HoldStalledWrite gives conn's own socket a send buffer of
// holdSocketBuffer (clientSocket finds that socket: testnats does not hold nats.go's net.Conn). It
// reads both sizes back, and fails the test, naming the three sizes, unless the payload, conn's
// MaxPayload less 4 KiB, is more than twice what the stack can take. It then freezes the proxy and
// makes the one publish, on a goroutine of its own, and returns once a try to take the lock for
// reading has failed (connLock) and the runtime's record of that goroutine, read after the try
// (goroutinetest.Traces), shows it parked in its write to the socket: waiting for the socket to
// take bytes (IO wait) in internal/poll's FD.Write, inside (*natsWriter).flush under
// (*Conn).publish. From then on the lock is held until the thaw, the write deadline or the
// socket's end, and no time and no read of the connection's status decides it: a publish that is
// only slow, or a process that was not scheduled, is not parked waiting for the socket. It reads
// that record about every 10 ms, and fails the test, with the goroutine's stack, when the publish
// has not parked by connLockSpare before the test binary's deadline (testwait.Left). The test's end
// calls the func it returns too.
//
// It never ends its goroutine, which may be a hook's rather than the test's, where t.Fatal would
// end the hook rather than the test. So when it cannot hold the write - nats.go's Conn has no lock
// it can read, conn is not connected, a socket or its buffer cannot be found, set or read, the
// payload is too small to prove the hold, the test binary's deadline is already no more than
// connLockSpare away, the publish returns before it is seen parked, or the bound passes - it fails
// the test by name with Errorf and returns a func that only thaws the proxy. It returns that func
// too, failing nothing, when the test ends first.
func (h *HandshakeHold) HoldStalledWrite(t *testing.T, conn *natsgo.Conn) (release func()) {
	t.Helper()
	lock, found := connLock(t, conn)
	if !found {
		return h.Restore
	}
	local := conn.LocalAddr()
	if local == "" {
		t.Error("control: the connection to hold a write on is not connected")
		return h.Restore
	}
	payload := int(conn.MaxPayload()) - 4<<10
	client, err := h.clientSocket(local)
	if err != nil {
		t.Error(err)
		return h.Restore
	}
	if err := syscall.SetsockoptInt(client, syscall.SOL_SOCKET, syscall.SO_SNDBUF, holdSocketBuffer); err != nil {
		t.Errorf("set the send buffer of the socket at %s: %v", local, err)
		return h.Restore
	}
	send, err := syscall.GetsockoptInt(client, syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	if err != nil {
		t.Errorf("read the send buffer of the socket at %s: %v", local, err)
		return h.Restore
	}
	receive, err := h.receiveBuffer(local)
	if err != nil {
		t.Error(err)
		return h.Restore
	}
	if payload <= 2*(send+receive+proxyReadBuffer) {
		t.Errorf("the hold cannot be proven on this host: a payload of %d bytes is not more than twice what the stack can take of it, a send buffer of %d bytes, a receive buffer of %d bytes and a read in flight of %d bytes",
			payload, send, receive, proxyReadBuffer)
		return h.Restore
	}
	bound, err := testwait.Left(t, connLockSpare)
	if err != nil {
		t.Error(err)
		return h.Restore
	}
	h.Freeze()
	release = sync.OnceFunc(h.Restore)
	t.Cleanup(release)
	publisher := make(chan uint64, 1)
	published := make(chan error, 1)
	go func() {
		publisher <- goroutinetest.ID()
		published <- conn.Publish("testnats.stalled-write", make([]byte, payload))
	}()
	header := fmt.Sprintf("goroutine %d [", <-publisher)
	deadline := time.Now().Add(bound)
	for {
		if lock.TryRLock() {
			lock.RUnlock()
		} else if parkedInItsWrite(goroutineTrace(header)) {
			return release
		}
		select {
		case err := <-published:
			if err != nil {
				t.Errorf("the publish of %d bytes failed while the proxy was frozen, before it was seen parked in its write: %v", payload, err)
				return h.Restore
			}
			t.Errorf("the publish of %d bytes returned while the proxy was frozen, before it was seen parked in its write: the stack took more of it than its send buffer of %d bytes, its receive buffer of %d bytes and the read in flight allow",
				payload, send, receive)
			return h.Restore
		default:
		}
		if time.Now().After(deadline) {
			t.Errorf("the publish of %d bytes was not seen parked in its write to the socket under nats.go's connection lock within %s of the proxy's freeze, %s before the test binary's deadline. The publishing goroutine:\n%s",
				payload, bound.Round(time.Millisecond), connLockSpare, goroutineTrace(header))
			return h.Restore
		}
		select {
		case <-t.Context().Done():
			return h.Restore
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// clientSocket is the descriptor of the socket conn writes to, the client side of the connection
// the hold's proxy accepted for it: the one descriptor of the process whose local address is local,
// conn's, and whose peer is the proxy's listener. It reads every open descriptor (/proc/self/fd, or
// /dev/fd where there is no /proc) with getsockname and getpeername, and returns an error, naming
// the address, unless exactly one matches.
func (h *HandshakeHold) clientSocket(local string) (int, error) {
	peer := h.Proxy.listener.Addr().String()
	descriptors, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		descriptors, err = os.ReadDir("/dev/fd")
	}
	if err != nil {
		return 0, fmt.Errorf("list the process's descriptors to find the socket at %s: %w", local, err)
	}
	var found []int
	for _, descriptor := range descriptors {
		fd, err := strconv.Atoi(descriptor.Name())
		if err != nil {
			continue
		}
		if name, err := syscall.Getsockname(fd); err != nil || sockaddrString(name) != local {
			continue
		}
		if remote, err := syscall.Getpeername(fd); err != nil || sockaddrString(remote) != peer {
			continue
		}
		found = append(found, fd)
	}
	if len(found) != 1 {
		return 0, fmt.Errorf("control: %d descriptors of the process are a socket at %s connected to the proxy at %s, want 1", len(found), local, peer)
	}
	return found[0], nil
}

// receiveBuffer is the receive buffer (SO_RCVBUF) the stack reports for the connection the hold's
// proxy accepted from local, or an error, naming the address, when it cannot read it.
func (h *HandshakeHold) receiveBuffer(local string) (int, error) {
	var accepted syscall.Conn
	h.Proxy.mu.Lock()
	for relayed := range h.Proxy.conns {
		if relayed.RemoteAddr().String() == local {
			accepted, _ = relayed.(syscall.Conn)
		}
	}
	h.Proxy.mu.Unlock()
	if accepted == nil {
		return 0, fmt.Errorf("control: the proxy relays no connection from %s", local)
	}
	raw, err := accepted.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("reach the socket the proxy accepted from %s: %w", local, err)
	}
	var size int
	var read error
	if err := raw.Control(func(fd uintptr) {
		size, read = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil {
		return 0, fmt.Errorf("reach the socket the proxy accepted from %s: %w", local, err)
	}
	if read != nil {
		return 0, fmt.Errorf("read the receive buffer of the socket the proxy accepted from %s: %w", local, read)
	}
	return size, nil
}

// sockaddrString is address as net.TCPAddr.String writes it, or "" for an address that is not an
// IPv4 or IPv6 one.
func sockaddrString(address syscall.Sockaddr) string {
	switch address := address.(type) {
	case *syscall.SockaddrInet4:
		return netip.AddrPortFrom(netip.AddrFrom4(address.Addr), uint16(address.Port)).String()
	case *syscall.SockaddrInet6:
		return netip.AddrPortFrom(netip.AddrFrom16(address.Addr), uint16(address.Port)).String()
	}
	return ""
}

// The frames of a publish parked in its write to the socket, as runtime.Stack prints them
// (parkedInItsWrite): a dot in the last element of a package's path is escaped (nats%2ego).
const (
	socketWriteFrame     = "internal/poll.(*FD).Write("
	natsWriterFlushFrame = "github.com/nats-io/nats%2ego.(*natsWriter).flush("
	connPublishFrame     = "github.com/nats-io/nats%2ego.(*Conn).publish("
)

// goroutineTrace is the trace of the goroutine whose trace opens with header, such as
// "goroutine 7 [", or "" when that goroutine has returned.
func goroutineTrace(header string) string {
	for _, trace := range goroutinetest.Traces() {
		if strings.HasPrefix(trace, header) {
			return trace
		}
	}
	return ""
}

// parkedInItsWrite reports whether trace is a publish parked in its write to the socket: the
// netpoller keeps it waiting for the socket to take bytes (IO wait) in internal/poll's FD.Write,
// inside (*natsWriter).flush under (*Conn).publish. A publish still copying bytes, or one whose
// goroutine is runnable or asleep, is not.
func parkedInItsWrite(trace string) bool {
	header, _, _ := strings.Cut(trace, "\n")
	return strings.Contains(header, "[IO wait") && strings.Contains(trace, socketWriteFrame) &&
		strings.Contains(trace, natsWriterFlushFrame) && strings.Contains(trace, connPublishFrame)
}

// RequireLockHeld fails the test when nats.go answers a read of conn's status within lockHeldWait:
// its connection lock is not held, so the handshake, HoldWriteLock or HoldStalledWrite the row holds
// it with is not. A row calls it once the calls it measures have answered, so their answers are known
// to have come while the lock was held. The read left waiting ends when the lock is let go.
func RequireLockHeld(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	if lockFree(conn) {
		t.Fatalf("nats.go answered a read of the connection's status within %s: its connection lock was not held when the calls answered", lockHeldWait)
	}
}

// lockFree reports whether nats.go answers a read of conn's status within lockHeldWait.
func lockFree(conn *natsgo.Conn) bool {
	read := make(chan struct{})
	go func() {
		conn.Status()
		close(read)
	}()
	select {
	case <-read:
		return true
	case <-time.After(lockHeldWait):
		return false
	}
}

// AnswersWithin runs each call at once, each on a goroutine of its own, and returns their answers in
// order. It fails the test, naming what, when any call has not answered within within of the start.
func AnswersWithin(t testing.TB, within time.Duration, what string, calls ...func() error) []error {
	t.Helper()
	answers := make([]chan error, len(calls))
	for i, call := range calls {
		answers[i] = make(chan error, 1)
		go func() { answers[i] <- call() }()
	}
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	got := make([]error, len(calls))
	for i, answer := range answers {
		select {
		case got[i] = <-answer:
		case <-deadline.C:
			t.Fatalf("%s %d of %d gave no answer within %s while nats.go's lock was held: it waited for the lock", what, i+1, len(calls), within)
		}
	}
	return got
}

// AwaitLockFree waits until nats.go lets conn's connection lock go, failing the test after 30 s.
// With every reconnect's handshake held (HoldReconnect), that is the end of an attempt, when nats.go
// reads the connection as reconnecting and waits between attempts (ReconnectWait, 1 s for a bus
// client) with its lock free.
func AwaitLockFree(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	read := make(chan struct{})
	go func() {
		conn.Status()
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(30 * time.Second):
		t.Fatal("nats.go still held its connection lock 30 s on: no attempt's handshake ended")
	}
}

// Attempts is how many handshake attempts have reached the server under a hold: each CONNECT the
// hold has kept the answer to.
func (h *HandshakeHold) Attempts() int64 { return h.attempts.Load() }

// AnswersWithinAttempt runs each call at once, each on a goroutine of its own, and returns their
// answers in order. It fails the test, naming what, unless every call answered within the handshake
// attempt the hold held when they began: no later attempt has reached the hold by the time all have
// answered, and nats.go's lock is still held then (RequireLockHeld). A call that waits for nats.go's
// lock answers only once that attempt has ended, at the client's connect timeout: in the gap after
// it, with the lock free, or once the next attempt has begun. The row decides by those events, and
// no wall-clock bound but a 30 s guard against a call that never answers.
func (h *HandshakeHold) AnswersWithinAttempt(t testing.TB, conn *natsgo.Conn, what string, calls ...func() error) []error {
	t.Helper()
	began := h.Attempts()
	answers := AnswersWithin(t, 30*time.Second, what, calls...)
	if ended := h.Attempts(); ended != began {
		t.Fatalf("%s answered only after %d more handshake attempt(s) had reached the hold: it waited out the attempt held when it began", what, ended-began)
	}
	RequireLockHeld(t, conn)
	return answers
}

// serve relays each connection the proxy makes to the hold until the hold's listener closes.
func (h *HandshakeHold) serve() {
	for {
		client, err := h.listener.Accept()
		if err != nil {
			return
		}
		go h.join(client)
	}
}

// join relays client, a connection the proxy made for one of its clients, to the server, under the
// hold when one is in force as client arrives, until either side ends; then it closes both. The
// client's stream is read as NATS protocol (protocolStream) to find its CONNECT, which may come in
// pieces.
func (h *HandshakeHold) join(client net.Conn) {
	defer client.Close()
	server, err := net.DialTimeout("tcp", h.target, dialTimeout)
	if err != nil {
		return
	}
	defer server.Close()
	h.mu.Lock()
	release, held, heldOnce := h.release, h.held, h.heldOnce
	h.mu.Unlock()
	gone := make(chan struct{})
	defer close(gone)
	go func() {
		defer client.Close()
		defer server.Close()
		answers := bufio.NewReader(server)
		if release != nil {
			info, err := answers.ReadBytes('\n')
			if err != nil {
				return
			}
			if _, err := client.Write(info); err != nil {
				return
			}
			select {
			case <-release:
			case <-gone:
				return
			}
		}
		_, _ = io.Copy(client, answers)
	}()
	var stream protocolStream
	buffer := make([]byte, 32<<10)
	for {
		read, err := client.Read(buffer)
		if read > 0 {
			if _, err := server.Write(buffer[:read]); err != nil {
				return
			}
			if release != nil && !stream.connected {
				stream.wrote(buffer[:read])
				if stream.connected {
					h.attempts.Add(1)
					heldOnce.Do(func() { close(held) })
				}
			}
		}
		if err != nil {
			return
		}
	}
}
