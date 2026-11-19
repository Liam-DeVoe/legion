package redact

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// TokenPiece is the shortest run of a credential's characters Tokens redacts wherever it stands.
// Something in a server's place, a gateway or a proxy, can echo the request back with a credential
// cut short, split by markup, or written twice with the copies overlapping (a token can end in its
// own first characters), so redacting only whole copies leaves most of one behind; a credential
// shorter than this is redacted only whole.
const TokenPiece = 8

// Redacted is what Tokens writes in place of a run of a credential's characters.
const Redacted = "[redacted]"

// Tokens is text with each run of at least TokenPiece characters of any of tokens (trimmed, as a
// header sends it) replaced with one Redacted, for text another party wrote that can echo a
// request back: a refusal's body, its reason phrase. A token shorter than TokenPiece is redacted
// only whole, and only where it stands as a token of its own, with no letter, digit or '_' right
// before or after it: a one-letter user name leaves "invalid_request" as it is. A character is a
// code point, and a byte that is not UTF-8 counts as one.
//
// limit bounds the text read, in characters; a negative limit reads it whole. Every run that
// starts inside the limit is redacted whole, one that runs past it read up to TokenPiece-1
// characters beyond, and the answer is cut to limit characters afterwards, since redaction changes
// the length; a run that starts past the limit is never reached, so nothing from past it moves in.
// The work is linear in the text read: each character is one lookup for each distinct piece
// length, at most TokenPiece of them, however many tokens or pieces there are.
func Tokens(text string, limit int, tokens ...string) string {
	pieces := map[string]struct{}{}
	lengths := map[int]struct{}{}
	for _, token := range tokens {
		characters := []rune(strings.TrimSpace(token))
		length := min(TokenPiece, len(characters))
		if length == 0 {
			continue
		}
		lengths[length] = struct{}{}
		for at := range len(characters) - length + 1 {
			pieces[string(characters[at:at+length])] = struct{}{}
		}
	}
	whole := limit < 0
	if whole {
		limit = utf8.RuneCountInString(text)
	}
	if len(pieces) == 0 {
		return text[:characterOffset(text, limit)]
	}
	// starts is the byte offset of each character read (the limit's and TokenPiece-1 more, for a
	// run that starts inside it), and one past the last.
	starts := make([]int, 0, min(len(text), limit+TokenPiece)+1)
	scanned := len(text)
	for at := range text {
		if len(starts) == limit+TokenPiece-1 {
			scanned = at
			break
		}
		starts = append(starts, at)
	}
	starts = append(starts, scanned)
	covered := make([]bool, scanned)
	for at := range len(starts) - 1 {
		for length := range lengths {
			if at+length >= len(starts) {
				continue
			}
			from, to := starts[at], starts[at+length]
			if _, ok := pieces[text[from:to]]; ok && (length == TokenPiece || !wordBefore(text, from) && !wordAt(text, to)) {
				for b := from; b < to; b++ {
					covered[b] = true
				}
			}
		}
	}
	end := characterOffset(text, limit)
	var out strings.Builder
	kept := 0
	for at := 0; at < end; at++ {
		if !covered[at] {
			continue
		}
		run := at
		for run < scanned && covered[run] {
			run++
		}
		out.WriteString(text[kept:at])
		out.WriteString(Redacted)
		kept, at = run, run
	}
	if kept < end {
		out.WriteString(text[kept:end])
	}
	answer := out.String()
	if whole {
		return answer
	}
	return answer[:characterOffset(answer, limit)]
}

// wordBefore reports whether the character that ends text[:at] is a letter, a digit or '_'.
func wordBefore(text string, at int) bool {
	r, size := utf8.DecodeLastRuneInString(text[:at])
	return size > 0 && wordRune(r)
}

// wordAt reports whether the character that starts text[at:] is a letter, a digit or '_'.
func wordAt(text string, at int) bool {
	r, size := utf8.DecodeRuneInString(text[at:])
	return size > 0 && wordRune(r)
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// characterOffset is the byte offset in s just past its first n characters, or len(s) where it
// holds no more than n; a byte that is not UTF-8 counts as one character.
func characterOffset(s string, n int) int {
	for at := range s {
		if n == 0 {
			return at
		}
		n--
	}
	return len(s)
}

// requestTokens is every credential r carries, for Tokens: every value it carries under a name a
// wire contract does not declare public (publicRequestNames). That is the whole value of each such
// header (a Cookie its client's jar added, an instance metadata token, a proof, whatever its
// name), with the part after a scheme where the value names one and a Basic pair's user and
// password, each also unescaped (x/oauth2 escapes them); the user and password of its URL and each
// such value of its query; and each such value of its form or string of its JSON body, read
// through GetBody so the request is left as it was, a subject or an actor token among them. A name
// nobody listed counts, so a new parameter is a credential until a contract declares it public. A
// request without GetBody names no body value; every one net/http or x/oauth2 builds has it.
func requestTokens(r *http.Request) []string {
	if r == nil {
		return nil
	}
	var tokens []string
	for name, values := range r.Header {
		if publicHeader(name) {
			continue
		}
		for _, value := range values {
			tokens = append(tokens, value)
			scheme, credential, ok := strings.Cut(strings.TrimSpace(value), " ")
			if !ok {
				continue
			}
			tokens = append(tokens, credential)
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(credential))
			if !strings.EqualFold(scheme, "Basic") || err != nil {
				continue
			}
			if user, password, ok := strings.Cut(string(decoded), ":"); ok {
				tokens = append(tokens, escapedAndNot(user)...)
				tokens = append(tokens, escapedAndNot(password)...)
			}
		}
	}
	if r.URL != nil {
		if r.URL.User != nil {
			tokens = append(tokens, r.URL.User.Username())
			if password, ok := r.URL.User.Password(); ok {
				tokens = append(tokens, password)
			}
		}
		for name, values := range r.URL.Query() {
			if !publicField(name) {
				tokens = append(tokens, values...)
			}
		}
	}
	return append(tokens, requestBodyValues(r)...)
}

// escapedAndNot is s, and s unescaped where that differs.
func escapedAndNot(s string) []string {
	if unescaped, err := url.QueryUnescape(s); err == nil && unescaped != s {
		return []string{s, unescaped}
	}
	return []string{s}
}

// requestBodyValues is each value of r's form, or string of its JSON body, under a name no contract
// declares public, read again through GetBody (at most requestBodyLimit); none where r carries
// neither or cannot give its body again. A JSON string's name is the key of the object it is a
// member of, through any arrays between; an object under a public key is read by its own keys.
func requestBodyValues(r *http.Request) []string {
	if r.GetBody == nil {
		return nil
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	form := media == "application/x-www-form-urlencoded"
	if !form && media != "application/json" && !strings.HasSuffix(media, "+json") {
		return nil
	}
	body, err := r.GetBody()
	if err != nil {
		return nil
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, requestBodyLimit))
	if err != nil {
		return nil
	}
	var values []string
	if form {
		parsed, _ := url.ParseQuery(string(data))
		for name, v := range parsed {
			if !publicField(name) {
				values = append(values, v...)
			}
		}
		return values
	}
	var decoded any
	if json.Unmarshal(data, &decoded) != nil {
		return nil
	}
	var walk func(v any, public bool)
	walk = func(v any, public bool) {
		switch v := v.(type) {
		case string:
			if !public {
				values = append(values, v)
			}
		case []any:
			for _, e := range v {
				walk(e, public)
			}
		case map[string]any:
			for k, e := range v {
				walk(e, publicField(k))
			}
		}
	}
	walk(decoded, false)
	return values
}

// requestBodyLimit is the most of a request's body requestTokens reads again, the bound x/oauth2
// reads a token endpoint's answer to (golang.org/x/oauth2/internal/token.go).
const requestBodyLimit = 1 << 20

// RefusalClient is base (nil is http.DefaultClient), its timeout, redirect rule and cookie jar as
// they are, which records the credentials each refused request carried (Carried: every header's
// value, the URL's user information and query values, and every form or JSON body value) for every
// answer outside 2xx it gets, and changes nothing of any answer: the library that sent the request
// parses the answer as it came, so its code and every decision made on it are the answer's own.
// OAuth2 then reads the text of an error a library made of such an answer with every recorded
// credential redacted (LibraryError): a library builds its requests' credentials itself and prints
// a refusal's body in an error of text alone, with no request left to read them from, as
// golang.org/x/oauth2/google's external_account exchange prints `status code <n>: <body>` and
// google.golang.org/api/impersonate `impersonate: status code <n>: <body>`. It is the one wrapper on
// every HTTP client this change's boundary and clients build or hand a library; the census in
// cmd/redact-gate fails on one without it.
func RefusalClient(base *http.Client) *http.Client {
	client := new(http.Client)
	if base != nil {
		*client = *base
	}
	client.Transport = refusalTransport{client.Transport}
	return client
}

// refusalTransport sends each request through base (nil is http.DefaultTransport) and records the
// credentials of each one answered outside 2xx (refused.record).
type refusalTransport struct{ base http.RoundTripper }

func (t refusalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	tokens := requestTokens(r)
	response, err := base.RoundTrip(r)
	if err != nil || response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, err
	}
	// A RefusalClient beneath this one recorded the answer already, with the credentials its own
	// request carried, a bearer a transport between the two set among them: one answer is one
	// refusal, which this request's credentials join.
	if inner, ok := response.Body.(readCloser); ok {
		refused.join(inner.refusal, tokens)
		return response, nil
	}
	// The body is read and handed on as the same bytes, the read part first and the rest after,
	// so the library parses what was answered.
	read, _ := io.ReadAll(io.LimitReader(response.Body, requestBodyLimit))
	one := &refusal{tokens: tokens, quote: quoted(read)}
	response.Body = readCloser{io.MultiReader(bytes.NewReader(read), response.Body), response.Body, one}
	refused.record(one)
	return response, nil
}

// readCloser reads from Reader and closes Closer: a refused answer's body, with the refusal
// RefusalClient recorded of it, which a RefusalClient above it joins.
type readCloser struct {
	io.Reader
	io.Closer
	refusal *refusal
}

// quotedLength is how much of a refused answer's body, trimmed, an error must quote for the
// boundary to read it with that refusal's request's credentials redacted: a library that prints
// a body prints at least its start.
const quotedLength = 64

// quoted is the start of body an error that quotes it holds: its first quotedLength bytes,
// trimmed, or "" for a body of nothing but space.
func quoted(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	return string(trimmed[:min(len(trimmed), quotedLength)])
}

// refusal is what RefusalClient records of a refused answer: the credentials its request carried,
// as each RefusalClient it passed through saw them, and the start of its body an error that quotes
// it holds.
type refusal struct {
	tokens []string
	quote  string
}

// refusedKept is how many refusals refused keeps, the newest: more than a process makes between a
// library's refused request and the boundary's reading of its error.
const refusedKept = 1024

// refused is the latest refusals RefusalClient's transports saw, newest last.
var refused refusals

type refusals struct {
	mu   sync.Mutex
	kept []*refusal
}

func (r *refusals) record(one *refusal) {
	if one.quote == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kept = append(r.kept, one)
	if len(r.kept) > refusedKept {
		r.kept = r.kept[len(r.kept)-refusedKept:]
	}
}

// join adds tokens, the credentials an outer RefusalClient's request carried, to one.
func (r *refusals) join(one *refusal, tokens []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	one.tokens = append(one.tokens, tokens...)
}

// quotedBy is the credentials of the newest refusal whose answer text quotes: only an error that
// prints a refused answer can echo that request's credentials, so no other request's values touch
// it, and the work is one request's however many refusals came before.
func (r *refusals) quotedBy(text string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.kept) - 1; i >= 0; i-- {
		if strings.Contains(text, r.kept[i].quote) {
			return append([]string(nil), r.kept[i].tokens...)
		}
	}
	return nil
}

// Carried is every credential r carries (requestTokens), for a client of this repository that
// reads a refused answer itself: it holds the answer's words with these redacted
// (RemoteText.WithoutTokens) before it prints them.
func Carried(r *http.Request) []string { return requestTokens(r) }
