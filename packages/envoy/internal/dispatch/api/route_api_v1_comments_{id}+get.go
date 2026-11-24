package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/comments/{id}",
	Auth:        authAny,
	Description: "Read one comment with its thread.",
	Handler:     (*server).getComment,
	// A comment's own routes. The reads and the thread writes answer writeHandlerError's
	// NOT_FOUND; the two delivery routes answer COMMENT_NOT_FOUND of their own.
	ID:    &idRoute{Kind: "comment", NotFound: "NOT_FOUND"},
	Shape: routeShape{Status: http.StatusOK, Response: commentRead{}},
})
