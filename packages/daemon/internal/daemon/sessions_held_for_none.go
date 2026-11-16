package daemon

import (
	"context"
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// suspendSessionsHeldForNone runs once at boot, before any claim is relaunched: it logs at Error
// each session id the claim_sessions migration found recorded by more than one claim, naming every
// claim whose row records it, and suspends each of those claims this daemon supervises through the
// operator's suspend (dispatch://INFRA-840, plan E's t-e-54). Each id is held to the first claim
// that recorded it (supervise's holdSession), and for one of these nothing recorded which that
// was: two claims holding one id is a role that took another's session, or a bug, so the store
// holds it for neither and refuses every claim its registration and its enrollment. A relaunch of
// such a claim would resume that id, be refused 409, exit and be relaunched again, so the claim is
// suspended instead, by the operator's source so that no rule of the daemon's resumes it. An
// operator records the rightful holder (the line gives the statement) and resumes that claim with
// the operator's resume.
//
// A claim in no state a suspension reaches (queued, launch_uncertain, failed or retired) is named
// and left as it is: a retired claim launches nothing, and a queued, uncertain or failed one that
// launches again is refused its registration each time and fails at its launch budget. A store
// that refuses the read or a suspension refuses the boot, naming what failed, as the controller's
// boot stop does (stopLaunchedController): the claims it names would otherwise be relaunched into
// that refusal.
func (s *supervision) suspendSessionsHeldForNone(ctx context.Context) error {
	held, err := s.supervisor.store.SessionsHeldForNone(ctx)
	if err != nil {
		return fmt.Errorf("read the session ids held for no claim: %w", err)
	}
	for _, h := range held {
		s.log.Error("supervise: a session id recorded by more than one claim is held for none of them; each of them this daemon supervises is suspended until an operator records the rightful holder and resumes it",
			"session", h.Session, "claims", h.Claims,
			"settle", fmt.Sprintf("update claim_sessions set claim = '<token>' where session = '%s'", h.Session))
		for _, token := range h.Claims {
			m, ok := s.supervisor.Machine(token)
			if !ok {
				continue
			}
			switch state := m.Claim().State; state {
			case supervise.StateQueued, supervise.StateLaunchUncertain, supervise.StateFailed, supervise.StateRetired:
				s.log.Warn("supervise: a claim whose session is held for no claim is in no state a suspension reaches; it is left as it is",
					"claim", token, "session", h.Session, "state", state)
				continue
			}
			if err := m.Handle(ctx, supervise.RequestSuspend{
				Claim: token, Reason: "session held twice: " + h.Session, By: supervise.SuspendedByOperator,
			}); err != nil {
				return fmt.Errorf("suspend %s, whose session %s is held for no claim: %w", token, h.Session, err)
			}
		}
	}
	return nil
}
