package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/asks/{id}/resolve",
	Auth:        authAny,
	Description: "Retract or resolve an ask without answering it (kind retracted or resolved; withdrawn is written only by an issue's close, and an ask on a closed issue is 409 ISSUE_CLOSED); a block ask's block records it too. A person who will not answer declines instead, on a route of their own, POST /api/v1/asks/{id}/decline.",
	Handler:     (*server).resolveAsk,
	WriteClass:  scopeProject,
	ID:          &idRoute{Kind: "ask", NotFound: "NOT_FOUND"},
	Shape:       routeShape{Status: http.StatusOK, Response: shapedAsk{}, Request: resolveAskInput{}},
})
