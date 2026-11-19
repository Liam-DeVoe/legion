package main

import (
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// R9: "For a check that a program runs, the failing example is a fault that a step plants in the
// code." R8: "A failing example that the check's test caught counts at another commit only while no
// file that the test reads changed." The failing example of a program check of the code counts from
// the commit that the step planted its fault on (planted_on), and never at the fault's own commit,
// which CI tested (commit), and only when each path of the fault (fault_paths) is a source file, one
// that ends in .go, .ts or .tsx, and no file of a test, and the tie of the check at planted_on, where
// it is known, covers it. A record with no planted_on or no path counts nowhere. In carry the step
// planted the fault on c1, in packages/envoy/cmd/listener/main.go, a file of the tie, and CI tested
// it at f1: the fault makes the check true at c1, and at c2 and at m2, whose files of the tie equal
// those of c1, and c3's change of a file that the test reads ends it. Each row: carry, with a subject
// at c1 itself, whose spec files are m2's (withSubjectAtC1), and the row's change; the check reads
// the row's line at each commit of the row's map. A build that counts the failing example at f1, the
// commit that CI tested, reads the check unknown at c1, c2 and m2, and fails the control.
func TestAFailingExampleCountsFromTheCommitThatItsFaultWasPlantedOn(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	const check = "a-restart-loses-no-message"
	const fromC1, none = "true (passed, counted from c1)", "unknown (no_failing_example)"
	subjects := map[string]string{"c1": "c1, the head of c1-head", "c2": "c2, the head of listener-restart", "m2": "m2, the head of main", "c3": "c3, the head of contract-fixture"}
	noneAtEach := map[string]string{"c1": none, "c2": none, "m2": none}
	// results changes carry's results.json by change; fault sets the members of set on carry's
	// failing example, result 2, and takes out each member whose value is nil; paths sets its
	// fault_paths.
	results := func(change func(r []map[string]any) []map[string]any) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			var r []map[string]any
			readJSONFile(t, filepath.Join(dir, "results.json"), &r)
			if r[1]["kind"] != "failing_example" || r[1]["commit"] != "f1" || r[1]["planted_on"] != "c1" {
				t.Fatalf("result 2 of carry is no failing example of the fault f1 planted on c1: %v", r[1])
			}
			writeJSONFile(t, filepath.Join(dir, "results.json"), change(r))
		}
	}
	fault := func(set map[string]any) func(t *testing.T, dir string) {
		return results(func(r []map[string]any) []map[string]any {
			for member, v := range set {
				if v == nil {
					delete(r[1], member)
				} else {
					r[1][member] = v
				}
			}
			return r
		})
	}
	paths := func(p ...string) func(t *testing.T, dir string) {
		return fault(map[string]any{"fault_paths": append([]string{}, p...)})
	}
	for _, row := range []struct {
		name   string
		change func(t *testing.T, dir string)
		want   map[string]string // the line of the check at the subject of each commit
	}{
		{"control: carry, whose fault the step planted on c1 and CI tested at f1", nil,
			map[string]string{"c1": fromC1, "c2": fromC1, "m2": fromC1, "c3": none}},
		{"a pass and a failure at one commit read unknown, and not true", results(func(r []map[string]any) []map[string]any {
			return append(r, withMembers(r[0], map[string]any{"outcome": "fail", "at": "2026-11-12T10:10:00Z", "seq": 5}))
		}), map[string]string{"c1": "unknown (disagree)", "c2": "unknown (disagree)", "m2": "unknown (disagree)"}},
		{"the fault path is the test's own _test.go file", paths("packages/envoy/cmd/listener/restart_test.go"), noneAtEach},
		{"the fault path is a source file under testdata, a directory of the tie that starts with test", paths("packages/envoy/cmd/listener/testdata/fault.go"), noneAtEach},
		{"the fault path is a source file outside the tie", paths("packages/envoy/cmd/sender/main.go"), noneAtEach},
		{"the fault path is a file of the tie that is no source file", paths("packages/contracts/fixtures/dispatch-targeted-delivery.json"), noneAtEach},
		{"one of two fault paths is the test's own _test.go file", paths("packages/envoy/cmd/listener/main.go", "packages/envoy/cmd/listener/restart_test.go"), noneAtEach},
		{"the record names no fault path", paths(), noneAtEach},
		{"the record leaves fault_paths out", fault(map[string]any{"fault_paths": nil}), noneAtEach},
		{"the record names no planted_on", fault(map[string]any{"planted_on": nil}), noneAtEach},
		{"the record names no planted_on, and c1 as its commit", fault(map[string]any{"planted_on": nil, "commit": "c1"}), noneAtEach},
		{"the fault path is a .tsx source file of the tie", paths("packages/envoy/cmd/listener/view.tsx"), map[string]string{"c1": fromC1, "c2": fromC1, "m2": fromC1}},
		{"the fault path is a .ts source file of the tie", paths("packages/envoy/cmd/listener/client.ts"), map[string]string{"c1": fromC1, "c2": fromC1, "m2": fromC1}},
		{"two fault paths, each a source file of the tie", paths("packages/envoy/cmd/listener/main.go", "packages/envoy/cmd/listener/store.go"), map[string]string{"c1": fromC1, "c2": fromC1, "m2": fromC1}},
		{"a second fault, planted on c3 and tested at f3, makes the check true at c3", results(func(r []map[string]any) []map[string]any {
			return append(r, withMembers(r[1], map[string]any{"commit": "f3", "planted_on": "c3", "at": "2026-11-13T10:05:00Z", "seq": 5}))
		}), map[string]string{"c1": fromC1, "c2": fromC1, "m2": fromC1, "c3": "true (passed, counted from c3)"}},
		{"the tie of the check at c1 is not known: the fault counts at c1 alone, where no tie is read", func(t *testing.T, dir string) {
			var ties map[string]map[string]any
			readJSONFile(t, filepath.Join(dir, "ties.json"), &ties)
			delete(ties["c1"], check)
			writeJSONFile(t, filepath.Join(dir, "ties.json"), ties)
		}, map[string]string{"c1": fromC1, "c2": "unknown (subject_moved)", "m2": "unknown (subject_moved)"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := copyScenario(t, "carry")
			withSubjectAtC1(t, dir)
			if row.change != nil {
				row.change(t, dir)
			}
			got := runBinary(t, binary, "", "state", "--scenario", dir)
			if got.code != 0 || got.stderr != "" {
				t.Fatalf("exit %d, stderr %q: want exit 0 and nothing on stderr", got.code, got.stderr)
			}
			for _, commit := range slices.Sorted(maps.Keys(row.want)) {
				want := "  check " + check + ": " + row.want[commit]
				if lines := subjectLines(got.stdout, subjects[commit]); !slices.Contains(lines, want) {
					t.Errorf("at subject %s: %q, want the line %q", subjects[commit], lines, want)
				}
			}
		})
	}
}
