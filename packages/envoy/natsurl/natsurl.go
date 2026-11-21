// Package natsurl holds the rules a NATS server URL must meet for the NATS client (nats.go) to
// read it as written, with the fixed reasons a refusal of one gives, for a caller in either module:
// the Envoy bus's Dial and Connect, natsprobe, and the Legion daemon's configuration and its NATS
// connection (packages/daemon/internal/config readNatsURLs, natsauth.Connect). It is one rule in
// one place, so the two modules cannot refuse different URLs or word the refusal differently.
//
// The rules exist because a credential written in a URL's userinfo that holds a "/", "?", "#",
// space or comma ends url.Parse's authority early, is no userinfo it reads, or is split from the
// rest of the URL by a caller that splits its list on commas. A password whose head is followed by a
// "/" makes url.Parse read that head as the port, and its error quotes it (`invalid port ":<the
// password's head>" after host`); a token holding a "/" written as the URL's user parses, the
// token's head as the host, which nats.go looks up and names as a host it could not reach; and a
// token or password holding a comma reaches nats.go as two URLs, the credential's head, which can
// parse, and its tail, its "@" and the host. nats.go refuses a URL it cannot parse with url.Parse's
// error, which quotes the URL whole. A refusal under these rules names the URL by its position in
// its list alone, with a reason of fixed words: any text of such a URL can be a part of the
// credential.
//
// A path, a query or a fragment with no "@" is kept: nats.go sends a websocket URL's path and
// query in its handshake (nats.go v1.55.0 ws.go wsInitHandshake), and the daemon's configuration
// takes a path. Like natsversion, it is a door for the daemon's module, which Go's internal rule
// keeps out of the envoy module's internal packages.
package natsurl

import (
	"net/url"
	"slices"
	"strings"
)

// The reasons a NATS server URL is refused for. They are fixed words, never url.Parse's, whose
// text quotes the part of the URL it could not read.
const (
	UnparsedReason         = `it does not parse as a URL; write a credential in it percent-encoded (a "/" as %2F, a comma as %2C)`
	UserinfoPastHostReason = `an "@" stands after its host, as it does where a credential in its userinfo holds a "/", "?" or "#"; write such a credential percent-encoded`
	HandshakeReason        = `its path does not parse once decoded, as the NATS client's websocket handshake decodes it; write a "%" in its path as %2525`
	// CommaReason is a comma's in one URL: the NATS client reads one as the end of a server URL, so
	// a URL that holds one is a list of servers or a credential holding a comma.
	CommaReason = `it holds a comma, which the NATS client reads as a list of servers: list each server as its own nats_urls entry, and write a comma in a credential percent-encoded (%2C)`
	// TailReason is a later URL's, of a list a caller split on its commas, that carries a user name
	// or password and names no scheme the NATS client dials: what a comma in a credential leaves
	// after it. nats.go dialled such a URL with no scheme as nats://, so a list that was written
	// so names the URL's scheme.
	TailReason = `it carries a user name or password and names no NATS scheme (nats, tls, ws or wss), as the part after a comma in a credential does once the list is split on its commas; name the URL's scheme, and write a comma in a credential percent-encoded (%2C)`
)

// schemes are the schemes nats.go dials a server by.
var schemes = []string{"nats", "tls", "ws", "wss"}

// Refusal is why nats.go would read server, one trimmed server URL, other than as written, and ""
// when it would not: read as nats.go reads it, a scheme added where it has none, it does not parse;
// it parses with an "@" after its host (UserinfoPastHost); or it is a websocket URL whose handshake
// URL does not parse (HandshakeURLParses).
func Refusal(server string) string {
	if !strings.Contains(server, "://") {
		server = "nats://" + server
	}
	parsed, err := url.Parse(server)
	switch {
	case err != nil:
		return UnparsedReason
	case UserinfoPastHost(parsed):
		return UserinfoPastHostReason
	case !HandshakeURLParses(parsed):
		return HandshakeReason
	}
	return ""
}

// Check trims each of urls, a list a caller split on its commas, and returns the trimmed list,
// which is the list to dial: nats.go parses a server as written, so an untrimmed URL would pass
// the rules trimmed and reach nats.go untrimmed, whose parse error quotes it whole. It also returns
// the 1-based position of the first URL nats.go would read other than as written, with its reason
// (Refusal, and for a later URL TailReason), or 0 and "".
func Check(urls []string) (trimmed []string, position int, reason string) {
	trimmed = make([]string, len(urls))
	for index, raw := range urls {
		trimmed[index] = strings.TrimSpace(raw)
	}
	for index, server := range trimmed {
		scheme, _, named := strings.Cut(server, "://")
		if index > 0 && strings.Contains(server, "@") && (!named || !slices.Contains(schemes, strings.ToLower(scheme))) {
			return trimmed, index + 1, TailReason
		}
		if why := Refusal(server); why != "" {
			return trimmed, index + 1, why
		}
	}
	return trimmed, 0, ""
}

// UserinfoPastHost reports whether parsed, a NATS server URL that parsed, holds an "@" after its
// host, in its path, query or fragment. That is where a credential holding a "/", "?" or "#" ends
// url.Parse's authority before its "@", so the URL's host is a part of the credential and every
// line that names the host names it.
func UserinfoPastHost(parsed *url.URL) bool {
	return strings.Contains(parsed.Path, "@") || strings.Contains(parsed.RawQuery, "@") || strings.Contains(parsed.Fragment, "@")
}

// HandshakeURLParses reports whether parsed, a NATS server URL that parsed, is no websocket URL or
// one whose handshake URL parses: nats.go's websocket handshake builds a URL of the host, the
// decoded path and the raw query and parses it again, and its error quotes that URL whole, its
// query, and a token in it, included.
func HandshakeURLParses(parsed *url.URL) bool {
	if !strings.EqualFold(parsed.Scheme, "ws") && !strings.EqualFold(parsed.Scheme, "wss") {
		return true
	}
	rebuilt := "http://" + parsed.Host + parsed.Path
	if parsed.RawQuery != "" {
		rebuilt += "?" + parsed.RawQuery
	}
	_, err := url.Parse(rebuilt)
	return err == nil
}
