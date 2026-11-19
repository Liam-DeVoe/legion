package specgraph

// The bases that the sweeps of sweep_test.go and blocksweep_test.go read (sweepBases), and the
// helpers that build and check them.

import (
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph/specgraphtest"
)

// sweepBases is each base that the sweeps read, by its name: each scenario of testdata/scenarios,
// and sixteen shapes that no scenario holds. silent-leg with the job's matrix dropped after c1, one
// run of no matrix at m2 that ended with no result: there a run result of the leg "" at c1, where
// the job had legs, would answer that run, and the check would read true (legFault refuses it).
// Three of carry where a failure and a later pass count at one subject, so the check reads unknown
// (disagree; R7: "A pass and a failure on one such subject and one procedure make the state
// unknown"): a failure of c2 at 10:03, between c1's pass and c1's caught failing example, and a
// later pass of c2 with its caught failing example; c0, c1's parent with c1's tree and ties, whose
// failure at 09:00 counts where c1's results count, and a later pass of c2; and a failure of c2 at
// 10:30 and a pass of m2 on 13 November with its caught failing example. A refusal of CI at a
// commit makes its check read unknown there and takes out the results of that commit alone (R48:
// "At that commit, such a check reads unknown"), so a rule by which a refusal cancelled the
// failure of another commit, or let newer evidence decide at the refused commit, would read the
// later pass true. And two of wait-cycle where a check that is false is the one fault under a
// statement, so a failing check that reads as a check of no statement would leave that statement
// met: envoy-delivers-messages, with its own check false and the check of
// a-message-survives-a-restart true (its two outcomes swapped); and a-message-survives-a-restart,
// with a second check that passes beside its failing one. And eight of production-and-graph, two
// for each kind of check that reads no commit: its query of the graph, and that check written as
// an agent of the graph, as a query of production and as an agent of production. A result of each
// belongs to no commit, so CI's refusal at the commit that its test_commit names takes out nothing
// of it (CheckStates, step 2), and R7 decides. Of each two, one holds test_commit c1 on the check's
// failing run and the other CI's refusal of the check at c1: the refusal sweep adds the refusal to
// the one and the member sweep the test_commit to the other, so an input of each holds both facts,
// and a rule by which such a refusal took out that run would read the check's earlier pass true.
// And two of production-and-graph with its program check of production written as another kind: as
// a program check of the graph, whose runs read the transaction tx1 and count at the commit of their
// test, and as an agent of production, whose block names no test. Each of these ten bases reads its
// check of the graph, or the check that it writes, in the state that the check's kind gives its
// results (readsAt), not only as that kind.
func sweepBases(t *testing.T) map[string]fstest.MapFS {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join("testdata", "scenarios", "*"))
	if err != nil || len(dirs) < 7 {
		t.Fatalf("the scenarios: %q, %v", dirs, err)
	}
	bases := map[string]fstest.MapFS{}
	for _, dir := range dirs {
		bases[filepath.Base(dir)] = scenarioFS(t, dir)
	}
	dropped := maps.Clone(bases["silent-leg"])
	dropped["jobs.json"] = &fstest.MapFile{Data: []byte(`[{"job":"go-checks","leg":"","commit":"m2","ended":true}]`)}
	bases["silent-leg with its matrix dropped at m2"] = dropped
	// carry is carry with records added to results.json, each a copy of carry's result 1 (from 0, a
	// pass of a-restart-loses-no-message at c1) or 2 (from 1, its caught failing example) with the
	// members of set, and a run of go-checks that ended at each commit of jobsAt.
	type add struct {
		from int
		set  map[string]any
	}
	carry := func(jobsAt []string, adds ...add) fstest.MapFS {
		fsys := maps.Clone(bases["carry"])
		editTree(t, fsys, "results.json", func(v any) any {
			results := v.([]any)
			for _, a := range adds {
				r := maps.Clone(results[a.from].(map[string]any))
				maps.Copy(r, a.set)
				results = append(results, r)
			}
			return results
		})
		editTree(t, fsys, "jobs.json", func(v any) any {
			jobs := v.([]any)
			for _, commit := range jobsAt {
				jobs = append(jobs, map[string]any{"job": "go-checks", "leg": "", "commit": commit, "ended": true})
			}
			return jobs
		})
		return fsys
	}
	bases["carry with c2's failure between c1's pass and c1's failing example"] = carry([]string{"c2"},
		add{0, map[string]any{"commit": "c2", "outcome": "fail", "at": "2026-11-12T10:03:00Z", "seq": 5}},
		add{0, map[string]any{"commit": "c2", "at": "2026-11-12T12:00:00Z", "seq": 6}},
		add{1, map[string]any{"commit": "c2", "at": "2026-11-12T12:05:00Z", "seq": 7}})
	parent := carry([]string{"c0", "c2"},
		add{0, map[string]any{"commit": "c0", "outcome": "fail", "at": "2026-11-12T09:00:00Z", "seq": 5}},
		add{0, map[string]any{"commit": "c2", "at": "2026-11-12T12:00:00Z", "seq": 6}},
		add{1, map[string]any{"commit": "c2", "at": "2026-11-12T12:05:00Z", "seq": 7}})
	editTree(t, parent, "history.json", func(v any) any {
		history := v.(map[string]any)
		commits := history["commits"].([]any)
		i := slices.IndexFunc(commits, func(c any) bool { return c.(map[string]any)["id"] == "c1" })
		c1 := maps.Clone(commits[i].(map[string]any))
		c0 := map[string]any{"id": "c0", "parent": c1["parent"], "files": c1["files"]}
		c1["parent"] = "c0"
		commits = slices.Clone(commits)
		commits[i] = c1
		history["commits"] = slices.Insert(commits, i, any(c0))
		return history
	})
	editTree(t, parent, "ties.json", func(v any) any {
		ties := v.(map[string]any)
		ties["c0"] = ties["c1"]
		return ties
	})
	bases["carry with a failure of c0, c1's parent, and a later pass of c2"] = parent
	bases["carry with c2's failure at 10:30 and a later pass of m2"] = carry([]string{"c2", "m2"},
		add{0, map[string]any{"commit": "c2", "outcome": "fail", "at": "2026-11-12T10:30:00Z", "seq": 5}},
		add{0, map[string]any{"commit": "m2", "at": "2026-11-13T12:00:00Z", "seq": 6}},
		add{1, map[string]any{"commit": "m2", "at": "2026-11-13T12:05:00Z", "seq": 7}})
	swapped := maps.Clone(bases["wait-cycle"])
	editTree(t, swapped, "results.json", func(v any) any {
		for _, r := range v.([]any) {
			switch r := r.(map[string]any); r["seq"] {
			case 1.0:
				r["outcome"] = "fail"
			case 5.0:
				r["outcome"] = "pass"
			}
		}
		return v
	})
	bases["wait-cycle with the outcomes of its two first runs swapped"] = swapped
	bases["wait-cycle with a second, passing check of a-message-survives-a-restart"] = withSecondCheck(t, bases["wait-cycle"])
	const graph, program = "the-graph-holds-a-delivery-of-each-message", "a-production-restart-loses-no-message"
	// written is production-and-graph with its check id written as a check of procedure and subject
	// by the one conversion that the command's tests read it through too (specgraphtest.As): its text
	// in the approved spec and in the files at m2, for a check of production each run of it that read
	// the transaction tx1 reading the deploy d1 of the subject instead, as a check of production reads
	// a deploy, and for a check of the graph each run of it that read the deploy d1 reading the
	// transaction tx1 instead. The files at m2 and the approved spec read the check as that kind.
	written := func(id string, procedure Procedure, subject Subject) fstest.MapFS {
		fsys, err := specgraphtest.As(bases["production-and-graph"], id, string(procedure), string(subject))
		if err != nil {
			t.Fatalf("production-and-graph with %s written with procedure %q and subject %q: %v", id, procedure, subject, err)
		}
		is := func(c Check) bool { return c.ID == id && c.Procedure == procedure && c.Subject == subject }
		s, err := ReadScenario(fsys)
		if err != nil {
			t.Fatalf("production-and-graph with %s written with procedure %q and subject %q: %v", id, procedure, subject, err)
		}
		if current, _ := s.Approved.Current(); !slices.ContainsFunc(s.Files["m2"].Checks, is) || !slices.ContainsFunc(current.Checks, is) {
			t.Fatalf("production-and-graph with %s written with procedure %q and subject %q: the files at m2 or the approved spec do not read it so", id, procedure, subject)
		}
		return fsys
	}
	// Each base of production-and-graph reads its check of the graph, or its program check of
	// production as written, in the state that its kind gives the scenario's results of it, a pass
	// at 10:00, a caught failing example and a failure at 11:00 (readsAt), so that a conversion that
	// gives the check its kind and results that read another subject, or no result that counts,
	// gives no base: a check of the graph unknown, since its pass and its failure at the transaction
	// tx1 disagree (R7), and one of production false, since its newest run decides, at the deploy d1.
	disagree := CheckState{State: checkUnknown, Reason: reasonDisagree, Disagree: true}
	failed := CheckState{State: checkFalse, Reason: reasonFailed, From: "d1"}
	type converted struct {
		fsys fstest.MapFS
		want CheckState
	}
	for what, base := range map[string]converted{
		"its query of the graph":                           {bases["production-and-graph"], disagree},
		"its check of the graph as an agent":               {written(graph, ProcedureAgent, SubjectGraph), disagree},
		"its check of the graph as a query of production":  {written(graph, ProcedureQuery, SubjectProduction), failed},
		"its check of the graph as an agent of production": {written(graph, ProcedureAgent, SubjectProduction), failed},
	} {
		withTestCommit := maps.Clone(base.fsys)
		editTree(t, withTestCommit, "results.json", func(v any) any {
			for _, r := range v.([]any) {
				if r := r.(map[string]any); r["check"] == graph && r["kind"] == "run" && r["outcome"] == "fail" {
					r["test_commit"] = "c1"
				}
			}
			return v
		})
		refused := maps.Clone(base.fsys)
		refused["uncited.json"] = &fstest.MapFile{Data: []byte(`{"c1": {"` + graph + `": "test-not-in-code"}}`)}
		for how, fsys := range map[string]fstest.MapFS{"with test_commit c1 on the failing run of ": withTestCommit, "with CI's refusal at c1 of ": refused} {
			name := "production-and-graph " + how + what
			readsAt(t, name, fsys, graph, base.want)
			bases[name] = fsys
		}
	}
	for what, base := range map[string]converted{
		"as a program check of the graph": {written(program, ProcedureProgram, SubjectGraph), disagree},
		"as an agent of production":       {written(program, ProcedureAgent, SubjectProduction), failed},
	} {
		name := "production-and-graph with its program check of production " + what
		readsAt(t, name, base.fsys, program, base.want)
		bases[name] = base.fsys
	}
	return bases
}

// withSecondCheck is wait-cycle with a-restart-keeps-the-order, a check of the code that passes,
// under a-message-survives-a-restart beside its failing check: a copy of
// a-late-session-gets-the-message with its results and its ties.
func withSecondCheck(t *testing.T, base fstest.MapFS) fstest.MapFS {
	t.Helper()
	const old, added = "a-late-session-gets-the-message", "a-restart-keeps-the-order"
	fsys := maps.Clone(base)
	rename := strings.NewReplacer("#"+old, "#"+added, `serves="a-message-reaches-a-late-session"`, `serves="a-message-survives-a-restart"`)
	var block string
	editTree(t, fsys, "approved.json", func(v any) any {
		nodes := v.(map[string]any)["nodes"].([]any)
		for _, n := range nodes {
			if n := n.(map[string]any); n["id"] == old {
				version := maps.Clone(n["versions"].([]any)[0].(map[string]any))
				block = rename.Replace(version["block"].(string))
				version["block"] = block
				c := maps.Clone(n)
				c["id"], c["versions"] = added, []any{version}
				nodes = append(nodes, c)
				break
			}
		}
		v.(map[string]any)["nodes"] = nodes
		return v
	})
	fsys["files/m1/spec.md"] = &fstest.MapFile{Data: append(slices.Clone(fsys["files/m1/spec.md"].Data), "\n"+block...)}
	editTree(t, fsys, "results.json", func(v any) any {
		results := v.([]any)
		for _, r := range slices.Clone(results) {
			if r := r.(map[string]any); r["check"] == old {
				c := maps.Clone(r)
				c["check"], c["seq"] = added, float64(len(results)+1)
				results = append(results, c)
			}
		}
		return results
	})
	editTree(t, fsys, "ties.json", func(v any) any {
		for _, ties := range v.(map[string]any) {
			ties := ties.(map[string]any)
			ties[added] = ties[old]
		}
		return v
	})
	return fsys
}

// readsAt fails the test unless the check id of the base name reads want, with id as its ID, at
// each subject of the base: its state, its reason, its detail and the subject that it counts from.
func readsAt(t *testing.T, name string, base fs.FS, id string, want CheckState) {
	t.Helper()
	s, err := ReadScenario(base)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	want.ID = id
	for i := range s.Subjects {
		state, err := Derive(s.Input(i))
		if err != nil {
			t.Fatalf("%s: subject %d: %v", name, i+1, err)
		}
		if j := slices.IndexFunc(state.Checks, func(c CheckState) bool { return c.ID == id }); j < 0 || state.Checks[j] != want {
			t.Fatalf("%s: at subject %d the check %s reads %+v, want %+v", name, i+1, id, state.Checks[max(j, 0):max(j+1, 0)], want)
		}
	}
}
