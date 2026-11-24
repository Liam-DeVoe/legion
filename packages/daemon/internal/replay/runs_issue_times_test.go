package replay

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// issueTimesRuns writes under dir a directory of runs of the window name, whose record gives ACME-1
// its first slot at recordedAt: one part under rule with one seed of the main branch and one of
// main+timing=wall, each run's line scorecard giving ACME-1's first slot at replayedAt, or none where
// replayedAt is zero. With named, the window's record names rule as the rule its daemon ran;
// otherwise it names none, as window.json with no parts does.
func issueTimesRuns(t *testing.T, dir, name, rule string, named bool, from, to, recordedAt, replayedAt time.Time) []BranchAt {
	t.Helper()
	rec := windowRecord{Name: name, Project: "ACME", From: from, To: to,
		Scorecard:     Scorecard{From: from, To: to, FirstSlot: map[string]time.Time{"ACME-1": recordedAt}, DoneAt: map[string]time.Time{}},
		WaitsGaveBack: []waitGaveBack{}}
	if named {
		rec.Rule = rule
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	wall, err := BranchNamed("main+timing=wall")
	must(err)
	branches := []BranchAt{MainBranchAt, {Branch: wall, Slowdown: 1}}
	must(writeJSON(filepath.Join(dir, name, recordFile), rec))
	part := partRecord{Window: name, Rule: rule, BreakEven: []partRun{}, Parallel: 1}
	for _, at := range branches {
		part.Branches = append(part.Branches, partRun{Branch: at.Branch.Name, Slowdown: at.Slowdown, Seeds: []int64{1}})
		run := runDir(dir, name, rule, at, 1)
		line := Scorecard{From: from, To: to, FirstSlot: map[string]time.Time{}, DoneAt: map[string]time.Time{}}
		if !replayedAt.IsZero() {
			line.FirstSlot["ACME-1"] = replayedAt
		}
		must(writeJSON(filepath.Join(run, runFile), runRecord{Rule: rule, Branch: at.Branch, Seed: 1, Slowdown: 1}))
		must(writeJSON(filepath.Join(run, scorecardFile), Scorecard{From: from, To: to}))
		must(writeJSON(filepath.Join(run, lineScorecardFile), line))
	}
	must(writeJSON(filepath.Join(dir, name, rule, partFile), part))
	return branches
}

// A validation read from runs keeps each replay's per-issue times when the window names no rule
// its daemon ran (window.json with no parts, so its record's rule is ""): the validation's own rule
// is then the part it reads, and each issue's error is the replay's time against the record's. A
// window collected without parts dropped those times, so every issue read as one the replay never
// placed and its error stood at the window's end less the record's time. ACME-1 takes its first
// slot at 01:00 in the record and at 01:10 in the one seed: its error is 10 minutes, not the 23
// hours from 01:00 to the window's end. The main branch of every part keeps its times for the issues
// file of a comparison whatever the window names (readPart), so the part holds a second branch,
// main+timing=wall, whose times only the window's rule kept, and the test reads both.
func TestAValidationOfAWindowNamingNoRuleKeepsTheReplaysIssueTimes(t *testing.T) {
	dir := t.TempDir()
	const window, rule = "no-parts", "legion-v20.42.5"
	from := time.Date(2001, time.March, 5, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	recordedAt, replayedAt := from.Add(time.Hour), from.Add(time.Hour+10*time.Minute)
	branches := issueTimesRuns(t, dir, window, rule, false, from, to, recordedAt, replayedAt)

	runs, err := ReadRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := runs.Rule(window); err != nil || got != "" {
		t.Fatalf("the fixture's window names the rule %q (%v), want none", got, err)
	}
	v, err := runs.Validation(window, rule)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Branches) != len(branches) {
		t.Fatalf("the validation holds %d branches, want %d", len(v.Branches), len(branches))
	}
	for _, b := range v.Branches {
		if len(b.Issues) != 1 {
			t.Errorf("on %s the validation holds the issues %+v, want ACME-1 alone", b.Branch.Label(), b.Issues)
			continue
		}
		if got := b.Issues[0].FirstSlot; got != 10 {
			t.Errorf("on %s ACME-1's first-slot error is %v minutes, want 10: the replay's time against the record's, not the window's end against the record's (%v)",
				b.Branch.Label(), got, to.Sub(recordedAt).Minutes())
		}
	}
}

// A validation whose replays give no issue a first slot or a done on any branch, while the record
// gives some, is refused, with an error that names the window: its issues' errors would all stand
// at the window's end less the record's times, a validation of nothing the replays did. The part of
// the window no-times, under the rule its record names, holds a run of the main branch and one of
// main+timing=wall, neither giving ACME-1 the first slot the record gives it at 01:00.
func TestAValidationWhoseReplaysGiveNoIssueTimeIsRefused(t *testing.T) {
	dir := t.TempDir()
	const window, rule = "no-times", "legion-v20.42.5"
	from := time.Date(2001, time.March, 5, 0, 0, 0, 0, time.UTC)
	issueTimesRuns(t, dir, window, rule, true, from, from.Add(24*time.Hour), from.Add(time.Hour), time.Time{})

	runs, err := ReadRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	v, err := runs.Validation(window, rule)
	if err == nil {
		t.Fatalf("the validation of %s, whose replays give no issue a time, is not refused: %+v", window, v.Branches)
	}
	if !strings.Contains(err.Error(), window) {
		t.Errorf("the refusal %q does not name the window %s", err, window)
	}
}
