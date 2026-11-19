package testnats

import (
	"net"

	"github.com/sjawhar/envoy/internal/testnats/loopback"
)

// NewHost is the loopback address a new NATS server of this process listens on, with every
// listener in front of it: for a server this package starts, its relay, its client and monitoring
// ports, a cluster member's route port and the route relay before it (StartClusterConfig), and the
// gates and proxies a test puts before it (StartGate, StartProxy); for a nats-server a test runs as
// a process of its own, every port it gives that process (infra/gcp's live hubs). It is chosen once
// per server and kept through every stop, start and reload, since the server's configuration and
// its URL name it. It is loopback.NewHost's: on Linux, where every address of 127.0.0.0/8 is the
// loopback, one at random from 127.1.0.1 through 127.254.255.254, never one this process gave
// before, so no two servers of one test binary ever share an address. Elsewhere it is 127.0.0.1,
// the only loopback address such a system brings up by default, and every server there shares it.
// A script's own stack takes its server's address from the same rule: natsd picks one, and
// cmd/loopbackhost prints one for a nats-server binary or container a script runs.
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
	return loopback.NewHost()
}

// hostOf is the host of address, a host:port, for a listener put in front of the server there
// (StartGate, StartProxy), or "" when address is no host:port.
func hostOf(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return host
}
