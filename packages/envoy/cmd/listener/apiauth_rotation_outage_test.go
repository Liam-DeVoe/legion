package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/oidc"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// The issuer rotates its signing key while its key set cannot be read. The listener holds the old
// key, so a token signed with the new one sends it to read the key set again, and that read
// fails: no verdict on the token is reached, on the first call or on any call after it. Each call
// is therefore answered 503 with a Retry-After, as an outage at the first read is
// (TestAPIAuthAnswersAnIssuerOutage503), so Dispatch tries again rather than settling the delivery
// as failed on a live token. A 401 here tells Dispatch the token was refused when it was never
// judged.
//
// Found by t-e-14's acceptance at 8604c9582e: on the real listener binary, with the issuer's key
// set answering 500 after the listener had read it once, a token signed with a newly published key
// was answered 503 on the first call and 401 on the second and third, and Dispatch settles a 401
// as failed (internal/dispatch/api, retryableDeliveryFailure).
func TestAPIAuthAnswersARotationDuringAnIssuerOutage503(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	issuer := oidctest.New(t)
	first := issuer.PublishKey(t, "listener-key-1")
	verifier, err := oidc.New(context.Background(), issuer.URL(), listenerTestAudience)
	if err != nil {
		t.Fatalf("oidc.New(%q, %q): %v", issuer.URL(), listenerTestAudience, err)
	}
	serve := func(authorize string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
		request.Host = "127.0.0.1:9020"
		request.Header.Set("Authorization", authorize)
		apiAuth("", verifier, nil, nil, logging.NewWithWriter("test-machine", &discard{}), next).ServeHTTP(recorder, request)
		return recorder
	}

	// The listener reads the key set once and holds the first key.
	if recorder := serve("Bearer " + issuer.Mint(t, first, issuer.Claims(listenerTestSubject, listenerTestAudience))); recorder.Code != http.StatusNoContent {
		t.Fatalf("the first key with the key set up: status = %d, want 204", recorder.Code)
	}

	// The issuer rotates to a second key, and its key set cannot be read.
	issuer.FailKeys(http.StatusInternalServerError)
	rotated := oidctest.NewKey(t, "listener-key-2")
	issuer.Publish(rotated)
	token := issuer.Mint(t, rotated, issuer.Claims(listenerTestSubject, listenerTestAudience))
	for call := 1; call <= 3; call++ {
		recorder := serve("Bearer " + token)
		if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" {
			t.Errorf("call %d with a rotated key while the key set answers 500: status = %d, Retry-After %q; want 503 with a Retry-After, since the key set could not be read and no verdict was reached",
				call, recorder.Code, recorder.Header().Get("Retry-After"))
		}
	}
}

// discard is an io.Writer that keeps nothing.
type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }
