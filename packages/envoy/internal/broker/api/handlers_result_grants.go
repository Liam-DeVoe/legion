package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sjawhar/envoy/internal/broker/resultgrant"
	"github.com/sjawhar/envoy/internal/brokerclient"
)

type resultGrantRequest struct {
	// The job's signed GitHub OIDC token, traded only inside the broker under its own key.
	SubjectToken string `json:"subject_token"`
}

// resultGrantResponse names a live, pod-owned capability, never its token or signing key.
type resultGrantResponse struct {
	// The grant whose opaque placeholder authenticates the proxy connection.
	GrantID string `json:"grant_id"`
	// The end of the capability; it is never renewed into another identity.
	ExpiresAt time.Time `json:"expires_at"`
	// The ordinary airlock connection details, containing placeholders only.
	Airlock *airlockRelease `json:"airlock"`
}

// createResultGrant authenticates through the existing OAuth exchange. Public here means
// no enrollment proof: the signed job identity is the credential, never a requested scope.
func (s *server) createResultGrant(w http.ResponseWriter, r *http.Request) {
	if !s.tokenAllows(r) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many token requests; try again later")
		return
	}
	if s.deps.ResultGrants == nil {
		writeError(w, http.StatusServiceUnavailable, "RESULT_GRANTS_OFF", "this broker has no results airlock configured")
		return
	}
	var body resultGrantRequest
	if !readJSON(w, r, &body, "INVALID_RESULT_GRANT") {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	answer, err := s.deps.ResultGrants.Create(ctx, body.SubjectToken)
	var refused *brokerclient.RefusedError
	switch {
	case errors.As(err, &refused) && refused.Status == http.StatusTooManyRequests:
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", refused.Error())
	case errors.As(err, &refused) && refused.Status >= http.StatusInternalServerError:
		writeError(w, http.StatusServiceUnavailable, "RESULT_GRANT_UNAVAILABLE", refused.Error())
	case errors.As(err, &refused):
		writeError(w, http.StatusForbidden, "RESULT_GRANT_REFUSED", refused.Error())
	case errors.Is(err, resultgrant.ErrNotResults):
		writeError(w, http.StatusForbidden, "RESULTS_SCOPE_REQUIRED", err.Error())
	case err != nil:
		writeUnavailable(w, "RESULT_GRANT_UNAVAILABLE", "make results grant", err)
	default:
		writeJSON(w, http.StatusOK, resultGrantResponse{GrantID: answer.GrantID, ExpiresAt: answer.ExpiresAt, Airlock: airlockReleaseOf(&answer.Airlock)})
	}
}
