package daemon

import (
	"strconv"
	"sync/atomic"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// projectRelease is the daemon-wide mark of a project release (dispatch://INFRA-3583 R7): while
// it is held, the daemon starts nothing of its project until its process ends. Every layer that
// could make a pod or start or prompt a role reads it:
//
//   - the launch check every machine launch makes, the controller's included (launchGatedStore);
//   - the Sandbox runtime itself (sandbox.Options.Held), which creates no Sandbox, patches none to
//     Running, makes no repository cache or probe and replaces no pod;
//   - the machines, which hand no claim its task (supervise.Deps.ProjectReleaseHolds);
//   - the daemon's periodic work: the orphan sweep and its cache sweep, the pod update sweep, the
//     stale-plugin refresh and the unfinished-launch relaunch (supervision.sweepOnce,
//     relaunchUnfinished), the idle and unowned ticks (workflowRuntime.idlePass), the controller
//     keeper (controllerKeeper.keep) and the wakes (workflowRuntime.wakePass);
//   - the outbox, which answers a wait for every row that would start or prompt a role
//     (record.HeldByProjectRelease);
//   - the operator's routes that start or prompt a role (api.Options.ProjectReleaseHolds).
//
// The release sets it just before its record commits (store.StartProjectRelease), so no launch
// slips between the record and the mark, and clears it only when that commit fails; otherwise only
// the process's end clears it, and the next boot, which ends the record, starts the claims the
// release stopped. The zero value holds nothing, and so does a nil mark: a test that builds a part
// of the daemon by hand reads every start as free.
type projectRelease struct {
	marked atomic.Bool
	// id is the running release's record id once its record has committed (recorded), 0 before:
	// what the release's own ask round is named by (askRound).
	id atomic.Int64
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

// recorded names the running release once StartProjectRelease committed its record, so the
// outbox lets that release's own asks through (askRound).
func (r *projectRelease) recorded(id int64) {
	r.id.Store(id)
}

// unmark clears the mark of a release whose record did not commit, the one way the mark is
// cleared before the process ends: nothing of that release ran, so nothing of the project is
// held for it. A task a machine held back while the mark stood is sent at once (unmarked) rather
// than at the claim's next event; a held outbox row runs on its backoff, a launch the launch
// check refused at its probe-interval retry, and the periodic work at its next tick.
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

// askRound is the update round the running release asks its roles under (projectReleaseRound),
// "" while no release is marked or its record has not committed yet: the one round of update asks
// the outbox runs while the mark is held.
func (r *projectRelease) askRound() string {
	if !r.held() {
		return ""
	}
	id := r.id.Load()
	if id == 0 {
		return ""
	}
	return projectReleaseRound(id)
}

// projectReleaseRound is the round a project release's ask rides (record.UpdateAsk.Round), named
// by the release's record id so it is never the round of a pod update (decision 6 of
// dispatch://INFRA-3583's plan).
func projectReleaseRound(id int64) string {
	return "release:" + strconv.FormatInt(id, 10)
}

// errProjectReleased is the refusal of a start the project release holds: the launch check's, for
// every claim the controller's included (launchGatedStore), and the outbox's wait for a row that
// would start or prompt a role. It is a wait (wait.ErrWaiting), since the start is due again once
// the daemon's next boot has ended the release: a machine whose relaunch it refuses keeps the
// claim waiting uncharged (supervise's relaunchRefused), and a row it holds runs again on its
// backoff. The wait is routine (routineWait): the operator who released the project knows what it
// holds, so a row held for the whole bound stays at debug after its first line.
var errProjectReleased error = projectReleaseWait{err: wait.New(api.ProjectReleaseHolds)}

// projectReleaseWait is errProjectReleased's type: a wait the outbox logs as routine.
type projectReleaseWait struct{ err error }

func (w projectReleaseWait) Error() string { return w.err.Error() }

func (w projectReleaseWait) Unwrap() error { return w.err }

func (projectReleaseWait) Routine() bool { return true }
