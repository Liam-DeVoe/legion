// broker_test.go drives the client under dispatch_token_broker against the real secrets broker
// (packages/envoy's own httptest server, mounted through brokertest.NewRig) and a stand-in for
// Dispatch that records what reaches it. It skips without BROKER_TEST_DATABASE_URL (the rig's own
// skip), except on GitHub Actions, where the daemon's test job sets it and a skip would hide that
// it stopped (newBrokerRig).
//
// It is the only file of this package that imports anything under github.com/sjawhar/envoy/...,
// and only through the public facades, because Go's internal-package rule refuses this module an
// import under github.com/sjawhar/envoy/internal/...: the client itself takes a RoundTripper and
// imports none of it.
package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/brokerclient"
	"github.com/sjawhar/envoy/brokertest"
)

// newBrokerRig is brokertest.NewRig, failing rather than skipping on GitHub Actions when
// BROKER_TEST_DATABASE_URL is unset: ci.yaml's daemon-go job names its Postgres service there, and
// a silent skip would let this test stop running unnoticed.
func newBrokerRig(t *testing.T) *brokertest.Rig {
	t.Helper()
	if os.Getenv("BROKER_TEST_DATABASE_URL") == "" && os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatal("BROKER_TEST_DATABASE_URL is unset on GitHub Actions: ci.yaml's daemon-go job names its Postgres service")
	}
	return brokertest.NewRig(t)
}

// wireRequest is one request as the stand-in for Dispatch received it: everything the daemon's
// request carries that the change of transport could touch.
type wireRequest struct {
	Method, URL, Accept, Authorization, ContentType, Proof, Body string
}

// recordingDispatch is a stand-in for Dispatch that answers every request 200 `{}` and keeps it.
type recordingDispatch struct {
	*httptest.Server
	mu       sync.Mutex
	requests []wireRequest
}

func newRecordingDispatch(t *testing.T) *recordingDispatch {
	t.Helper()
	d := &recordingDispatch{}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the request body: %v", err)
		}
		d.mu.Lock()
		d.requests = append(d.requests, wireRequest{
			Method: r.Method, URL: "http://" + r.Host + r.URL.RequestURI(), Accept: r.Header.Get("Accept"),
			Authorization: r.Header.Get("Authorization"), ContentType: r.Header.Get("Content-Type"),
			Proof: r.Header.Get("DPoP"), Body: string(body),
		})
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(d.Close)
	return d
}

// last is the request the stand-in received last.
func (d *recordingDispatch) last(t *testing.T) wireRequest {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		t.Fatal("Dispatch received no request")
	}
	return d.requests[len(d.requests)-1]
}

// TestTheDaemonCallsDispatchWithItsOwnBrokerToken: under dispatch_token_broker the daemon's client
// presents a token the broker traded for the daemon's own pod identity, as `Authorization: DPoP
// <token>` beside a proof of the call's own method and URL whose ath is that token's hash, and the
// broker reads that token back as the session legion-daemon:EXAMPLE, the daemon's session of the
// project its entry names. With the setting off, the same call reaches Dispatch with `Bearer <the
// file's token>` and is otherwise the request it always was: method, URL, Accept, Content-Type and
// body, and no proof.
func TestTheDaemonCallsDispatchWithItsOwnBrokerToken(t *testing.T) {
	rig := newBrokerRig(t)
	server := newRecordingDispatch(t)
	const key, notice = "EXAMPLE-7", "Pull request checks are blocked."
	target := server.URL + "/api/v1/issues/" + key + "/messages"
	wantBody := `{"body":"Pull request checks are blocked.","actor":{"kind":"session","id":"legion-daemon:EXAMPLE"}}`

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(rig.MintSubjectToken(t, brokertest.DaemonExchangeAccount, "daemon-pod-uid")), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{brokerclient.IdentityVar: brokerclient.IdentityKubernetes, brokerclient.MachineTokenFileVar: tokenFile}
	getenv := func(name string) string { return environment[name] }
	identity, err := brokerclient.MachineIdentity(getenv)
	if err != nil {
		t.Fatal(err)
	}
	proved, err := brokerclient.MachineKey(getenv)
	if err != nil {
		t.Fatal(err)
	}
	client, err := brokerclient.New(brokerclient.Config{
		BrokerURL: rig.URL, Key: proved, Subject: identity, Session: DaemonSession(key),
		NoLauncherCredential: true, HTTP: brokerclient.HTTPClient(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := New(server.URL, brokerclient.NewTokenTransport(client, http.DefaultTransport)).PostMessage(context.Background(), key, notice); err != nil {
		t.Fatalf("post the notice under the broker's token: %v", err)
	}
	brokered := server.last(t)
	token, found := strings.CutPrefix(brokered.Authorization, "DPoP ")
	if !found || token == "" {
		t.Fatalf("Authorization = %q, want DPoP and the broker's token", brokered.Authorization)
	}
	claims := proofClaims(t, brokered.Proof)
	hash := sha256.Sum256([]byte(token))
	if claims["htm"] != http.MethodPost || claims["htu"] != target || claims["ath"] != base64.RawURLEncoding.EncodeToString(hash[:]) {
		t.Fatalf("the proof claims htm=%v htu=%v ath=%v, want POST, %s and the hash of the token beside it", claims["htm"], claims["htu"], claims["ath"], target)
	}
	if read := introspect(t, rig, token); !read.Active || read.Session != "legion-daemon:EXAMPLE" {
		t.Fatalf("the broker reads the daemon's token back as %+v, want a live token of the session legion-daemon:EXAMPLE", read)
	}

	// The control: the setting off, and the bearer dispatch_token_file names.
	const fileToken = "dispatch-file-token"
	if err := New(server.URL, BearerTransport(fileToken, http.DefaultTransport)).PostMessage(context.Background(), key, notice); err != nil {
		t.Fatalf("post the notice under the file's bearer: %v", err)
	}
	bearer := server.last(t)
	want := wireRequest{
		Method: http.MethodPost, URL: target, Accept: "application/json", Authorization: "Bearer " + fileToken,
		ContentType: "application/json", Body: wantBody,
	}
	if bearer != want {
		t.Fatalf("with the setting off Dispatch received %+v, want the request as it always was, %+v", bearer, want)
	}
	brokered.Authorization, brokered.Proof = want.Authorization, ""
	if brokered != want {
		t.Fatalf("under the broker's token Dispatch received %+v, want the same request but for its credential, %+v", brokered, want)
	}
}

// proofClaims is a DPoP proof's claims, read without checking its signature: the broker's own
// tests judge whether a proof verifies, and this one what it names.
func proofClaims(t *testing.T, compact string) map[string]any {
	t.Helper()
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		t.Fatalf("DPoP = %q, want a compact JWS", compact)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("the proof's claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("the proof's claims: %v", err)
	}
	return claims
}

// introspection is what this file reads of the broker's introspection answer.
type introspection struct {
	Active  bool   `json:"active"`
	Session string `json:"session"`
}

// introspect is the broker's reading of token, asked as the listener asks.
func introspect(t *testing.T, rig *brokertest.Rig, token string) introspection {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, rig.URL+"/v1/oauth/introspect", strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer "+rig.ListenerToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("introspect the daemon's token: %v", err)
	}
	defer response.Body.Close()
	var read introspection
	if err := json.NewDecoder(response.Body).Decode(&read); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("introspect the daemon's token: %d, %v", response.StatusCode, err)
	}
	return read
}
