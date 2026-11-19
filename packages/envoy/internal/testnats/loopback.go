package testnats

import (
	"crypto/rand"
	"fmt"
	"net"
)

// Loopback is the address every listener of this package opens on, a server's client, route and
// monitoring listeners and every relay, proxy and gate in front of one, but for a callout server's
// relay, which takes an address of its own (calloutHost): a loopback address of this test binary's
// own, 127.a.b.c with a, b and c drawn when the binary starts, or 127.0.0.1 where the system
// serves no other loopback address (Linux serves all of 127.0.0.0/8).
//
// A port this package held and let go is dialed by nobody of this binary, but it is dialed by a
// client of another test binary whose test ended without closing it: nats.go reconnects to the URL
// it was handed for as long as its process lives, and a bus client that owns the stream ensures
// ENVOY_NOTIFICATIONS on every server it reaches. On a box where many test binaries run at once,
// the system hands the same port on 127.0.0.1 to the next binary that asks, and such a client
// then reached that binary's fresh cluster: a probe of 3,200 fresh three-member clusters, 32
// binaries at a time, found a client named "envoy" that this binary never made in 10 of them, 9
// of those with ENVOY_NOTIFICATIONS already created, and a test that deployed the stream itself
// failed "nats: stream name already in use". A client of another binary knows only addresses its
// own binary gave it, on 127.0.0.1 for a binary built before this address or on that binary's own
// 127.a.b.c, so it reaches none of this binary's listeners.
var Loopback = ownLoopback()

// ownLoopback draws this binary's loopback address: one this process can listen on, and never
// 127.0.0.1 or another address in 127.0.0.0/16, where a binary of an earlier build listens.
func ownLoopback() string {
	for range 8 {
		var b [3]byte
		if _, err := rand.Read(b[:]); err != nil {
			break
		}
		host := fmt.Sprintf("127.%d.%d.%d", 1+int(b[0])%254, b[1], 1+int(b[2])%254)
		listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err == nil {
			_ = listener.Close()
			return host
		}
	}
	return "127.0.0.1"
}

// Listen opens a TCP listener on a port the system picks at Loopback: the listener of every test
// relay, proxy or fake server another test binary's client could otherwise reach, in front of a
// NATS server or a database or in place of one, so no client of another test binary reaches it.
func Listen() (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(Loopback, "0"))
}

// loopback4 is Loopback as the four bytes of an IPv4 socket address, for a socket this package
// binds itself (holdGatePort).
func loopback4() [4]byte {
	var address [4]byte
	copy(address[:], net.ParseIP(Loopback).To4())
	return address
}
