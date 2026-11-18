package redact

import "strings"

// WordsNotReadable is what a message prints in place of a remote field the rule cannot read as
// clean (Remote, Field). It is a different case from brokeraddr.WordsNotPrinted, which an address
// that is not clean prints in place of every word; brokeraddr.Address.Words prints both.
const WordsNotReadable = "(not printed: the answer's words hold a URL the rule cannot read as clean)"

// Remote is the rule for one field another party wrote - a refusal's words, a code, a body. It
// fails closed: it answers the field with each URL's user information removed, each query and
// fragment value removed (the names stay) and each bare query or fragment segment removed, and
// true, only when it can read every URL in the field with certainty; otherwise "" and false.
//
// The grammar, applied to the field:
//  1. A URL starts at a scheme ([A-Za-z][A-Za-z0-9+.-]*) followed by ':' and two slashes, or at
//     two slashes not preceded by a scheme character, ':' or a slash. Either slash may be a
//     backslash, since a browser reads one as a slash; step 3 rejects such a start.
//  2. A run is the text from a URL start to the next ASCII whitespace or the end of the field.
//     When it does not end inside a query or a fragment, the characters .,;:!?)]}'" at its end
//     are not part of it; when it does, they are part of the last value.
//  3. Not readable when a run holds a '%', a '\', a quote, '<', '>', a backtick, a control
//     character or any non-ASCII character (U+2028 and U+2029 among them); when its authority
//     (before the first '/', '?' or '#') holds more than one '@'; when it holds an '@' after its
//     authority; or when a port is not all digits.
//  4. Not readable when a query or fragment segment (split on '&' and ';') has a value that
//     holds '=' or '?'.
//  5. Not readable when a whitespace-delimited token, trimmed as in 2, holds an '@' with a ':'
//     before it (user:secret@10.0.0.5:443, proxy=user:pw@host). In a token that holds a URL
//     start this reads the text before the start; the run itself is read by 3 and 4, so a URL
//     with user information stays readable.
//  6. Whitespace can split a URL, so not readable when a token after the token that holds the
//     first URL start holds an '@'; when the token right after a run holds '?', '#' or '='; or
//     when a run ends inside a query or a fragment and more text follows it in the field.
//  7. An encoded URL has no start for 1 to find, so not readable when the field holds an encoded
//     '/', ':', '@', '?', '#', '=', '&', '%' or '\': %2F, %3A, %40, %3F, %23, %3D, %26, %25 or
//     %5C, the same as \u00XX, or as \xXX, in any case. An encoded '%' catches a URL encoded more
//     than once; a '%' with no such pair after it, as in "100%", is ordinary text.
//  8. Whitespace can split a credential however many gaps it puts in it, so the field is read
//     again with its ASCII whitespace removed (splitCredential): not readable when that text holds
//     a user:secret@ user information right after a scheme's "://" or a key's '=', or a query or
//     fragment value of a URL, whose characters the field itself splits with whitespace. No run 2
//     reads holds whitespace, so such a piece is one the steps above did not read as part of one
//     clean URL. A label, a ':' and an address with no scheme and no key before them, as in
//     "sign-in: ada@example.com", is text: no syntax tells a split user:secret@host from it, and
//     a refusal prints identities.
//
// A field with no URL comes back as it is. Remote does not quote: Field puts its answer through
// OneLine. Its answer is not meant to be read by the rule again: a run whose last value was
// removed can end in '=' with text after it. The TypeScript half is readRemoteText in
// packages/contracts/src/remote-text.ts; both are held to the fixture at testdata/remote.json.
func Remote(field string) (string, bool) {
	if encodedSeparator(field) {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(field))
	seenURL, afterRun := false, false
	for i := 0; i < len(field); {
		if asciiSpace(field[i]) {
			b.WriteByte(field[i])
			i++
			continue
		}
		end := i
		for end < len(field) && !asciiSpace(field[end]) {
			end++
		}
		token := field[i:end]
		if seenURL && strings.IndexByte(token, '@') >= 0 || afterRun && strings.ContainsAny(token, "?#=") {
			return "", false
		}
		start := urlStart(token)
		outside := token
		if start >= 0 {
			outside = token[:start]
		}
		bare := trimRunEnd(outside)
		if colon := strings.IndexByte(bare, ':'); colon >= 0 && strings.LastIndexByte(bare, '@') > colon {
			return "", false
		}
		b.WriteString(outside)
		afterRun = false
		if start >= 0 {
			shown, length, inQuery, ok := readRun(token[start:])
			if !ok || inQuery && strings.TrimLeft(field[end:], " \t\n\v\f\r") != "" {
				return "", false
			}
			b.WriteString(shown)
			b.WriteString(token[start+length:])
			seenURL, afterRun = true, true
		}
		i = end
	}
	if splitCredential(field) {
		return "", false
	}
	return b.String(), true
}

// userInfoStop are the characters a user:secret@ user information cannot hold, which end the
// piece splitCredential reads one for.
const userInfoStop = "/?#&;=,\"'<>()[]{}`\\|"

// splitCredential reports whether field, read with its ASCII whitespace removed, holds a
// credential-shaped piece whose characters the field splits with whitespace (Remote's step 8): a
// user:secret@ user information, a ':' and the next '@' with none of userInfoStop between them,
// right after a scheme's "://" or a key's '=' (userInfoLead) and before a host's first character
// (hostStart), or a query or fragment value, one that is not empty, of a URL that starts where
// step 1 finds one in the field, read in the whitespace-removed text from that start to the first
// quote, '<', '>' or backtick. The start is found in the field and not in the squeezed text, where
// a scheme could run back across a gap into the word before it.
func splitCredential(field string) bool {
	squeezed := make([]byte, 0, len(field))
	at := make([]int, 0, len(field))
	for i := 0; i < len(field); i++ {
		if !asciiSpace(field[i]) {
			squeezed = append(squeezed, field[i])
			at = append(at, i)
		}
	}
	if len(squeezed) == len(field) {
		return false
	}
	split := func(from, to int) bool { return to > from && at[to-1]-at[from] != to-1-from }
	from, colon := 0, -1
	for i, c := range squeezed {
		switch {
		case strings.IndexByte(userInfoStop, c) >= 0:
			from, colon = i+1, -1
		case c == ':' && colon < 0:
			colon = i
		case c == '@':
			if colon >= 0 && split(from, i+1) && userInfoLead(squeezed, from) && i+1 < len(squeezed) && hostStart(squeezed[i+1]) {
				return true
			}
			// A user information ends at its '@'.
			from, colon = i+1, -1
		}
	}
	// Each URL start step 1 finds in the field, as an index into squeezed, one per token.
	var starts []int
	for t := 0; t < len(squeezed); {
		end := t
		for end+1 < len(squeezed) && at[end+1] == at[end]+1 {
			end++
		}
		if u := urlStart(string(squeezed[t : end+1])); u >= 0 {
			starts = append(starts, t+u)
		}
		t = end + 1
	}
	for n, start := range starts {
		end := len(squeezed)
		if n+1 < len(starts) {
			end = starts[n+1]
		}
		if stop := strings.IndexAny(string(squeezed[start+1:end]), "\"'<>`"); stop >= 0 {
			end = start + 1 + stop
		}
		q := strings.IndexAny(string(squeezed[start:end]), "?#")
		if q < 0 {
			continue
		}
		for seg := start + q + 1; seg < end; {
			next := seg
			for next < end && strings.IndexByte("&;#?", squeezed[next]) < 0 {
				next++
			}
			if eq := strings.IndexByte(string(squeezed[seg:next]), '='); eq >= 0 && seg+eq+1 < next && split(start, next) {
				return true
			}
			seg = next + 1
		}
	}
	return false
}

// userInfoLead reports whether the piece at from in squeezed, the whitespace-removed field, follows
// a scheme and its "://", either slash a backslash too (https://user: pw@host), or a key and its
// '=' (proxy=user: pw@host). Either tells a split user:secret@ from a label and an address
// ("sign-in: ada@example.com"), which step 8 reads as text.
func userInfoLead(squeezed []byte, from int) bool {
	switch {
	case from >= 2 && squeezed[from-1] == '=':
		return strings.IndexByte(userInfoStop, squeezed[from-2]) < 0
	case from >= 4 && slash(squeezed[from-1]) && slash(squeezed[from-2]) && squeezed[from-3] == ':':
		return schemeStart(string(squeezed), from-3) >= 0
	}
	return false
}

// hostStart reports whether c can start the host a user information's '@' comes before: a
// letter, a digit, or the '[' of an IPv6 address.
func hostStart(c byte) bool { return letter(c) || '0' <= c && c <= '9' || c == '[' }

// Field is a remote field as a message may print it: what Remote answers, on one line (OneLine),
// or WordsNotReadable where Remote cannot read it.
func Field(field string) string {
	if shown, ok := Remote(field); ok {
		return OneLine(shown)
	}
	return WordsNotReadable
}

// urlStart is where the first URL in token starts (Remote's step 1), or -1.
func urlStart(token string) int {
	for i := 0; i+1 < len(token); i++ {
		if token[i] == ':' && i+2 < len(token) && slash(token[i+1]) && slash(token[i+2]) {
			if start := schemeStart(token, i); start >= 0 {
				return start
			}
			continue
		}
		if slash(token[i]) && slash(token[i+1]) && (i == 0 || !schemeByte(token[i-1]) && token[i-1] != ':' && !slash(token[i-1])) {
			return i
		}
	}
	return -1
}

// readRun reads the run at the start of text, which runs to text's end (Remote's steps 2, 3 and
// 4): it answers the run as it prints, its length in text, whether it ends inside a query or a
// fragment, and whether it is readable.
func readRun(text string) (shown string, length int, inQuery, ok bool) {
	head := 2
	if !slash(text[0]) {
		head = strings.IndexByte(text, ':') + 3
	}
	run := text
	inQuery = strings.ContainsAny(run[head:], "?#")
	if !inQuery {
		run = trimRunEnd(run)
	}
	for i := range len(run) {
		if c := run[i]; c < ' ' || c >= 0x7f || strings.IndexByte("%\\\"'<>`", c) >= 0 {
			return "", 0, false, false
		}
	}
	rest := run[head:]
	authEnd := len(rest)
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authEnd = i
	}
	authority, after := rest[:authEnd], rest[authEnd:]
	if strings.Count(authority, "@") > 1 || strings.IndexByte(after, '@') >= 0 {
		return "", 0, false, false
	}
	host := authority[strings.IndexByte(authority, '@')+1:]
	if !digitPort(host) {
		return "", 0, false, false
	}
	path, query, fragment := after, "", ""
	if i := strings.IndexByte(after, '#'); i >= 0 {
		path, fragment = after[:i], after[i:]
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path, query = path[:i], path[i:]
	}
	var b strings.Builder
	b.WriteString(run[:head])
	b.WriteString(host)
	b.WriteString(path)
	for _, part := range []string{query, fragment} {
		if part == "" {
			continue
		}
		b.WriteByte(part[0])
		segments := part[1:]
		for {
			end := strings.IndexAny(segments, "&;")
			segment := segments
			if end >= 0 {
				segment = segments[:end]
			}
			name, value, named := strings.Cut(segment, "=")
			if strings.ContainsAny(value, "=?") {
				return "", 0, false, false
			}
			if named {
				b.WriteString(name)
				b.WriteByte('=')
			}
			if end < 0 {
				break
			}
			b.WriteByte(segments[end])
			segments = segments[end+1:]
		}
	}
	return b.String(), len(run), inQuery, true
}

// digitPort reports whether host's port, if it names one, is all digits: after "]:" for a
// bracketed IPv6 host, and after the first ':' otherwise. A '[' with no ']' is not read.
func digitPort(host string) bool {
	port := ""
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return false
		}
		switch rest := host[end+1:]; {
		case rest == "":
		case rest[0] == ':':
			port = rest[1:]
		default:
			return false
		}
	} else if i := strings.IndexByte(host, ':'); i >= 0 {
		port = host[i+1:]
	}
	return strings.Trim(port, "0123456789") == ""
}

// encodedSeparators are the escapes Remote's step 7 rejects, lower-cased: of '/', ':', '@', '?',
// '#', '=', '&', '%' and '\', each as %XX, \u00XX and \xXX.
var encodedSeparators = func() []string {
	var all []string
	for _, hex := range []string{"2f", "3a", "40", "3f", "23", "3d", "26", "25", "5c"} {
		all = append(all, "%"+hex, `\u00`+hex, `\x`+hex)
	}
	return all
}()

// encodedSeparator reports whether field holds one of encodedSeparators, in any case. None holds
// whitespace, so a field holds one exactly where one of its tokens does.
func encodedSeparator(field string) bool {
	lower := strings.ToLower(field)
	for _, e := range encodedSeparators {
		if strings.Contains(lower, e) {
			return true
		}
	}
	return false
}

// trimRunEnd is s without the characters .,;:!?)]}'" it ends in (Remote's steps 2 and 5).
func trimRunEnd(s string) string { return strings.TrimRight(s, `.,;:!?)]}'"`) }

// schemeStart is where the scheme ending at colon starts in s: the first letter of the longest
// run of scheme bytes before colon, or -1 when that run holds no letter.
func schemeStart(s string, colon int) int {
	start := colon
	for start > 0 && schemeByte(s[start-1]) {
		start--
	}
	for start < colon && !letter(s[start]) {
		start++
	}
	if start == colon {
		return -1
	}
	return start
}

func slash(c byte) bool { return c == '/' || c == '\\' }

func letter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func schemeByte(c byte) bool {
	return letter(c) || '0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'
}

// asciiSpace reports whether c is ASCII whitespace, which ends a run and a token.
func asciiSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}
