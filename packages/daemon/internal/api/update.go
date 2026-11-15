package api

import (
	"net/http"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// readyForUpdate is the worker's answer to its update round's ask: it has pushed everything it
// wants kept, and the process holding its claim may now be replaced. The machine records the
// signal and the head the worker reports (supervise.PodUpdateSignalled); the round's own sweep is
// what acts on it. An answer only brings the round's end forward: the round ends early once every
// claim it asks has answered and no turn runs, and at its bound (pod_update_grace_minutes) it ends
// whatever the claims answered.
//
// `round` is required, and is the round the worker answers for, as the ask it read named it: the
// machine takes the answer only for the round its claim stands in, and refuses one naming any
// other round (supervise.ErrForeignUpdateRound), which claimFailure answers 409 with the sentence
// the agent reads. An answer with no round at all would be an answer for every round in turn,
// which is the one thing the round carries it to stop.
//
// It does not call refuseIfStaleClaim, and may not: the claims this route exists to hear from are
// exactly the stale ones. A claim in an open update round is served on its previous contract
// (staleClaim) so that it can finish its turn, push and signal here; refusing it would leave the
// round no early answer from it, and the round would wait for its bound.
//
// The project release asks its roles under a round of its own, "release:<id>" (decision 6 of
// dispatch://INFRA-3583's plan), through the plugin's same operation, which takes any round. An
// answer naming the running release's round is handed to the release (Options.ProjectRelease),
// which checks the claim's session and generation and records the answer on its own record; the
// claim's machine is asked nothing, so no round column of the claim records it. Any other round
// goes to the machine, which refuses one its claim is not in, a release's round no release runs
// any more among them.
func (s *server) readyForUpdate(w http.ResponseWriter, r *http.Request) {
	var req claim.ReadyForUpdateRequest
	if !s.readBody(w, r, &req, s.claimSecretCheck) || !requireFields(w,
		field{"claimToken", string(req.ClaimToken)}, field{"sessionId", req.SessionID},
		field{"secret", req.Secret}, field{"round", req.Round},
	) || !requireGeneration(w, req.Generation) {
		return
	}
	m, _, ok := s.authenticated(req.ClaimToken, req.Secret)
	if !ok {
		writeJSON(w, claim.InvalidSecret.Status, claim.InvalidSecret)
		return
	}
	ctx, decided := s.decision(r, req.ClaimToken)
	defer decided()
	if s.projectRelease != nil {
		if round := s.projectRelease.AskRound(); round != "" && req.Round == round {
			if err := s.projectRelease.Answer(ctx, req.ClaimToken, req.SessionID, req.Generation, req.Head); err != nil {
				s.claimFailure(w, "ready_for_update", req.ClaimToken, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	err := m.Handle(ctx, supervise.PodUpdateSignalled{
		Claim: req.ClaimToken, Generation: req.Generation, Session: req.SessionID,
		Round: req.Round, Head: req.Head,
	})
	if err != nil {
		s.claimFailure(w, "ready_for_update", req.ClaimToken, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
