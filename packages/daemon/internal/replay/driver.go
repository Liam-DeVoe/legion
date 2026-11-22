package replay

import (
	"cmp"
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/admit"
	"github.com/sjawhar/legion/daemon/internal/blockers"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/pause"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// FactWay is how a replay comes by one kind of fact the daemon applies. A daemon applies facts
// its Dispatch consumer decodes and facts its own producers make — the API, the ticks and boots,
// the outbox's executors, supervision, GitHub — and a replay has none of those producers, so each
// kind of fact is either rebuilt from the window's record, made by the replay as the daemon's own
// producer makes it, or left out under an assumption the run names.
type FactWay string

const (
	// FactInjected is a fact the replay applies at its recorded time, or at its recorded offset
	// from the start it depends on, built from the window's record: a Dispatch event through
	// intake.DecodeDispatchEvent, or a recorded line of the act that applied it.
	FactInjected FactWay = "injected"
	// FactProduced is a fact the replay's own driver or worker stub applies as the daemon's own
	// producer applies it, from the replay's own state: its ticks, its boots and the outbox rows
	// it executes.
	FactProduced FactWay = "produced"
	// FactLeftOut is a fact a replay never applies, under the assumption FactSource.Assumption
	// names: one that changes nothing the replay decides or measures.
	FactLeftOut FactWay = "left_out"
)

// FactSource is how a replay comes by one kind of fact: its way, what it builds the fact from or
// the act of its own that applies it, and, for a fact it leaves out, the assumption it leaves it
// out under.
type FactSource struct {
	// Way is how the replay comes by the fact.
	Way FactWay
	// From is what the replay builds the fact from — the recorded Dispatch event or line it reads
	// — or the act of the replay's own that applies it; empty for a fact left out.
	From string
	// Assumption is the number of the assumption (Assumptions) the fact is left out under; zero
	// for a fact the replay applies.
	Assumption int
}

// The classes of the items of a replay's queue, which order the items of one instant: a boot
// first, then the turns that end, then the record's facts in the record's own order, then the
// ticks, the outbox rows woken at their next_at, then the turns that begin, and last the sizing of
// each piece of work opened in the instant (sizePiece), once every pass of the instant has run.
const (
	classBoot = iota
	classTurnEnd
	classAct
	classTick
	classWake
	classTurnStart
	classSize
)

// item is one thing a replay does at a moment of the simulated clock: apply a fact, end a
// segment, write a turn of a claim, boot, tick, wake an outbox row. rec and order are the record's
// moment and order of what the item comes from, which order two items of one instant and class.
type item struct {
	at    time.Time
	class int
	rec   time.Time
	order int64
	seq   int64
	// piece is the piece of work the item is an act of, nil for none: a paused piece holds its
	// items back and a cancelled one drops them. eventKey and eventKind are the issue and the type
	// of a recorded Dispatch event the item applies, "" for an item of another kind.
	piece               *piece
	eventKey, eventKind string
	done                bool
	// dispatch is the item's change to the replay's copy of Dispatch, which runs at its moment
	// whether or not the daemon is up (Dispatch is), and run its act on the daemon, which waits
	// for the daemon's next boot while it is down (assumption 24).
	dispatch func() error
	run      func() error
}

// itemQueue is the replay's queue of items, by moment, class, the record's moment and order, and
// the order they were queued in.
type itemQueue []*item

func (q itemQueue) Len() int { return len(q) }
func (q itemQueue) Less(i, j int) bool {
	x, y := q[i], q[j]
	return cmp.Or(x.at.Compare(y.at), cmp.Compare(x.class, y.class), x.rec.Compare(y.rec), cmp.Compare(x.order, y.order),
		cmp.Compare(x.seq, y.seq)) < 0
}
func (q itemQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *itemQueue) Push(x any)   { *q = append(*q, x.(*item)) }
func (q *itemQueue) Pop() any {
	old := *q
	last := old[len(old)-1]
	*q = old[:len(old)-1]
	return last
}

// lineOrder is the order of an item that comes from a line of the record, after every Dispatch
// event of the same instant: the fact a pass applied comes before what the pass wrote.
const lineOrder = int64(1) << 50

// tapped is the daemon's lines the driver reads as the replay writes them (lineTap).
var tapped = []string{
	phaseChangedLine,
	"workflow: issue left the workflow",
	"admission: slot taken",
	"admission: slot released",
	"admission: an issue waits for the Dispatch children under it",
	"workflow: design gate closed",
	mergeForwardAskedLine,
	mergeForwardLandedLine,
	admit.CoverTakenLine,
}

// driver is one replay's run: the window and its reading, the replay's database, its simulated
// clock and log, the daemon's chain it drives, its copy of Dispatch, its queue, its worker stub's
// state and what it counts.
type driver struct {
	ctx    context.Context
	cfg    RunConfig
	window Window
	trace  Trace
	script script
	lines  recordLines
	rule   record.SlotRule

	clock   *simClock
	st      *store.Store
	pool    *store.Pool
	records *record.Postgres
	log     *slog.Logger
	tap     *lineTap
	shadow  *shadow
	// token is the project's claim token, which claims and tree lifecycles are keyed by.
	token string

	engine    *workflow.Engine
	admission *admit.Admission
	handlers  []intake.Handler
	// boots is how many boots the replay has made, which names each boot's facts.
	boots int

	queue itemQueue
	seq   int64
	// held is every act that fell while the daemon was down, in order, which the next boot
	// applies after it has booted (assumption 24).
	held []*item
	// wakeAt is the moment of the outbox wake queued, zero for none.
	wakeAt time.Time

	// claims is every claim the stub holds, as last written, with its pending delivery, and
	// working the piece each claim's turn under way stands in.
	claims  map[claim.Token]supervise.Claim
	working map[claim.Token]*piece

	// inFlight is the issues in flight in the replay, started is when each issue of its current
	// entry first took a slot, childWait when each issue's children wait began, and placed when
	// each recorded event was placed, by its index in the script.
	inFlight  map[string]bool
	started   map[string]time.Time
	childWait map[string]time.Time
	placed    map[int]time.Time
	// anchored is the events whose place waits for the replay's start of the wait they ended.
	anchored []int
	// signOffs is each issue whose sign-off the engine holds behind a member beneath, which the
	// replay applies again at each leave of a member of its tree.
	signOffs map[string]bool
	// drops is, for each issue whose recorded move back the replay is entering, the line of its
	// pull request leaving the merge queue the record shows before it (Move.Drop), which names the
	// move's cause in the snapshot timeline.
	drops map[string]string
	// moves counts the replay's own facts, which names each.
	moves int
	// releasedOrTook is what the slots of the current fact did: a slot released and a slot
	// taken in one fact is a slot given out in the pass that freed it (assumption 11).
	released, took bool

	// snap is the replay's snapshot timeline (snapshots), which each admission pass's observer
	// and each settle feed, and dirty says an act ran since the last settle.
	snap  *snapshots
	dirty bool

	counters Divergence
	used     map[int]bool
	segments []Segment
	facts    int
	passTime time.Duration
	paused   bool
	pauseID  int64
	unplaced []Unplaced

	// conflicts is the conflict model the run draws by (with its merges term set to zero under
	// the branch's Merges zero), merges the window's commits of each base branch it counts a
	// stretch's M from, loops the recorded conflict loops a drawn conflict's work is drawn from,
	// draws the run's seeded source of every conflict draw, and stretchFrom the replay's start of
	// the stretch each issue stands in. heads counts the heads the replay's own pull request
	// records moved to, which names each.
	conflicts   ConflictModel
	merges      baseMerges
	loops       []conflictLoop
	draws       *rand.Rand
	stretchFrom map[string]time.Time
	heads       int
	// timingWall and answerWall say the branch keeps the events that end a wait, and a person's
	// answers, at their recorded times (Branch.Timing and Branch.Answer wall), not at their
	// recorded offsets from their anchors.
	timingWall, answerWall bool
}

// Divergence is what a replay counts of where it left the record's course, which a replay under
// the recorded rule leaves at zero but for the causes the record itself holds (design 10.9).
type Divergence struct {
	// Refused is each recorded move the replay entered and the daemon's code refused, with the
	// reason; the segment after it never opens.
	Refused []RefusedMove `json:"refused"`
	// HeldSignOffs is how many sign-offs the engine held behind a member beneath, each applied
	// again at the member's leave.
	HeldSignOffs int `json:"heldSignOffs"`
	// HeldMerges is how many merges left their issue in merging, its close waiting for the issues
	// beneath it still in the workflow (record.OpenBeneath), which the engine makes at the last
	// one's leave as the daemon does.
	HeldMerges int `json:"heldMerges"`
	// Reassertions is how many starts re-sent a claim the entry it had already been given
	// (admission's repair of a role behind its entry), which opened no segment.
	Reassertions int `json:"reassertions"`
	// NoSegment is how many starts found no recorded segment of their issue left to open.
	NoSegment int `json:"noSegment"`
	// SkippedSegments is how many recorded segments the replay passed over, a later one of its
	// issue opening first.
	SkippedSegments int `json:"skippedSegments"`
	// Fenced is how many Dispatch events a fence of the daemon's dropped, its issue's record
	// already past the event's sequence.
	Fenced int `json:"fenced"`
	// Unanchored is how many recorded events never took a place, their anchor never reached in
	// the replay before the window's end.
	Unanchored int `json:"unanchored"`
	// Finished is how many outbox rows the stub finished without acting, by the assumption each
	// rests on.
	Finished map[int]int `json:"finished"`
	// Retried is how many times the stub gave an outbox row back to wait on its backoff, as a lane
	// gives back a row whose executor waits on something.
	Retried int `json:"retried"`
	// TurnsLeftOut is how many recorded turns the replay wrote no claim of, by the assumption each
	// rests on: a turn of an issue outside the workflow, a production check's (assumption 30),
	// standing at the window's start (SeedReport.LeftOut) or begun in a tree that runs no lifecycle
	// in the replay (ensureClaim).
	TurnsLeftOut map[int]int `json:"turnsLeftOut"`
	// NoLifecycleTurns is how many recorded turns of an issue in the workflow, or of one the replay
	// does not record, began in the replay while the issue's tree ran no lifecycle there, which the
	// replay wrote no claim of and went on (ensureClaim).
	NoLifecycleTurns int `json:"noLifecycleTurns"`
	// InferredSlots is how many of the start state's slots the trace inferred an issue held with no
	// slot line of it (assumption 34), and SlotCountDifference the slots the trace holds right after
	// the slot line at the window's start, the first at or after it, less the slots in use that line
	// says (SlotCount.Difference), for which the start state names no issue.
	InferredSlots       int `json:"inferredSlots"`
	SlotCountDifference int `json:"slotCountDifference"`
}

// RefusedMove is one recorded move the daemon's code refused in a replay.
type RefusedMove struct {
	Issue   string               `json:"issue"`
	At      time.Time            `json:"at"`
	Trigger workflow.TriggerKind `json:"trigger"`
	From    phase.Phase          `json:"from"`
	To      phase.Phase          `json:"to"`
	Reason  string               `json:"reason"`
}

// push queues it, numbered after every item queued before it.
func (d *driver) push(it *item) *item {
	d.seq++
	it.seq = d.seq
	if it.piece != nil {
		it.piece.pending = append(it.piece.pending, it)
	}
	heap.Push(&d.queue, it)
	return it
}

// now is the simulated clock's next moment.
func (d *driver) now() time.Time { return d.clock.Now() }

// newChain builds the daemon's handler chain afresh, as each of the daemon's boots does: a new
// engine and admission pass under the run's rule, on the simulated clock, with the window's
// settings and the cap the daemon served under at at, the replay's tables of what a rule the
// daemon does not run records (ruleTables), and the replay's own handler before the chain.
func (d *driver) newChain(at time.Time) {
	w := d.window
	source := rand.New(rand.NewPCG(uint64(d.cfg.Seed), 0x7265706c6179))
	draw := func(bound uint64) (uint64, error) {
		if bound == 0 {
			return 0, fmt.Errorf("draw a value below %d: there is no value below zero", bound)
		}
		return source.Uint64N(bound), nil
	}
	d.engine = workflow.New(d.records, workflow.Config{Project: w.Project, Steps: w.GateSteps, ReviewRoundCap: w.ReviewRoundCap,
		Linger: time.Duration(w.Linger), Clock: d.clock.Now, Repositories: replayRepositories{branches: d.trace.Branches},
		Experiments: w.Experiments, ExperimentSource: draw, SlotRule: d.rule}, d.log)
	d.admission = admit.New(d.records, d.engine, capAt(w, at), w.Project, d.log)
	d.admission.UseSlotRule(d.rule)
	d.admission.UseRuleTables(ruleTables{})
	d.admission.UseClock(d.clock.Now)
	d.admission.UseExperiments(w.Experiments)
	d.admission.UseExperimentSource(draw)
	d.admission.UseStalledTreeNotice(time.Duration(w.StalledTreeNotice))
	d.admission.UseWorkerSilence(time.Duration(w.WorkerSilence), time.Duration(w.WorkerBackground))
	d.admission.UsePassObserver(d.snap.pass)
	d.handlers = append([]intake.Handler{replayHandler{engine: d.engine, clock: d.clock.Now, rule: d.rule, records: d.records,
		project: w.Project}}, admit.Handlers(d.records, w.Project, d.engine, d.admission)...)
}

// capAt is the admission cap w's daemon ran with at at: the last change at or before it, or the
// first where at is before every change.
func capAt(w Window, at time.Time) int {
	if len(w.Caps) == 0 {
		return 0
	}
	limit := w.Caps[0].Cap
	for _, change := range w.Caps {
		if !change.At.After(at) {
			limit = change.Cap
		}
	}
	return limit
}

// replayRepositories is the replay's stand-in for the daemon's view of its repositories
// (workflow.Repositories): every repository an issue resolves to is served, on the base branch the
// window's merges name for it, or main.
type replayRepositories struct {
	branches map[string]string
}

// ResolveRepository serves resolved.
func (r replayRepositories) ResolveRepository(resolved string) (workflow.Resolution, error) {
	return workflow.Resolution{Repo: resolved, GitHubRepo: resolved, DefaultBranch: cmp.Or(r.branches[resolved], defaultBranch)}, nil
}

// apply applies fact as source's eventID through the chain, on the simulated clock, and reads
// what the daemon's lines say it did.
func (d *driver) apply(source, eventID string, fact intake.Fact) (intake.Result, error) {
	began := time.Now()
	d.released, d.took = false, false
	result, err := intake.ApplyFactWith(d.ctx, d.pool, d.records, source, eventID, fact, d.handlers...)
	d.facts++
	d.passTime += time.Since(began)
	if err != nil {
		return result, fmt.Errorf("replay: apply %s %s at %s: %w", source, eventID, stamp(d.clock.Instant()), err)
	}
	if err := d.observe(); err != nil {
		return result, err
	}
	if d.cfg.afterFact != nil {
		if err := d.cfg.afterFact(d.ctx, d.pool, d.shadow); err != nil {
			return result, err
		}
	}
	return result, nil
}

// factID names one of the replay's own facts uniquely in its run.
func (d *driver) factID(kind string, parts ...any) string {
	d.moves++
	id := fmt.Sprintf("replay-%s:%d", kind, d.moves)
	for _, part := range parts {
		id += fmt.Sprintf(":%v", part)
	}
	return id
}

// observe reads the daemon's lines the last fact wrote: the moves that open a segment no start
// opens and the leaves that apply a held sign-off again, the slots that put an issue in flight,
// the children waits that place the events that ended them, and what the run's assumptions are
// named from.
func (d *driver) observe() error {
	for _, line := range d.tap.take() {
		key := line.str("issue")
		switch line.msg {
		case phaseChangedLine:
			from, to := phase.Phase(line.str("from")), phase.Phase(line.str("to"))
			trigger, drop := workflow.TriggerKind(line.str("trigger")), ""
			if trigger == workflow.TriggerBackward {
				drop = d.drops[key]
				delete(d.drops, key)
			}
			d.snap.moved(key, d.clock.Instant(), from, to, trigger, drop)
			if to == phase.Accepting || to == phase.Retro || to == phase.Merging {
				d.used[2] = true
			}
			if phase.MovesBack(from, to) {
				d.used[3], d.used[13] = true, true
			}
			if startsStretch(from, to) {
				d.stretchFrom[key] = d.clock.Instant()
			}
			if to == phase.Done {
				delete(d.inFlight, key)
			}
			if to == phase.Accepting {
				d.openOnPhase(key, to)
			}
		case "workflow: issue left the workflow":
			delete(d.inFlight, key)
			delete(d.started, key)
			delete(d.childWait, key)
			d.cancelWork(key)
			tree := line.str("tree")
			for _, waiting := range slices.Sorted(maps.Keys(d.signOffs)) {
				if waiting != key && d.treeOf(waiting) == tree {
					d.push(&item{at: d.clock.Instant(), class: classAct, rec: d.clock.Instant(), order: lineOrder,
						run: func() error { return d.signOff(waiting) }})
				}
			}
		case "admission: slot taken":
			d.used[9] = true
			d.took = true
			d.inFlight[key] = true
			if _, ok := d.started[key]; !ok {
				d.started[key] = d.clock.Instant()
			}
			if d.released {
				d.used[11] = true
			}
		case "admission: slot released":
			d.released = true
		case "admission: an issue waits for the Dispatch children under it":
			d.used[4] = true
			if _, ok := d.childWait[key]; !ok {
				d.childWait[key] = d.clock.Instant()
			}
		case "workflow: design gate closed":
			d.used[4] = true
		case mergeForwardAskedLine:
			d.snap.roundBegins(key, d.clock.Instant())
		case mergeForwardLandedLine:
			d.snap.roundEnds(key, d.clock.Instant())
		case admit.CoverTakenLine:
			d.used[14] = true
		}
	}
	return d.placeAnchored()
}

// treeOf is the tree key's record stands in, "" for an issue the replay does not record.
func (d *driver) treeOf(key string) string {
	var tree string
	_ = pgx.BeginTxFunc(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		issue, err := d.records.Issue(d.ctx, tx, key)
		if err == nil && issue != nil {
			tree = issue.Tree
		}
		return err
	})
	return tree
}

// recorded is key's record in the replay's database, nil for an issue it does not record.
func (d *driver) recorded(key string) (*record.Issue, error) {
	var issue *record.Issue
	err := pgx.BeginTxFunc(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		issue, err = d.records.Issue(d.ctx, tx, key)
		return err
	})
	return issue, err
}

// run replays the window: the start state at its start, the replay's copy of Dispatch brought to
// the record's state there, then every item of the queue in order to its end, the worker stub
// after each.
func (d *driver) run() error {
	w, t := d.window, d.trace
	d.clock.advance(w.From)
	if err := d.seed(); err != nil {
		return err
	}
	// The copy of Dispatch starts where the record stands at the window's start: every recorded
	// event before it, the daemon's own writes among them, at its recorded time and sequence, in
	// the script's order (shadow.seedEvent).
	for i := range d.script.events {
		event := &d.script.events[i]
		if !event.at.Before(w.From) {
			continue
		}
		if err := d.shadow.seedEvent(event.env, event.parsed.Seq, event.at); err != nil {
			return fmt.Errorf("replay: bring the copy of Dispatch to the window's start with the %s event of %s at %s: %w", event.env.kind,
				event.env.key, stamp(event.at), err)
		}
	}
	if d.cfg.atStart != nil {
		d.cfg.atStart(d.shadow)
	}
	// An issue that stands in a stretch at the window's start began it where the record shows.
	for _, key := range sortedKeys(t.Issues) {
		if start, standing := stretchStartAt(t.Issues[key], w.From); standing {
			d.stretchFrom[key] = start
		}
	}
	// The process that runs at the window's start is the newest that started before it; a boot
	// at the start itself is applied as a boot.
	d.newChain(w.From)
	// Under a rule whose start may take a cover the start state the record's rule left is first
	// booted onto the rule, as the release that runs it boots (FirstBoot, spec v4 R9): every started
	// issue holding no slot takes one, and the pass of the same fact works out the covers.
	if d.rule.Take == record.TakeCover {
		if _, err := d.apply("replay", d.factID("first-boot"), FirstBoot{}); err != nil {
			return err
		}
	}
	for i := range t.Boots {
		boot := t.Boots[i]
		if boot.At.Before(w.From) || !boot.At.Before(w.To) {
			continue
		}
		d.push(&item{at: boot.At, class: classBoot, rec: boot.At, run: func() error { return d.boot(boot) }})
	}
	for _, at := range t.Ticks {
		if at.Before(w.From) || !at.Before(w.To) {
			continue
		}
		d.push(&item{at: at, class: classTick, rec: at, run: func() error { return d.tick(at) }})
	}
	for _, pause := range t.Pauses {
		if !pause.From.Before(w.From) && pause.From.Before(w.To) {
			d.push(&item{at: pause.From, class: classAct, rec: pause.From, order: lineOrder, run: func() error { return d.pause(pause) }})
		}
		if !pause.To.IsZero() && !pause.To.Before(w.From) && pause.To.Before(w.To) {
			d.push(&item{at: pause.To, class: classAct, rec: pause.To, order: lineOrder, run: func() error { return d.resume(pause) }})
		}
	}
	for i := range d.script.events {
		event := &d.script.events[i]
		switch {
		case event.at.Before(w.From):
			continue
		case event.anchor == anchorTime, event.anchor == anchorWait && d.timingWall, event.anchor == anchorEvent && d.answerWall:
			d.placeEvent(i, event.at)
		case event.anchor == anchorWait:
			d.anchored = append(d.anchored, i)
			d.push(&item{at: event.at, class: classAct, rec: event.at, order: int64(event.order), run: func() error {
				d.placeUnbegun(i)
				return nil
			}})
		}
	}
	d.openSeeded()
	d.releaseSeededHolds()
	if err := d.drain(); err != nil {
		return err
	}
	if err := d.startView(); err != nil {
		return err
	}
	d.dirty = true
	for d.queue.Len() > 0 {
		it := heap.Pop(&d.queue).(*item)
		if !it.at.Before(w.To) {
			break
		}
		if it.done {
			continue
		}
		if it.piece != nil && it.piece.state != pieceRunning {
			// A paused piece keeps the item for its resume; an ended or cancelled one drops it.
			if it.piece.state != piecePaused {
				it.done = true
			}
			continue
		}
		it.done = true
		// Every act of the instant before this one has run: the snapshot timeline takes the
		// state they left before the clock moves on.
		if d.dirty && it.at.After(d.clock.Instant()) {
			if err := d.settle(); err != nil {
				return err
			}
		}
		d.clock.advance(it.at)
		if it.dispatch != nil {
			if err := it.dispatch(); err != nil {
				return err
			}
		}
		if it.class != classBoot && d.down(it.at) {
			d.held = append(d.held, it)
			continue
		}
		if err := it.run(); err != nil {
			return err
		}
		if err := d.drain(); err != nil {
			return err
		}
		d.dirty = true
	}
	for _, i := range d.anchored {
		if _, placed := d.placed[i]; !placed {
			d.counters.Unanchored++
		}
	}
	return d.settle()
}

// startView hands the snapshot timeline the view of the start state, read as a pass reads it
// (admit.Admission.View), so the states from the window's start to its first pass are the start
// state's.
func (d *driver) startView() error {
	return pgx.BeginTxFunc(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		view, err := d.admission.View(d.ctx, tx)
		if err != nil {
			return fmt.Errorf("replay: read the start state at %s as a pass reads it: %w", stamp(d.window.From), err)
		}
		d.snap.pass(view)
		return nil
	})
}

// settle takes the state every act of the clock's instant left into the snapshot timeline, where
// an act ran since the last settle, and hands the instant to the test's look at it
// (RunConfig.atSettled).
func (d *driver) settle() error {
	if !d.dirty {
		return nil
	}
	d.dirty = false
	at := d.clock.Instant()
	d.snap.settle(at, d.claims, d.inFlight, d.shadow)
	if d.cfg.atSettled != nil {
		return d.cfg.atSettled(d.ctx, d.st, at)
	}
	return nil
}

// down says whether no daemon ran at at: inside a downtime of the record, after its last line and
// before the next process started, or after the last process began to stop where no process
// started after it (assumption 24).
func (d *driver) down(at time.Time) bool {
	for _, downtime := range d.trace.Downtimes {
		if downtime.From.Before(at) && at.Before(downtime.To) {
			d.used[24] = true
			return true
		}
	}
	if n := len(d.trace.Boots); n > 0 {
		if stopped := d.trace.Boots[n-1].Stopped; !stopped.IsZero() && stopped.Before(at) {
			d.used[24] = true
			return true
		}
	}
	return false
}

// seed writes the start state at the window's start (SeedAndReport), on the trace whose ranks the
// branch shows, reads its report into the run — the issues it named, and into the divergence the
// turns it wrote no claim of under each assumption, the slots the trace inferred and the difference
// of the trace's slots from the record's count, the assumptions of the turns and of the slots being
// named among those the run rests on where it counted any — and reads back the claims it wrote for
// the stub.
func (d *driver) seed() error {
	return d.st.Tx(d.ctx, func(tx pgx.Tx) error {
		report, err := SeedAndReport(d.ctx, tx, d.records, d.st, withRanks(d.trace, d.shadow.ranks), d.window.From)
		if err != nil {
			return err
		}
		d.unplaced = report.Unplaced
		for number, turns := range report.LeftOut {
			d.counters.TurnsLeftOut[number] += turns
			d.used[number] = d.used[number] || turns > 0
		}
		d.counters.InferredSlots, d.counters.SlotCountDifference = report.InferredSlots, report.SlotCount.Difference()
		if report.InferredSlots > 0 {
			d.used[assumeSlotFromBeforeTheReach] = true
		}
		issues, err := d.records.Issues(d.ctx, tx)
		if err != nil {
			return err
		}
		if len(issues) > 0 {
			d.used[6] = true
		}
		for _, issue := range issues {
			if !record.InWorkflow(issue) {
				continue
			}
			if withinStretches(d.script.flight[issue.Key], d.window.From) {
				d.inFlight[issue.Key] = true
			}
			if first := d.firstSlotBefore(issue.Key); !first.IsZero() {
				d.started[issue.Key] = first
			}
		}
		claims, err := d.st.ClaimsTx(d.ctx, tx)
		if err != nil {
			return err
		}
		for _, c := range claims {
			d.claims[c.Token] = c
		}
		return nil
	})
}

// firstSlotBefore is when key first took a slot in the run it stands in at the window's start,
// zero where it took none before then.
func (d *driver) firstSlotBefore(key string) time.Time {
	iss, ok := d.trace.Issues[key]
	if !ok {
		return time.Time{}
	}
	entry := iss.entryAt(d.window.From.Add(-time.Nanosecond))
	for _, span := range iss.Slots {
		if span.Start.Before(d.window.From) && (entry == nil || !span.Start.Before(entry.At) || span.Before) {
			return span.Start
		}
	}
	return time.Time{}
}

// placeEvent queues the recorded event events[i] at at: its change to the shadow, then its fact.
func (d *driver) placeEvent(i int, at time.Time) {
	event := &d.script.events[i]
	if event.placed {
		return
	}
	event.placed = true
	d.placed[i] = at
	var applied []byte
	d.push(&item{at: at, class: classAct, rec: event.at, order: int64(event.order), eventKey: event.env.key, eventKind: event.env.kind,
		dispatch: func() error {
			var err error
			applied, err = d.dispatchEvent(i)
			return err
		},
		run: func() error { return d.applyEvent(i, applied) }})
	if d.answerWall {
		return
	}
	for _, dependent := range event.dependents {
		d.placeEvent(dependent, at.Add(d.script.events[dependent].offset))
	}
}

// placeAnchored places each event whose anchor, the replay's start of the wait it ended in the
// record, the replay has now reached (Branch.Timing).
func (d *driver) placeAnchored() error {
	kept := d.anchored[:0]
	for _, i := range d.anchored {
		event := &d.script.events[i]
		if event.placed {
			continue
		}
		start, reached, err := d.waitStart(event.wait)
		if err != nil {
			return err
		}
		if !reached {
			kept = append(kept, i)
			continue
		}
		at := start.Add(event.offset)
		if now := d.clock.Instant(); at.Before(now) {
			at = now
		}
		d.placeEvent(i, at)
	}
	d.anchored = kept
	return nil
}

// placeUnbegun places the recorded event events[i], whose place waits for the replay's start of
// the wait it ended (Branch.Timing offset), at its recorded time, the clock's instant, where that
// wait cannot begin (the windows' README, "The knobs": an event whose wait never begins in the
// replay happens at its recorded time): a link wait of an issue the replay has not started, its
// link standing, since an issue whose blocker is unfinished does not start, and so never waits on
// it as a started issue does.
func (d *driver) placeUnbegun(i int) {
	event := &d.script.events[i]
	if event.placed || event.wait.kind != WaitLink {
		return
	}
	if _, started := d.started[event.wait.issue]; started {
		return
	}
	if event.wait.added >= 0 {
		if added, placed := d.placed[event.wait.added]; !placed || added.After(d.clock.Instant()) {
			return
		}
	}
	d.placeEvent(i, d.clock.Instant())
}

// waitStart is the replay's start of the wait an anchor names, and whether the replay has reached
// it: for a link wait, the later of the replay's moment of the event that added the link and the
// waiting issue's start; for a children wait, the moment the replay's pass began it.
func (d *driver) waitStart(wait waitAnchor) (time.Time, bool, error) {
	switch wait.kind {
	case WaitLink:
		started, ok := d.started[wait.issue]
		if !ok {
			return time.Time{}, false, nil
		}
		if wait.added < 0 {
			return started, true, nil
		}
		added, placed := d.placed[wait.added]
		if !placed || added.After(d.clock.Instant()) {
			return time.Time{}, false, nil
		}
		return later(added, started), true, nil
	case WaitChildren:
		began, ok := d.childWait[wait.issue]
		return began, ok, nil
	}
	return time.Time{}, false, nil
}

// later is the later of a and b.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// dispatchEvent applies the recorded event events[i] to the shadow at the clock's instant, and
// answers the event as the replay applies it; nil for an event of the daemon's own, which the
// replay makes again from its own outbox, and which the shadow only notes.
func (d *driver) dispatchEvent(i int) ([]byte, error) {
	event := &d.script.events[i]
	if daemonWrote(event.env.actor, event.env.key) && !d.keepsDaemonEvent(event) {
		d.shadow.note(event.env)
		return nil, nil
	}
	d.used[10] = d.used[10] || d.namesLinkOrChild(event)
	return d.shadow.apply(event.env, d.clock.Instant())
}

// keepsDaemonEvent says whether event, one the daemon's own session wrote, is a write the replay's
// outbox does not make again and so is applied as recorded: an architect's release of a child
// into its tree (release_children), which the daemon's API writes from the architect's call, a
// backlog, icebox or triage child set to todo with no label added. Every other write of the
// daemon's own is its outbox's status write, which the replay's own rows make.
func (d *driver) keepsDaemonEvent(event *scriptEvent) bool {
	after := event.parsed.Issue
	if after == nil {
		return false
	}
	iss, ok := d.shadow.issues[event.env.key]
	if !ok || iss.recorded == nil {
		return false
	}
	var before struct {
		Status string   `json:"status"`
		Labels []string `json:"labels"`
	}
	if !decodeField(iss.recorded, &before) {
		return false
	}
	released := before.Status == "backlog" || before.Status == "icebox" || before.Status == "triage"
	return released && after.Status == "todo" && after.Parent != "" && slices.Equal(after.Labels, before.Labels)
}

// namesLinkOrChild says whether event shows an issue with a link or a parent (assumption 10).
func (d *driver) namesLinkOrChild(event *scriptEvent) bool {
	shown := event.parsed.Issue
	if shown == nil {
		return false
	}
	for _, blocker := range shown.BlockedBy {
		if project, _, _ := cutKey(blocker); project != d.window.Project {
			d.used[25] = true
		}
	}
	return len(shown.BlockedBy) > 0 || shown.Parent != ""
}

// cutKey splits an issue key into its project and number.
func cutKey(key string) (string, string, bool) {
	for i := range len(key) {
		if key[i] == '-' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

// applyEvent applies the event the shadow answered for events[i] (applied) through the daemon's
// decoder, its preparation's stand-in and the chain.
func (d *driver) applyEvent(i int, applied []byte) error {
	if applied == nil {
		return nil
	}
	event := &d.script.events[i]
	fact, _, err := intake.DecodeDispatchEvent(d.window.Project, event.env.key, applied)
	if err != nil {
		return fmt.Errorf("replay: decode the %s event of %s at %s: %w", event.env.kind, event.env.key, stamp(event.at), err)
	}
	if fact == nil {
		return nil
	}
	if event.env.actor.Kind == "user" && answers(fact) {
		d.used[5] = true
	}
	prepared, recorded, err := d.prepare(fact)
	if err != nil {
		return err
	}
	d.countFence(prepared, recorded)
	_, err = d.apply("dispatch", "dispatch-"+strconv.Itoa(event.order), prepared)
	return err
}

// answers says whether fact is a person's answer or review, which moves a root's wait on a person
// (assumption 5).
func answers(fact intake.Fact) bool {
	switch fact.(type) {
	case intake.DispatchArtifact, intake.DispatchAskClosed, intake.DispatchAskReply:
		return true
	}
	return false
}

// boot is a daemon process starting at the record's moment of it: a fresh engine and admission
// pass, the reconcile of the shadow's listing with the holds the record's lines show at that boot
// (the listing's summaries held for the Dispatch consumer, and the roots the boot's catch-up
// holds), the release of each root's catch-up where the record shows it applied, DaemonBooted,
// and then every act that fell while the daemon was down, in order (assumption 24).
func (d *driver) boot(boot Boot) error {
	d.boots++
	d.used[assumeBootRepairsNothing] = true
	d.newChain(boot.At)
	if err := d.serialized(func(ctx context.Context, tx pgx.Tx) error {
		_, err := d.engine.OpenUncountedRuns(ctx, tx)
		return err
	}); err != nil {
		return fmt.Errorf("replay: open the uncounted runs of the boot at %s: %w", stamp(boot.At), err)
	}
	listing := d.shadow.listing()
	listed := make([]intake.DispatchIssue, 0, len(listing))
	for _, summary := range listing {
		fact := intake.ListedIssue(summary)
		prepared, err := d.prepareListed(fact)
		if err != nil {
			return err
		}
		listed = append(listed, prepared)
	}
	// A boot whose listing the record shows held for the Dispatch consumer reads the consumer
	// short of the stream's position, so the summaries ahead of the record wait for the release
	// the record shows; every other boot finds the consumer caught up.
	position, target := intake.DispatchConsumerPosition{Idle: true}, int64(1)
	if !boot.Holding.IsZero() {
		position = intake.DispatchConsumerPosition{}
	}
	var catchingUp []record.Issue
	if err := d.serialized(func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := d.records.TakeDispatchRefill(ctx, tx, d.window.Project); err != nil {
			return err
		}
		if err := blockers.Reconcile(ctx, tx, d.records, d.window.Project, listing); err != nil {
			return err
		}
		var err error
		if catchingUp, err = d.catchesUp(ctx, tx); err != nil {
			return err
		}
		keys := make([]string, len(catchingUp))
		for i, root := range catchingUp {
			keys[i] = root.Key
		}
		d.admission.HoldForCatchUp(keys...)
		return d.admission.Reconcile(ctx, tx, listed, target, position)
	}); err != nil {
		return fmt.Errorf("replay: reconcile the boot at %s: %w", stamp(boot.At), err)
	}
	if err := d.observe(); err != nil {
		return err
	}
	if !boot.Holding.IsZero() && !boot.CaughtUp.IsZero() && boot.CaughtUp.Before(d.window.To) {
		bootNumber := d.boots
		d.push(&item{at: boot.CaughtUp, class: classAct, rec: boot.CaughtUp, order: lineOrder, run: func() error {
			_, err := d.apply("dispatch", d.factID("dispatch-position", d.window.Project, bootNumber),
				intake.DispatchConsumerPosition{AckFloorStream: target, Idle: true})
			return err
		}})
	}
	// Each root the catch-up holds is released where the record shows its catch-up applied, and
	// the boot's DaemonBooted comes after the last of them, as the daemon serves only once its
	// catch-up is done.
	last := boot.At
	for _, root := range catchingUp {
		at := d.caughtUpAt(boot, root.Key)
		last = later(last, at)
		d.push(&item{at: at, class: classAct, rec: at, order: lineOrder - 1, run: func() error { return d.releaseCatchUp(root) }})
	}
	bootNumber := d.boots
	d.push(&item{at: last, class: classAct, rec: last, order: lineOrder, run: func() error {
		if _, err := d.apply("daemon", fmt.Sprintf("boot:%s:%d", d.window.Project, bootNumber), intake.DaemonBooted{Boot: int64(bootNumber)}); err != nil {
			return err
		}
		held := d.held
		d.held = nil
		for _, it := range held {
			if err := it.run(); err != nil {
				return err
			}
			if err := d.drain(); err != nil {
				return err
			}
		}
		return nil
	}})
	return nil
}

// serialized runs write in one transaction that takes the lock every fact applies under, with
// the commit hooks and the run settling a fact has (intake.WithCommitHooks, intake.OpenSettlers),
// as the daemon's boot runs its reconcile and its uncounted runs outside ApplyFact.
func (d *driver) serialized(write func(ctx context.Context, tx pgx.Tx) error) error {
	scoped, committed := intake.WithCommitHooks(d.ctx)
	scoped, settle := intake.OpenSettlers(scoped, d.handlers...)
	if err := pgx.BeginFunc(scoped, d.pool, func(tx pgx.Tx) error {
		if err := intake.SerializeFacts(scoped, tx); err != nil {
			return err
		}
		if err := write(scoped, tx); err != nil {
			return err
		}
		return settle(scoped, tx)
	}); err != nil {
		return err
	}
	committed()
	return nil
}

// catchesUp is every root a boot's catch-up reads, and so holds out of the pass's decision whether
// it waits on a person until its catch-up has applied what Dispatch recorded while the daemon was
// down: a root of its tree, not lingering, that waits on a person, stands at admitted under an open
// gate, or may wait on one (record.CanWaitOnPerson), as the daemon's boot reads them.
func (d *driver) catchesUp(ctx context.Context, tx pgx.Tx) ([]record.Issue, error) {
	issues, err := d.records.Issues(ctx, tx)
	if err != nil {
		return nil, err
	}
	gates, err := record.ReadTreeGates(ctx, tx, d.records, issues, d.window.GateSteps)
	if err != nil {
		return nil, err
	}
	slots, err := d.records.Slots(ctx, tx)
	if err != nil {
		return nil, err
	}
	slotted := make(map[string]bool, len(slots))
	for _, slot := range slots {
		slotted[slot.Issue] = true
	}
	var roots []record.Issue
	for _, issue := range issues {
		if !claim.IsTreeRoot(issue.Key, issue.Tree) || issue.Lingers() {
			continue
		}
		gate := gates[issue.Key]
		if issue.Phase == phase.AwaitingPerson || gate.Open() && issue.Phase == phase.Admitted ||
			record.CanWaitOnPerson(issue, gate, slotted[issue.Key]) {
			roots = append(roots, issue)
		}
	}
	return roots, nil
}

// caughtUpAt is when the record shows the catch-up of root at boot applied: the first gate line of
// the root from the boot on, which is the catch-up's own act on what the downtime left it, or the
// daemon's start of service, whichever is first; the boot itself where the record shows neither.
func (d *driver) caughtUpAt(boot Boot, root string) time.Time {
	next := d.window.To
	for _, other := range d.trace.Boots {
		if other.At.After(boot.At) && other.At.Before(next) {
			next = other.At
		}
	}
	at := time.Time{}
	if iss, ok := d.trace.Issues[root]; ok {
		for _, gate := range iss.Gates {
			if (gate.Kind == GateOpenedFact || gate.Kind == GateClosedFact) && !gate.At.Before(boot.At) && gate.At.Before(next) {
				at = gate.At
				break
			}
		}
	}
	if !boot.Started.IsZero() && (at.IsZero() || boot.Started.Before(at)) {
		at = boot.Started
	}
	if at.IsZero() {
		at = boot.At
	}
	return at
}

// releaseCatchUp ends the catch-up hold of root: the ask, comment and artifact events of it the
// downtime left, which the daemon's catch-up reads (its catchUpWindow), applied first, then the
// release and CatchUpApplied.
func (d *driver) releaseCatchUp(root record.Issue) error {
	var rest []*item
	for _, it := range d.held {
		if it.eventKey == root.Key && caughtUpKind(it.eventKind) {
			if err := it.run(); err != nil {
				return err
			}
			continue
		}
		rest = append(rest, it)
	}
	d.held = rest
	d.admission.ReleaseCatchUp(root.Key)
	through := int64(0)
	if iss, ok := d.shadow.issues[root.Key]; ok {
		through = iss.seq
	}
	_, err := d.apply("daemon", fmt.Sprintf("awaiting-catch-up:caught-up:%s:%d:%d:%d", root.Key, root.Generation, through, d.boots),
		intake.CatchUpApplied{Issue: root.Key, Through: through})
	return err
}

// caughtUpKind says whether a Dispatch event of kind is one a boot's catch-up of a root reads:
// an ask's, a comment's or a document's, the events the boot's listing carries nothing of.
func caughtUpKind(kind string) bool {
	return strings.HasPrefix(kind, "ask.") || strings.HasPrefix(kind, "comment.") || strings.HasPrefix(kind, "artifact.")
}

// tick is the controller's tick at at: Observed read from the shadow by the keys the daemon's own
// selection reads (blockers.WaitedOn, blockers.ReadPlan), as the daemon's tick reads Dispatch.
func (d *driver) tick(at time.Time) error {
	var keys []string
	if err := pgx.BeginTxFunc(d.ctx, d.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		keys, err = blockers.WaitedOn(d.ctx, tx, d.records, d.window.Project, d.window.GateSteps, d.rule, d.now())
		return err
	}); err != nil {
		return fmt.Errorf("replay: read what the tick at %s re-reads: %w", stamp(at), err)
	}
	var observed []intake.IssueStatus
	if len(keys) > 0 {
		listProject, single := blockers.ReadPlan(d.window.Project, keys)
		if listProject {
			for _, summary := range d.shadow.listing() {
				observed = append(observed, blockers.Observed(d.window.Project, summary))
			}
		}
		for _, key := range single {
			if iss, ok := d.shadow.issues[key]; ok && iss.payload != nil && iss.project != "" {
				observed = append(observed, d.shadow.observed(iss))
			}
		}
	}
	_, err := d.apply("controller", fmt.Sprintf("controller-tick:%s:%d:%d", d.window.Project, d.boots, at.UnixNano()),
		intake.ControllerTick{Observed: observed})
	return err
}

// pause is the project's pause the record shows beginning: the replay's own, through the store's
// pause, which holds every promotion and the outbox rows a pause holds, and stops every live claim
// with a pause_stop row (internal/pause's StopRow), which the stub finishes (assumption 28).
func (d *driver) pause(span PauseSpan) error {
	paused, _, err := d.st.Pause(d.ctx, store.PauseRequest{Project: d.window.Project, By: store.PauseActor{Kind: store.PauseByOperator},
		Reason: cmp.Or(span.Reason, "the record's pause"), At: d.now(), StopRow: pause.StopRow})
	if err != nil {
		return fmt.Errorf("replay: pause the project at %s: %w", stamp(span.From), err)
	}
	d.paused, d.pauseID = true, paused.ID
	d.log.Info("pause: the project is paused", "pause", paused.ID, "by", string(store.PauseByOperator), "reason",
		cmp.Or(span.Reason, "the record's pause"))
	return nil
}

// resume ends the replay's pause where the record shows the pause ended, and applies the resume's
// own pass (intake.ProjectResumed).
func (d *driver) resume(span PauseSpan) error {
	if !d.paused {
		return nil
	}
	at := d.now()
	if _, err := d.st.Resume(d.ctx, store.ResumeRequest{Project: d.window.Project, By: store.PauseActor{Kind: store.PauseByOperator},
		At: at, MarkHeld: func(ctx context.Context, tx pgx.Tx, held store.Pause) error {
			_, err := d.records.MarkPauseHeldRows(ctx, tx, d.window.Project, held.ID, d.now())
			return err
		}}); err != nil {
		return fmt.Errorf("replay: resume the project at %s: %w", stamp(span.To), err)
	}
	d.paused = false
	d.log.Info("pause: the project is resumed", "pause", d.pauseID, "by", string(store.PauseByOperator))
	_, err := d.apply("pause", fmt.Sprintf("project-resumed:%s:%d", d.window.Project, d.pauseID), intake.ProjectResumed{})
	return err
}
