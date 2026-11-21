package main

// edge_forwarded.go is how an edge listener reads its caller's address. The edge is reached
// through Google's global external Application Load Balancer, so every request comes from a
// Google Front End proxy: the connection's own address (r.RemoteAddr) names that proxy, which many
// callers share, and one caller's requests come from many proxies. The broker-token credential
// keys its introspection bucket on a caller's address (brokertoken.DefaultAddressLimit), and a
// bucket keyed on the proxy would let a stranger's junk tokens refuse every caller behind it
// while bounding no one caller.
//
// Google writes the client's address into X-Forwarded-For: the balancer appends
// "<client address>,<load balancer address>" to whatever the client sent, as one line
// (https://cloud.google.com/load-balancing/docs/https#x-forwarded-for_header), and its proxies
// connect from 35.191.0.0/16 and 130.211.0.0/22
// (https://cloud.google.com/load-balancing/docs/firewall-rules). The rule is the one the secrets
// broker's own edge reads its caller by, held to those ranges: a request from one of them is keyed
// on the second-to-last entry of its one X-Forwarded-For line, an IPv4 address or the /64 an IPv6
// address is in, and is refused 400 when it carries no such line; a request from any other peer is
// keyed on its connection, as before, and its forwarded headers choose nothing.

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

// googleFrontEnds are the ranges Google's load balancer proxies and health checkers connect from.
var googleFrontEnds = []netip.Prefix{
	netip.MustParsePrefix("35.191.0.0/16"),
	netip.MustParsePrefix("130.211.0.0/22"),
}

// edgeForwardedFor is the header Google's load balancer writes the client's address into.
const edgeForwardedFor = "X-Forwarded-For"

// edgeForwardedRefusal is the 400 an edge listener answers a request from Google's proxies that
// carries no address the balancer forwarded.
const edgeForwardedRefusal = "the edge listener reads its caller's address from the second-to-last entry of one " +
	edgeForwardedFor + " line, the entry Google's load balancer appends, and this request carries no such line"

// The ways a request from Google's proxies carries no forwarded address (edgeCallerAddress).
var (
	errForwardedLines   = errors.New("the request carries other than one " + edgeForwardedFor + " line")
	errForwardedEntries = errors.New("its " + edgeForwardedFor + " line holds fewer than two entries")
	errForwardedAddress = errors.New("the second-to-last entry of its " + edgeForwardedFor + " line is no IP address")
)

// edgeCallerAddress is the address an edge listener keys r's caller on, and whether Google's load
// balancer forwarded it. For a connection from googleFrontEnds it is the second-to-last entry of
// r's one X-Forwarded-For line, as forwardedKey writes it, and an error when r carries no such
// line: none, more than one (Google documents one, and which of several it would append to is not
// documented, so a line the client sent could be the one read), a line of fewer than two entries,
// or an entry there that is no IP address. The last entry is the balancer's own address, the same
// for every caller, and every entry before the second-to-last is the client's own text. For any
// other connection it is "" and false: that peer did not come through the balancer, so what it
// wrote in the header is its own and chooses nothing, and listenerSourceAddress keys it on the
// connection.
func edgeCallerAddress(r *http.Request) (string, bool, error) {
	if !fromGoogleFrontEnd(r.RemoteAddr) {
		return "", false, nil
	}
	lines := r.Header.Values(edgeForwardedFor)
	if len(lines) != 1 {
		return "", false, errForwardedLines
	}
	hops := strings.Split(lines[0], ",")
	if len(hops) < 2 {
		return "", false, errForwardedEntries
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-2]))
	if err != nil {
		return "", false, errForwardedAddress
	}
	return forwardedKey(addr), true, nil
}

// fromGoogleFrontEnd reports whether remote, a connection's address as net/http writes it
// (host:port), is in googleFrontEnds.
func fromGoogleFrontEnd(remote string) bool {
	peer, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	addr := canonicalAddr(peer.Addr())
	for _, prefix := range googleFrontEnds {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// forwardedKey is the bucket key of a caller forwarded from addr: the IPv4 address it is, an IPv4
// address written as IPv6 (::ffff:a.b.c.d) included, and for IPv6 the /64 holding it, since one
// host or one site holds a whole /64 and could take a fresh bucket from each of its addresses.
func forwardedKey(addr netip.Addr) string {
	addr = canonicalAddr(addr)
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}

// canonicalAddr is addr as the edge compares and keys it: an IPv4-mapped address is the IPv4
// address it holds, which netip.Prefix.Contains would otherwise not find in an IPv4 range, and a
// zone is dropped, for which Contains is false whatever the range. It is the broker's own reading
// (internal/broker/netaddr.Canonical), written here so the listener's build reaches no broker
// package for one line.
func canonicalAddr(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

// callerAddressKey is the context key the address apiAuth read for a request's caller is carried
// under (withCallerAddress).
type callerAddressKey struct{}

// withCallerAddress carries the address an edge listener read for a request's caller
// (edgeCallerAddress) to the introspection bucket (listenerSourceAddress). apiAuth sets it once,
// where it refuses a request it reads no address from, so the address that decides the refusal is
// the one the bucket keys on.
func withCallerAddress(ctx context.Context, address string) context.Context {
	return context.WithValue(ctx, callerAddressKey{}, address)
}
