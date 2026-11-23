package replay

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// A directory of runs is where validate, forecast and compare write each run they make and read
// every report back from, so a report is the same whether one process made every run or many
// processes each made a part (a window under a rule) and the report was written from their
// directories (ReadRuns):
//
//	<dir>/<window>/record.json                    the window's record (windowRecord)
//	<dir>/<window>/<rule>/part.json               the part's plan, written once every run of it is in (partRecord)
//	<dir>/<window>/<rule>/<branch>@<f>/seed-<n>/  one run: run.json, scorecard.json, scorecard-line.json
//
// A window goes by its directory's base name. A run whose three files are all there is not run
// again, so a part that stopped part way goes on where it stopped.
const (
	recordFile = "record.json"
	partFile   = "part.json"
)

// SeedsPerDraw is how many seeds each branch with a draw runs, the seeds 1 to 5 (plan,
// "Branches"); a branch with no draw runs the seed 1 alone. A branch's value of a measure is the
// mean of its seeds.
const SeedsPerDraw = 5

// BreakEvenSlowdowns is the slowdown factors the break-even search runs the main branch at (plan,
// "Branches"): 1.0 to 2.0 by 0.1.
func BreakEvenSlowdowns() []float64 {
	factors := make([]float64, 0, 11)
	for tenth := 10; tenth <= 20; tenth++ {
		factors = append(factors, float64(tenth)/10)
	}
	return factors
}

// Label is b as a run's directory and every report name it: the branch's name, "@" and its
// slowdown factor, "main+timing=wall@1.2".
func (b BranchAt) Label() string {
	return b.Branch.Name + "@" + strconv.FormatFloat(b.Slowdown, 'f', -1, 64)
}

// branchAtLabel is the branch a label names (Label), refused where it names none.
func branchAtLabel(label string) (BranchAt, error) {
	name, factor, ok := strings.Cut(label, "@")
	if !ok {
		return BranchAt{}, fmt.Errorf("replay: %q is no branch at a slowdown factor (<branch>@<f>)", label)
	}
	branch, err := BranchNamed(name)
	if err != nil {
		return BranchAt{}, err
	}
	f, err := strconv.ParseFloat(factor, 64)
	if err != nil || f <= 0 {
		return BranchAt{}, fmt.Errorf("replay: %q names no slowdown factor above zero", label)
	}
	return BranchAt{Branch: branch, Slowdown: f}, nil
}

// windowName is the name a window goes by in a directory of runs and in every report: its
// directory's base name.
func windowName(dir string) (string, error) {
	name := filepath.Base(filepath.Clean(dir))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return "", fmt.Errorf("replay: the window directory %q has no name of its own", dir)
	}
	return name, nil
}

// readWindowHead is the window in dir as window.json alone gives it, its inputs not read: what a
// comparison needs of a window it runs no part of.
func readWindowHead(dir string) (Window, error) {
	raw, err := os.ReadFile(filepath.Join(dir, windowFile))
	if err != nil {
		return Window{}, fmt.Errorf("read the window in %s: %w", dir, err)
	}
	var w Window
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&w); err != nil {
		return Window{}, fmt.Errorf("read %s: %w", filepath.Join(dir, windowFile), err)
	}
	if w.Format != WindowFormat {
		return Window{}, fmt.Errorf("read the window in %s: format %d, want %d", dir, w.Format, WindowFormat)
	}
	return w, nil
}

// partsRule is the name of the one rule parts say the window's daemon ran, "" where they name none
// or more than one.
func partsRule(parts []Part) string {
	rule := ""
	for i, part := range parts {
		if i > 0 && part.Rule != rule {
			return ""
		}
		rule = part.Rule
	}
	return rule
}

// windowRecord is a window's record.json in a directory of runs: its name, its place among the
// windows compared (0 for the window before the change of the rule), its project, span and parts,
// the rule its daemon ran ("" where its parts name more than one), its record's scorecard (Score),
// the recorded stretches inside its span, which its conflict model is fitted on (windowConflicts),
// with how many ended in a conflict, which say whether a branch of it draws (draws), and each wait
// on another issue that gave a slot back in its record (waitsGaveBack), which the comparison reads
// what each rule's grace keeps from (graceKeeps).
type windowRecord struct {
	Name          string         `json:"name"`
	Order         int            `json:"order"`
	Project       string         `json:"project"`
	From          time.Time      `json:"from"`
	To            time.Time      `json:"to"`
	Rule          string         `json:"rule"`
	Parts         []Part         `json:"parts"`
	Scorecard     Scorecard      `json:"scorecard"`
	Stretches     int            `json:"stretches"`
	Conflicted    int            `json:"conflicted"`
	WaitsGaveBack []waitGaveBack `json:"waitsGaveBack"`
}

// draws says whether a replay of the window on branch draws anything with its seed: a conflict,
// where some recorded stretch of the window ended in one and the fitted chance is above zero, or
// the work of an issue that never started, under a drawn scenario.
func (r windowRecord) draws(branch Branch) bool {
	return r.Conflicted > 0 || branch.orMain().NeverStarted != MainBranch.NeverStarted
}

// seeds is the seeds a run of the window on branch takes: 1 to perDraw where it draws, and 1
// alone where it draws nothing.
func (r windowRecord) seeds(branch Branch, perDraw int) []int64 {
	n := 1
	if r.draws(branch) {
		n = max(perDraw, 1)
	}
	seeds := make([]int64, n)
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	return seeds
}

// newWindowRecord is the record of w, named name at order: its record's scorecard, its conflict
// model's counts and its waits that gave a slot back, all read from one trace.
func newWindowRecord(w Window, name string, order int) (windowRecord, error) {
	t, err := BuildTrace(w)
	if err != nil {
		return windowRecord{}, err
	}
	model := windowConflicts(t, newBaseMerges(w, t))
	return windowRecord{Name: name, Order: order, Project: w.Project, From: w.From, To: w.To, Rule: partsRule(w.Parts), Parts: w.Parts,
		Scorecard: scoreTrace(t), Stretches: model.Stretches, Conflicted: model.Conflicts, WaitsGaveBack: waitsGaveBack(t)}, nil
}

// partRun is one branch of a part's plan with the seeds it runs.
type partRun struct {
	Branch   string  `json:"branch"`
	Slowdown float64 `json:"slowdown"`
	Seeds    []int64 `json:"seeds"`
}

// at is the branch r runs.
func (r partRun) at() (BranchAt, error) {
	branch, err := BranchNamed(r.Branch)
	if err != nil {
		return BranchAt{}, err
	}
	return BranchAt{Branch: branch, Slowdown: r.Slowdown}, nil
}

// label is the label of the branch r runs (BranchAt.Label), read without checking its name.
func (r partRun) label() string {
	return BranchAt{Branch: Branch{Name: r.Branch}, Slowdown: r.Slowdown}.Label()
}

// partRecord is a part's part.json: the window and the rule it ran, each branch it ran with its
// seeds, the factors of the break-even search it ran the main branch at beyond its branches', and
// what the part took — the wall time of its window, whose parts' runs share one pool (runWindow),
// so every part of one window writes the same time; how many runs it ran at once; and the
// process's peak memory — so a later Job can be sized from it (each run's own counts are in its
// run.json).
type partRecord struct {
	Window          string    `json:"window"`
	Rule            string    `json:"rule"`
	RuleCommit      string    `json:"ruleCommit"`
	Branches        []partRun `json:"branches"`
	BreakEven       []partRun `json:"breakEven"`
	Parallel        int       `json:"parallel"`
	WallSeconds     float64   `json:"wallSeconds"`
	PeakMemoryBytes uint64    `json:"peakMemoryBytes"`
}

// partSpec is one part a process runs: one window under one rule, over branches and the main
// branch at the factors breakEven names beyond them.
type partSpec struct {
	rule      record.SlotRule
	branches  []BranchAt
	breakEven []float64
}

// windowSpec is one window a process runs parts of: its directory, its name, its place among the
// windows compared, and its parts (none for a window whose record alone is read).
type windowSpec struct {
	dir   string
	name  string
	order int
	parts []partSpec
}

// RunDone is one run of a part as it ends: which run it was, whether its files were already there
// so it was not run again (Kept), what its run.json counted, and how many runs of the process have
// ended of how many it has planned so far (Ended, Of): a window's runs are planned when its turn
// comes, since its seeds rest on its record.
type RunDone struct {
	Window          string
	Rule            string
	Branch          BranchAt
	Seed            int64
	Kept            bool
	Facts           int
	Passes          int
	WallSeconds     float64
	PeakMemoryBytes uint64
	Ended, Of       int
}

// runOptions is how a process runs its parts: the directory of runs, the server each run makes its
// database on, how many runs at once, the seeds of a branch with a draw, whether each run's log is
// kept, and who is told of each run as it ends.
type runOptions struct {
	root     string
	pgDSN    string
	parallel int
	perDraw  int
	keepLogs bool
	progress func(RunDone)
}

// runWindows runs every part of windows, one window at a time so one window's inputs are in memory
// at once, up to opts.parallel runs at once against opts.pgDSN's server, each in a database of its
// own (Run).
func runWindows(ctx context.Context, windows []windowSpec, opts runOptions) error {
	if opts.root == "" {
		return errors.New("replay: no directory to write the runs to")
	}
	ended, of := 0, 0
	for _, ws := range windows {
		if err := runWindow(ctx, ws, opts, &ended, &of); err != nil {
			return err
		}
	}
	return nil
}

// planned is one run a part plans.
type planned struct {
	part int
	at   BranchAt
	seed int64
	dir  string
}

// runWindow reads ws's window, writes its record.json (kept where one of the same window is there
// already), and runs every run of its parts not already in, ended and of counting the runs of the
// process; each part's part.json is written once all of its runs are in.
func runWindow(ctx context.Context, ws windowSpec, opts runOptions, ended, of *int) error {
	began := time.Now()
	w, err := ReadWindow(ws.dir)
	if err != nil {
		return err
	}
	rec, err := windowRecordFor(w, ws, opts.root)
	if err != nil {
		return err
	}
	plans := make([]partRecord, len(ws.parts))
	var runs []planned
	for i, part := range ws.parts {
		plan := partRecord{Window: ws.name, Rule: part.rule.Name, RuleCommit: part.rule.Commit, Branches: []partRun{}, BreakEven: []partRun{},
			Parallel: max(opts.parallel, 1)}
		add := func(list *[]partRun, at BranchAt) {
			seeds := rec.seeds(at.Branch, opts.perDraw)
			*list = append(*list, partRun{Branch: at.Branch.Name, Slowdown: at.Slowdown, Seeds: seeds})
			for _, seed := range seeds {
				runs = append(runs, planned{part: i, at: at, seed: seed, dir: runDir(opts.root, ws.name, part.rule.Name, at, seed)})
			}
		}
		for _, at := range part.branches {
			add(&plan.Branches, at)
		}
		for _, f := range part.breakEven {
			add(&plan.BreakEven, BranchAt{Branch: MainBranch, Slowdown: f})
		}
		plans[i] = plan
	}
	*of += len(runs)
	var mu sync.Mutex
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(max(opts.parallel, 1))
	for _, run := range runs {
		group.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			done, err := runOne(gctx, w, ws.name, ws.parts[run.part].rule, run, opts)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			*ended++
			done.Ended, done.Of = *ended, *of
			if opts.progress != nil {
				opts.progress(done)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	for _, plan := range plans {
		plan.WallSeconds, plan.PeakMemoryBytes = time.Since(began).Seconds(), peakMemory()
		if err := writeJSON(filepath.Join(opts.root, ws.name, plan.Rule, partFile), plan); err != nil {
			return err
		}
	}
	return nil
}

// windowRecordFor is the record of ws's window w, as record.json under root holds it where a
// record of the same window (its project and span) is there, at ws's place, and otherwise read from
// w (newWindowRecord), as it is where the record there was written before it held the waits that
// gave a slot back (WaitsGaveBack nil); either way it is written back with ws's place.
func windowRecordFor(w Window, ws windowSpec, root string) (windowRecord, error) {
	path := filepath.Join(root, ws.name, recordFile)
	var rec windowRecord
	if err := readJSON(path, &rec); err != nil || rec.Name != ws.name || rec.Project != w.Project || !rec.From.Equal(w.From) || !rec.To.Equal(w.To) ||
		rec.WaitsGaveBack == nil {
		if rec, err = newWindowRecord(w, ws.name, ws.order); err != nil {
			return windowRecord{}, err
		}
	}
	rec.Order = ws.order
	return rec, writeJSON(path, rec)
}

// runDir is the directory of one run under root.
func runDir(root, window, rule string, at BranchAt, seed int64) string {
	return filepath.Join(root, window, rule, at.Label(), fmt.Sprintf("seed-%d", seed))
}

// runOne makes the run p plans of w under rule on opts.pgDSN's server, unless its three files are
// there already, and deletes its log unless opts keeps it: the run reads its log back for its line
// scorecard (Run), and a window's log is the largest file a run writes.
func runOne(ctx context.Context, w Window, window string, rule record.SlotRule, p planned, opts runOptions) (RunDone, error) {
	done := RunDone{Window: window, Rule: rule.Name, Branch: p.at, Seed: p.seed}
	if runComplete(p.dir) {
		done.Kept = true
	} else {
		if _, err := Run(ctx, RunConfig{Window: w, Rule: rule, Branch: p.at.Branch, Seed: p.seed, Slowdown: p.at.Slowdown, PGDSN: opts.pgDSN, Out: p.dir}); err != nil {
			return done, fmt.Errorf("replay: %s under %s on %s, seed %d: %w", window, rule.Name, p.at.Label(), p.seed, err)
		}
		if !opts.keepLogs {
			if err := os.Remove(filepath.Join(p.dir, logFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return done, fmt.Errorf("replay: delete the log of %s: %w", p.dir, err)
			}
		}
	}
	var ran runRecord
	if err := readJSON(filepath.Join(p.dir, runFile), &ran); err != nil {
		return done, err
	}
	done.Facts, done.Passes, done.WallSeconds, done.PeakMemoryBytes = ran.Facts, ran.Passes, ran.WallSeconds, ran.PeakMemoryBytes
	return done, nil
}

// runComplete says whether dir holds a run's three files, each one JSON value.
func runComplete(dir string) bool {
	for _, name := range []string{runFile, scorecardFile, lineScorecardFile} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !json.Valid(data) {
			return false
		}
	}
	return true
}

// readJSON reads the JSON value in path into v.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("replay: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("replay: read %s: %w", path, err)
	}
	return nil
}

// writeJSON writes v to path as indented JSON, making its directory.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("replay: write %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("replay: write %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("replay: write %s: %w", path, err)
	}
	return nil
}

// Runs is what directories of runs hold, read back (ReadRuns): each window's record and each part's
// plan with what its runs measured. Every report — a validation, a forecast, a comparison — is
// written from it alone, so it is the same whether its runs were made in one process or in many.
type Runs struct {
	windows map[string]windowRecord
	parts   map[partKey]*partRead
}

// partKey names a part: a window and a rule.
type partKey struct{ window, rule string }

// partRead is one part read back: its plan, and each run of each of its branches and break-even
// factors, by the branch's label, in the order of its seeds.
type partRead struct {
	plan partRecord
	runs map[string][]runRead
}

// runRead is what a report reads of one run: its seed, its scorecard from the snapshot timeline
// with the decision's costs read from it, its scorecard from the line timeline, the gap of each
// measure between the two (run.json's, each issue's own left out), and the assumptions and the
// scenario it ran under. A run of a part that no validation or forecast reads keeps no issue's
// times of its line scorecard, and no run keeps its snapshot scorecard's, which no report reads.
type runRead struct {
	seed        int64
	snapshot    Scorecard
	costs       Costs
	line        Scorecard
	gaps        []MeasureGap
	assumptions []int
	scenario    string
}

// ReadRuns reads every window's record and every part, with its runs, that dirs hold. A part found
// in two of them is refused, as is a part whose plan names a run that is not there; a window's
// record found in more than one is read from the first.
func ReadRuns(dirs ...string) (Runs, error) {
	runs := Runs{windows: map[string]windowRecord{}, parts: map[partKey]*partRead{}}
	found := map[partKey]string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return Runs{}, fmt.Errorf("replay: read the runs in %s: %w", dir, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			windowDir := filepath.Join(dir, entry.Name())
			if _, err := os.Stat(filepath.Join(windowDir, recordFile)); err != nil {
				continue
			}
			var rec windowRecord
			if err := readJSON(filepath.Join(windowDir, recordFile), &rec); err != nil {
				return Runs{}, err
			}
			if _, seen := runs.windows[rec.Name]; !seen {
				runs.windows[rec.Name] = rec
			}
			rules, err := os.ReadDir(windowDir)
			if err != nil {
				return Runs{}, fmt.Errorf("replay: read the runs in %s: %w", windowDir, err)
			}
			for _, rule := range rules {
				if !rule.IsDir() {
					continue
				}
				if _, err := os.Stat(filepath.Join(windowDir, rule.Name(), partFile)); err != nil {
					continue
				}
				key := partKey{window: rec.Name, rule: rule.Name()}
				if other, twice := found[key]; twice {
					return Runs{}, fmt.Errorf("replay: the part of %s under %s is in both %s and %s", key.window, key.rule, other, dir)
				}
				found[key] = dir
			}
		}
	}
	for key, dir := range found {
		part, err := readPart(dir, key, runs.keepsIssues(key))
		if err != nil {
			return Runs{}, err
		}
		runs.parts[key] = part
	}
	return runs, nil
}

// keepsIssues says whether a report reads each issue's times from the line scorecards of the part
// key: the part of a window under the rule its daemon ran, which its validation reads, and the
// part of the window before the change under the rule of the window after it, which the forecast
// reads.
func (r Runs) keepsIssues(key partKey) bool {
	rec, ok := r.windows[key.window]
	if !ok {
		return false
	}
	if key.rule == rec.Rule {
		return true
	}
	for _, other := range r.windows {
		if other.Order == rec.Order+1 && other.Rule == key.rule {
			return true
		}
	}
	return false
}

// readPart reads the part key from the directory of runs dir: its part.json and each run its plan
// names; keepIssues keeps each issue's times of the runs' line scorecards.
func readPart(dir string, key partKey, keepIssues bool) (*partRead, error) {
	partDir := filepath.Join(dir, key.window, key.rule)
	part := &partRead{runs: map[string][]runRead{}}
	if err := readJSON(filepath.Join(partDir, partFile), &part.plan); err != nil {
		return nil, err
	}
	for _, run := range slices.Concat(part.plan.Branches, part.plan.BreakEven) {
		at, err := run.at()
		if err != nil {
			return nil, fmt.Errorf("replay: the part of %s under %s: %w", key.window, key.rule, err)
		}
		if _, read := part.runs[at.Label()]; read {
			continue
		}
		for _, seed := range run.Seeds {
			read, err := readRun(filepath.Join(partDir, at.Label(), fmt.Sprintf("seed-%d", seed)), keepIssues)
			if err != nil {
				return nil, err
			}
			read.seed = seed
			part.runs[at.Label()] = append(part.runs[at.Label()], read)
		}
	}
	return part, nil
}

// readRun reads one run's three files from dir; keepIssues keeps each issue's times of its line
// scorecard.
func readRun(dir string, keepIssues bool) (runRead, error) {
	var read runRead
	var ran runRecord
	if err := readJSON(filepath.Join(dir, runFile), &ran); err != nil {
		return runRead{}, err
	}
	if err := readJSON(filepath.Join(dir, scorecardFile), &read.snapshot); err != nil {
		return runRead{}, err
	}
	if err := readJSON(filepath.Join(dir, lineScorecardFile), &read.line); err != nil {
		return runRead{}, err
	}
	read.costs = costsOf(read.snapshot)
	dropIssueTimes(&read.snapshot)
	if !keepIssues {
		dropIssueTimes(&read.line)
	}
	for _, gap := range ran.Gaps {
		if !strings.HasPrefix(gap.Measure, "firstSlot[") && !strings.HasPrefix(gap.Measure, "doneAt[") {
			read.gaps = append(read.gaps, gap)
		}
	}
	read.assumptions, read.scenario = ran.Assumptions, ran.Scenario
	return read, nil
}

// dropIssueTimes empties card's times of each issue, and its first day's, which only a validation's
// and a forecast's line scorecards are read for.
func dropIssueTimes(card *Scorecard) {
	card.FirstSlot, card.DoneAt = nil, nil
	if card.FirstDayApart != nil {
		day := *card.FirstDayApart
		day.FirstSlot, day.DoneAt = nil, nil
		card.FirstDayApart = &day
	}
}

// Windows is the name of each window the runs hold a record of, in their order among the windows
// compared (the window before the change of the rule first), then by name.
func (r Runs) Windows() []string {
	records := slices.SortedFunc(maps.Values(r.windows), windowOrder)
	names := make([]string, 0, len(records))
	for _, rec := range records {
		names = append(names, rec.Name)
	}
	return names
}

// windowOrder orders two windows' records by their place among the windows compared, then by name.
func windowOrder(a, b windowRecord) int {
	return cmp.Or(cmp.Compare(a.Order, b.Order), strings.Compare(a.Name, b.Name))
}

// Rule is the rule the daemon of the window name ran, as its record says ("" where its parts name
// more than one), refused where the runs hold no record of it.
func (r Runs) Rule(window string) (string, error) {
	rec, err := r.window(window)
	return rec.Rule, err
}

// window is the record of the window name, refused where the runs hold none.
func (r Runs) window(name string) (windowRecord, error) {
	rec, ok := r.windows[name]
	if !ok {
		return windowRecord{}, fmt.Errorf("replay: the runs hold no record of the window %s", name)
	}
	return rec, nil
}

// part is the part of window under rule, refused where the runs hold none.
func (r Runs) part(window, rule string) (*partRead, error) {
	part, ok := r.parts[partKey{window: window, rule: rule}]
	if !ok {
		return nil, fmt.Errorf("replay: the runs hold no part of the window %s under the rule %s", window, rule)
	}
	return part, nil
}

// Validation is the validation of the window named window under rule (spec R5), read from the runs:
// each branch of the part's plan against the window's record, both read from line timelines.
func (r Runs) Validation(window, rule string) (Validation, error) {
	rec, err := r.window(window)
	if err != nil {
		return Validation{}, err
	}
	part, err := r.part(window, rule)
	if err != nil {
		return Validation{}, err
	}
	v := Validation{Window: window, Rule: rule, Record: rec.Scorecard}
	for _, run := range part.plan.Branches {
		at, err := run.at()
		if err != nil {
			return Validation{}, err
		}
		v.Branches = append(v.Branches, ValidateBranch(rec.Scorecard, at, lineCards(part.runs[at.Label()])))
	}
	return v, nil
}

// Forecast is the forecast across the change from the window before to the window after under rule
// (spec R5), read from the runs: the main branch of the part of before under rule against the
// record of after.
func (r Runs) Forecast(before, after, rule string) (ForecastResult, error) {
	from, err := r.window(before)
	if err != nil {
		return ForecastResult{}, err
	}
	to, err := r.window(after)
	if err != nil {
		return ForecastResult{}, err
	}
	part, err := r.part(before, rule)
	if err != nil {
		return ForecastResult{}, err
	}
	runs, ok := part.runs[MainBranchAt.Label()]
	if !ok {
		return ForecastResult{}, fmt.Errorf("replay: the part of %s under %s holds no run of its main branch %s", before, rule, MainBranchAt.Label())
	}
	f := ForecastResult{Before: before, After: after, Rule: rule, Branch: MainBranchAt, FromCommit: lastCommit(from.Parts), ToCommit: firstCommit(to.Parts)}
	cards := lineCards(runs)
	for _, card := range cards {
		f.Seeds = append(f.Seeds, card.Seed)
	}
	f.Checks = ForecastChecks(to.Scorecard, cards)
	return f, nil
}

// lineCards is the line scorecard of each run of runs, in their order, each with its run's seed.
func lineCards(runs []runRead) []Scorecard {
	cards := make([]Scorecard, 0, len(runs))
	for _, run := range runs {
		card := run.line
		card.Seed = run.seed
		cards = append(cards, card)
	}
	return cards
}

// firstCommit and lastCommit are the commits of the first and the last of parts, "" for none.
func firstCommit(parts []Part) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0].Commit
}

func lastCommit(parts []Part) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1].Commit
}
