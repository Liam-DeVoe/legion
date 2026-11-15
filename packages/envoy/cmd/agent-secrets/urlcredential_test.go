// packages/envoy/cmd/agent-secrets/urlcredential_test.go

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// TestSelfPrintsNoPartOfTheBrokerURLCredential: `agent-secrets self` on a machine that proves an
// identity of its own trades it at the broker before anything else, and with the broker
// unreachable prints that trade's failure on stderr. The line names the failure, and no part of a
// user name, password, query or fragment AGENT_SECRETS_URL carries. The values are made up.
func TestSelfPrintsNoPartOfTheBrokerURLCredential(t *testing.T) {
	for form, address := range map[string]string{
		"a user name and password": "http://demouser7q:demopass9z-not-a-secret@127.0.0.1:1",
		"a query":                  "http://127.0.0.1:1/?token=demoquery4k",
		"a fragment":               "http://127.0.0.1:1#demofrag8w",
	} {
		t.Run(form, func(t *testing.T) {
			tmp := t.TempDir()
			projected := filepath.Join(tmp, "projected-token")
			if err := os.WriteFile(projected, []byte("projected.service.account.token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", tmp)
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
			t.Setenv("AGENT_SECRETS_SIGNIN_DIR", filepath.Join(tmp, "signins"))
			t.Setenv("AGENT_SECRETS_HELPER_SOCK", "")
			t.Setenv("AGENT_SECRETS_KEY_DIR", filepath.Join(tmp, "keys"))
			t.Setenv("AGENT_SECRETS_ENROLL_WAIT", "0s")
			t.Setenv("AGENT_SECRETS_URL", address)
			t.Setenv("AGENT_SECRETS_MACHINE_IDENTITY", "kubernetes")
			t.Setenv("AGENT_SECRETS_MACHINE_TOKEN_FILE", projected)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"self"}, &stdout, &stderr); code == 0 {
				t.Fatalf("self against a broker that never answers exited 0\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "connection refused") {
				t.Errorf("stderr does not name the failure:\n%s", stderr.String())
			}
			for _, marker := range []string{"demouser7q", "demopass9z-not-a-secret", "demoquery4k", "demofrag8w"} {
				if strings.Contains(stdout.String()+stderr.String(), marker) {
					t.Errorf("the output holds %q\nstdout: %s\nstderr: %s", marker, stdout.String(), stderr.String())
				}
			}
		})
	}
}

// TestTheCLIsBrokerClientFollowsNoRedirect: every call this command makes to the broker carries a
// Proof header of this session's own, so a 3xx answered for the broker - by the broker, or by
// anyone who can answer for it - must reach no other host. The client answers the 3xx to its
// caller instead, and the target receives nothing.
func TestTheCLIsBrokerClientFollowsNoRedirect(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var targetCalls atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			var brokerCalls atomic.Int64
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				brokerCalls.Add(1)
				http.Redirect(w, r, target.URL+"/v1/enrollments/self", status)
			}))
			defer broker.Close()

			c := newClient(broker.URL)
			_, _, _ = c.doStatus(context.Background(), http.MethodGet, broker.URL+"/v1/enrollments/self", nil, map[string]string{"Proof": "the-session-proof"})
			if got := targetCalls.Load(); got != 0 {
				t.Errorf("the redirect's target received %d requests, want none", got)
			}
			if got := brokerCalls.Load(); got != 1 {
				t.Errorf("the broker received %d requests, want 1", got)
			}
		})
	}
}
