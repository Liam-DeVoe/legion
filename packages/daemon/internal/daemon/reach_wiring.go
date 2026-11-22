package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fleetalert"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
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

// reachNotify is the workflow's write the reach keeper tells through: an architect's notice with
// its mark (enqueueReachNotice). It is nil in a deployment with no workflow, which tells nobody.
type reachNotify struct {
	enqueue architectEnqueue
}

// architectEnqueue writes an architect's notice row on an issue and its marks in one transaction
// (enqueueReachNotice).
type architectEnqueue func(ctx context.Context, issue string, notice record.Notice, marks []reachMark) error

// reachNotify is w's reach write, and none for a deployment with no workflow (w nil), which
// openSupervision is handed before the workflow attaches to it.
func (w *workflowRuntime) reachNotify() reachNotify {
	if w == nil {
		return reachNotify{}
	}
	return reachNotify{enqueue: w.enqueueReachNotice}
}

// enqueueReachNotice writes an architect's notice row of issue, due now, and marks — the reach
// records the notice marks told — in one transaction of its own, which owns the commit hook its
// write registers, as enqueueOutbox's does: the notice and its marks commit together or not at
// all (reachMark).
func (w *workflowRuntime) enqueueReachNotice(ctx context.Context, issue string, notice record.Notice, marks []reachMark) error {
	row, err := record.NewOutboxRow(issue, notice, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("enqueue a %s notice of %s: %w", notice.Kind, issue, err)
	}
	scoped, committed := intake.WithCommitHooks(store.ForConcurrentWorker(ctx))
	if err := pgx.BeginFunc(scoped, w.pool, func(tx pgx.Tx) error {
		if err := w.records.Enqueue(scoped, tx, row); err != nil {
			return fmt.Errorf("enqueue a %s notice of %s: %w", notice.Kind, issue, err)
		}
		return putReachMarks(scoped, tx, marks)
	}); err != nil {
		return err
	}
	committed()
	return nil
}

// putReachMarks writes each of marks, a claim's reach record, through tx. A claim the store no
// longer holds is marked nothing, and its notice is written all the same.
func putReachMarks(ctx context.Context, tx pgx.Tx, marks []reachMark) error {
	for _, mark := range marks {
		if _, err := store.PutClaimReachTx(ctx, tx, mark.token, mark.reach); err != nil {
			return err
		}
	}
	return nil
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
