//go:build e2e

package daemon

import (
	"bufio"
	"net"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSessionsNATSURLNamesNatsdsOwnAddress holds that the URL part two's setup hands the Envoy
// listener, the scratch Dispatch and the daemon (startSessionsNATS) reaches the natsd it started:
// a dial of its host and port is answered by a NATS INFO line. natsd with no -a claims a loopback
// address of its own, so on Linux that host is a 127.a.b.c other than 127.0.0.1; a URL built on
// 127.0.0.1 reaches nothing there, or another stack's server that holds the port.
func TestSessionsNATSURLNamesNatsdsOwnAddress(t *testing.T) {
	raw := startSessionsNATS(t, t.TempDir())
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("startSessionsNATS gave %q, which does not parse: %v", raw, err)
	}
	if runtime.GOOS == "linux" && parsed.Hostname() == "127.0.0.1" {
		t.Fatalf("startSessionsNATS gave %s, on 127.0.0.1: natsd with no -a listens on an address of its own on Linux", raw)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 10*time.Second)
	if err != nil {
		t.Fatalf("startSessionsNATS gave %s, and nothing answers there: %v", raw, err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "INFO ") {
		t.Fatalf("startSessionsNATS gave %s, and what answers there sent %q (%v), not a NATS INFO line", raw, line, err)
	}
}
