package api

import (
	"net/http"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/settings/project-default-assignees",
	Auth:        authAny,
	Description: "List every project with the person it defaults to, by project key: every project names one, and that person is the last step of the chain that decides who a question reaches. Needs the person scope, as the default names a person: a credential the deployment configured, the shared token or a pod's, holds none.",
	Handler:     (*server).listProjectDefaultAssignees,
	// The rows that read or write what belongs to the calling person: their inbox, their own
	// answers and replies, their read marks, their conversations with sessions, and the people
	// and default assignees an assignee picker reads.
	OAuthScopes: routeScopeSet{model.ScopePerson},
	Shape:       routeShape{Status: http.StatusOK, Response: []projectDefaultAssignee{}},
})
