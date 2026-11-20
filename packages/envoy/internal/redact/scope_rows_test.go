package redact_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/sjawhar/envoy/internal/redact"
)

// scopeRoundTripper answers every request with a status line and a body the server controls.
type scopeRoundTripper struct {
	status int
	reason func(*http.Request) string
	body   func(*http.Request) string
}

func (t scopeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.status,
		Status:     fmt.Sprintf("%d %s", t.status, t.reason(r)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": {"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(t.body(r))),
		Request:    r,
	}, nil
}

func scopedDiscovery(ctx context.Context, client *http.Client) (*gooidc.Provider, error) {
	s := redact.Scope[*gooidc.Provider](ctx)
	return redact.OAuth2(s.Read(gooidc.NewProvider(gooidc.ClientContext(s.Context(), client), "https://idp.example")))
}

// TestScopedLibraryErrorsKeepTheirOwnRecord proves that records travel with the answer, never by
// matching its words. Two separate refusals share a gateway preamble and a later "Forbidden" one
// happens before the first reaches the boundary; the first error redacts only its own bearer.
func TestScopedLibraryErrorsKeepTheirOwnRecord(t *testing.T) {
	const first = "scope-first-RDX-7e19"
	const second = "scope-second-RDX-3c5a"
	page := func(r *http.Request) string {
		return "gateway preamble shared by every refusal; it carried " + r.Header.Get("Authorization")
	}
	refused := func(token string) error {
		s := redact.Scope[*gooidc.Provider](context.Background())
		inner := redact.RefusalClient(&http.Client{Transport: scopeRoundTripper{
			status: http.StatusForbidden,
			reason: func(*http.Request) string { return "Forbidden" },
			body:   page,
		}})
		client := s.Client(oauth2.NewClient(
			context.WithValue(s.Context(), oauth2.HTTPClient, inner),
			oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}),
		))
		_, err := gooidc.NewProvider(gooidc.ClientContext(s.Context(), client), "https://idp.example")
		if err == nil {
			t.Fatal("discovery answered no error")
		}
		_, err = redact.OAuth2(s.Read(nil, fmt.Errorf("%w; unrelated %s", err, second)))
		return err
	}
	firstErr := refused(first)
	if firstErr == nil {
		t.Fatal("first discovery answered no error")
	}
	// A later short answer has no relationship to the first error. A text lookup would select it.
	later := redact.RefusalClient(&http.Client{Transport: scopeRoundTripper{
		status: http.StatusForbidden,
		reason: func(*http.Request) string { return "Forbidden" },
		body:   func(*http.Request) string { return "Forbidden" },
	}})
	response, err := later.Get("https://other.example/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	out := firstErr.Error()
	t.Logf("REDACT-CAPTURE redact.record-interleaved %q", out)
	if strings.Contains(out, first) {
		t.Fatalf("REDACT-ASSERT redact.record-interleaved: %q", out)
	}
	if !strings.Contains(out, second) {
		t.Fatalf("the first record redacted a credential another request carried: %q", out)
	}
}

// TestAnEmptyRefusalStatusLineUsesItsOwnRecord exercises a body of nothing and a status phrase
// that echoes the bearer: an error can name the phrase even when it reads no body.
func TestAnEmptyRefusalStatusLineUsesItsOwnRecord(t *testing.T) {
	const bearer = "status-bearer-RDX-7e19"
	client := redact.RefusalClient(&http.Client{Transport: scopeRoundTripper{
		status: http.StatusForbidden,
		reason: func(r *http.Request) string { return "gateway echoed " + r.Header.Get("Authorization") },
		body:   func(*http.Request) string { return "" },
	}})
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	s := redact.Scope[*gooidc.Provider](ctx)
	_, err := redact.OAuth2(s.Read(gooidc.NewProvider(gooidc.ClientContext(s.Context(), client), "https://idp.example")))
	if err == nil {
		t.Fatal("discovery answered no error")
	}
	out := err.Error()
	t.Logf("REDACT-CAPTURE redact.status-empty-body %q", out)
	if strings.Contains(out, bearer) {
		t.Fatalf("REDACT-ASSERT redact.status-empty-body: %q", out)
	}
}

// TestRedirectHopsKeepTheCredentialsOfEarlierRequests proves that a token endpoint redirect to a
// Location path carrying its refresh token, whether it reaches a dead port or a second hop that
// echoes the path, redacts that token from the error.
func TestRedirectHopsKeepTheCredentialsOfEarlierRequests(t *testing.T) {
	const refresh = "redirect-refresh-RDX-7e19"
	mode := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/echo/") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "the second hop echoed "+r.URL.Path)
			return
		}
		_ = r.ParseForm()
		switch mode {
		case "dead-port":
			w.Header().Set("Location", "http://127.0.0.1:1/"+r.PostForm.Get("refresh_token")+"/token")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "second-hop":
			w.Header().Set("Location", "/echo/"+r.PostForm.Get("refresh_token"))
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	for _, testMode := range []string{"dead-port", "second-hop"} {
		t.Run(testMode, func(t *testing.T) {
			mode = testMode
			s := redact.Scope[*oauth2.Token](context.Background())
			config := &oauth2.Config{ClientID: "probe", Endpoint: oauth2.Endpoint{TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInParams}}
			_, err := redact.OAuth2(s.Read(config.TokenSource(s.Context(), &oauth2.Token{RefreshToken: refresh}).Token()))
			if err == nil {
				t.Fatal("refresh answered no error")
			}
			out := err.Error()
			t.Logf("REDACT-CAPTURE redact.redirect-%s %q", testMode, out)
			if strings.Contains(out, refresh) {
				t.Fatalf("REDACT-ASSERT redact.redirect-%s: %q", testMode, out)
			}
		})
	}
}
