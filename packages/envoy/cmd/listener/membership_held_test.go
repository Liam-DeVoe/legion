package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/listenerwire"
)

// heldTestClock is the broker-token check's clock in a test that outlives the answer the check
// keeps (brokertoken.AffirmativeCacheTTL) without sleeping through it.
type heldTestClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *heldTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *heldTestClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// TestAPIAuthRefusesAHeldBrokerToken (K2, K3): a broker token whose introspection answers its
// person held is refused 401 MEMBERSHIP_HELD in the broker's own words, naming the person, and
// never reaches the route; only after the listener's own checks, so the same token under a
// stranger's proof is the 401 every refused credential gets here, with no code. Once the broker
// answers the person not held, the same token is served at the first request after the answer the
// check kept lapses (brokertoken.AffirmativeCacheTTL).
//
// Control: the Verifier leaving Caller.Held false whatever the answer says; the held token is
// served 200 and the test fails naming it.
func TestAPIAuthRefusesAHeldBrokerToken(t *testing.T) {
	broker := brokertokentest.NewStubBroker(t)
	caller, stranger := brokertokentest.NewCaller(t), brokertokentest.NewCaller(t)
	clock := &heldTestClock{at: time.Now()}
	tokens := newBrokerTokens(listenerBrokerConfig{
		BrokerURL:        broker.Server.URL,
		AccountTokenFile: "/var/run/secrets/broker/token",
		Origin:           listenerBrokerOrigin,
	})
	verifier, err := brokertoken.New(brokertoken.Options{
		BrokerURL: broker.Server.URL,
		Account:   listenerBrokerAccount("listener-account-token"),
		Origin:    listenerBrokerOrigin,
		Replay:    &spentProofsInMemory{},
		Address:   listenerSourceAddress,
		Now:       clock.now,
	})
	if err != nil {
		t.Fatalf("build the broker-token verifier: %v", err)
	}
	tokens.verifier.Store(verifier)

	const person, token = "ada@example.com", "access-token-of-a-held-person"
	held := sessionAnswer("ses_held", person, caller.JKT())
	held.Held = true
	broker.Says(token, held)
	var served int
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	})
	serve := func(request *http.Request) (*httptest.ResponseRecorder, string) {
		t.Helper()
		var logged bytes.Buffer
		response := httptest.NewRecorder()
		apiAuth(listenerAPIAuth{brokers: tokens, logger: logging.NewWithWriter("test-machine", &logged)}, next).ServeHTTP(response, request)
		return response, logged.String()
	}

	stolen := brokerTokenRequest(t, stranger, http.MethodPost, "/v1/messages/publish", token, `{}`)
	response, logged := serve(stolen)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "MEMBERSHIP_HELD") || served != 0 {
		t.Fatalf("the held token under a stranger's proof = %d %s, served %d times; want the 401 every refusal gets, with no code", response.Code, response.Body.String(), served)
	}
	assertBrokerRejectionLog(t, logged, "key_mismatch", token, stolen.Header.Get("DPoP"))

	request := brokerTokenRequest(t, caller, http.MethodPost, "/v1/messages/publish", token, `{}`)
	response, logged = serve(request)
	var refusal listenerwire.APIError
	_ = json.Unmarshal(response.Body.Bytes(), &refusal)
	want := person + "'s credentials are held by Dispatch; they work again when " + person + " signs in to Dispatch, or when Dispatch can confirm " + person + " with Google again"
	if response.Code != http.StatusUnauthorized || refusal.Code != "MEMBERSHIP_HELD" || refusal.Error != want || served != 0 {
		t.Fatalf("%s's broker token, which the broker answers held, = %d %s, served %d times; want 401 MEMBERSHIP_HELD saying %q", person, response.Code, response.Body.String(), served, want)
	}
	assertBrokerRejectionLog(t, logged, "membership_held", token, request.Header.Get("DPoP"))

	released := held
	released.Held = false
	broker.Says(token, released)
	clock.advance(brokertoken.AffirmativeCacheTTL)
	response, logged = serve(brokerTokenRequest(t, caller, http.MethodPost, "/v1/messages/publish", token, `{}`))
	if response.Code != http.StatusOK || served != 1 {
		t.Fatalf("%s's broker token once the broker answers her not held = %d %s, served %d times; want 200", person, response.Code, response.Body.String(), served)
	}
	assertBrokerRejectionLog(t, logged, "", token, "")
}
