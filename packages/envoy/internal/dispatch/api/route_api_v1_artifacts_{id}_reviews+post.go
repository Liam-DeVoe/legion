package api

import (
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/artifacts/{id}/reviews",
	Auth:        authAny,
	Description: "Approve or request changes on a document's latest settled version; answers the open approval ask when present, preserving its thread, and reviews every document that ask named.",
	Handler:     (*server).createArtifactReview,
	WriteClass:  scopeProject,
	ID:          &idRoute{Kind: "artifact", NotFound: "ARTIFACT_NOT_FOUND"},
	// The rows that settle what a person settles: a document's review, a credential's fate, and
	// a personal token's own scopes, which a credential may not widen past its own.
	OAuthScopes: routeScopeSet{model.ScopePerson, model.ScopeApprove},
	Shape: routeShape{
		Status: http.StatusCreated, Response: model.ArtifactReview{}, Request: createArtifactReviewInput{},
	},
})
