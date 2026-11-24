package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/live/_test/disconnect",
	Auth:        authAny,
	Description: "Test hook: close every open live socket on every process with 1012, as a restart does.",
	Handler:     (*server).disconnectLiveSockets,
	TestHook:    true,
	WriteClass:  scopeDeployment,
	// The test hooks, mounted only with Deps.TestHooksEnabled (routes()).
	Shape: routeShape{Status: http.StatusOK, Response: testHookOK{}},
})
