package testnats

import (
	"bytes"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Proxy relays a client's TCP connections to a NATS server, so a test can take one client off
// NATS while every other client keeps its own connection: Cut drops it, as a network that ends a
// connection does, and Freeze silences it, as a network that drops packets does, and Restore ends
// either. A Proxy started with ProxyLatency adds a round trip's delay to everything it relays, as
// the route to a remote NATS hub does, and one started with ProxyRate moves no faster than its
// rate, as a slow link does.
type Proxy struct {
	listener net.Listener
	target   string
	url      string
	delay    time.Duration
	done     chan struct{}
	closing  sync.Once
	// rate is the bytes a second each direction of each connection reads at, 0 for no limit.
	rate int

	mu sync.Mutex
	// conns are both ends of every connection the proxy is relaying.
	conns map[net.Conn]struct{}
	// cut is set from Cut until Restore: every connection is closed as it is accepted.
	cut bool
	// gate is nil while the proxy moves bytes, and open from Freeze until Restore: every read
	// and every write of a relayed connection waits for it to close.
	gate chan struct{}
	// hold is set from HoldConnectAnswers until Restore: each connection accepted meanwhile is
	// held at its handshake.
	hold *ConnectHold
}

// ConnectHold is a hold of the server's answer to a client's CONNECT (Proxy.HoldConnectAnswers):
// the client has sent its CONNECT, and its handshake waits for the server's answer, with nats.go's
// connection lock held (nats.go v1.53.1 processConnectInit, under doReconnect's lock, :3458-3485),
// until Restore or until the client's own connect timeout ends the attempt. Every connection the
// proxy accepts while the hold lasts is held the same way, each until Restore or its own end; Held
// and Ended report the first connection whose CONNECT reached the server.
type ConnectHold struct {
	held, ended, release chan struct{}
	heldOnce, endedOnce  sync.Once
	// waiting gets a token each time a connection's answer starts to wait for the hold, and
	// passed the reason each such wait ended ("release", "own end", "proxy closed"), for the
	// package's own rows; a token a full channel has no room for is dropped.
	waiting chan struct{}
	passed  chan string
}

// Held is closed once a client's CONNECT has reached the server through the hold, so its
// handshake is waiting for the answer the hold keeps.
func (h *ConnectHold) Held() <-chan struct{} { return h.held }

// Ended is closed once a connection the hold held has closed: the client gave its handshake up,
// at its connect timeout, or the proxy cut it. A connection accepted after that one is still held
// until Restore or its own end.
func (h *ConnectHold) Ended() <-chan struct{} { return h.ended }

// HoldConnectAnswers ends a Cut and holds the handshake of each connection the proxy accepts from
// now until Restore: the server's INFO reaches the client, the client's CONNECT reaches the server,
// and nothing more the server sends reaches the client until Restore, its answer to the CONNECT
// included. That is a hub that holds a CONNECT its auth service has not answered.
func (p *Proxy) HoldConnectAnswers() *ConnectHold {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cut = false
	if p.hold == nil {
		p.hold = &ConnectHold{held: make(chan struct{}), ended: make(chan struct{}), release: make(chan struct{}),
			waiting: make(chan struct{}, 64), passed: make(chan string, 64)}
	}
	return p.hold
}

// ProxyOption configures a Proxy.
type ProxyOption func(*Proxy)

// ProxyLatency holds every chunk read from either side for half of rtt before writing it on, in
// the order it was read, so a connection through the proxy sees rtt added to each round trip and
// nothing else.
func ProxyLatency(rtt time.Duration) ProxyOption {
	return func(p *Proxy) { p.delay = rtt / 2 }
}

// ProxyRate reads each direction of each connection at about bytesPerSecond, so a sender's socket
// backs up as it does behind a slow link: the throttle is on the read, so the proxy's queue never
// holds more than one read ahead of the rate, and the sender, not the proxy, buffers the rest.
func ProxyRate(bytesPerSecond int) ProxyOption {
	return func(p *Proxy) { p.rate = bytesPerSecond }
}

// StartProxy relays to the NATS server at target, a client URL such as Start returns, on a
// loopback port of its own, until the test ends. URL is the same server through the proxy.
func StartProxy(t testing.TB, target string, options ...ProxyOption) *Proxy {
	t.Helper()
	server, err := url.Parse(target)
	if err != nil || server.Host == "" {
		t.Fatalf("read the NATS URL %q the proxy relays to: %v", target, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the NATS proxy: %v", err)
	}
	proxied := *server
	proxied.Host = listener.Addr().String()
	p := &Proxy{
		listener: listener,
		target:   server.Host,
		url:      proxied.String(),
		done:     make(chan struct{}),
		conns:    map[net.Conn]struct{}{},
	}
	for _, option := range options {
		option(p)
	}
	go p.serve()
	t.Cleanup(p.close)
	return p
}

// URL is the NATS URL of the proxy's address: the target's, credentials included, with the host
// and port the proxy listens on.
func (p *Proxy) URL() string { return p.url }

// Cut closes every connection the proxy is relaying and every one it accepts until Restore, as a
// dial the network refuses ends: a client through it is disconnected at once, and each of its
// reconnects fails.
func (p *Proxy) Cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cut = true
	for conn := range p.conns {
		_ = conn.Close()
	}
	clear(p.conns)
}

// Freeze stops the proxy moving bytes in either direction until Restore, and keeps every
// connection open: a client through it stays connected while nothing it sends reaches the server
// and nothing the server sends reaches it, as on a network that silently drops packets. What either
// side sent meanwhile is delivered once the proxy is restored, as TCP retransmits it.
func (p *Proxy) Freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate == nil {
		p.gate = make(chan struct{})
	}
}

// Restore ends a Cut, a Freeze and a hold of CONNECT answers: the proxy accepts and relays
// connections again, and the bytes a freeze or a hold kept move on.
func (p *Proxy) Restore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cut = false
	if p.gate != nil {
		close(p.gate)
		p.gate = nil
	}
	if p.hold != nil {
		close(p.hold.release)
		p.hold = nil
	}
}

// serve relays each connection the proxy accepts until it closes.
func (p *Proxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.join(client)
	}
}

// join dials the server for client and relays the two into each other, or closes client at once
// while the proxy is cut or when the dial fails. A connection accepted while CONNECT answers are
// held (HoldConnectAnswers) is relayed under that hold.
func (p *Proxy) join(client net.Conn) {
	if !p.track(client) {
		return
	}
	server, err := net.DialTimeout("tcp", p.target, dialTimeout)
	if err != nil || !p.track(server) {
		if err == nil {
			_ = server.Close()
		}
		p.untrack(client)
		return
	}
	p.mu.Lock()
	hold := p.hold
	p.mu.Unlock()
	// pair is closed once either direction of this connection has ended, so the direction that
	// holds the server's answer is released by its own connection's end, never by another's.
	pair := &relayPair{done: make(chan struct{})}
	go p.relay(server, client, hold, pair, false)
	p.relay(client, server, hold, pair, true)
}

// relayPair is one relayed connection's two directions: done is closed once either has ended.
type relayPair struct {
	done chan struct{}
	once sync.Once
}

func (r *relayPair) end() { r.once.Do(func() { close(r.done) }) }

// track records conn as relayed and reports true, or closes it and reports false while the proxy
// is cut or closed.
func (p *Proxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		_ = conn.Close()
		return false
	default:
	}
	if p.cut {
		_ = conn.Close()
		return false
	}
	p.conns[conn] = struct{}{}
	return true
}

func (p *Proxy) untrack(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
		delete(p.conns, conn)
	}
}

// proxyReadBuffer is the most one read of a relayed connection takes. With ProxyRate the reader
// forwards each read before it sleeps for it, so a direction can run up to proxyReadBuffer bytes
// ahead of its rate.
const proxyReadBuffer = 64 << 10

// relay copies src to dst, each chunk written the proxy's delay after it was read and each read
// held to the proxy's rate, until either ends; then it closes both, which ends the other direction
// too. Under a hold (hold not nil), toClient says the direction: the server's bytes after the end
// of its INFO line wait for the hold's release, and the end of the client's first line, when that
// line is its CONNECT, marks the hold held. Both are found across reads, since a TCP read need not
// hold a whole line or its CRLF.
func (p *Proxy) relay(dst, src net.Conn, hold *ConnectHold, pair *relayPair, toClient bool) {
	defer p.untrack(dst, src)
	defer pair.end()
	held := false
	if hold != nil {
		defer func() {
			if held {
				hold.endedOnce.Do(func() { close(hold.ended) })
			}
		}()
	}
	type chunk struct {
		data []byte
		due  time.Time
	}
	queue := make(chan chunk, 4096)
	go func() {
		defer close(queue)
		buffer := make([]byte, proxyReadBuffer)
		for {
			p.pass()
			read, err := src.Read(buffer)
			if read > 0 {
				queue <- chunk{data: bytes.Clone(buffer[:read]), due: time.Now().Add(p.delay)}
				if p.rate > 0 {
					time.Sleep(time.Duration(read) * time.Second / time.Duration(p.rate))
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// firstLine is the direction's first protocol line so far (the server's INFO, the client's
	// CONNECT), firstDone whether its CRLF has passed, and prev the last byte relayed before it ended.
	var firstLine []byte
	firstDone, prev := false, byte(0)
	for c := range queue {
		time.Sleep(time.Until(c.due))
		p.pass()
		data := c.data
		if hold != nil && !firstDone {
			end := lineEnd(prev, data)
			if end < 0 {
				firstLine = append(firstLine, data...)
				prev = data[len(data)-1]
				if _, err := dst.Write(data); err != nil {
					break
				}
				continue
			}
			firstLine = append(firstLine, data[:end]...)
			if _, err := dst.Write(data[:end]); err != nil {
				break
			}
			firstDone, data = true, data[end:]
			if !toClient && bytes.HasPrefix(firstLine, []byte("CONNECT ")) {
				held = true
				hold.heldOnce.Do(func() { close(hold.held) })
			}
		}
		if len(data) == 0 {
			continue
		}
		if hold != nil && toClient {
			if reason, waited := hold.wait(pair.done, p.done); waited {
				select {
				case hold.passed <- reason:
				default:
				}
			}
		}
		if _, err := dst.Write(data); err != nil {
			// The reader ends once src is closed below.
			break
		}
	}
	_ = src.Close()
	for range queue {
	}
}

// wait holds a connection's answer until the hold's release, its own connection's end (own) or the
// proxy's (closed), and reports which, and whether it had to wait: an end already there passes at
// once with no report. A wait that blocks reports so first (waiting), so a row sees the answer held
// before the proxy could write it.
func (h *ConnectHold) wait(own, closed <-chan struct{}) (string, bool) {
	ends := func(block bool) string {
		if !block {
			select {
			case <-h.release:
				return "release"
			case <-own:
				return "own end"
			case <-closed:
				return "proxy closed"
			default:
				return ""
			}
		}
		select {
		case <-h.release:
			return "release"
		case <-own:
			return "own end"
		case <-closed:
			return "proxy closed"
		}
	}
	if reason := ends(false); reason != "" {
		return reason, false
	}
	select {
	case h.waiting <- struct{}{}:
	default:
	}
	return ends(true), true
}

// lineEnd is the index just past the CRLF that ends a line in data, -1 when data holds none; prev
// is the byte relayed just before data, so a CRLF a read split ends the line at index 1.
func lineEnd(prev byte, data []byte) int {
	if prev == '\r' && len(data) > 0 && data[0] == '\n' {
		return 1
	}
	if i := bytes.Index(data, []byte("\r\n")); i >= 0 {
		return i + 2
	}
	return -1
}

// pass waits while the proxy is frozen, and returns at once while it is not or once it closes.
func (p *Proxy) pass() {
	p.mu.Lock()
	gate := p.gate
	p.mu.Unlock()
	if gate == nil {
		return
	}
	select {
	case <-gate:
	case <-p.done:
	}
}

// close stops listening, closes every relayed connection and releases every copy a freeze holds.
func (p *Proxy) close() {
	p.closing.Do(func() {
		close(p.done)
		_ = p.listener.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for conn := range p.conns {
			_ = conn.Close()
		}
		clear(p.conns)
	})
}
