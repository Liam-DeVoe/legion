package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// R8: "A result at one commit also counts at another commit when no code that the check runs
// changed between the two." A fact against a check at a commit counts wherever the results of that
// commit count, as it counts at the commit itself: a leg that ended with no result (R12: "Each leg
// of a matrix reports its own result, and no leg replaces the result of another"; R7: "silence is
// not a pass"), a run of the job that ended with no result (R9: "When a check has no result that
// counts after a run of its job ends, it reads unknown, and Dispatch reports a fault of CI"), a
// refusal of CI (R48: "At that commit, such a check reads unknown, and no earlier result of it
// counts", so no result of it there also counts elsewhere), and a failing example that missed its
// fault (R9). A fact at a commit whose results do not count at the subject, such as c4, whose
// listener code differs, counts nowhere else. A leg with no result that counts, whose run has not
// ended, is no silent leg and no pass: it reads unknown (no_result) until it reports, and a run of
// the job that has not ended is no fault of CI; a leg that has a result that counts reads that
// result. An older result of a leg that counts answers a later run of that leg that ended with no
// result (R8: "So an unrelated merge leaves a check true"), and a result of that run that says it
// could not run is its answer (R7: "Of several results that count, the latest counts").
// TestAcceptASilentLegIsNotHiddenWhereItsResultsCarry holds the silent leg. Each row: carry, with a
// subject at c1 itself, whose spec files are m2's, and the row's change; the check reads the row's
// line at c1, and at c2 and at m2, the head of main, where the results of c1 carry.
func TestAFactAgainstACheckCountsWhereTheResultsOfItsCommitCount(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	const check = "a-restart-loses-no-message"
	for _, row := range []struct {
		name   string
		change func(results []map[string]any, jobs []any, uncited map[string]any) ([]map[string]any, []any)
		want   string // the line of the check at c1, c2 and m2
	}{
		{"control: carry's records read true at c1 and where they carry", func(r []map[string]any, j []any, _ map[string]any) ([]map[string]any, []any) {
			return r, j
		}, "true (passed, counted from c1)"},
		{"CI refused the check at c1", func(r []map[string]any, j []any, uncited map[string]any) ([]map[string]any, []any) {
			uncited["c1"] = map[string]any{check: "test-not-in-code"}
			return r, j
		}, "unknown (uncited)"},
		{"control: CI refused the check at c4, whose results do not count at c1, c2 or m2", func(r []map[string]any, j []any, uncited map[string]any) ([]map[string]any, []any) {
			uncited["c4"] = map[string]any{check: "test-not-in-code"}
			return append(r, withMembers(r[0], map[string]any{"commit": "c4", "at": "2026-11-13T11:00:00Z", "seq": 5})), j
		}, "true (passed, counted from c1)"},
		{"the job ended at c1 with no result of the check", func(r []map[string]any, j []any, _ map[string]any) ([]map[string]any, []any) {
			return r[1:], j // carry's pass at c1 taken out: the job's run there ended, and c3's pass does not carry
		}, "unknown (ci_fault)"},
		{"a failing example at c1 that missed its fault, after the caught one", func(r []map[string]any, j []any, _ map[string]any) ([]map[string]any, []any) {
			missed := map[string]any{}
			for k, v := range r[1] {
				missed[k] = v
			}
			missed["outcome"], missed["at"], missed["seq"] = "pass", "2026-11-12T10:06:00Z", 5
			return append(r, missed), j
		}, "unknown (failing_example_missed)"},
		{"leg linux passed at c1, and the run of leg arm there has not ended", func(r []map[string]any, _ []any, _ map[string]any) ([]map[string]any, []any) {
			r[0]["leg"] = "linux"
			return r, []any{
				map[string]any{"job": "go-checks", "leg": "linux", "commit": "c1", "ended": true},
				map[string]any{"job": "go-checks", "leg": "arm", "commit": "c1", "ended": false},
			}
		}, "unknown (no_result)"},
		{"no run of the check counts, and the job's run at c1 has not ended", func(r []map[string]any, _ []any, _ map[string]any) ([]map[string]any, []any) {
			return []map[string]any{r[1], r[3]}, []any{map[string]any{"job": "go-checks", "leg": "", "commit": "c1", "ended": false}}
		}, "unknown (no_result)"},
		{"leg linux passed at c1, the run of leg arm there ended with no result, and a run of leg x86 there has not ended", func(r []map[string]any, _ []any, _ map[string]any) ([]map[string]any, []any) {
			r[0]["leg"] = "linux"
			return r, []any{
				map[string]any{"job": "go-checks", "leg": "linux", "commit": "c1", "ended": true},
				map[string]any{"job": "go-checks", "leg": "arm", "commit": "c1", "ended": true},
				map[string]any{"job": "go-checks", "leg": "x86", "commit": "c1", "ended": false},
			}
		}, "unknown (ci_fault)"},
		{"a pass at c1, and a later run of the job at c2 that ended with no result", func(r []map[string]any, j []any, _ map[string]any) ([]map[string]any, []any) {
			return r, append(j, map[string]any{"job": "go-checks", "leg": "", "commit": "c2", "ended": true})
		}, "true (passed, counted from c1)"},
		{"the same, and the run at c2 reports that it could not run", func(r []map[string]any, j []any, _ map[string]any) ([]map[string]any, []any) {
			cannot := withMembers(r[0], map[string]any{"commit": "c2", "outcome": "could_not_run", "at": "2026-11-13T09:00:00Z", "seq": 5})
			return append(r, cannot), append(j, map[string]any{"job": "go-checks", "leg": "", "commit": "c2", "ended": true})
		}, "unknown (could_not_run)"},
		{"control: leg linux and leg arm passed at c1, and a run of leg arm there has not ended", func(r []map[string]any, _ []any, _ map[string]any) ([]map[string]any, []any) {
			r[0]["leg"] = "linux"
			arm := map[string]any{}
			for k, v := range r[0] {
				arm[k] = v
			}
			arm["leg"], arm["seq"] = "arm", 5
			return append(r, arm), []any{
				map[string]any{"job": "go-checks", "leg": "linux", "commit": "c1", "ended": true},
				map[string]any{"job": "go-checks", "leg": "arm", "commit": "c1", "ended": true},
				map[string]any{"job": "go-checks", "leg": "arm", "commit": "c1", "ended": false},
			}
		}, "true (passed, counted from c1)"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := copyScenario(t, "carry")
			if err := os.CopyFS(filepath.Join(dir, "files", "c1"), os.DirFS(filepath.Join(dir, "files", "m2"))); err != nil {
				t.Fatal(err)
			}
			var subjects []any
			readJSONFile(t, filepath.Join(dir, "subjects.json"), &subjects)
			writeJSONFile(t, filepath.Join(dir, "subjects.json"), append([]any{map[string]any{"commit": "c1", "branch": "c1-head", "now": "2026-11-14T00:00:00Z"}}, subjects...))
			var results []map[string]any
			readJSONFile(t, filepath.Join(dir, "results.json"), &results)
			var jobs []any
			readJSONFile(t, filepath.Join(dir, "jobs.json"), &jobs)
			uncited := map[string]any{}
			results, jobs = row.change(results, jobs, uncited)
			writeJSONFile(t, filepath.Join(dir, "results.json"), results)
			writeJSONFile(t, filepath.Join(dir, "jobs.json"), jobs)
			writeJSONFile(t, filepath.Join(dir, "uncited.json"), uncited)
			got := runBinary(t, binary, "", "state", "--scenario", dir)
			if got.code != 0 || got.stderr != "" {
				t.Fatalf("exit %d, stderr %q: want exit 0 and nothing on stderr", got.code, got.stderr)
			}
			for _, subject := range []string{"c1, the head of c1-head", "c2, the head of listener-restart", "m2, the head of main"} {
				want := "  check " + check + ": " + row.want
				if lines := subjectLines(got.stdout, subject); !slices.Contains(lines, want) {
					t.Errorf("at subject %s: %q, want the line %q", subject, lines, want)
				}
			}
		})
	}
}

// R12: "Each leg of a matrix reports its own result, and no leg replaces the result of another."
// One job at one commit has legs or it has none. In silent-leg the job had legs at c1, whose run
// results are of leg linux, and here it runs at m2 with no matrix, in one run of leg "" that ended
// with no result: no result of c1 answers that run, so the check reads unknown (ci_fault) at m2,
// where the results of c1 count (the control; R9: "When a check has no result that counts after a
// run of its job ends, it reads unknown"). A run result of leg "" at c1 beside those of leg linux
// would answer it, and the check would read true (passed, counted from c1), so the reader refuses
// the two forms, with a line that names the file, the commit, the job and the two legs. The rule
// reads a run result at the commit where the judge counts it, its commit, so a test_commit, which
// a result of a check of the code does not need, changes neither (the deep audit's input: a
// test_commit of no commit of the history, or of m2, where the run of no matrix is).
func TestARunResultOfNoMatrixAnswersNoRunWhereItsJobHadLegs(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	dir := copyScenario(t, "silent-leg")
	writeJSONFile(t, filepath.Join(dir, "jobs.json"), []any{map[string]any{"job": "go-checks", "leg": "", "commit": "m2", "ended": true}})
	got := runBinary(t, binary, "", "state", "--scenario", dir)
	want := "  check a-restart-loses-no-message: unknown (ci_fault)"
	if lines := subjectLines(got.stdout, "m2, the head of main"); got.code != 0 || got.stderr != "" || !slices.Contains(lines, want) {
		t.Fatalf("control: exit %d, stderr %q, at m2 %q; want exit 0 and the line %q", got.code, got.stderr, lines, want)
	}
	var base []map[string]any
	readJSONFile(t, filepath.Join(dir, "results.json"), &base)
	if base[0]["kind"] != "run" || base[0]["commit"] != "c1" || base[2]["kind"] != "run" || base[2]["leg"] != "linux" {
		t.Fatalf("silent-leg's results 1 and 3 are no runs of leg linux at c1: %v", base[:3])
	}
	const mix = `one job at one commit has legs or it has none, and "" is the leg of no matrix`
	for _, row := range []struct {
		result int
		set    map[string]any
		want   string
	}{
		{0, map[string]any{"leg": ""}, `result 3: it sets the leg "linux", and result 1 of its job "go-checks" at the commit "c1" sets the leg ""; ` + mix},
		{0, map[string]any{"leg": "", "test_commit": "c9"}, `result 3: it sets the leg "linux", and result 1 of its job "go-checks" at the commit "c1" sets the leg ""; ` + mix},
		{2, map[string]any{"leg": "", "test_commit": "c9"}, `result 3: it sets the leg "", and result 1 of its job "go-checks" at the commit "c1" sets the leg "linux"; ` + mix},
		{2, map[string]any{"leg": "", "test_commit": "m2"}, `result 3: it sets the leg "", and result 1 of its job "go-checks" at the commit "c1" sets the leg "linux"; ` + mix},
	} {
		results := slices.Clone(base)
		results[row.result] = withMembers(results[row.result], row.set)
		writeJSONFile(t, filepath.Join(dir, "results.json"), results)
		expect(t, runBinary(t, binary, "", "state", "--scenario", dir), result{code: 2, stderr: "spec-check: " + filepath.Join(dir, "results.json") + ": " + row.want + "\n"})
	}
}

// R48: "At that commit, such a check reads unknown, and no earlier result of it counts"; R7: "Of
// several results that count, the latest counts"; R9: "The check reads true only when its test, as
// it is now, failed on that fault", and "a check whose current text has no failing example that
// failed when it should" reads unknown; R48: "At that commit, such a check reads unknown, and no
// earlier result of it counts". A refusal of CI at c1 cancels each result of the check that counts
// at the subject and is recorded no later than c1's newest record: the passes, the failures, the
// runs that could not run and the failing examples, of c1 and of each other commit, and no newer
// one. The refusal carries no time, and the history gives no order of commits, so "earlier" is the
// order of record. The state is what the newer evidence gives: a refusal can move a check from
// false to unknown, from unknown to false, and from unknown to true, when it cancels an earlier
// failure and the newer evidence passes (a row of each). A refusal at a commit that holds no result
// of the check counts at that commit alone, as the subject. Each row: carry, with the row's
// records, and c0 as c1's parent with c1's tree and ties where the row says so; the check reads the
// row's line at c2, where the results of c0, c1 and m2 carry (R8), and at m2, the head of main.
// m2's pass and the run of the job that ended at m2 are the reviewer's case; c1's results are
// recorded on 12 November, its pass at 10:00 and its caught failing example at 10:05.
func TestARefusalOfCICancelsEachResultOfItsCommitAndOlder(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	const check = "a-restart-loses-no-message"
	refused := map[string]any{"c1": map[string]any{check: "test-did-not-run"}}
	type change = func(r []map[string]any) []map[string]any
	// run adds a run of the check at commit with its outcome, recorded at its time and seq, and fault
	// a caught failing example; at is m2's pass, beside the run of the job that ended at m2; failsAtC1
	// makes c1's run at 10:00 a failure.
	run := func(commit, outcome, runAt string, seq int) change {
		return func(r []map[string]any) []map[string]any {
			return append(r, withMembers(r[0], map[string]any{"commit": commit, "outcome": outcome, "at": runAt, "seq": seq}))
		}
	}
	fault := func(commit, faultAt string, seq int) change {
		return func(r []map[string]any) []map[string]any {
			return append(r, withMembers(r[1], map[string]any{"commit": commit, "at": faultAt, "seq": seq}))
		}
	}
	at := func(passAt string) change { return run("m2", "pass", passAt, 6) }
	failsAtC1 := func(r []map[string]any) []map[string]any {
		r[0] = withMembers(r[0], map[string]any{"outcome": "fail"})
		return r
	}
	for _, row := range []struct {
		name, why  string
		changes    []change
		uncited    map[string]any
		c2, m2     string // the line of the check at c2 and at m2
		none       string // the line at c2 and at m2 with no refusal, "" for a row that reads none
		c0         bool
		noM2JobRun bool
	}{
		{name: "control: m2's pass and no refusal", changes: []change{at("2026-11-13T12:00:00Z")}, uncited: map[string]any{},
			c2: "true (passed, counted from m2)", m2: "true (passed, counted from m2)"},
		{name: "CI refused the check at c1", why: "c1's caught failing example is taken out with its pass, and m2's pass has none",
			changes: []change{at("2026-11-13T12:00:00Z")}, uncited: refused, c2: "unknown (no_failing_example)", m2: "unknown (no_failing_example)"},
		{name: "the same, and a caught failing example at m2", changes: []change{at("2026-11-13T12:00:00Z"), fault("m2", "2026-11-13T12:05:00Z", 7)},
			uncited: refused, c2: "true (passed, counted from m2)", m2: "true (passed, counted from m2)"},
		{name: "the same, and a caught failing example at c2, newer than c1's results", changes: []change{at("2026-11-13T12:00:00Z"), fault("c2", "2026-11-13T11:00:00Z", 7)},
			uncited: refused, c2: "true (passed, counted from m2)", m2: "true (passed, counted from m2)"},
		{name: "m2's pass and its caught failing example recorded before c1's results", changes: []change{at("2026-11-12T09:00:00Z"), fault("m2", "2026-11-12T09:05:00Z", 7)},
			uncited: refused, c2: "unknown (uncited)", m2: "unknown (uncited)"},
		{name: "control: the same with no refusal", changes: []change{at("2026-11-12T09:00:00Z"), fault("m2", "2026-11-12T09:05:00Z", 7)},
			uncited: map[string]any{}, c2: "true (passed, counted from c1)", m2: "true (passed, counted from c1)"},
		{name: "m2's pass recorded between c1's pass and c1's failing example",
			why:     "the refusal's place is the newest record of c1, its failing example at 10:05, not its pass at 10:00: m2's pass at 10:02 is older, so it is taken out, and m2's later failing example alone counts no run",
			changes: []change{at("2026-11-12T10:02:00Z"), fault("m2", "2026-11-13T12:05:00Z", 7)}, uncited: refused, c2: "unknown (uncited)", m2: "unknown (uncited)"},
		{name: "CI refused the check at c2, which holds no result and no run", why: "the refusal counts at c2 alone", noM2JobRun: true,
			uncited: map[string]any{"c2": map[string]any{check: "test-not-in-code"}}, c2: "unknown (uncited)", m2: "true (passed, counted from c1)"},
		// Each row below holds a failure, a run that could not run or a failing example that missed
		// its fault. A refusal cancels each one recorded no later than c1's newest record, and keeps
		// each newer one (R48: "no earlier result of it counts").
		{name: "unknown to true: c2 fails at 10:03, between c1's pass and c1's failing example, then passes at 12:00",
			why:     "the refusal cancels c2's failure with c1's pass and failing example, each recorded no later than c1's newest record, and c2's newer pass and caught failing example decide (R48)",
			changes: []change{run("c2", "fail", "2026-11-12T10:03:00Z", 5), run("c2", "pass", "2026-11-12T12:00:00Z", 6), fault("c2", "2026-11-12T12:05:00Z", 7)},
			uncited: refused, c2: "true (passed, counted from c2)", m2: "true (passed, counted from c2)", none: "unknown (disagree)"},
		{name: "c2 fails at 10:03, and m2 passes on 13 November",
			why:     "the refusal cancels c2's failure, recorded before c1's newest record, and m2's newer pass and caught failing example decide (R48)",
			changes: []change{run("c2", "fail", "2026-11-12T10:03:00Z", 5), at("2026-11-13T12:00:00Z"), fault("m2", "2026-11-13T12:05:00Z", 7)},
			uncited: refused, c2: "true (passed, counted from m2)", m2: "true (passed, counted from m2)", none: "unknown (disagree)"},
		{name: "c1 fails at 10:00, and c0, c1's parent, passes at 11:00",
			why: "the refusal cancels c1's failure and c1's caught failing example, and c0's newer pass and caught failing example decide (R48)",
			c0:  true, changes: []change{failsAtC1, run("c0", "pass", "2026-11-12T11:00:00Z", 5), fault("c0", "2026-11-12T11:05:00Z", 6)},
			uncited: refused, c2: "true (passed, counted from c0)", m2: "true (passed, counted from c0)", none: "unknown (disagree)"},
		{name: "false to unknown: c0, c1's parent, passes at 09:00, and c1 fails at 10:00",
			why: "the refusal cancels c0's pass, c1's failure and the caught failing examples of c0 and c1, each recorded no later than c1's newest record, " +
				"so no run that counts is left: from the false that reads with no refusal to unknown",
			c0: true, changes: []change{failsAtC1, run("c0", "pass", "2026-11-12T09:00:00Z", 5), fault("c0", "2026-11-12T09:05:00Z", 6)},
			uncited: refused, c2: "unknown (uncited)", m2: "unknown (uncited)", none: "false (failed, counted from c1)"},
		{name: "c2 fails at 10:03 and passes at 10:04, before c1's failing example, and catches its fault at 12:05",
			why: "the refusal cancels c1's pass and failing example and c2's failure and pass, each recorded no later than c1's newest record, " +
				"so only c2's newer caught failing example is left, and no run that counts: from the unknown of a pass and a failure at c2 (R7) to unknown, not to false (R48)",
			changes: []change{run("c2", "fail", "2026-11-12T10:03:00Z", 5), run("c2", "pass", "2026-11-12T10:04:00Z", 6), fault("c2", "2026-11-12T12:05:00Z", 7)},
			uncited: refused, c2: "unknown (uncited)", m2: "unknown (uncited)", none: "unknown (disagree)"},
		{name: "false to unknown: c0, c1's parent, fails at 09:00, and c1 holds only its caught failing example",
			why: "the refusal at c1, a later commit, cancels c0's failure, recorded before c1's newest record, and no run that counts is left " +
				"(R48: \"no earlier result of it counts\"): from the false that reads with no refusal to unknown",
			c0: true, changes: []change{func(r []map[string]any) []map[string]any {
				r[0] = withMembers(r[0], map[string]any{"commit": "c0", "outcome": "fail", "at": "2026-11-12T09:00:00Z"})
				return r
			}, fault("m2", "2026-11-13T12:05:00Z", 6)},
			uncited: refused, c2: "unknown (uncited)", m2: "unknown (uncited)", none: "false (failed, counted from c0)"},
		{name: "control: a failure at c2 at 11:00, newer than c1's results, with c2's caught failing example",
			why:     "the refusal cancels c1's pass and failing example, and keeps c2's newer failure and failing example, which decide (R48)",
			changes: []change{run("c2", "fail", "2026-11-12T11:00:00Z", 5), fault("c2", "2026-11-12T11:05:00Z", 6)},
			uncited: refused, c2: "false (failed, counted from c2)", m2: "false (failed, counted from c2)", none: "false (failed, counted from c2)"},
		{name: "unknown to false: c2 passes at 10:02, before c1's failing example, fails at 11:00 and catches its fault at 11:05",
			why:     "the refusal cancels c2's pass, recorded no later than c1's newest record, and c2's newer failure and caught failing example decide (R48): from the unknown of a pass and a failure at c2 (R7) to false",
			changes: []change{run("c2", "pass", "2026-11-12T10:02:00Z", 5), run("c2", "fail", "2026-11-12T11:00:00Z", 6), fault("c2", "2026-11-12T11:05:00Z", 7)},
			uncited: refused, c2: "false (failed, counted from c2)", m2: "false (failed, counted from c2)", none: "unknown (disagree)"},
		{name: "c1 fails at 10:00, and m2 passes on 13 November",
			why:     "the refusal cancels c1's failure and failing example, and m2's newer pass and caught failing example decide (R48)",
			changes: []change{failsAtC1, at("2026-11-13T12:00:00Z"), fault("m2", "2026-11-13T12:05:00Z", 7)},
			uncited: refused, c2: "true (passed, counted from m2)", m2: "true (passed, counted from m2)", none: "unknown (disagree)"},
		{name: "a run at c2 at 10:03 that could not run, between c1's pass and c1's failing example",
			why:     "the refusal cancels it with c1's results, and no run that counts is left (R48)",
			changes: []change{run("c2", "could_not_run", "2026-11-12T10:03:00Z", 5)},
			uncited: refused, c2: "unknown (uncited)", m2: "unknown (uncited)", none: "unknown (could_not_run)"},
		{name: "a failing example at c1 at 10:06 that missed its fault, and m2's later pass",
			why: "the failing example that missed is c1's newest record, so the refusal cancels it with c1's other results, and m2's newer pass has no caught failing example " +
				"(R9: \"a check whose current text has no failing example that failed when it should\")",
			changes: []change{func(r []map[string]any) []map[string]any {
				return append(r, withMembers(r[1], map[string]any{"outcome": "pass", "at": "2026-11-12T10:06:00Z", "seq": 5}))
			}, at("2026-11-13T12:00:00Z")},
			uncited: refused, c2: "unknown (no_failing_example)", m2: "unknown (no_failing_example)", none: "unknown (failing_example_missed)"},
		{name: "control: a failing example at m2 on 13 November that missed its fault, and m2's pass",
			why: "the failing example that missed is newer than c1's results, so the refusal keeps it, and it is the newest failing example (R9)",
			changes: []change{at("2026-11-13T12:00:00Z"), fault("m2", "2026-11-13T12:05:00Z", 7), func(r []map[string]any) []map[string]any {
				return append(r, withMembers(r[1], map[string]any{"commit": "m2", "outcome": "pass", "at": "2026-11-13T12:06:00Z", "seq": 8}))
			}},
			uncited: refused, c2: "unknown (failing_example_missed)", m2: "unknown (failing_example_missed)", none: "unknown (failing_example_missed)"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			lines := func(uncited map[string]any) map[string][]string {
				dir := copyScenario(t, "carry")
				if row.c0 {
					withParentOfC1(t, dir)
				}
				var results []map[string]any
				readJSONFile(t, filepath.Join(dir, "results.json"), &results)
				for _, change := range row.changes {
					results = change(results)
				}
				var jobs []any
				readJSONFile(t, filepath.Join(dir, "jobs.json"), &jobs)
				if !row.noM2JobRun {
					jobs = append(jobs, map[string]any{"job": "go-checks", "leg": "", "commit": "m2", "ended": true})
				}
				writeJSONFile(t, filepath.Join(dir, "results.json"), results)
				writeJSONFile(t, filepath.Join(dir, "jobs.json"), jobs)
				writeJSONFile(t, filepath.Join(dir, "uncited.json"), uncited)
				got := runBinary(t, binary, "", "state", "--scenario", dir)
				if got.code != 0 || got.stderr != "" {
					t.Fatalf("exit %d, stderr %q: want exit 0 and nothing on stderr", got.code, got.stderr)
				}
				return map[string][]string{"c2": subjectLines(got.stdout, "c2, the head of listener-restart"), "m2": subjectLines(got.stdout, "m2, the head of main")}
			}
			got := lines(row.uncited)
			for subject, want := range map[string]string{"c2": row.c2, "m2": row.m2} {
				if !slices.Contains(got[subject], "  check "+check+": "+want) {
					t.Errorf("at subject %s: %q, want the line %q (%s)", subject, got[subject], "  check "+check+": "+want, row.why)
				}
			}
			if row.none == "" {
				return
			}
			none := lines(map[string]any{})
			for _, subject := range []string{"c2", "m2"} {
				if !slices.Contains(none[subject], "  check "+check+": "+row.none) {
					t.Errorf("with no refusal, at subject %s: %q, want the line %q", subject, none[subject], "  check "+check+": "+row.none)
				}
			}
		})
	}
}

// withParentOfC1 gives carry's c1 a parent c0, with c1's tree and c1's ties, so the results of c0
// count where those of c1 count.
func withParentOfC1(t *testing.T, dir string) {
	t.Helper()
	var history map[string]any
	readJSONFile(t, filepath.Join(dir, "history.json"), &history)
	commits := history["commits"].([]any)
	i := slices.IndexFunc(commits, func(c any) bool { return c.(map[string]any)["id"] == "c1" })
	if i < 0 {
		t.Fatal("carry's history holds no c1")
	}
	c1 := commits[i].(map[string]any)
	c0 := map[string]any{"id": "c0", "parent": c1["parent"], "files": maps.Clone(c1["files"].(map[string]any))}
	c1["parent"] = "c0"
	history["commits"] = slices.Insert(commits, i, any(c0))
	writeJSONFile(t, filepath.Join(dir, "history.json"), history)
	var ties map[string]any
	readJSONFile(t, filepath.Join(dir, "ties.json"), &ties)
	ties["c0"] = ties["c1"]
	writeJSONFile(t, filepath.Join(dir, "ties.json"), ties)
}

// withMembers is a copy of the record r with the members of set set to their values.
func withMembers(r, set map[string]any) map[string]any {
	c := maps.Clone(r)
	maps.Copy(c, set)
	return c
}
