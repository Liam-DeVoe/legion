package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// suspendSessionConflicts runs once at boot, before any claim is relaunched (dispatch://INFRA-840,
// plan E's t-e-54). Each name of a session (supervise.Hold) is held to the first claim of the
// project that recorded it (supervise's holdRecorded), and a claim records a name without holding it
// only where a daemon from before the hold ran on this database: before the claim_sessions
// migration, or rolled back to an earlier release after it, which applies only the migrations it
// has and never writes the table. So the boot first holds every recorded name no row holds yet
// (store.HoldRecordedSessions, the migration's backfill again), and then logs at Error, and suspends,
// every claim recording a name it does not hold (store.SessionConflicts): one held for no claim,
// which several claims recorded and nothing says which first, and one held for another claim, which
// a rolled-back daemon let this claim take. Each such claim is suspended once, whatever number of
// names it shares, by the operator's source so that no rule of the daemon's resumes it; relaunched,
// it would be refused before it starts, again and again until its launch budget ran out, and a
// claim that took another's name in the window would keep the grants it adopted then. An operator
// records the rightful holder (the line gives the statement) and resumes that claim with the
// operator's resume. On a database only daemons with the hold have run, both steps find nothing.
//
// A claim in no state a suspension reaches (queued, launch_uncertain, failed or retired) is named
// and left as it is: a retired claim launches nothing, and a queued, uncertain or failed one that
// launches again is refused each time and fails at its launch budget. A store that refuses a read,
// a write or a suspension refuses the boot, naming what failed, as the controller's boot stop does
// (stopLaunchedController): the claims it names would otherwise be relaunched into that refusal. A
// suspension the claim holds for later is no failure: a working claim's is held for its turn and
// stops the process at the operator's stop timeout (holdSuspension), and a launching claim's is held
// until its agent is ready, which an agent refused its registration never is, so its registration
// deadline suspends it (registrationDeadline). Neither is relaunched meanwhile, and a restart before
// either completes asks again.
//
// The settle statement is a statement an operator runs, and a session's names are the agent's own
// text, so each is written into it as a SQL literal (sqlLiteral), never as it came.
func (s *supervision) suspendSessionConflicts(ctx context.Context) error {
	held, err := s.supervisor.store.HoldRecordedSessions(ctx, s.plan.project)
	if err != nil {
		return err
	}
	if held > 0 {
		s.log.Warn("supervise: held session names claims recorded while a daemon before the hold ran on this database",
			"names", held)
	}
	conflicts, err := s.supervisor.store.SessionConflicts(ctx, s.plan.project)
	if err != nil {
		return err
	}
	suspended := map[claim.Token]bool{}
	for _, c := range conflicts {
		holder := string(c.Holder)
		if holder == "" {
			holder = "none"
		}
		s.log.Error("supervise: a session name is recorded by claims that do not hold it; each of them this daemon supervises is suspended until an operator records the rightful holder and resumes it",
			"kind", c.Hold.Kind, "name", c.Hold.Name, "holder", holder, "claims", c.Claims,
			"settle", "update claim_sessions set claim = '<token>' where project = "+sqlLiteral(s.plan.project)+
				" and kind = "+sqlLiteral(string(c.Hold.Kind))+" and name = "+sqlLiteral(c.Hold.Name))
		for _, token := range c.Claims {
			m, ok := s.supervisor.Machine(token)
			if !ok || suspended[token] {
				continue
			}
			suspended[token] = true
			switch state := m.Claim().State; state {
			case supervise.StateQueued, supervise.StateLaunchUncertain, supervise.StateFailed, supervise.StateRetired:
				s.log.Warn("supervise: a claim recording a session name it does not hold is in no state a suspension reaches; it is left as it is",
					"claim", token, "kind", c.Hold.Kind, "name", c.Hold.Name, "state", state)
				continue
			}
			err := m.Handle(ctx, supervise.RequestSuspend{
				Claim: token, Reason: fmt.Sprintf("session name not its own: %s %s", c.Hold.Kind, c.Hold.Name), By: supervise.SuspendedByOperator,
			})
			if errors.Is(err, supervise.ErrSuspendHeld) {
				s.log.Warn("supervise: the suspension of a claim recording a session name it does not hold waits for its turn or its launch",
					"claim", token, "kind", c.Hold.Kind, "name", c.Hold.Name)
				continue
			}
			if err != nil {
				return fmt.Errorf("suspend %s, which records the %s %s it does not hold: %w", token, c.Hold.Kind, c.Hold.Name, err)
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
