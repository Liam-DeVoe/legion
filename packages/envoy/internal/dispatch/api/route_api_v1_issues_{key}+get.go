package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/issues/{key}",
	Auth:        authAny,
	Description: "Read an issue with its open asks, primary document, children, and progress: {tasks, children}, its spec's task-list items (`- [ ]` / `- [x]`, nested lists included) and its direct children as {done, total}, each null when it has none.",
	Handler:     (*server).getIssue,
	Shape:       routeShape{Status: http.StatusOK, Response: issueDetail{}},
})
