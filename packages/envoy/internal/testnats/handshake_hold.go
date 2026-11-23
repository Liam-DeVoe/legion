package testnats

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testport"
	"github.com/sjawhar/envoy/internal/testwait"
)

// HandshakeHold is a Proxy whose relay to the server can hold a reconnect's handshake: Hold drops
// every connection the proxy relays, so a client through it reconnects, and until Release it holds
// the server's answer to the CONNECT of each connection made meanwhile. The server's INFO reaches
// the client and the client's CONNECT reaches the server; nothing more the server sends does, as at
// a hub whose auth service has not answered the CONNECT. nats.go holds its connection lock through a
// reconnect attempt's dial and handshake (v1.55.0 doReconnect, nats.go:3554-3581), so while a
// handshake is held every read of the connection's state waits. The client's own connect timeout
// would end the attempt, and the next would be held in turn; HoldReconnect gives the client a
// connect timeout of the test's own (OwnConnectTimeout), so a held attempt ends only when the row
// ends it: Release lets the answer through, and Cut drops the connection.
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
// (testport.LoopbackIP), until the test ends. URL is the same server through both.
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
	h.Proxy = StartProxy(t, relayed.String())
	return h
}

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

// HoldReconnect gives conn a connect timeout of the test's own (OwnConnectTimeout), holds (Hold)
// and waits until conn's reconnect handshake is held: a client's CONNECT has reached the server
// through the hold, and nats.go holds its connection lock for that handshake (AwaitLockHeld). Both
// waits are testwait's, so a reconnect that never reaches the hold fails the test naming what it
// waited for. A bus client reconnects about a second after its connection drops (ReconnectWait).
// The attempt it holds ends only when the row ends it (Release, Cut).
func (h *HandshakeHold) HoldReconnect(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	OwnConnectTimeout(t, conn)
	testwait.Receive(t, "a reconnect's CONNECT to reach the server through the handshake hold", h.Hold())
	AwaitLockHeld(t, conn)
}

// OwnConnectTimeout sets conn's connect timeout to what is left of the test binary's deadline less
// testwait's margin, and with no deadline (-timeout 0) to no bound a test can reach, as
// RequestTimeout bounds a JetStream request. nats.go's connect timeout (Options.Timeout, v1.55.0)
// bounds two things. One is the TCP dial, through the net.Dialer that Options.Connect builds from it
// once (nats.go:2017-2021); this leaves that dial's bound as Connect set it, since the proxy accepts
// every dial at once. The other is each connect and reconnect attempt's whole handshake, read at each
// attempt: processConnectInit sets the socket's deadline that far ahead (nats.go:2944) for the
// server's INFO, any TLS handshake, and the CONNECT and PING through the server's PONG
// (sendConnect). A hold keeps that PONG, so the handshake's deadline is what ends a held attempt, at
// the client's own timeout: nats.go's DefaultTimeout (2 s), or a bus client's connectTimeout (5 s).
// A row that reads a held attempt then turns on which of two real timers fires first, that one or
// the bound under test (an intake stop's callNATS, bus.ConnLockWait, 100 ms), and a loaded runner
// can reverse them. With the test's own timeout a held attempt ends only when the row ends it, so
// the bound under test is the one bound in the row. nats.go reads the timeout with its connection
// lock held (createConn and processConnectInit run under it), and its Conn.Opts doc calls a change
// to a running connection's options a race, so the timeout is set with that lock taken (connLock),
// as HoldWriteLock takes it.
func OwnConnectTimeout(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	timeout := left(t)
	lock := connLock(t, conn)
	testwait.Eventually(t, "nats.go to let its connection lock go, so the row can set the connect timeout under it", lock.TryLock)
	conn.Opts.Timeout = timeout
	lock.Unlock()
}

// AwaitLockHeld waits until nats.go holds its connection lock on conn (lockFree), as a held
// handshake or a held write holds it.
func AwaitLockHeld(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	testwait.Eventually(t, "nats.go to hold its connection lock: a handshake or a write holding it", func() bool {
		return !lockFree(t, conn)
	})
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

// HoldWriteLock holds conn's nats.go connection lock as a write to a socket that does not drain
// holds it, on a connection that stays connected: it takes that lock itself (TakeWriteLock), so
// from the moment it returns every call of conn's that reads or writes the connection waits, as
// each waits behind such a write, and nothing about the connection changes. The fixture takes the
// lock rather than filling the socket, so no span decides when a write blocked: the lock is held
// when the take returns, and let go when the row calls the func it returns, which the test's end
// calls too. The take's give-up fails the test. The proxy is left as it is. Since the lock is the
// row's own, no reset or close of the connection's socket lets it go: a row that measures what such
// a reset ends holds the lock with a write of nats.go's own (HoldStalledWrite).
func (h *HandshakeHold) HoldWriteLock(t testing.TB, conn *natsgo.Conn) (release func()) {
	t.Helper()
	release, err := h.TakeWriteLock(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return release
}

// TakeWriteLock takes conn's nats.go connection lock (connLockOf) as HoldWriteLock describes, with
// no t, for a hook the code under test runs on a goroutine of its own, where no method of t that
// ends the test may run: it waits for the lock through the shared wait's helper form
// (testwait.InHelper), with the bound every wait has, and returns that wait's give-up, or a nats.go
// whose Conn holds no connection lock, as an error rather than failing a test. It registers no
// cleanup: the hook hands the error to the test goroutine, which fails the row on it, and has the
// release run at the test's end. The release may be called more than once.
func (h *HandshakeHold) TakeWriteLock(conn *natsgo.Conn) (release func(), err error) {
	lock, err := connLockOf(conn)
	if err != nil {
		return nil, err
	}
	if err := testwait.InHelper("testnats", "nats.go to let its connection lock go, so the fixture can take it as a write does", lock.TryLock); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(lock.Unlock) }, nil
}

// stalledWriteSpan is how long one publish must stay inside nats.go, with every try to take its
// connection lock for reading failing, before HoldStalledWrite takes the write to be blocked on the
// frozen socket rather than merely under way (main's lockHeldWait, 200 ms). It decides when the
// hold is in place and is no verdict: the shared wait it sits in has no bound of its own, and what
// a row asserts of the lock is RequireLockHeld's reading of it.
const stalledWriteSpan = 200 * time.Millisecond

// HoldStalledWrite holds conn's nats.go connection lock with a write of nats.go's own, on a
// connection through the hold that stays connected: it freezes the proxy (Freeze) and publishes to
// conn until a write to the socket blocks under the lock, as a write to a socket whose peer has
// stopped reading blocks. HoldWriteLock takes the lock itself, so only its release lets the lock go;
// here the lock is the write's, so a reset or a close of the write's socket (bus.Client.Sever) fails
// the write and lets the lock go, as it does in production. It returns once one publish has stayed in
// nats.go for stalledWriteSpan while every try to take the lock for reading failed, so neither a slow
// read nor a slow publish alone decides it. The func it returns thaws the proxy (Restore) and stops
// the publishes; the test's end calls it too. It waits through the shared wait's helper form
// (testwait.InHelper), and when no write holds the lock by the time that wait gives up it fails the
// test by name without ending its goroutine, which may be a hook's rather than the test's, and
// returns that func.
func (h *HandshakeHold) HoldStalledWrite(t *testing.T, conn *natsgo.Conn) (release func()) {
	t.Helper()
	h.Freeze()
	stop := make(chan struct{})
	release = sync.OnceFunc(func() {
		close(stop)
		h.Restore()
	})
	t.Cleanup(release)
	lock, err := connLockOf(conn)
	if err != nil {
		t.Errorf("testnats: %v", err)
		return release
	}
	// begun counts the publishes begun and returned those that returned, so returned below begun
	// means the publish numbered begun is still inside nats.go.
	var begun, returned atomic.Uint64
	go func() {
		payload := make([]byte, 64<<10)
		for {
			select {
			case <-stop:
				return
			default:
			}
			begun.Add(1)
			err := conn.Publish("testnats.stalled-write", payload)
			returned.Add(1)
			if err != nil {
				return
			}
		}
	}()
	var watched uint64
	var since time.Time
	if err := testwait.InHelper("testnats", "a write of nats.go's own to hold its connection lock behind the frozen proxy", func() bool {
		publish := begun.Load()
		inside := returned.Load() < publish
		free := lock.TryRLock()
		if free {
			lock.RUnlock()
		}
		switch {
		case !inside || free:
			watched = 0
		case publish != watched:
			watched, since = publish, time.Now()
		case time.Since(since) >= stalledWriteSpan:
			return true
		}
		return false
	}); err != nil {
		t.Errorf("%v", err)
	}
	return release
}

// RequireLockHeld fails the test when nats.go's connection lock on conn is free (lockFree): the
// handshake, HoldWriteLock or HoldStalledWrite the row holds it with is not held. A row calls it
// once the calls it measures have answered, so their answers are known to have come while the lock
// was held.
func RequireLockHeld(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	if lockFree(t, conn) {
		t.Fatal("nats.go's connection lock was free: the handshake or the write the row holds it with was not held when the calls answered")
	}
}

// lockFree reports whether nats.go's connection lock on conn is free, read from the lock itself
// (connLock): TryRLock fails while a writer holds it or waits for it, which is when a read of conn's
// status (Conn.Status, which takes it to read) waits.
func lockFree(t testing.TB, conn *natsgo.Conn) bool {
	t.Helper()
	lock := connLock(t, conn)
	if !lock.TryRLock() {
		return false
	}
	lock.RUnlock()
	return true
}

// connLock is nats.go's connection lock on conn: the sync.RWMutex every call that reads or writes
// the connection takes (Conn.mu, nats.go v1.55.0 nats.go:659), which a reconnect's dial and
// handshake hold (doReconnect) and a write to a socket that does not drain holds. nats.go does not
// export it, so it is read through reflect, as cmd/listener's rows read the fields of the
// listener's own types; a nats.go whose Conn holds no such field fails the test, naming it.
func connLock(t testing.TB, conn *natsgo.Conn) *sync.RWMutex {
	t.Helper()
	lock, err := connLockOf(conn)
	if err != nil {
		t.Fatal(err)
	}
	return lock
}

// connLockOf is connLock with no t: a nats.go whose Conn holds no such field is its error.
func connLockOf(conn *natsgo.Conn) (*sync.RWMutex, error) {
	field := reflect.ValueOf(conn).Elem().FieldByName("mu")
	if !field.IsValid() || field.Type() != reflect.TypeFor[sync.RWMutex]() {
		return nil, errors.New("nats.go's Conn holds no mu sync.RWMutex, the connection lock the handshake hold reads (nats.go v1.55.0 nats.go:659)")
	}
	return (*sync.RWMutex)(unsafe.Pointer(field.UnsafeAddr())), nil
}

// Answers runs each call at once, each on a goroutine of its own, and returns their answers in
// order. Each answer is waited for through testwait, so a call that never answers fails the test
// naming what and its place.
func Answers(t testing.TB, what string, calls ...func() error) []error {
	t.Helper()
	answers := make([]chan error, len(calls))
	for i, call := range calls {
		answers[i] = make(chan error, 1)
		go func() { answers[i] <- call() }()
	}
	got := make([]error, len(calls))
	for i, answer := range answers {
		got[i] = testwait.Receive(t, fmt.Sprintf("%s %d of %d to answer", what, i+1, len(calls)), answer)
	}
	return got
}

// AwaitLockFree waits until nats.go lets conn's connection lock go (lockFree). A handshake held
// through HoldReconnect ends only when the row ends it; once Cut has dropped its connection, nats.go
// reads the connection as reconnecting and waits between attempts (ReconnectWait, 1 s for a bus
// client) with its lock free, and each attempt it makes while the proxy is cut ends at once.
func AwaitLockFree(t testing.TB, conn *natsgo.Conn) {
	t.Helper()
	testwait.Eventually(t, "nats.go to let its connection lock go: the held attempt to end", func() bool {
		return lockFree(t, conn)
	})
}

// Attempts is how many handshake attempts have reached the server under a hold: each CONNECT the
// hold has kept the answer to.
func (h *HandshakeHold) Attempts() int64 { return h.attempts.Load() }

// AnswersWithinAttempt runs each call at once, each on a goroutine of its own, and returns their
// answers in order. It fails the test, naming what, unless every call answered within the handshake
// attempt the hold held when they began: no later attempt has reached the hold by the time all have
// answered, and nats.go's lock is still held then (RequireLockHeld). The attempt ends only when the
// row ends it (HoldReconnect's connect timeout is the test's own), so a call that waits for nats.go's
// lock gives no answer while the hold holds, and the wait for its answer (Answers) fails the test,
// naming the call; the attempt count and the lock catch an answer that came only once something
// else had ended the attempt. The row decides by those events, and its waits are testwait's, with no
// bound of their own.
func (h *HandshakeHold) AnswersWithinAttempt(t testing.TB, conn *natsgo.Conn, what string, calls ...func() error) []error {
	t.Helper()
	began := h.Attempts()
	answers := Answers(t, what, calls...)
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
	server, err := net.Dial("tcp", h.target)
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
