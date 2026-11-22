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

// strictDecisionBroker is a secrets broker from before the approving account and the session
// generation, decoding a machine login's approve as that broker does (internal/cipher/api's
// readJSON and decideBody there): exactly one JSON value, holding approver and code alone, with
// json.Decoder.DisallowUnknownFields, so any other field is refused 400 INVALID_DECISION. It
// answers a body it decodes 200, and records every body it receives.
type strictDecisionBroker struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newStrictDecisionBroker(t *testing.T) *strictDecisionBroker {
	t.Helper()
	b := &strictDecisionBroker{}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b.mu.Lock()
		b.bodies = append(b.bodies, r.Method+" "+r.URL.Path+" "+string(raw))
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/credential-requests/") || !strings.HasSuffix(r.URL.Path, "/approve") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND","error":"not found"}`))
			return
		}
		var body struct {
			Approver string  `json:"approver"`
			Code     *string `json:"code"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&body)
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

// received is every call the broker received, as "<method> <path> <body>".
func (b *strictDecisionBroker) received() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.bodies...)
}

// TestTheApproveNamesTheApproverFieldsOnlyWithTheSetting (DISPATCH_AGENT_SECRETS_APPROVER_FIELDS,
// Deps.BrokerApproverFields): carol's personal token is bound to her Google account, the one people
// records for her, so her approve of a machine login has an account and a session generation to
// name. With the setting off, the production default, the approve reaches a broker from before
// those fields as the approver and the code alone, and that broker approves it. With it on, the
// broker receives approver_issuer, approver_subject and approver_session_generation, which such a
// broker refuses 400 INVALID_DECISION, relayed as it answered.
//
// Control: approveCredentialRecord filling the three whatever the setting says (its gate read as
// on) fails the off row: the broker receives them and answers 400 INVALID_DECISION.
func TestTheApproveNamesTheApproverFieldsOnlyWithTheSetting(t *testing.T) {
	const (
		google  = "https://accounts.google.com"
		carol   = "carol@example.com"
		account = "carol-account"
	)
	approve := func(t *testing.T, fieldsOff bool) (*httptest.ResponseRecorder, []string, int64) {
		t.Helper()
		broker := newStrictDecisionBroker(t)
		handler, database, _ := newTestServer(t, testServerOptions{signInIssuer: google, agentSecretsURL: broker.server.URL, approverFieldsOff: fieldsOff})
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
		response := agentRequest(t, handler, http.MethodPost, "/api/v1/credential-requests/rec1/approve", map[string]any{"code": "ABCD-1234"}, plaintext)
		return response, broker.received(), generation
	}

	t.Run("off", func(t *testing.T) {
		response, received, _ := approve(t, true)
		want := `POST /v1/credential-requests/rec1/approve {"approver":"carol@example.com","code":"ABCD-1234"}`
		if len(received) != 1 || received[0] != want {
			t.Fatalf("with the setting off the broker received %q, want only %s", received, want)
		}
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"approved"`) {
			t.Fatalf("with the setting off carol's approve answered %d %s, want the broker's 200 approved", response.Code, response.Body.String())
		}
	})

	t.Run("on", func(t *testing.T) {
		response, received, generation := approve(t, false)
		want := fmt.Sprintf(`POST /v1/credential-requests/rec1/approve {"approver":"carol@example.com","code":"ABCD-1234","approver_issuer":%q,"approver_subject":%q,"approver_session_generation":%d}`,
			google, account, generation)
		if len(received) != 1 || received[0] != want {
			t.Fatalf("with the setting on the broker received %q, want only %s", received, want)
		}
		// The broker from before the fields refuses them, which is what the setting is for.
		if response.Code != http.StatusBadRequest || responseCode(t, response) != "INVALID_DECISION" {
			t.Fatalf("with the setting on carol's approve answered %d %s, want the broker's 400 INVALID_DECISION relayed", response.Code, response.Body.String())
		}
	})
}
