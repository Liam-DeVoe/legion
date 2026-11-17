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

// withMembers is a copy of the record r with the members of set set to their values.
func withMembers(r, set map[string]any) map[string]any {
	c := maps.Clone(r)
	maps.Copy(c, set)
	return c
}
