// packages/envoy/internal/brokerclient/tokentransport_test.go

package brokerclient

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// TestAnInactiveTokenIsDroppedAndTradedAgain: a server that answers one 401
// BROKER_TOKEN_NOT_ACTIVE, as Dispatch answers a token the broker ended before it lapsed, makes the
// next request carry a token traded afresh rather than the one it refused, which the client would
// otherwise present until its renewal came due. The refusal reaches the caller as the server
// wrote it, body and all.
func TestAnInactiveTokenIsDroppedAndTradedAgain(t *testing.T) {
	broker := newStubBroker(t, func(n int, _ url.Values) (int, map[string]any) {
		return 200, map[string]any{"access_token": "token-" + strconv.Itoa(n), "token_type": "DPoP", "expires_in": 3600}
	})
	const refusal = `{"code":"BROKER_TOKEN_NOT_ACTIVE","error":"the secrets broker does not hold this access token as live"}`
	var mu sync.Mutex
	var presented []string
	server := newRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		presented = append(presented, r.Header.Get("Authorization"))
		first := len(presented) == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if first {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, refusal)
			return
		}
		_, _ = io.WriteString(w, `{"kind":"agent"}`)
	})
	client, err := New(Config{BrokerURL: broker.URL(), Key: testKey(t), Subject: projectedStub()})
	if err != nil {
		t.Fatal(err)
	}
	caller := &http.Client{Transport: NewTokenTransport(client, http.DefaultTransport)}

	if status, body := presentTo(t, caller, server+"/api/v1/whoami"); status != http.StatusUnauthorized || body != refusal {
		t.Fatalf("the first request was answered %d %q, want the server's 401 %q as it wrote it", status, body, refusal)
	}
	if status, body := presentTo(t, caller, server+"/api/v1/whoami"); status != http.StatusOK || body != `{"kind":"agent"}` {
		t.Fatalf("the second request was answered %d %q, want 200", status, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(presented) != 2 || presented[0] != "DPoP token-0" || presented[1] != "DPoP token-1" {
		t.Fatalf("the server was presented %q, want DPoP token-0 and then the token traded after its refusal, DPoP token-1", presented)
	}
	if trades := len(broker.Requests()); trades != 2 {
		t.Fatalf("the client traded %d times, want 2: once for each token", trades)
	}
}

// TestAFailedTradeSendsNothing: a request whose token the broker will not trade for answers the
// broker's refusal and never reaches the server, so no request goes out without a token.
func TestAFailedTradeSendsNothing(t *testing.T) {
	broker := newStubBroker(t, func(int, url.Values) (int, map[string]any) {
		return 400, map[string]any{"error": "invalid_grant", "error_description": "no entry lists this account"}
	})
	var sent atomic.Int64
	server := newRawServer(t, func(http.ResponseWriter, *http.Request) { sent.Add(1) })
	caller := &http.Client{Transport: NewTokenTransport(refusalClient(t, broker), nil)}

	response, err := caller.Get(server + "/api/v1/whoami")
	if err == nil {
		response.Body.Close()
	}
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Description != "no entry lists this account" {
		t.Fatalf("the request answered %v, want the broker's refusal", err)
	}
	if n := sent.Load(); n != 0 {
		t.Fatalf("the server received %d requests, want none: the trade failed", n)
	}
}

// presentTo sends GET target through caller, answering the status and the body.
func presentTo(t *testing.T, caller *http.Client, target string) (int, string) {
	t.Helper()
	response, err := caller.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer of GET %s: %v", target, err)
	}
	return response.StatusCode, string(body)
}
