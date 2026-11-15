// packages/envoy/internal/broker/helper/urlcredential_test.go
//go:build linux

package helper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/brokeraddr"
	"github.com/sjawhar/envoy/internal/broker/proof"
)

// TestTheHelpersBrokerCallsFollowNoRedirect: every call the helper makes to the broker carries
// something a host the deployment never named must not receive - the refresh a refresh token, the
// device authorization a signed request object, a renew the session's proof - so a broker address
// answering 3xx gets that answer back, and the host it names receives no request at all: not the
// call again (307, 308), and not the bodiless GET net/http turns a POST into on a 301, 302 or 303
// either. The broker receives each call itself, once, with the client the helper is given and
// with net/http's default alike.
func TestTheHelpersBrokerCallsFollowNoRedirect(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		for name, httpClient := range map[string]*http.Client{"a supplied client": {Timeout: 5 * time.Second}, "the default client": nil} {
			t.Run(http.StatusText(status)+" with "+name, func(t *testing.T) {
				var elsewhere atomic.Int64
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					elsewhere.Add(1)
					w.WriteHeader(http.StatusBadRequest)
				}))
				t.Cleanup(target.Close)
				var mu sync.Mutex
				asked := map[string]int{}
				broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					asked[r.URL.Path]++
					mu.Unlock()
					http.Redirect(w, r, target.URL+r.URL.Path, status)
				}))
				t.Cleanup(broker.Close)
				key, err := proof.NewKey()
				if err != nil {
					t.Fatal(err)
				}
				b := &Broker{URL: brokeraddr.New(broker.URL), OperatorFile: operatorFile(t, "ada@example.com"), HTTP: httpClient}
				t.Cleanup(b.Close)
				ctx := context.Background()

				_, _, refreshErr := b.redeem(ctx, &machineCredential{key: key, id: "credential-1", refreshToken: "demo-refresh-not-a-secret", gone: make(chan struct{})})
				_, loginErr := b.Login(ctx, "helper-host")
				_, renewErr := b.renewIdentity(ctx, identity{EnrollmentID: "enrollment-1", Key: key})
				for call, err := range map[string]error{"the refresh": refreshErr, "the device authorization": loginErr, "the renew": renewErr} {
					if err == nil {
						t.Errorf("%s took the redirect as the broker's answer", call)
					}
				}
				if got := elsewhere.Load(); got != 0 {
					t.Errorf("the redirect's target received %d requests, want none (refresh: %v, login: %v, renew: %v)", got, refreshErr, loginErr, renewErr)
				}
				mu.Lock()
				defer mu.Unlock()
				for _, path := range []string{"/v1/oauth/token", "/v1/oauth/device_authorization", "/v1/enrollments/enrollment-1/renew"} {
					if asked[path] != 1 {
						t.Errorf("the broker received %d requests at %s, want the call once (all: %v)", asked[path], path, asked)
					}
				}
			})
		}
	}
}

// TestALoginRefusalPrintsNoCredentialItsDescriptionRepeats: the broker, or whoever answers at its
// address, refuses a machine login with a description that repeats the credential the configured
// address carries. Beside that address the redactor prints the refusal as its status alone, and
// the helper must not take the description back out from under it: no BrokerError, and no text
// of the error, holds any part of the credential. Beside a clean address the broker's own words
// are what a person acts on, and the refusal still carries its code and its description whole.
func TestALoginRefusalPrintsNoCredentialItsDescriptionRepeats(t *testing.T) {
	var asked atomic.Int64
	var description string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request","error_description":"` + description + `"}`))
	}))
	t.Cleanup(srv.Close)
	credentialed := strings.Replace(srv.URL, "://", "://demouser7q:demopass9z-not-a-secret@", 1)
	description = "REQUEST_INVALID: the request object names " + credentialed + ", which is not this broker"
	login := func(t *testing.T, address string) error {
		t.Helper()
		before := asked.Load()
		b := &Broker{URL: brokeraddr.New(address), OperatorFile: operatorFile(t, "ada@example.com"), HTTP: srv.Client()}
		t.Cleanup(b.Close)
		_, err := b.Login(context.Background(), "helper-host")
		if err == nil {
			t.Fatal("Login took the broker's refusal as a login")
		}
		if asked.Load() == before {
			t.Fatalf("the login never reached the broker (Login: %v)", err)
		}
		return err
	}

	t.Run("an address carrying a credential", func(t *testing.T) {
		err := login(t, credentialed)
		for _, marker := range []string{"demouser7q", "demopass9z-not-a-secret"} {
			if strings.Contains(err.Error(), marker) {
				t.Errorf("the refusal holds %q: %s", marker, err)
			}
			var be *BrokerError
			if errors.As(err, &be) && (strings.Contains(be.Code, marker) || strings.Contains(be.Message, marker)) {
				t.Errorf("the refusal's BrokerError holds %q: %+v", marker, be)
			}
		}
		if err.Error() != "the broker answered HTTP 400" {
			t.Errorf("the refusal is %q, want the status the broker answered", err)
		}
	})

	t.Run("a clean address", func(t *testing.T) {
		err := login(t, srv.URL)
		var be *BrokerError
		if !errors.As(err, &be) {
			t.Fatalf("Login: %v, want the broker's refusal as a BrokerError", err)
		}
		_, words, _ := strings.Cut(description, ": ")
		if be.Status != http.StatusBadRequest || be.Code != "REQUEST_INVALID" || be.Message != words {
			t.Errorf("the refusal is %+v, want 400 REQUEST_INVALID and the broker's description whole, %q", be, words)
		}
	})
}
