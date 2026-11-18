package redact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"golang.org/x/oauth2"
)

// TestOAuth2HoldsEveryAnswerAsTheRuleReadsIt: an OAuth error answer x/oauth2 returns, alone, under
// the *url.Error net/http wraps a transport's error in, or under any other wrapper, comes out of
// OAuth2 as an *OAuthAnswer, which prints none of the answer's words whole, under any fmt verb,
// and through the *oauth2.RetrieveError errors.As still reaches, encoding/json included; its
// closed code and its "<CODE>: <message>" split are read from the words as they came, and its
// status and headers stay. An error that holds no answer, and nil, come back as they are. The
// input, printed whole, holds the words: the control.
func TestOAuth2HoldsEveryAnswerAsTheRuleReadsIt(t *testing.T) {
	answer := func() *oauth2.RetrieveError {
		return &oauth2.RetrieveError{
			Response: &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests",
				Header:  http.Header{"Retry-After": {"7"}},
				Request: &http.Request{URL: &url.URL{Scheme: "https", User: url.UserPassword("u", "not-a-secret-password"), Host: "h.example"}}},
			Body:             []byte(`{"error":"invalid_request","error_description":"RATE_LIMITED: ` + probeWords + `"}`),
			ErrorCode:        "invalid_request",
			ErrorDescription: "RATE_LIMITED: " + probeWords,
			ErrorURI:         probeWords,
		}
	}
	for name, err := range map[string]error{
		"alone":                answer(),
		"under a *url.Error":   &url.Error{Op: "Post", URL: "https://h.example/token", Err: answer()},
		"under a %w wrapper":   fmt.Errorf("oauth2: cannot fetch token: %w", answer()),
		"under an errors.Join": errors.Join(errors.New("first"), answer()),
	} {
		t.Run(name, func(t *testing.T) {
			if !holdsProbeSecret(fmt.Sprintf("%v", err)) {
				t.Fatalf("the control: %v prints none of the words", err)
			}
			_, got := OAuth2(struct{}{}, err)
			var held *OAuthAnswer
			var re *oauth2.RetrieveError
			if !errors.As(got, &held) || !errors.As(got, &re) {
				t.Fatalf("OAuth2 = %v, want an *OAuthAnswer over an *oauth2.RetrieveError", got)
			}
			data, _ := json.Marshal(re)
			for form, text := range map[string]string{"Error": got.Error(), "%v": fmt.Sprintf("%v", got), "%+v": fmt.Sprintf("%+v", got),
				"%#v": fmt.Sprintf("%#v", got), "the answer's %#v": fmt.Sprintf("%#v", re), "json.Marshal": string(data)} {
				if holdsProbeSecret(text) {
					t.Errorf("%s prints the words: %s", form, text)
				}
			}
			code, message := held.Refusal()
			if held.RefusalCode() != InvalidRequest || RefusalCode(got) != InvalidRequest || !code.Equal("RATE_LIMITED") || !message.Equal(probeWords) {
				t.Errorf("the closed code is %v and the split %v, %v; want invalid_request, RATE_LIMITED and the words", held.RefusalCode(), code, message)
			}
			if re.Response.StatusCode != http.StatusTooManyRequests || re.Response.Header.Get("Retry-After") != "7" || re.Response.Request != nil {
				t.Errorf("the answer's response is %+v, want its status and headers without its request", re.Response)
			}
		})
	}
	// Every other error a library call answers prints the rule's reading of its text: the
	// external-account exchange's error quotes the security token service's body, and go-oidc's
	// discovery error the discovery answer's; the errors as the libraries make them, printed, hold
	// the words: the control.
	for name, err := range map[string]error{
		"the external-account exchange's error": fmt.Errorf("oauth2/google: status code %d: %s", http.StatusBadRequest,
			`{"error":"invalid_grant","error_description":"`+probeWords+`"}`),
		"go-oidc's discovery error": fmt.Errorf("%s: %s", "500 Internal Server Error", probeWords),
	} {
		if !holdsProbeSecret(err.Error()) {
			t.Fatalf("the control: %s prints none of the words", name)
		}
		_, got := OAuth2(struct{}{}, err)
		var library *LibraryError
		if !errors.As(got, &library) || holdsProbeSecret(got.Error()) || holdsProbeSecret(fmt.Sprintf("%+v %#v", got, got)) {
			t.Errorf("%s: OAuth2 = %v, want a *LibraryError that prints none of the words", name, got)
		}
		if errors.Unwrap(got) != nil {
			t.Errorf("%s: errors.Unwrap hands back %v", name, errors.Unwrap(got))
		}
	}
	// It finds what the error found: an ended context, and the *url.Error a transport's failure
	// comes in, as errors.Is and errors.As find them in the error as the library made it.
	ended := &url.Error{Op: "Post", URL: "https://h.example/token", Err: context.DeadlineExceeded}
	_, got := OAuth2(0, ended)
	var asURL *url.Error
	if _, ok := got.(*LibraryError); !ok || !errors.Is(got, context.DeadlineExceeded) || !errors.As(got, &asURL) || asURL != ended {
		t.Errorf("OAuth2 of %v = %#v; want a *LibraryError that errors.Is finds DeadlineExceeded in and errors.As finds the *url.Error in", ended, got)
	}
	if got.Error() != ended.Error() {
		t.Errorf("OAuth2 of %v prints %q, want the text the rule leaves as it is", ended, got)
	}
	if value, got := OAuth2("value", nil); got != nil || value != "value" {
		t.Errorf("OAuth2 of a value and no error = %q, %v", value, got)
	}
}

// refusing is a token source whose Token is an OAuth error answer quoting the probe's URL.
type refusing struct{}

func (refusing) Token() (*oauth2.Token, error) {
	return nil, &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusBadRequest}, ErrorCode: "invalid_grant", ErrorDescription: probeWords}
}

// TestATokenSourceBehindTheBoundaryRefusesWithTheRulesReading: a token source handed to another
// library behind TokenSource answers its refusal as OAuth2 does, an *OAuthAnswer that prints none of
// the words, while the source itself prints them: the control. A source already behind the
// boundary is not wrapped again.
func TestATokenSourceBehindTheBoundaryRefusesWithTheRulesReading(t *testing.T) {
	if _, err := (refusing{}).Token(); !holdsProbeSecret(err.Error()) {
		t.Fatalf("the control: %v prints none of the words", err)
	}
	bound := TokenSource(refusing{})
	_, err := bound.Token()
	var answer *OAuthAnswer
	if !errors.As(err, &answer) || holdsProbeSecret(err.Error()) || RefusalCode(err) != InvalidGrant {
		t.Errorf("the bound source's Token = %v, want an *OAuthAnswer that prints none of the words", err)
	}
	if TokenSource(bound) != bound {
		t.Error("TokenSource wrapped a source already behind the boundary again")
	}
}
