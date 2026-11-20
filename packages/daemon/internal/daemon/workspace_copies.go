package daemon

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// runningCopyPeriod is the most the cadence leaves between the starts of two copies of one
// issue's workspace inside a turn, when the daemon's minute tick (idleTickInterval) arrives on
// time and the project is not paused (runningCopyProjectPaused). It is counted from the start of
// the copy before it, and an issue is due once less than one tick of it is left (due), so the tick
// that starts the next copy comes at most one period after the last copy started, and never less
// than a period less one tick after it. A copy holds the workspace as it was when the copy
// started, so the work an eviction loses is the work since the last copy's start, which is what
// the period bounds; counted from the end of the copy instead, a copy that took two minutes would
// put the next start seven minutes or more after its own. Counting from the start runs no copy
// into the next: a copy's budget ends it by the time the next can fall due (runningCopyBudget),
// and the issue's one place in flight keeps a second copy from starting beside it whatever the
// timing (runningCopies). Five minutes is the bound the change holds itself to, and it is the cost
// as well, since each copy is a `git bundle create` in the workspace a role is working in.
//
// It is a constant and not a `legion.yaml` key on purpose. A deployment that had set a key could
// not go back to an earlier daemon release without editing its file first, and editing the file
// is exactly the one action a deployment takes when this change goes wrong.
const runningCopyPeriod = 5 * time.Minute

// runningCopyBudget is how long one copy is given before the daemon stops waiting for it. It is
// the period less one tick of the daemon's own minute (idleTickInterval), which is how long after
// a copy's start the next copy of its issue can first fall due (due). A copy that spends its whole
// budget has therefore ended, and given the issue's place back, by the first tick that finds the
// issue due, so the next copy starts on that tick and the period's bound holds for a slow
// workspace as for a quick one, with no two copies running at once. A budget of a whole period
// would have such a copy still holding the place at that tick, and its next copy would start a
// whole tick past the bound, since the place keeps the next copy from starting beside it: two runs
// of the shim's bundler in one workspace share the ref prefix `refs/legion-bundle/`, and one run's
// delete lands inside the other's `git bundle create`.
//
// The shim's own side is given the same budget, not one of its own: the runtime hands it the
// smaller of runtime.WorkspaceBundleTimeout and what is left of this context
// (sandbox's Runtime.BundleWorkspace), so a copy the daemon stops waiting for is a copy the shim
// stops building. A bundler left running past the daemon's wait would still hold the workspace
// when the next turn's end asked for a copy, which is the second run in one workspace the ref
// prefix cannot carry. The copy is abandoned with the reason logged all the same.
const runningCopyBudget = runningCopyPeriod - idleTickInterval

// The lines one copy writes are pinned as constants, as supervise's turn lines are: an
// operator and a production check read each whole, and a message reworded at one call site would
// answer that search with a part of the answer.
const (
	// runningCopyStoredLine is a copy taken and stored, at Info: the issue, the bytes it holds,
	// `taken`, the store's time the copy's take began (store.WorkspaceCopyBegun), which is the
	// copy's own time, and how long the copy took, the bundler's build and the write together. The
	// cadence's period runs on its own clock from runningCopy.started, which the copy's take sets a
	// moment before the store's time is read, so `taken` stands close to the period's start. The
	// line's own timestamp is when the copy ended, so a reader measuring the cadence reads `taken`
	// and not the line's time.
	runningCopyStoredLine = "running copy: stored a copy of an issue workspace's unpushed work"
	// runningCopyDroppedLine is a workspace that bundled to nothing, at Info. The store refuses
	// an empty copy, so the issue's running copy is dropped instead: a copy taken minutes ago
	// holds work that has since been pushed, and the next pod must never restore stale work,
	// which is the rule the release bundle's own save follows (outbox.bundleIssueWorkspace).
	runningCopyDroppedLine = "running copy: the issue workspace holds nothing unpushed, so its running copy is dropped"
	// runningCopyUnsavedLine is a copy that was not taken or not stored, at Warn, under the
	// reason it failed by name. Such a copy holds nothing and leaves the issue with the copy it
	// had; the issue is tried again at the next period, between turns as well, once a role of it
	// is live (runningCopy.retry), unless the copy was refused for its size (runningCopyTooLarge).
	// A push's copy and an owed copy of an issue with no live role write it at Debug instead
	// (copyWorkspaceAtPush, takeRunningCopy), and a copy off a pod an idle release has saved its
	// bundle off writes it at Info, since nothing failed (runningCopyReleaseBundled), as do a
	// copy the store refuses because the issue's running copy was taken after it
	// (runningCopyNewerStands) and a copy of a paused project (runningCopyProjectPaused,
	// pausedCopy).
	runningCopyUnsavedLine = "running copy: no copy of the issue workspace was stored"
	// runningCopyOvertakenLine is a copy that was being built when the daemon recorded a push of
	// the issue's branch, at Info. It is not stored: it may have read the workspace before the
	// pushed commits were made, and a copy stored after a push that does not hold it is the very
	// copy the restore's push rule exists to keep from a pod. The copy the push asked for is owed
	// and is taken as this one ends, or, where it came to nothing before it could be owed, the
	// issue owes a retry in its place (runningCopy.missed).
	runningCopyOvertakenLine = "running copy: a push of the issue's branch was recorded while this copy was being taken, so it is not stored and the copy the push asked for is taken next"
	// runningCopyStoppedLine is a copy the daemon's own stop ended, at Debug: no copy was stored,
	// nothing failed, and the next daemon takes the issue's copies again.
	runningCopyStoppedLine = "running copy: the daemon is stopping, so this copy of the issue workspace ends unstored"
)

// The reasons runningCopyUnsavedLine carries. They are names rather than sentences because a
// deployment counts them: a cadence that stores nothing is read from which of these it writes.
const (
	// runningCopyClaimsUnread: the daemon could not read its own claims, so no role was chosen.
	runningCopyClaimsUnread = "claims_unread"
	// runningCopyNoRole: the issue has no live role with a process to ask. Only a shim can build
	// the bundle, in the workspace it already works in. A push's copy writes it at Debug, since a
	// push of a released or finished issue meets it every time (copyWorkspaceAtPush), and so does
	// an owed copy, whose role went after everything that owed it found one (takeRunningCopy).
	// The cadence never writes it for a retry: it retries only an issue with a live role in the
	// claims it read (copyWorkingWorkspaces).
	runningCopyNoRole = "no_live_role"
	// runningCopyClockUnread: the daemon could not read the store's clock as the copy's take
	// began, which is the copy's time (store.Store.BeginWorkspaceCopy), so it asked no shim.
	runningCopyClockUnread = "clock_unread"
	// runningCopyBundlerFailed: the bundler answered an error.
	runningCopyBundlerFailed = "bundler_failed"
	// runningCopyBudgetSpent: the bundler was still building at runningCopyBudget and the daemon
	// stopped waiting for it.
	runningCopyBudgetSpent = "budget_spent"
	// runningCopyTooLarge: the workspace's unpushed work is over runtime.MaxWorkspaceBundleBytes,
	// the bound a saved copy carries. The shim refuses such a bundle itself and names its size,
	// and the worker stream's connection answers that refusal, and a bundle that arrives whole
	// over the bound all the same, as runtime.WorkspaceBundleTooLargeError
	// (stream.Conn.BundleWorkspace), whose size the line carries as `bytes`. It is no fault to
	// retry, so it owes no retry (runningCopy.retry): the work stays over the bound until a role
	// pushes or drops some of it, and a retry between turns would have the shim build the same
	// refused bundle every period for as long as the pod stands. The cadence asks again at its
	// next period while a turn of the issue runs, and a turn's end and a push ask as they come.
	runningCopyTooLarge = "bundle_too_large"
	// runningCopySaveFailed: the store did not take the copy.
	runningCopySaveFailed = "save_failed"
	// runningCopyDropFailed: the store did not drop the copy of a workspace that bundled to
	// nothing, which leaves the issue holding work that has since been pushed.
	runningCopyDropFailed = "drop_failed"
	// runningCopyReleaseBundled: an idle release of the issue has saved its bundle off the pod
	// the copy would be taken off, and that bundle is the pod's last copy, so no copy is taken
	// off that pod (takeRunningCopy, store.Store.ReleaseBundleSavedOff). It is written at Info:
	// it is no fault, and it is not one of the reasons above that a deployment counts. The pod's
	// roles are still live between the release's save and their stops, and a push the daemon
	// records then asks them for a copy.
	runningCopyReleaseBundled = "release_bundle_saved"
	// runningCopyBundleUnread: the daemon could not read whether the issue's release bundle came
	// off the pod the copy would be taken off, so it asked no shim of that pod.
	runningCopyBundleUnread = "release_bundle_unread"
	// runningCopyNewerStands: the issue's running copy was taken after this copy's take began —
	// a cancelled release or a confirmed restore wrote it at that move, or a copy of another pod
	// came first — and a copy never replaces a newer one, nor drops one
	// (store.ErrNewerCopyStands, store.Store.PutRunningWorkspaceCopy,
	// store.Store.DeleteRunningWorkspaceCopy). A workspace that bundled to nothing writes it with
	// `bytes` 0: it says what the workspace held as its take began, and the row holds work read
	// after that. It is written at Info: it is no fault, and it is not one of the reasons above
	// that a deployment counts.
	runningCopyNewerStands = "newer_copy_stands"
	// runningCopyProjectPaused: the daemon's project is paused (pause.Service.Paused), and a
	// pause saves no workspace (docs/kubernetes.md, "Pausing a project"), so no copy starts while
	// it stands. The pause's hold of a pod stops only the idle release and the update round: a
	// close, a park, a teardown or an end from outside the pod still ends a held pod, and a role
	// of it may still be in its turn. A pod that ends during the pause loses the work done since
	// its last copy, which is from before the pause: the next pod restores that copy, and the
	// turns that ended in the pause are in none. That cost is chosen, as a pause saves no
	// workspace. It is written at Info with the open pause's id, since nothing failed, and it
	// owes a retry (runningCopy.retry): a turn that ended while the project was paused is in no
	// copy, so the cadence takes it again once the project has resumed and a role of the issue is
	// live.
	runningCopyProjectPaused = "project_paused"
)

// runningCopies is what the cadence holds in memory: per issue, whether a copy of that issue is
// in flight, when the last one started and whether it came to nothing and so owes a retry; and
// the context of the daemon's run, which every copy runs under.
//
// The count is per issue and never per role. One workspace belongs to one issue and the six roles
// of that issue share it, so six roles ending their turns in the same minute must take one bundle
// of that workspace and not six: two runs of the shim's bundler in one workspace share the ref
// prefix `refs/legion-bundle/`, and one run's delete would land inside the other's `git bundle
// create`. A release's save is such a run as well, and it takes the same place (hold).
//
// It is memory and not a table. A daemon that restarts has taken no copy in this process, so
// every issue in a turn is due at its first pass, which is the right answer: the daemon cannot
// know what the daemon before it copied, and a copy whose content equals the stored row's moves
// that row's two times alone (store.PutRunningWorkspaceCopy). A retry owed goes with the process
// as well: an issue between turns whose last copy came to nothing before a restart is copied
// again at its next turn's end or its next push. An entry is a few words per issue the daemon has
// copied since it started, and the cadence drops the entry of an issue no claim names at all
// (forgetUnclaimed).
//
// The run's context is held here rather than handed down because the paths that start a copy are
// callbacks — a claim's durable state (copyWorkspaceAtTurnEnd) and a push fact's commit
// (copyWorkspaceAtPush) — that are given no context of their own. A copy that ran under none
// would outlive the daemon's stop: it would run to its budget against a store already closed and
// log a failed save for a copy nobody could have stored.
//
// It holds nothing of how a copy is logged. A copy chooses the role it asks before it takes the
// issue's place (takeRunningCopy), so one with no role to ask never takes the place, and each
// entry holds the order of the issue's copies alone: the place, the copy owed behind it, the push
// that overtook it, the retry a copy that came to nothing beside it owes, the start the next
// period runs from, and the retry its last copy owes.
type runningCopies struct {
	mu     sync.Mutex
	issues map[string]runningCopy
	run    context.Context
	// startOwed starts the copy of an issue that was owed while a release's save held the issue's
	// place, once the save gives the place back (hold). It is the daemon's own start of a copy
	// (workflowRuntime.startOwedCopy), set where the daemon attaches the cadence (attach), because
	// the save runs in the outbox, which holds this memory and nothing that starts a copy. nil
	// starts nothing, which is a daemon whose runtime bundles no workspace and so owes no copy.
	startOwed func(issue string)
	// now is the cadence's clock, which starts an issue's period and reads it (take, untaken,
	// giveBack, copyWorkingWorkspaces): the wall clock when nil, and a test's own clock where the
	// test set one, so a test moves the cadence's time rather than waiting it out.
	now func() time.Time
}

// runUnder makes ctx, the daemon's run, the context every copy is started under from now on.
func (c *runningCopies) runUnder(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.run = ctx
}

// startsOwedWith makes start what takes a copy owed behind a release's save
// (runningCopies.startOwed).
func (c *runningCopies) startsOwedWith(start func(issue string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startOwed = start
}

// running is the daemon's run's context, and false before the run has started or once it has
// ended, when no copy is started at all.
func (c *runningCopies) running() (context.Context, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil || c.run.Err() != nil {
		return nil, false
	}
	return c.run, true
}

// runningCopy is one issue's place in the cadence.
type runningCopy struct {
	// owed is whether a copy of the issue was asked for while the one in flight was being built
	// and so is still to be taken: that copy read the workspace before the work which asked for
	// this one was written, so it holds none of that work.
	//
	// It is one flag and not a queue on purpose. Six roles ending their turns while one copy is
	// being built owe one copy between them and not six, because the copy taken after the one in
	// flight reads the workspace as it stands then and so holds the work of all six turns; a
	// queue would buy nothing but one `git bundle create` per turn end in a workspace a role is
	// working in.
	owed bool
	// pushed is whether the daemon recorded a push of the issue's branch while this copy was in
	// flight. Such a copy may have read the workspace before the pushed commits were made, so it
	// is not stored (storeRunningCopy), and the push's own copy, owed behind it, is; or, where the
	// push's copy came to nothing before it could be owed, the issue owes the retry it marked
	// (missed).
	pushed bool
	// missed is whether a copy of the issue came to nothing for a fault while a copy or a release's
	// save held the issue's place, before that copy could ask for the place itself: the claims or
	// the release bundle's pod unread, or no live role (untaken). The run holding the place read
	// the workspace before whatever asked for the missed copy was written, so whatever that run
	// comes to settles nothing of it, and its give-back leaves the retry owed (giveBack).
	//
	// The case that decides it is a push that overtakes a copy in flight while the issue has no
	// live role: the push's copy finds no role, so it is not owed behind the copy in flight, and
	// the copy in flight is not stored. Without the mark nothing would follow that copy and nothing
	// would be owed, so the newest copy of the idle pod would stay older than the push and than the
	// work that asked for the copy in flight. A release's save its release then cancels is the
	// other: it stores no bundle that holds the missed copy's work.
	missed bool
	// started is when the last copy of the issue started — when it took the issue's place, or
	// when it came to nothing before it could take it (untaken) — whatever it stored; zero for an
	// issue this daemon has copied none of. A release's save that takes the place starts it too.
	// The period is counted from it (due).
	started time.Time
	// retry is whether the last copy of the issue came to nothing for a fault — the claims, the
	// clock or the release bundle's pod unread, no live role, the bundler failed or ran out of
	// budget, the write or the drop failed — or a copy beside it did (missed), so the cadence takes
	// the issue again once its period is due and a role of it is live, between turns as well
	// (copyWorkingWorkspaces). A turn's end that failed would otherwise leave the issue's newest
	// copy short of that turn until the next turn ends, and an eviction of the idle pod would lose
	// it. A copy that stored the issue's copy, dropped it as empty, or was refused for no fault — a
	// newer copy stands, the release bundle is the pod's last copy, or a push overtook it — clears
	// it, and so do a copy refused for its size, which is no fault to retry (runningCopyTooLarge),
	// and the cadence's forgetting of an issue no claim names (forgetUnclaimed).
	retry bool
	// free is the issue's one place in flight: nil while nothing of the issue runs, made when a
	// copy or a release's save takes the place (take) and closed when that run gives it back
	// (giveBack), which is what a save waiting for the place waits on (hold).
	free chan struct{}
}

// inFlight is whether a copy of the issue or a release's save holds the issue's place.
func (held runningCopy) inFlight() bool { return held.free != nil }

// take takes issue's one place in flight if nothing holds it, starting the issue's period, and
// answers the issue's entry as it found it and whether it took the place. It is the one way the
// place is taken, by a copy (begin) and by a release's save (hold). The caller holds c.mu.
func (c *runningCopies) take(issue string) (runningCopy, bool) {
	if c.issues == nil {
		c.issues = make(map[string]runningCopy)
	}
	held := c.issues[issue]
	if held.inFlight() {
		return held, false
	}
	held.free, held.started = make(chan struct{}), c.clock()
	c.issues[issue] = held
	return held, true
}

// giveBack gives issue's place back, and answers the issue's entry as it found it, which says
// whether a copy was owed while the place was held. It is the one way the place is given back, by
// a copy (done) and by a release's save (hold): it wakes a save waiting for the place, keeps the
// period the take started with nothing owed and nothing overtaken, marks the retry as retry says
// or as a copy that came to nothing beside the run left it owed (runningCopy.retry,
// runningCopy.missed), and drops the entry of every issue whose place is free, that owes no retry
// and whose period has passed. The caller holds c.mu.
//
// Such an entry answers due exactly as a missing one does, so keeping it says nothing the map
// would not say without it, and a daemon that runs for weeks would hold one word per issue it
// has ever copied for ever. It is pruned where the map is written, as the runtime's two records
// of a pod's end are (sandbox's prunePodEnds), so nothing has to sweep it. An entry that owes a
// retry says what a missing one does not, and stays until a copy of its issue clears it or the
// cadence finds no claim of its issue at all (forgetUnclaimed), so what the map holds beyond the
// issues being copied is one entry per claimed issue whose last copy came to nothing. The entry
// this call writes stays unless the run it ends held the place a whole period, which no copy can
// (runningCopyBudget) and only a slow release's save can; such an entry is due at once, as a
// missing one is.
func (c *runningCopies) giveBack(issue string, retry bool) runningCopy {
	held := c.issues[issue]
	if held.inFlight() {
		close(held.free)
	}
	c.issues[issue] = runningCopy{started: held.started, retry: retry || held.missed}
	now := c.clock()
	maps.DeleteFunc(c.issues, func(_ string, held runningCopy) bool {
		return !held.inFlight() && !held.retry && now.Sub(held.started) >= runningCopyPeriod
	})
	return held
}

// begin takes the issue's one place in flight for a copy, answering false when a copy of it or a
// release's save already holds it, and marking that refused copy owed so that the copy in flight
// takes it when it ends (done).
//
// A refused copy is owed and not dropped because the copy in flight read the workspace before
// the work that asked for this one was written. The copy which closes a turn is the case that
// decides it: the copy in flight may well be stored, the cadence takes no copy between turns of
// an issue whose last copy was stored, and no push need come to copy it, so a turn's end that was
// dropped would leave the newest copy of a pod whose roles have all gone idle short of the work
// its last turn ended with, and an eviction of that idle pod would then lose the end of the turn
// — which is exactly what the daemon holds itself to (R1).
func (c *runningCopies) begin(issue string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, took := c.take(issue)
	if !took {
		held.owed = true
		c.issues[issue] = held
	}
	return took
}

// pushed marks the copy of issue in flight, if one is, as overtaken by a push of the issue's
// branch the daemon has just recorded (runningCopy.pushed).
func (c *runningCopies) pushed(issue string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if held, ok := c.issues[issue]; ok && held.inFlight() {
		held.pushed = true
		c.issues[issue] = held
	}
}

// overtaken is whether the copy of issue in flight was overtaken by a push (pushed).
func (c *runningCopies) overtaken(issue string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.issues[issue].pushed
}

// hold takes the issue's one place in flight for a release's save, waiting while a copy holds
// it, and answers the call that gives it back; it answers ctx's error when ctx ends first. The
// save and a copy both run the shim's bundler in the issue's workspace, so the save must never
// run beside one, and only the timing of the two kept them apart before: a turn's end and the
// copy it was owed can take two copy budgets, which is more than a short `idle_minutes`.
//
// Giving the place back is a copy's own give-back (giveBack): it keeps the period the save's take
// started and prunes as a copy's end does. The save is no copy of the cadence's — it neither
// stores the issue's running copy nor fails one — so its give-back leaves the retry the issue owed
// as it found it (runningCopy.retry), and adds the retry a copy that came to nothing while the save
// held the place owes (runningCopy.missed): the save read the workspace before the work that asked
// for that copy was written, so neither its bundle nor the running copy a cancelled release leaves
// holds that work, and dropping the retry would leave an eviction of the pod short of it. A copy
// owed while the save held the place — a push's above all, or a turn's end — is started once the
// place is given back, through the daemon's own start of a copy (startOwed), and takes the place
// again as any copy does, whatever the save did. A save that stored no bundle — its bundler
// failed, the bundle was too large, its write failed, or the workspace held nothing unpushed —
// leaves the pod with no last copy of its own, and its release is cancelled or goes on with
// nothing to restore, so the owed copy is the one that holds the work that asked for it, which
// was written after whatever the issue's copy holds; dropping it would leave an eviction of the
// pod short of that work. A save that stored its bundle made that bundle the pod's last copy, and
// the copy then started, or retried, is refused by the one rule that says so, before any shim is
// asked and again at its write (takeRunningCopy, store.Store.ReleaseBundleSavedOff), so the
// give-back holds no rule of its own about it.
func (c *runningCopies) hold(ctx context.Context, issue string) (func(), error) {
	for {
		c.mu.Lock()
		held, took := c.take(issue)
		c.mu.Unlock()
		if took {
			return func() {
				c.mu.Lock()
				owed := c.giveBack(issue, c.issues[issue].retry).owed
				start := c.startOwed
				c.mu.Unlock()
				if owed && start != nil {
					start(issue)
				}
			}, nil
		}
		select {
		case <-held.free:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// done gives the issue's place back (giveBack), marks whether the copy that held it owes a retry
// (runningCopy.retry) — retry, or one a copy beside it left owed (runningCopy.missed) — and answers
// whether a copy of the issue is owed: one was asked for while this one was in flight, and the
// caller takes it now. Only the caller whose begin answered true calls it.
//
// The owed copy it reports is cleared by the reading: the copy the caller then starts takes the
// issue's place in flight again and is owed another only if a third is asked for while it runs,
// so a storm of turn ends is one copy after the one in flight rather than one copy per end.
func (c *runningCopies) done(issue string, retry bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.giveBack(issue, retry).owed
}

// due is whether the cadence takes a copy of issue at a tick at now, with tick the period the
// cadence's ticker runs at: nothing of it is in flight, and less than one tick of the period is
// left since the last copy started. The tick before the one that finds an issue due found less
// than a period less one tick gone, so when ticks arrive on time the copy due starts is at most
// one period after the last start, and never less than a period less one tick after it, which is
// the bound runningCopyPeriod states. An issue this daemon has copied none of is due at once.
func (c *runningCopies) due(issue string, now time.Time, tick time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	held := c.issues[issue]
	return !held.inFlight() && now.Sub(held.started) >= runningCopyPeriod-tick
}

// untaken records a copy of issue that came to nothing before it took the issue's place — the
// claims unread, no live role, or the pod's release bundle unread or saved — as the start of the
// issue's period, owing a retry where retry says (runningCopy.retry). Its period starts so that a
// retry waits a period from the copy that owed it, as every retry does, and a copy that keeps
// failing before it can take the place is tried once a period rather than at every tick.
//
// Beside a copy in flight or a release's save, which holds the place and has started the period,
// a copy that owes a retry marks it on that run's entry instead (runningCopy.missed), and the run's
// give-back leaves it owed whatever the run came to: the run read the workspace before whatever
// asked for this copy was written. One that owes none records nothing.
func (c *runningCopies) untaken(issue string, retry bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if held := c.issues[issue]; held.inFlight() {
		if retry {
			held.missed = true
			c.issues[issue] = held
		}
		return
	}
	if c.issues == nil {
		c.issues = make(map[string]runningCopy)
	}
	c.issues[issue] = runningCopy{started: c.clock(), retry: retry}
}

// retries is every issue whose last copy owes a retry (runningCopy.retry), by key.
func (c *runningCopies) retries() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var owed []string
	for issue, held := range c.issues {
		if held.retry {
			owed = append(owed, issue)
		}
	}
	slices.Sort(owed)
	return owed
}

// forgetUnclaimed drops the entry of every issue that claimed does not name — the issues the
// cadence's read of the project's claims holds a claim of — retry and all, unless a copy or a
// release's save of it holds its place. Such an issue has no pod whose work a copy could hold,
// finished or never run in this project, as the issue of a branch pushed from outside, and an
// entry kept for it would never be cleared: no copy of it can be stored. An issue with a claim in
// any state is kept, a released one above all, whose retry runs once a woken pod's role is live.
func (c *runningCopies) forgetUnclaimed(claimed map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	maps.DeleteFunc(c.issues, func(issue string, held runningCopy) bool {
		return !held.inFlight() && !claimed[issue]
	})
}

// clock is the cadence's time (runningCopies.now).
func (c *runningCopies) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// tickWorkspaceCopies keeps a copy of each issue workspace's unpushed work outside its pod while
// the pod runs, on the daemon's own period (idleTickInterval), so the work a pod loses when the
// cluster ends it is the work of the minutes since the last copy rather than the work of the
// whole turn. It is the other half of the copy a turn's end takes (copyWorkspaceAtTurnEnd): the
// turn's end gives the copy that closes each turn, and this gives the one inside a turn long
// enough to outlast the period, and takes again, between turns as well, the copy of an issue whose
// last copy came to nothing (copyWorkingWorkspaces).
//
// It is a ticker of its own beside tickIdle and tickWakes, and it is not the idle release's: a
// deployment that releases no pod (`idle_minutes: 0`) still has pods the cluster can end under
// it, so the cadence is wired in attach rather than in attachIdleRelease. What turns it off is
// having nothing to ask or nowhere to put the answer — a runtime that implements no
// runtime.WorkspaceBundler, which is `runtime: tmux`, or a daemon with no store — and either
// leaves it running no ticker at all.
func (w *workflowRuntime) tickWorkspaceCopies(ctx context.Context) {
	if w.copyBundler == nil || w.copyStore == nil {
		return
	}
	ticker := time.NewTicker(w.copyTick())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.copyWorkingWorkspaces(ctx)
		}
	}
}

// copyWorkingWorkspaces takes a copy of every issue of the project that is in a turn and is due,
// and of every issue whose last copy came to nothing, that is due and that has a live role in the
// claims it read, whatever its roles are doing (runningCopy.retry). An issue between turns whose
// last copy was stored is left alone: the turn that ended took the copy that holds that work, and
// copying a workspace nobody is writing would store the same bundle again every period for as
// long as the pod stands. One whose last copy came to nothing — a turn's end whose bundler failed
// or whose write failed, above all — holds no copy of the turn that ended, so it is taken again
// each period until a copy of it is stored or settled, and an eviction of its idle pod then
// restores the end of the turn.
//
// The pass reads one list: the issues in a turn, then every issue owing a retry that is not among
// them. It takes a copy of each that is due and has a live role in the claims it read, chosen as
// the copy itself chooses the role it asks (runningCopyClaim); an issue in a turn always has one,
// since the role in the turn is live. A retry so waits for a live role of its issue. An issue with
// none — released, or with every role stopped — has no running pod whose work a copy could hold,
// so the pass skips it with no line and it keeps its retry, which runs at the first due tick after
// a resumed or relaunched pod's role is live again. The pass also forgets every issue no claim of
// the project names at all (forgetUnclaimed), so the cadence's memory does not grow with every
// issue that ever failed a copy.
//
// Which issues are in a turn is read from the claims the daemon's own store holds rather than
// from anything this process remembers, so a daemon that has just restarted reads it exactly as
// one that has been up for a day. Those same claims are what each copy chooses its role from, so
// the read is made once here and handed on (takeRunningCopy) rather than made again per issue.
// The cadence's clock is read once for the pass, and the tick due allows for is the period the
// ticker runs at (copyTick).
//
// The idle release's gate on the Envoy listener (listenerLive) is not read here. That gate asks
// whether a released pod could be woken by a message, which is the release's question alone: a
// copy releases no pod and wakes none, it is taken while the pod runs, and a deployment whose
// listener has gone quiet is one whose pods keep working and whose work still has to be held
// against an eviction. What turns the cadence off is having nothing to ask or nowhere to put the
// answer, which tickWorkspaceCopies reads before it starts a ticker at all.
func (w *workflowRuntime) copyWorkingWorkspaces(ctx context.Context) {
	claims, err := w.copyStore.Claims(ctx)
	if err != nil {
		w.log.Warn("running copy: could not read the project's claims, so no issue was copied at this tick", "error", err)
		return
	}
	claimed := make(map[string]bool)
	for _, c := range claims {
		if c.Project == w.projectID && c.Issue != "" {
			claimed[c.Issue] = true
		}
	}
	w.copies.forgetUnclaimed(claimed)
	now, tick := w.copies.clock(), w.copyTick()
	issues := issuesInTurn(claims, w.projectID)
	for _, issue := range w.copies.retries() {
		if !slices.Contains(issues, issue) {
			issues = append(issues, issue)
		}
	}
	for _, issue := range issues {
		if ctx.Err() != nil {
			return
		}
		if !w.copies.due(issue, now, tick) {
			continue
		}
		if _, live := runningCopyClaim(claims, w.projectID, issue); !live {
			continue
		}
		w.startRunningCopy(issue, claims, slog.LevelWarn)
	}
}

// copyWorkspaceAtTurnEnd takes the copy that ends a turn: the claim of the issue has just gone
// durably idle, so what the role did in that turn is in the workspace and in no copy yet.
//
// It is the daemon's durable-state callback and runs on the machine's own goroutine, so it hands
// the copy to a goroutine of its own and returns at once, exactly as podResume does. Nothing of
// the turn waits for it: not the turn's end, not the send of the next task, not the claim's own
// state write, each of which the machine makes with this callback still running.
func (w *workflowRuntime) copyWorkspaceAtTurnEnd(c supervise.Claim, state supervise.ClaimState) {
	if state != supervise.StateIdle || w.copyBundler == nil || w.copyStore == nil {
		return
	}
	if c.Issue == "" || c.Role == claim.RoleController {
		return
	}
	// It hands on no claims: this path holds the one claim whose turn ended and not the
	// project's, so the copy reads them itself.
	w.startRunningCopy(c.Issue, nil, slog.LevelWarn)
}

// copyWorkspaceAtPush takes a copy of issue's workspace because the daemon has just recorded a
// push of its branch: the workflow engine calls it once the push fact's transaction has committed
// (workflow.Config.PushRecorded). The restore of a new pod refuses every running copy older than
// the issue's newest recorded push (runtime.DecideWorkspaceRestore), because such a copy would put
// the workspace back on work behind the pushed commits; this is the copy that takes the refused
// one's place, taken after the push and so holding it and whatever the roles did beyond it. Update
// rounds make it the common case, since roles push just before their pod is replaced. No restore
// sets the issue's branch itself (runtime.RestorableBookmarks), so neither copy can move it.
//
// It goes through the cadence's own single flight (takeRunningCopy), so it never runs beside
// another copy or a release's save in the same workspace. A copy already in flight is marked
// overtaken first: it may have read the workspace before the pushed commits were made, so it is
// not stored, and this copy, owed behind it, is taken as it ends. It runs on a goroutine of its
// own like every other copy, so the fact's commit waits for nothing. The engine asks for it only
// for a push that moved the issue's push record (store.Store.RecordIssuePush), so a redelivered
// or late webhook neither discards the copy in flight nor bundles the workspace again. A push the
// daemon records while an idle release runs, between its save and its role stops, takes no copy:
// the release's bundle is that pod's last copy (takeRunningCopy).
//
// An issue with no live role to ask is logged at Debug for a push, where the turn's end and the
// cadence inside a turn log it at Warn (takeRunningCopy). Every push of the branch that moves the
// record asks, whoever pushed and whatever the issue is doing, so a push of an issue that was
// released or has finished finds no role at all, and a Warn for each would bury the cadence's and
// the turn end's own, which are the ones a deployment counts: those paths ask only of an issue
// that had a role a moment ago. The role is read before the issue's place in flight is taken, so
// such a push's copy logs at once, beside a copy in flight as well, and is never owed behind it.
// It owes a retry all the same, which the cadence takes once a role of the issue is live
// (copyWorkingWorkspaces). Beside a copy in flight it marks that retry on the issue's place
// (runningCopies.untaken), and the copy in flight, which this push overtook and which is not
// stored, leaves it owed as it ends: nothing else follows that copy.
func (w *workflowRuntime) copyWorkspaceAtPush(issue string) {
	if w.copyBundler == nil || w.copyStore == nil || issue == "" {
		return
	}
	w.copies.pushed(issue)
	w.startRunningCopy(issue, nil, slog.LevelDebug)
}

// startRunningCopy hands one copy of issue's workspace to a goroutine of its own, under the
// budget one copy is given and inside the daemon's run (runningCopies.running), so the daemon's
// stop ends it. Nothing waits on it: a copy is a saving against an eviction, never a step of the
// turn, the tick or the fact that asked for it. claims is the caller's own read of the project's
// claims where it has one, and nil where it has not. It is how the owed copy is taken too
// (startOwedCopy), so a copy owed to a turn's end holds up neither the copy that owed it nor
// anything else. Before the run starts and once it has ended no copy is started at all. noRole is
// the level the copy logs an issue with no live role to ask at (takeRunningCopy).
//
// The goroutine takes a hold mark of its own for the store's pool (store.ForConcurrentWorker),
// as every loop of the daemon's run does: copies of different issues run beside each other and
// beside those loops, and each reads the claims and writes its row through the pool, so a mark
// shared with any of them would have the pool refuse one its first connection while another held
// one, and an unmarked context would leave the pool unable to refuse a copy a second connection
// of its own. The run it is made from holds no connection, so a fresh mark sees everything.
func (w *workflowRuntime) startRunningCopy(issue string, claims []supervise.Claim, noRole slog.Level) {
	run, ok := w.copies.running()
	if !ok {
		w.log.Debug(runningCopyStoppedLine, "issue", issue)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(store.ForConcurrentWorker(run), w.runningCopyBudget())
		defer cancel()
		w.takeRunningCopy(ctx, issue, claims, noRole)
	}()
}

// startOwedCopy takes the copy of issue that was owed while a copy in flight or a release's save
// held the issue's place (runningCopies.done, runningCopies.hold), once that place is given back.
// The owed copy is a copy like every other: a goroutine of its own and a budget of its own,
// because nothing of a turn waits for a copy, under the daemon's run and not the context of what
// held the place, which ends as it gives the place back. It is handed no claims — any the holder
// was given were read before the work that owed the copy was written, and the role it asks is read
// as the turn's end reads it — and it logs an issue with no live role at Debug (takeRunningCopy).
func (w *workflowRuntime) startOwedCopy(issue string) {
	w.startRunningCopy(issue, nil, slog.LevelDebug)
}

// takeRunningCopy asks one live role of issue for the git bundle of its workspace's unpushed work
// and stores it as the issue's running copy (storeRunningCopy) — the same BundleWorkspace call an
// idle release's save makes (outbox.bundleIssueWorkspace), the same bound on its size, and the same
// answer to a workspace that bundles to nothing.
//
// claims is the project's claims as the caller already read them, which is what the role is
// chosen from (runningCopyClaim); the tick reads them once for the whole pass and hands that one
// slice to every copy it starts, and a caller with none — the turn's end, which holds one claim
// and not the project's — passes nil and has them read here.
//
// The role is chosen first, and a copy with no role to ask, or no claims to choose one from, ends
// there, logged, without taking the issue's place in flight: it bundles nothing, so there is no
// bundler of it to keep apart from another and nothing to be owed behind it. It starts the
// issue's period and owes a retry all the same, which it marks on the run that holds the place
// where one does (runningCopies.untaken). noRole is the level it logs an issue with no live role
// at: Warn for the turn's end and the cadence, which ask of an issue that had a role a moment ago
// or, for a retry, has one in the claims the cadence read, and Debug for a push
// (copyWorkspaceAtPush) and for an owed copy. An owed copy is owed only by copies that found a
// live role before they asked, behind a copy in flight that found one as well, so one that finds
// none has met a pod that went in the moment since, and the Warn the turn's end and the cadence
// write is for an issue they found with none.
//
// A copy with a role takes the issue's one place in flight and holds it until the call has
// answered, so no second copy of the issue is ever being built: a copy abandoned at its budget
// while the shim went on building would leave exactly the two runs in one workspace that the ref
// prefix cannot carry. A copy refused by that mark is owed rather than dropped, and this call takes
// it as it returns: the copy in flight read the workspace before the work that asked for the
// refused one was written, and the cadence takes no copy between turns of an issue whose last copy
// was stored, so a turn's end that was dropped would leave the newest copy of a pod whose roles
// have all gone idle short of the work its last turn ended with, and an eviction of that idle pod
// would lose the end of the turn (R1). The next period runs from the moment the copy took the
// place, and what the copy came to is the retry the issue owes as it gives the place back
// (storeRunningCopy, runningCopies.done).
//
// A copy off a pod an idle release has saved its bundle off is refused, before the issue's place
// is taken and the shim is asked, and again by the store's write: once an idle release has saved
// its bundle, that bundle is the pod's last copy. The release's rows run the save first, then a
// stop per role, then the teardown (idleReleaseRows), so the pod's roles are still live after the
// save, and a copy asked of them then — the one a push the daemon records in that window asks
// for above all — would be stored as the later of the issue's two copies, offered to the next pod
// in the bundle's place, and the bundle's row would hold the issue's pod from every idle release
// after it (workflow.IdleHoldBundleUnrestored). The read names the pod the chosen role's locator
// records, which is the pod the runtime's bundler names the copy by (sandbox's
// Runtime.BundleWorkspace), so no bundle is built off that pod; the write refuses by the pod the
// copy itself names (store.ErrReleaseBundleSaved), which also refuses a copy whose read ran
// before the save and whose place was taken after the save gave it back, and a copy asked of a
// role whose locator names no pod. The two are one rule (store.Store.ReleaseBundleSavedOff), and
// each logs it at Info under a reason of its own (runningCopyReleaseBundled). Every copy is taken
// here — the cadence's (copyWorkingWorkspaces), a turn's end (copyWorkspaceAtTurnEnd), a push's
// (copyWorkspaceAtPush) and the copy owed behind one in flight (done) or behind a release's save
// (runningCopies.hold) — so each meets it.
//
// Before all of that, a copy of a paused project is not taken at all: a pause saves no workspace
// (runningCopyProjectPaused). The flag is the one every launch of the project reads
// (pause.Service.Paused), so the check is one atomic load and reads no claim. It is read as the
// copy starts and again just before the shim is asked (storeRunningCopy), so only a copy already
// asking its shim when the pause commits runs to its end: it read the workspace before the pause,
// and the pause holds the pod it came off. The skipped copy starts the issue's period and owes a
// retry, as one with no live role does.
func (w *workflowRuntime) takeRunningCopy(ctx context.Context, issue string, claims []supervise.Claim, noRole slog.Level) {
	if pause, paused := w.projectPaused(); paused {
		w.copies.untaken(issue, true)
		w.pausedCopy(issue, pause)
		return
	}
	if claims == nil {
		read, err := w.copyStore.Claims(ctx)
		if err != nil {
			w.copies.untaken(issue, true)
			w.unsavedCopy(issue, "reason", runningCopyClaimsUnread, "error", err)
			return
		}
		claims = read
	}
	chosen, ok := runningCopyClaim(claims, w.projectID, issue)
	if !ok {
		w.copies.untaken(issue, true)
		w.log.Log(ctx, noRole, runningCopyUnsavedLine, "issue", issue, "reason", runningCopyNoRole)
		return
	}
	role := chosen.Role
	// A locator that names no pod leaves the refusal to the store's write, which reads the pod
	// the copy itself names.
	if pod := podOf(chosen.Locator); pod != "" {
		saved, err := w.copyStore.ReleaseBundleSavedOff(ctx, issue, pod)
		if err != nil {
			w.copies.untaken(issue, true)
			w.unsavedCopy(issue, "role", role, "reason", runningCopyBundleUnread, "pod", pod, "error", err)
			return
		}
		if saved {
			w.copies.untaken(issue, false)
			w.log.Info(runningCopyUnsavedLine, "issue", issue, "role", role, "reason", runningCopyReleaseBundled, "pod", pod)
			return
		}
	}
	if !w.copies.begin(issue) {
		w.log.Debug("running copy: a copy of this issue's workspace is already being taken, so this one is owed and will be taken when that one ends", "issue", issue)
		return
	}
	if w.copies.done(issue, w.storeRunningCopy(ctx, issue, role)) {
		w.startOwedCopy(issue)
	}
}

// storeRunningCopy asks role's shim for the bundle of issue's workspace and stores it as the
// issue's running copy, while the copy takeRunningCopy started holds the issue's place, and
// answers whether the issue owes a retry (runningCopy.retry), which the place's give-back marks
// (runningCopies.done). Each way it ends writes its own line before it answers.
//
// A copy a push of the issue's branch overtook while it was being built is not stored
// (copyWorkspaceAtPush): it may hold none of the pushed commits. It owes no retry of its own,
// since the push's own copy settles what the push asked for: it is owed behind this one, or it was
// refused for no fault, or it came to nothing for a fault before it could be owed and left its
// retry owed on the issue's place (runningCopy.missed).
//
// The copy's time is the store's clock as its take begins, read once the copy holds the issue's
// place and before the shim is asked (store.Store.BeginWorkspaceCopy), and the store writes that
// time and not its own clock at the write. A copy read before a push and written after the push's
// record is then older than the push, and the restore refuses it (runtime.DecideWorkspaceRestore):
// the overtaken mark above is set only once the push's transaction has committed, so a copy that
// checks the mark just before it and writes just after the push's record is stored all the same.
// A clock the daemon cannot read is a copy not taken, logged under its reason as every other.
// That time is handed to the drop of a workspace that bundled to nothing as well
// (store.Store.DeleteRunningWorkspaceCopy), which drops no copy taken after it: the read says
// what the workspace held as the take began, and a row written since — a cancelled release's
// move, a confirmed restore's keep — holds work read after that. Such a drop is logged at Info
// under runningCopyNewerStands, and the copy it left stands for the next pod.
//
// Every way it can come to nothing for a fault is logged under its own reason and holds nothing:
// the issue keeps the copy it had and owes a retry, which the cadence takes at the next period,
// between turns as well, once a role of the issue is live (runningCopy.retry). A copy that settles
// the issue's copy — stores it, drops it as empty, or is refused for no fault by a newer copy, by
// the release bundle or by a push that overtook it — owes none. Nor does a bundle over the bound,
// which the connection answers as runtime.WorkspaceBundleTooLargeError whether the shim refused it
// with its size or it arrived whole (stream.Conn.BundleWorkspace): it is no fault to retry
// (runningCopyTooLarge). The daemon's own stop is not a fault: the run's context ending ends the
// copy, which is the stop and nothing an operator need read, so it is logged at Debug alone.
func (w *workflowRuntime) storeRunningCopy(ctx context.Context, issue string, role claim.Role) (retry bool) {
	// How long the copy took, read at whichever line is written: the bundler's own build and,
	// where one was made, the write that stored or dropped the row. A deployment reads it to see
	// a workspace growing slow long before a copy is abandoned at its budget.
	started := time.Now()
	took := func() string { return time.Since(started).Round(time.Millisecond).String() }
	begun, err := w.copyStore.BeginWorkspaceCopy(ctx)
	if err != nil {
		w.unsavedCopy(issue, "role", role, "reason", runningCopyClockUnread, "error", err)
		return true
	}
	// The copy read no pause as it started (takeRunningCopy) and has waited on the store since,
	// so it reads the flag again before it asks the shim: only a copy already asking its shim
	// when a pause commits runs to its end.
	if pause, paused := w.projectPaused(); paused {
		w.pausedCopy(issue, pause, "role", role)
		return true
	}
	bundle, err := w.copyBundler.BundleWorkspace(ctx, issue, role)
	var tooLarge *runtime.WorkspaceBundleTooLargeError
	switch {
	case errors.As(err, &tooLarge):
		w.log.Warn(runningCopyUnsavedLine, "issue", issue, "role", role, "reason", runningCopyTooLarge,
			"bytes", tooLarge.Bytes, "max", tooLarge.Max, "took", took())
		return false
	case err != nil:
		reason := runningCopyBundlerFailed
		if errors.Is(err, context.DeadlineExceeded) {
			reason = runningCopyBudgetSpent
		}
		w.unsavedCopy(issue, "role", role, "reason", reason, "budget", w.runningCopyBudget().String(), "took", took(), "error", err)
		return true
	}
	size := bundle.Size()
	if size == 0 {
		err := w.copyStore.DeleteRunningWorkspaceCopy(ctx, issue, begun)
		switch {
		case errors.Is(err, store.ErrNewerCopyStands):
			w.log.Info(runningCopyUnsavedLine, "issue", issue, "role", role, "reason", runningCopyNewerStands,
				"pod", bundle.PodUID, "bytes", 0, "taken", begun.At(), "took", took())
			return false
		case err != nil:
			w.unsavedCopy(issue, "role", role, "reason", runningCopyDropFailed, "took", took(), "error", err)
			return true
		}
		w.log.Info(runningCopyDroppedLine, "issue", issue, "role", role, "bytes", 0, "took", took())
		return false
	}
	if w.copies.overtaken(issue) {
		w.log.Info(runningCopyOvertakenLine, "issue", issue, "role", role, "bytes", size, "taken", begun.At(), "took", took())
		return false
	}
	err = w.copyStore.PutRunningWorkspaceCopy(ctx, issue, begun, bundle)
	switch {
	case errors.Is(err, store.ErrReleaseBundleSaved):
		w.log.Info(runningCopyUnsavedLine, "issue", issue, "role", role, "reason", runningCopyReleaseBundled,
			"pod", bundle.PodUID, "bytes", size, "took", took())
		return false
	case errors.Is(err, store.ErrNewerCopyStands):
		w.log.Info(runningCopyUnsavedLine, "issue", issue, "role", role, "reason", runningCopyNewerStands,
			"pod", bundle.PodUID, "bytes", size, "taken", begun.At(), "took", took())
		return false
	case err != nil:
		w.unsavedCopy(issue, "role", role, "reason", runningCopySaveFailed, "bytes", size, "took", took(), "error", err)
		return true
	}
	w.log.Info(runningCopyStoredLine, "issue", issue, "role", role, "bytes", size, "taken", begun.At(), "took", took())
	return false
}

// unsavedCopy logs a copy of issue that came to nothing under its reason, at Warn
// (runningCopyUnsavedLine), or the daemon's stop at Debug where that is what ended it
// (runningCopyStoppedLine).
func (w *workflowRuntime) unsavedCopy(issue string, args ...any) {
	if _, running := w.copies.running(); !running {
		w.log.Debug(runningCopyStoppedLine, "issue", issue)
		return
	}
	w.log.Warn(runningCopyUnsavedLine, append([]any{"issue", issue}, args...)...)
}

// pausedCopy logs a copy of issue that the open pause of the daemon's project kept from its shim,
// at Info under runningCopyProjectPaused with the pause's id, since nothing failed
// (runningCopyUnsavedLine). args are what the line names between the issue and the reason: the
// role the copy was asked of, where one was chosen.
func (w *workflowRuntime) pausedCopy(issue string, pause int64, args ...any) {
	args = append(append([]any{"issue", issue}, args...), "reason", runningCopyProjectPaused, "pause", pause)
	w.log.Info(runningCopyUnsavedLine, args...)
}

// issuesInTurn is every issue of the project with a turn in flight, each named once, in the order
// the claims were read. A claim in supervise.StateWorking is a role whose agent is in a turn, and
// the cadence copies that issue's workspace every period. An issue between turns is copied by the
// cadence only to retry a copy of it that came to nothing (copyWorkingWorkspaces).
func issuesInTurn(claims []supervise.Claim, project string) []string {
	var issues []string
	seen := make(map[string]bool)
	for _, c := range claims {
		if c.Project != project || c.Issue == "" || c.Role == claim.RoleController {
			continue
		}
		if c.State != supervise.StateWorking || seen[c.Issue] {
			continue
		}
		seen[c.Issue] = true
		issues = append(issues, c.Issue)
	}
	return issues
}

// runningCopyClaim is the claim of the role the copy is asked of: the first live role of the
// issue with a registered agent and a running process (supervise.HoldsCapability), which is what
// a shim is. It is the claim and not the role alone because its locator names the pod the copy
// would be taken off (takeRunningCopy).
//
// The order is the order the store reads the claims in, by claim token, which is the order the
// idle release's own view reads them in and so the order of the roles its decision carries
// (workflow.IdleRelease); the release's save asks the first of those (decision.Roles[0]). One
// workspace is shared by every role of the issue, so which of them answers decides nothing but
// whose process does the work — and the two paths choosing the same way means a deployment reads
// one role's log for both.
func runningCopyClaim(claims []supervise.Claim, project, issue string) (supervise.Claim, bool) {
	for _, c := range claims {
		if c.Project != project || c.Issue != issue || c.Role == claim.RoleController {
			continue
		}
		if supervise.HoldsCapability(c.State) {
			return c, true
		}
	}
	return supervise.Claim{}, false
}

// runningCopyBudget is how long one copy is given, the production constant unless a test
// shortened it.
func (w *workflowRuntime) runningCopyBudget() time.Duration {
	if w.copyBudget > 0 {
		return w.copyBudget
	}
	return runningCopyBudget
}

// copyTick is the period the cadence's ticker runs at: the daemon's idle tick, idleTickInterval
// unless a test shortened it. It is the tick due allows for, so the bound runningCopyPeriod states
// holds at whatever period the ticker runs.
func (w *workflowRuntime) copyTick() time.Duration {
	if w.idleTick > 0 {
		return w.idleTick
	}
	return idleTickInterval
}

// projectPaused is the open pause's id and true while the daemon's project is paused, as the
// pause service last read or made it (pause.Service.Paused), and false for a runtime that holds no
// pause: run builds the project's pause before supervision and before any copy can start
// (workflowRuntime.pauses), and a runtime a test builds by hand holds none.
func (w *workflowRuntime) projectPaused() (int64, bool) {
	if w.pauses == nil {
		return 0, false
	}
	return w.pauses.service.Paused()
}
