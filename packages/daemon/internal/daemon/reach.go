package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fleetalert"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/timewords"
)

// The bounds of R5 and R6 the keeper counts beside the 20 minutes a session may go unregistered
// (supervise.UnreachableAfter): the guard's refusal of a workflow claim's relaunch that becomes a
// case for a person, the wait after a relaunch before one that did not bring the claim back is,
// and the 20 minutes of failed reads or of a list with no live session that write R6's line
// (fleetalert.ListSilentFor, which that line's sentence names).
const (
	guardHeldAfter = 60 * time.Minute
	notBackAfter   = 20 * time.Minute
	silentListFor  = fleetalert.ListSilentFor
)

// relaunchedByDaemon is the cause the daemon's relaunch records on the claim
// (supervise.RequestRelaunch.Cause), read after "because" in the cut-turn note the task the claim
// owed comes back behind.
var relaunchedByDaemon = "Envoy could not reach its session for " + timewords.Words(supervise.UnreachableAfter) +
	", so the daemon relaunched it on the same session"

// reachKeeper is the daemon's reading of which claims Envoy cannot reach (R5), and what it does
// about them. Once a minute, on the orphan sweep that already reads every claim, it reads Envoy's
// whole session list with its unresponsive sessions (notify.HTTPPublisher.ListSessions) and judges
// each claim's session from the last registration it has read of it: the listener's bucket
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
// The keeper relaunches each marked claim itself, on its same session, once in 6 hours and under
// the relaunch guard (relaunchMarked). What it cannot get past is a case for a person, which it
// writes as R6's line (fleetalert), since no agent can act on it; no agent is asked anything.
//
// The record is kept in memory, seeded from what the store read of each claim, and written
// through to the store at each change (store's PutClaimReach): the sweep and the relaunches it
// starts are its only writers, and both write under mu, so neither writes over the other's
// change. A write the store refuses is written again at the next sweep (dirty).
//
// Two facts of it are written with the act they record rather than after it, so no end of the
// daemon and no refused write between the two can lose one or repeat the other. The architect's
// notice is written with the record that marks it told (ArchitectTold) in one transaction
// (reachMark, enqueue). A relaunch is written by the launch it makes, onto the claim's own row
// (supervise.Claim.LastRelaunch), and the keeper takes it into its record from the claim at its
// next read of it (relaunchTakenIn), whether this daemon or the next made it.
type reachKeeper struct {
	reachKeeperOptions

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
	// relaunching is each claim whose relaunch by the daemon is in flight.
	relaunching map[claim.Token]bool
	// loaded is whether this daemon has read the project's notice records yet (loadNotices), and
	// noLiveUncleared whether the record of a no-live run that was told still waits for its
	// delete, a read having found a live session since.
	loaded          bool
	noLiveUncleared bool
}

// reachNotices is the store of the project's own notice records (reachKeeperOptions.notices).
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

// reachKeeperOptions are what a keeper is built from, every field of it that is not its own state.
// A daemon builds its keeper once, from all of them, and never sets one afterwards.
type reachKeeperOptions struct {
	log              *slog.Logger
	project          string
	boot             time.Time
	daemonController bool
	now              func() time.Time
	list             func(context.Context) ([]notify.ListedSession, error)
	put              func(context.Context, claim.Token, supervise.Reach) (bool, error)
	// reachNotify is the workflow's two writes the keeper tells through: the controller's notice
	// that Envoy lists no live session, with its mark (tell), and an architect's notice of a claim
	// of its tree out of reach, with its mark (enqueue), each in one transaction. Both are nil in
	// a deployment with no workflow, which tells nobody.
	reachNotify
	alerts *fleetalert.Writer
	// relaunch is the daemon's own relaunch of a marked claim on its same session at a moment of
	// the keeper's clock, which may replace the pod of the controller's claim alone, answering the
	// claim as the relaunch left it; nil relaunches nothing.
	relaunch func(context.Context, supervise.Claim, time.Time) (supervise.Claim, error)
	// step reads the step record the store holds for a claim (store's ClaimStep); nil reads the
	// record the claim carries (stepOf).
	step func(context.Context, claim.Token) (supervise.StepRecord, error)
	// notices is the store of the project's own notice records (store.ReachProjectNotice), which
	// holds whether the notice that Envoy lists no live session was told, written beside that
	// notice in its own transaction; nil keeps it in this daemon's memory alone.
	notices reachNotices
}

func newReachKeeper(o reachKeeperOptions) *reachKeeper {
	return &reachKeeper{reachKeeperOptions: o, records: map[claim.Token]supervise.Reach{},
		dirty: map[claim.Token]bool{}, seen: map[string]time.Time{}, relaunching: map[claim.Token]bool{}}
}

// wait waits for the relaunches a sweep started and for every record of R6's lines the writer
// still owes the store, for the daemon's stop and for a test.
func (k *reachKeeper) wait() {
	k.wg.Wait()
	k.alerts.Wait()
}

// registrationRead takes in a registration of session read outside the minute's list: the one the
// daemon's clean stop of a dead role's session reads just before it deletes it (sessionStop),
// which takes it out of Envoy's list before the next minute's read can see it. Like a listed one,
// it counts only when it is later than every registration of the session already read, so a
// claim whose session registered in the last 20 minutes is never marked.
func (k *reachKeeper) registrationRead(session string, lastSeen time.Time) {
	if session == "" || lastSeen.IsZero() {
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
// last relaunch is the claim's own (supervise.Claim.LastRelaunch, LastRelaunchGeneration), and no
// part of the record, which takes it in from the claim (relaunchTakenIn). The caller holds mu.
func (k *reachKeeper) recordOf(c supervise.Claim) supervise.Reach {
	if held, ok := k.records[c.Token]; ok {
		return held
	}
	k.records[c.Token] = c.Reach
	return c.Reach
}

// relaunchTakenIn is rec once it has taken in c's last relaunch, as c's own row records it
// (supervise.Claim.LastRelaunch): a relaunch later than the one rec holds is the relaunch of the
// episode rec marks, if it marks one, which R6's not-back line counts 20 minutes from, and the
// guard's refusals before it are over. The relaunch's launch wrote it, so it is read here whether
// this daemon made it or one that ended before its own write of rec.
func relaunchTakenIn(c supervise.Claim, rec supervise.Reach) supervise.Reach {
	if !c.LastRelaunch.After(rec.RelaunchedAt) {
		return rec
	}
	rec.RelaunchedAt = c.LastRelaunch
	if rec.Unreachable() {
		rec.Relaunched, rec.GuardRefusedAt = true, time.Time{}
	}
	return rec
}

// relaunchGeneration is the generation c's last relaunch counts its launches from: the one that
// relaunch's own launch recorded (LastRelaunchGeneration), or else, while that launch has recorded
// none, the claim's own generation as c holds it. That launch can land at a later generation than
// the first read of it: its own loop retries a refused start, a store that did not answer has it
// tried again at the probe interval, or a daemon that ended mid-start spawns the claim once more,
// and each try moves the claim's generation before any of them records it (carriesItsEpisode).
func relaunchGeneration(c supervise.Claim) uint64 {
	if c.LastRelaunchGeneration != 0 {
		return c.LastRelaunchGeneration
	}
	return c.Generation
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

// projectTold takes in the project's own record of the no-live notice, as the store holds it: the
// no-live run it says was told. The caller holds mu.
func (k *reachKeeper) projectTold(n store.ReachProjectNotice) {
	if n.Told && record.NoticeKind(n.Kind) == record.EnvoyNoLiveSessionNotice {
		k.noLiveTold, k.noLiveUncleared = true, false
	}
}

// loadNotices reads the project's own record of the no-live notice, once, and answers whether it
// is read: the no-live run the last daemon told. Until it is read, the no-live notice is not told,
// since whether it was told is not known; a read that fails is made again at the next sweep. The
// caller holds mu.
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
		k.projectTold(n)
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

// sweep is one minute's read of Envoy's list and judgement of every watched claim of claims: the
// reading of each first, then what the daemon does about each (judge), so a pod's claims are
// judged against each other's marks of the same minute.
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
// The project's notice record of the no-live run is read first, at the first sweep that can, and
// the record of a claim the sweep no longer reads is forgotten (forgetGone). The lock-out notices
// the minute sweep resends are no reading of the list and are kept apart (lockOutNotices.resend).
//
// R6's writer loads the lines the store holds open at the boot, and every sweep, the first after
// that load included, closes each line whose case does not hold now: the list's lines where no
// list is read, and at the sweep's end the claims' lines whose case does not hold
// (closeEndedClaimLines). A line the daemon before this one wrote is open in the store until its
// case ends, and that daemon may have run in another configuration, or been a release that writes
// no such line and so closes none. Left open, such a line would silence the next line of its case.
func (k *reachKeeper) sweep(ctx context.Context, claims []supervise.Claim) {
	k.mu.Lock()
	k.forgetGone(claims)
	if k.list == nil {
		// No list is read, so nothing judges the list's cases, nor any claim's: no claim's line
		// holds either, whatever record its claim carries.
		k.alerts.Close(fleetalert.NoLiveSession, "")
		k.alerts.Close(fleetalert.SessionListUnreadable, "")
		k.closeEndedClaimLines(nil)
		k.mu.Unlock()
		return
	}
	k.mu.Unlock()
	sessions, err := k.list(ctx)
	now := k.now()
	k.mu.Lock()
	defer k.mu.Unlock()
	// Run last, under mu, whether the read failed or every claim was judged.
	defer k.closeEndedClaimLines(claims)
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
		// The records are read once, at the first read that can, and only a run some daemon told
		// has a record to delete; a read of them the store refuses deletes the record unread,
		// which is the one way to end a run whose record cannot be read.
		if !k.loadNotices(ctx) || k.noLiveTold || k.noLiveUncleared {
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
					Reason: fmt.Sprintf("Envoy's session list has held no live session since %s, %s, so no agent of the fleet can hear anything; the daemon marks no claim out of reach while it holds none",
						stamp(k.noLiveSince), timewords.Words(silentListFor))}
				var marks []reachMark
				if n, ok := projectNotice(k.log, store.ReachProjectNotice{Project: k.project, Kind: string(notice.Kind), Issue: k.project, Told: true}, notice); ok {
					marks = []reachMark{{project: &n}}
				}
				k.tellController(ctx, notice, k.project, marks...)
			}
		}
	}
	type read struct {
		rec     supervise.Reach
		verdict reachVerdict
	}
	readings := make(map[claim.Token]read, len(claims))
	var watched []supervise.Claim
	for _, c := range claims {
		if !k.reachWatched(c) {
			continue
		}
		rec, verdict := k.reading(c, now)
		readings[c.Token] = read{rec, verdict}
		watched = append(watched, c)
	}
	// The pod reaches Envoy for another of its claims only while Envoy lists that claim's session
	// as live: a claim marked this minute does not, nor does one whose session has not registered
	// in the listener's live window, which goes out of reach with c when the pod's path to Envoy is
	// what broke, and is marked within the minute after c's mark.
	reaches := func(other supervise.Claim) bool {
		if r, ok := readings[other.Token]; ok && r.verdict == reachMarked {
			return false
		}
		return k.sessions[other.Session].Live()
	}
	pods := podsOf(claims)
	for _, c := range watched {
		r := readings[c.Token]
		k.storeEach(ctx, c.Token, k.judge(ctx, c, r.rec, r.verdict, pods.siblings(c), reaches, now))
	}
	for token := range k.dirty {
		k.storeEach(ctx, token, k.records[token])
	}
}

// closeEndedClaimLines closes each claim's line whose case does not hold once this sweep is over:
// every such line holds only while its claim is among claims, is one the keeper watches, and its
// record is marked out of reach. So a claim back in reach, or gone from the claims, holds none;
// nor does a controller under an operator, which the keeper does not watch, nor any where no list
// is read, whose caller passes no claims, since no read marks one. The record is the keeper's,
// seeded from the claim's stored one (recordOf), so a read that failed before any read answered,
// which leaves the keeper's memory empty, reads the case as the store holds it and closes no line
// of a case that still holds. The list's own lines need no such pass: every read that answers
// closes the unreadable list's line, and every list with a live session the no-live line. The
// caller holds mu.
func (k *reachKeeper) closeEndedClaimLines(claims []supervise.Claim) {
	marked := map[string]bool{}
	for _, c := range claims {
		if k.reachWatched(c) && k.recordOf(c).Unreachable() {
			marked[string(c.Token)] = true
		}
	}
	holds := func(token string) bool { return marked[token] }
	for _, c := range []fleetalert.Case{fleetalert.ControllerUnreachable, fleetalert.ControllerRelaunchRefused,
		fleetalert.ClaimUnreachable, fleetalert.ClaimRelaunchRefused} {
		k.alerts.CloseEnded(c, holds)
	}
}

// forgetGone forgets the record of each claim the daemon no longer holds, with any write of it
// still owed: claims is every claim the sweep read, so a claim that retired and was deleted leaves
// nothing behind in the keeper. The caller holds mu.
func (k *reachKeeper) forgetGone(claims []supervise.Claim) {
	held := make(map[claim.Token]bool, len(claims))
	for _, c := range claims {
		held[c.Token] = true
	}
	maps.DeleteFunc(k.records, func(token claim.Token, _ supervise.Reach) bool { return !held[token] })
	maps.DeleteFunc(k.dirty, func(token claim.Token, _ bool) bool { return !held[token] })
}

// reachVerdict is what one minute's reading says of a claim's reach (reading), the same for every
// role; what follows from it is the role's (judge).
type reachVerdict int

const (
	// reachOver: the claim no longer records a session a process runs on — suspended, failed,
	// retired, or between launches with no session yet.
	reachOver reachVerdict = iota
	// reachRegistered: its session has registered with Envoy in the last 20 minutes.
	reachRegistered
	// reachNotDue: no mark is made yet: the session has not said ready, the 20 minutes have not
	// passed since what the count reads, or the agent has not settled.
	reachNotDue
	// reachMarked: the claim is marked out of reach.
	reachMarked
)

// judge is c's record once the daemon has acted on this minute's reading of it (rec, verdict): an
// episode that is over closes the claim's lines, and a claim marked out of reach has its
// architect told, where it has one to tell, and is relaunched (relaunchMarked). siblings is the
// live claims of c's pod, c among them, and reaches whether Envoy reaches one of them this minute.
// The caller holds mu.
func (k *reachKeeper) judge(ctx context.Context, c supervise.Claim, rec supervise.Reach, verdict reachVerdict, siblings []supervise.Claim, reaches func(supervise.Claim) bool, now time.Time) supervise.Reach {
	switch verdict {
	case reachNotDue:
		return rec
	case reachOver, reachRegistered:
		if !k.episodeGoesOn(c, rec, verdict) {
			for _, line := range linesOf(c) {
				k.alerts.Close(line, string(c.Token))
			}
			return k.episodeOver(c, rec, verdict)
		}
	}
	if c.Role != claim.RoleController && !rec.ArchitectTold {
		rec.ArchitectTold = k.tellArchitect(ctx, c, rec)
	}
	return k.relaunchMarked(ctx, c, rec, siblings, reaches, now)
}

// reading is c's record after this minute's read and what it says (reachVerdict): it takes in
// c's last relaunch (relaunchTakenIn), begins a record anew for a session c records afresh, counts
// from the session's first ready, and makes the mark, which it logs; it acts on nothing, and it
// ends no episode, which is judge's to decide. The one session change that keeps the record is the
// daemon's own controller's relaunch launching it on a fresh session (carriesItsEpisode).
// The caller holds mu.
func (k *reachKeeper) reading(c supervise.Claim, now time.Time) (supervise.Reach, reachVerdict) {
	rec := relaunchTakenIn(c, k.recordOf(c))
	if k.carriesItsEpisode(c, rec) {
		if c.Session == "" {
			// The relaunch's own launch has recorded no session yet: the episode goes on.
			return rec, reachMarked
		}
		// The fresh session has said no ready of its own yet, and the mark stands.
		rec.Session, rec.FirstReady = c.Session, time.Time{}
	}
	if c.Session == "" || !holdsEpisode(c.State) {
		return rec, reachOver
	}
	if rec.Session != c.Session {
		rec = supervise.Reach{Session: c.Session, RelaunchedAt: rec.RelaunchedAt}
	}
	if rec.FirstReady.IsZero() {
		switch {
		case supervise.SaidReady(c.State) && !c.LastReady.IsZero():
			rec.FirstReady = c.LastReady
		case !rec.Unreachable():
			// This session has not said ready yet: nothing is counted for it.
			return rec, reachNotDue
		}
	}
	lastSeen := k.seen[c.Session]
	if !lastSeen.IsZero() && now.Sub(lastSeen) <= supervise.UnreachableAfter {
		return rec, reachRegistered
	}
	// The boot, the session's first ready, a failed read's end and a read of a list with no live
	// session each hold back a mark not made yet; a mark already made stays as it is, since none of
	// them is a registration of the claim's session (a read that fails changes no mark).
	countedFrom := latest(lastSeen, k.boot, rec.FirstReady, k.failedAt, k.noLiveAt)
	if !rec.Unreachable() && now.Sub(countedFrom) <= supervise.UnreachableAfter {
		return rec, reachNotDue
	}
	if !rec.Unreachable() {
		if !supervise.SaidReady(c.State) {
			// A mark is made of a settled agent; once made it holds in every launch state of the
			// session (holdsEpisode).
			return rec, reachNotDue
		}
		rec.Since, rec.LastSeen = countedFrom, lastSeen
		k.log.Warn("reach: the claim's session has not registered with Envoy for "+timewords.Words(supervise.UnreachableAfter)+"; it is out of reach, and keeps its state",
			"claim", c.Token, "state", c.State, "since", countedFrom, "lastSeen", lastSeen)
	}
	return rec, reachMarked
}

// episodeOver is rec once its episode is over, read as verdict, with the line that says so where
// the record marked one. The caller holds mu.
func (k *reachKeeper) episodeOver(c supervise.Claim, rec supervise.Reach, verdict reachVerdict) supervise.Reach {
	ended := rec.EndEpisode()
	switch {
	case verdict == reachOver && rec.Session != "" && rec != ended:
		k.log.Info("reach: the claim no longer runs on the session it was judged on; its mark is over", "claim", c.Token, "state", c.State)
	case verdict == reachRegistered && (rec.Unreachable() || rec.Relaunched):
		k.log.Info("reach: the claim's session registered with Envoy again; it is reachable", "claim", c.Token, "lastSeen", k.seen[c.Session])
	}
	return ended
}

// episodeGoesOn is whether c's episode lasts past a reading that ends any other claim's: c is the
// daemon's own controller, which a relaunch reached in this episode, and either its claim failed
// on the same session, as a controller whose path to Envoy stays blocked does once each boot fails
// its Envoy claim and exits, or its session registered but the controller is not back
// (controllerBack), since a process that registers and goes no further, or says ready and never
// takes the controller's Envoy role, hears nothing as the controller (R6). A workflow claim's
// session that registers is back, since its role's messages reach it. The caller holds mu.
func (k *reachKeeper) episodeGoesOn(c supervise.Claim, rec supervise.Reach, verdict reachVerdict) bool {
	if c.Role != claim.RoleController || !rec.Relaunched {
		return false
	}
	if verdict == reachRegistered {
		return !k.controllerBack(c, rec)
	}
	return c.Session != "" && c.Session == rec.Session && c.State == supervise.StateFailed
}

// relaunchMarked is the daemon's relaunch of c, marked out of reach, on its same session (R5): no
// agent is asked to relaunch it. It relaunches it at most once in 6 hours, under the relaunch
// guard (supervise.RelaunchGuard). A workflow claim's relaunch replaces no pod, since that would
// end every other role of the pod; the controller's replaces its pod where the launch needs it or
// its launcher is not connected, since no other role runs there (supervise.RequestRelaunch's
// MayReplacePod). Each case it cannot get past writes R6's line for c, once until c is reachable
// again (fleetalert.Writer.Open, whose store keeps which lines are open):
//
//   - the relaunch did not bring c back within 20 minutes, or c is out of reach a second time
//     within the 6 hours (the first of linesOf);
//   - Envoy reaches no claim of c's pod, two or more; the runtime refuses the relaunch,
//     because the role's launcher is not connected or because the launch would replace the pod;
//     the process outlived the relaunch's stop; or the guard refuses it, at once for the
//     controller, which nobody else can relaunch, and for 60 minutes for a workflow claim, whose
//     process may still run a turn that ends (the second of linesOf).
//
// A refused relaunch uses no part of the 6 hours, and the daemon asks again at each sweep, so a
// claim the guard lets through later, or whose launcher is connected again, is relaunched then.
// The relaunch it made is the one c's claim records (supervise.Claim.LastRelaunch), which the
// record has taken in by now (relaunchTakenIn), so a daemon that ended before its own write of
// the record does not relaunch c a second time within the 6 hours. The caller holds mu.
func (k *reachKeeper) relaunchMarked(ctx context.Context, c supervise.Claim, rec supervise.Reach, siblings []supervise.Claim, reaches func(supervise.Claim) bool, now time.Time) supervise.Reach {
	lines, token := linesOf(c), string(c.Token)
	if rec.Relaunched {
		if now.Sub(rec.RelaunchedAt) >= notBackAfter {
			k.alerts.Open(lines[0], token, "relaunchedAt", rec.RelaunchedAt, "reason", notBack(c))
		}
		return rec
	}
	err := refusalOf(c, rec, siblings, reaches, now)
	switch {
	case errors.Is(err, supervise.ErrRelaunchedRecently):
		k.alerts.Open(lines[0], token, "relaunchedAt", rec.RelaunchedAt, "reason", fmt.Sprintf(
			"it is out of reach a second time within %[1]s of its last relaunch, and a claim is relaunched once in %[1]s", timewords.Words(supervise.RelaunchAgainAfter)))
	case errors.Is(err, supervise.ErrClaimReachable):
	case err != nil:
		k.alerts.Open(lines[1], token, "reason", err.Error())
	case k.relaunching[c.Token] || k.relaunch == nil:
	default:
		if why := supervise.RelaunchGuard(c, time.Time{}, now); why != "" {
			return k.guardRefuses(c, rec, "the relaunch guard refuses it: "+why, now)
		}
		k.relaunching[c.Token] = true
		k.wg.Add(1)
		go k.relaunchClaim(ctx, c, now)
	}
	return rec
}

// linesOf is c's two lines of R6: the first says its relaunch did not bring it back, or it is out
// of reach again within 6 hours of the last, and the second that its relaunch cannot be made. The
// controller's are its own, since nobody else holds its role.
func linesOf(c supervise.Claim) [2]fleetalert.Case {
	if c.Role == claim.RoleController {
		return [2]fleetalert.Case{fleetalert.ControllerUnreachable, fleetalert.ControllerRelaunchRefused}
	}
	return [2]fleetalert.Case{fleetalert.ClaimUnreachable, fleetalert.ClaimRelaunchRefused}
}

// notBack is the reason R6's line gives for c, relaunched 20 minutes ago and still out of reach:
// the controller is back only once its session holds the controller's Envoy role
// (controllerBack), and a workflow claim once its session registers again.
func notBack(c supervise.Claim) string {
	if c.Role == claim.RoleController {
		return "the daemon's relaunch did not bring it back: in the " + timewords.Words(notBackAfter) +
			" since, it has not said ready with the controller's Envoy role held by its session"
	}
	return "its relaunch did not bring it back: its session has not registered with Envoy in the " +
		timewords.Words(notBackAfter) + " since, so the cause is in its pod or its connection, not in its process"
}

// guardRefuses is rec once the guard has refused c's relaunch at at, for why: the episode's first
// refusal is kept, and logged with its reason, since the daemon asks again at each sweep and says
// nothing more until R6's line; that line is written once the guard has refused c for long enough,
// at once for the controller (R6) and after 60 minutes for a workflow claim (R5). The caller
// holds mu.
func (k *reachKeeper) guardRefuses(c supervise.Claim, rec supervise.Reach, why string, at time.Time) supervise.Reach {
	if rec.GuardRefusedAt.IsZero() {
		rec.GuardRefusedAt = at
		k.log.Info("reach: the relaunch guard holds the daemon's relaunch of a claim Envoy cannot reach; it asks again at each sweep",
			"claim", c.Token, "reason", why)
	}
	held := guardHeldAfter
	if c.Role == claim.RoleController {
		held = 0
	}
	if at.Sub(rec.GuardRefusedAt) >= held {
		k.alerts.Open(linesOf(c)[1], string(c.Token), "since", rec.GuardRefusedAt, "reason", why)
	}
	return rec
}

// errRelaunchWaitsForResume is the daemon's relaunch of a workflow claim while the project is
// paused, which starts no turn of a role but the controller's: the sweep after the resume asks
// again.
var errRelaunchWaitsForResume = errors.New("the project is paused, and a paused project starts no turn of a role but the controller's")

// relaunchClaim runs the daemon's relaunch of c off the sweep, since a Sandbox relaunch waits
// minutes on its pods, and records what it did. The relaunch's launch has written the relaunch
// onto c's claim, so a write of the record the store refuses here is written again at the next
// sweep and costs no part of the 6 hours.
func (k *reachKeeper) relaunchClaim(ctx context.Context, c supervise.Claim, at time.Time) {
	defer k.wg.Done()
	relaunched, err := k.relaunch(ctx, c, at)
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.relaunching, c.Token)
	refused := linesOf(c)[1]
	switch {
	case err == nil:
		k.log.Warn("reach: the daemon relaunched a claim Envoy could not reach, on its same session", "claim", c.Token, "generation", relaunched.Generation)
		k.alerts.Close(refused, string(c.Token))
		k.storeEach(ctx, c.Token, relaunchTakenIn(relaunched, k.recordOf(c)))
	case errors.Is(err, supervise.ErrRelaunchGuard):
		if rec := k.recordOf(c); rec.Unreachable() {
			k.storeEach(ctx, c.Token, k.guardRefuses(c, rec, err.Error(), at))
		}
	case errors.Is(err, runtime.ErrLauncherNotConnected), errors.Is(err, runtime.ErrRelaunchReplacesPod),
		errors.Is(err, runtime.ErrProcessOutlivedStop):
		k.alerts.Open(refused, string(c.Token), "reason", err.Error())
	case errors.Is(err, errRelaunchWaitsForResume):
	default:
		if ctx.Err() == nil {
			k.log.Error("reach: the daemon's relaunch of a claim Envoy could not reach failed; it is tried again at the next sweep", "claim", c.Token, "error", err)
		}
	}
}

// controllerBack is whether the daemon's own controller c, which a relaunch reached in the
// episode rec marks, is back from that relaunch (R6): it said ready after the relaunch, and its
// session holds the controller's Envoy role in the last list read. The caller holds mu.
func (k *reachKeeper) controllerBack(c supervise.Claim, rec supervise.Reach) bool {
	return supervise.SaidReady(c.State) && c.LastReady.After(rec.RelaunchedAt) &&
		k.sessions[c.Session].HoldsRole(string(c.Token))
}

// carriesItsEpisode is whether c, the daemon's own controller, has left the session rec marks
// for its relaunch's own launch (R6): a resume that finds no session starts the controller on a
// fresh one, or has recorded none yet. That launch is the relaunch's while c records the relaunch
// rec has taken in and stands at the generation that relaunch counts from (relaunchGeneration),
// and a later launch is not. So the episode, and R6's line 20 minutes after the relaunch, goes on
// through that change, for a launch that runs, is starting, or has failed; a worker's relaunch
// keeps its session, and a controller suspended or retired has nothing left to judge.
func (k *reachKeeper) carriesItsEpisode(c supervise.Claim, rec supervise.Reach) bool {
	return c.Role == claim.RoleController && rec.Relaunched && rec.Unreachable() && c.Session != rec.Session &&
		c.LastRelaunch.Equal(rec.RelaunchedAt) &&
		c.Generation == relaunchGeneration(c) &&
		(holdsEpisode(c.State) || c.State == supervise.StateFailed)
}

// tellArchitect tells the architect of c's tree that c is out of reach, written with rec marked
// told in the notice's own transaction, and answers whether it did: a notice of the claim on its
// issue, which the owner walk takes to the architect that owns the issue, or for a child's own
// architect on its tree's root. The tree architect's own claim is told to nobody here, since it
// could not hear it. The notice asks nothing, since the daemon relaunches the claim. The caller
// holds mu.
func (k *reachKeeper) tellArchitect(ctx context.Context, c supervise.Claim, rec supervise.Reach) bool {
	if k.enqueue == nil || c.Issue == "" || claim.IsTreeArchitect(c.Role, c.Issue, c.Tree) {
		return true
	}
	issue := c.Issue
	if c.Role == claim.RoleArchitect {
		issue = c.Tree
	}
	notice := record.Notice{Kind: record.ClaimOutOfReachNotice, Role: c.Role, Claim: string(c.Token), Reason: fmt.Sprintf(
		"%s's session has not registered with Envoy since %s, more than %s, so it cannot hear its notices. Its claim keeps its state, and the daemon relaunches it on the same session; nothing is asked of you.",
		c.Token, stamp(rec.Since), timewords.Words(supervise.UnreachableAfter))}
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

// tellController queues notice on issue, with the records marks says it marks told written in the
// same transaction, and answers whether it queued it (tellQueued), taking the marks in where it
// did. The caller holds mu.
func (k *reachKeeper) tellController(ctx context.Context, notice record.ControllerNotice, issue string, marks ...reachMark) bool {
	told := tellQueued(ctx, k.log, k.tell, notice, issue, marks...)
	if told {
		k.marked(marks)
	}
	return told
}

// refusalOf is why a relaunch of c, whose reach record is rec, is refused before c's machine
// is asked, and nil where it goes on. c must be marked out of reach; its last relaunch — the later
// of the one rec has taken in and the one c's own row records, which the relaunch's launch wrote —
// must be at least 6 hours old; and of siblings, the live claims of c's pod with c among them, two
// or more, Envoy must reach one besides c (reaches), since a relaunch into a pod that reaches
// Envoy for none of them fails each worker at its deaths limit and recovers none.
func refusalOf(c supervise.Claim, rec supervise.Reach, siblings []supervise.Claim, reaches func(supervise.Claim) bool, now time.Time) error {
	last := rec.RelaunchedAt
	switch {
	case !rec.Unreachable():
		return supervise.ErrClaimReachable
	case !last.IsZero() && now.Sub(last) < supervise.RelaunchAgainAfter:
		return fmt.Errorf("%w: at %s", supervise.ErrRelaunchedRecently, stamp(last))
	case len(siblings) < 2:
		return nil
	}
	tokens := make([]claim.Token, 0, len(siblings))
	for _, other := range siblings {
		if other.Token != c.Token && reaches(other) {
			return nil
		}
		tokens = append(tokens, other.Token)
	}
	slices.Sort(tokens)
	return fmt.Errorf("%w: pod %s holds %d live claims, and Envoy reaches none of them: %v", supervise.ErrPodUnreachable, c.Locator.Sandbox.PodUID, len(tokens), tokens)
}

// podClaims is the live claims of each pod, by its UID (podsOf).
type podClaims map[string][]supervise.Claim

// podsOf is the live claims of claims grouped by the pod each runs in; a claim that runs in no pod
// (tmux) shares nothing and is in none.
func podsOf(claims []supervise.Claim) podClaims {
	pods := podClaims{}
	for _, c := range claims {
		if c.Locator != nil && c.Locator.Sandbox != nil && runsOnSession(c.State) {
			pods[c.Locator.Sandbox.PodUID] = append(pods[c.Locator.Sandbox.PodUID], c)
		}
	}
	return pods
}

// siblings is the live claims of c's pod, c among them, and none for a claim that runs in no pod
// or for the controller, whose relaunch may replace its own pod.
func (p podClaims) siblings(c supervise.Claim) []supervise.Claim {
	if c.Role == claim.RoleController || c.Locator == nil || c.Locator.Sandbox == nil {
		return nil
	}
	return p[c.Locator.Sandbox.PodUID]
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
