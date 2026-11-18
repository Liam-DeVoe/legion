package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/stream"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// reachNoticeBudget bounds the write of one notice the keeper or the worker stream's lock-out
// report makes outside a sweep's own context.
const reachNoticeBudget = 30 * time.Second

// listSessions is the keeper's read of Envoy's list over the listener the daemon's notices go
// through, and nil where no listener is configured, which leaves the keeper reading nothing.
func listSessions(envoyURL, token string) func(context.Context) ([]notify.ListedSession, error) {
	if envoyURL == "" {
		return nil
	}
	return notify.New(envoyURL, token).ListSessions
}

// tellControllerQueued queues one controller notice in a transaction of its own and answers
// whether it did (admit.Admission.TellControllerQueued), writing each of marks — the reach
// records the notice marks told — in that same transaction where it queued it, so the notice and
// its marks commit together or not at all (reachMark): the keeper's notices and the worker
// stream's lock-out report are made outside any fact, as the provider-errors notice is
// (providerErrorRunReached), and own the commit hook their write registers, so the controller's
// lane is woken once the row commits.
func (w *workflowRuntime) tellControllerQueued(ctx context.Context, notice record.ControllerNotice, issue string, marks []reachMark) (bool, error) {
	if w.admission == nil {
		return false, nil
	}
	scoped, committed := intake.WithCommitHooks(store.ForConcurrentWorker(ctx))
	var told bool
	if err := pgx.BeginFunc(scoped, w.pool, func(tx pgx.Tx) error {
		var err error
		if told, err = w.admission.TellControllerQueued(scoped, tx, notice, issue); err != nil || !told {
			return err
		}
		return putReachMarks(scoped, tx, marks)
	}); err != nil {
		return false, err
	}
	committed()
	return told, nil
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

// putReachMarks writes each of marks through tx: a claim's reach record, or the project's own
// record of a notice that speaks for no claim's. A claim the store no longer holds is marked
// nothing, and its notice is written all the same.
func putReachMarks(ctx context.Context, tx pgx.Tx, marks []reachMark) error {
	for _, mark := range marks {
		if mark.project != nil {
			if err := store.PutReachProjectNoticeTx(ctx, tx, *mark.project); err != nil {
				return err
			}
			continue
		}
		if _, err := store.PutClaimReachTx(ctx, tx, mark.token, mark.reach); err != nil {
			return err
		}
	}
	return nil
}

// reportLockOut tells the project's controller of a claim whose newer process the worker stream
// has refused for stream.LockOutReportAfter because an older generation's connection will not close
// (R3, stream.Options.ReportLockOut): only a person can end that process. The stream calls it once
// per lock-out, on a goroutine of its own, so the notice is written here with a budget of its own.
// It is told once whatever the daemon's restarts, and one the controller's lane did not queue is
// kept and told again at each sweep for as long as the stream still refuses that newer process
// (lockOutNotices). A claim the daemon does not supervise, and a deployment with no workflow,
// tell nobody, and the stream's own R6 line stands for it.
func (s *supervision) reportLockOut(out stream.LockOut) {
	m, ok := s.supervisor.Machine(out.Claim)
	if !ok {
		s.log.Warn("worker stream: a lock-out of a claim this daemon does not supervise tells nobody", "claim", out.Claim)
		return
	}
	c := m.Claim()
	ctx, cancel := context.WithTimeout(context.Background(), reachNoticeBudget)
	defer cancel()
	notice := record.ControllerNotice{Kind: record.WorkerLockedOutNotice, Role: c.Role, Claim: string(out.Claim), Reason: out.Reason()}
	s.lockOuts.report(ctx, out, notice, c.Issue)
}

// relaunchOwnController is the daemon's relaunch of its own controller at at (reachKeeper's
// relaunchController): RequestRelaunch to the controller claim's machine, which may replace the
// controller's pod, with the step the store holds as the guard's freshest durable reading. It
// answers the claim as the relaunch left it, which records the relaunch
// (supervise.Reach.LastRelaunch).
func (s *supervision) relaunchOwnController(ctx context.Context, c supervise.Claim, at time.Time) (supervise.Claim, error) {
	m, ok := s.supervisor.Machine(c.Token)
	if !ok {
		return supervise.Claim{}, fmt.Errorf("the controller's claim %s is not supervised", c.Token)
	}
	step, err := s.reach.stepOf(ctx, c)
	if err != nil {
		return supervise.Claim{}, err
	}
	err = m.Handle(store.ForConcurrentWorker(ctx), supervise.RequestRelaunch{Claim: c.Token, Step: step, Cause: relaunchedByDaemon, MayReplacePod: true, At: at})
	return m.Claim(), err
}

// relaunch is the controller's `relaunch` of an unreachable claim (api.Options.Relaunch): the
// three things only the keeper knows are checked first — the claim is marked out of reach, its
// last relaunch is at least 6 hours old, and its pod holds a claim Envoy can still reach — and
// then the machine is asked, which checks the guard again under its lock and asks the runtime
// before it stops anything (supervise.RequestRelaunch). The last relaunch is the one the claim
// itself records (supervise.Reach.LastRelaunch), which the relaunch's own launch wrote, so a
// relaunch whose keeper never wrote its record — a daemon that ended first, a store that refused
// the write — is still refused a second time within the 6 hours.
//
// A guard's refusal and a relaunch that reached the claim are recorded for the notices that
// follow (reachKeeper.guardRefused, reachKeeper.relaunched). A refusal the store would not record
// is answered as a failure of its own (supervise.ErrReachUnwritten): the notice that the guard
// would now let the claim through counts from it, so the controller must not wait for that
// notice. A relaunch's record the store would not write is logged, and the relaunch is answered
// as made, since the launch has recorded it already.
//
// The pod check reads every claim the daemon holds: a relaunch into a pod that reaches Envoy for
// none of its two or more live claims fails each worker at its deaths limit and recovers none, so
// that pod is a case for a person.
func (s *supervision) relaunch(ctx context.Context, m *supervise.Machine) error {
	if s.reach == nil {
		return supervise.ErrClaimReachable
	}
	c := m.Claim()
	now := s.reach.now()
	s.reach.mu.Lock()
	rec := s.reach.recordOf(c)
	s.reach.mu.Unlock()
	last := latest(rec.RelaunchedAt, c.Reach.LastRelaunch)
	switch {
	case !rec.Unreachable():
		return supervise.ErrClaimReachable
	case !last.IsZero() && now.Sub(last) < supervise.RelaunchAgainAfter:
		return fmt.Errorf("%w: at %s", supervise.ErrRelaunchedRecently, stamp(last))
	}
	if err := s.podReachesNone(c); err != nil {
		return err
	}
	step, err := s.reach.stepOf(ctx, c)
	if err != nil {
		return err
	}
	err = m.Handle(ctx, supervise.RequestRelaunch{Claim: c.Token, Step: step, Cause: relaunchedByController, At: now})
	switch {
	case errors.Is(err, supervise.ErrRelaunchGuard):
		if unwritten := s.reach.guardRefused(ctx, c.Token, now); unwritten != nil {
			return fmt.Errorf("%w; %w, so no notice will say when the guard would let it through: %w", err, supervise.ErrReachUnwritten, unwritten)
		}
	case err == nil:
		if unwritten := s.reach.relaunched(ctx, m.Claim()); unwritten != nil && ctx.Err() == nil {
			s.log.Error("reach: the keeper's record of a relaunch was not written; its launch recorded the relaunch, and the next sweep writes the record again",
				"claim", c.Token, "error", unwritten)
		}
	}
	return err
}

// podReachesNone refuses the relaunch of c when every live claim of the pod c runs in, two or
// more, is marked out of reach. A claim that runs in no pod (tmux) shares nothing to refuse for.
// The claims are read as their machines last published them, which is where the daemon holds
// each claim's process now.
func (s *supervision) podReachesNone(c supervise.Claim) error {
	if c.Locator == nil || c.Locator.Sandbox == nil {
		return nil
	}
	pod := c.Locator.Sandbox.PodUID
	var siblings []claim.Token
	s.reach.mu.Lock()
	defer s.reach.mu.Unlock()
	for _, m := range s.supervisor.supervised() {
		other := m.View().Claim
		if other.Locator == nil || other.Locator.Sandbox == nil || other.Locator.Sandbox.PodUID != pod || !runsOnSession(other.State) {
			continue
		}
		if !s.reach.recordOf(other).Unreachable() {
			return nil
		}
		siblings = append(siblings, other.Token)
	}
	if len(siblings) < 2 {
		return nil
	}
	slices.Sort(siblings)
	return fmt.Errorf("%w: pod %s holds %d live claims, every one out of reach: %v", supervise.ErrPodUnreachable, pod, len(siblings), siblings)
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
