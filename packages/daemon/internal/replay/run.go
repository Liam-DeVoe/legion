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
}

// RunResult is what one replay made and measured.
type RunResult struct {
	// Database is the name of the database the replay made on RunConfig.PGDSN's server
	// (legion_replay_<run id>), which is dropped at the end of the run unless RunConfig.KeepDB
	// kept it.
	Database string
	// Scorecard is the replay's measures, read from its own log. The run fills what it names of
	// itself (its source, rule, branch, seed, slowdown, span, unplaced count, the assumptions it
	// used and its scenario); the measures are the measures' own code's to fill.
	Scorecard Scorecard
	// Segments is each work segment the replay ran, under RunConfig.TraceSegments.
	Segments []Segment
	// Unplaced is each issue the start state could not place at the window's start.
	Unplaced []Unplaced
	// Divergence is what the replay counted of where it left the record's course: refused moves
	// with their reasons, reassertion starts, events a sequence fence dropped, and the outbox rows
	// finished under each assumption (design 10.9).
	Divergence Divergence
	// Assumptions is every assumption the run rests on, by number: the spec's and the plan's,
	// which the scorecard names too, and the tool's own (26 on), under which it left a fact out or
	// finished an outbox row without acting, which run.json names beside them.
	Assumptions []int
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

// The files a run writes into RunConfig.Out beside its log (logFile): its scorecard, what it ran
// and counted, and its segments under TraceSegments.
const (
	scorecardFile = "scorecard.json"
	runFile       = "run.json"
	segmentsFile  = "segments.jsonl"
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
// each fact; and measures the replay from the log it writes. It makes no request outside the
// database: no Dispatch, no GitHub and no NATS.
//
// It runs the main branch (MainBranch) and draws no conflict: a knob set to another value is
// refused, with its name, where the window gives it something to decide.
func Run(ctx context.Context, cfg RunConfig) (RunResult, error) {
	began := time.Now()
	w := cfg.Window
	if cfg.Branch == (Branch{}) {
		cfg.Branch = MainBranch
	}
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
	if firstStarts := cmp.Or(cfg.Branch.FirstStarts, MainBranch.FirstStarts); firstStarts != MainBranch.FirstStarts {
		return RunResult{}, unbuiltKnob("firstStarts", firstStarts, MainBranch.FirstStarts)
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
	result := RunResult{Database: db.name}
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
		shadow: newShadow(w.Project, firstStartRanks(trace)), token: token,
		claims: map[claim.Token]supervise.Claim{}, working: map[claim.Token]*piece{},
		inFlight: map[string]bool{}, started: map[string]time.Time{}, childWait: map[string]time.Time{}, placed: map[int]time.Time{},
		signOffs: map[string]bool{}, used: map[int]bool{1: true},
		counters: Divergence{Refused: []RefusedMove{}, Finished: map[int]int{}},
	}
	runErr := d.run()
	flushErr := written.Flush()
	if err := errors.Join(runErr, flushErr); err != nil {
		return result, errors.Join(err, closeDB())
	}
	if err := closeDB(); err != nil {
		return result, err
	}
	d.traceAssumptions()
	result.Unplaced, result.Divergence, result.Assumptions = d.unplaced, d.counters, d.assumptionsUsed(len(Assumptions))
	if cfg.TraceSegments {
		result.Segments = d.segments
	}
	result.Scorecard = d.scorecard()
	if err := d.writeOutputs(result, time.Since(began)); err != nil {
		return result, err
	}
	return result, nil
}

// unbuiltKnob is the refusal of a branch knob set to a value this replay does not run: it runs the
// main branch's value alone.
func unbuiltKnob(knob, value, main string) error {
	return fmt.Errorf("replay: the branch sets %s to %q, and this replay runs %q alone, the main branch's", knob, value, main)
}

// refuseUnbuiltKnobs refuses the run where its branch sets a knob to a value this replay does not
// run and the window gives that knob something to decide: Timing where an event ends a wait in
// the record, Answer where a person answers a question, AfterSendBack where work follows a send-back,
// Merges where a stretch's head can conflict.
func (d *driver) refuseUnbuiltKnobs() error {
	branch := d.cfg.Branch
	for _, event := range d.script.events {
		if event.at.Before(d.window.From) {
			continue
		}
		switch event.anchor {
		case anchorWait:
			if timing := cmp.Or(branch.Timing, MainBranch.Timing); timing != MainBranch.Timing {
				return unbuiltKnob("timing", timing, MainBranch.Timing)
			}
		case anchorEvent:
			if answer := cmp.Or(branch.Answer, MainBranch.Answer); answer != MainBranch.Answer {
				return unbuiltKnob("answer", answer, MainBranch.Answer)
			}
		}
	}
	for _, key := range sortedKeys(d.script.pieces) {
		for _, p := range d.script.pieces[key] {
			if p.visit == nil {
				continue
			}
			if in := p.visit.In; in != nil && phase.MovesBack(in.From, in.To) {
				if after := cmp.Or(branch.AfterSendBack, MainBranch.AfterSendBack); after != MainBranch.AfterSendBack {
					return unbuiltKnob("afterSendBack", after, MainBranch.AfterSendBack)
				}
			}
			if out := p.visit.Out; out != nil && out.Source.Way == MoveDraw {
				if merges := cmp.Or(branch.Merges, MainBranch.Merges); merges != MainBranch.Merges {
					return unbuiltKnob("merges", merges, MainBranch.Merges)
				}
			}
		}
	}
	return nil
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

// scorecard is the part of the replay's Scorecard the run itself names: its source, rule, branch,
// seed, slowdown and span, the issues its start state could not place, the assumptions of the
// spec and the plan its measures rest on, and its never-started scenario. The measures are left
// to the measures' own code, which reads the run's log.
func (d *driver) scorecard() Scorecard {
	return Scorecard{Source: "replay", Rule: d.rule.Name, Branch: d.cfg.Branch.Name, Seed: d.cfg.Seed, Slowdown: d.cfg.Slowdown,
		From: d.window.From, To: d.window.To, Unplaced: len(d.unplaced), Assumptions: d.assumptionsUsed(specAssumptionsTop),
		Scenario: cmp.Or(d.cfg.Branch.NeverStarted, MainBranch.NeverStarted)}
}

// runRecord is a run's run.json: what it ran — the rule, the branch, the seed, the slowdown, the
// scenario and every assumption it used by number — and what it counted: the facts it applied and
// the passes they ran, the mean time of a pass, the run's wall time and peak memory, the issues the
// start state could not place, the segments it ran and its divergence counters. It holds numbers
// and names alone, never an assumption's text.
type runRecord struct {
	Rule            string     `json:"rule"`
	RuleCommit      string     `json:"ruleCommit"`
	Branch          Branch     `json:"branch"`
	Seed            int64      `json:"seed"`
	Slowdown        float64    `json:"slowdown"`
	Scenario        string     `json:"scenario"`
	Assumptions     []int      `json:"assumptions"`
	Database        string     `json:"database"`
	KeptDatabase    bool       `json:"keptDatabase"`
	Facts           int        `json:"facts"`
	Passes          int        `json:"passes"`
	MeanPassMillis  float64    `json:"meanPassMillis"`
	WallSeconds     float64    `json:"wallSeconds"`
	PeakMemoryBytes uint64     `json:"peakMemoryBytes"`
	Unplaced        int        `json:"unplaced"`
	Segments        int        `json:"segments"`
	Divergence      Divergence `json:"divergence"`
}

// writeOutputs writes the run's scorecard.json and run.json into its output directory, and its
// segments.jsonl under TraceSegments.
func (d *driver) writeOutputs(result RunResult, wall time.Duration) error {
	mean := 0.0
	if d.facts > 0 {
		mean = float64(d.passTime.Microseconds()) / 1000 / float64(d.facts)
	}
	ran := runRecord{Rule: d.rule.Name, RuleCommit: d.rule.Commit, Branch: d.cfg.Branch, Seed: d.cfg.Seed, Slowdown: d.cfg.Slowdown,
		Scenario: result.Scorecard.Scenario, Assumptions: d.assumptionsUsed(len(Assumptions)), Database: result.Database,
		KeptDatabase: d.cfg.KeepDB, Facts: d.facts, Passes: d.facts + d.boots, MeanPassMillis: mean, WallSeconds: wall.Seconds(),
		PeakMemoryBytes: peakMemory(), Unplaced: len(result.Unplaced), Segments: len(d.segments), Divergence: result.Divergence}
	for name, value := range map[string]any{scorecardFile: result.Scorecard, runFile: ran} {
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
// each issue of the project that the record started, by its first slot, is shown at a rank that
// orders it by that first start, below every rank Dispatch gave any issue of the window, and every
// issue the record never started keeps its own rank, after them. The line orders the issues whose
// first phase can start by their tree root's rank and then their own (record.Waiting), so the
// replay starts them in the recorded order where the rule lets it; the measures read the true
// ranks from the trace. Nil where no issue started or no rank sorts below the window's lowest.
func firstStartRanks(t Trace) map[string]string {
	type first struct {
		key  string
		at   time.Time
		rank string
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
		if iss.Project != t.Project || len(iss.Slots) == 0 {
			continue
		}
		started = append(started, first{key: key, at: iss.Slots[0].Start, rank: rank})
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
	ranks := make(map[string]string, len(started))
	for i, issue := range started {
		ranks[issue.key] = fmt.Sprintf("%s%0*d", prefix, width, i+1)
	}
	return ranks
}
