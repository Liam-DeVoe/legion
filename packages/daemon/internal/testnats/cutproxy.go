package testnats

import (
	"io"
	"net"
	"sync"
	"testing"
)

// CutProxy forwards every connection to its address (Addr) on to an upstream NATS server. Drop
// closes the connections forwarded so far and forwards new ones; Cut also refuses new ones, closing
// each connection it accepts at once, so a client stays disconnected until Restore.
type CutProxy struct {
	// Addr is the proxy's host:port on loopback.
	Addr    string
	mu      sync.Mutex
	conns   []net.Conn
	refused bool
}

// NewCutProxy is a proxy to upstream (host:port), closed with t.
func NewCutProxy(t testing.TB, upstream string) *CutProxy {
	t.Helper()
	ln, err := ListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	p := &CutProxy{Addr: ln.Addr().String()}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			refused := p.refused
			p.mu.Unlock()
			if refused {
				client.Close()
				continue
			}
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, client, server)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(server, client); server.Close() }()
			go func() { _, _ = io.Copy(client, server); client.Close() }()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		p.Drop()
	})
	return p
}

// Drop closes every connection forwarded so far, and keeps forwarding new ones.
func (p *CutProxy) Drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

// Cut drops every connection and refuses new ones until Restore.
func (p *CutProxy) Cut() {
	p.mu.Lock()
	p.refused = true
	p.mu.Unlock()
	p.Drop()
}

// Restore forwards new connections again.
func (p *CutProxy) Restore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused = false
}
