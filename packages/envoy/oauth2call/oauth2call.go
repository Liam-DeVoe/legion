// Package oauth2call is the library boundary of golang.org/x/oauth2 (internal/redact's OAuth2)
// for a module outside this one, which cannot import an internal package: the daemon's launcher,
// say. Every call of x/oauth2, or of a subpackage of it, that can answer an error is written
// oauth2call.Call(call(...)) there, as it is redact.OAuth2(call(...)) here, and the boundary census
// (internal/redact/testdata/wholeuse/boundary) accepts a library call only as the sole argument
// of one of the two. Every HTTP client such a module builds or hands a library is
// oauth2call.RefusalClient's, as it is redact.RefusalClient's here.
package oauth2call

import (
	"net/http"

	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/internal/redact"
)

// Call is redact.OAuth2: the value, and the error with every answer x/oauth2 returned in it held
// as the rule reads its words.
func Call[T any](value T, err error) (T, error) { return redact.OAuth2(value, err) }

// TokenSource is redact.TokenSource: source behind the boundary, for a token source handed to a
// function or a struct field of another module, which can ask it for a token and print the refusal
// it answers.
func TokenSource(source oauth2.TokenSource) *redact.BoundTokenSource {
	return redact.TokenSource(source)
}

// RefusalClient is redact.RefusalClient: base, whose answers outside 2xx hold none of the
// credentials their own request carried, for every HTTP client a module outside this one builds or
// hands a library.
func RefusalClient(base *http.Client) *http.Client { return redact.RefusalClient(base) }
