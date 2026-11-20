package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// wireMachineTokens is GET /v1/admin/machine-tokens's answer as the wire carries it.
type wireMachineTokens struct {
	Project       string     `json:"project"`
	LiveTokens    int        `json:"live_tokens"`
	LastExpiresAt *time.Time `json:"last_expires_at"`
}

// TestOnlyDispatchCountsDeveloperBoxTokens: GET /v1/admin/machine-tokens answers Dispatch's caller
// how many live tokens act as their person and list the project, a developer box's, and when the
// last expires. A token that lists the project among others counts; a named token, a revoked one
// and an expired one do not, nor one of another project. A key no token lists answers 0 and no
// expiry, and a query that is no one project key is 400 PROJECT_INPUT. The access review and the
// listener, each listed for powers of their own, are 403 ACCOUNT_LACKS_POWER.
func TestOnlyDispatchCountsDeveloperBoxTokens(t *testing.T) {
	ts, reviewToken, listenerToken := newGrantLogServer(t)
	ctx := context.Background()
	soon, later := time.Now().Add(10*time.Minute).UTC().Truncate(time.Second), time.Now().Add(14*time.Minute).UTC().Truncate(time.Second)
	for _, row := range []struct {
		signature, scope, projects string
		active                     bool
		expires                    time.Time
	}{
		{"box-priv", "person", "PRIV", true, soon},
		{"box-acme-priv", "person", "ACME PRIV", true, later},
		{"named-priv", "named", "PRIV", true, later},
		{"revoked-priv", "person", "PRIV", false, later},
		{"expired-priv", "person", "PRIV", true, time.Now().Add(-time.Minute)},
		{"box-acme", "person", "ACME", true, later},
	} {
		if _, err := ts.Store.Pool.Exec(ctx, `insert into oauth_access_tokens (signature, request_id, request, active, expires_at)
			values ($1, $1, jsonb_build_object('session', jsonb_build_object('extra', jsonb_build_object('project_scope', $2::text, 'projects', $3::text))), $4, $5)`,
			row.signature, row.scope, row.projects, row.active, row.expires); err != nil {
			t.Fatalf("store the token %s: %v", row.signature, err)
		}
	}
	count := func(t *testing.T, bearer, query string) (int, []byte) {
		t.Helper()
		return ts.req(t, http.MethodGet, "/v1/admin/machine-tokens"+query, map[string]string{"Authorization": "Bearer " + bearer}, nil)
	}

	status, body := count(t, ts.uiToken, "?project=PRIV")
	answer := decode[wireMachineTokens](t, body)
	if status != http.StatusOK || answer.Project != "PRIV" || answer.LiveTokens != 2 || answer.LastExpiresAt == nil || !answer.LastExpiresAt.Equal(later) {
		t.Fatalf("GET /v1/admin/machine-tokens?project=PRIV as dispatch = %d %s, want 200 counting box-priv and box-acme-priv, the last expiring at %s", status, body, later)
	}
	status, body = count(t, ts.uiToken, "?project=NONE")
	if answer := decode[wireMachineTokens](t, body); status != http.StatusOK || answer.LiveTokens != 0 || answer.LastExpiresAt != nil {
		t.Fatalf("GET /v1/admin/machine-tokens?project=NONE as dispatch = %d %s, want 200, 0 and no expiry", status, body)
	}
	for _, query := range []string{"", "?project=priv", "?project=PRIV+ACME"} {
		status, body := count(t, ts.uiToken, query)
		if status != http.StatusBadRequest || decode[wireError](t, body).Code != "PROJECT_INPUT" {
			t.Errorf("GET /v1/admin/machine-tokens%s as dispatch = %d %s, want 400 PROJECT_INPUT", query, status, body)
		}
	}
	for name, bearer := range map[string]string{"access-review": reviewToken, "listener": listenerToken} {
		status, body := count(t, bearer, "?project=PRIV")
		if status != http.StatusForbidden || decode[wireError](t, body).Code != "ACCOUNT_LACKS_POWER" {
			t.Errorf("GET /v1/admin/machine-tokens as %s = %d %s, want 403 ACCOUNT_LACKS_POWER: the count is Dispatch's question alone", name, status, body)
		}
	}
}
