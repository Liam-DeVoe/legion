package api

import (
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodDelete,
	Pattern:     "/api/v1/issues/{key}/claim",
	Auth:        authAny,
	Description: "Release the issue's claim: its holder, a caller whose credential holds the person scope, or anyone when the holding session is no longer live. The status does not move. The answer is the issue with legion_managed_status beside it: true when the issue carries the legion label in any case, or sits under one that does, which means the Legion daemon owns the status and a status another writer sets is undone. 409 ISSUE_CLAIMED names the holder this caller may not release (there is no force on this route); 409 CLAIM_CONTENDED means the holder changed twice while the request ran, so nothing was applied and nobody's liveness was checked.",
	Handler:     (*server).releaseIssueClaim,
	WriteClass:  scopeProject,
	Shape: routeShape{
		Status: http.StatusOK, Response: claimAnswer{}, Request: actorInput{},
		Refusals: []routeAnswer{{Status: http.StatusConflict, Body: issueClaimedError{}}},
	},
})
