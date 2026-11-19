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
// until Restore or until the client's own connect timeout ends the attempt.
type ConnectHold struct {
	held, ended, release chan struct{}
	heldOnce, endedOnce  sync.Once
}

// Held is closed once a client's CONNECT has reached the server through the hold, so its
// handshake is waiting for the answer the hold keeps.
func (h *ConnectHold) Held() <-chan struct{} { return h.held }

// Ended is closed once a connection the hold holds has closed: the client gave its handshake up,
// at its connect timeout, or the proxy cut it.
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
		p.hold = &ConnectHold{held: make(chan struct{}), ended: make(chan struct{}), release: make(chan struct{})}
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
	go p.relay(server, client, hold, false)
	p.relay(client, server, hold, true)
}

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
// too. Under a hold (hold not nil), toClient says the direction: the server's chunks after the
// line of its INFO wait for the hold's release, and the client's CONNECT marks the hold held.
func (p *Proxy) relay(dst, src net.Conn, hold *ConnectHold, toClient bool) {
	defer p.untrack(dst, src)
	if hold != nil {
		defer hold.endedOnce.Do(func() { close(hold.ended) })
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
	infoPassed := false
	for c := range queue {
		time.Sleep(time.Until(c.due))
		p.pass()
		if hold != nil && toClient && infoPassed {
			select {
			case <-hold.release:
			case <-hold.ended:
			case <-p.done:
			}
		}
		if _, err := dst.Write(c.data); err != nil {
			// The reader ends once src is closed below.
			break
		}
		if hold != nil && toClient && bytes.Contains(c.data, []byte("\r\n")) {
			infoPassed = true
		}
		if hold != nil && !toClient && bytes.Contains(c.data, []byte("CONNECT ")) {
			hold.heldOnce.Do(func() { close(hold.held) })
		}
	}
	_ = src.Close()
	for range queue {
	}
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
