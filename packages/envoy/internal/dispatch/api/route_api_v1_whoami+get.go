package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/whoami",
	Auth:        authAny,
	Description: "Who the server takes the caller for: {kind: user, login, scopes} (login is the person's lowercase email) or {kind: agent, owner, service, session, project, scopes} (owner is a personal token's owner or a broker-issued token's person by lowercase email, null for the shared token and for a broker-issued token that names a service; service is a verified service-account token's Kubernetes subject, or the fleet a broker-issued token naming no person belongs to, null for every other credential; session is the session the caller's credential proved, null for a credential that proves none, which every one but an access token the secrets broker issued is; project is the one project key that credential lets the caller write, null where it names none). scopes is the OAuth scopes the credential holds, an empty array for one that holds none: person acts as the person on every route the dashboard reaches, and approve settles what a person settles. A write records its actor with proven: true only where this credential proved that actor, the person at a browser or the session above, and never for a session a request names under a credential that proves none.",
	Handler:     (*server).whoami,
	Shape: routeShape{
		Status: http.StatusOK, Response: whoamiUser{},
		// A caller the server takes for an agent is answered the other shape at the same
		// status (issue_assignee.go).
		Also: []routeAnswer{{Status: http.StatusOK, Body: whoamiAgent{}}},
	},
})
