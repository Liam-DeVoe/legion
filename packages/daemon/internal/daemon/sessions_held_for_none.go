package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// suspendSessionsHeldForNone runs once at boot, before any claim is relaunched: it logs at Error
// each name of a session (supervise.Hold) the claim_sessions migration found recorded by more than
// one claim, naming every claim whose row records it, and suspends each of those claims this daemon
// supervises through the operator's suspend, once whatever number of names it shares
// (dispatch://INFRA-840, plan E's t-e-54). Each name is held to the first claim that recorded it
// (supervise's holdRecorded), and for one of these nothing recorded which that was: two claims
// holding one name is a role that took another's session, or a bug, so the store holds it for
// neither and refuses every claim its registration, its resume and its enrollment under it. A
// relaunch of such a claim would be refused before it starts, again and again until its launch
// budget ran out, so the claim is suspended instead, by the operator's source so that no rule of
// the daemon's resumes it. An operator records the rightful holder (the line gives the statement)
// and resumes that claim with the operator's resume.
//
// A claim in no state a suspension reaches (queued, launch_uncertain, failed or retired) is named
// and left as it is: a retired claim launches nothing, and a queued, uncertain or failed one that
// launches again is refused each time and fails at its launch budget. A store that refuses the read
// or a suspension refuses the boot, naming what failed, as the controller's boot stop does
// (stopLaunchedController): the claims it names would otherwise be relaunched into that refusal. A
// suspension the claim holds for later is no failure: a working claim's is held for its turn and
// stops the process at the operator's stop timeout (holdSuspension), and a launching claim's is held
// until its agent is ready, which an agent refused its registration never is, so its registration
// deadline suspends it (registrationDeadline). Neither is relaunched meanwhile, and a restart before
// either completes asks again.
//
// The settle statement is a statement an operator runs, and a session's names are the agent's own
// text, so each is written into it as a SQL literal (sqlLiteral), never as it came.
func (s *supervision) suspendSessionsHeldForNone(ctx context.Context) error {
	held, err := s.supervisor.store.SessionsHeldForNone(ctx, s.plan.project)
	if err != nil {
		return fmt.Errorf("read the session names held for no claim: %w", err)
	}
	suspended := map[claim.Token]bool{}
	for _, h := range held {
		s.log.Error("supervise: a session name recorded by more than one claim is held for none of them; each of them this daemon supervises is suspended until an operator records the rightful holder and resumes it",
			"kind", h.Hold.Kind, "name", h.Hold.Name, "claims", h.Claims,
			"settle", "update claim_sessions set claim = '<token>' where project = "+sqlLiteral(s.plan.project)+
				" and kind = "+sqlLiteral(string(h.Hold.Kind))+" and name = "+sqlLiteral(h.Hold.Name))
		for _, token := range h.Claims {
			m, ok := s.supervisor.Machine(token)
			if !ok || suspended[token] {
				continue
			}
			suspended[token] = true
			switch state := m.Claim().State; state {
			case supervise.StateQueued, supervise.StateLaunchUncertain, supervise.StateFailed, supervise.StateRetired:
				s.log.Warn("supervise: a claim whose session is held for no claim is in no state a suspension reaches; it is left as it is",
					"claim", token, "kind", h.Hold.Kind, "name", h.Hold.Name, "state", state)
				continue
			}
			err := m.Handle(ctx, supervise.RequestSuspend{
				Claim: token, Reason: fmt.Sprintf("session held twice: %s %s", h.Hold.Kind, h.Hold.Name), By: supervise.SuspendedByOperator,
			})
			if errors.Is(err, supervise.ErrSuspendHeld) {
				s.log.Warn("supervise: the suspension of a claim whose session is held for no claim waits for its turn or its launch",
					"claim", token, "kind", h.Hold.Kind, "name", h.Hold.Name)
				continue
			}
			if err != nil {
				return fmt.Errorf("suspend %s, whose session's %s %s is held for no claim: %w", token, h.Hold.Kind, h.Hold.Name, err)
			}
		}
	}
	return nil
}

// sqlLiteral is s as a PostgreSQL string literal under standard_conforming_strings, the default
// since PostgreSQL 9.1: quoted, with each quote doubled, so no text of s ends the literal.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
