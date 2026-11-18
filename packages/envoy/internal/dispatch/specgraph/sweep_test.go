package specgraph

import (
	"encoding/json/v2"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// sweepMaps is each object of the JSON files of a scenario whose names its writer picks, as the
// forms write them ({"<repository path>":"<content label>"}, {"<commit>":{"<check id>":…}}), by its
// place: the names of the path to it from the root of the file, "*" for any name or index. A member
// of such an object is an entry of a map: a record of the scenario, which the sweep puts null in the
// place of and TestTakingOutAWholeRecordMovesAStateOnlyByItsRight takes out, and whose key the
// sweep writes as a name that names nothing.
var sweepMaps = map[string][][]string{
	"history.json": {{"commits", "*", "files"}},
	"ties.json":    {{}, {"*"}},
	"uncited.json": {{}, {"*"}},
}

// sweepPaths is each place of a repository path in the JSON files of a scenario: the keys of a tree
// of the history, and the entries of a tie. The sweep writes each such path in the forms that name
// no repository path, which no tie entry covers.
var sweepPaths = map[string]struct{ keysOf, values [][]string }{
	"history.json": {keysOf: [][]string{{"commits", "*", "files"}}},
	"ties.json":    {values: [][]string{{"*", "*", "*"}}},
}

// namesOfNothing are the texts that the sweep writes at each string place and as each key of a map:
// no name, and a name of no letter and no digit.
var namesOfNothing = []string{"", " "}

// notRepoPaths are the forms of path that name no repository path, as the sweep writes them.
func notRepoPaths(path string) []string {
	forms := []string{"./" + path, "/" + path, path + "/"}
	if strings.Contains(path, "/") {
		forms = append(forms, strings.Replace(path, "/", "//", 1))
	}
	return forms
}

// An input that says nothing at one place reads as the zero value of that place, a real value, and
// a rule that read it as one would turn a state toward true. So the sweep reads each JSON file of each
// scenario of testdata/scenarios as a JSON tree, with no Go type, and derives the state of each input
// that says nothing at one place: each member of each object of the format left out, null at each
// place (a member, an entry of a map, an element of a list, the whole file), each file of the
// scenario taken out; each name that names nothing: "" and " " at each string place and as each
// key of each map of the format (sweepMaps), and each repository path of a tree or a tie written as
// "./<path>", "/<path>", "<path>/" and with a "/" doubled (sweepPaths). No such input may move a
// check toward true, a statement or its own state toward met, or a wait toward ends, against the
// state of the whole scenario: a member with a mark of zero is no exception, and a mark whose
// reading moves a state toward true is wrong. The reader may refuse the input. A record taken out
// whole, such as a result, is another input, not one that says nothing
// (TestTakingOutAWholeRecordMovesAStateOnlyByItsRight), and so is a named leg written where a
// record has "": it states that the job had legs there, which an older result of that leg answers
// by right (R8). The scenarios hold each shape that a rule reads: a check of the code with a matrix
// and a silent leg, a wait on its statement, a failure of production at the at of its pass, a
// refusal of CI, and, in production-and-graph, a program check of production whose runs name the
// commit of their test (test_commit), a query of the graph at the transaction of its subject
// (graph_tx), and a run of a job that has not ended. So each member that a rule reads is in a
// record of some scenario, and each place of it is swept. The sweep also reads the shapes that no
// scenario holds (sweepBases). One right: a repository path of a tree written " " is a real path,
// since git holds a file of that name (git ls-tree of a tree that holds one lists the path " "), so
// the tree changed at a path that a tie covers, and a result there, a failure among them, carries
// no more across it (R8: "A result at one commit also counts at another commit when no code that
// the check runs changed between the two"); a base whose failure at one commit carries to another
// shows it. A content label is no path, and one of " " names no content, so the reader refuses it
// and it has no right.
func TestNoInputThatSaysNothingMovesAStateTowardTrue(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	byRight := func(what string) string {
		if strings.HasPrefix(what, "history.json /commits/") && strings.Contains(what, "/files/") && strings.HasSuffix(what, ` as the key " "`) {
			return `the path " " of the tree is a real path, which git holds, so the tree changed at a path that a tie covers, and the carry ends (R8)`
		}
		return ""
	}
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			inputs, roundTrip := sweepInputs(t, base)
			whole := baseStates(t, base)
			if got, err := sweepStates(roundTrip); err != nil || !maps.Equal(got, whole) {
				t.Fatalf("each JSON file of the scenario written again by the sweep does not read as the scenario: %v", err)
			}
			n := sweepAgainst(t, whole, inputs, byRight)
			legs := 0 // the inputs that write the leg of no matrix at the leg of a record
			for _, in := range inputs {
				if strings.HasSuffix(in.what, `/leg as ""`) {
					legs++
				}
			}
			t.Logf(`%d inputs: %d refused, %d read as the whole scenario, %d read another state, %d of them toward true by right; %d of them a leg written as ""`,
				n.inputs, n.refused, n.same, n.moved, n.byRight, legs)
		})
	}
}

// sweepBases is each base that the sweeps read, by its name: each scenario of testdata/scenarios,
// and three shapes that no scenario holds. silent-leg with the job's matrix dropped after c1, one
// run of no matrix at m2 that ended with no result: there a run result of the leg "" at c1, where
// the job had legs, would answer that run, and the check would read true (legFault refuses it).
// carry with a failure of c2 at 10:03, between c1's pass and c1's caught failing example, and a
// later pass of c2 with its caught failing example: c2 reads unknown (disagree), and a refusal of
// CI at c1 that took out c2's failure with c1's evidence would make it read true (CheckStates, step
// 2). And carry with c0, c1's parent with c1's tree and ties, whose failure at 09:00 counts where
// c1's results count, and a later pass of c2: a refusal at c1 that took out c0's failure as earlier
// evidence would make c2 read true.
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
	return bases
}

// baseStates is the state of each check, statement and wait of the base, as sweepStates gives it.
func baseStates(t *testing.T, base fs.FS) map[string]string {
	t.Helper()
	whole, err := sweepStates(base)
	if err != nil {
		t.Fatalf("the base: %v", err)
	}
	return whole
}

// sweepCount is what a sweep read: its inputs, how many the reader or the derivation refused, how
// many read as the base, how many read another state, and how many of those moved a state toward
// true by a right that the sweep gives.
type sweepCount struct{ inputs, refused, same, moved, byRight int }

// sweepAgainst derives each input and compares its states with whole, the states of its base: an
// input that moves a state toward true fails the test, unless byRight, when it is not nil, gives
// the reason of a right for it.
func sweepAgainst(t *testing.T, whole map[string]string, inputs []sweepInput, byRight func(what string) string) sweepCount {
	t.Helper()
	n := sweepCount{inputs: len(inputs)}
	for _, in := range inputs {
		got, err := sweepStates(in.fsys)
		switch {
		case err != nil:
			n.refused++
		case maps.Equal(got, whole):
			n.same++
		default:
			n.moved++
			toward := towardTrue(whole, got)
			switch {
			case len(toward) == 0:
			case byRight != nil && byRight(in.what) != "":
				n.byRight++
			default:
				t.Errorf("%s: %s", in.what, strings.Join(toward, "; "))
			}
		}
	}
	return n
}

// A refusal of CI takes out only passing evidence (CheckStates, step 2), so adding one moves no
// check toward true, no statement or its own state toward met, and no wait toward ends. The sweep
// adds one refusal at a time to uncited.json of each base (sweepBases), at each commit that a record
// of the base names, for each check of its approved spec that the base does not refuse there.
func TestAddingARefusalOfCIMovesNoStateTowardTrue(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			s, err := ReadScenario(base)
			if err != nil {
				t.Fatalf("the base: %v", err)
			}
			current, _ := s.Approved.Current()
			var inputs []sweepInput
			for _, commit := range namesOf(t, base)["commit"] {
				for _, c := range current.Checks {
					if _, refused := s.Uncited[commit][c.ID]; refused {
						continue
					}
					fsys := maps.Clone(base)
					editTree(t, fsys, "uncited.json", func(v any) any {
						uncited := v.(map[string]any)
						checks := map[string]any{}
						if at, held := uncited[commit].(map[string]any); held {
							checks = maps.Clone(at)
						}
						checks[c.ID] = "test-did-not-run"
						uncited[commit] = checks
						return uncited
					})
					inputs = append(inputs, sweepInput{fmt.Sprintf("uncited.json: a refusal of %s at %s", c.ID, commit), fsys})
				}
			}
			n := sweepAgainst(t, baseStates(t, base), inputs, nil)
			t.Logf("%d refusals added: %d refused, %d read as the base, %d read another state", n.inputs, n.refused, n.same, n.moved)
		})
	}
}

// addableMembers is each member that a record of a JSON file of a scenario can leave out (the ten
// members marked zero that a file can leave out, memberMark), by the place of the list of its
// records and the kind of the values that the sweep gives it.
var addableMembers = map[string]struct {
	records []string
	members [][2]string // each member and its kind
}{
	"results.json": {nil, [][2]string{{"commit", "commit"}, {"deploy", "deploy"}, {"graph_tx", "graph_tx"}, {"job", "job"},
		{"attempt", "attempt"}, {"fault_author", "author"}, {"test_authors", "authors"}, {"test_commit", "commit"}}},
	"subjects.json": {nil, [][2]string{{"graph_tx", "graph_tx"}}},
	"history.json":  {[]string{"commits"}, [][2]string{{"parent", "commit"}}},
}

// A member that a record leaves out, added, states one more fact of the record. No such input may
// move a state toward true: the sweep adds each member of addableMembers to each record of each base
// (sweepBases) that leaves it out, one at a time, with each value of its kind that a record of the
// base names and one that none names. A test_commit on a result of a check of the code moves
// nothing, since the commit of a result has one reading, its commit (resultCommit), for the judge
// and for the leg rule alike, and a commit on a result of a check of production or of the graph is
// refused.
func TestAddingAMemberMovesNoStateTowardTrue(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			names := namesOf(t, base)
			values := map[string][]any{"attempt": {2}}
			for kind, other := range map[string]string{"commit": "c9", "deploy": "d9", "graph_tx": "g9", "job": "other-job", "author": "other-author"} {
				for _, v := range append(slices.Clone(names[kind]), other) {
					values[kind] = append(values[kind], v)
					if kind == "author" {
						values["authors"] = append(values["authors"], []any{v})
					}
				}
			}
			var inputs []sweepInput
			for _, file := range slices.Sorted(maps.Keys(addableMembers)) {
				var tree any
				if err := json.Unmarshal(base[file].Data, &tree); err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				list := tree
				for _, name := range addableMembers[file].records {
					list = list.(map[string]any)[name]
				}
				for i, record := range list.([]any) {
					place := append(slices.Clone(addableMembers[file].records), strconv.Itoa(i))
					for _, m := range addableMembers[file].members {
						if _, set := record.(map[string]any)[m[0]]; set {
							continue
						}
						for _, v := range values[m[1]] {
							fsys := maps.Clone(base)
							fsys[file] = &fstest.MapFile{Data: encodeTree(t, treeWith(tree, append(slices.Clone(place), m[0]), v))}
							inputs = append(inputs, sweepInput{fmt.Sprintf("%s %s with %s %s", file, treePointer(place), m[0], encodeTree(t, v)), fsys})
						}
					}
				}
			}
			n := sweepAgainst(t, baseStates(t, base), inputs, nil)
			t.Logf("%d members added: %d refused, %d read as the base, %d read another state", n.inputs, n.refused, n.same, n.moved)
		})
	}
}

// A text inside a block that names nothing, " " or "", at one place: the id or a value of an
// attribute of the block's opening line, or a line of its body after the marker of a field of a
// check (blockTexts), in a block of approved.json or of a spec file of the base. No such input may
// move a state toward true: the reader refuses a relation of an approved block that is no id
// (notAnID), and Validate refuses one in the files.
func TestNoNameOfNothingInsideABlockMovesAStateTowardTrue(t *testing.T) {
	t.Parallel()
	bases := sweepBases(t)
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := bases[name]
			var inputs []sweepInput
			var tree any
			if err := json.Unmarshal(base["approved.json"].Data, &tree); err != nil {
				t.Fatalf("approved.json: %v", err)
			}
			for n, node := range tree.(map[string]any)["nodes"].([]any) {
				for v, version := range node.(map[string]any)["versions"].([]any) {
					place := []string{"nodes", strconv.Itoa(n), "versions", strconv.Itoa(v), "block"}
					block := version.(map[string]any)["block"].(string)
					for _, at := range blockTexts(block) {
						for _, form := range namesOfNothing {
							if block[at[0]:at[1]] != form {
								fsys := maps.Clone(base)
								fsys["approved.json"] = &fstest.MapFile{Data: encodeTree(t, treeWith(tree, place, block[:at[0]]+form+block[at[1]:]))}
								inputs = append(inputs, sweepInput{fmt.Sprintf("approved.json %s: %q as %q", treePointer(place), block[at[0]:at[1]], form), fsys})
							}
						}
					}
				}
			}
			for _, file := range slices.Sorted(maps.Keys(base)) {
				if !strings.HasPrefix(file, "files/") || path.Ext(file) != ".md" {
					continue
				}
				text := string(base[file].Data)
				for _, at := range blockTexts(text) {
					for _, form := range namesOfNothing {
						if text[at[0]:at[1]] != form {
							fsys := maps.Clone(base)
							fsys[file] = &fstest.MapFile{Data: []byte(text[:at[0]] + form + text[at[1]:])}
							inputs = append(inputs, sweepInput{fmt.Sprintf("%s at byte %d: %q as %q", file, at[0], text[at[0]:at[1]], form), fsys})
						}
					}
				}
			}
			n := sweepAgainst(t, baseStates(t, base), inputs, nil)
			t.Logf("%d texts inside a block: %d refused, %d read as the base, %d read another state", n.inputs, n.refused, n.same, n.moved)
		})
	}
}

// blockOpening is the opening line of a block, with what its braces hold, and blockAttribute the
// id and each value of an attribute in it.
var (
	blockOpening   = regexp.MustCompile(`^:::[a-z]+\{(.*)\}$`)
	blockAttribute = regexp.MustCompile(`#([^ }]*)|[a-z_]+="([^"]*)"`)
)

// blockTexts is the place of each text inside each block of text, as the offsets of its first and
// its last byte and one: the id and each value of an attribute of the block's opening line, and
// each line of its body, after the marker of a field of a check and its space where it has one.
func blockTexts(text string) [][2]int {
	var places [][2]int
	inBlock, at := false, 0
	for _, line := range strings.SplitAfter(text, "\n") {
		body := strings.TrimSuffix(line, "\n")
		switch open := blockOpening.FindStringSubmatchIndex(body); {
		case !inBlock && open != nil:
			inBlock = true
			for _, m := range blockAttribute.FindAllStringSubmatchIndex(body[open[2]:open[3]], -1) {
				for g := 2; g < len(m); g += 2 {
					if m[g] >= 0 {
						places = append(places, [2]int{at + open[2] + m[g], at + open[2] + m[g+1]})
					}
				}
			}
		case inBlock && body == ":::":
			inBlock = false
		case inBlock && body != "":
			start := 0
			for _, marker := range checkFields {
				if strings.HasPrefix(body, marker+" ") {
					start = len(marker) + 1
				}
			}
			places = append(places, [2]int{at + start, at + len(body)})
		}
		at += len(line)
	}
	return places
}

// namesOf is each name that a record of the scenario fsys names, by its kind (commit, deploy,
// graph_tx, job, author), sorted, each once: the commits of the history, of the subjects, of the
// results, of the job runs and of the maps of ties.json and uncited.json; the deploys and the
// transactions of the subjects and of the results; the jobs of the results and of the job runs; and
// the authors of the results.
func namesOf(t *testing.T, fsys fstest.MapFS) map[string][]string {
	t.Helper()
	names := map[string]map[string]bool{}
	add := func(kind string, v any) {
		if s, ok := v.(string); ok && s != "" {
			if names[kind] == nil {
				names[kind] = map[string]bool{}
			}
			names[kind][s] = true
		}
	}
	read := func(file string) any {
		var tree any
		if err := json.Unmarshal(fsys[file].Data, &tree); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		return tree
	}
	for _, c := range read("history.json").(map[string]any)["commits"].([]any) {
		add("commit", c.(map[string]any)["id"])
	}
	for _, file := range []string{"subjects.json", "results.json", "jobs.json"} {
		for _, r := range read(file).([]any) {
			r := r.(map[string]any)
			add("commit", r["commit"])
			add("commit", r["test_commit"])
			add("deploy", r["deploy"])
			add("graph_tx", r["graph_tx"])
			add("job", r["job"])
			add("author", r["fault_author"])
			if authors, ok := r["test_authors"].([]any); ok {
				for _, a := range authors {
					add("author", a)
				}
			}
		}
	}
	for _, file := range []string{"ties.json", "uncited.json"} {
		for commit := range read(file).(map[string]any) {
			add("commit", commit)
		}
	}
	sorted := map[string][]string{}
	for kind, set := range names {
		sorted[kind] = slices.Sorted(maps.Keys(set))
	}
	return sorted
}

// sweepInput is one input of the sweep: what it changed, and the files of the scenario with it.
type sweepInput struct {
	what string
	fsys fstest.MapFS
}

// sweepInputs is each input of the sweep of the scenario base, and base with each JSON file written
// again by the sweep's own decoder and encoder, unchanged, which the sweep reads as base.
func sweepInputs(t *testing.T, base fstest.MapFS) ([]sweepInput, fstest.MapFS) {
	t.Helper()
	var inputs []sweepInput
	roundTrip := maps.Clone(base)
	with := func(what, name string, data []byte) {
		fsys := maps.Clone(base)
		if data == nil {
			delete(fsys, name)
		} else {
			fsys[name] = &fstest.MapFile{Data: data}
		}
		inputs = append(inputs, sweepInput{what, fsys})
	}
	for _, name := range slices.Sorted(maps.Keys(base)) {
		with(name+" taken out", name, nil)
		if path.Ext(name) != ".json" {
			continue
		}
		var tree any
		if err := json.Unmarshal(base[name].Data, &tree); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		roundTrip[name] = &fstest.MapFile{Data: encodeTree(t, tree)}
		with(name+" as null", name, []byte("null"))
		var walk func(v any, at []string)
		walk = func(v any, at []string) {
			switch v := v.(type) {
			case map[string]any:
				entries := slices.ContainsFunc(sweepMaps[name], func(p []string) bool { return placeMatches(p, at) })
				paths := slices.ContainsFunc(sweepPaths[name].keysOf, func(p []string) bool { return placeMatches(p, at) })
				for _, key := range slices.Sorted(maps.Keys(v)) {
					place := append(slices.Clone(at), key)
					with(name+" "+treePointer(place)+" as null", name, encodeTree(t, treeChanged(tree, place, false)))
					if !entries {
						with(name+" "+treePointer(place)+" left out", name, encodeTree(t, treeChanged(tree, place, true)))
					}
					var renames []string
					if entries {
						renames = namesOfNothing
					}
					if paths {
						renames = append(slices.Clone(renames), notRepoPaths(key)...)
					}
					for _, renamed := range renames {
						if _, taken := v[renamed]; !taken {
							with(fmt.Sprintf("%s %s as the key %q", name, treePointer(place), renamed), name, encodeTree(t, treeRenamed(tree, place, renamed)))
						}
					}
					walk(v[key], place)
				}
			case []any:
				for i, x := range v {
					place := append(slices.Clone(at), strconv.Itoa(i))
					with(name+" "+treePointer(place)+" as null", name, encodeTree(t, treeChanged(tree, place, false)))
					walk(x, place)
				}
			case string:
				forms := slices.Clone(namesOfNothing)
				if slices.ContainsFunc(sweepPaths[name].values, func(p []string) bool { return placeMatches(p, at) }) {
					forms = append(forms, notRepoPaths(v)...)
				}
				for _, form := range forms {
					if form != v {
						with(fmt.Sprintf("%s %s as %q", name, treePointer(at), form), name, encodeTree(t, treeWith(tree, at, form)))
					}
				}
			}
		}
		walk(tree, nil)
	}
	return inputs, roundTrip
}

// sweepStates is the state of each check, of each statement and its own state, and of each wait of
// each unit, at each subject of the scenario fsys, by its place in the output: the subject by its
// place in subjects.json, not its value, since an input that leaves out a member of a subject, such
// as graph_tx, derives the states of that subject; or the refusal of the reader or of the derivation.
func sweepStates(fsys fs.FS) (map[string]string, error) {
	s, err := ReadScenario(fsys)
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for i := range s.Subjects {
		state, err := Derive(s.Input(i))
		if err != nil {
			return nil, err
		}
		at := fmt.Sprintf("subject %d", i+1)
		for _, c := range state.Checks {
			states[at+" check "+c.ID] = c.State
		}
		for _, st := range state.Statements {
			states[at+" statement "+st.ID] = st.State
			states[at+" own state of "+st.ID] = st.Own
		}
		for _, u := range s.Units {
			for _, statement := range u.WaitsOn {
				states[at+" wait of "+u.ID+" on "+statement] = map[bool]string{true: "ends", false: "holds"}[state.WaitEnds(statement)]
			}
		}
	}
	return states, nil
}

// towardTrue is each state of got that reads true, met or ends where whole reads otherwise.
func towardTrue(whole, got map[string]string) []string {
	var toward []string
	for _, at := range slices.Sorted(maps.Keys(got)) {
		if v := got[at]; (v == checkTrue || v == statementMet || v == "ends") && whole[at] != v {
			toward = append(toward, fmt.Sprintf("%s: %q, and %q in the whole scenario", at, v, whole[at]))
		}
	}
	return toward
}

// scenarioFS is each file of the scenario at dir, by its path in the scenario.
func scenarioFS(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		fsys[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

// recordKinds is each kind of record of a scenario, a fact that CI or the repository recorded about
// a check, by its file and its place: a result, a run of a job, a refusal of CI, a tie, a commit of
// the history and a file of a commit's tree. The approved spec, the subjects and the units are what
// the derivation judges and where, not records.
var recordKinds = map[string][][]string{
	"results.json": {{"*"}},
	"jobs.json":    {{"*"}},
	"uncited.json": {{"*"}, {"*", "*"}},
	"ties.json":    {{"*"}, {"*", "*"}},
	"history.json": {{"commits", "*"}, {"commits", "*", "files", "*"}},
}

// A record taken out whole is another input, not one that says nothing: the history in which the
// fact did not happen. A record that states a fact against a check, taken out, can move a state
// toward true by right, and each kind of record has a row here, with the direction that it must
// have and its reason; a kind with no row fails the test. A file taken out of a commit's tree is
// not there at that commit, so it changed, and the carry of a result across it ends (R8), as a
// changed label ends it: that row must not read true. Each row derives the check at the subject of
// its scenario with the record, and with the record taken out; setup makes the scenario first.
func TestTakingOutAWholeRecordMovesAStateOnlyByItsRight(t *testing.T) {
	t.Parallel()
	const c = "a-restart-loses-no-message"
	const q = "production-delivery-takes-under-a-second"
	type row struct {
		kind, reason      string
		scenario          string
		setup             func(t *testing.T, fsys fstest.MapFS)
		file              string
		place             []string
		subject           int    // from 1
		check             string // whose state the row reads
		with, without     string // the state of the check with the record and with it taken out
		mayMoveTowardTrue bool
	}
	// carriedFailure is carry with a failure of c at c1 after its pass, which carries to c2, and a
	// pass and a caught failing example at c2 after it: c2 reads unknown (disagree).
	carriedFailure := func(t *testing.T, fsys fstest.MapFS) {
		editTree(t, fsys, "results.json", func(v any) any {
			results := v.([]any)
			run := func(commit, outcome, at string, seq int) map[string]any {
				r := maps.Clone(results[0].(map[string]any))
				r["commit"], r["outcome"], r["at"], r["seq"] = commit, outcome, at, seq
				return r
			}
			fault := maps.Clone(results[1].(map[string]any))
			fault["commit"], fault["at"], fault["seq"] = "c2", "2026-11-13T09:05:00Z", 7
			return append(results, run("c1", "fail", "2026-11-12T10:10:00Z", 5), run("c2", "pass", "2026-11-13T09:00:00Z", 6), fault)
		})
	}
	rows := []row{
		// The first row: a file of a commit's tree must not move a state toward true.
		{kind: "a file of a commit's tree", reason: "a tied file that the later commit does not hold changed, so the carry ends (R8), as a changed label ends it",
			scenario: "carry", file: "history.json", place: []string{"commits", "2", "files", "packages/envoy/cmd/listener/main.go"},
			subject: 1, check: c, with: "true", without: "unknown"},
		{kind: "a failing result", reason: "the failure is the record against the check, and the history without it holds no failure (R7: \"a later failure cancels an earlier pass\")",
			scenario: "silent-leg", file: "results.json", place: []string{"5"},
			subject: 3, check: q, with: "false", without: "true", mayMoveTowardTrue: true},
		{kind: "a run of a job", reason: "with no run of leg arm that ended, no leg is silent (R9: \"after a run of its job ends\")",
			scenario: "silent-leg", file: "jobs.json", place: []string{"1"},
			subject: 1, check: c, with: "unknown", without: "true", mayMoveTowardTrue: true},
		{kind: "the refusals of CI at a commit", reason: "CI then refused no check at the commit (R48)",
			scenario: "refusal", file: "uncited.json", place: []string{"c2"},
			subject: 1, check: c, with: "unknown", without: "true", mayMoveTowardTrue: true},
		{kind: "a refusal of CI", reason: "CI then refused no check at the commit (R48)",
			scenario: "refusal", file: "uncited.json", place: []string{"c2", c},
			subject: 1, check: c, with: "unknown", without: "true", mayMoveTowardTrue: true},
		{kind: "the ties at a commit", reason: "no tie is then known at the commit, and a tie that is not known carries no result (R18, R8), so the failure there counts nowhere else",
			scenario: "carry", setup: carriedFailure, file: "ties.json", place: []string{"c1"},
			subject: 1, check: c, with: "unknown", without: "true", mayMoveTowardTrue: true},
		{kind: "a tie", reason: "the tie is then not known at the commit, and a tie that is not known carries no result (R18, R8), so the failure there counts nowhere else",
			scenario: "carry", setup: carriedFailure, file: "ties.json", place: []string{"c1", c},
			subject: 1, check: c, with: "unknown", without: "true", mayMoveTowardTrue: true},
		{kind: "a commit of the history", reason: "the history then holds no tree of the commit, so no result carries from it (R8), and the failure there counts nowhere else",
			scenario: "carry", setup: carriedFailure, file: "history.json", place: []string{"commits", "1"},
			subject: 1, check: c, with: "unknown", without: "true", mayMoveTowardTrue: true},
	}
	for kind := range maps.Keys(recordKinds) {
		for _, pattern := range recordKinds[kind] {
			if !slices.ContainsFunc(rows, func(r row) bool { return r.file == kind && placeMatches(pattern, r.place) }) {
				t.Errorf("no row takes out a record of %s at %s", kind, treePointer(pattern))
			}
		}
	}
	for _, r := range rows {
		if !r.mayMoveTowardTrue && r.without == checkTrue {
			t.Errorf("%s: a record of this kind must not move a state toward true, and the row reads true without it", r.kind)
		}
		base := scenarioFS(t, filepath.Join("testdata", "scenarios", r.scenario))
		if r.setup != nil {
			r.setup(t, base)
		}
		without := maps.Clone(base)
		editTree(t, without, r.file, func(v any) any { return treeChanged(v, r.place, true) })
		for _, read := range []struct {
			fsys fstest.MapFS
			want string
			form string
		}{{base, r.with, "with"}, {without, r.without, "without"}} {
			states, err := sweepStates(read.fsys)
			if err != nil {
				t.Errorf("%s, %s the record: %v", r.kind, read.form, err)
				continue
			}
			at := ""
			for key := range states {
				if strings.HasPrefix(key, fmt.Sprintf("subject %d ", r.subject)) && strings.HasSuffix(key, " check "+r.check) {
					at = key
				}
			}
			if got := states[at]; got != read.want {
				t.Errorf("%s, %s the record %s %s: the check reads %q, want %q (%s)", r.kind, read.form, r.file, treePointer(r.place), got, read.want, r.reason)
			}
		}
	}
}

// editTree changes the JSON file name of fsys by change, which takes its tree and gives the new one.
func editTree(t *testing.T, fsys fstest.MapFS, name string, change func(any) any) {
	t.Helper()
	var tree any
	if err := json.Unmarshal(fsys[name].Data, &tree); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	fsys[name] = &fstest.MapFile{Data: encodeTree(t, change(tree))}
}

// placeMatches is whether the place at is the place pattern, whose "*" stands for any one name.
func placeMatches(pattern, at []string) bool {
	if len(pattern) != len(at) {
		return false
	}
	for i, name := range pattern {
		if name != "*" && name != at[i] {
			return false
		}
	}
	return true
}

// treeChanged is a copy of the JSON tree v with the value at place null, or, with leftOut, taken
// out of its object or its list.
func treeChanged(v any, place []string, leftOut bool) any {
	if len(place) == 0 {
		return nil
	}
	switch v := v.(type) {
	case map[string]any:
		c := maps.Clone(v)
		if len(place) == 1 && leftOut {
			delete(c, place[0])
		} else {
			c[place[0]] = treeChanged(v[place[0]], place[1:], leftOut)
		}
		return c
	case []any:
		i, _ := strconv.Atoi(place[0])
		if len(place) == 1 && leftOut {
			return slices.Delete(slices.Clone(v), i, i+1)
		}
		c := slices.Clone(v)
		c[i] = treeChanged(v[i], place[1:], leftOut)
		return c
	}
	panic(fmt.Sprintf("no value at %q", place))
}

// treeWith is a copy of the JSON tree v with x at place.
func treeWith(v any, place []string, x any) any {
	if len(place) == 0 {
		return x
	}
	switch v := v.(type) {
	case map[string]any:
		c := maps.Clone(v)
		c[place[0]] = treeWith(v[place[0]], place[1:], x)
		return c
	case []any:
		i, _ := strconv.Atoi(place[0])
		c := slices.Clone(v)
		c[i] = treeWith(v[i], place[1:], x)
		return c
	}
	panic(fmt.Sprintf("no value at %q", place))
}

// treeRenamed is a copy of the JSON tree v with the member at place, the last name of place, under
// the name renamed in its object.
func treeRenamed(v any, place []string, renamed string) any {
	object := v
	for _, name := range place[:len(place)-1] {
		switch o := object.(type) {
		case map[string]any:
			object = o[name]
		case []any:
			i, _ := strconv.Atoi(name)
			object = o[i]
		}
	}
	c := maps.Clone(object.(map[string]any))
	c[renamed] = c[place[len(place)-1]]
	delete(c, place[len(place)-1])
	return treeWith(v, place[:len(place)-1], c)
}

// treePointer is the JSON pointer of place.
func treePointer(place []string) string {
	var b strings.Builder
	for _, name := range place {
		b.WriteString("/" + pointerToken.Replace(name))
	}
	return b.String()
}

// encodeTree is the JSON text of the tree v.
func encodeTree(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
