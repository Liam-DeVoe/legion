package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/artifacts/{id}/comments",
	Auth:        authAny,
	Description: "Comment on a document or one of its quotes.",
	Handler:     (*server).createArtifactComment,
	WriteClass:  scopeProject,
	ID:          &idRoute{Kind: "artifact", NotFound: "ARTIFACT_NOT_FOUND"},
	Shape: routeShape{
		Status: http.StatusCreated, Response: commentWriteResponse{}, Request: commentInput{}, Advice: writeAdvice{},
	},
})
