package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPost,
	Pattern:     "/api/v1/issues/{key}/claim",
	Auth:        authAny,
	Description: "Claim the issue for the calling session (or human): records who is working it. The status does not move. The answer is the issue with legion_managed_status beside it: true when the issue carries the legion label in any case, or sits under one that does, which means the Legion daemon owns the status and a status another writer sets is undone. 409 ISSUE_CLAIMED names a live holder (or a human one, whose claim has no session to end); a caller whose credential holds the person scope may pass {force: true} to take it anyway, and one that does not is 403 OAUTH_SCOPE_REQUIRED. 409 CLAIM_CONTENDED means the holder changed twice while the request ran, so nothing was applied and nobody's liveness was checked: read the issue and retry.",
	Handler:     (*server).claimIssue,
	WriteClass:  scopeProject,
	Shape: routeShape{
		Status: http.StatusOK, Response: claimAnswer{}, Request: claimIssueInput{},
		Refusals: []routeAnswer{{Status: http.StatusConflict, Body: issueClaimedError{}}},
	},
})
