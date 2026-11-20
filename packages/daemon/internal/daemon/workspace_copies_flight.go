package daemon

// The per-issue single flight of the running copy (R1 of dispatch://INFRA-1117): what the
// cadence holds in memory of each issue's copies (runningCopies) — the one place in flight that a
// copy or an idle release's save takes, the copy owed behind it and whom that copy asks and
// answers (owedCopy), the push that overtook it, the retry a copy that came to nothing owes, and
// the start the next period runs from. workspace_copies.go holds the copies themselves: the
// cadence's tick, a turn's end, a push, and the take and the store of one copy.

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/supervise"
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
// callbacks — a machine's turn's end (copyWorkspaceAtTurnEnd, supervise.Deps.CopyTurnEnd) and a
// push fact's commit (copyWorkspaceAtPush) — that are given no context of their own. A copy that
// ran under none would outlive the daemon's stop: it would run to its budget against a store
// already closed and log a failed save for a copy nobody could have stored.
//
// It holds nothing of how a copy is logged. A copy has the role it asks before it takes the
// issue's place (takeRunningCopy, takeRunningCopyOf), so one with no role to ask never takes the
// place, and each entry holds the order of the issue's copies alone: the place, the copy owed
// behind it and whom that copy asks and answers, the push that overtook it, the retry a copy that
// came to nothing beside it owes, the start the next period runs from, and the retry its last
// copy owes.
type runningCopies struct {
	mu     sync.Mutex
	issues map[string]runningCopy
	run    context.Context
	// startOwed starts the copy of an issue that was owed while a release's save held the issue's
	// place, once the save gives the place back (hold). It is the daemon's own start of a copy
	// (workflowRuntime.startOwedCopy), set where the daemon attaches the cadence (attach), because
	// the save runs in the outbox, which holds this memory and nothing that starts a copy. nil
	// starts nothing, which is a daemon whose runtime bundles no workspace and so owes no copy.
	startOwed func(owed owedCopy)
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
func (c *runningCopies) startsOwedWith(start func(owed owedCopy)) {
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
	// owed is the copy of the issue asked for while the one in flight was being built and so
	// still to be taken, nil while none is: that copy read the workspace before the work which
	// asked for this one was written, so it holds none of that work.
	//
	// It is one copy and not a queue on purpose. Six roles ending their turns while one copy is
	// being built owe one copy between them and not six, because the copy taken after the one in
	// flight reads the workspace as it stands then and so holds the work of all six turns; a
	// queue would buy nothing but one `git bundle create` per turn end in a workspace a role is
	// working in. Whom that one copy asks, and whom it answers once it has ended, is what it
	// carries of the asks it stands for (owedCopy).
	owed *owedCopy
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

// owedCopy is the one copy of an issue owed behind the copy in flight or a release's save
// (runningCopy.owed): the claim of the role it asks, and the end of every turn whose own copy it
// now stands for, each told once it has ended.
//
// It asks the role of the last turn's end that owed it: the copy holds that turn's end, and a stop
// held for that end waits for it, so that role's process is the one no stop can take before the
// copy has asked it, and the last turn's end is the one whose wait has the most of its bound left.
// A copy owed only by the cadence or a push asks the role that caller chose, and a turn's end
// owing it afterwards takes it over; a push or the cadence owing it after a turn's end leaves it
// to that turn's role. The copy is handed its claim and reads no claims of its own, as the turn's
// end hands its own copy the claim whose turn ended (takeRunningCopyOf).
type owedCopy struct {
	// asks is the claim of the role the owed copy asks, which names the pod it would be taken off.
	asks supervise.Claim
	// finished is the end of each turn whose copy this one stands for (copyWorkspaceAtTurnEnd):
	// the machine's stop held for that end waits for it, and each is called once the owed copy
	// has ended, whatever it came to, or once it can no longer be started (startOwedCopy).
	finished []func()
}

// owing is owed with one more ask for the copy: asked's, whose turn's end, if it is one, is told
// through finished. A nil owed is the first ask.
func owing(owed *owedCopy, asked supervise.Claim, finished []func()) *owedCopy {
	if owed == nil {
		return &owedCopy{asks: asked, finished: finished}
	}
	if len(finished) > 0 || len(owed.finished) == 0 {
		owed.asks = asked
	}
	owed.finished = append(owed.finished, finished...)
	return owed
}

// tellCopied tells the end of each turn in finished that its copy has ended.
func tellCopied(finished []func()) {
	for _, f := range finished {
		f()
	}
}

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

// begin takes the issue's one place in flight for a copy asking asked's role, answering false when
// a copy of asked's issue or a release's save already holds it, and marking that refused copy
// owed so that the copy in flight takes it when it ends (done). The owed copy asks the role and
// answers the turn's ends the asks for it name (owing): finished is the end of the turn this copy
// is of, told once the copy that holds it has ended, and nil for a copy no turn's end asked for.
//
// A refused copy is owed and not dropped because the copy in flight read the workspace before
// the work that asked for this one was written. The copy which closes a turn is the case that
// decides it: the copy in flight may well be stored, the cadence takes no copy between turns of
// an issue whose last copy was stored, and no push need come to copy it, so a turn's end that was
// dropped would leave the newest copy of a pod whose roles have all gone idle short of the work
// its last turn ended with, and an eviction of that idle pod would then lose the end of the turn
// — which is exactly what the daemon holds itself to (R1).
func (c *runningCopies) begin(asked supervise.Claim, finished ...func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, took := c.take(asked.Issue)
	if !took {
		held.owed = owing(held.owed, asked, finished)
		c.issues[asked.Issue] = held
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
// place is given back, through the daemon's own start of a copy (startOwed), asking the role and
// answering the turn's ends it was owed with, and takes the place again as any copy does, whatever
// the save did. A save that stored no bundle — its bundler failed, the bundle was too large, its
// write failed, or the workspace held nothing unpushed — leaves the pod with no last copy of its
// own, and its release is cancelled or goes on with nothing to restore, so the owed copy is the
// one that holds the work that asked for it, which was written after whatever the issue's copy
// holds; dropping it would leave an eviction of the pod short of that work. A save that stored its
// bundle made that bundle the pod's last copy, and the copy then started, or retried, is refused
// by the one rule that says so, before any shim is asked and again at its write
// (takeRunningCopyOf, store.Store.ReleaseBundleSavedOff), so the give-back holds no rule of its
// own about it.
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
				if owed != nil && start != nil {
					start(*owed)
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
// the copy of the issue that is owed, nil for none: one was asked for while this one was in
// flight, and the caller takes it now, asking the role and answering the turn's ends it carries
// (owedCopy). Only the caller whose begin answered true calls it.
//
// The owed copy it reports is cleared by the reading: the copy the caller then starts takes the
// issue's place in flight again and is owed another only if a third is asked for while it runs,
// so a storm of turn ends is one copy after the one in flight rather than one copy per end.
func (c *runningCopies) done(issue string, retry bool) *owedCopy {
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
