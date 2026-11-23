// packages/envoy/internal/cipher/proxy/leftover.go
// The refusal of a request that still carries a placeholder after the swap (serveBound runs
// leftoverPlaceholder last, before anything goes upstream): what is read as a placeholder, where
// it is looked for, and the grant its refusal line names.
package proxy

import (
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sjawhar/envoy/internal/cipher/requests"
)

// placeholderLeft is the words a request that still carries a placeholder after the swap is
// refused with (leftoverPlaceholder). They name no placeholder and no grant: the refusal line
// names the grant.
const placeholderLeft = "this request still carries a placeholder after the swap - one no live grant of this session swaps on this host, or one outside the headers its entry names - so it is not sent: send a live grant's placeholder, in the header its entry names"

// leftoverPlaceholder is the first placeholder a request about to go upstream still carries, and
// false when it carries none: in a header, in the value itself or inside the Basic credential it
// is - which reads at least what the swap reads, a Basic scheme in any case, a payload with or
// without base64 padding and one that is no UTF-8 among them, the safe side for a refusal - and
// then in the path, its raw form, the query, the query unescaped, and each key and value of the
// query unescaped on its own, so an escape that is no escape in one pair hides nothing in
// another. A placeholder is one as the broker mints it (placeholderIn). Headers are read in
// headersOf's order only once one has been found to carry one, and a query's pairs in the order
// of their keys, so which placeholder a line names does not depend on the run, and a request that
// carries none - every request the swap served - costs no sort.
func leftoverPlaceholder(header http.Header, path, rawPath, rawQuery string) (string, bool) {
	for _, values := range header {
		for _, v := range values {
			if headerPlaceholder(v) == "" {
				continue
			}
			for _, h := range headersOf(header) {
				if placeholder := headerPlaceholder(h[1]); placeholder != "" {
					return placeholder, true
				}
			}
		}
	}
	query, err := url.QueryUnescape(rawQuery)
	if err != nil {
		query = ""
	}
	for _, text := range [...]string{path, rawPath, rawQuery, query} {
		if placeholder := placeholderIn(text); placeholder != "" {
			return placeholder, true
		}
	}
	if err == nil {
		// The whole query unescaped, so each pair of it unescaped was read above.
		return "", false
	}
	pairs, _ := url.ParseQuery(rawQuery) // every pair that unescapes, the others left out
	for _, key := range slices.Sorted(maps.Keys(pairs)) {
		for _, text := range append([]string{key}, pairs[key]...) {
			if placeholder := placeholderIn(text); placeholder != "" {
				return placeholder, true
			}
		}
	}
	return "", false
}

// headerPlaceholder is the first placeholder a header value carries, in the value itself or in
// the Basic credential it is, as git sends a token, with its base64 padded or not, and "" when it
// carries none.
func headerPlaceholder(v string) string {
	if placeholder := placeholderIn(v); placeholder != "" {
		return placeholder
	}
	if scheme, payload, ok := strings.Cut(v, " "); ok && strings.EqualFold(scheme, "basic") {
		payload = strings.Trim(payload, " \t")
		for _, encoding := range [...]*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
			if decoded, err := encoding.DecodeString(payload); err == nil {
				return placeholderIn(string(decoded))
			}
		}
	}
	return ""
}

// placeholderIn is the first placeholder text holds, and "" when it holds none. A placeholder is
// one as the broker mints it: requests.PlaceholderPrefix and then PlaceholderLength letters and
// digits of PlaceholderAlphabet. The prefix with fewer of them after it is no placeholder and
// opens nothing - a code search for the prefix itself is one - so it is not read as one. A whole
// placeholder is, whether or not a grant holds it: the broker deletes an ended grant's rows, so a
// placeholder it no longer knows and one it never minted cannot be told apart.
//
// It scans the default prefix alone, the one prefix every placeholder has today. A kind that sets
// a prefix of its own (placeholder_prefix) adds that prefix to what this scans, or a placeholder of
// that kind left after the swap would go upstream.
func placeholderIn(text string) string {
	for rest := text; ; {
		i := strings.Index(rest, requests.PlaceholderPrefix)
		if i < 0 {
			return ""
		}
		rest = rest[i:]
		if placeholder := placeholderAt(rest); len(placeholder) == len(requests.PlaceholderPrefix)+requests.PlaceholderLength {
			return placeholder
		}
		rest = rest[len(requests.PlaceholderPrefix):]
	}
}

// placeholderAt is what text opens with of a placeholder: the prefix and the letters and digits of
// PlaceholderAlphabet after it, at most PlaceholderLength of them.
func placeholderAt(text string) string {
	end := len(requests.PlaceholderPrefix)
	for end < len(text) && end-len(requests.PlaceholderPrefix) < requests.PlaceholderLength && strings.IndexByte(requests.PlaceholderAlphabet, text[end]) >= 0 {
		end++
	}
	return text[:end]
}

// placeholderGrant is the grant the broker minted placeholder for, live or ended, which the
// refusal of a request that still carried it after the swap names, and "" where no row holds it.
// A read that failed is logged and names nothing: the request is refused either way.
func (s *Server) placeholderGrant(ctx context.Context, placeholder string) string {
	grantID, err := s.grants.ProxyPlaceholderGrant(ctx, placeholder)
	if err != nil {
		s.log.Error("proxy: the grant of a placeholder left after the swap could not be read", "error", err)
		return ""
	}
	return grantID
}
