package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fleetalert"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/stream"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// listSessions is the keeper's read of Envoy's list over the listener the daemon's notices go
// through, and nil where no listener is configured, which leaves the keeper reading nothing.
func listSessions(envoyURL, token string) func(context.Context) ([]notify.ListedSession, error) {
	if envoyURL == "" {
		return nil
	}
	return notify.New(envoyURL, token).ListSessions
}

// operatorHolds is whether an operator's suspension holds token's claim, as its machine last
// published it (supervise.Claim.RuleHolds): a suspension held for the claim's turn is the machine's
// memory alone, which the store's copy the sweep reads never carries. It reads the published view,
// which never waits on a decision in flight. A claim this supervisor holds no machine of is held by
// no operator here.
func (s *supervision) operatorHolds(token claim.Token) bool {
	m, ok := s.supervisor.Machine(token)
	return ok && m.View().Claim.RuleHolds()
}

// closeEndedLockOuts closes R6's line of each lock-out the worker stream's lockedOut no longer
// holds (R3): a line the last daemon opened names a connection that ended with it, and this
// daemon's stream tells that older process to stop at its hello rather than binding it. The stream
// closes the line of a lock-out it saw end itself.
func (k *reachKeeper) closeEndedLockOuts(lockedOut func(claim.Token) (stream.LockOut, bool)) {
	k.alerts.CloseEnded(fleetalert.NewerProcessRefused, func(token string) bool {
		_, locked := lockedOut(claim.Token(token))
		return locked
	})
}

// relaunchByDaemon is the daemon's relaunch of a marked claim at at (reachKeeper's relaunch):
// RequestRelaunch to the claim's machine, with the step the store holds as the guard's freshest
// durable reading. The controller's relaunch may replace its pod, since no other role runs there;
// a workflow claim's may not, and waits while the project is paused, which starts no turn of a
// role but the controller's (errRelaunchWaitsForResume). It answers the claim as the relaunch left
// it, which records the relaunch (supervise.Claim.LastRelaunch).
func (s *supervision) relaunchByDaemon(ctx context.Context, c supervise.Claim, at time.Time) (supervise.Claim, error) {
	controller := c.Role == claim.RoleController
	if !controller && s.supervisor.deps.Paused != nil {
		if _, paused := s.supervisor.deps.Paused(); paused {
			return c, errRelaunchWaitsForResume
		}
	}
	m, ok := s.supervisor.Machine(c.Token)
	if !ok {
		return supervise.Claim{}, fmt.Errorf("the claim %s is not supervised", c.Token)
	}
	step, err := s.reach.stepOf(ctx, c)
	if err != nil {
		return supervise.Claim{}, err
	}
	err = m.Handle(store.ForConcurrentWorker(ctx), supervise.RequestRelaunch{Claim: c.Token, Step: step, Cause: relaunchedByDaemon, MayReplacePod: controller, At: at})
	return m.Claim(), err
}

// markControllerLocator puts the mark of the project controller's claim, where the daemon holds
// one and has marked it out of reach, on the controller's locator of the state: read_state names an
// unreachable controller there (R5), since the controller stands on no issue whose claim view would
// carry it. A nil locator, no controller registered, carries nothing.
func markControllerLocator(locator *api.ControllerLocator, project string, claims []supervise.Claim) {
	if locator == nil {
		return
	}
	token := claim.ControllerToken(project)
	for _, c := range claims {
		if c.Token == token {
			locator.Unreachable = api.UnreachableViewOf(c.Reach)
			return
		}
	}
}
