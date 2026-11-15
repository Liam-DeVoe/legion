package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// ClaimOperationResponse is what the controller's four claim operations answer — stop, retry,
// suspend and resume (claimStop, claimRetry, claimSuspend, claimResume): the claim as the request
// left it, read from its machine's published view as every operator request on a claim reads it
// (claimRequest).
//
// SuspendedBy and SuspensionHeldBy say who holds a suspension over the claim: `controller` for one
// the controller placed, which its own resume lifts, and `operator` for the operator's, which only
// the operator lifts (suspendedByOperator). These four routes are the one session-facing surface
// that names a suspender. The state does not, and must not: a session parses the state with the
// schema of its own plugin's contract, and a key added there refuses the whole state in every pod
// whose plugin predates INFRA-1048 (StrandedClaim). Only a plugin at the contract that added these
// routes calls them, so no reader of this body predates its keys.
type ClaimOperationResponse struct {
	// State is the claim's state after the request: retired after a stop; suspended after a
	// suspension that landed, and still working or launching after one held for the agent's
	// turn or its ready (the 202 answer); launching after a resume, or a retry the claim's own
	// machine ran; and still failed or retired after a retry of a phase worker, whose start the
	// issue's phase queued and runs once that start comes (the 202 answer, retryStart).
	State string `json:"state"`
	// SuspendedBy is who suspended the claim while it is suspended — operator, controller,
	// daemon, exit, released or unknown (supervise.SuspendSource) — and absent otherwise.
	SuspendedBy string `json:"suspendedBy,omitempty"`
	// SuspensionHeld is a suspension held for the agent's turn or ready, and SuspensionHeldBy who
	// asked for it; both are absent when none is held.
	SuspensionHeld   bool   `json:"suspensionHeld,omitempty"`
	SuspensionHeldBy string `json:"suspensionHeldBy,omitempty"`
}

// suspendedByOperator is the refusal of a request of the controller's that would get past a
// suspension the operator holds: its resume, which would lift it; its suspend, which would take
// it over or change nothing; its stop, which would retire the claim under it for a retry to
// relaunch; and its fresh session, which would resume it (supervise.ErrOperatorHolds). Only the
// operator undoes the operator's decision, so the sentence sends the controller to the operator.
const suspendedByOperator = "SUSPENDED_BY_OPERATOR"

// controllerSuspendReason is what the controller's suspension records as its cause.
const controllerSuspendReason = "the controller suspended it"

// The controller's four claim operations in the words their refusals name them by
// (controllerOp). Each takes the name and the refusal of the role `controller` its door gives
// (controllerClaim); the two that refuse a claim in a turn, the retry and the resume, also name
// the states they do reach (refuseClaimInATurn). The turn's cut and the live states are the two
// recoveries' own (freshSessionOp, compactOp): a stop ends a turn by design, and a suspension is
// held for the turn's end.
var (
	stopOp = controllerOp{
		name:             "a stop",
		notTheController: "the controller does not stop its own claim",
	}
	retryOp = controllerOp{
		name:             "a retry",
		notTheController: "the controller does not retry its own claim",
		reaches:          "A retry reaches a claim that failed and one a stop retired",
	}
	suspendOp = controllerOp{
		name:             "a suspension",
		notTheController: "the controller does not suspend its own claim",
	}
	resumeOp = controllerOp{
		name:             "a resume",
		notTheController: "the controller does not resume its own claim",
		reaches:          "A resume reaches a suspended claim the operator's own suspension does not hold",
	}
)

// claimStop is the controller's door onto the operator's `legion claims stop` (stopEvent): the
// claim is released and retired, a turn it is in cut off with it. The one difference is the
// operator's hold, which the controller's stop does not end (supervise.RequestStop.ByController):
// a stop would retire the claim, and the controller's retry would then relaunch it past that
// hold.
func (s *server) claimStop(w http.ResponseWriter, r *http.Request) {
	s.claimEventOperation(w, r, stopOp, "stop", "stopped", func(token claim.Token) supervise.Event {
		return supervise.RequestStop{Claim: token, ByController: true}
	})
}

// claimSuspend is the controller's door onto the operator's `legion claims suspend`
// (suspendEvent): the claim's process is stopped and its session kept, and a suspension that
// reaches a claim in a turn, or one whose launch has not said ready, is held for that turn's end
// or that ready, bounded at the stop timeout as the operator's is, and answered 202. The
// suspension is recorded as the controller's (supervise.SuspendedByController): it holds against
// every rule as the operator's does, the controller's own resume lifts it, and the operator's
// lifts it too. One the operator already holds is refused (SUSPENDED_BY_OPERATOR).
func (s *server) claimSuspend(w http.ResponseWriter, r *http.Request) {
	s.claimEventOperation(w, r, suspendOp, "suspend", "suspended", func(token claim.Token) supervise.Event {
		return supervise.RequestSuspend{Claim: token, Reason: controllerSuspendReason, By: supervise.SuspendedByController}
	})
}

// claimResume is the controller's door onto the operator's `legion claims resume` (resumeEvent):
// the suspended claim's session is relaunched, whoever suspended it but the operator, whose own
// suspension only the operator lifts (supervise.RequestResume.ByController).
func (s *server) claimResume(w http.ResponseWriter, r *http.Request) {
	s.claimEventOperation(w, r, resumeOp, "resume", "resumed", func(token claim.Token) supervise.Event {
		return supervise.RequestResume{Claim: token, ByController: true}
	})
}

// claimRetry is the controller's door onto the operator's plain `legion claims retry`
// (retryStart), with no fresh session: a claim that failed, or that a stop retired, is started
// again in the session it recorded — a phase worker through its issue's phase, which hands it
// that phase's task, and an architect by its own machine. The store records no one as having
// retired a claim (supervise.Machine.retire writes the state alone), so a claim the operator
// stopped is retried here as one the controller stopped is. A claim stored working is refused as
// a claim in a turn before anything is asked (refuseClaimInATurn); every other refusal is the
// effect's own, in the words the operator's door gives it.
func (s *server) claimRetry(w http.ResponseWriter, r *http.Request) {
	target, ok := s.controllerClaim(w, r, retryOp)
	if !ok {
		return
	}
	if target.machine.View().State == supervise.StateWorking {
		refuseClaimInATurn(w, target.token, retryOp)
		return
	}
	status, ok := s.retryStart(w, r, target.machine, target.token, false)
	if !ok {
		return
	}
	s.answerClaimOperation(w, target, "retried", status)
}

// claimEventOperation is the controller's door onto one operator request a single event to the
// claim's machine makes (claimRequest): the claim is named by its issue and role and the caller
// authenticated as the project's controller (controllerClaim), the event is posted, and the
// claim is answered as it then stands. A suspension the machine holds for the agent's turn is no
// refusal and answers 202, as the operator's does.
func (s *server) claimEventOperation(w http.ResponseWriter, r *http.Request, op controllerOp, request, did string,
	event func(claim.Token) supervise.Event) {
	target, ok := s.controllerClaim(w, r, op)
	if !ok {
		return
	}
	posted, err := s.postClaimEvent(w, r, target.machine, target.token, request, event(target.token))
	if !posted {
		return
	}
	status := http.StatusOK
	if errors.Is(err, supervise.ErrSuspendHeld) {
		status = http.StatusAccepted
	} else if err != nil {
		s.refuseClaimOperation(w, request, target.token, err, op)
		return
	}
	s.answerClaimOperation(w, target, did, status)
}

// refuseClaimOperation answers an event of the controller's that the claim's machine refused:
// the operator's hold in its own code (refuseOperatorHold), a claim stored working that the
// operation does not reach as a claim in a turn (refuseClaimInATurn), and every other refusal as
// the operator's door answers it, in the machine's own words (operatorFailure).
func (s *server) refuseClaimOperation(w http.ResponseWriter, request string, token claim.Token, err error, op controllerOp) {
	var refused *supervise.RefusedError
	switch {
	case errors.Is(err, supervise.ErrOperatorHolds):
		refuseOperatorHold(w, token, op)
	case op.reaches != "" && errors.As(err, &refused) && refused.State == supervise.StateWorking:
		refuseClaimInATurn(w, token, op)
	default:
		s.operatorFailure(w, request, token, err)
	}
}

// refuseOperatorHold is the refusal of a request of the controller's that the operator's own
// suspension stands in the way of (suspendedByOperator), which names who lifts it. Nothing of the
// claim changed.
func refuseOperatorHold(w http.ResponseWriter, token claim.Token, op controllerOp) {
	writeFailure(w, http.StatusConflict, suspendedByOperator, fmt.Sprintf(
		"%s is held by the operator's own suspension, which only the operator lifts, so %s by the controller is refused and nothing of the claim changed. Ask the operator: `legion claims resume --claim %s` is theirs to run",
		token, op.name, token))
}

// refuseClaimInATurn is the retry's and the resume's refusal of a claim stored working
// (claimInATurn): neither reaches a claim in a turn, the machine refuses each for a claim that
// has not failed or is not suspended, and the code is the one both recoveries give a claim stored
// working, so the controller reads one code for it whichever operation it asked. The sentence
// names the states the operation reaches and sends the caller to the state (askAgainLater).
func refuseClaimInATurn(w http.ResponseWriter, token claim.Token, op controllerOp) {
	writeFailure(w, http.StatusConflict, claimInATurn, fmt.Sprintf(
		"%s is stored working, and %s reaches no claim in a turn. %s. %s", token, op.name, op.reaches, askAgainLater))
}

// answerClaimOperation writes the claim as the controller's operation left it
// (ClaimOperationResponse) under status, and logs what the controller did to which claim, as the
// controller named it.
func (s *server) answerClaimOperation(w http.ResponseWriter, target controllerTarget, did string, status int) {
	view := target.machine.View().Claim
	answer := ClaimOperationResponse{
		State:            string(view.State),
		SuspensionHeld:   view.SuspensionHeld,
		SuspensionHeldBy: string(view.SuspensionHeldBy),
	}
	if view.State == supervise.StateSuspended {
		answer.SuspendedBy = string(view.SuspendedBy)
	}
	s.log.Info("api: the controller "+did+" a claim", "claim", target.token, "issue", target.issue, "role", target.role,
		"state", answer.State, "suspendedBy", answer.SuspendedBy, "suspensionHeldBy", answer.SuspensionHeldBy, "status", status)
	writeJSON(w, status, answer)
}
