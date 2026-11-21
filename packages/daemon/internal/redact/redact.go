// Package redact removes the credentials a URL can carry from text the daemon did not write: a
// far side's error, an HTTP client's error, a log line. Such text can hold a URL with a token in
// its query or in its userinfo (a redirect to a signed download, a DSN, a remote with a token as
// its user name), and the daemon carries it on into a claim, the state, the store and the log.
//
// The rule is for error text only. Text that a person or an agent wrote (a handoff summary, a
// review body, an ask's note) keeps its links whole, query included (`?plain=1#L10`, `?pr=M`), so
// the store's own rule (internal/store/storetext.Text) does not apply this one.
//
// The daemon's own log takes the rule in its handler (HandlerOptions). The log of the worker's and
// the pod's own processes (the commands cmd/legion's podLogs names) takes it whole, through
// Writer: such a log mixes a far side's error with what an agent wrote (a tool call's summary),
// and a link of an agent's that loses its query in that log keeps it everywhere else.
//
// A far side's text that is cut to a bound is cut here, by Runes, Bytes or Tail, which give the
// whole text through URLCredentials before they cut it, or by Read, for a text a bounded read cut
// already. A far side is anything outside the process that writes the text: an HTTP answer, a
// child process's output, a frame from the daemon or the agent, a kubelet's message, an error that
// wraps any of those. The order is the point: a cut made before the rule can stop inside a URL's
// userinfo or its query and leave a part of either where the rule no longer finds it. Every such
// cut in the legion binary, the daemon's and the pod's commands alike, is one of theirs but those
// named here, each of which says where it cuts and what of a URL's credential it can keep: the
// role log's cut of a line longer than its bound (the launcher's logLineCut); the sandbox's drop
// of the head a cut log read starts inside, up to its first newline or else its first byte that
// ends a URL (dropCutHead, which readLog and lastBytes share), and readLog's drop of the line its
// read bound stopped inside; storetext.LogWhole's parts of a text the rule has read whole; and a
// detail whose own rules have masked every userinfo before its cut (capabilities' cleanDetail). A
// text the URL rule has already read whole, through URLCredentials or one of these helpers, may be
// cut by any means after it, as the workflow's quote of a failed claim's sentence and its death
// account is (quoteBounded): the supervisor keeps both through storetext's FarSideError and
// FarSideText. So what is cut and how is read from their callers and those rather than from every
// way Go shortens a string.
package redact

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sjawhar/envoy/natsurl"
)

// URLNotPrinted stands for a URL value URL will not print.
const URLNotPrinted = "(a URL that is not printed)"

// URLWithCredentials stands in place of the authority of a URL whose authority holds an "@", in
// what URL, URLCredentials and the state's endpointOf print: its userinfo is a credential, and a
// password holding an unencoded "@" leaves its tail after the last "@", which reads as a host
// (`http://user:p@ssword` names the host `ssword`), so nothing of such an authority is printed.
// URLCredentials writes it for the whole of a URL with an "@" past its host
// (natsurl.UserinfoPastHost).
const URLWithCredentials = "[a URL with credentials]"

// URL is raw, one value known to be a URL (a setting's, an environment variable's), as a line
// prints it: read by url.Parse, with its query and fragment removed, and its authority, when it
// holds an "@", as URLWithCredentials. The rule on text, URLCredentials, finds a URL's end at a
// character such as "'", which a password may hold; a value known to be a URL is read whole here
// instead. A raw that does not parse as a URL with a host, or that holds an "@" past its host
// (natsurl.UserinfoPastHost: where a credential holding a "/", "?" or "#" written in the userinfo
// lands, its head read as the host), prints as URLNotPrinted.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || natsurl.UserinfoPastHost(u) || strings.Contains(u.EscapedPath(), "@") {
		return URLNotPrinted
	}
	if u.User != nil {
		return u.Scheme + "://" + URLWithCredentials + u.EscapedPath()
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u.String()
}

// URLCredentials is text with the query of every URL written in it removed, the authority of
// every URL whose authority holds an "@" written as URLWithCredentials, and every URL with an "@"
// past its host written whole, after its "://", as URLWithCredentials.
//
// A URL starts at <scheme>://, where the scheme is a letter followed by letters, digits, "+", "-"
// or ".". It ends at the first white-space character, `"`, `'`, "`", "<" or ">" after the "://",
// or at the end of the text, so a URL that an error quotes (Go's %q, as a *url.Error writes its
// URL) or writes inside <…> ends where the quote does. In the URL:
//
//   - the authority runs from after the "://" up to the first "/", "?" or "#". One that holds an
//     "@" holds userinfo, a password and a token written as the user name alike, and a password
//     holding an unencoded "@" leaves its tail after the last "@", which reads as a host; such an
//     authority is written URLWithCredentials, nothing of it kept;
//   - an "@" after the authority, in the path, the query or the fragment, is where a credential
//     holding a "/", "?" or "#" written in the userinfo lands, its head then read as the authority
//     (`https://<head>?<tail>@host`): such a URL is written URLWithCredentials from its "://" to
//     its end, nothing of it kept (natsurl.UserinfoPastHost's rule, read on the text). A file URL
//     of this machine, its authority empty or `localhost`, keeps an "@" in its path: RFC 8089
//     gives it no userinfo to write a credential in, and a path's own "@" is common there
//     (`file:///…/node_modules/@scope/…`). An "@" in such a URL's query or fragment, or in any
//     file URL that names a host, marks it as any other;
//   - the fragment starts at the first "#" after the authority, and it stays. A "?" after that "#"
//     is the fragment's;
//   - the query runs from the first "?" after the authority up to the fragment, or up to the end
//     of the URL, and it is removed with its "?".
//
// A URL written in the path or the fragment of another (a redirect's target, an archived page)
// is a URL of its own, under the same rule; one holding an "@" puts that "@" past the other's
// host too, so the other is written whole as the mark. One written in a query goes with that
// query. Nothing else changes: the scheme, an authority with no "@", the path, the fragment and
// every word around the URLs stay as written. Neither a part nor the length of a credential is
// left, and URLCredentials(URLCredentials(s)) == URLCredentials(s): URLWithCredentials holds a
// space, which ends the URL it stands in.
//
// A text the rule does not change, a text with no "://" above all, is returned as it is, with no
// allocation. The rule takes time linear in the text's length, a run of "://" that nothing ends
// included.
func URLCredentials(text string) string {
	if !strings.Contains(text, "://") {
		return text
	}
	var out strings.Builder
	// text[:kept] is written to out or replaced. Every replacement starts after a "://", so kept
	// is 0 until the first one.
	kept := 0
	replace := func(from, to int, with string) {
		if kept == 0 {
			out.Grow(len(text))
		}
		out.WriteString(text[kept:from])
		out.WriteString(with)
		kept = to
	}
	remove := func(from, to int) { replace(from, to, "") }
	// A URL's query is removed once the search for the next "://" passes its start: a URL in the
	// path before it has the same query, and a userinfo of that URL comes first in the text.
	query, fragment := -1, -1
	// The searches that read on to a URL's end are kept, so that a run of text holding many "://"
	// is read once rather than once for each of them: end is the first endsURL rune at or after
	// the position it was last looked for from, and slash, question and hash are the next "/",
	// "?" and "#" (nextByte). Each is looked for again only when it is behind the position now
	// asked about: one found from a position p is the answer for any later position it is not
	// behind, since none lies between p and it. So a kept answer is right only while no position
	// asked is before one asked earlier, and none is: in one URL at ≤ host; host is at or before
	// the "/" of the next "://", so before the next URL's at; and a removed query moves at to
	// fragment, after the query's "?", at or before which lie the at and the host of the URL whose
	// query it is and of each URL after it, whose "://" comes before the "?". The runes decoded
	// from at and from an earlier position agree from at on, since the ":" of the "://" before at
	// starts a rune either way. The search for the next "://" reads a stretch twice at most: once,
	// and again from fragment after a removed query. lastAt is the last "@" before end, found when
	// end is: every URL that ends at end starts at or after the at it was found from, so a URL has
	// an "@" past its host exactly when lastAt is at or after that host, and the stretches it is
	// looked for in lie apart from one another.
	end, lastAt := -1, -1
	slash, question, hash := nextByte{c: '/', at: -1}, nextByte{c: '?', at: -1}, nextByte{c: '#', at: -1}
	for at := 0; ; {
		i := strings.Index(text[at:], "://")
		if query >= 0 && (i < 0 || at+i >= query) {
			remove(query, fragment)
			at, query = fragment, -1
			continue
		}
		if i < 0 {
			break
		}
		sep := at + i
		at = sep + len("://")
		// schemeEndsAt reads back from sep over scheme characters alone, so no further than the
		// ":" of the "://" before: its reads of the whole text add up to the text's length.
		if !schemeEndsAt(text, sep) {
			continue
		}
		if end < at {
			end = at + orLen(strings.IndexFunc(text[at:], endsURL), len(text)-at)
			lastAt = strings.LastIndexByte(text[at:end], '@')
			if lastAt >= 0 {
				lastAt += at
			}
		}
		host := min(slash.from(text, at), question.from(text, at), hash.from(text, at), end)
		// An "@" past the host writes the whole URL as the mark, and the search goes on from the
		// URL's end, so every "://" in [host, end) is this URL's own. A query pending from a URL
		// before this one lies in it only when that URL is a local file URL, which an "@" in its
		// path leaves whole, and which ends where this one does: the mark covers that query, so it
		// is no longer pending.
		if lastAt >= host && !localFilePath(text, sep, at, host, min(question.from(text, host), hash.from(text, host), end), lastAt) {
			replace(at, end, URLWithCredentials)
			at = end
			query = -1
			continue
		}
		// [at, host) ends at or before the "/" of the next "://", so the userinfo's searches read
		// spans apart from one another, the whole text once at most.
		if strings.IndexByte(text[at:host], '@') >= 0 {
			replace(at, host, URLWithCredentials)
		}
		frag := min(hash.from(text, host), end)
		if q := question.from(text, host); q < frag {
			query, fragment = q, frag
		}
	}
	if kept == 0 {
		return text
	}
	out.WriteString(text[kept:])
	return out.String()
}

// localFilePath reports whether the URL whose "://" is at sep, whose authority is text[at:host],
// whose path ends at pathEnd (its first "?" or "#", or its end) and whose last "@" is at lastAt,
// is a file URL of this machine whose every "@" past its host is in its path: its scheme is "file"
// in any case, its authority is empty or `localhost`, and no "@" stands in its query or fragment.
func localFilePath(text string, sep, at, host, pathEnd, lastAt int) bool {
	if !fileSchemeEndsAt(text, sep) {
		return false
	}
	if authority := text[at:host]; authority != "" && !strings.EqualFold(authority, "localhost") {
		return false
	}
	return lastAt < pathEnd
}

// nextByte is the index of the next c in a text at or after the position it was last looked for
// from, or the text's length when there is none.
type nextByte struct {
	c  byte
	at int
}

// from is the index of the next c in text at or after i, looked for again only when the one kept
// is behind i. It is right only when i is no smaller than every position asked before it.
func (n *nextByte) from(text string, i int) int {
	if n.at < i {
		n.at = i + orLen(strings.IndexByte(text[i:], n.c), len(text)-i)
	}
	return n.at
}

// schemeEndsAt reports whether a scheme ends at sep, the index of a "://" in text: the run of
// letters, digits, "+", "-" and "." before it holds a letter, where a scheme can start.
func schemeEndsAt(text string, sep int) bool {
	letter := false
	for i := sep - 1; i >= 0; i-- {
		switch c := text[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
			letter = true
		case '0' <= c && c <= '9', c == '+', c == '-', c == '.':
		default:
			return letter
		}
	}
	return letter
}

// fileSchemeEndsAt reports whether the scheme that ends at sep, the index of a "://" in text, is
// "file", in any case.
func fileSchemeEndsAt(text string, sep int) bool {
	const file = "file"
	start := sep - len(file)
	return start >= 0 && strings.EqualFold(text[start:sep], file) && (start == 0 || !schemeByte(text[start-1]))
}

// schemeByte reports whether c can stand in a scheme: a letter, a digit, "+", "-" or ".".
func schemeByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'
}

// endsURL reports whether r ends a URL: a white-space character, `"`, `'`, "`", "<" or ">". A rune
// below utf8.RuneSelf is the byte EndsURLByte reads: of those runes, unicode.IsSpace holds for the
// ASCII white space EndsURLByte names and for no other.
func endsURL(r rune) bool {
	if r < utf8.RuneSelf {
		return EndsURLByte(byte(r))
	}
	return unicode.IsSpace(r)
}

// EndsURLByte reports whether c is a byte that ends a URL for URLCredentials: an ASCII white-space
// character, `"`, `'`, "`", "<" or ">". None of them is part of a longer UTF-8 sequence, so a cut
// just after one never splits a rune.
func EndsURLByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r', '"', '\'', '`', '<', '>':
		return true
	}
	return false
}

// orLen is i, or n when i is -1 (not found).
func orLen(i, n int) int {
	if i < 0 {
		return n
	}
	return i
}

// HandlerOptions is a copy of opts, or of the zero options when opts is nil, whose ReplaceAttr
// gives the text of every attribute through URLCredentials: a string value, an error value (its
// Error()) and a fmt.Stringer value (its String()), the record's message among them. An error or a
// Stringer comes out as a string attribute of what the rule leaves. Every other value (a number, a
// time, a bool, any other type) stays as it is. The caller's own ReplaceAttr, when opts has one,
// runs first, and the rule reads what it returns. A handler calls ReplaceAttr for each attribute
// inside a group and for each attribute a Logger.With carries too, so those take the rule as well.
func HandlerOptions(opts *slog.HandlerOptions) *slog.HandlerOptions {
	var o slog.HandlerOptions
	if opts != nil {
		o = *opts
	}
	own := o.ReplaceAttr
	o.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
		if own != nil {
			a = own(groups, a)
			// The caller's function may return an unresolved value, which the handler resolves
			// after this function: the rule reads the value the handler writes.
			a.Value = a.Value.Resolve()
		}
		switch a.Value.Kind() {
		case slog.KindString:
			a.Value = slog.StringValue(URLCredentials(a.Value.String()))
		case slog.KindAny:
			if text, ok := textOf(a.Value.Any()); ok {
				a.Value = slog.StringValue(URLCredentials(text))
			}
		}
		return a
	}
	return &o
}

// textOf is the text of an error or a fmt.Stringer. A method that panics (a nil pointer's,
// mostly) gives none, and the value is left to the handler, which writes such a value under its
// own recovery, as it does with no ReplaceAttr: ReplaceAttr runs outside that recovery, and a
// panic there would end the caller's log call.
func textOf(v any) (text string, ok bool) {
	defer func() {
		if recover() != nil {
			text, ok = "", false
		}
	}()
	switch v := v.(type) {
	case error:
		return v.Error(), true
	case fmt.Stringer:
		return v.String(), true
	}
	return "", false
}
