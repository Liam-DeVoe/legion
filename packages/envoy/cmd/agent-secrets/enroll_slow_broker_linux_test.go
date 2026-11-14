// packages/envoy/cmd/agent-secrets/enroll_slow_broker_linux_test.go
//go:build linux

// The real `agent-secrets` pass-through commands against a broker slow to answer the call the
// helper passes through. The client gives each such call passThroughCallWait (30 s), above the
// helper's own 20 s bound (helper.DefaultBrokerCallBound), so the helper's answer reaches the
// client rather than the client timing out first. It used to reach nobody for a call past 5 s:
// the helper put a 5 s deadline on the connection, the broker's work was done, its reply could
// not be written, and the caller read an empty reply and exited 1
// (dispatch://INFRA-49/comment/9bf092b6).
//
// Linux-only, for the real helper these run against (internal/broker/helper is //go:build
// linux). enroll-session-key and login are no peer ops, so these need no SO_PEERPIDFD.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEnrollHelperPrintsAnEnrollmentASlowBrokerAnswered drives the real `agent-secrets enroll
// --helper` through a real agent-secrets-helper whose broker answers the enrollment after 8 s.
//
// The named revert: put the 5 s connection deadline back over the pass-through ops (drop the
// SetDeadline of handle's passThroughOps branch in internal/broker/helper/server.go) and this
// row reads an empty reply and exits 1 with the enrollment made.
func TestEnrollHelperPrintsAnEnrollmentASlowBrokerAnswered(t *testing.T) {
	const (
		brokerDelay  = 8 * time.Second
		enrollmentID = "enr-slow-box-1"
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/oauth/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"device_code": "device-1", "user_code": "ABCD-EFGH", "verification_uri": "https://s", "expires_in": 900, "interval": 1})
	})
	mux.HandleFunc("POST /v1/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "not-a-secret-access", "token_type": "DPoP", "expires_in": 900, "refresh_token": "not-a-secret-refresh",
			"credential_id": "cred-1", "credential_expires_at": time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	})
	// Room for more than the one enrollment this row expects, so a second one is counted and
	// fails the row rather than blocking its handler.
	enrolled := make(chan struct{}, 4)
	mux.HandleFunc("POST /v1/enrollments", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(brokerDelay):
		case <-r.Context().Done():
			return
		}
		enrolled <- struct{}{}
		writeJSON(w, map[string]any{"enrollment_id": enrollmentID, "lease_expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)})
	})
	broker := httptest.NewServer(mux)
	defer broker.Close()

	binary := buildAgentSecrets(t)
	srv, sock := serveRealHelper(t, broker.URL, "ada@example.com")
	if _, err := srv.Broker.Login(context.Background(), srv.Hostname); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !srv.Broker.HasCredential() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !srv.Broker.HasCredential() {
		t.Fatal("the helper never installed the issued launcher credential")
	}

	keyDir := t.TempDir()
	started := time.Now()
	stdout, stderr, exit := runAgentSecrets(t, binary, broker.URL, keyDir, []string{"AGENT_SECRETS_HELPER_SOCK=" + sock},
		"enroll", "--helper", "--runtime-id", "slow-box-1", "--thumbprint", "tp-slow-box-1")
	waited := time.Since(started)
	if exit != 0 || stdout != enrollmentID+"\n" {
		t.Fatalf("enroll --helper against a broker answering in %s = %d %q (stderr %q) after %s; want exit 0 and the bare enrollment id",
			brokerDelay, exit, stdout, stderr, waited)
	}
	if waited < brokerDelay {
		t.Fatalf("enroll --helper answered after %s, before the broker did: the row proves nothing", waited)
	}
	if len(enrolled) != 1 {
		t.Fatalf("the broker answered %d enrollments, want the one this row printed", len(enrolled))
	}
	if data, err := os.ReadFile(filepath.Join(keyDir, "enrollment")); err != nil || strings.TrimSpace(string(data)) != enrollmentID {
		t.Fatalf("enrollment file: err=%v content=%q, want %q", err, data, enrollmentID)
	}
	t.Logf("enroll --helper read the enrollment a broker answered in %s after %s", brokerDelay, waited)
}

// TestMachineLoginPrintsItsCodeAgainstASlowBroker drives the real `agent-secrets machine login`
// through a real agent-secrets-helper whose broker answers the device authorization after 12 s.
// The client waits passThroughCallWait (30 s) for that call, above the helper's 20 s bound, so
// the helper's answer reaches it; the login then issues and the command prints its code and
// exits 0.
//
// The named revert: restore `machine login`'s own 10 s helper.Call wait (machine.go) with an
// overlay, and a 12 s broker times the client out before the helper answers — the command exits
// 1 with a transport timeout, the bug this client wait closes.
func TestMachineLoginPrintsItsCodeAgainstASlowBroker(t *testing.T) {
	const (
		brokerDelay = 12 * time.Second
		userCode    = "ABCD-EFGH"
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/oauth/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(brokerDelay):
		case <-r.Context().Done():
			return
		}
		writeJSON(w, map[string]any{"device_code": "device-1", "user_code": userCode, "verification_uri": "https://s", "expires_in": 900, "interval": 1})
	})
	mux.HandleFunc("POST /v1/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "not-a-secret-access", "token_type": "DPoP", "expires_in": 900, "refresh_token": "not-a-secret-refresh",
			"credential_id": "cred-1", "credential_expires_at": time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	})
	broker := httptest.NewServer(mux)
	defer broker.Close()

	binary := buildAgentSecrets(t)
	_, sock := serveRealHelper(t, broker.URL, "ada@example.com")

	started := time.Now()
	stdout, stderr, exit := runAgentSecrets(t, binary, broker.URL, t.TempDir(), []string{"AGENT_SECRETS_HELPER_SOCK=" + sock},
		"machine", "login")
	waited := time.Since(started)
	if exit != 0 || !strings.Contains(stdout, "machine login code: "+userCode) {
		t.Fatalf("machine login against a broker answering in %s = %d %q (stderr %q) after %s; want exit 0 and the broker's code",
			brokerDelay, exit, stdout, stderr, waited)
	}
	if waited < brokerDelay {
		t.Fatalf("machine login answered after %s, before the broker did: the row proves nothing", waited)
	}
	t.Logf("machine login read the code a broker answered in %s after %s", brokerDelay, waited)
}
