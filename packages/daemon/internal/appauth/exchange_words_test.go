package appauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/oauth2call/oauth2calltest"
)

// TestATokenExchangeAnswerEchoingTheAppJWTPrintsNoneOfIt: something in GitHub's place answers a
// token exchange 200 with a permissions object that echoes the request's Authorization, the App's
// JWT, as a permission's name (permission) or as the access of a permission the request asked for
// (access). The mint refuses the token as wider than asked, and neither its error nor what the
// Manager logged holds a run of the JWT: the refusal prints a permission only from the request's
// own names and an access only from the ones accessOrder ranks, with a fixed phrase in place of
// either (heldWords). Every line that names the refusal, the daemon's credential route's among
// them (api's appLease), prints it by its Error. Captures appauth.answer-words/exchange-<case>.
func TestATokenExchangeAnswerEchoingTheAppJWTPrintsNoneOfIt(t *testing.T) {
	expires := time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for _, tc := range []struct {
		name        string
		permissions func(authorization string) map[string]string
		named       string
	}{
		{"permission", func(authorization string) map[string]string {
			return map[string]string{authorization: "read", "contents": "read", "metadata": "read", "pull_requests": "read", "actions": "read"}
		}, "it holds (a permission the request did not name): read"},
		{"access", func(authorization string) map[string]string {
			return map[string]string{"contents": authorization, "metadata": "read", "pull_requests": "read", "actions": "read"}
		}, "it holds contents: (an access this daemon cannot order)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			var said bytes.Buffer
			github := &keyModeGitHub{owners: []string{"acme"}, slug: "legion-reviewer"}
			github.answer = func(int, accessTokensBody) (int, string) {
				permissions, err := json.Marshal(tc.permissions(github.lastAuthorization()))
				if err != nil {
					t.Errorf("encode the stand-in's permissions: %v", err)
				}
				return http.StatusOK, `{"token":"ghs_wide","expires_at":"` + expires + `","permissions":` + string(permissions) + `}`
			}
			tokens := keyModeManager(t, github, &now, &said)

			lease, err := tokens.ReadToken(context.Background(), Review, "acme")
			if err == nil || lease.Token != "" {
				t.Fatalf("a token whose answer echoes the App's JWT = %q, %v; want no lease and a refusal", lease.Token, err)
			}
			// The JWT the exchange carried, which the stand-in echoed: a check of an empty one
			// would pass whatever the refusal printed.
			jwt, bearer := strings.CutPrefix(github.lastAuthorization(), "Bearer ")
			if !bearer || jwt == "" {
				t.Fatalf("the exchange carried Authorization %q, want the App's JWT as a bearer", github.lastAuthorization())
			}
			oauth2calltest.RequireNoPiece(t, "appauth.answer-words/exchange-"+tc.name, err.Error()+"\n"+said.String(), jwt)
			if !strings.Contains(err.Error(), tc.named) {
				t.Errorf("the refusal is %q, want it to say %q", err, tc.named)
			}
		})
	}
}

// TestAnInstallationReadEchoingTheAppJWTAsItsSuspensionPrintsNoneOfIt: something in GitHub's place
// answers the read of an installation before its exchange with a suspended_at that echoes the
// read's Authorization, the App's JWT. The mint refuses the installation as suspended, decided on
// the field as it came, and neither its error nor what the Manager logged holds a run of the JWT:
// the refusal prints suspended_at only as the time it parses as (shownSuspension). The control
// prints GitHub's own time. Captures appauth.answer-words/suspended-at-<case>.
func TestAnInstallationReadEchoingTheAppJWTAsItsSuspensionPrintsNoneOfIt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		suspended func(authorization string) string
		named     string
	}{
		{"echo", func(authorization string) string { return authorization }, "with suspended_at (a value that is no time)"},
		{"control", func(string) string { return suspendedSince }, "with suspended_at " + suspendedSince},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
			var said bytes.Buffer
			github := &keyModeGitHub{owners: []string{"acme"}, slug: "legion-reviewer"}
			github.granted = func(installation string, _ int) (int, string) {
				suspendedAt, err := json.Marshal(tc.suspended(github.lastAuthorization()))
				if err != nil {
					t.Errorf("encode the stand-in's suspended_at: %v", err)
				}
				return http.StatusOK, installationAnswer(installation, grantedEverythingAsked, string(suspendedAt))
			}
			github.answer = func(int, accessTokensBody) (int, string) {
				t.Error("a suspended installation was asked for a token")
				return http.StatusForbidden, forbiddenAnswer
			}
			tokens := keyModeManager(t, github, &now, &said)

			lease, err := tokens.ReadToken(context.Background(), Review, "acme")
			if err == nil || lease.Token != "" {
				t.Fatalf("a mint of a suspended installation = %q, %v; want no lease and a refusal", lease.Token, err)
			}
			jwt, bearer := strings.CutPrefix(github.lastAuthorization(), "Bearer ")
			if !bearer || jwt == "" {
				t.Fatalf("the installation read carried Authorization %q, want the App's JWT as a bearer", github.lastAuthorization())
			}
			oauth2calltest.RequireNoPiece(t, "appauth.answer-words/suspended-at-"+tc.name, err.Error()+"\n"+said.String(), jwt)
			if !strings.Contains(err.Error(), tc.named) {
				t.Errorf("the refusal is %q, want it to say %q", err, tc.named)
			}
		})
	}
}
