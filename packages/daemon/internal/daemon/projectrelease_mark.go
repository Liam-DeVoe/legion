package daemon

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// projectRelease is the daemon-wide mark of a project release (dispatch://INFRA-3583 R7): while
// it is held, the daemon starts nothing of its project until its process ends. Every layer that
// could make a pod or start or prompt a role reads it:
//
//   - the launch check every machine launch makes, the controller's included (launchGatedStore);
//   - the Sandbox runtime itself (sandbox.Options.ProjectReleaseHolds), which creates no Sandbox,
//     patches none to Running, makes no repository cache or probe and replaces no pod;
//   - the machines, which hand no claim its task (supervise.Deps.ProjectReleaseHolds);
//   - the daemon's periodic work: the orphan sweep and its cache sweep, the pod update sweep, the
//     stale-plugin refresh and the unfinished-launch relaunch (supervision.sweepOnce,
//     relaunchUnfinished), the idle and unowned ticks (workflowRuntime.idlePass), the controller
//     keeper (controllerKeeper.keep) and the wakes (workflowRuntime.wakePass);
//   - the outbox, which answers a wait for every row that would start or prompt a role
//     (record.HeldByProjectRelease);
//   - the operator's routes that start or prompt a role (api.Options.ProjectReleaseHolds).
//
// The release sets it before its record commits (store.StartProjectRelease) and before that
// transaction reads the claims it stops, and every role start reads it once more after its
// claim's `launching` write, just before the launcher starts the role (the Sandbox runtime's
// launch), so a launch either sees the mark or is among the claims the release stops. It clears
// the mark only when that commit fails; otherwise only the process's end clears it, and the next
// boot, which ends the record, starts the claims the release stopped. The zero value holds
// nothing, and so does a nil mark: a test that builds a part of the daemon by hand reads every
// start as free.
type projectRelease struct {
	marked atomic.Bool
	// id is the release's record id once its record has committed (recorded), 0 before: what the
	// release's own ask round is named by (ownRound).
	id atomic.Int64
	// finishedActs is set once the release finished its acts (finish, finished): its round is then
	// no round the outbox publishes an ask under without the claim-round read (askRound), while
	// the mark itself stays held.
	finishedActs atomic.Bool
	// unmarked runs once unmark has cleared the mark: the machines' send of every task the mark
	// held back (supervisor.sendHeldTasks). openSupervision sets it once, at boot, before anything
	// can set the mark; nil runs nothing.
	unmarked func()
}

// mark sets the mark and reports whether this call set it: a process runs one release, so a mark
// already held is a release already running or finished here, and the caller starts no second
// one.
func (r *projectRelease) mark() bool {
	return r.marked.CompareAndSwap(false, true)
}

// recorded names the release once StartProjectRelease committed its record, so the outbox lets
// that release's own asks through (ownRound, askRound).
func (r *projectRelease) recorded(id int64) {
	r.id.Store(id)
}

// finish records that the release finished its acts: from then on an ask of its round is
// published only where the claim records that round, which no claim does (askRound), so none is
// published. The mark stays held until the process ends.
func (r *projectRelease) finish() {
	r.finishedActs.Store(true)
}

// finished is whether the release of this process has finished its acts: the one flag that says
// so, which the release itself reads for the acts it no longer does (projectReleaser.current,
// record, the authorize of a pod's delete). A nil mark has finished nothing.
func (r *projectRelease) finished() bool {
	return r != nil && r.finishedActs.Load()
}

// unmark clears the mark of a release whose record did not commit, the one way the mark is
// cleared before the process ends: nothing of that release ran, so nothing of the project is
// held for it. A task a machine held back while the mark stood is sent at once (unmarked) rather
// than at the claim's next event; a held outbox row runs at its next read, within
// releaseHoldSpacing, a launch the launch check refused at its probe-interval retry, and the
// periodic work at its next tick.
func (r *projectRelease) unmark() {
	r.id.Store(0)
	r.marked.Store(false)
	if r.unmarked != nil {
		r.unmarked()
	}
}

// held is whether the project release holds every start now. A nil mark holds nothing.
func (r *projectRelease) held() bool {
	return r != nil && r.marked.Load()
}

// ownRound is the round the release of this process asks its roles under (projectReleaseRound),
// running or finished, "" while no release is marked or its record has not committed yet: the
// one round of update asks the outbox runs while the mark is held (record.HeldByProjectRelease).
func (r *projectRelease) ownRound() string {
	if !r.held() {
		return ""
	}
	id := r.id.Load()
	if id == 0 {
		return ""
	}
	return projectReleaseRound(id)
}

// askRound is ownRound while the release runs, and "" once it finished (finish): the round whose
// asks the outbox publishes without the claim-round read, since only a running release holds the
// answers to them (projectReleaser.AskRound, which answers the same). An ask of the release's round
// that runs after its finish meets the claim-round read, and since no claim records that round it
// finishes without a publish.
func (r *projectRelease) askRound() string {
	if r.finished() {
		return ""
	}
	return r.ownRound()
}

// projectReleaseRound is the round a project release's ask rides (record.UpdateAsk.Round), named
// by the release's record id so it is never the round of a pod update.
func projectReleaseRound(id int64) string {
	return "release:" + strconv.FormatInt(id, 10)
}

// errProjectReleased is the refusal of a start the project release holds: the launch check's, for
// every claim the controller's included (launchGatedStore), and the outbox's wait for a row that
// would start or prompt a role. It is a wait (wait.ErrWaiting), since the start is due again once
// the daemon's next boot has ended the release: a machine whose relaunch it refuses keeps the
// claim waiting uncharged (supervise's relaunchRefused), and a row it holds is put back unrun,
// with no attempt counted, and read again every releaseHoldSpacing (outbox.holdForRelease). The
// wait is routine (routineWaitError): the operator who released the project knows what it holds,
// so a row held for the whole bound stays at debug after its first line.
var errProjectReleased error = routineWaitError{err: wait.New(api.ProjectReleaseHeldSentence)}

// releaseStoppedClaims is the claims this process's boot read stored suspended by a project
// release (supervise.SuspendedByProjectRelease) that have not reported ready, working or failed
// since. The boot starts such a claim again through an outbox start, which the wake pacing lets
// out in its turn (workflow's booted), and between that start and its session's ready the claim
// reads as any claim coming up does. So the set is what tells it from a claim no release
// stopped: a delivery waits for the first (outbox.awaitsBootStart), and one for the second is
// ended when nobody holds its role. attach builds the set from the claims supervision read at
// the boot, and the daemon's durable-state callback takes a claim out
// (workflowRuntime.claimDurableState). A nil set holds no claim: an outbox or a runtime a test
// built by hand reads every claim as one no release stopped.
type releaseStoppedClaims struct {
	mu     sync.Mutex
	tokens map[claim.Token]struct{}
}

// newReleaseStoppedClaims is the set over claims, the boot's read of the store: each claim that
// reads stopped by the project release (supervise.Claim.ProjectReleased).
func newReleaseStoppedClaims(claims []supervise.Claim) *releaseStoppedClaims {
	stopped := &releaseStoppedClaims{tokens: map[claim.Token]struct{}{}}
	for _, c := range claims {
		if c.ProjectReleased() {
			stopped.tokens[c.Token] = struct{}{}
		}
	}
	return stopped
}

// comingBack is whether held's claim is in the set and on its way up: started, with a session
// that does not hold its role yet. Such a claim reads launching, shim_connected or registered.
func (s *releaseStoppedClaims) comingBack(held supervise.ClaimView) bool {
	if s == nil {
		return false
	}
	switch held.State {
	case supervise.StateLaunching, supervise.StateShimConnected, supervise.StateRegistered:
	default:
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, stopped := s.tokens[held.Token]
	return stopped
}

// settled takes token's claim out of the set: its session holds its role again, or the claim
// failed, and either way no delivery waits for the boot's start of it any longer.
func (s *releaseStoppedClaims) settled(token claim.Token) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}
