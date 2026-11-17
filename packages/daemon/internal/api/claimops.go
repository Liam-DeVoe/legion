package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// ClaimOperationResponse is what the claim door's four claim operations answer — stop, retry,
// suspend and resume (claimStop, claimRetry, claimSuspend, claimResume), the controller's and an
// architect's alike: the claim as the request left it, read from its machine's published view as
// every operator request on a claim reads it (claimRequest).
//
// SuspendedBy and SuspensionHeldBy say who holds a suspension over the claim: `architect` for one
// an architect placed, which an architect's resume lifts and so does the controller's;
// `controller` for one the controller placed, which its own resume lifts and an architect's does
// not (suspendedByController); and `operator` for the operator's, which only the operator lifts
// (suspendedByOperator). These four routes are the one session-facing surface that names a
// suspender. The state does not, and must not: a session parses the state with the schema of its
// own plugin's contract, and a key added there refuses the whole state in every pod whose plugin
// predates the stranded-claim reading (StrandedClaim). Only a plugin at the contract that added these routes calls
// them, so no reader of this body predates its keys.
type ClaimOperationResponse struct {
	// State is the claim's state after the request: retired after a stop; suspended after a
	// suspension that landed, and still working or launching after one held for the agent's
	// turn or its ready (the 202 answer); launching after a resume, or a retry the claim's own
	// machine ran; and still failed or retired after a retry of a phase worker, whose start the
	// issue's phase queued and runs once that start comes (the 202 answer, retryStart).
	State string `json:"state"`
	// SuspendedBy is who suspended the claim while it is suspended — operator, controller,
	// architect, daemon, exit, released or unknown (supervise.SuspendSource) — and absent
	// otherwise.
	SuspendedBy string `json:"suspendedBy,omitempty"`
	// SuspensionHeld is a suspension held for the agent's turn or ready, and SuspensionHeldBy who
	// asked for it; both are absent when none is held.
	SuspensionHeld   bool   `json:"suspensionHeld,omitempty"`
	SuspensionHeldBy string `json:"suspensionHeldBy,omitempty"`
}

// The refusals of a request the claim door's caller may not make past a suspension
// (refuseHold), each 409. Each says what of the claim changed, which is nothing but where a
// fresh session's own suspension ran first (nothingChanged, itsSuspensionTakenOver).
const (
	// suspendedByOperator: a request of the controller's or an architect's that would get past a
	// suspension the operator holds, held for a turn or landed — its resume, which would lift it;
	// its suspend, which would take it over or change nothing; its stop, which would retire the
	// claim under it for a retry to relaunch; its retry; and its fresh session, which would
	// resume it — and, for an architect, its compaction as well, so every operation of the door
	// but the controller's compaction (supervise.ErrOperatorHolds, claimOp.controllerPassesHold).
	// Only the operator undoes the operator's decision, so the sentence sends the caller to the
	// operator.
	suspendedByOperator = "SUSPENDED_BY_OPERATOR"
	// suspendedByController: an architect's operation on a claim the controller's suspension
	// holds, held or landed (supervise.ErrControllerHolds). The controller's suspension is
	// project-wide work — a deploy, a merge hold, a stuck pod — which a tree's architect must not
	// undo, so the sentence sends the architect to the controller.
	suspendedByController = "SUSPENDED_BY_CONTROLLER"
	// suspendedByDaemon: an architect's request that would lift, take over or end a claim under a
	// suspension the daemon placed rather than an architect (supervise.ErrNotTheArchitectsSuspension):
	// its resume, or its fresh session, of a claim the daemon suspended or released, and its suspend
	// or stop of a claim the daemon's own suspension holds — a park, a linger, a close, a root's
	// exit, or one recorded before sources existed — which, taken over, its resume would then undo,
	// and which, stopped, its retry would relaunch.
	suspendedByDaemon = "SUSPENDED_BY_DAEMON"
)

// What a refusal of a hold says became of the claim (refuseHold): nothing, for a request the
// hold refused before it changed anything, which is every such refusal but one; and the
// caller's own suspension and nothing more, for the fresh session whose forget a suspension
// above the caller refused after taking the claim over from that suspension, which leaves the
// claim suspended by the holder with the session it recorded (freshSession). Each reads after
// "so <operation> by <caller>", or after "so it" in the daemon's sentence.
const (
	nothingChanged         = "is refused and nothing of the claim changed"
	itsSuspensionTakenOver = "went no further than its own suspension of the claim, which that suspension then took over, and the claim keeps the session it recorded"
)

// The operator's four claim operations in the words their refusals name them by (claimOp), with
// the request each makes of the claim's machine and what its log line says it did
// (claimEventOperation). Each takes the name and the refusal of the role `controller` its door
// gives (claimDoor); the two that refuse a claim in a turn, the retry and the resume, say so
// and name the states they do reach (refusesATurn, refuseClaimInATurn). The turn's cut and the
// live states are the two recoveries' own (freshSessionOp, compactOp): a stop ends a turn by
// design, and a suspension is held for the turn's end.
var (
	stopOp = claimOp{
		name:        "a stop",
		invalidRole: "the controller does not stop its own claim",
		request:     "stop",
		did:         "stopped",
	}
	retryOp = claimOp{
		name:         "a retry",
		invalidRole:  "the controller does not retry its own claim",
		request:      "retry",
		did:          "retried",
		refusesATurn: true,
		reaches:      "A retry reaches a claim that failed and one a stop retired",
	}
	suspendOp = claimOp{
		name:        "a suspension",
		invalidRole: "the controller does not suspend its own claim",
		request:     "suspend",
		did:         "suspended",
	}
	resumeOp = claimOp{
		name:         "a resume",
		invalidRole:  "the controller does not resume its own claim",
		request:      "resume",
		did:          "resumed",
		refusesATurn: true,
		reaches:      "A resume reaches a suspended claim: the controller's every one but one the operator's own suspension holds, and an architect's one an architect suspended",
	}
)

// callerName is who came through a door of the claim operations (claimTarget.by, and the
// operator's fresh session), in the words its refusals and its log lines name it by. Each source
// that comes through one has its own words, and any other is named as the source it is, so a
// sentence never gives one caller's request another's name.
func callerName(by supervise.SuspendSource) string {
	switch by {
	case supervise.SuspendedByArchitect:
		return "the architect"
	case supervise.SuspendedByController:
		return "the controller"
	case supervise.SuspendedByOperator:
		return "the operator"
	default:
		return fmt.Sprintf("a caller of source %q, which no door of the claim operations lets in", by)
	}
}

// claimStop is the claim door's `legion claims stop` (stopEvent): the claim is released and
// retired, a turn it is in cut off with it. The one difference is a hold the caller may not get
// past, which its stop does not end (claimDoor, supervise.RequestStop.By,
// supervise.Claim.StopOrSuspendRefusal): the operator's for the controller, the operator's or the
// controller's for an architect (SUSPENDED_BY_OPERATOR, SUSPENDED_BY_CONTROLLER), and for an
// architect the daemon's own suspension too (SUSPENDED_BY_DAEMON) — a park, a linger, a close. A
// stop would retire the claim, and a retry would then relaunch it past that hold. An architect's
// stop of a claim the daemon released retires it, since any wake would bring that claim back
// anyway.
func (s *server) claimStop(w http.ResponseWriter, r *http.Request) {
	s.claimEventOperation(w, r, stopOp, func(target claimTarget) supervise.Event {
		return supervise.RequestStop{Claim: target.token, By: target.by}
	})
}

// claimSuspend is the claim door's `legion claims suspend` (suspendEvent): the claim's process is
// stopped and its session kept, and a suspension that reaches a claim in a turn, or one whose
// launch has not said ready, is held for that turn's end or that ready, bounded at the stop
// timeout as the operator's is, and answered 202. The suspension is recorded as the caller's
// (supervise.SuspendedByController, supervise.SuspendedByArchitect): it holds against every rule
// as the operator's does, and the caller's own resume lifts it. The operator's resume lifts
// either, and the controller's lifts an architect's. One a source above the caller already holds
// is refused (SUSPENDED_BY_OPERATOR, and for an architect SUSPENDED_BY_CONTROLLER), and so is an
// architect's of one the daemon's own suspension holds (SUSPENDED_BY_DAEMON); an architect's
// suspend of a released claim takes it over.
func (s *server) claimSuspend(w http.ResponseWriter, r *http.Request) {
	s.claimEventOperation(w, r, suspendOp, func(target claimTarget) supervise.Event {
		return supervise.RequestSuspend{Claim: target.token, Reason: callerName(target.by) + " suspended it", By: target.by}
	})
}

// claimResume is the claim door's `legion claims resume` (resumeEvent): the suspended claim's
// session is relaunched. The controller's resume lifts every suspension but the operator's own,
// which only the operator lifts; an architect's lifts an architect's suspension alone
// (claimDoor, supervise.RequestResume.By, supervise.Claim.ResumeRefusal).
func (s *server) claimResume(w http.ResponseWriter, r *http.Request) {
	s.claimEventOperation(w, r, resumeOp, func(target claimTarget) supervise.Event {
		return supervise.RequestResume{Claim: target.token, By: target.by}
	})
}

// claimRetry is the claim door's plain `legion claims retry` (retryStart), with no fresh
// session: a claim that failed, or that a stop retired, is started again in the session it
// recorded — a phase worker through its issue's phase, which hands it that phase's task, and an
// architect by its own machine. The store records no one as having retired a claim
// (supervise.Machine.retire writes the state alone), so a claim the operator stopped is retried
// here as one the controller stopped is. A claim a suspension above the caller holds, landed or
// held for its turn, is refused by the door before anything is asked, as every operation of the
// door is (claimDoor). A claim stored working is refused as a claim in a turn
// (refuseClaimInATurn); every other refusal is the effect's own, in the words the operator's door
// gives it.
func (s *server) claimRetry(w http.ResponseWriter, r *http.Request) {
	target, ok := s.claimDoor(w, r, retryOp)
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
	s.answerClaimOperation(w, target, retryOp.did, status)
}

// claimEventOperation is the claim door's one operator request a single event to the claim's
// machine makes (claimRequest), op.request: the claim is named by its issue and role and the
// caller authenticated as the project's controller or an architect that owns the claim's issue
// (claimDoor), the event is posted, and the claim is answered as it then stands, the log line
// saying the caller op.did it. A suspension the machine holds for the agent's turn is no refusal
// and answers 202, as the operator's does.
func (s *server) claimEventOperation(w http.ResponseWriter, r *http.Request, op claimOp,
	event func(claimTarget) supervise.Event) {
	target, ok := s.claimDoor(w, r, op)
	if !ok {
		return
	}
	posted, err := s.postClaimEvent(w, r, target.machine, target.token, op.request, event(target))
	if !posted {
		return
	}
	status := http.StatusOK
	if errors.Is(err, supervise.ErrSuspendHeld) {
		status = http.StatusAccepted
	} else if err != nil {
		s.refuseClaimOperation(w, target, err, op)
		return
	}
	s.answerClaimOperation(w, target, op.did, status)
}

// refuseClaimOperation answers an event of the caller's that the claim's machine refused: a
// suspension the caller may not get past in that hold's own code (refuseHold), a claim stored
// working that the operation does not reach as a claim in a turn (claimOp.refusesATurn,
// refuseClaimInATurn), and every other refusal as the operator's door answers it, in the
// machine's own words (operatorFailure).
func (s *server) refuseClaimOperation(w http.ResponseWriter, target claimTarget, err error, op claimOp) {
	var refused *supervise.RefusedError
	switch {
	case holdRefusal(err):
		s.refuseHold(w, target.token, op, target.by, err, nothingChanged)
	case op.refusesATurn && errors.As(err, &refused) && refused.State == supervise.StateWorking:
		refuseClaimInATurn(w, target.token, op)
	default:
		s.operatorFailure(w, op.request, target.token, err)
	}
}

// holdRefusal is whether err is the machine's refusal of a request a suspension stands in the way
// of, which refuseHold answers: one above the caller (supervise.Claim.HeldAgainst), or one the
// daemon placed under an architect's resume, suspend, stop or forget
// (supervise.Claim.ResumeRefusal, supervise.Claim.StopOrSuspendRefusal).
func holdRefusal(err error) bool {
	return errors.Is(err, supervise.ErrOperatorHolds) || errors.Is(err, supervise.ErrControllerHolds) ||
		errors.Is(err, supervise.ErrNotTheArchitectsSuspension)
}

// refuseHold is the refusal of a request of the controller's or an architect's (by) that a
// suspension it may not get past stands in the way of, err being the machine's word for which
// (holdRefusal): the operator's own (suspendedByOperator), the controller's for an architect
// (suspendedByController), and one the daemon placed for an architect's resume, suspend, stop or
// fresh session (suspendedByDaemon). Each sentence names who lifts the suspension, and outcome
// says what of the claim changed (nothingChanged, itsSuspensionTakenOver). An error it does not
// know is no hold it can name: it is logged and answered 500 in its own words, as the daemon's
// own failure, rather than given the words of a hold it may not be.
func (s *server) refuseHold(w http.ResponseWriter, token claim.Token, op claimOp, by supervise.SuspendSource, err error,
	outcome string) {
	switch {
	case errors.Is(err, supervise.ErrOperatorHolds):
		writeFailure(w, http.StatusConflict, suspendedByOperator, fmt.Sprintf(
			"%s is held by the operator's own suspension, which only the operator lifts, so %s by %s %s. Ask the operator: `legion claims resume --claim %s` is theirs to run",
			token, op.name, callerName(by), outcome, token))
	case errors.Is(err, supervise.ErrControllerHolds):
		writeFailure(w, http.StatusConflict, suspendedByController, fmt.Sprintf(
			"%s is held by the controller's suspension, which only the controller or the operator lifts, so %s by %s %s. Ask the controller: its `resume` of the claim is its own to run",
			token, op.name, callerName(by), outcome))
	case errors.Is(err, supervise.ErrNotTheArchitectsSuspension):
		writeFailure(w, http.StatusConflict, suspendedByDaemon, fmt.Sprintf(
			"%s is suspended by the daemon, not by an architect, and %s by %s does not reach the daemon's suspension, so it %s. The daemon's own rules start such a claim again — its issue's phase, or its role's next wake — and so do the controller's resume and the operator's",
			token, op.name, callerName(by), outcome))
	default:
		s.logFailure("api: a refusal of a hold that no answer of the claim door decides", "claim", token, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorBody(fmt.Sprintf(
			"%s by %s of %s met a refusal this daemon has no answer for: %v", op.name, callerName(by), token, err)))
	}
}

// refuseClaimInATurn is the retry's and the resume's refusal of a claim stored working
// (claimInATurn): neither reaches a claim in a turn, the machine refuses each for a claim that
// has not failed or is not suspended, and the code is the one both recoveries give a claim stored
// working, so the caller reads one code for it whichever operation it asked. A claim stored
// working that a suspension above the caller holds, held for its turn, is no exception: the
// door refuses that claim in the hold's code before any operation reads its state, so it too
// takes one code whichever operation was asked (claimDoor). The sentence names the states the
// operation reaches and sends the caller to the state (askAgainLater).
func refuseClaimInATurn(w http.ResponseWriter, token claim.Token, op claimOp) {
	writeFailure(w, http.StatusConflict, claimInATurn, fmt.Sprintf(
		"%s is stored working, and %s reaches no claim in a turn. %s. %s", token, op.name, op.reaches, askAgainLater))
}

// answerClaimOperation writes the claim as the caller's operation left it
// (ClaimOperationResponse) under status, and logs what the caller did to which claim, as the
// caller named it.
func (s *server) answerClaimOperation(w http.ResponseWriter, target claimTarget, did string, status int) {
	view := target.machine.View().Claim
	answer := ClaimOperationResponse{
		State:            string(view.State),
		SuspensionHeld:   view.SuspensionHeld,
		SuspensionHeldBy: string(view.SuspensionHeldBy),
	}
	if view.State == supervise.StateSuspended {
		answer.SuspendedBy = string(view.SuspendedBy)
	}
	s.log.Info("api: "+callerName(target.by)+" "+did+" a claim", "claim", target.token, "issue", target.issue, "role", target.role,
		"state", answer.State, "suspendedBy", answer.SuspendedBy, "suspensionHeldBy", answer.SuspensionHeldBy, "status", status)
	writeJSON(w, status, answer)
}
