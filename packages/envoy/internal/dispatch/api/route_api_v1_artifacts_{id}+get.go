package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/artifacts/{id}",
	Auth:        authAny,
	Description: "Read a document's metadata and current version.",
	Handler:     (*server).getArtifact,
	// A document's own routes, and the caller's read mark on one: the {id} is an artifact,
	// looked for among every document and file, as a whole artifact id is. Every one of them
	// reads the row through loadArtifact, which answers ARTIFACT_NOT_FOUND.
	ID:    &idRoute{Kind: "artifact", NotFound: "ARTIFACT_NOT_FOUND"},
	Shape: routeShape{Status: http.StatusOK, Response: artifactRead{}},
})
