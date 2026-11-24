package api

import (
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPatch,
	Pattern:     "/api/v1/comments/{id}",
	Auth:        authAny,
	Description: "Edit a comment's body. The dashboard editor also sends blocks ([{id, from}], from in UTF-16 code units into the new body) and base_revision, the revision GET /api/v1/comments/{id}/blocks gave, so each block keeps its id; without them the ids follow the text.",
	Handler:     (*server).editComment,
	WriteClass:  scopeProject,
	ID:          &idRoute{Kind: "comment", NotFound: "NOT_FOUND"},
	OAuthScopes: routeScopeSet{model.ScopePerson},
	Shape:       routeShape{Status: http.StatusOK, Response: shapedComment{}, Request: editCommentInput{}},
})
