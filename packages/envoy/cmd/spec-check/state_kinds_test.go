package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// The kind of a check, its procedure and its subject, decides whether its results count at a
// commit: a result of a check of the code counts at its commit, one of a program check of
// production or of the graph at its test_commit, the commit of the test that ran, and one of a
// query or an agent of production or of the graph at no commit, whatever test_commit it names.

// graphCheck is the check of the graph of production-and-graph, a query.
const graphCheck = "the-graph-holds-a-delivery-of-each-message"

// readsNoCommit is each kind of check that reads no commit, as production-and-graph's query of the
// graph is written in it (writtenAs): its texts changed, and whether it reads production.
var readsNoCommit = []struct {
	kind       string
	production bool
	texts      [][2]string
}{
	{"a query of the graph", false, nil},
	{"an agent of the graph", false, [][2]string{{`procedure="query"`, `procedure="agent"`}, {"Procedure: The query reads", "Procedure: An agent reads"}}},
	{"a query of production", true, [][2]string{{`subject="graph"`, `subject="production"`}}},
	{"an agent of production", true, [][2]string{{`procedure="query"`, `procedure="agent"`}, {"Procedure: The query reads", "Procedure: An agent reads"},
		{`subject="graph"`, `subject="production"`}}},
}

// writtenAs writes the check id of the scenario at dir as another kind of check: each pair of
// texts replaced in each approved version of its block and in each spec file, and, for a check of
// production, each result of it that read a transaction of the graph reading the deploy d1
// instead, as a result of production reads a deploy. The test fails when a text is not there, so
// that no row passes on a change that changed nothing.
func writtenAs(t *testing.T, dir, id string, production bool, texts [][2]string) {
	t.Helper()
	editApproved(t, filepath.Join(dir, "approved.json"), func(nodes []specgraph.ApprovedNode) []specgraph.ApprovedNode {
		n := approvedNode(t, nodes, id)
		for i := range n.Versions {
			for _, text := range texts {
				if !strings.Contains(n.Versions[i].Block, text[0]) {
					t.Fatalf("version %d of %s does not hold %q", n.Versions[i].Version, id, text[0])
				}
				n.Versions[i].Block = strings.Replace(n.Versions[i].Block, text[0], text[1], 1)
			}
		}
		return nodes
	})
	for _, text := range texts {
		editSpecFiles(t, dir, text[0], text[1])
	}
	if production {
		var results []map[string]any
		readJSONFile(t, filepath.Join(dir, "results.json"), &results)
		for _, r := range results {
			if r["check"] == id && r["graph_tx"] != nil {
				delete(r, "graph_tx")
				r["deploy"] = "d1"
			}
		}
		writeJSONFile(t, filepath.Join(dir, "results.json"), results)
	}
}

// editResults changes the results of the scenario at dir by change.
func editResults(t *testing.T, dir string, change func(results []map[string]any)) {
	t.Helper()
	var results []map[string]any
	readJSONFile(t, filepath.Join(dir, "results.json"), &results)
	change(results)
	writeJSONFile(t, filepath.Join(dir, "results.json"), results)
}

// R12: "Each leg of a matrix reports its own result, and no leg replaces the result of another."
// One job at one commit has legs or it has none, and the leg rule reads a run result at the commit
// where the judge counts it, as its check decides: a result of a query or an agent of production
// or of the graph is a record of its job at no commit, so a test_commit on it changes no answer.
// Each row: production-and-graph with its check of the graph written as the row's kind, its two
// runs reported by the job production-checks and its failing run in the leg x, where that job's
// runs at c1 are of the leg "": with test_commit c1 on that run, the command prints what it prints
// with none. The control: the program check of production's failing run in the leg x at its
// test_commit c1 is refused, since its test ran there.
func TestTheLegRuleReadsNoCommitOfACheckThatReadsNoCommit(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	for _, k := range readsNoCommit {
		t.Run(k.kind, func(t *testing.T) {
			t.Parallel()
			var got [2]result
			for i, testCommit := range []string{"", "c1"} {
				dir := copyScenario(t, "production-and-graph")
				writtenAs(t, dir, graphCheck, k.production, k.texts)
				editResults(t, dir, func(results []map[string]any) {
					for _, r := range results {
						if r["check"] == graphCheck && r["kind"] == "run" {
							r["job"] = "production-checks"
							if r["outcome"] == "fail" {
								r["leg"] = "x"
								if testCommit != "" {
									r["test_commit"] = testCommit
								}
							}
						}
					}
				})
				got[i] = runBinary(t, binary, "", "state", "--scenario", dir)
			}
			if got[0].code != 0 || got[0].stderr != "" || !strings.Contains(got[0].stdout, "  check "+graphCheck+": ") {
				t.Fatalf("with no test_commit: exit %d, stderr %q, stdout %q; want exit 0 and a line of %s", got[0].code, got[0].stderr, got[0].stdout, graphCheck)
			}
			expect(t, got[1], got[0])
		})
	}
	t.Run("control: a program check of production in the leg x at its test_commit c1", func(t *testing.T) {
		t.Parallel()
		dir := copyScenario(t, "production-and-graph")
		editResults(t, dir, func(results []map[string]any) {
			if r := results[2]; r["check"] != "a-production-restart-loses-no-message" || r["outcome"] != "fail" || r["test_commit"] != "c1" {
				t.Fatalf("result 3 of production-and-graph is no failure of its program check at test_commit c1: %v", r)
			}
			results[2]["leg"] = "x"
		})
		expect(t, runBinary(t, binary, "", "state", "--scenario", dir), result{code: 2, stderr: "spec-check: " + filepath.Join(dir, "results.json") +
			`: result 3: it sets the leg "x", and job run 1 of its job "production-checks" at the commit "c1" sets the leg ""; one job at one commit has legs or it has none, and "" is the leg of no matrix` + "\n"})
	})
}

// R48: "At that commit, such a check reads unknown, and no earlier result of it counts." R7: "Of
// several results that count, the latest counts, and a later failure cancels an earlier pass." A
// query or an agent of production or of the graph reads no commit, so CI's refusal of it at the
// commit that a test_commit of its result names takes out nothing of it, and R7 decides. Each row:
// production-and-graph with its check of the graph written as the row's kind and the program
// check's failure at 11:00 taken out, so that the check of the graph decides both statements; its
// failing run at 11:00 names test_commit c1, and CI refused it at c1. The command prints what it
// prints with no refusal, and the check reads unknown (disagree) at the transaction of the graph,
// where its pass and its failure count, and false at the deploy, where the newest run decides.
func TestARefusalAtATestCommitTakesOutNoResultOfACheckThatReadsNoCommit(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	for _, k := range readsNoCommit {
		t.Run(k.kind, func(t *testing.T) {
			t.Parallel()
			var got [2]result
			for i, refused := range []bool{false, true} {
				dir := copyScenario(t, "production-and-graph")
				writtenAs(t, dir, graphCheck, k.production, k.texts)
				editResults(t, dir, func(results []map[string]any) {
					for _, r := range results {
						if r["check"] == graphCheck && r["kind"] == "run" && r["outcome"] == "fail" {
							r["test_commit"] = "c1"
						}
					}
				})
				var results []map[string]any
				readJSONFile(t, filepath.Join(dir, "results.json"), &results)
				results = slices.DeleteFunc(results, func(r map[string]any) bool {
					return r["check"] == "a-production-restart-loses-no-message" && r["kind"] == "run" && r["outcome"] == "fail"
				})
				writeJSONFile(t, filepath.Join(dir, "results.json"), results)
				if refused {
					writeJSONFile(t, filepath.Join(dir, "uncited.json"), map[string]any{"c1": map[string]any{graphCheck: "test-not-in-code"}})
				}
				got[i] = runBinary(t, binary, "", "state", "--scenario", dir)
			}
			want := []string{"statement messages-reach-sessions: unknown (check " + graphCheck + ": disagree)",
				"  check " + graphCheck + ": unknown (disagree)"}
			if k.production {
				want = []string{"statement messages-reach-sessions: not met (check " + graphCheck + " is false)",
					"  check " + graphCheck + ": false (failed, counted from d1)"}
			}
			lines := strings.Split(got[1].stdout, "\n")
			if got[1].code != 0 || got[1].stderr != "" || !slices.Contains(lines, want[0]) || !slices.Contains(lines, want[1]) {
				t.Fatalf("with CI's refusal at c1: exit %d, stderr %q, stdout:\n%s\nwant exit 0 and the lines %q", got[1].code, got[1].stderr, got[1].stdout, want)
			}
			expect(t, got[1], got[0])
		})
	}
}

// R48: "At that commit, such a check reads unknown, and no earlier result of it counts." R8: "A
// result at one commit also counts at another commit when no code that the check runs changed
// between the two." An agent or a query of the code reads the commit of its results, as a program
// check of the code does, so the results of a commit where CI refused it count nowhere, at it or
// where R8 would carry them. Each row: refusal with its check of the code written as the row's
// kind, whose pass and caught failing example at c1 count at c2 and at m2. With CI's refusal at
// c2, as the scenario holds it, the check reads true at m2 (the control); with the refusal at c1
// instead, it reads unknown (uncited) at c2 and at m2.
func TestARefusalOfAnAgentOrAQueryOfTheCodeTakesOutTheResultsOfItsCommit(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	const check = "a-restart-loses-no-message"
	const program = ` procedure="program" runner="go" selector="github.com/sjawhar/envoy/cmd/listener TestARestartLosesNoMessage" job="go-checks"`
	for _, k := range []struct {
		kind  string
		texts [][2]string
	}{
		{"an agent of the code", [][2]string{{program, ` procedure="agent"`}, {"Procedure: The program stores", "Procedure: An agent stores"}}},
		{"a query of the code", [][2]string{{program, ` procedure="query"`}, {"Procedure: The program stores", "Procedure: The query stores"}}},
	} {
		t.Run(k.kind, func(t *testing.T) {
			t.Parallel()
			for _, row := range []struct {
				refused string
				want    map[string]string // the line of the check at each subject
			}{
				{"c2", map[string]string{"c2, the head of listener-restart": "unknown (uncited)", "m2, the head of main": "true (passed, counted from c1)"}},
				{"c1", map[string]string{"c2, the head of listener-restart": "unknown (uncited)", "m2, the head of main": "unknown (uncited)"}},
			} {
				dir := copyScenario(t, "refusal")
				writtenAs(t, dir, check, false, k.texts)
				writeJSONFile(t, filepath.Join(dir, "uncited.json"), map[string]any{row.refused: map[string]any{check: "test-not-in-code"}})
				got := runBinary(t, binary, "", "state", "--scenario", dir)
				if got.code != 0 || got.stderr != "" {
					t.Fatalf("CI's refusal at %s: exit %d, stderr %q; want exit 0 and nothing on stderr", row.refused, got.code, got.stderr)
				}
				for subject, want := range row.want {
					if line := "  check " + check + ": " + want; !slices.Contains(subjectLines(got.stdout, subject), line) {
						t.Errorf("CI's refusal at %s: at subject %s %q, want the line %q", row.refused, subject, subjectLines(got.stdout, subject), line)
					}
				}
			}
		})
	}
}
