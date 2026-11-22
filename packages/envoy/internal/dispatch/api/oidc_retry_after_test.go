package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/oidc"
)

// TestAServiceTokenNobodyJudgedNamesItsWait: while the issuer's key set cannot be read, a
// service-account token is answered 503 OIDC_ISSUER_UNAVAILABLE with the shared sentence and
// Retry-After: 1, the secrets broker's and the Envoy listener's answer to the same fault. A token
// for another audience, which the verifier judges with no key, is 401 OIDC_TOKEN_INVALID and names
// no wait, since no wait changes that answer.
func TestAServiceTokenNobodyJudgedNamesItsWait(t *testing.T) {
	issuer := newServiceTokenIssuer(t)
	verifier := issuer.verifier(t)
	// The verifier read the discovery document and no key yet, so every token's key is a read of
	// the key set, which fails from here on.
	issuer.FailKeys(http.StatusServiceUnavailable)
	handler, _, _ := newTestServer(t, testServerOptions{oidc: verifier})

	unjudged := agentRequest(t, handler, http.MethodGet, "/api/v1/whoami", nil, issuer.mint(t, serviceTokenAudience))
	if body := unjudged.Body.String(); unjudged.Code != http.StatusServiceUnavailable ||
		!strings.Contains(body, `"code":"`+oidc.IssuerUnavailableCode+`"`) || !strings.Contains(body, oidc.IssuerUnavailableMessage) {
		t.Fatalf("a service-account token while the key set cannot be read: status=%d body=%s, want 503 %s %q",
			unjudged.Code, body, oidc.IssuerUnavailableCode, oidc.IssuerUnavailableMessage)
	}
	if got := unjudged.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("the 503 carries Retry-After %q, want \"1\", the secrets broker's wait for the same fault", got)
	}

	refused := agentRequest(t, handler, http.MethodGet, "/api/v1/whoami", nil, issuer.mint(t, "envoy"))
	if refused.Code != http.StatusUnauthorized || !strings.Contains(refused.Body.String(), `"code":"OIDC_TOKEN_INVALID"`) {
		t.Fatalf("a token for another audience: status=%d body=%s, want 401 OIDC_TOKEN_INVALID", refused.Code, refused.Body.String())
	}
	if got := refused.Header().Get("Retry-After"); got != "" {
		t.Fatalf("the 401 carries Retry-After %q, want none", got)
	}
}
