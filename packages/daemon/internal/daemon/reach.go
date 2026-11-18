package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fleetalert"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/stream"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The bounds of R5 and R6 the keeper counts beside the 20 minutes a session may go unregistered
// (supervise.UnreachableAfter): the guard's refusal that becomes a case for a person, the wait
// after a relaunch before one that did not bring the claim back is, and the 20 minutes of failed
// reads or of a list with no live session that write R6's line.
const (
	guardHeldAfter = 60 * time.Minute
	notBackAfter   = 20 * time.Minute
	silentListFor  = 20 * time.Minute
)

// The causes the two relaunches record on the claim (supervise.RequestRelaunch.Cause), each read
// after "because" in the cut-turn note the task the claim owed comes back behind.
const (
	relaunchedByController = "Envoy could not reach its session for 20 minutes, so the project's controller relaunched it on the same session"
	relaunchedByDaemon     = "Envoy could not reach its session for 20 minutes, so the daemon relaunched it on the same session"
)

// reachKeeper is the daemon's reading of which claims Envoy cannot reach (R5), and what it tells
// and does about them. Once a minute, on the orphan sweep that already reads every claim, it reads
// Envoy's whole session list with its unresponsive sessions (notify.HTTPPublisher.ListSessions) and
// judges each claim's session from the last registration it has read of it: the listener's bucket
// forgets a session after 5 minutes, so the keeper keeps that time itself (seen), for each session
// a watched claim records.
//
// A claim is out of reach when the latest of its session's last registration, the daemon's boot,
// its session's first ready (supervise.Reach.FirstReady), the end of the keeper's last failed read
// and its last read that found no live session in the list is more than 20 minutes old. Counting
// from the boot, a failed read and an empty list keeps a daemon outage, a listener outage and its
// end from marking every claim at once; counting from the first ready of the session keeps a
// relaunch, which keeps the session, from counting the 20 minutes again. The mark is the claim's
// reach record (supervise.Reach), which the claim's own row holds and the state shows, and which
// changes no state, so every rule that reads the state reads it as before.
//
// The record is kept in memory, seeded from what the store read of each claim, and written
// through to the store at each change (store's PutClaimReach): the sweep and the `relaunch` route
// are its only writers, and both write under mu, so neither writes over the other's change. A
// write the store refuses is written again at the next sweep (dirty).
//
// Two facts of it are written with the act they record rather than after it, so no end of the
// daemon and no refused write between the two can lose one or repeat the other. A notice is
// written with the record that marks it told — Reported, ArchitectTold, ReadyTold, HeldTold,
// NotBackTold or ExitTold — in one transaction (reachMark, tell and enqueue). A relaunch is
// written by the launch it makes, onto the claim's own row (supervise.Reach.LastRelaunch), and
// the keeper takes it into its record from the claim at its next read of it
// (relaunchTakenIn), whether this daemon or the next made it.
type reachKeeper struct {
	log              *slog.Logger
	project          string
	boot             time.Time
	daemonController bool
	now              func() time.Time
	list             func(context.Context) ([]notify.ListedSession, error)
	put              func(context.Context, claim.Token, supervise.Reach) (bool, error)
	// tell queues a controller notice on an issue (admit.Admission.TellControllerQueued) and,
	// where it queued it, writes each of its marks in the same transaction, answering whether it
	// queued it (workflowRuntime.tellControllerQueued); enqueue writes an architect's notice row
	// and its marks in one transaction (workflowRuntime.enqueueReachNotice). Both are nil in a
	// deployment with no workflow, which tells nobody.
	tell    func(context.Context, record.ControllerNotice, string, []reachMark) (bool, error)
	enqueue func(context.Context, string, record.Notice, []reachMark) error
	alerts  *fleetalert.Writer
	// relaunchController is the daemon's own relaunch of its controller's claim at a moment of
	// the keeper's clock, which may replace that claim's pod, answering the claim as the relaunch
	// left it; nil where the operator runs the controller.
	relaunchController func(context.Context, supervise.Claim, time.Time) (supervise.Claim, error)
	// step reads the step record the store holds for a claim (store's ClaimStep); nil reads the
	// record the claim carries (stepOf).
	step func(context.Context, claim.Token) (supervise.StepRecord, error)

	wg sync.WaitGroup

	mu      sync.Mutex
	records map[claim.Token]supervise.Reach
	dirty   map[claim.Token]bool
	seen    map[string]time.Time
	// sessions is each session of the last list that answered, with the Envoy roles it holds.
	sessions map[string]notify.ListedSession
	// failingSince is the first of a run of failed reads, and failedAt the end of the last failed
	// read; noLiveSince is the first of a run of reads that found no live session, noLiveAt the
	// last such read, and noLiveTold whether the controller has been told of that run.
	failingSince, failedAt time.Time
	noLiveSince, noLiveAt  time.Time
	noLiveTold             bool
	// relaunching is the controller's claim while the daemon's relaunch of it is in flight.
	relaunching claim.Token
	// lockOuts is each lock-out notice the controller's lane has not queued yet, by claim
	// (tellLockOut).
	lockOuts map[claim.Token]untoldLockOut

	// notices is the store of the project's own notice records (store.ReachProjectNotice): the
	// notice that Envoy lists no live session and each claim's lock-out notice, each with whether
	// it was told, which a notice's told record is written beside in its own transaction; nil
	// keeps them in this daemon's memory alone. lockedOut is the worker stream's open lock-out of
	// a claim (stream.Listener.LockedOut); nil reads none open.
	notices   reachNotices
	lockedOut func(claim.Token) (stream.LockOut, bool)
	// loaded is whether this daemon has read the project's notice records yet (loadNotices).
	// lockOutTold is the two generations of the lock-out each claim's told notice named, and
	// noLiveUncleared whether the record of a no-live run that was told still waits for its
	// delete, a read having found a live session since.
	loaded          bool
	lockOutTold     map[claim.Token][2]uint64
	noLiveUncleared bool
}

// reachNotices is the store of the project's own notice records (reachKeeper.notices).
type reachNotices interface {
	ReachProjectNotices(ctx context.Context, project string) ([]store.ReachProjectNotice, error)
	PutReachProjectNotice(ctx context.Context, n store.ReachProjectNotice) error
	DeleteReachProjectNotice(ctx context.Context, project, kind string, token claim.Token) error
}

// reachMark is a record a notice marks told, written in the notice's own transaction: a claim's
// reach record, or for a notice that speaks for no claim's reach record the project's own record
// of it (project). A daemon that ends after the notice commits never tells it again, and a notice
// that does not commit has marked nothing.
type reachMark struct {
	token   claim.Token
	reach   supervise.Reach
	project *store.ReachProjectNotice
}

// untoldLockOut is a lock-out notice the controller's lane did not queue, of the lock-out that
// names generations newer and older, told again at each sweep while the worker stream still
// refuses that newer process for that older one (tellLockOut).
type untoldLockOut struct {
	notice       record.ControllerNotice
	issue        string
	newer, older uint64
}

// reachKeeperOptions are what a keeper is built from (reachKeeper says what each is).
type reachKeeperOptions struct {
	log                *slog.Logger
	project            string
	boot               time.Time
	daemonController   bool
	now                func() time.Time
	list               func(context.Context) ([]notify.ListedSession, error)
	put                func(context.Context, claim.Token, supervise.Reach) (bool, error)
	tell               func(context.Context, record.ControllerNotice, string, []reachMark) (bool, error)
	enqueue            func(context.Context, string, record.Notice, []reachMark) error
	alerts             *fleetalert.Writer
	relaunchController func(context.Context, supervise.Claim, time.Time) (supervise.Claim, error)
}

func newReachKeeper(o reachKeeperOptions) *reachKeeper {
	return &reachKeeper{
		log: o.log, project: o.project, boot: o.boot, daemonController: o.daemonController, now: o.now,
		list: o.list, put: o.put, tell: o.tell, enqueue: o.enqueue, alerts: o.alerts, relaunchController: o.relaunchController,
		records: map[claim.Token]supervise.Reach{}, dirty: map[claim.Token]bool{}, seen: map[string]time.Time{},
		lockOuts: map[claim.Token]untoldLockOut{}, lockOutTold: map[claim.Token][2]uint64{},
	}
}

// wait waits for the controller relaunch a sweep started, for the daemon's stop and for a test.
func (k *reachKeeper) wait() { k.wg.Wait() }

// registrationRead takes in a registration of session read outside the minute's list: the one the
// daemon's clean stop of a dead role's session reads just before it deletes it (sessionStop),
// which takes it out of Envoy's list before the next minute's read can see it. Like a listed one,
// it counts only when it is later than every registration of the session already read, so a
// claim whose session registered in the last 20 minutes is never marked.
func (k *reachKeeper) registrationRead(session string, lastSeen time.Time) {
	if k == nil || session == "" || lastSeen.IsZero() {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if lastSeen.After(k.seen[session]) {
		k.seen[session] = lastSeen
	}
}

// stepOf is the step record the store holds for c, the freshest durable reading of its worker
// stream that a relaunch's guard reads (supervise.RequestRelaunch.Step), and the record c carries
// where the keeper reads no store.
func (k *reachKeeper) stepOf(ctx context.Context, c supervise.Claim) (supervise.StepRecord, error) {
	if k.step == nil {
		return c.Step, nil
	}
	return k.step(ctx, c.Token)
}

// reachWatched is whether the keeper judges c at all: a workflow role's claim, or the project
// controller's where the daemon runs it, and nothing else.
func (k *reachKeeper) reachWatched(c supervise.Claim) bool {
	if c.Role == claim.RoleController {
		return k.daemonController
	}
	return claim.IsRole(c.Role)
}

// recordOf is the keeper's record of c, seeded from the store's on its first sight. The claim's
// last relaunch is the claim's own (supervise.Reach.LastRelaunch, LastRelaunchGeneration), and no
// part of the record, which takes it in from the claim (relaunchTakenIn). The caller holds mu.
func (k *reachKeeper) recordOf(c supervise.Claim) supervise.Reach {
	if held, ok := k.records[c.Token]; ok {
		return held
	}
	seeded := c.Reach
	seeded.LastRelaunch, seeded.LastRelaunchGeneration = time.Time{}, 0
	k.records[c.Token] = seeded
	return seeded
}

// relaunchTakenIn is rec once it has taken in c's last relaunch, as c's own row records it
// (supervise.Reach.LastRelaunch): a relaunch later than the one rec holds is the relaunch of the
// episode rec marks, if it marks one, which the not-back notice counts 20 minutes from and whose
// own launch is no exit of the claim's (UnreachableExitNotice), and the guard's refusals before
// it are over. The relaunch's launch wrote it, so it is read here whether this daemon made it or
// one that ended before its own write of rec. The launches are counted from the generation the
// relaunch's launch left the claim at (LastRelaunchGeneration), so a later launch of the claim,
// made before rec takes the relaunch in, is told as the exit it is; a relaunch whose launch is not
// recorded counts from the claim's generation as c holds it.
func relaunchTakenIn(c supervise.Claim, rec supervise.Reach) supervise.Reach {
	if !c.Reach.LastRelaunch.After(rec.RelaunchedAt) {
		return rec
	}
	rec.RelaunchedAt = c.Reach.LastRelaunch
	if rec.Unreachable() {
		launched := c.Generation
		if c.Reach.LastRelaunchGeneration != 0 {
			launched = c.Reach.LastRelaunchGeneration
		}
		rec.Relaunched, rec.Generation, rec.NotBackTold = true, launched, false
		rec.GuardRefusedAt, rec.ReadyTold, rec.HeldTold = time.Time{}, false, false
	}
	return rec
}

// store keeps next as token's record, and writes it through when it changed or an earlier write
// of it was refused, answering the store's refusal: the record stays in memory and dirty, and the
// next sweep writes it again. The caller holds mu.
func (k *reachKeeper) store(ctx context.Context, token claim.Token, next supervise.Reach) error {
	if k.records[token] == next && !k.dirty[token] {
		return nil
	}
	k.records[token] = next
	if k.put == nil {
		return nil
	}
	known, err := k.put(ctx, token, next)
	if err != nil {
		k.dirty[token] = true
		return fmt.Errorf("write the reach record of %s: %w", token, err)
	}
	if !known {
		k.log.Info("reach: the store holds no claim to write a reach record onto", "claim", token)
	}
	delete(k.dirty, token)
	return nil
}

// storeEach is store for the sweep, which writes again at its next pass whatever the store
// refused now, and so logs the refusal and goes on. The caller holds mu.
func (k *reachKeeper) storeEach(ctx context.Context, token claim.Token, next supervise.Reach) {
	if err := k.store(ctx, token, next); err != nil && ctx.Err() == nil {
		k.log.Error("reach: the store would not write a claim's reach record; writing it again at the next sweep", "claim", token, "error", err)
	}
}

// marked takes the records marks wrote with their notice as the store now holds them. The
// caller holds mu.
func (k *reachKeeper) marked(marks []reachMark) {
	for _, mark := range marks {
		if mark.project != nil {
			k.projectTold(*mark.project)
			continue
		}
		k.records[mark.token] = mark.reach
		delete(k.dirty, mark.token)
	}
}

// projectTold takes in the project's own record of a notice, as the store holds it: the no-live
// run it says was told, or the lock-out of a claim its notice named. The caller holds mu.
func (k *reachKeeper) projectTold(n store.ReachProjectNotice) {
	if !n.Told {
		return
	}
	switch record.NoticeKind(n.Kind) {
	case record.EnvoyNoLiveSessionNotice:
		k.noLiveTold, k.noLiveUncleared = true, false
	case record.WorkerLockedOutNotice:
		k.lockOutTold[n.Claim] = [2]uint64{n.Newer, n.Older}
		delete(k.lockOuts, n.Claim)
	}
}

// projectRecord is the project's own record of notice, a notice that speaks for no claim's reach
// record, told or kept untold: of claim token ("" for the no-live notice), naming the lock-out of
// generations newer and older (none for the no-live notice), and queued on issue. It is nil, and
// marks nothing, for a notice that does not encode.
func (k *reachKeeper) projectRecord(notice record.ControllerNotice, issue string, token claim.Token, newer, older uint64, told bool) []reachMark {
	encoded, err := json.Marshal(notice)
	if err != nil {
		k.log.Error("reach: a notice did not encode, so its record is not written with it", "kind", notice.Kind, "error", err)
		return nil
	}
	return []reachMark{{project: &store.ReachProjectNotice{Project: k.project, Kind: string(notice.Kind), Claim: token,
		Newer: newer, Older: older, Issue: issue, Notice: encoded, Told: told}}}
}

// loadNotices reads the project's own notice records, once, and answers whether they are read:
// the no-live run the last daemon told, the lock-out each claim's told notice named, and each
// lock-out notice kept untold, which this daemon tells while its worker stream still refuses the
// same newer process (retellLockOuts). Until the records are read, no notice they mark is told,
// since whether one was told is not known; a read that fails is made again at the next sweep.
// The caller holds mu.
func (k *reachKeeper) loadNotices(ctx context.Context) bool {
	if k.loaded {
		return true
	}
	if k.notices == nil {
		k.loaded = true
		return true
	}
	records, err := k.notices.ReachProjectNotices(ctx, k.project)
	if err != nil {
		if ctx.Err() == nil {
			k.log.Error("reach: the project's notice records could not be read; no notice they mark is told until they are", "error", err)
		}
		return false
	}
	for _, n := range records {
		if n.Told {
			k.projectTold(n)
			continue
		}
		if record.NoticeKind(n.Kind) != record.WorkerLockedOutNotice {
			continue
		}
		var notice record.ControllerNotice
		if err := json.Unmarshal(n.Notice, &notice); err != nil {
			k.log.Error("reach: a kept lock-out notice did not decode; it is told no more", "claim", n.Claim, "error", err)
			continue
		}
		if _, kept := k.lockOuts[n.Claim]; !kept {
			k.lockOuts[n.Claim] = untoldLockOut{notice: notice, issue: n.Issue, newer: n.Newer, older: n.Older}
		}
	}
	k.loaded = true
	return true
}

// clearNoLive deletes the record of a no-live run that was told, a read having found a live
// session, which ends that run; a delete the store refuses is made again at the next read that
// finds one. The caller holds mu.
func (k *reachKeeper) clearNoLive(ctx context.Context) {
	if k.notices == nil {
		return
	}
	if err := k.notices.DeleteReachProjectNotice(ctx, k.project, string(record.EnvoyNoLiveSessionNotice), ""); err != nil {
		k.noLiveUncleared = true
		if ctx.Err() == nil {
			k.log.Error("reach: the record of a told no-live run was not deleted; deleting it again at the next live read", "error", err)
		}
		return
	}
	k.noLiveUncleared = false
}

// sweep is one minute's read of Envoy's list and judgement of every watched claim of claims.
//
// A read that fails changes no mark and sends no notice: it says nothing of any session. Its end
// is one of the moments the 20 minutes are counted from, and reads that fail for 20 minutes write
// R6's line once, until a read answers (fleetalert.SessionListUnreadable). A list that holds no
// live session — Legion's or any other — marks none: its read is counted from as well, and after
// 20 minutes of such reads the controller is told once a run, written with the project's record
// that it was told, so the next daemon tells that run no more, and R6's line is written once
// (record.EnvoyNoLiveSessionNotice, fleetalert.NoLiveSession). A read that finds a live session
// ends the run and deletes its record.
//
// The project's notice records are read first, at the first sweep that can, and each lock-out
// notice the controller's lane has not queued yet is told again, whatever the list answers, since
// it is no reading of the list (tellLockOut). R6's line of each lock-out the worker stream no
// longer holds is ended too: a line the last daemon opened names a connection that ended with it,
// and this daemon's stream tells that older process to stop at its hello rather than binding it.
func (k *reachKeeper) sweep(ctx context.Context, claims []supervise.Claim) {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.retellLockOuts(ctx)
	k.mu.Unlock()
	k.alerts.CloseEnded(fleetalert.NewerProcessRefused, func(token string) bool {
		_, locked := k.lockOutHeld(claim.Token(token))
		return locked
	})
	if k.list == nil {
		return
	}
	sessions, err := k.list(ctx)
	now := k.now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil {
		if k.failingSince.IsZero() {
			k.failingSince = now
		}
		k.failedAt = now
		if now.Sub(k.failingSince) >= silentListFor {
			k.alerts.Open(fleetalert.SessionListUnreadable, "", "since", k.failingSince, "error", err.Error())
		}
		if ctx.Err() == nil {
			k.log.Warn("reach: Envoy's session list could not be read; no claim's mark changes and nobody is told", "error", err)
		}
		return
	}
	k.failingSince = time.Time{}
	k.alerts.Close(fleetalert.SessionListUnreadable, "")
	// Envoy's list names every session of the fleet, about 1,100 a minute, and the keeper reads
	// only the sessions its watched claims record: the registration of any other is forgotten,
	// so seen and sessions hold no more sessions than the daemon has claims.
	recorded := make(map[string]bool, len(claims))
	for _, c := range claims {
		if c.Session != "" && k.reachWatched(c) {
			recorded[c.Session] = true
		}
	}
	maps.DeleteFunc(k.seen, func(session string, _ time.Time) bool { return !recorded[session] })
	live := false
	k.sessions = make(map[string]notify.ListedSession, len(recorded))
	for _, s := range sessions {
		live = live || s.Live()
		if !recorded[s.Session] {
			continue
		}
		if s.LastSeen.After(k.seen[s.Session]) {
			k.seen[s.Session] = s.LastSeen
		}
		k.sessions[s.Session] = s
	}
	if live {
		if k.noLiveTold || k.noLiveUncleared || !k.loaded {
			k.clearNoLive(ctx)
		}
		k.noLiveSince, k.noLiveTold = time.Time{}, false
		k.alerts.Close(fleetalert.NoLiveSession, "")
	} else {
		if k.noLiveSince.IsZero() {
			k.noLiveSince = now
		}
		k.noLiveAt = now
		if now.Sub(k.noLiveSince) >= silentListFor {
			k.alerts.Open(fleetalert.NoLiveSession, "", "since", k.noLiveSince)
			if !k.noLiveTold && k.loadNotices(ctx) && !k.noLiveTold {
				notice := record.ControllerNotice{Kind: record.EnvoyNoLiveSessionNotice,
					Reason: fmt.Sprintf("Envoy's session list has held no live session since %s, 20 minutes, so no agent of the fleet can hear anything; the daemon marks no claim out of reach while it holds none",
						stamp(k.noLiveSince))}
				k.tellController(ctx, notice, k.project, k.projectRecord(notice, k.project, "", 0, 0, true)...)
			}
		}
	}
	var listed []supervise.Claim
	for _, c := range claims {
		if !k.reachWatched(c) {
			continue
		}
		next, newly := k.judge(ctx, c, now)
		if newly {
			listed = append(listed, c)
		}
		k.storeEach(ctx, c.Token, next)
	}
	k.report(ctx, listed)
	for token := range k.dirty {
		k.storeEach(ctx, token, k.records[token])
	}
}

// judge is one claim's record after this minute's read, and whether the claim is newly out of
// reach and not yet reported, which the caller lists in one notice. The caller holds mu.
func (k *reachKeeper) judge(ctx context.Context, c supervise.Claim, now time.Time) (supervise.Reach, bool) {
	rec := relaunchTakenIn(c, k.recordOf(c))
	controller := c.Role == claim.RoleController
	if controller && rec.Relaunched && c.Session != "" && c.Session == rec.Session && c.State == supervise.StateFailed {
		// The daemon's relaunch of its own controller has not brought it back: a controller
		// whose path to Envoy stays blocked fails each boot's Envoy claim and exits, and its
		// claim fails once its launch failures run out, on the same session. The episode lasts
		// until the controller is back (controllerBack), so R6's line says, 20 minutes after the
		// relaunch, that the relaunch did not bring it back.
		return k.judgeController(ctx, c, rec, now), false
	}
	if c.Session == "" || !holdsEpisode(c.State) {
		// The claim no longer records a session a process runs on: suspended, failed, retired,
		// or between launches with no session yet. Its episode is over.
		if rec.Session != "" && rec != rec.EndEpisode() {
			k.log.Info("reach: the claim no longer runs on the session it was judged on; its mark is over", "claim", c.Token, "state", c.State)
		}
		if controller {
			k.controllerReachable(c)
		}
		return rec.EndEpisode(), false
	}
	if rec.Session != c.Session {
		rec = supervise.Reach{Session: c.Session, RelaunchedAt: rec.RelaunchedAt}
	}
	if rec.FirstReady.IsZero() {
		if !supervise.SaidReady(c.State) || c.LastReady.IsZero() {
			// This session has not said ready yet: nothing is counted for it.
			return rec, false
		}
		rec.FirstReady = c.LastReady
	}
	lastSeen := k.seen[c.Session]
	if !lastSeen.IsZero() && now.Sub(lastSeen) <= supervise.UnreachableAfter {
		if controller && rec.Relaunched && !k.controllerBack(c, rec) {
			// A registration alone does not bring back the controller the daemon relaunched:
			// a process that registers and goes no further, or says ready and never takes the
			// controller's Envoy role, hears nothing as the controller (R6). The episode lasts,
			// so R6's line says, 20 minutes after the relaunch, that it did not bring it back.
			return k.judgeController(ctx, c, rec, now), false
		}
		if rec.Unreachable() || rec.Reported || rec.Relaunched {
			k.log.Info("reach: the claim's session registered with Envoy again; it is reachable", "claim", c.Token, "lastSeen", lastSeen)
		}
		if controller {
			k.controllerReachable(c)
		}
		return rec.EndEpisode(), false
	}
	// The boot, the session's first ready, a failed read's end and a read of a list with no live
	// session each hold back a mark not made yet; a mark already made stays as it is, since none of
	// them is a registration of the claim's session (a read that fails changes no mark).
	countedFrom := latest(lastSeen, k.boot, rec.FirstReady, k.failedAt, k.noLiveAt)
	if !rec.Unreachable() && now.Sub(countedFrom) <= supervise.UnreachableAfter {
		return rec, false
	}
	if !rec.Unreachable() {
		if !supervise.SaidReady(c.State) {
			// A mark is made of a settled agent; once made it holds in every launch state of the
			// session (holdsEpisode).
			return rec, false
		}
		rec.Since, rec.LastSeen, rec.Generation = countedFrom, lastSeen, c.Generation
		k.log.Warn("reach: the claim's session has not registered with Envoy for 20 minutes; it is out of reach, and keeps its state",
			"claim", c.Token, "state", c.State, "since", countedFrom, "lastSeen", lastSeen)
	}
	if controller {
		return k.judgeController(ctx, c, rec, now), false
	}
	// Every marked claim not yet reported is listed, so one whose list the outbox did not take is
	// listed again at this sweep.
	return k.tellOfMarked(ctx, c, rec, now), !rec.Reported
}

// tellOfMarked sends the per-claim notices of a marked claim that are due (each once): the guard
// that refused its relaunch would now let it through, or has refused it for 60 minutes; a relaunch
// reached it and it is still out of reach 20 minutes later; or it owes no task and its process
// was launched again since the mark, which is a process that exits at its Envoy role claim. The
// architect is told of the claim here too, once an episode, beside the controller's list. Each
// notice is written with the record that marks it told (tellOf), and one the outbox did not take
// leaves the record as it was, to be told at the next sweep: an exit's record does not move to
// its launch until the exit is told, so a refused notice is not lost with the launch it was of.
// The caller holds mu.
func (k *reachKeeper) tellOfMarked(ctx context.Context, c supervise.Claim, rec supervise.Reach, now time.Time) supervise.Reach {
	if !rec.ArchitectTold {
		rec.ArchitectTold = k.tellArchitect(ctx, c, rec)
	}
	if c.Generation > rec.Generation {
		if supervise.HoldsTask(c.Pending) || c.State == supervise.StateWorking || rec.ExitTold {
			rec.Generation = c.Generation
		} else {
			rec = k.tellOf(ctx, c, rec, record.UnreachableExitNotice, fmt.Sprintf(
				"%s is out of reach of Envoy and owes no task, and its process ended and was launched again on the same session at generation %d: a process that exits at its Envoy role claim. Each launch clears its activity report and exits again, so relaunch would add nothing to the daemon's own. This is a case for a person.",
				c.Token, c.Generation), func(told *supervise.Reach) { told.Generation, told.ExitTold = c.Generation, true })
		}
	}
	guard := supervise.RelaunchGuard(c, time.Time{}, now)
	if !rec.GuardRefusedAt.IsZero() && !rec.ReadyTold {
		switch {
		case guard == "":
			rec = k.tellOf(ctx, c, rec, record.RelaunchReadyNotice, fmt.Sprintf(
				"The relaunch guard refused to relaunch %s at %s, and would now let it through: its worker stream shows no step for 20 minutes, its session has sent no activity report for 20 minutes in a turn it is still in, its open call is none or a wait, and its last activity report shows nothing running. relaunch it again.",
				c.Token, stamp(rec.GuardRefusedAt)), func(told *supervise.Reach) { told.ReadyTold = true })
		case !rec.HeldTold && now.Sub(rec.GuardRefusedAt) >= guardHeldAfter:
			rec = k.tellOf(ctx, c, rec, record.RelaunchGuardHeldNotice, fmt.Sprintf(
				"The relaunch guard has refused to relaunch %s since %s, 60 minutes: %s. A process that still steps or reports in its turn runs while Envoy cannot reach it, and a frozen process sends nothing newer than the report that refuses it, so the guard will not let it through. This is a case for a person.",
				c.Token, stamp(rec.GuardRefusedAt), guard), func(told *supervise.Reach) { told.HeldTold = true })
		}
	}
	if rec.Relaunched && !rec.NotBackTold && now.Sub(rec.RelaunchedAt) >= notBackAfter {
		rec = k.tellOf(ctx, c, rec, record.RelaunchNotBackNotice, fmt.Sprintf(
			"relaunch reached %s at %s, and its session has not registered with Envoy in the 20 minutes since: the cause is in its pod or its connection, not in its process. This is a case for a person.",
			c.Token, stamp(rec.RelaunchedAt)), func(told *supervise.Reach) { told.NotBackTold = true })
	}
	return rec
}

// tellOf tells the controller the per-claim notice of kind about c, written with rec as told marks
// it, in one transaction (reachMark), and answers the record as it then stands: the marked one
// once the notice is queued, and rec as it was otherwise, to be told at the next sweep. The caller
// holds mu.
func (k *reachKeeper) tellOf(ctx context.Context, c supervise.Claim, rec supervise.Reach, kind record.NoticeKind, reason string, told func(*supervise.Reach)) supervise.Reach {
	marked := rec
	told(&marked)
	if k.tellController(ctx, k.claimNotice(kind, c, reason), c.Issue, reachMark{token: c.Token, reach: marked}) {
		return marked
	}
	return rec
}

// judgeController is the daemon's own relaunch of its marked controller (`controller: daemon`):
// nobody else can relaunch it, since the controller is the one that would decide. It relaunches
// it at most once in 6 hours, under the same guard as `relaunch`, replacing the controller's pod
// where the launch needs it or its launcher is not connected (supervise.RequestRelaunch's
// MayReplacePod). The controller hears nothing of itself, since it is the one out of reach, so
// each case nobody else can act on writes R6's line instead, once until the controller is
// reachable again: the relaunch did not bring it back within 20 minutes, it is out of reach a
// second time within the 6 hours, or the guard refused the relaunch or its process outlived the
// stop. A refused relaunch uses no part of the 6 hours. The relaunch it made is the one the
// controller's claim records (supervise.Reach.LastRelaunch), which the record has taken in by
// now (relaunchTakenIn), so a daemon that ended before its own write of the record does not
// relaunch the controller a second time within the 6 hours. The caller holds mu.
func (k *reachKeeper) judgeController(ctx context.Context, c supervise.Claim, rec supervise.Reach, now time.Time) supervise.Reach {
	switch {
	case rec.Relaunched:
		if now.Sub(rec.RelaunchedAt) >= notBackAfter && !rec.NotBackTold {
			k.alerts.Open(fleetalert.ControllerUnreachable, string(c.Token), "relaunchedAt", rec.RelaunchedAt,
				"reason", "the daemon's relaunch did not bring it back: it has not registered with Envoy in the 20 minutes since")
			rec.NotBackTold = true
		}
	case !rec.RelaunchedAt.IsZero() && now.Sub(rec.RelaunchedAt) < supervise.RelaunchAgainAfter:
		k.alerts.Open(fleetalert.ControllerUnreachable, string(c.Token), "relaunchedAt", rec.RelaunchedAt,
			"reason", "it is out of reach a second time within 6 hours of the daemon's relaunch of it, which is made once in 6 hours")
	case k.relaunching == c.Token || k.relaunchController == nil:
	default:
		if why := supervise.RelaunchGuard(c, time.Time{}, now); why != "" {
			k.alerts.Open(fleetalert.ControllerRelaunchRefused, string(c.Token), "reason", "the relaunch guard refuses it: "+why)
			return rec
		}
		k.relaunching = c.Token
		k.wg.Add(1)
		go k.relaunchOwnController(ctx, c, now)
	}
	return rec
}

// relaunchOwnController runs the daemon's relaunch of its controller off the sweep, since a
// Sandbox relaunch waits minutes on its pods, and records what it did. The relaunch's launch has
// written the relaunch onto the controller's claim, so a write of the record the store refuses
// here is written again at the next sweep and costs no part of the 6 hours.
func (k *reachKeeper) relaunchOwnController(ctx context.Context, c supervise.Claim, at time.Time) {
	defer k.wg.Done()
	relaunched, err := k.relaunchController(ctx, c, at)
	k.mu.Lock()
	defer k.mu.Unlock()
	k.relaunching = ""
	switch {
	case err == nil:
		k.log.Warn("reach: the daemon relaunched its own controller, which Envoy could not reach, on its same session", "claim", c.Token, "generation", relaunched.Generation)
		k.alerts.Close(fleetalert.ControllerRelaunchRefused, string(c.Token))
		k.storeEach(ctx, c.Token, relaunchTakenIn(relaunched, k.recordOf(c)))
	case errors.Is(err, supervise.ErrRelaunchGuard), errors.Is(err, runtime.ErrProcessOutlivedStop):
		k.alerts.Open(fleetalert.ControllerRelaunchRefused, string(c.Token), "reason", err.Error())
	default:
		if ctx.Err() == nil {
			k.log.Error("reach: the daemon's relaunch of its own controller failed; it is tried again at the next sweep", "claim", c.Token, "error", err)
		}
	}
}

// controllerReachable ends the controller's open lines, its episode being over. The caller holds mu.
func (k *reachKeeper) controllerReachable(c supervise.Claim) {
	k.alerts.Close(fleetalert.ControllerUnreachable, string(c.Token))
	k.alerts.Close(fleetalert.ControllerRelaunchRefused, string(c.Token))
}

// controllerBack is whether the daemon's own controller c, which a relaunch reached in the
// episode rec marks, is back from that relaunch (R6): it said ready after the relaunch, and its
// session holds the controller's Envoy role in the last list read. The caller holds mu.
func (k *reachKeeper) controllerBack(c supervise.Claim, rec supervise.Reach) bool {
	return supervise.SaidReady(c.State) && c.LastReady.After(rec.RelaunchedAt) &&
		k.sessions[c.Session].HoldsRole(string(c.Token))
}

// report tells the controller, in one notice, of every claim newly out of reach this minute, and
// marks each reported in the notice's own transaction: a list the wake did not queue — no
// controller registered, or an earlier list still waiting unsent — marks none, and leaves its
// claims to be listed at the next sweep. The caller holds mu.
func (k *reachKeeper) report(ctx context.Context, listed []supervise.Claim) {
	if len(listed) == 0 {
		return
	}
	rows := make([]record.UnreachableClaim, 0, len(listed))
	marks := make([]reachMark, 0, len(listed))
	for _, c := range listed {
		rec := k.records[c.Token]
		rows = append(rows, record.UnreachableClaim{Claim: string(c.Token), Issue: c.Issue, Role: c.Role,
			State: string(c.State), LastSeen: rec.LastSeen})
		rec.Reported = true
		marks = append(marks, reachMark{token: c.Token, reach: rec})
	}
	k.tellController(ctx, record.ControllerNotice{Kind: record.UnreachableClaimsNotice, Unreachable: rows}, k.project, marks...)
}

// tellArchitect tells the architect of c's tree that c is out of reach, written with rec marked
// told in the notice's own transaction, and answers whether it did: a notice of the claim on its
// issue, which the owner walk takes to the architect that owns the issue, or for a child's own
// architect on its tree's root. The tree architect's own claim is told to nobody here, since it
// could not hear it, and the controller's list names it. The notice asks nothing, since the
// controller decides the relaunch. The caller holds mu.
func (k *reachKeeper) tellArchitect(ctx context.Context, c supervise.Claim, rec supervise.Reach) bool {
	if k.enqueue == nil || c.Issue == "" || claim.IsTreeArchitect(c.Role, c.Issue, c.Tree) {
		return true
	}
	issue := c.Issue
	if c.Role == claim.RoleArchitect {
		issue = c.Tree
	}
	notice := record.Notice{Kind: record.ClaimUnreachableNotice, Role: c.Role, Claim: string(c.Token), Reason: fmt.Sprintf(
		"%s's session has not registered with Envoy since %s, more than 20 minutes, so it cannot hear its notices. Its claim keeps its state. The project's controller decides whether to relaunch it; nothing is asked of you.",
		c.Token, stamp(rec.Since))}
	rec.ArchitectTold = true
	marks := []reachMark{{token: c.Token, reach: rec}}
	if err := k.enqueue(ctx, issue, notice, marks); err != nil {
		if ctx.Err() == nil {
			k.log.Error("reach: the architect's notice of an unreachable claim was not written; writing it again at the next sweep", "claim", c.Token, "error", err)
		}
		return false
	}
	k.marked(marks)
	return true
}

// claimNotice is a per-claim controller notice of kind about c.
func (k *reachKeeper) claimNotice(kind record.NoticeKind, c supervise.Claim, reason string) record.ControllerNotice {
	return record.ControllerNotice{Kind: kind, Role: c.Role, Claim: string(c.Token), Reason: reason}
}

// tellController queues notice on issue, with the records marks says it marks told written in the
// same transaction, and answers whether it queued it: false for a deployment with no workflow, a
// controller not registered, a notice of the same kind still waiting, and a store that refused
// either write, each of which marks nothing and which the caller tells again at a later sweep.
// The caller holds mu.
func (k *reachKeeper) tellController(ctx context.Context, notice record.ControllerNotice, issue string, marks ...reachMark) bool {
	if k.tell == nil {
		return false
	}
	told, err := k.tell(ctx, notice, issue, marks)
	if err != nil {
		if ctx.Err() == nil {
			k.log.Error("reach: a controller notice was not written; telling it again at the next sweep", "kind", notice.Kind, "error", err)
		}
		return false
	}
	if told {
		k.marked(marks)
	}
	return told
}

// tellLockOut tells the project's controller of the lock-out out (supervision.reportLockOut),
// written with the project's record that it was told, so no daemon tells the same lock-out — the
// same newer process refused for the same older one — twice: the next daemon's worker stream
// reports it again when the older process binds first again after a restart. A notice its lane did
// not queue — no controller registered, a notice of the same claim still waiting unsent, a store
// that refused it — is kept, in this daemon's memory and in the project's record, and told again
// at each sweep until it is queued or the lock-out has ended (retellLockOuts): the worker stream
// reports a lock-out once, so a notice not kept would be lost, and one of a lock-out that has
// ended names a process that is gone.
func (k *reachKeeper) tellLockOut(ctx context.Context, out stream.LockOut, notice record.ControllerNotice, issue string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	untold := untoldLockOut{notice: notice, issue: issue, newer: out.Newer, older: out.Older}
	k.lockOuts[out.Claim] = untold
	if !k.loadNotices(ctx) {
		// Whether it was told is not known until the records are read: a sweep tells it then.
		return
	}
	k.tellLockOutNow(ctx, out.Claim, untold, true)
}

// tellLockOutNow tells untold, the kept notice of token's lock-out, written with its told record.
// A notice of a lock-out already told is forgotten, and one the lane did not queue stays kept,
// written to the project's record as kept where keep says so. The caller holds mu.
func (k *reachKeeper) tellLockOutNow(ctx context.Context, token claim.Token, untold untoldLockOut, keep bool) {
	if k.lockOutTold[token] == [2]uint64{untold.newer, untold.older} {
		k.log.Info("reach: the controller was told of this lock-out already; it is not told again", "claim", token,
			"newer", untold.newer, "older", untold.older)
		delete(k.lockOuts, token)
		return
	}
	if k.tellController(ctx, untold.notice, untold.issue, k.projectRecord(untold.notice, untold.issue, token, untold.newer, untold.older, true)...) {
		return
	}
	if !keep || k.notices == nil {
		return
	}
	kept := k.projectRecord(untold.notice, untold.issue, token, untold.newer, untold.older, false)
	if len(kept) == 0 {
		return
	}
	if err := k.notices.PutReachProjectNotice(ctx, *kept[0].project); err != nil && ctx.Err() == nil {
		k.log.Error("reach: a lock-out notice the controller's lane did not queue was not kept in the project's record; this daemon still keeps it",
			"claim", token, "error", err)
	}
}

// retellLockOuts tells each kept lock-out notice again, once the project's records are read, and
// forgets each that is queued now or whose lock-out has ended: the worker stream no longer refuses
// that newer process for that older one (lockedOut), which is how a daemon that restarted finds a
// notice the last one kept, since every connection of the last daemon ended with it. The record
// of one that ended untold is deleted, since there is nothing to keep. The caller holds mu.
func (k *reachKeeper) retellLockOuts(ctx context.Context) {
	if !k.loadNotices(ctx) {
		return
	}
	for token, untold := range k.lockOuts {
		if !k.lockOutOpen(token, untold) {
			k.log.Info("reach: a lock-out ended before the controller's notice of it was queued; it is told no more", "claim", token)
			delete(k.lockOuts, token)
			if k.notices != nil {
				if err := k.notices.DeleteReachProjectNotice(ctx, k.project, string(record.WorkerLockedOutNotice), token); err != nil && ctx.Err() == nil {
					k.log.Error("reach: the record of a lock-out that ended untold was not deleted", "claim", token, "error", err)
				}
			}
			continue
		}
		k.tellLockOutNow(ctx, token, untold, false)
	}
}

// lockOutOpen is whether the worker stream still refuses the newer process untold names for the
// older one it names.
func (k *reachKeeper) lockOutOpen(token claim.Token, untold untoldLockOut) bool {
	open, locked := k.lockOutHeld(token)
	return locked && open.Newer == untold.newer && open.Older == untold.older
}

// lockOutHeld is the worker stream's open lock-out of token, if it holds one (stream's
// Listener.LockedOut); a keeper with no stream reads none open.
func (k *reachKeeper) lockOutHeld(token claim.Token) (stream.LockOut, bool) {
	if k.lockedOut == nil {
		return stream.LockOut{}, false
	}
	return k.lockedOut(token)
}

// guardRefused records that the relaunch guard refused token's relaunch at at: the first refusal
// of the episode, or the first after the controller was told the guard would let it through, which
// is a refusal of its own. It answers the store's refusal of the record, which the next sweep
// writes again: until it does, a daemon that ends loses the refusal the notices count from.
func (k *reachKeeper) guardRefused(ctx context.Context, token claim.Token, at time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	rec := k.records[token]
	if rec.GuardRefusedAt.IsZero() || rec.ReadyTold {
		rec.GuardRefusedAt, rec.ReadyTold, rec.HeldTold = at, false, false
	}
	return k.store(ctx, token, rec)
}

// relaunched takes the relaunch c's machine has just made into the keeper's record of c at once,
// rather than at the next sweep, so the notices that follow it count from it, and writes the
// record through. The relaunch's launch has written it onto the claim already
// (supervise.Reach.LastRelaunch), so the store's refusal, which it answers, costs no part of the
// 6-hour refusal, and the next sweep writes the record again.
func (k *reachKeeper) relaunched(ctx context.Context, c supervise.Claim) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.store(ctx, c.Token, relaunchTakenIn(c, k.recordOf(c)))
}

// runsOnSession is whether a claim in state runs, or is starting, a process on the session it
// records: every live state.
func runsOnSession(state supervise.ClaimState) bool {
	switch state {
	case supervise.StateLaunching, supervise.StateShimConnected, supervise.StateRegistered,
		supervise.StateReady, supervise.StateWorking, supervise.StateIdle:
		return true
	}
	return false
}

// holdsEpisode is whether a claim in state keeps the episode its reach record marks: every live
// state (runsOnSession), so a claim relaunched again and again on its session stays marked
// through its launches, and the two a launch waits in. A boot restores a claim the last daemon
// left mid-launch as launch_uncertain, and a claim with nothing launched yet is queued: neither
// has left the session it records, so a claim reported before the boot is not reported again
// after it, and a relaunch's not-back notice still counts from that relaunch (R5).
func holdsEpisode(state supervise.ClaimState) bool {
	return runsOnSession(state) || state == supervise.StateLaunchUncertain || state == supervise.StateQueued
}

// latest is the latest of moments.
func latest(moments ...time.Time) time.Time {
	var last time.Time
	for _, at := range moments {
		if at.After(last) {
			last = at
		}
	}
	return last
}

// stamp is a moment as a notice writes it.
func stamp(at time.Time) string { return at.UTC().Format(time.RFC3339) }
