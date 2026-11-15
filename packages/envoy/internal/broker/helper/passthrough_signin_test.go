// packages/envoy/internal/broker/helper/passthrough_signin_test.go
//go:build linux

package helper

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// boxMachine is this machine's own identity, the actor token of a person's trade on it.
type boxMachine struct{}

func (boxMachine) Token(context.Context) (string, string, error) {
	return "this-machine's-identity-token", oauthwire.SubjectTokenTypeGoogle, nil
}

func (boxMachine) Describe() string { return "this machine" }

// signedInBox is the sign-in `agent-secrets login` leaves on a box: a person's device grant at
// an issuer, kept with a box key of its own, read through the REAL brokerclient.SignInAuthority
// that agent-secrets-helper serves its sessions with (cmd/agent-secrets-helper, signInAuthority).
// Its trades go to tokenURL.
func signedInBox(t *testing.T, tokenURL string) *brokerclient.SignInAuthority {
	t.Helper()
	const clientID, clientSecret, person = "example-device-client", "example-device-secret", "ada@example.com"
	issuer := oidctest.New(t)
	issuer.EnableDeviceFlow(issuer.PublishKey(t, "google-key"), clientID, clientSecret)
	issuer.SignInByDefault(map[string]any{"sub": person, "email": person, "email_verified": true})
	flow := &brokerclient.DeviceFlow{Issuer: issuer.URL(), ClientID: clientID, ClientSecret: clientSecret}
	authorization, err := flow.Start(context.Background())
	if err != nil {
		t.Fatalf("start the sign-in: %v", err)
	}
	if err := issuer.ApproveDeviceCode(authorization.UserCode); err != nil {
		t.Fatalf("type the code in: %v", err)
	}
	authorization.Interval = 1
	granted, err := flow.Wait(context.Background(), authorization)
	if err != nil {
		t.Fatalf("the sign-in: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	store := brokerclient.SignInStore{Dir: filepath.Join(t.TempDir(), "signins")}
	if err := store.Write(brokerclient.SignInRecord{
		Person: person, Issuer: flow.Issuer, ClientID: clientID, RefreshToken: granted.RefreshToken,
		BoxKey:     string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		Thumbprint: "thumbprint-of-" + person, SignedInAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("keep the sign-in: %v", err)
	}
	return &brokerclient.SignInAuthority{
		Store: store, Machine: boxMachine{}, Flow: flow, BrokerURL: tokenURL, HTTP: &http.Client{Timeout: 30 * time.Second},
	}
}

// TestAPassThroughOpAnswersInsideItsBoundWhileTheBoxsSignInIsTrading: A10 says the helper answers
// every request it accepted within the time its client waits, and that a pass-through call that
// runs out of time answers with its own failure code saying it timed out, so a client never
// reads an empty reply.
//
// A box a person has signed in on enrols every call under that person's authority
// (Broker.signingAuthority), and brokerclient.SignInAuthority.Current holds its mutex across
// the trade that renews it, as Client.Token holds its own. A registered session's enrollLoop
// makes that trade on the serve loop's context, which has no deadline. So an
// `agent-secrets enroll --helper` that arrives while the broker is slow to answer that trade
// waits on the mutex, which no context reaches, for as long as the trade takes: past the bound
// brokerCallBound gives it, and past the connection deadline that bound's reply is written
// inside. The helper then has nothing to write by the time it could, and the client reads an
// empty reply - the defect A10 was written for, reached by another road.
//
// The row runs under a bound of overBound, so the trade it holds need only outlast
// overBound + replyWriteSlack rather than the real 25 s.
func TestAPassThroughOpAnswersInsideItsBoundWhileTheBoxsSignInIsTrading(t *testing.T) {
	const tradeTakes = overBound + replyWriteSlack + 3*time.Second
	var trades atomic.Int64
	tradeStarted := make(chan struct{})
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != brokerclient.TokenPath {
			http.NotFound(w, r)
			return
		}
		if trades.Add(1) == 1 {
			close(tradeStarted)
			select {
			case <-time.After(tradeTakes):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("access-token-%d", trades.Load()), "token_type": "DPoP", "expires_in": 3600,
			"credential_id": "credential-1", "credential_expires_at": "2099-01-01T00:00:00Z",
		})
	}))
	t.Cleanup(tokens.Close)

	r := startRig(t, "")
	r.srv.Broker.SignIns = signedInBox(t, tokens.URL)
	r.srv.SetBrokerCallBound(overBound)

	// A session the helper registered for itself enrols, as enrollLoop does, and its trade for
	// the person's token is the slow one.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _, _ = r.srv.Broker.EnrollIdentity(context.Background(), identity{
			RuntimeID: "session-registered-on-this-box", Thumbprint: "tp-registered", Key: key,
		})
	}()
	select {
	case <-tradeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the registered session's enrollment never traded for the person's token")
	}

	started := time.Now()
	resp, err := Call(r.sock, Request{Op: "enroll-session-key", RuntimeID: "rt-during-the-trade", Thumbprint: "tp-during-the-trade"}, clientCallWait)
	waited := time.Since(started)
	if err != nil {
		t.Fatalf("enroll-session-key while the box's sign-in is trading: %v after %s; the client must read one JSON reply, never an empty one (the bound is %s, its reply written inside %s)",
			err, waited, overBound, overBound+replyWriteSlack)
	}
	if resp.OK || resp.Code != CodeEnrollFailed || !strings.Contains(resp.Error, "timed out") {
		t.Fatalf("enroll-session-key while the box's sign-in is trading: %+v after %s; want %s saying it timed out", resp, waited, CodeEnrollFailed)
	}
	if waited >= overBound+replyWriteSlack {
		t.Fatalf("enroll-session-key answered after %s, past the %s its reply is written inside", waited, overBound+replyWriteSlack)
	}
}
