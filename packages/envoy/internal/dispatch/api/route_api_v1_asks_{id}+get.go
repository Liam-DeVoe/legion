package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/asks/{id}",
	Auth:        authAny,
	Description: "Read one ask with its replies, edit history, answers and followers, plus claimant (the session holding its issue's claim) and route (its issue's route), whom a person's answer and replies also reach, and route_holder (the live session that route reaches now: the session itself or the role's holder); each is null when absent, route_holder also when nobody holds the route or the Envoy listener did not answer, and all are null on a project document's ask.",
	Handler:     (*server).getAsk,
	// An ask's own routes and the caller's snooze of one. Each reads the row through loadAsk
	// or loadAskOwner, whose missing row is writeHandlerError's NOT_FOUND; the snooze write
	// answers its own NOT_FOUND, and the un-snooze answers 204 for any ask at all.
	ID:    &idRoute{Kind: "ask", NotFound: "NOT_FOUND"},
	Shape: routeShape{Status: http.StatusOK, Response: askRead{}},
})
