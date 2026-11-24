package api

import (
	"fmt"
	"net/http"
)

var _ = declareRoute(routeDecl{
	Method:      http.MethodPut,
	Pattern:     "/api/v1/issues/{key}/role-holders",
	Auth:        authAny,
	Description: fmt.Sprintf("Publish the session that holds each Legion role of this issue {holders}: each entry {role, session}, role one of architect, planner, implementer, tester, reviewer and merger and session a session id of at most %d bytes, or [] for an issue where no session holds a role. The actor must be session legion-daemon:<project>, the key up to its first -, the session the Legion daemon of the issue's project writes as; any other actor is 403 ROLE_HOLDER_FORBIDDEN. Before the airlock a bearer caller names its own session, so any caller whose credential proves no session can name that one. The body is the issue's whole set and replaces the stored set under the issue's owner lock, so a session that it does not name holds no role of the issue, two writes at once store the set of the one that finishes last, and a pair that did not change keeps its recorded_at. The write appends no issue event, notifies nobody and takes a closed issue. Answers 200 {issue, holders} with the set this write stored, in workflow order, each {role, session, recorded_at}. A body without holders, a role that is not one of the six, a session that is not a session id or is over %d bytes, a role or a session named twice and an empty session are each 400 INVALID_ROLE_HOLDER, and nothing is stored; an issue Dispatch does not know is 404 ISSUE_NOT_FOUND. The session of an ended claim keeps its role until the daemon's next publication lands, and its process can still run in that window.", maxRoleHolderSessionBytes, maxRoleHolderSessionBytes),
	Handler:     (*server).putIssueRoleHolders,
	WriteClass:  scopeProject,
	Shape: routeShape{
		Status: http.StatusOK, Response: issueRoleHolders{}, Request: issueRoleHoldersInput{},
	},
})
