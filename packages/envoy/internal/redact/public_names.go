package redact

import (
	"cmp"
	"net/http"
	"strings"
)

// publicRequestName is one name whose value a wire contract declares public: Place is "header" or
// "field" (a query, form or JSON body field), Source is that contract, and Routes the routes it
// names the value public on, each "<METHOD> <PATH>" (routeMatches), the value public on no other;
// with no Routes, as for a name the contract declares public in every request that carries it,
// the value is public on every request. The table, publicRequestNames, is generated from
// packages/contracts/src/public-request-names.ts, which the TypeScript half reads, so both halves
// read one table.
type publicRequestName struct {
	Place, Name, Source string
	Routes              []string
}

// requestRoute is the method and escaped path of the request a value is carried by, which an
// entry's Routes are matched against: the method in upper case, an empty one GET, and ok false for
// a request with no URL, which is on no route.
type requestRoute struct {
	method, path string
	ok           bool
}

// routeOf is r's requestRoute.
func routeOf(r *http.Request) requestRoute {
	if r.URL == nil {
		return requestRoute{}
	}
	return requestRoute{method: strings.ToUpper(cmp.Or(r.Method, http.MethodGet)), path: r.URL.EscapedPath(), ok: true}
}

// publicHeader reports whether a header named name, on a request at route, carries a value public
// by its contract, the name compared without case, as HTTP compares it.
func publicHeader(route requestRoute, name string) bool {
	for _, entry := range publicRequestNames {
		if entry.Place == "header" && strings.EqualFold(entry.Name, name) && entry.publicOn(route) {
			return true
		}
	}
	return false
}

// publicField reports whether a query, form or JSON body field named name, on a request at route,
// carries a value public by its contract.
func publicField(route requestRoute, name string) bool {
	for _, entry := range publicRequestNames {
		if entry.Place == "field" && entry.Name == name && entry.publicOn(route) {
			return true
		}
	}
	return false
}

// publicOn reports whether the entry's value is public on a request at route: on every request
// where the entry names no Routes, and otherwise only on one that one of them matches.
func (entry publicRequestName) publicOn(route requestRoute) bool {
	if len(entry.Routes) == 0 {
		return true
	}
	if !route.ok {
		return false
	}
	for _, r := range entry.Routes {
		if routeMatches(r, route.method, route.path) {
			return true
		}
	}
	return false
}

// routeMatches reports whether route, "<METHOD> <PATH>", matches a request's method, in upper
// case, and its whole escaped path, compared segment by segment: a "{name}" in a segment matches
// one or more characters other than '/', a last segment "*" matches the path above it and every
// path below it ("/*" every path), and every other character is literal.
func routeMatches(route, method, path string) bool {
	routeMethod, want, ok := strings.Cut(route, " ")
	if !ok || routeMethod != method {
		return false
	}
	got := path
	for {
		wantSegment, wantRest, wantMore := strings.Cut(want, "/")
		if wantSegment == "*" && !wantMore {
			return true
		}
		gotSegment, gotRest, gotMore := strings.Cut(got, "/")
		if !segmentMatches(wantSegment, gotSegment) {
			return false
		}
		if !wantMore || !gotMore {
			return wantMore == gotMore || (wantMore && wantRest == "*")
		}
		want, got = wantRest, gotRest
	}
}

// segmentMatches reports whether a path's segment matches a route's, pattern: each "{name}" of it
// matches one or more characters of segment, and every other character itself.
func segmentMatches(pattern, segment string) bool {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return pattern == segment
	}
	end := strings.IndexByte(pattern[open:], '}')
	if end < 0 {
		return pattern == segment
	}
	if !strings.HasPrefix(segment, pattern[:open]) {
		return false
	}
	rest, tail := pattern[open+end+1:], segment[open:]
	for taken := 1; taken <= len(tail); taken++ {
		if segmentMatches(rest, tail[taken:]) {
			return true
		}
	}
	return false
}
