package testdb

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// RefusedAddress is a TCP address on 127.0.0.1 nothing listens on any more, so a dial of it is
// refused by the host, as a database that is down refuses one.
func RefusedAddress(t testing.TB) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// Refusing is the address of a stand-in for a Postgres server that answers every connection's
// startup with a FATAL ErrorResponse of SQLSTATE code and message, then closes it, as a real server
// refuses a connection it cannot take: one starting up or shutting down (57P03), one with no client
// slot left (53300), or a login it does not take (28P01). An SSL or GSS encryption request before
// the startup is declined, as a server without TLS declines it. It speaks nothing of the protocol
// past that first answer, and its listener closes when the test ends.
func Refusing(t testing.TB, code, message string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fields := []byte("SFATAL\x00VFATAL\x00C" + code + "\x00M" + message + "\x00\x00")
	answer := binary.BigEndian.AppendUint32([]byte{'E'}, uint32(4+len(fields)))
	answer = append(answer, fields...)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				for {
					var size [4]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil || binary.BigEndian.Uint32(size[:]) < 8 {
						return
					}
					body := make([]byte, binary.BigEndian.Uint32(size[:])-4)
					if _, err := io.ReadFull(conn, body); err != nil {
						return
					}
					// 80877103 is an SSLRequest's code and 80877104 a GSSENCRequest's; the startup
					// message comes next.
					if request := binary.BigEndian.Uint32(body); len(body) == 4 && (request == 80877103 || request == 80877104) {
						if _, err := conn.Write([]byte{'N'}); err != nil {
							return
						}
						continue
					}
					_, _ = conn.Write(answer)
					return
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		wg.Wait()
	})
	return listener.Addr().String()
}

// Breaking is dsn reached through a TCP proxy on 127.0.0.1 that ends the first connection whose
// client sends marker, at that moment, as a session store that restarts or fails over ends the
// connections it holds: the client's end is reset (RST) when reset is set, and otherwise closed as
// a server that hangs up closes it (an end of file). What the client sent with marker never reaches
// the server. Every other connection, and every one after the one it ended, passes through as it
// is, so the store answers again at once. The proxy and every connection through it close when the
// test ends, before the database is dropped (Database's cleanup is registered first, so it runs
// after this one). The URL it answers is rewriteDSN's, as Delayed's is, for the reason Delayed
// gives.
func Breaking(t testing.TB, dsn, marker string, reset bool) string {
	t.Helper()
	const named = "the database URL to break"
	target, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx refuses %s", named)
	}
	upstream := net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the breaking Postgres proxy: %v", err)
	}
	address := listener.Addr().(*net.TCPAddr)
	through, err := rewriteDSN(named, dsn, rewrite{host: address.IP.String(), port: uint16(address.Port)})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	proxy := &breakingProxy{listener: listener, upstream: upstream, marker: []byte(marker), reset: reset, conns: map[net.Conn]struct{}{}}
	proxy.wg.Add(1)
	go proxy.serve()
	t.Cleanup(proxy.close)
	return through
}

// breakingProxy is Breaking's proxy: broken is whether it has ended its one connection, and conns
// are the client and server connections open through it, closed with it.
type breakingProxy struct {
	listener net.Listener
	upstream string
	marker   []byte
	reset    bool

	mu     sync.Mutex
	closed bool
	broken bool
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

func (p *breakingProxy) serve() {
	defer p.wg.Done()
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.upstream)
		if err != nil {
			_ = client.Close()
			continue
		}
		if !p.track(client, server) {
			return
		}
		p.wg.Add(2)
		go func() {
			defer p.wg.Done()
			_, _ = io.Copy(client, server)
			p.untrack(client, server)
		}()
		go func() {
			defer p.wg.Done()
			p.relay(client, server)
			p.untrack(client, server)
		}()
	}
}

// relay copies client to server until the client sends marker on the first connection to send it,
// which ends that pair instead. A marker split between two reads is still seen: each read is
// searched together with the end of the one before it.
func (p *breakingProxy) relay(client, server net.Conn) {
	buffer := make([]byte, 64<<10)
	var tail []byte
	for {
		n, err := client.Read(buffer)
		if n > 0 {
			seen := append(tail, buffer[:n]...)
			if bytes.Contains(seen, p.marker) && p.breakOnce() {
				if tcp, ok := client.(*net.TCPConn); ok && p.reset {
					_ = tcp.SetLinger(0)
				}
				return
			}
			if _, err := server.Write(buffer[:n]); err != nil {
				return
			}
			tail = bytes.Clone(seen[max(0, len(seen)-len(p.marker)+1):])
		}
		if err != nil {
			return
		}
	}
}

// breakOnce reports whether this is the first connection to send the marker, and marks it so.
func (p *breakingProxy) breakOnce() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken {
		return false
	}
	p.broken = true
	return true
}

// track records a pair of connections, and closes them instead when the proxy is already closed.
func (p *breakingProxy) track(client, server net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = client.Close()
		_ = server.Close()
		return false
	}
	p.conns[client], p.conns[server] = struct{}{}, struct{}{}
	return true
}

// untrack closes a pair of connections, once either direction ends.
func (p *breakingProxy) untrack(client, server net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, client)
	delete(p.conns, server)
	_ = client.Close()
	_ = server.Close()
}

func (p *breakingProxy) close() {
	p.mu.Lock()
	p.closed = true
	_ = p.listener.Close()
	for conn := range p.conns {
		_ = conn.Close()
	}
	clear(p.conns)
	p.mu.Unlock()
	p.wg.Wait()
}
