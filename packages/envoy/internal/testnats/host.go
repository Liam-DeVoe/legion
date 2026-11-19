package testnats

import (
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// NewHost is the loopback address a new NATS server of this process listens on, with every
// listener in front of it: for a server this package starts, its relay, its client and monitoring
// ports, a cluster member's route port and the route relay before it (StartClusterConfig), and the
// gates and proxies a test puts before it (StartGate, StartProxy); for a nats-server a test runs as
// a process of its own, every port it gives that process (infra/gcp's live hubs). It is chosen once
// per server and kept through every stop, start and reload, since the server's configuration and
// its URL name it. On Linux, where every address of 127.0.0.0/8 is the loopback, it is one at
// random from 127.1.0.1 through 127.254.255.254, never one this process gave before (serverHosts),
// so no two servers of one test binary ever share an address. Elsewhere it is 127.0.0.1, the only
// loopback address such a system brings up by default, and every server there shares it.
//
// A test counts what its server is sent, and a server answers every client that reaches it. A
// server's relay holds its port only for its own test's life, and the server picks a new client
// port at each start. A client a test leaves behind keeps dialing the address it was given (a bus
// client reconnects forever), and the kernel can hand the same port to a later server, in this
// test binary or in another binary go test runs beside it. In the envoy-go-race step such a client
// reached a callout test's hub twice, each time adding requests the test never sent: first through
// the relay's port while the relay listened on 127.0.0.1, then through the server's own client port
// on 127.0.0.1 once only the relay had an address of its own. A client dials the address its own
// server had, so a server on an address no earlier server of the process had is out of its reach,
// whatever port it gets; one of another binary shares its address only by a draw of one in
// 16,516,096, and then needs the same port too.
func NewHost() string {
	if runtime.GOOS != "linux" {
		return "127.0.0.1"
	}
	serverHostsMu.Lock()
	defer serverHostsMu.Unlock()
	for {
		host := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(254), rand.IntN(256), 1+rand.IntN(254))
		if !serverHosts[host] {
			serverHosts[host] = true
			return host
		}
	}
}

// serverHosts is every address NewHost has given in this process, kept for the process's
// life: an address is never given twice, so a client of an ended test never reaches a later one.
var (
	serverHostsMu sync.Mutex
	serverHosts   = map[string]bool{}
)

// hostOf is the host of address, a host:port, for a listener put in front of the server there
// (StartGate, StartProxy), or "" when address is no host:port.
func hostOf(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return host
}

// Listen is a TCP listener for a relay, a proxy or a tap a test puts in front of the server at
// target, a client URL (nats://user:pass@host:port, as Start returns) or a host:port: on a port of
// its own at target's host, the server's own address (NewHost), and never on 127.0.0.1 unless the
// server is there. A relay on 127.0.0.1 is the way back in that a server's own address closes: a
// client of a relay an earlier test closed keeps dialing 127.0.0.1 at that relay's port, and once
// the kernel gives the port to a later relay on 127.0.0.1 the client reaches the later relay's
// server. The listener is closed when the test ends; a caller may close it sooner.
func Listen(t testing.TB, target string) net.Listener {
	t.Helper()
	address := target
	if strings.Contains(target, "://") {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			t.Fatalf("testnats: read the URL %q a listener goes in front of: %v", target, err)
			return nil
		}
		address = parsed.Host
	}
	host := hostOf(address)
	if host == "" {
		t.Fatalf("testnats: %q names no host:port to put a listener in front of", target)
		return nil
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("testnats: listen in front of %s: %v", address, err)
		return nil
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}
