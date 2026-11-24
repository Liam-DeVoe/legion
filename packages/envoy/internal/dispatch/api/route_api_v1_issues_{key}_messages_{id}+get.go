package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/issues/{key}/messages/{id}",
	Auth:        authAny,
	Description: "Read one message with its deliveries and replies.",
	Handler:     (*server).getMessage,
	ID:          &idRoute{Kind: "message", WithinIssue: true, NotFound: "NOT_FOUND"},
	Shape:       routeShape{Status: http.StatusOK, Response: messageRead{}},
})
