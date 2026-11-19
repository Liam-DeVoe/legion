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
// a plain TCP listener, so the proxy relays exactly the bytes the row writes.
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
	case line := <-answered:
		t.Fatalf("the client read %q while the hold held the server's answer", line)
	case <-time.After(200 * time.Millisecond):
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
