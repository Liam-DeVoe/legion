package redact

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
)

// OAuth2 is the one way this repository calls golang.org/x/oauth2, the subpackages of it it
// imports, and github.com/coreos/go-oidc: every call of theirs that can answer an error is written
// redact.OAuth2(call(...)), or redact.OAuth2(struct{}{}, call(...)) for a call that answers an
// error alone, and the census in testdata/wholeuse/boundary, which reads the libraries' own
// exported API, fails on any call that is not. An OAuth error answer x/oauth2 returns
// (*oauth2.RetrieveError) carries the other party's words in its code, description, error_uri and
// body, and its Error prints them; OAuth2 turns it into an *OAuthAnswer before the caller sees it,
// under a *url.Error that holds it as well, whose operation and URL the rule reads. An error that
// holds one under any other wrapper becomes the answer alone, since that wrapper's words can quote
// it. Every other error becomes a *LibraryError, which prints the rule's reading of its text and
// answers errors.Is and errors.As as it does: go-oidc's discovery error and the external-account
// exchange's quote the answer's body, and a verifier's quotes a token's claims. Every field of an
// error OAuth2 builds is a closed value or the rule's reading. The value comes back as it is.
func OAuth2[T any](value T, err error) (T, error) {
	return value, oauthAnswerOf(err)
}

// oauthAnswerOf is err as OAuth2 hands it on.
func oauthAnswerOf(err error) error {
	switch e := err.(type) {
	case nil:
		return nil
	case *OAuthAnswer, *LibraryError:
		return err
	case *oauth2.RetrieveError:
		return newOAuthAnswer(e)
	}
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		text := err.Error()
		return &LibraryError{text: NewRemoteText(Tokens(text, -1, refused.quotedBy(text)...)), err: &err}
	}
	if ue, ok := err.(*url.Error); ok {
		if inner := oauthAnswerOf(ue.Err); inner != ue.Err {
			// Op and URL are the request's, whose URL a redirect the answering party named can
			// set: the rule reads both.
			return &url.Error{Op: ruled(ue.Op), URL: ruled(ue.URL), Err: inner}
		}
		return err
	}
	var held *OAuthAnswer
	if errors.As(err, &held) {
		return held
	}
	return newOAuthAnswer(re)
}

// LibraryError is an error a library call across OAuth2 answered that holds no OAuth error answer:
// a transport's failure, an ended context, go-oidc's discovery error, which quotes the answer's
// body (`<status>: <body>`), the external-account exchange's (`oauth2/google: status code <n>:
// <body>`), a verifier's, which quotes a token's claims. Its Error is the rule's reading of the
// error's text (Field). It answers errors.Is and errors.As as the error does, through Is and As,
// and has no Unwrap, so errors.Unwrap cannot hand back the error and the text it prints. It holds
// the error behind a pointer to the interface, which fmt prints as an address under every verb at
// every depth, as RemoteText's *string: a plain error field, or a pointer to a struct holding one,
// lets fmt's reflection spell out a library error of a value type, its text and all.
type LibraryError struct {
	text RemoteText
	err  *error
}

// Error is the rule's reading of the library's error.
func (e *LibraryError) Error() string { return e.text.String() }

// Is is errors.Is of the library's error.
func (e *LibraryError) Is(target error) bool { return errors.Is(*e.err, target) }

// As is errors.As of the library's error.
func (e *LibraryError) As(target any) bool { return errors.As(*e.err, target) }

// BoundTokenSource is an oauth2.TokenSource as this repository hands it to another library - a
// Secret Manager client, an http.Client oauth2.NewClient builds, a google.Credentials: its Token is
// the source's Token through OAuth2, so a refusal that library prints in an error of its own holds
// only the rule's reading of the answer's words. The census fails on a token source handed to a
// function or a struct field of another module that is not one. TokenSource makes one.
type BoundTokenSource struct{ source oauth2.TokenSource }

// TokenSource is source behind the boundary; a source that already is one comes back as it is.
func TokenSource(source oauth2.TokenSource) *BoundTokenSource {
	if bound, ok := source.(*BoundTokenSource); ok {
		return bound
	}
	return &BoundTokenSource{source: source}
}

// Token is the source's Token through OAuth2.
func (s *BoundTokenSource) Token() (*oauth2.Token, error) { return OAuth2(s.source.Token()) }

// OAuthAnswer is an OAuth error answer x/oauth2 returned, as OAuth2 holds it once it has crossed
// the boundary. Its code and description are RemoteText, read only through RefusalCode and
// Refusal, which split the device authorization endpoint's "<CODE>: <message>" before either part
// is held (NewOAuthRefusal). It unwraps to an *oauth2.RetrieveError every field of which is a
// closed value or the rule's reading: the rule's reading of the code, the description and the
// error_uri, the body as a message may print it (Field), the answer's status code with its
// standard reason phrase (statusLine) and its Retry-After as the number or date it parses as
// (retryAfter), and no other header and no request, so errors.As, the status and Retry-After
// reads, and this package's printing read it as the answer it is, and no reader reaches the
// words. The status line the answering party wrote is not kept: x/oauth2's text of an answer with
// no error code prints it. The body is read with every credential the refused request carried
// redacted first (Tokens, requestTokens): something in the token endpoint's place, a gateway or a
// proxy, can answer with a page of its own that echoes the request back, its headers and its form,
// and the rule reads only the URLs in a field. Its Error is x/oauth2's text of that reading.
type OAuthAnswer struct {
	code           RemoteText
	refusalCode    RemoteText
	refusalMessage RemoteText
	answer         *oauth2.RetrieveError
}

// newOAuthAnswer is re as OAuth2 holds it. Every word of the answer it prints, its code,
// description, error_uri and body, is read with every credential the refused request carried
// redacted first (Tokens, requestTokens), then by the rule; the closed code (RefusalCode) is read
// from the code as it came, which prints nothing, so a request value that shares a run with a code
// cannot turn the code into one this client does not name.
func newOAuthAnswer(re *oauth2.RetrieveError) *OAuthAnswer {
	var carried []string
	if re.Response != nil {
		carried = requestTokens(re.Response.Request)
	}
	refusalCode, refusalMessage := NewOAuthRefusal(re.ErrorCode, re.ErrorDescription, carried...)
	answer := &oauth2.RetrieveError{
		ErrorCode:        ruled(Tokens(re.ErrorCode, -1, carried...)),
		ErrorDescription: ruled(Tokens(re.ErrorDescription, -1, carried...)),
		ErrorURI:         ruled(Tokens(re.ErrorURI, -1, carried...)),
	}
	if len(re.Body) > 0 {
		// An HTML page can write a URL's separators as character references, which a reader of
		// the page decodes: the rule reads the body as that reader does (UnescapeHTML).
		answer.Body = []byte(Field(Tokens(UnescapeHTML(string(re.Body)), -1, carried...)))
	}
	if r := re.Response; r != nil {
		answer.Response = &http.Response{Status: statusLine(r.StatusCode), StatusCode: r.StatusCode, Header: retryAfter(r.Header)}
	}
	return &OAuthAnswer{code: NewRemoteText(re.ErrorCode), refusalCode: refusalCode, refusalMessage: refusalMessage, answer: answer}
}

// statusLine is the status line of an answer with status code: the code and its standard reason
// phrase (http.StatusText), never the phrase the answering party wrote, which can quote a
// credential's URL and which x/oauth2 prints for an answer with no error code.
func statusLine(code int) string {
	return strings.TrimSpace(strconv.Itoa(code) + " " + http.StatusText(code))
}

// retryAfter is h's Retry-After, the one header of a refusal this repository reads (the helper's
// wait after a 429, serverwait.Read), rebuilt from what it parses as, as serverwait reads it: a
// count of seconds, one past an int64 as the largest or smallest, or an HTTP date. A header with
// no Retry-After, or one that is neither, is nil.
func retryAfter(h http.Header) http.Header {
	value := h.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil || errors.Is(err, strconv.ErrRange) {
		return http.Header{"Retry-After": {strconv.FormatInt(seconds, 10)}}
	}
	if at, err := http.ParseTime(value); err == nil {
		return http.Header{"Retry-After": {at.UTC().Format(http.TimeFormat)}}
	}
	return nil
}

// Error is x/oauth2's text of the rule's reading of the answer.
func (a *OAuthAnswer) Error() string { return a.answer.Error() }

// Unwrap is the rule's reading of the answer as x/oauth2's own type.
func (a *OAuthAnswer) Unwrap() error { return a.answer }

// RefusalCode is the answer's code read as the closed OAuthCode, from the code as it came.
func (a *OAuthAnswer) RefusalCode() OAuthCode { return a.code.OAuthCode() }

// Refusal is the answer's code and description as NewOAuthRefusal splits them.
func (a *OAuthAnswer) Refusal() (code, message RemoteText) { return a.refusalCode, a.refusalMessage }
