// packages/envoy/internal/cipher/netaddr/dial.go
// Where the airlock proxy may dial for a command (DialRefusal), and the forms of an IPv6 address
// that carry an IPv4 address (carriedIPv4), which both of the package's rules read. The proxy
// (internal/cipher/proxy) resolves a name, holds each of its addresses to DialRefusal, and dials
// the addresses it checked; this file decides the rule and nothing else: no name, socket or log
// line is here.
package netaddr

import "net/netip"

// clusterRange is 100.64.0.0/10, the shared address space of RFC 6598. Kubernetes clusters and
// cloud networks hand node and pod addresses out of it, and netip's own predicates do not name
// it, so it is spelled out here.
var clusterRange = netip.MustParsePrefix("100.64.0.0/10")

// thisNetworkRange is 0.0.0.0/8, which RFC 1122 reserves for "this network". netip's
// IsGlobalUnicast refuses only 0.0.0.0 of it, and Linux routes the rest as unicast since 5.3, so
// it is spelled out here.
var thisNetworkRange = netip.MustParsePrefix("0.0.0.0/8")

// The IPv6 forms that carry an IPv4 address (carriedIPv4). A network with a translator or relay
// for a form sends a packet for such an address to the IPv4 address inside it: a NAT64
// translator rewrites it as a packet to that address, and a 6to4, Teredo or ISATAP relay, or a
// host that still routes the IPv4-compatible form, carries it there inside an IPv4 packet. So
// where the network passes the address on, a tunnel to 64:ff9b::a9fe:a9fe is a connection to
// 169.254.169.254, the node's metadata server. The IPv4-translated form of SIIT names an IPv6
// node rather than an IPv4 destination (RFC 2765 section 2.1), but a translator or host that
// reads its last 32 bits reaches the IPv4 address there, so it is refused the same way. The
// proxy refuses each such address by the address it carries rather than rely on every translator
// and relay to refuse it. netip's predicates read none of these as the address they carry, and
// the IPv4-mapped form is read as its IPv4 address already (Canonical).
var (
	// 64:ff9b::/96, NAT64's well-known prefix (RFC 6052): the IPv4 address is the last 32 bits.
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	// 2002::/16, 6to4 (RFC 3056): the IPv4 address is the 32 bits after the prefix.
	sixToFourPrefix = netip.MustParsePrefix("2002::/16")
	// 2001::/32, Teredo (RFC 4380): the server's IPv4 address is the 32 bits after the prefix,
	// and the client's is the last 32 bits with every bit inverted.
	teredoPrefix = netip.MustParsePrefix("2001::/32")
	// ::/96, the IPv4-compatible form RFC 4291 deprecated: the IPv4 address is the last 32 bits.
	// :: and ::1 are inside it, and both rules refuse each as itself first.
	ipv4CompatiblePrefix = netip.MustParsePrefix("::/96")
	// ::ffff:0:0:0/96, the IPv4-translated form of SIIT (RFC 2765), which RFC 6145 replaced: the
	// IPv4 address is the last 32 bits. It is not the IPv4-mapped ::ffff:0:0/96, which Canonical
	// reads as its IPv4 address.
	ipv4TranslatedPrefix = netip.MustParsePrefix("::ffff:0:0:0/96")
	// 64:ff9b:1::/48, the local-use translation prefix (RFC 8215), which IANA lists as not
	// globally reachable. A network places translation prefixes of its own choosing inside it,
	// for mechanisms of its own choosing, so the address alone does not say where an IPv4 address
	// sits in it, or whether one does; RFC 8215 section 5 forbids a host to assume either. So no
	// reading of the address finds what it carries, and both rules refuse the whole prefix.
	localUseTranslationPrefix = netip.MustParsePrefix("64:ff9b:1::/48")
)

// DialRefusal names the rule that refuses a dial of addr by the airlock proxy for a command - a
// tunnel's, a bound host's request's and a plain-HTTP request's alike - and "" for an address the
// proxy may dial. Each answer names the rule and the address family, because an operator reading
// a refusal has to know which of a name's addresses it was about: a name with one public IPv4
// address and one link-local IPv6 address is refused, and the line says which half did it.
//
// clusterRanges are the broker cluster's own ranges (BROKER_AIRLOCK_CLUSTER_RANGES), read after
// the fixed rules: a Service range such as 34.118.224.0/20 is global unicast, so it is the one
// rule that stands between a tunnel and a ClusterIP of the cluster the broker runs in, and an
// address a fixed rule already refuses keeps that rule's more particular name.
//
// An IPv6 address that carries an IPv4 address (carriedIPv4) is refused by every rule that
// refuses the address it carries, and the answer names that address and its rule, unless a rule
// above already refuses the IPv6 address itself, whose name the answer then keeps.
func DialRefusal(addr netip.Addr, clusterRanges []netip.Prefix) string {
	// A 4-in-6 address is the IPv4 address it holds, for every predicate below and for the
	// family each answer names.
	addr = Canonical(addr)
	family := "IPv6"
	if addr.Is4() {
		family = "IPv4"
	}
	switch {
	case addr.IsLoopback():
		// 127.0.0.0/8 and ::1: the broker's own pod.
		return "a loopback " + family + " address"
	case clusterRange.Contains(addr):
		// 100.64.0.0/10: the cluster's own nodes and pods.
		return "a cluster " + family + " address"
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		// 169.254.0.0/16 and fe80::/10: the node's metadata server among them.
		return "a link-local " + family + " address"
	case addr.IsPrivate():
		// 10/8, 172.16/12 and 192.168/16; fc00::/7, which is unique-local rather than private.
		if family == "IPv6" {
			return "a unique-local IPv6 address"
		}
		return "a private IPv4 address"
	case localUseTranslationPrefix.Contains(addr):
		// 64:ff9b:1::/48: a translator inside the network, whose layout the address does not say.
		return "a local-use translation IPv6 address (64:ff9b:1::/48), whose IPv4 address the address alone does not say"
	case !addr.IsGlobalUnicast():
		// Everything left that is not a host on the public internet: the unspecified address,
		// multicast, the IPv4 broadcast address, and the interface-local scopes.
		return "not a global unicast " + family + " address"
	case thisNetworkRange.Contains(addr):
		// 0.0.0.1 to 0.255.255.255: reserved for this network, and no host on the internet.
		return "an IPv4 address of 0.0.0.0/8, which is reserved for this network"
	}
	for _, own := range clusterRanges {
		if own.Contains(addr) {
			return "an " + family + " address inside BROKER_AIRLOCK_CLUSTER_RANGES, the broker cluster's own addresses"
		}
	}
	held, n := carriedIPv4(addr)
	for _, c := range held[:n] {
		if rule := DialRefusal(c.addr, clusterRanges); rule != "" {
			return c.form + " " + c.addr.String() + ", " + rule
		}
	}
	return ""
}

// carried is an IPv4 address an IPv6 address carries, and the words of a refusal that say how.
// The address is always IPv4, for which carriedIPv4 answers nothing, so DialRefusal's reading of
// a carried address goes one level deep and no further.
type carried struct {
	addr netip.Addr
	form string
}

// carriedIPv4 answers the IPv4 addresses addr carries in one of the forms above or in an ISATAP
// interface identifier, n of them: a Teredo address's server and client, the address one other
// form carries, and beside either the address an ISATAP identifier carries; none for an IPv4
// address or an IPv6 address in no such form. addr is read as Canonical answers it, as both rules
// hand it over, with no zone: a zoned address is inside no prefix (netip.Prefix.Contains), so it
// would carry no address of the prefix forms. An array rather than a slice, since every address
// of every dial is read here.
func carriedIPv4(addr netip.Addr) (held [3]carried, n int) {
	if !addr.Is6() {
		return held, 0
	}
	a := addr.As16()
	at := func(i int) netip.Addr { return netip.AddrFrom4([4]byte(a[i : i+4])) }
	switch {
	case nat64Prefix.Contains(addr):
		held[0] = carried{at(12), "the NAT64 form (64:ff9b::/96) of"}
		n = 1
	case sixToFourPrefix.Contains(addr):
		held[0] = carried{at(2), "the 6to4 form (2002::/16) of"}
		n = 1
	case teredoPrefix.Contains(addr):
		held[0] = carried{at(4), "a Teredo address (2001::/32) whose server is"}
		held[1] = carried{netip.AddrFrom4([4]byte{^a[12], ^a[13], ^a[14], ^a[15]}), "a Teredo address (2001::/32) whose client is"}
		n = 2
	case ipv4CompatiblePrefix.Contains(addr):
		held[0] = carried{at(12), "the IPv4-compatible form (::/96) of"}
		n = 1
	case ipv4TranslatedPrefix.Contains(addr):
		held[0] = carried{at(12), "the IPv4-translated form (::ffff:0:0:0/96) of"}
		n = 1
	}
	// ISATAP (RFC 5214): the interface identifier 0:5efe, or 200:5efe for a globally unique IPv4
	// address, then that address, under any /64 prefix, a 6to4 one among them. So it is read
	// beside the prefixes above, and an address may carry one IPv4 address in each.
	if a[8]&^0x02 == 0 && a[9] == 0 && a[10] == 0x5e && a[11] == 0xfe {
		held[n] = carried{at(12), "the ISATAP form (interface identifier 0:5efe or 200:5efe) of"}
		n++
	}
	return held, n
}
