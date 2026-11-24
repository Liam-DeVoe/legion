package api

import (
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/me/agent-tokens",
	Auth:        authAny,
	Description: "Mint a personal agent token {name, scopes?}: scopes is the set the new token holds, defaulting to the scopes the caller's own credential holds. A scope this deployment does not know, or approve without person, is 400 TOKEN_SCOPES_INPUT; a scope the caller does not hold itself is 403 OAUTH_SCOPE_REQUIRED. The answer is the one time the token's plaintext is shown.",
	Handler:     (*server).createAgentToken,
	WriteClass:  scopeSession,
	OAuthScopes: routeScopeSet{model.ScopePerson, model.ScopeApprove},
	Shape: routeShape{
		Status: http.StatusCreated, Response: createAgentTokenResponse{}, Request: createAgentTokenInput{},
	},
})
