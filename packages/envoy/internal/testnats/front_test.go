package testnats

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// greeter is a TCP server on a loopback port of its own that writes its name to each connection it
// accepts and then echoes what the connection sends, and hands the test its side of each one.
type greeter struct {
	name     string
	listener net.Listener
	accepted chan net.Conn
}

func startGreeter(t *testing.T, name string) *greeter {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for %s: %v", name, err)
	}
	g := &greeter{name: name, listener: listener, accepted: make(chan net.Conn, 8)}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			g.accepted <- conn
			go func() {
				defer conn.Close()
				if _, err := conn.Write([]byte(name)); err != nil {
					return
				}
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return g
}

// readFor reads exactly len(want) bytes from conn within 2 s and fails the test unless they are
// want.
func readFor(t *testing.T, conn net.Conn, want, what string) {
	t.Helper()
	got := make([]byte, len(want))
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != want {
		t.Fatalf("%s: read %q, %v; want %q", what, got, err, want)
	}
}

// readsEOF fails the test unless conn ends within 2 s with nothing more to read.
func readsEOF(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Read(make([]byte, 1))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("%s: read %d bytes, %v; want the connection closed (EOF)", what, n, err)
	}
}

// A front holds one address for the test's life and relays each connection to wherever its target
// says the server listens as the connection arrives: a connection made while the target names no
// server is closed at once; one made after the target moves reaches the new server, while one the
// front relays already keeps its server until that server drops it, and then the client's side is
// closed too.
func TestAFrontRelaysToWhereverItsServerListensAndClosesWhileItListensNowhere(t *testing.T) {
	var mu sync.Mutex
	address := ""
	front := StartFront(t, func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if address == "" {
			return "", errors.New("the server is not listening")
		}
		return address, nil
	})
	point := func(to string) {
		mu.Lock()
		address = to
		mu.Unlock()
	}
	dial := func() net.Conn {
		t.Helper()
		conn, err := net.DialTimeout("tcp", front.Addr(), time.Second)
		if err != nil {
			t.Fatalf("dial the front at %s: %v", front.Addr(), err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	readsEOF(t, dial(), "a connection while the front names no server")

	first := startGreeter(t, "first")
	point(first.listener.Addr().String())
	kept := dial()
	readFor(t, kept, "first", "a connection once the front names the first server")
	firstSide := <-first.accepted

	second := startGreeter(t, "second")
	point(second.listener.Addr().String())
	readFor(t, dial(), "second", "a connection once the front names the second server")
	if _, err := kept.Write([]byte("still")); err != nil {
		t.Fatalf("write on the connection relayed to the first server: %v", err)
	}
	readFor(t, kept, "still", "the connection relayed to the first server, after the front moved")

	_ = firstSide.Close()
	readsEOF(t, kept, "the connection relayed to the first server, after that server dropped it")

	point("")
	readsEOF(t, dial(), "a connection once the front names no server again")
}
