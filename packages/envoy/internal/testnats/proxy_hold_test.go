package testnats

import (
	"bufio"
	"io"
	"net"
	"net/url"
	"testing"
	"time"
)

// A hold of the server's CONNECT answer (HoldConnectAnswers) finds the INFO and CONNECT lines
// across TCP reads: a client whose CONNECT arrives in two reads, split inside the word, still marks
// the hold held, and the server's answer after its INFO line waits for Restore. The row's server is
// a plain TCP listener, so the proxy relays exactly the bytes the row writes. It decides by the
// hold's events: the proxy reports the answer waiting before it writes it, so the client has read
// nothing at that moment, and the answer reaches the client once the row calls Restore.
//
// The control is the hold matching "CONNECT " inside one read: Held never closes.
func TestAConnectHoldFindsItsLinesAcrossReads(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	proxy := StartProxy(t, "nats://"+listener.Addr().String())
	t.Cleanup(proxy.Restore)
	hold := proxy.HoldConnectAnswers()
	address, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatalf("parse the proxy's URL %q: %v", proxy.URL(), err)
	}
	client, err := net.Dial("tcp", address.Host)
	if err != nil {
		t.Fatalf("dial the proxy: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept the proxy's dial: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	if _, err := server.Write([]byte("INFO {}\r\n")); err != nil {
		t.Fatalf("write INFO: %v", err)
	}
	read := bufio.NewReader(client)
	if line, err := read.ReadString('\n'); err != nil || line != "INFO {}\r\n" {
		t.Fatalf("the client read %q, %v, want the server's INFO line", line, err)
	}
	if _, err := client.Write([]byte("CON")); err != nil {
		t.Fatalf("write the CONNECT's first part: %v", err)
	}
	// The proxy relays the first part before the rest is written, so its two reads are two: the
	// server reads "CON" first.
	first := make([]byte, 3)
	if _, err := io.ReadFull(server, first); err != nil || string(first) != "CON" {
		t.Fatalf("the server read %q, %v, want the CONNECT's first part alone", first, err)
	}
	if _, err := client.Write([]byte("NECT {}\r\n")); err != nil {
		t.Fatalf("write the CONNECT's rest: %v", err)
	}
	select {
	case <-hold.Held():
	case <-time.After(Remaining(t)):
		t.Fatal("a CONNECT split across two reads had not marked the hold held by the test's deadline")
	}
	if _, err := server.Write([]byte("+OK\r\n")); err != nil {
		t.Fatalf("write the answer: %v", err)
	}
	answered := make(chan string, 1)
	go func() {
		line, _ := read.ReadString('\n')
		answered <- line
	}()
	select {
	case <-hold.waiting:
	case <-time.After(Remaining(t)):
		t.Fatal("the server's answer never waited for the hold by the test's deadline")
	}
	select {
	case line := <-answered:
		t.Fatalf("the client read %q while the hold held the server's answer", line)
	default:
	}
	proxy.Restore()
	select {
	case line := <-answered:
		if line != "+OK\r\n" {
			t.Fatalf("the client read %q once the hold ended, want the server's answer", line)
		}
	case <-time.After(Remaining(t)):
		t.Fatal("the server's answer never reached the client after Restore")
	}
}

// startHeldConnection makes one connection through proxy to listener, relays the server's INFO and
// the client's CONNECT, and writes the server's answer, which the hold keeps: it waits for the
// hold's report that the answer waits. It returns the client's side.
func startHeldConnection(t *testing.T, proxy *Proxy, listener net.Listener, hold *ConnectHold) net.Conn {
	t.Helper()
	address, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatalf("parse the proxy's URL %q: %v", proxy.URL(), err)
	}
	client, err := net.Dial("tcp", address.Host)
	if err != nil {
		t.Fatalf("dial the proxy: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept the proxy's dial: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if _, err := server.Write([]byte("INFO {}\r\n")); err != nil {
		t.Fatalf("write INFO: %v", err)
	}
	if line, err := bufio.NewReader(client).ReadString('\n'); err != nil || line != "INFO {}\r\n" {
		t.Fatalf("the client read %q, %v, want the server's INFO line", line, err)
	}
	if _, err := client.Write([]byte("CONNECT {}\r\n")); err != nil {
		t.Fatalf("write the CONNECT: %v", err)
	}
	connect := make([]byte, len("CONNECT {}\r\n"))
	if _, err := io.ReadFull(server, connect); err != nil {
		t.Fatalf("the server read %q, %v, want the CONNECT", connect, err)
	}
	if _, err := server.Write([]byte("+OK\r\n")); err != nil {
		t.Fatalf("write the answer: %v", err)
	}
	select {
	case <-hold.waiting:
	case <-time.After(Remaining(t)):
		t.Fatal("the server's answer never waited for the hold by the test's deadline")
	}
	return client
}

// A hold keeps each connection's answer until Restore or that connection's own end, never until
// another's end: the first held connection's client closes, so its wait ends by its own end and
// Ended closes; a connection the proxy accepts after that is held all the same, and its wait ends
// only at Restore. It decides by the hold's events: a wait that must block reports so before it
// blocks (ConnectHold.waiting), and each blocked wait reports why it ended (ConnectHold.passed).
//
// The control is each wait ending at Ended, the first held connection's end, as the hold did
// before it was made per connection: the later connection's answer passes at once, never waits,
// and the row fails at the test's deadline.
func TestAConnectHoldHoldsEachConnectionUntilRestoreOrItsOwnEnd(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	proxy := StartProxy(t, "nats://"+listener.Addr().String())
	t.Cleanup(proxy.Restore)
	hold := proxy.HoldConnectAnswers()
	first := startHeldConnection(t, proxy, listener, hold)

	_ = first.Close()
	next := func(what string) string {
		t.Helper()
		select {
		case reason := <-hold.passed:
			return reason
		case <-time.After(Remaining(t)):
			t.Fatalf("%s: no held answer's wait ended by the test's deadline", what)
		}
		return ""
	}
	if reason := next("the first connection's end"); reason != "own end" {
		t.Fatalf("the first held answer's wait ended by %q, want its own connection's end", reason)
	}
	select {
	case <-hold.Ended():
	case <-time.After(Remaining(t)):
		t.Fatal("Ended never closed after the first held connection ended")
	}
	startHeldConnection(t, proxy, listener, hold)
	proxy.Restore()
	if reason := next("Restore"); reason != "release" {
		t.Fatalf("the later held answer's wait ended by %q, want Restore's release", reason)
	}
}
