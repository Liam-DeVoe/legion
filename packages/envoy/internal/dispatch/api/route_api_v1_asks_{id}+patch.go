package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPatch,
	Pattern:     "/api/v1/asks/{id}",
	Auth:        authAny,
	Description: "Reword an open ask (its author, or a credential holding the person scope); a block ask's `:::ask` block is rewritten with it.",
	Handler:     (*server).editAsk,
	WriteClass:  scopeProject,
	ID:          &idRoute{Kind: "ask", NotFound: "NOT_FOUND"},
	Shape:       routeShape{Status: http.StatusOK, Response: shapedAsk{}, Request: editAskInput{}},
})
