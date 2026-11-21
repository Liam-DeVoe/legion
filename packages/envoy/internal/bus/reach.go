package bus

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// AllowRemoteEnvVar names the variable a run sets to "1" to reach a NATS server that is not this
// machine's, or any NATS through a SOCKS5 proxy (SOCKS5ProxyEnvVar). Every connect refuses one
// otherwise, whether or not it owns the stream: a process that reaches another machine's bus
// publishes into an event stream every agent consumes, and one that owns the stream reconfigures a
// resource several deployments read.
//
// Nothing infers the reach, because nothing can tell the deployed Dispatch from the same binary
// run out of a checkout: both read envoy.natsUrls from the user envoy.json
// (${XDG_CONFIG_HOME:-~/.config}/legion/envoy.json), which on an agent's machine names
// production (LEGION-249). Each deployment states its reach instead
// (packages/envoy/deploy/compose/*.compose.yml, the production deployment's listener and Dispatch
// service definitions, and the on-prem fleet's Pulumi), and it must be set there BEFORE a binary that
// reads it runs on that deployment: without it, this one refuses the shared NATS its deployment
// names and exits. Setting it early costs nothing, because a binary built before the variable
// ignores it.
const AllowRemoteEnvVar = "ENVOY_ALLOW_REMOTE_NATS"

// ErrRemoteNATS is what a connect refuses a NATS server this machine does not run with.
var ErrRemoteNATS = errors.New("bus: refusing a NATS server that is not this machine's")

// ErrNoNATSServer is a connect's refusal of a server list that names no server, or holds an entry
// that is blank, names no host, or names the unspecified address (refuseNoServer).
var ErrNoNATSServer = errors.New("bus: refusing a NATS server list that names no server")

// refuseNoServer reports urls naming no server, or an entry that is blank, names no host, or names
// the unspecified address, read as LocalNATSURL reads it (nats://, nats://:4222,
// nats://0.0.0.0:4222, nats://[::]:4222). nats.go fills an empty list with its DefaultURL,
// nats://127.0.0.1:4222, and reads a blank entry or one with no host as port 4222 (or the port the
// entry names) on this machine; a connect to the unspecified address, 0.0.0.0 or ::, reaches this
// machine too, since the network stack takes it for its own. So such a client would reach
// whatever listens there, presenting its credential: on a shared machine, another process's
// server, the hazard a test server on a loopback address of its own (testport) closes for the
// tests. ENVOY_ALLOW_REMOTE_NATS does not admit such an entry: it names no server to allow. Every
// connect refuses it first, before its environment is read. An entry that does not parse is left
// to nats.go, which refuses it at its connect rather than dialing a default.
func refuseNoServer(urls []string) error {
	if len(urls) == 0 {
		return fmt.Errorf("%w: nats.go would dial its default, nats://127.0.0.1:4222, in its place", ErrNoNATSServer)
	}
	for i, raw := range urls {
		if strings.TrimSpace(raw) == "" {
			return fmt.Errorf("%w: entry %d of %d is blank, which nats.go reads as port 4222 on this machine", ErrNoNATSServer, i+1, len(urls))
		}
		host, err := natsURLHost(raw)
		if err != nil {
			continue
		}
		if host == "" {
			return fmt.Errorf("%w: entry %d of %d, %s, names no host, which nats.go dials on this machine", ErrNoNATSServer, i+1, len(urls), natsURLWithoutUserinfo(raw))
		}
		// netip reads an address with a zone (::%lo), which the unspecified address may carry too.
		if address, err := netip.ParseAddr(host); err == nil && address.WithZone("").Unmap().IsUnspecified() {
			return fmt.Errorf("%w: entry %d of %d, %s, names the unspecified address, which a connect reaches on this machine", ErrNoNATSServer, i+1, len(urls), natsURLWithoutUserinfo(raw))
		}
	}
	return nil
}

// refuseRemoteNATS reports the first url naming another machine, unless the run opted in through
// lookup (the connect's environment, WithEnvironment). A set SOCKS5ProxyEnvVar is remote reach
// whatever the URLs say: the proxy chooses the server a connection reaches, so a loopback URL no
// longer names this machine's. A list naming no server never reaches it: refuseNoServer refuses it
// first.
func refuseRemoteNATS(urls []string, lookup func(string) (string, bool)) error {
	if allow, _ := lookup(AllowRemoteEnvVar); allow == "1" {
		return nil
	}
	if _, set := lookup(SOCKS5ProxyEnvVar); set {
		// Not the value: a proxy can be written with credentials, and this refusal is logged.
		return fmt.Errorf("%w: %s is set, which sends every connection through a proxy that chooses the server (set %s=1 to reach it)", ErrRemoteNATS, SOCKS5ProxyEnvVar, AllowRemoteEnvVar)
	}
	for _, raw := range urls {
		if LocalNATSURL(raw) {
			continue
		}
		return fmt.Errorf("%w: %s (set %s=1 to reach it)", ErrRemoteNATS, natsURLWithoutUserinfo(raw), AllowRemoteEnvVar)
	}
	return nil
}

// LocalNATSURL reports whether raw names a server on this machine. It reads the URL and never
// resolves it: a lookup would reach the network the refusal is about, and a name that resolves to
// a loopback address on one machine resolves to a shared server on another - the devbox's own
// envoy-nats is exactly such a name. A URL it cannot read names no server it can vouch for.
func LocalNATSURL(raw string) bool {
	host, err := natsURLHost(raw)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// natsURLHost is the host raw names, read as a NATS server URL is read here: trimmed, with nats://
// in front when it names no scheme, and without a root dot, since a host name's root dot is not
// part of the name. It does not change case: a host name is case-insensitive, so a caller compares
// it so.
func natsURLHost(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.Contains(trimmed, "://") {
		trimmed = "nats://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(parsed.Hostname(), "."), nil
}

// natsURLWithoutUserinfo renders raw, a configured NATS server URL, for a log line or an error
// that is logged, with no userinfo at all. nats.go authenticates with the URL's userinfo
// (connectProto, nats.go v1.55.0 nats.go:3133-3143): a user name alone is a token and
// user:password a password, so both parts are the credential, and url.URL.Redacted, which keeps
// the user name, would log a token whole. raw is read as LocalNATSURL reads it. A URL with no "@"
// carries no userinfo and comes back as written. Any other comes back as nothing that preceded an
// "@" in it: parsed and rendered without its userinfo when that leaves no "@", and otherwise -
// a URL that does not parse, or one with an "@" past its authority - as what follows its last "@".
func natsURLWithoutUserinfo(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.Contains(trimmed, "@") {
		return trimmed
	}
	withScheme := trimmed
	if !strings.Contains(withScheme, "://") {
		withScheme = "nats://" + withScheme
	}
	if parsed, err := url.Parse(withScheme); err == nil {
		parsed.User = nil
		if rendered := parsed.String(); !strings.Contains(rendered, "@") {
			return rendered
		}
	}
	return trimmed[strings.LastIndex(trimmed, "@")+1:]
}
