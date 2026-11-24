package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodGet,
	Pattern:     "/api/v1/github/app-installation/{owner}/{repo}",
	Auth:        authAny,
	Description: "Whether Dispatch's GitHub App, which GitHub sends the repository's webhook events through, is installed on the repository: 200 {installed}, false when GitHub answers 404; 400 INVALID_REPOSITORY, before GitHub is asked anything, when the path's owner or name has a character GitHub does not allow or the name is . or .. or empty or longer than 100 characters (a trailing .git trimmed); 503 GITHUB_APP_UNCONFIGURED when Dispatch holds no App key, 502 GITHUB_UNREADABLE naming the repository and GitHub's error on any other failure.",
	Handler:     (*server).getAppInstallation,
	Shape:       routeShape{Status: http.StatusOK, Response: appInstallationResponse{}},
})
