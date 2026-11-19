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
//  8. Whitespace can split a URL or a credential however many gaps it puts in it, so the rule
//     reads the field a second time with its ASCII whitespace removed, by the same grammar
//     (squeezedRemovesNoMore): one run (2, 3 and 4) from the earliest URL start step 1 finds in
//     that text or in a token of the field, since a gap can hide a start as well as make one
//     ("fetch //host" reads "fetch//host"), and, in the text before it, each user:secret@ user
//     information (userInfoPiece): a ':' and the next '@' with only bytes RFC 3986 lets a user
//     information hold between them, led by a key's '=', or by a quote after one, and before a
//     byte a host can start with. That run ends at the first byte step 3 refuses that the first
//     reading read outside every run: a quote or a '%' in the words after a URL ends it there,
//     as a gap ended the first reading's run, rather than making the URL unreadable. Nor does
//     the second reading ask step 3 for an all-digit port: there a URL with no path runs into
//     the next word ("nats://a:4222, nats://b:4222"), and the first reading asks it of every run.
//     The field is readable only when the second reading is readable and removes no byte the
//     first keeps. Step 5's token is the field's own, so across a gap a key leads a user
//     information and a token's start does not: a label, a ':' and an address with no key before
//     them, as in "sign-in: ada@example.com", is text, since no syntax tells a split
//     user:secret@host from it and a refusal prints identities. The known residual is that form
//     and two others: words a quote begins after a gap (https://idp.example/cb x '?session=...'),
//     where the run ends at the quote, and a split https://user: password/x with no '@', which no
//     URL parser reads as user information.
//
// A field with no URL comes back as it is. Remote does not quote: Field puts its answer through
// OneLine. Its answer is not meant to be read by the rule again: a run whose last value was
// removed can end in '=' with text after it. The TypeScript half is readRemoteText in
// packages/contracts/src/remote-text.ts; both are held to the fixture at testdata/remote.json.
func Remote(field string) (string, bool) {
	if encodedSeparator(field) {
		return "", false
	}
	// read is what the reading of field's tokens did with each byte, which step 8 compares with;
	// a field with no whitespace has no second reading.
	var read []byteRead
	if strings.ContainsAny(field, " \t\n\v\f\r") {
		read = make([]byteRead, len(field))
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
			runAt := i + start
			shown, length, inQuery, ok := readRun(token[start:], true, func(from, to int) {
				for k := runAt + from; read != nil && k < runAt+to; k++ {
					read[k] = removedByRun
				}
			})
			for k := runAt; read != nil && k < runAt+length; k++ {
				read[k] = max(read[k], keptByRun)
			}
			if !ok || inQuery && strings.TrimLeft(field[end:], " \t\n\v\f\r") != "" {
				return "", false
			}
			b.WriteString(shown)
			b.WriteString(token[start+length:])
			seenURL, afterRun = true, true
		}
		i = end
	}
	if read != nil && !squeezedRemovesNoMore(field, read) {
		return "", false
	}
	return b.String(), true
}

// byteRead is what Remote's reading of a field's tokens did with one byte: read it outside every
// run, keep it in a run, or remove it from one.
type byteRead uint8

const (
	outsideRuns byteRead = iota
	keptByRun
	removedByRun
)

// squeezedRemovesNoMore is Remote's step 8: whether field, read with its ASCII whitespace
// removed, is readable and removes no byte that read, the reading of its tokens, keeps. That
// reading reads one run, from the earliest URL start step 1 finds in the whitespace-removed text
// or in a token of the field to the first byte step 3 refuses (runRefuses) that the first
// reading read outside every run, or the text's end, and each user information userInfoPiece
// finds in the text before that start.
func squeezedRemovesNoMore(field string, read []byteRead) bool {
	squeezed := make([]byte, 0, len(field))
	at := make([]int, 0, len(field))
	start := -1
	for i := 0; i < len(field); {
		if asciiSpace(field[i]) {
			i++
			continue
		}
		end := i
		for end < len(field) && !asciiSpace(field[end]) {
			end++
		}
		if u := urlStart(field[i:end]); u >= 0 && start < 0 {
			start = len(squeezed) + u
		}
		for k := i; k < end; k++ {
			squeezed = append(squeezed, field[k])
			at = append(at, k)
		}
		i = end
	}
	text := string(squeezed)
	if u := urlStart(text); u >= 0 && (start < 0 || u < start) {
		start = u
	}
	before := text
	if start >= 0 {
		before = text[:start]
	}
	if userInfoPiece(before) {
		return false
	}
	if start < 0 {
		return true
	}
	// The run holds at least its start, the scheme and its two slashes or the two slashes alone:
	// either slash may be a backslash, which step 3 then refuses.
	end := start + 2
	if !slash(text[start]) {
		end = start + strings.IndexByte(text[start:], ':') + 3
	}
	for end < len(text) && !(runRefuses(text[end]) && read[at[end]] == outsideRuns) {
		end++
	}
	more := false
	_, _, _, ok := readRun(text[start:end], false, func(from, to int) {
		for k := start + from; k < start+to; k++ {
			more = more || read[at[k]] != removedByRun
		}
	})
	return ok && !more
}

// userInfoPiece reports whether text holds a user:secret@ user information as step 8 reads one
// across whitespace: an '@' before a byte a host can start with (hostByte), the longest run of
// user-information bytes (userInfoByte) before it, and in that run a ':' after a key's '=' (one
// that is not text's first byte), or a ':' anywhere in it when a '"' after a '=' comes right
// before it (proxy="user: pw@host"); a "'" is a user-information byte, so proxy='user: pw@host'
// reads as the first. A run with no key before its ':' is a label, a ':' and an address.
func userInfoPiece(text string) bool {
	for a := 0; a+1 < len(text); a++ {
		if text[a] != '@' || !hostByte(text[a+1]) {
			continue
		}
		from := a
		for from > 0 && userInfoByte(text[from-1]) {
			from--
		}
		colon := strings.LastIndexByte(text[from:a], ':')
		if colon < 0 {
			continue
		}
		colon += from
		if eq := strings.IndexByte(text[from:colon], '='); eq >= 0 && from+eq > 0 {
			return true
		}
		if from >= 2 && text[from-1] == '"' && text[from-2] == '=' {
			return true
		}
	}
	return false
}

// userInfoByte reports whether c is a byte RFC 3986 lets a user information hold: unreserved
// (a letter, a digit, '-', '.', '_', '~'), a sub-delimiter (!$&'()*+,;=), ':' or the '%' of an
// escape.
func userInfoByte(c byte) bool {
	return letter(c) || '0' <= c && c <= '9' || strings.IndexByte("-._~!$&'()*+,;=:%", c) >= 0
}

// hostByte reports whether c can start the host a user information's '@' comes before: a byte
// RFC 3986 lets a host name hold, other than ':', or the '[' of an IPv6 address.
func hostByte(c byte) bool { return c != ':' && userInfoByte(c) || c == '[' }

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
// fragment, and whether it is readable, and calls cut with each span of text, [from, to), that
// it removes: the user information with its '@', each value, and each bare segment. checkPort
// is whether a port that is not all digits makes the run unreadable: step 8's second reading
// does not ask, since a gap-free URL with no path runs into the next word there.
func readRun(text string, checkPort bool, cut func(from, to int)) (shown string, length int, inQuery, ok bool) {
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
		if runRefuses(run[i]) {
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
	at := strings.IndexByte(authority, '@')
	host := authority[at+1:]
	if checkPort && !digitPort(host) {
		return "", 0, false, false
	}
	if at >= 0 {
		cut(head, head+at+1)
	}
	path, query, fragment := after, "", ""
	queryAt, fragmentAt := head+authEnd, head+authEnd
	if i := strings.IndexByte(after, '#'); i >= 0 {
		path, fragment = after[:i], after[i:]
		fragmentAt += i
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path, query = path[:i], path[i:]
		queryAt += i
	}
	var b strings.Builder
	b.WriteString(run[:head])
	b.WriteString(host)
	b.WriteString(path)
	for _, part := range []struct {
		text string
		at   int
	}{{query, queryAt}, {fragment, fragmentAt}} {
		if part.text == "" {
			continue
		}
		b.WriteByte(part.text[0])
		segments, at := part.text[1:], part.at+1
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
			switch {
			case named:
				b.WriteString(name)
				b.WriteByte('=')
				if value != "" {
					cut(at+len(name)+1, at+len(segment))
				}
			case segment != "":
				cut(at, at+len(segment))
			}
			if end < 0 {
				break
			}
			b.WriteByte(segments[end])
			segments, at = segments[end+1:], at+end+1
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

// runRefuses reports whether a run that holds c is not readable (Remote's step 3): a '%', a '\',
// a quote, '<', '>', a backtick, a control character, or a byte of a non-ASCII character.
func runRefuses(c byte) bool {
	return c < ' ' || c >= 0x7f || strings.IndexByte("%\\\"'<>`", c) >= 0
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
