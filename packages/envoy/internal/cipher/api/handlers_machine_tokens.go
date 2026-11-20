// handlers_machine_tokens.go: GET /v1/admin/machine-tokens?project=<KEY> — how many live tokens
// a developer box holds that list one project. Dispatch reads it before it completes a project's
// switch to private: the exchange mints no such token for a project Dispatch answers private or
// closing (internal/cipher/oauth's projectAccess), and Dispatch makes the project private only
// once none minted before is still live. Part of the routes a listed service calls with its own
// ServiceAccount token (accountAuth), behind the power only Dispatch's caller name holds.
package api

import (
	"net/http"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/oauthwire"
)

// machineTokensResponse is GET /v1/admin/machine-tokens's answer.
type machineTokensResponse struct {
	// Project is the key the request named.
	Project string `json:"project"`
	// LiveTokens is how many live access tokens act as their person and list the project: a
	// machine login's trades and a person's sign-ins on a machine, which are a developer box's.
	LiveTokens int `json:"live_tokens"`
	// LastExpiresAt is when the last of them expires, null when none is live: the latest a
	// switch to private waits for, unless they are revoked first.
	LastExpiresAt *time.Time `json:"last_expires_at"`
}

// countMachineTokens answers how many live developer-box tokens list the project ?project= names
// (store.Store.LiveDeveloperBoxTokens). The key is read as the exchange reads a project
// (oauthwire.ParseProjects): one key, or 400 PROJECT_INPUT. A key no token lists, one Dispatch
// does not hold among them, answers 0: the broker holds no copy of Dispatch's projects, and the
// one caller that may ask, Dispatch, knows which it holds.
func (s *server) countMachineTokens(w http.ResponseWriter, r *http.Request, _ string) {
	projects, err := oauthwire.ParseProjects(r.URL.Query().Get("project"))
	if err != nil || len(projects) != 1 {
		writeError(w, http.StatusBadRequest, "PROJECT_INPUT", "project must be one project key, as Dispatch writes one")
		return
	}
	live, err := s.deps.Enroll.Store.LiveDeveloperBoxTokens(r.Context(), projects[0])
	if err != nil {
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "count the live developer-box tokens", err)
		return
	}
	answer := machineTokensResponse{Project: projects[0], LiveTokens: live.Live}
	if !live.LastExpiresAt.IsZero() {
		last := live.LastExpiresAt.UTC()
		answer.LastExpiresAt = &last
	}
	writeJSON(w, http.StatusOK, answer)
}
