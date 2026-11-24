package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// suspendToLeave makes machine's claim run nothing where payload, a start, leaves the session the
// claim records (record.SuperviseRequest.LeaveSession) and the claim still runs a process: that
// process runs the session of the key whose name the start's agent took, which the agent must not
// resume (workflow's leaveKeySessions). It asks the claim to suspend, as the daemon and keeping its
// session, and is a wait (wait.ErrWaiting) until the claim runs nothing: a claim in a turn holds
// the suspension for the turn's end and a launching one until its agent is ready (supervise's
// suspend rows), as reroleForStep's suspension is held. The start then brings the claim back, and
// the decision that does leaves the session (supervise's revive). A start that leaves no session,
// or one the claim no longer records, and a claim that already runs nothing, ask nothing.
//
// A start of a step's entry asks the suspension with the step's standing, as reroleForStep does
// (supervise.Standing, stepStands): a step the issue left before the machine's lock is
// supervise.ErrStepLeft, with nothing of the claim changed.
func (r *outbox) suspendToLeave(ctx context.Context, machine *supervise.Machine, token claim.Token, row record.OutboxRow, payload record.SuperviseRequest) error {
	held := machine.Claim()
	if payload.LeaveSession == "" || held.Session != payload.LeaveSession || held.RunsNothing() {
		return nil
	}
	if payload.Entry > 0 && payload.Phase != "" {
		ctx = supervise.Standing(ctx, func(ctx context.Context) (bool, error) { return r.stepStands(ctx, row, payload) })
	}
	err := machine.Handle(ctx, supervise.RequestSuspend{Claim: token, By: supervise.SuspendedByDaemon,
		Reason: fmt.Sprintf("its agent's step on %s starts in a session of its own, which leaves session %s", row.Issue, payload.LeaveSession)})
	var refused *supervise.RefusedError
	if err != nil && !errors.Is(err, supervise.ErrSuspendHeld) && !errors.As(err, &refused) {
		return fmt.Errorf("suspend claim %s to leave session %s: %w", token, payload.LeaveSession, err)
	}
	if held = machine.Claim(); !held.RunsNothing() {
		return wait.Errorf("the %s step of %s waits for its claim %s to end its turn in session %s, which it leaves", payload.Role, row.Issue, token, payload.LeaveSession)
	}
	return nil
}
