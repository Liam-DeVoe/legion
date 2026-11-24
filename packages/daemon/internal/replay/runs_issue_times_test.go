package replay

import (
	"path/filepath"
	"testing"
	"time"
)

// A validation read from runs keeps each replay's per-issue times when the window names no rule
// its daemon ran (window.json with no parts, so its record's rule is ""): the validation's own rule
// is then the part it reads, and each issue's error is the replay's time against the record's. A
// window collected without parts dropped those times, so every issue read as one the replay never
// placed and its error stood at the window's end less the record's time. ACME-1 takes its first
// slot at 01:00 in the record and at 01:10 in the one seed: its error is 10 minutes, not the 23
// hours from 01:00 to the window's end.
func TestAValidationOfAWindowNamingNoRuleKeepsTheReplaysIssueTimes(t *testing.T) {
	dir := t.TempDir()
	const window, rule = "no-parts", "legion-v20.42.5"
	from := time.Date(2001, time.March, 5, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	recordedAt, replayedAt := from.Add(time.Hour), from.Add(time.Hour+10*time.Minute)
	rec := windowRecord{Name: window, Project: "ACME", From: from, To: to,
		Scorecard:     Scorecard{From: from, To: to, FirstSlot: map[string]time.Time{"ACME-1": recordedAt}, DoneAt: map[string]time.Time{}},
		WaitsGaveBack: []waitGaveBack{}}
	if rec.Rule != "" || len(rec.Parts) != 0 {
		t.Fatalf("the fixture's window names the rule %q and %d parts, want none", rec.Rule, len(rec.Parts))
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(writeJSON(filepath.Join(dir, window, recordFile), rec))
	must(writeJSON(filepath.Join(dir, window, rule, partFile), partRecord{Window: window, Rule: rule,
		Branches: []partRun{{Branch: MainBranch.Name, Slowdown: 1, Seeds: []int64{1}}}, BreakEven: []partRun{}, Parallel: 1}))
	run := runDir(dir, window, rule, MainBranchAt, 1)
	must(writeJSON(filepath.Join(run, runFile), runRecord{Rule: rule, Branch: MainBranch, Seed: 1, Slowdown: 1}))
	must(writeJSON(filepath.Join(run, scorecardFile), Scorecard{From: from, To: to}))
	must(writeJSON(filepath.Join(run, lineScorecardFile), Scorecard{From: from, To: to,
		FirstSlot: map[string]time.Time{"ACME-1": replayedAt}, DoneAt: map[string]time.Time{}}))

	runs, err := ReadRuns(dir)
	must(err)
	v, err := runs.Validation(window, rule)
	must(err)
	if len(v.Branches) != 1 || len(v.Branches[0].Issues) != 1 {
		t.Fatalf("the validation holds %d branches, the first with %+v issues; want one branch with ACME-1", len(v.Branches), v.Branches)
	}
	if got := v.Branches[0].Issues[0].FirstSlot; got != 10 {
		t.Errorf("ACME-1's first-slot error is %v minutes, want 10: the replay's time against the record's, not the window's end against the record's (%v)",
			got, to.Sub(recordedAt).Minutes())
	}
}
