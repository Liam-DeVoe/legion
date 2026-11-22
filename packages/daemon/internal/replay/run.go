// Package replay replays a recorded window of a Legion daemon's project (Window) through the
// daemon's own handler chain (admit.Handlers: the Dispatch mirror, the workflow engine and the
// admission pass) under a named slot rule (record.SlotRule), on a simulated clock and in a
// database of its own, and measures the record and each replay with one code (Scorecard). It is
// what `legion replay` runs: a rule is judged by the daemon's own decisions over a window the
// daemon lived through, not by a copy of its rules.
package replay

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// RunConfig is one replay of a window: the window, the rule and branch it runs under, and where
// it runs and writes.
type RunConfig struct {
	// Window is the recorded window (ReadWindow).
	Window Window
	// Rule is the slot rule the replay's admission pass and engine run (record.SlotRuleNamed).
	Rule record.SlotRule
	// Branch is the value of each knob the record does not give.
	Branch Branch
	// Seed seeds every draw the replay makes: the experiments' and the conflicts'.
	Seed int64
	// Slowdown is the factor f by which a work segment's recorded length is scaled for each 42
	// issues in flight above the record's; 1.0 keeps the recorded lengths.
	Slowdown float64
	// PGDSN is a Postgres server the replay may create a database on. The replay writes only the
	// database it makes there, never the one the DSN names.
	PGDSN string
	// Out is the directory the replay writes to: log.jsonl, the daemon's lines stamped with the
	// simulated time; scorecard.json; run.json; and segments.jsonl under TraceSegments.
	Out string
	// KeepDB keeps the replay's database after the run, where it is otherwise dropped.
	KeepDB bool
	// TraceSegments writes each work segment's n, m and length (Segment) to Out and to the result.
	TraceSegments bool

	// afterFact, where set, runs after each fact the replay applies, with the replay's pool and
	// its copy of Dispatch, between facts: a test's look at the replay's state as each pass leaves
	// it (TestTheShadowMatchesTheMirrorAfterEveryFact).
	afterFact func(ctx context.Context, pool *store.Pool, shadow *shadow) error
	// atStart, where set, runs once, before the replay's first fact, with its copy of Dispatch
	// brought to the record's state at the window's start: a test's look at the copy the window
	// starts from (TestAPlacedIssuesDispatchWritesApplyAfterAMidwayStart).
	atStart func(shadow *shadow)
	// atSettled, where set, runs at each instant the snapshot timeline takes a state at, once
	// every act of the instant has run, with the replay's store and the instant: a test's look at
	// the replay's database at the moments its timeline reads (TestReplayStatesAreTheProjectionsStates).
	atSettled func(ctx context.Context, st *store.Store, at time.Time) error
	// noSkip, where set, has the stub run every try of a row it gave back to wait on its backoff,
	// as the daemon's lanes do, rather than skip the tries no change of state can end
	// (skipWaits): the run a test compares the skip's against (TestTheSkipOfAWaitingRowChangesNoOutput).
	noSkip bool
}

// RunResult is what one replay made and measured.
type RunResult struct {
	// Database is the name of the database the replay made on RunConfig.PGDSN's server
	// (legion_replay_<run id>), which is dropped at the end of the run unless RunConfig.KeepDB
	// kept it.
	Database string
	// Scorecard is the replay's measures, read from its snapshot timeline (design 10.1 (a)): the
	// states the daemon's own admission passes decided from, as each pass's observer handed them
	// over, and the claims the worker stub holds. The run fills what it names of itself (its
	// source, rule, branch, seed, slowdown, span, unplaced count, the assumptions it used and its
	// scenario); the measures are Measure's.
	Scorecard Scorecard
	// LineScorecard is the same measures read from the replay's line timeline (design 10.1 (b)):
	// BuildTrace over the replay's own log and its own copy of Dispatch's events, read through the
	// window's line kinds as a record's scorecard is (Score), with the same run's own names.
	LineScorecard Scorecard
	// Gaps is each measure of the two scorecards side by side (Gaps): the error of reading a
	// course from its lines, which a record's scorecard rests on.
	Gaps []MeasureGap
	// Segments is each work segment the replay ran, under RunConfig.TraceSegments.
	Segments []Segment
	// Unplaced is each issue the start state could not place at the window's start.
	Unplaced []Unplaced
	// Divergence is what the replay counted of where it left the record's course: refused moves
	// with their reasons, the recorded conflicts it kept for a loop open at the window's end,
	// reassertion starts, events a sequence fence dropped, the outbox rows finished under each
	// assumption, the recorded turns it wrote no claim of, and what its start state counted of the
	// slots the trace inferred and of the record's own count (design 10.9).
	Divergence Divergence
	// Assumptions is every assumption the run rests on, by number: the spec's and the plan's,
	// which the scorecard names too, and the tool's own (26 on), under which it left a fact or a
	// turn out, finished an outbox row without acting, seeded a slot the trace inferred or kept a
	// recorded conflict, which run.json names beside them.
	Assumptions []int
	// Conflicts is the conflict model the run drew each stretch's conflict by (spec R2): fitted on
	// the recorded stretches inside the window (windowConflicts) with each coefficient's interval,
	// its merges term set to zero where the branch's Merges is zero, and how many stretches, of the
	// window's and of the replay's, counted a merge in M that may be their own.
	Conflicts ConflictModel

	// snapshot is the snapshot timeline Scorecard is read from, which a test reads states from.
	snapshot Timeline
	// triesRun is how many tries of rows given back to wait on their backoff the stub ran, of the
	// Divergence.Retried tries it counted: the rest it skipped (skipWaits).
	triesRun int
}

// Segment is one work segment of an issue in a replay: the phase it worked, when it started, the
// issues in flight at its start in the replay (N) and in the record (M), its recorded length and
// the length the replay gave it, Recorded × f^((N − M)/42).
type Segment struct {
	Issue    string      `json:"issue"`
	Phase    phase.Phase `json:"phase"`
	Start    time.Time   `json:"start"`
	N        int         `json:"n"`
	M        int         `json:"m"`
	Recorded Duration    `json:"recorded"`
	Length   Duration    `json:"length"`
}

// Unplaced is an issue the replay's start state could not place at the window's start, and why.
type Unplaced struct {
	Issue  string `json:"issue"`
	Reason string `json:"reason"`
}

// The files a run writes into RunConfig.Out beside its log (logFile): its scorecard, read from its
// snapshot timeline, the same read from its line timeline, what it ran and counted, and its
// segments under TraceSegments.
const (
	scorecardFile     = "scorecard.json"
	lineScorecardFile = "scorecard-line.json"
	runFile           = "run.json"
	segmentsFile      = "segments.jsonl"
)

// specAssumptionsTop is the highest number of an assumption of the spec and the plan (1 to 25),
// the ones a scorecard names; the tool's own after them (26 on) are named in run.json alone.
const specAssumptionsTop = 25

// Run replays cfg.Window under cfg.Rule and cfg.Branch. It makes a database of its own on
// cfg.PGDSN's server, migrated with the daemon's migrations; applies every fact of the window —
// the project's Dispatch events, the recorded phase moves, the ticks and the boots — each in one
// pass of the daemon's handler chain (intake.ApplyFact over admit.Handlers), with the rule set on
// the engine and the admission pass and one simulated clock set on the engine, the admission
// pass and the store; runs a worker stub over the outbox rows the daemon's lanes would take after
// each fact; and measures the replay twice (design 10.1): from the states its admission passes
// left and its stub's claims (the snapshot timeline, RunResult.Scorecard), and from the log and
// the Dispatch events it wrote, read back as a record is read (the line timeline,
// RunResult.LineScorecard), with the gap between the two per measure. It makes no request
// outside the database: no Dispatch, no GitHub and no NATS.
//
// The branch's knobs (design section 7) shape the record's work before the replay runs it: the
// anchors of the events that end a wait and of a person's answers (Timing, Answer), the length of
// the work after a send-back (AfterSendBack), the ranks the replay's copy of Dispatch shows
// (FirstStarts, design 10.7), the work of an issue the record never started (NeverStarted), and
// the conflict model each stretch draws by (Merges), fitted on the recorded stretches inside the
// window.
func Run(ctx context.Context, cfg RunConfig) (RunResult, error) {
	began := time.Now()
	w := cfg.Window
	if cfg.Branch == (Branch{}) {
		cfg.Branch = MainBranch
	}
	if err := cfg.Branch.check(); err != nil {
		return RunResult{}, err
	}
	cfg.Branch = cfg.Branch.orMain()
	if cfg.Slowdown <= 0 {
		return RunResult{}, fmt.Errorf("replay: the slowdown factor is %v; it must be above zero (1 keeps the recorded lengths)", cfg.Slowdown)
	}
	if cfg.Out == "" {
		return RunResult{}, errors.New("replay: no directory to write the run's output to")
	}
	trace, err := BuildTrace(w)
	if err != nil {
		return RunResult{}, err
	}
	lines, err := readRecordLines(w)
	if err != nil {
		return RunResult{}, err
	}
	script, err := buildScript(trace, w, lines)
	if err != nil {
		return RunResult{}, err
	}
	if cfg.Branch.AfterSendBack == lessSlotWait {
		script.cutSlotWaits()
	}
	if cfg.Branch.NeverStarted != MainBranch.NeverStarted {
		script.drawNeverStarted(trace, cfg.Branch.NeverStarted, rand.New(rand.NewPCG(uint64(cfg.Seed), neverStartedStream)))
	}
	ranks := firstStartRanks(trace)
	if cfg.Branch.FirstStarts == firstStartsRank {
		ranks = nil
	}
	merges := newBaseMerges(w, trace)
	conflicts := windowConflicts(trace, merges)
	if cfg.Branch.Merges == mergesZero {
		conflicts = conflicts.WithoutTerm()
	}
	token, err := claim.ProjectToken(w.Project)
	if err != nil {
		return RunResult{}, fmt.Errorf("replay: the claim token of %s: %w", w.Project, err)
	}
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		return RunResult{}, fmt.Errorf("replay: make the run's output directory: %w", err)
	}
	logged, err := os.Create(filepath.Join(cfg.Out, logFile))
	if err != nil {
		return RunResult{}, fmt.Errorf("replay: open the run's log: %w", err)
	}
	defer func() { _ = logged.Close() }()
	written := bufio.NewWriter(logged)

	db, st, err := openReplayDatabase(ctx, cfg.PGDSN)
	if err != nil {
		return RunResult{}, err
	}
	result := RunResult{Database: db.name, Conflicts: conflicts}
	closeDB := func() error {
		st.Close()
		if cfg.KeepDB {
			return nil
		}
		return db.drop(ctx)
	}

	clock := newSimClock(w.From)
	st.UseClock(clock.Now)
	tap := newLineTap(tapped...)
	d := &driver{
		ctx: ctx, cfg: cfg, window: w, trace: trace, script: script, lines: lines, rule: cfg.Rule.OrMain(),
		clock: clock, st: st, pool: st.Pool(), records: record.NewStoreWithClock(clock.Now), tap: tap,
		log:    slog.New(&clockHandler{inner: slog.NewJSONHandler(written, &slog.HandlerOptions{Level: slog.LevelInfo}), clock: clock, tap: tap}),
		shadow: newShadow(w.Project, ranks), token: token,
		claims: map[claim.Token]supervise.Claim{}, working: map[claim.Token]*piece{},
		inFlight: map[string]bool{}, started: map[string]time.Time{}, childWait: map[string]time.Time{}, placed: map[int]time.Time{},
		signOffs: map[string]bool{}, drops: map[string]string{}, used: map[int]bool{1: true},
		snap:     newSnapshots(w.From, w.To, ranksOf(trace)),
		counters: Divergence{Refused: []RefusedMove{}, Finished: map[int]int{}, TurnsLeftOut: map[int]int{}},
		waiting:  map[int64]waitingRow{}, rankMoments: rankChanges(trace),
	}
	d.conflicts, d.merges, d.loops, d.stretchFrom = conflicts, merges, recordedLoops(script), map[string]time.Time{}
	d.draws, d.numbers = rand.New(rand.NewPCG(uint64(cfg.Seed), conflictStream)), newPullRequestNumbers(trace)
	d.timingWall, d.answerWall = cfg.Branch.Timing == valueWall, cfg.Branch.Answer == valueWall
	runErr := d.run()
	flushErr := written.Flush()
	if err := errors.Join(runErr, flushErr); err != nil {
		return result, errors.Join(err, closeDB())
	}
	if err := closeDB(); err != nil {
		return result, err
	}
	d.traceAssumptions()
	// What the replay's own passes judged names two more: 4 where an issue waited on another issue
	// (a gate's closing names it as the replay writes it, observe), and 7 where a hold other than
	// the cap held the first start of an issue not yet started.
	if d.snap.waitsOnIssues() {
		d.used[4] = true
	}
	if d.snap.firstStartHeld {
		d.used[7] = true
	}
	result.Unplaced, result.Divergence, result.Assumptions = d.unplaced, d.counters, d.assumptionsUsed(len(Assumptions))
	result.Conflicts = d.conflicts
	if cfg.TraceSegments {
		result.Segments = d.segments
	}
	result.snapshot, result.triesRun = d.snap.timelineAt(), d.triesRun
	// An issue that waits for a slot at the start began that wait where the record shows, its start
	// state being the record's.
	result.snapshot.beginWaitsAt(recordedWaitStarts(trace, ranksOf(trace), trace.ruleAt, w.From, result.snapshot.startWaiters()))
	result.Scorecard = d.scorecard(Measure(result.snapshot))
	lined, err := d.lineTimeline()
	if err != nil {
		return result, err
	}
	result.LineScorecard = d.scorecard(Measure(lined))
	result.Gaps = Gaps(result.Scorecard, result.LineScorecard)
	if err := d.writeOutputs(result, time.Since(began)); err != nil {
		return result, err
	}
	return result, nil
}

// traceAssumptions names the assumptions the run rests on that the window's own record shows
// rather than a fact the replay applied: 8 where it holds an issue handed to Legion that the
// record never started, 15 where work is left in flight at its end, 16 where an issue reopens, 17
// where a child is filed after its parent started, and 18 where it records a conflict.
func (d *driver) traceAssumptions() {
	w := d.window
	within := func(at time.Time) bool { return !at.Before(w.From) && at.Before(w.To) }
	if len(d.inFlight) > 0 {
		d.used[15] = true
	}
	for _, key := range sortedKeys(d.trace.Issues) {
		iss := d.trace.Issues[key]
		if iss.Project != d.trace.Project {
			continue
		}
		for _, entry := range iss.Entries {
			if entry.At.Before(w.To) && d.neverStarted(key) {
				d.used[8] = true
			}
			if entry.Reopen && within(entry.At) {
				d.used[16] = true
			}
		}
		for _, filing := range iss.Children {
			if !filing.Moved && within(filing.At) && startedBy(iss, filing.At) {
				d.used[17] = true
			}
		}
		for _, visit := range iss.Visits {
			if visit.Out != nil && visit.Out.Trigger == workflow.TriggerConflict && within(visit.Out.At) {
				d.used[18] = true
			}
		}
	}
}

// startedBy says whether iss had started by at: it held a slot or a worker of it had started.
func startedBy(iss *IssueTrace, at time.Time) bool {
	for _, span := range iss.Slots {
		if span.Start.Before(at) {
			return true
		}
	}
	return slices.ContainsFunc(iss.Visits, func(visit Visit) bool { return !visit.WorkerStart.IsZero() && visit.WorkerStart.Before(at) })
}

// assumptionsUsed is the numbers of the assumptions the run used, in order, up to top.
func (d *driver) assumptionsUsed(top int) []int {
	used := []int{}
	for _, number := range slices.Sorted(maps.Keys(d.used)) {
		if d.used[number] && number <= top {
			used = append(used, number)
		}
	}
	return used
}

// scorecard is card, measured from one of the replay's timelines, with what the run itself names:
// its source, rule, branch, seed, slowdown and span, the issues its start state could not place,
// the assumptions of the spec and the plan its measures rest on, and its never-started scenario.
func (d *driver) scorecard(card Scorecard) Scorecard {
	card.Source, card.Rule, card.Branch, card.Seed, card.Slowdown = "replay", d.rule.Name, d.cfg.Branch.Name, d.cfg.Seed, d.cfg.Slowdown
	card.From, card.To, card.Unplaced = d.window.From, d.window.To, len(d.unplaced)
	card.Assumptions = d.assumptionsUsed(specAssumptionsTop)
	card.Scenario = cmp.Or(d.cfg.Branch.NeverStarted, MainBranch.NeverStarted)
	return card
}

// laterLines is the line kinds a later release writes where an older one wrote other lines that
// BuildTrace reads the same facts from, the trace reading each where the window holds its kind and
// the older lines where it does not: the record line of a pass that moves slots and the hold's
// beginning (passMovedSlotsLine, phaseHeldLine).
var laterLines = []string{passMovedSlotsLine, phaseHeldLine}

// lineTimeline is the replay's line timeline (design 10.1 (b)): BuildTrace over a window that holds
// the record's lines and events from before the window's start, which the replay's start state
// was built from, and from the start on the replay's own log and the events its copy of Dispatch
// applied, recorded and its own alike: each issue's log past the recorded events that brought the
// copy to the window's start (shadowIssue.seeded), which the record's own events before the start
// stand for here. Its log from the start on is read as written under the rule the replay ran
// (buildTrace), and it is measured by the record's true ranks (design 10.7) under that rule.
//
// The replay's log is read through the window's line kinds as the record is, so a later kind the
// replay's daemon writes in place of the older lines a window holds (laterLines) is left out, and
// the record and the replay are read by one path. Every other kind the trace reads is read where the
// replay wrote it: a window lacks such a kind only because nothing it holds wrote one (a window in
// which no slot went back holds no `admission: slot released`), and the replay's line is of what the
// replay did.
func (d *driver) lineTimeline() (Timeline, error) {
	w := d.window
	lined := w
	lined.Log, lined.Events = nil, make(map[string][]json.RawMessage, len(w.Events)+len(d.shadow.issues))
	lined.Lines = slices.Clone(w.Lines)
	for i, raw := range w.Log {
		var head struct {
			Time time.Time `json:"time"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return Timeline{}, fmt.Errorf("replay: line %d of the window's log has no time: %w", i+1, err)
		}
		if head.Time.Before(w.From) {
			lined.Log = append(lined.Log, raw)
		}
	}
	replayed, err := os.ReadFile(filepath.Join(d.cfg.Out, logFile))
	if err != nil {
		return Timeline{}, fmt.Errorf("replay: read the run's own log back: %w", err)
	}
	for i, line := range bytes.Split(replayed, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var head struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			return Timeline{}, fmt.Errorf("replay: line %d of the run's own log is no JSON object: %w", i+1, err)
		}
		if TraceReads(head.Msg) && !slices.Contains(lined.Lines, head.Msg) && !slices.Contains(laterLines, head.Msg) {
			lined.Lines = append(lined.Lines, head.Msg)
		}
		lined.Log = append(lined.Log, json.RawMessage(line))
	}
	for key, events := range w.Events {
		for i, raw := range events {
			event, err := ParseDispatchEvent(raw)
			if err != nil {
				return Timeline{}, fmt.Errorf("replay: event %d of %s: %w", i+1, key, err)
			}
			if event.At.Before(w.From) {
				lined.Events[key] = append(lined.Events[key], raw)
			}
		}
	}
	for key, iss := range d.shadow.issues {
		lined.Events[key] = append(lined.Events[key], iss.log[iss.seeded:]...)
	}
	rule := d.rule
	t, err := buildTrace(lined, &rule)
	if err != nil {
		return Timeline{}, fmt.Errorf("replay: read the run's own log and events back as a record: %w", err)
	}
	return lineTimeline(t, ranksOf(d.trace), func(time.Time) record.SlotRule { return d.rule }), nil
}

// runRecord is a run's run.json: what it ran — the rule, the branch, the seed, the slowdown, the
// scenario and every assumption it used by number — and what it counted: the facts it applied and
// the passes they ran, the mean time of a pass, the run's wall time and peak memory, the issues the
// start state could not place, the segments it ran, its divergence counters, the gap of each
// measure between its snapshot and line timelines, and the conflict model it drew by. It holds
// numbers and names alone, never an assumption's text.
type runRecord struct {
	Rule            string        `json:"rule"`
	RuleCommit      string        `json:"ruleCommit"`
	Branch          Branch        `json:"branch"`
	Seed            int64         `json:"seed"`
	Slowdown        float64       `json:"slowdown"`
	Scenario        string        `json:"scenario"`
	Assumptions     []int         `json:"assumptions"`
	Database        string        `json:"database"`
	KeptDatabase    bool          `json:"keptDatabase"`
	Facts           int           `json:"facts"`
	Passes          int           `json:"passes"`
	MeanPassMillis  float64       `json:"meanPassMillis"`
	WallSeconds     float64       `json:"wallSeconds"`
	PeakMemoryBytes uint64        `json:"peakMemoryBytes"`
	Unplaced        int           `json:"unplaced"`
	Segments        int           `json:"segments"`
	Divergence      Divergence    `json:"divergence"`
	Gaps            []MeasureGap  `json:"gaps"`
	Conflicts       ConflictModel `json:"conflicts"`
}

// writeOutputs writes the run's scorecard.json (its snapshot timeline's), scorecard-line.json (its
// line timeline's) and run.json into its output directory, and its segments.jsonl under
// TraceSegments.
func (d *driver) writeOutputs(result RunResult, wall time.Duration) error {
	mean := 0.0
	if d.facts > 0 {
		mean = float64(d.passTime.Microseconds()) / 1000 / float64(d.facts)
	}
	ran := runRecord{Rule: d.rule.Name, RuleCommit: d.rule.Commit, Branch: d.cfg.Branch, Seed: d.cfg.Seed, Slowdown: d.cfg.Slowdown,
		Scenario: result.Scorecard.Scenario, Assumptions: d.assumptionsUsed(len(Assumptions)), Database: result.Database,
		KeptDatabase: d.cfg.KeepDB, Facts: d.facts, Passes: d.facts + d.boots, MeanPassMillis: mean, WallSeconds: wall.Seconds(),
		PeakMemoryBytes: peakMemory(), Unplaced: len(result.Unplaced), Segments: len(d.segments), Divergence: result.Divergence,
		Gaps: result.Gaps, Conflicts: result.Conflicts}
	for name, value := range map[string]any{scorecardFile: result.Scorecard, lineScorecardFile: result.LineScorecard, runFile: ran} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Errorf("replay: write the run's %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(d.cfg.Out, name), append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("replay: write the run's %s: %w", name, err)
		}
	}
	if !d.cfg.TraceSegments {
		return nil
	}
	var segments bytes.Buffer
	for _, segment := range d.segments {
		line, err := json.Marshal(segment)
		if err != nil {
			return fmt.Errorf("replay: write the run's %s: %w", segmentsFile, err)
		}
		segments.Write(append(line, '\n'))
	}
	if err := os.WriteFile(filepath.Join(d.cfg.Out, segmentsFile), segments.Bytes(), 0o644); err != nil {
		return fmt.Errorf("replay: write the run's %s: %w", segmentsFile, err)
	}
	return nil
}

// peakMemory is the most memory the process has held, in bytes: the kernel's high-water mark of
// its resident set where /proc shows it, and otherwise what the Go runtime has taken from the
// system.
func peakMemory() uint64 {
	if status, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(status), "\n") {
			if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
				if kib, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64); err == nil {
					return kib * 1024
				}
			}
		}
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.Sys
}

// withRanks is t with each issue of ranks shown at its rank there in every Dispatch event of it,
// the ranks the replay's copy of Dispatch shows (firstStartRanks), so the start state records the
// ranks the replay's events carry; t itself where ranks is empty.
func withRanks(t Trace, ranks map[string]string) Trace {
	if len(ranks) == 0 {
		return t
	}
	shown := t
	shown.Issues = make(map[string]*IssueTrace, len(t.Issues))
	for key, iss := range t.Issues {
		rank, ranked := ranks[key]
		if !ranked {
			shown.Issues[key] = iss
			continue
		}
		copied := *iss
		copied.Dispatch = slices.Clone(iss.Dispatch)
		for i := range copied.Dispatch {
			if state := copied.Dispatch[i].Issue; state != nil {
				reranked := *state
				reranked.Rank = rank
				copied.Dispatch[i].Issue = &reranked
			}
		}
		shown.Issues[key] = &copied
	}
	return shown
}

// firstStartRanks is the input transform of the main branch's FirstStarts, recorded (design 10.7):
// each issue of the project that the record started in the window, by its first slot that begins
// at or after the window's start, is shown at a rank that orders it by that first start, below
// every rank Dispatch gave any issue of the window. A slot the issue held at the start from before
// it is no start in the window, nor is a slot of an earlier run. The line orders the issues whose
// first phase can start by their tree root's rank and then their own (record.Waiting), so each
// tree's root is shown at the earliest first start of its tree, its own or that of a member of the
// tree its entry stood in at that start, where that comes before the root's own or the root has
// none: a root in flight at the start ranks by its first child's start, and its children follow in
// their recorded order. Every other issue keeps its own rank, after them: a root that left the
// workflow before the window and was set back to todo waits at its own rank behind the issues the
// record started, as it did in the record, rather than at the head of the line with no recorded
// work left to open. So the replay starts the issues in the recorded order where the rule lets it,
// but for the one case root-first ordering cannot keep: a later child of an earlier tree that waits
// at the same pass as a later tree's first start goes first by its root's rank, though the record
// started the later tree first where the board ranked that tree higher. The measures read the true
// ranks from the trace. Nil where no issue started in the window or no rank sorts below the
// window's lowest.
func firstStartRanks(t Trace) map[string]string {
	type first struct {
		key, tree string
		at        time.Time
		rank      string
	}
	var started []first
	lowest, ranked := "", false
	for _, key := range sortedKeys(t.Issues) {
		iss := t.Issues[key]
		rank := ""
		for _, event := range iss.Dispatch {
			if event.Issue != nil {
				rank = event.Issue.Rank
				if !ranked || rank < lowest {
					lowest, ranked = rank, true
				}
			}
		}
		if iss.Project != t.Project {
			continue
		}
		at := slices.IndexFunc(iss.Slots, func(span SlotSpan) bool { return !span.Before && !span.Start.Before(t.From) })
		if at < 0 {
			continue
		}
		start, tree := iss.Slots[at].Start, key
		if entry := iss.entryAt(start); entry != nil && entry.Tree != "" {
			tree = entry.Tree
		}
		started = append(started, first{key: key, tree: tree, at: start, rank: rank})
	}
	if len(started) == 0 || lowest == "" || lowest[0] <= ' '+1 {
		return nil
	}
	prefix := "0"
	if lowest[0] <= '0' {
		prefix = string(rune(lowest[0] - 1))
	}
	slices.SortStableFunc(started, func(x, y first) int {
		return cmp.Or(x.at.Compare(y.at), cmp.Compare(x.rank, y.rank), cmp.Compare(x.key, y.key))
	})
	width := len(strconv.Itoa(len(started)))
	// In the order of the first starts the first rank given a key is the earliest of its own and,
	// for a tree's root, its members'.
	ranks := make(map[string]string, len(started))
	for i, issue := range started {
		rank := fmt.Sprintf("%s%0*d", prefix, width, i+1)
		if _, shown := ranks[issue.key]; !shown {
			ranks[issue.key] = rank
		}
		if root, known := t.Issues[issue.tree]; known && root.Project == t.Project {
			if _, shown := ranks[issue.tree]; !shown {
				ranks[issue.tree] = rank
			}
		}
	}
	return ranks
}
