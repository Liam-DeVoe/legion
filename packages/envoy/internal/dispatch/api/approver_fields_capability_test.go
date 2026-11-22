package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// capabilityBroker is a secrets broker answering GET /v1/settings and a machine login's approve the
// way one release or another does. An older broker (takesFields false) answers settings with no
// approver_fields, as the production broker from before the account binding does, and decodes the
// approve as that broker does (internal/cipher/api's readJSON into decideBody on main): exactly one
// JSON value holding approver and code alone, with json.Decoder.DisallowUnknownFields, refusing any
// other field 400 INVALID_DECISION. A broker that takes the fields answers approver_fields true and
// decodes the three approver fields beside them, refusing any other field the same way.
// settingsStatus, when set, is the status its settings read answers instead, with no settings.
type capabilityBroker struct {
	server         *httptest.Server
	takesFields    bool
	settingsStatus int

	mu            sync.Mutex
	settingsReads int
	approves      []string
}

func newCapabilityBroker(t *testing.T, takesFields bool, settingsStatus int) *capabilityBroker {
	t.Helper()
	b := &capabilityBroker{takesFields: takesFields, settingsStatus: settingsStatus}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/settings" {
			b.mu.Lock()
			b.settingsReads++
			b.mu.Unlock()
			switch {
			case b.settingsStatus != 0:
				w.WriteHeader(b.settingsStatus)
				_, _ = w.Write([]byte(`{"code":"INTERNAL","error":"the test's broker cannot answer its settings"}`))
			case b.takesFields:
				_, _ = w.Write([]byte(`{"store":"gcp-secret-manager","secrets_prefix":"projects/1/secrets/agent-secrets-","secret_type_tag":true,"approver_fields":true}`))
			default:
				_, _ = w.Write([]byte(`{"store":"gcp-secret-manager","secrets_prefix":"projects/1/secrets/agent-secrets-","secret_type_tag":true}`))
			}
			return
		}
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/credential-requests/") || !strings.HasSuffix(r.URL.Path, "/approve") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND","error":"not found"}`))
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b.mu.Lock()
		b.approves = append(b.approves, r.URL.Path+" "+string(raw))
		b.mu.Unlock()
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if b.takesFields {
			var body struct {
				Approver                  string  `json:"approver"`
				Code                      *string `json:"code"`
				ApproverIssuer            string  `json:"approver_issuer"`
				ApproverSubject           string  `json:"approver_subject"`
				ApproverSessionGeneration *int64  `json:"approver_session_generation"`
			}
			err = decoder.Decode(&body)
		} else {
			var body struct {
				Approver string  `json:"approver"`
				Code     *string `json:"code"`
			}
			err = decoder.Decode(&body)
		}
		if err == nil && decoder.More() {
			err = errors.New("body must contain one JSON value")
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "INVALID_DECISION", "error": "body must be one valid JSON object: " + err.Error()})
			return
		}
		_, _ = w.Write([]byte(`{"state":"approved","credential_id":"cred-1"}`))
	}))
	t.Cleanup(b.server.Close)
	return b
}

// read is how many settings reads the broker answered and every approve it received, as
// "<path> <body>".
func (b *capabilityBroker) read() (int, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settingsReads, append([]string(nil), b.approves...)
}

// TestTheApproveNamesTheApproverFieldsOnlyToABrokerThatSaysItTakesThem
// (cipher.Client.TakesApproverFields): carol's personal token is bound to the account people records
// for her, so her approve of a machine login has an account and a session generation to name.
//
//   - An older broker, whose settings name no approver_fields, is sent the approver and the code
//     alone, exactly the body it decodes, and approves it.
//   - A broker whose settings answer approver_fields true is sent approver_issuer, approver_subject
//     and approver_session_generation, and approves it.
//   - A broker whose settings read fails is sent the approver and the code alone, which every
//     broker takes; here the older broker's decoder takes it.
//
// Each approve reads the settings once. Control: approveCredentialRecord filling the three whatever
// the broker says (its capability read as true) fails the first row: the older broker receives them
// and refuses the approve 400 INVALID_DECISION.
func TestTheApproveNamesTheApproverFieldsOnlyToABrokerThatSaysItTakesThem(t *testing.T) {
	const (
		google  = "https://accounts.google.com"
		carol   = "carol@example.com"
		account = "carol-account"
	)
	approve := func(t *testing.T, broker *capabilityBroker) (*httptest.ResponseRecorder, int64) {
		t.Helper()
		handler, database, _ := newTestServer(t, testServerOptions{signInIssuer: google, agentSecretsURL: broker.server.URL})
		ctx := context.Background()
		seedPeople(t, database, carol)
		if _, err := database.Pool.Exec(ctx, `update people set sub = $2 where email = $1`, carol, account); err != nil {
			t.Fatalf("record carol's account: %v", err)
		}
		plaintext, hash, err := newAgentToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(ctx, `
			insert into agent_tokens (owner, name, token_hash, prefix, scopes, subject)
			values ($1, 'carol''s agent', $2, $3, $4, $5)`, carol, hash[:], plaintext[len("dsp_"):len("dsp_")+agentTokenPrefixLength],
			[]string{model.ScopePerson, model.ScopeApprove}, account); err != nil {
			t.Fatalf("store carol's token, bound to her account: %v", err)
		}
		var generation int64
		if err := database.Pool.QueryRow(ctx, `select coalesce((select generation from user_sessions where login = $1), 0)`, carol).Scan(&generation); err != nil {
			t.Fatalf("read carol's session generation: %v", err)
		}
		return agentRequest(t, handler, http.MethodPost, "/api/v1/credential-requests/rec1/approve", map[string]any{"code": "ABCD-1234"}, plaintext), generation
	}
	const todays = `/v1/credential-requests/rec1/approve {"approver":"carol@example.com","code":"ABCD-1234"}`
	for _, row := range []struct {
		name           string
		takesFields    bool
		settingsStatus int
		want           func(generation int64) string
	}{
		{"an older broker", false, 0, func(int64) string { return todays }},
		{"a broker that takes the fields", true, 0, func(generation int64) string {
			return fmt.Sprintf(`/v1/credential-requests/rec1/approve {"approver":"carol@example.com","code":"ABCD-1234","approver_issuer":%q,"approver_subject":%q,"approver_session_generation":%d}`,
				google, account, generation)
		}},
		{"a broker whose settings read fails", false, http.StatusInternalServerError, func(int64) string { return todays }},
	} {
		t.Run(row.name, func(t *testing.T) {
			broker := newCapabilityBroker(t, row.takesFields, row.settingsStatus)
			response, generation := approve(t, broker)
			reads, approves := broker.read()
			if want := row.want(generation); len(approves) != 1 || approves[0] != want {
				t.Fatalf("the broker received %q, want only %s", approves, want)
			}
			if reads != 1 {
				t.Errorf("the approve read the broker's settings %d times, want once", reads)
			}
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"approved"`) {
				t.Fatalf("carol's approve answered %d %s, want the broker's 200 approved", response.Code, response.Body.String())
			}
		})
	}
}
