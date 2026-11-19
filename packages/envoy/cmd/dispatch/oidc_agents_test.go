package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/routes"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// oidcAgentsAudience is the audience the agents' projected service-account tokens carry in these
// tests, and oidcAgentsSubject the pod's service account they name.
const (
	oidcAgentsAudience = "dispatch-agents"
	oidcAgentsSubject  = "system:serviceaccount:agents:worker"
)

// Dispatch boots with the shared agent token, with the OIDC pair, or with both, and refuses to
// boot with neither, naming all three settings, so a deployment can run with no static shared
// token at all. The token alone is production's boot, unchanged: the shared token parsed, no
// service-account verifier. The pair alone leaves the shared token unset (AgentTokens nil). Half
// the pair with no token is refused as half the pair always was. Control: the refused
// environment and the pair-alone one differ only by the pair, so the pair is what lets it boot.
func TestDispatchBootsWithTheSharedTokenTheOIDCPairOrBoth(t *testing.T) {
	pair := map[string]string{"DISPATCH_OIDC_ISSUER": "https://issuer.example", "DISPATCH_OIDC_AUDIENCE": oidcAgentsAudience}
	with := func(overrides ...map[string]string) map[string]string {
		values := map[string]string{"DISPATCH_AGENT_TOKEN": ""}
		for _, set := range overrides {
			for name, value := range set {
				values[name] = value
			}
		}
		return values
	}

	tokenOnly := resolveWith(t, map[string]string{"DISPATCH_AGENT_TOKEN": "agent-token"})
	if tokenOnly.AgentTokens == nil || tokenOnly.OIDCIssuer != "" || tokenOnly.OIDCAudience != "" {
		t.Errorf("the shared token alone: AgentTokens=%v OIDC=%q,%q, want the token parsed and no verifier", tokenOnly.AgentTokens, tokenOnly.OIDCIssuer, tokenOnly.OIDCAudience)
	}
	if serviceTokenVerifier(tokenOnly) != nil {
		t.Errorf("the shared token alone builds a service-token verifier")
	}

	pairOnly := resolveWith(t, with(pair))
	if pairOnly.AgentTokens != nil || pairOnly.OIDCIssuer != "https://issuer.example" || pairOnly.OIDCAudience != oidcAgentsAudience {
		t.Errorf("the pair alone: AgentTokens=%v OIDC=%q,%q, want no shared token and the pair", pairOnly.AgentTokens, pairOnly.OIDCIssuer, pairOnly.OIDCAudience)
	}
	if serviceTokenVerifier(pairOnly) == nil {
		t.Errorf("the pair alone builds no service-token verifier")
	}

	both := resolveWith(t, with(pair, map[string]string{"DISPATCH_AGENT_TOKEN": "agent-token"}))
	if both.AgentTokens == nil || both.OIDCAudience != oidcAgentsAudience {
		t.Errorf("the token and the pair: AgentTokens=%v OIDCAudience=%q, want both", both.AgentTokens, both.OIDCAudience)
	}

	for _, blank := range []string{"", " \n "} {
		_, err := resolveBootConfig(bootEnvironment(map[string]string{"DISPATCH_AGENT_TOKEN": blank}))
		if err == nil {
			t.Fatalf("DISPATCH_AGENT_TOKEN=%q and no pair: booted, want a refusal", blank)
		}
		for _, name := range []string{"DISPATCH_AGENT_TOKEN", "DISPATCH_OIDC_ISSUER", "DISPATCH_OIDC_AUDIENCE"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("DISPATCH_AGENT_TOKEN=%q and no pair: err = %v, want it to name %s", blank, err, name)
			}
		}
	}
	refusedWith(t, with(map[string]string{"DISPATCH_OIDC_ISSUER": "https://issuer.example"}), "DISPATCH_OIDC_AUDIENCE")
	refusedWith(t, with(map[string]string{"DISPATCH_OIDC_AUDIENCE": oidcAgentsAudience}), "DISPATCH_OIDC_ISSUER")
}

// oidcAgentsRouter is the router main builds for boot, with the service-token verifier main gives
// it (serviceTokenVerifier, discovered here so the first token is not answered 503), and a store.
func oidcAgentsRouter(t *testing.T, boot bootConfig) http.Handler {
	t.Helper()
	verifier := serviceTokenVerifier(boot)
	if verifier == nil {
		t.Fatalf("boot %+v configures no service-token verifier", boot)
	}
	if err := verifier.Discover(context.Background()); err != nil {
		t.Fatalf("discover the service-token issuer: %v", err)
	}
	return routerFor(t, boot, routes.AppContextOptions{Store: storetest.Open(t), OIDC: verifier})
}

// answersService reports whether a whoami response names an agent with no owner that
// authenticated as subject's service-account token.
func answersService(response *httptest.ResponseRecorder, subject string) bool {
	var caller struct {
		Kind    string  `json:"kind"`
		Owner   *string `json:"owner"`
		Service *string `json:"service"`
	}
	return response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &caller) == nil &&
		caller.Kind == "agent" && caller.Owner == nil && caller.Service != nil && *caller.Service == subject
}

// A Dispatch booted with the OIDC pair and no shared token takes no bearer as the shared token:
// an empty bearer, a blank one, the value a deployment's shared token often holds, and any other
// value are each 401. A projected service-account token the issuer signed for the audience is an
// agent with no owner, named by its subject; one that expired, one for another audience, one
// naming another issuer and one another issuer signed are each 401.
//
// Control: the same router with the shared token set takes that value as the shared token, so a
// shared-token acceptance is one this test can see, and the refusals above are the unset token's.
func TestADispatchWithNoSharedTokenTakesOnlyVerifiedServiceTokens(t *testing.T) {
	issuer := oidctest.New(t)
	key := issuer.PublishKey(t, "pod-key")
	pair := map[string]string{"DISPATCH_AGENT_TOKEN": "", "DISPATCH_OIDC_ISSUER": issuer.URL(), "DISPATCH_OIDC_AUDIENCE": oidcAgentsAudience}
	handler := oidcAgentsRouter(t, resolveWith(t, pair))

	for name, bearer := range map[string]string{
		"an empty bearer":           "",
		"a blank bearer":            " ",
		"a placeholder value":       "change-me",
		"a deployment's old token":  "agent-token",
		"an arbitrary long value":   strings.Repeat("x", 48),
		"a JWT-shaped forged value": "header.payload.signature",
	} {
		if response := whoamiAs(handler, bearer, ""); response.Code != http.StatusUnauthorized {
			t.Errorf("%s (%q) with no shared token: GET /api/v1/whoami answered %d %s, want 401", name, bearer, response.Code, response.Body.String())
		}
	}

	valid := issuer.Mint(t, key, issuer.Claims(oidcAgentsSubject, oidcAgentsAudience))
	if response := whoamiAs(handler, valid, ""); !answersService(response, oidcAgentsSubject) {
		t.Errorf("a valid projected token: GET /api/v1/whoami answered %d %s, want 200 naming an agent with no owner as %s", response.Code, response.Body.String(), oidcAgentsSubject)
	}

	expired := issuer.Claims(oidcAgentsSubject, oidcAgentsAudience)
	expired["iat"], expired["exp"] = time.Now().Add(-2*time.Hour).Unix(), time.Now().Add(-time.Hour).Unix()
	wrongIssuer := issuer.Claims(oidcAgentsSubject, oidcAgentsAudience)
	wrongIssuer["iss"] = "https://other-issuer.example"
	other := oidctest.New(t)
	otherKey := other.PublishKey(t, "pod-key")
	for name, token := range map[string]string{
		"an expired token":               issuer.Mint(t, key, expired),
		"a token for another audience":   issuer.Mint(t, key, issuer.Claims(oidcAgentsSubject, "another-audience")),
		"a token naming another issuer":  issuer.Mint(t, key, wrongIssuer),
		"a token another issuer signed":  other.Mint(t, otherKey, other.Claims(oidcAgentsSubject, oidcAgentsAudience)),
		"another issuer's key, our name": issuer.Mint(t, otherKey, issuer.Claims(oidcAgentsSubject, oidcAgentsAudience)),
	} {
		if response := whoamiAs(handler, token, ""); response.Code != http.StatusUnauthorized {
			t.Errorf("%s: GET /api/v1/whoami answered %d %s, want 401", name, response.Code, response.Body.String())
		}
	}

	pair["DISPATCH_AGENT_TOKEN"] = "agent-token"
	control := oidcAgentsRouter(t, resolveWith(t, pair))
	if response := whoamiAs(control, "agent-token", ""); !answersSharedToken(response) {
		t.Errorf("control, the shared token set: GET /api/v1/whoami with it answered %d %s, want the shared token", response.Code, response.Body.String())
	}
	if response := whoamiAs(control, valid, ""); !answersService(response, oidcAgentsSubject) {
		t.Errorf("control, the shared token set: a valid projected token answered %d %s, want the service", response.Code, response.Body.String())
	}
}
