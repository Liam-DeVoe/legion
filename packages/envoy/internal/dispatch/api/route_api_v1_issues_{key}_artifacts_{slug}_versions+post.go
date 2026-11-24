package api

import (
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/issues/{key}/artifacts/{slug}/versions",
	Auth:        authAny,
	Description: "Name the current version of an issue document, by slug or unambiguous filename.",
	Handler:     (*server).createNamedVersion,
	WriteClass:  scopeProject,
	Shape: routeShape{
		Status: http.StatusCreated, Response: model.Version{}, Request: createVersionInput{},
	},
})
