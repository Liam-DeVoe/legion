package resultgrant

import (
	"context"
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/broker/requests"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fallbackSpy struct{ calls int }

func (f *fallbackSpy) AirlockConnection(context.Context, string) (requests.AirlockConnection, error) {
	f.calls++
	return requests.AirlockConnection{GrantID: "ordinary"}, nil
}

func (f *fallbackSpy) AirlockValues(context.Context, string) (map[string]requests.AirlockSecret, string, error) {
	f.calls++
	return map[string]requests.AirlockSecret{}, "ordinary", nil
}

func (f *fallbackSpy) AirlockAccessPlaceholder(context.Context, string, string, string) (string, error) {
	f.calls++
	return "ordinary", nil
}

func (*fallbackSpy) PolicyVersion() string { return "ordinary" }

func rig(t *testing.T, ordinary bool) (*Manager, string, *time.Time) {
	t.Helper()
	token := rand.Text()
	now := time.Now()
	var thumb string
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("subject_token_type") != oauthwire.SubjectTokenTypeGitHub || r.Form.Get("subject_token") != "signed-job" {
			t.Fatalf("wrong subject: %v", r.Form)
		}
		proof, err := jose.ParseSigned(r.Header.Get("DPoP"), []jose.SignatureAlgorithm{jose.ES256})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := proof.Signatures[0].Header.JSONWebKey.Thumbprint(crypto.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		thumb = base64.RawURLEncoding.EncodeToString(raw)
		body, _ := json.Marshal(map[string]any{"access_token": token, "token_type": "DPoP", "expires_in": 600, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token"})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	manager, err := New(Options{BrokerURL: brokeraddr.New("https://broker.test"), ProxyURL: "http://proxy.test", CACertificate: "public-ca", HTTP: client, Now: func() time.Time { return now }, Rules: airlock.Rules{Secrets: []airlock.SecretRule{{Name: Name, Hosts: []string{"dispatch.test"}, Kind: airlock.KindDPoP}}}, Introspect: func(context.Context, string) (oauthwire.Introspection, error) {
		scope, projectScope := oauthwire.ScopeResults, oauthwire.ProjectScopeResults
		if ordinary {
			scope, projectScope = "", oauthwire.ProjectScopeNamed
		}
		return oauthwire.Introspection{Active: true, Session: "job", Person: "ci@example.com", ClientID: oauthwire.ExchangeClientID, Scope: scope, ProjectScope: projectScope, Projects: []string{"ACME"}, CI: &oauthwire.CIRun{}, Thumbprint: thumb, ExpiresAt: now.Add(time.Minute).Unix()}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return manager, token, &now
}

func TestOnlyPlaceholderLeavesBroker(t *testing.T) {
	manager, token, _ := rig(t, false)
	release, err := manager.Create(context.Background(), "signed-job")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(release)
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "PRIVATE KEY") {
		t.Fatal("credential escaped in release")
	}
	placeholder := release.Airlock.Names[Name].Value
	if !strings.HasPrefix(placeholder, requests.PlaceholderPrefix) || len(placeholder) != len(requests.PlaceholderPrefix)+32 {
		t.Fatal("not an airlock placeholder")
	}
	conn, err := manager.AirlockConnection(context.Background(), placeholder)
	if err != nil || conn.GrantID != release.GrantID {
		t.Fatalf("grant missing: %+v %v", conn, err)
	}
	var sent int
	transport, err := manager.DPoPTransport(context.Background(), release.GrantID, roundTrip(func(r *http.Request) (*http.Response, error) {
		sent++
		if r.Header.Get("Authorization") != "DPoP "+token || r.Header.Get("DPoP") == "" || r.Header.Get("DPoP") == "child-proof" {
			t.Fatal("proxy did not present its own proof")
		}
		return &http.Response{StatusCode: 204, Body: http.NoBody, Header: http.Header{}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("POST", "https://dispatch.test/api/v1/projects/ACME/results", nil)
	request.Header.Set("Authorization", "DPoP "+token)
	request.Header.Set("DPoP", "child-proof")
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	request.Header.Set("Authorization", "DPoP "+rand.Text())
	if _, err := transport.RoundTrip(request); err == nil || sent != 1 {
		t.Fatal("transport signed a credential not owned by its grant")
	}
}

func TestOrdinaryCredentialCannotBecomeResultGrant(t *testing.T) {
	manager, _, _ := rig(t, true)
	if _, err := manager.Create(context.Background(), "signed-job"); !errors.Is(err, ErrNotResults) {
		t.Fatalf("ordinary token admitted: %v", err)
	}
}

func TestExpiredGrantNeverChangesIdentityOrFallsThrough(t *testing.T) {
	manager, _, now := rig(t, false)
	release, err := manager.Create(context.Background(), "signed-job")
	if err != nil {
		t.Fatal(err)
	}
	fallback := &fallbackSpy{}
	manager.options.Fallback = fallback
	// Show that the fallback is reachable for an ordinary placeholder before testing that
	// this known, expired CI grant cannot become an ordinary grant.
	ordinary, err := manager.AirlockConnection(context.Background(), "ordinary-placeholder")
	if err != nil || ordinary.GrantID != "ordinary" || fallback.calls != 1 {
		t.Fatalf("ordinary fallback control: %+v %v calls=%d", ordinary, err, fallback.calls)
	}
	fallback.calls = 0
	*now = now.Add(2 * time.Minute)
	if _, err := manager.AirlockConnection(context.Background(), release.Airlock.Names[Name].Value); !errors.Is(err, requests.ErrGrantNotLive) {
		t.Fatalf("expired grant admitted: %v", err)
	}
	if _, _, err := manager.AirlockValues(context.Background(), release.GrantID); !errors.Is(err, requests.ErrGrantNotLive) {
		t.Fatalf("expired values admitted: %v", err)
	}
	if _, err := manager.DPoPTransport(context.Background(), release.GrantID, nil); !errors.Is(err, requests.ErrGrantNotLive) {
		t.Fatalf("expired signer admitted: %v", err)
	}
	if fallback.calls != 0 {
		t.Fatalf("expired CI grant fell through to an ordinary grant %d times", fallback.calls)
	}
}
