package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// testPrint runs print over the fixtures of testdata/compare: it writes the current approved
// document of each node it is named, and of each check of a statement it is named, into the files
// of a directory, byte for byte as the files of the fixture hold them. A print of each current
// node passes the comparison, and the same files with one word more fail it on that statement
// (v-two-specs: "A change copies the text that Dispatch prints for an approved statement and its
// checks, and it passes the comparison. The same text with one word changed fails it").
func testPrint(t *testing.T, binary string) {
	t.Run("print writes a statement and its two checks as the files hold them", func(t *testing.T) {
		t.Parallel()
		approved := fixture(t, "compare", "two-checks", "approved.json")
		fixed, err := os.ReadFile(fixture(t, "compare", "two-checks", "spec", "orders.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		top := splitDocuments(string(fixed))[0]
		writeFile(t, filepath.Join(dir, "acme-keeps-each-order.yaml"), []byte(top))
		file := filepath.Join(dir, "acme-keeps-each-order.yaml")
		expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "an-order-survives-a-restart", "--dir", dir), result{
			stdout: "spec-check: wrote an-order-survives-a-restart to " + file + ":7\n" +
				"spec-check: wrote a-crash-keeps-each-order to " + file + ":14\n" +
				"spec-check: wrote a-restart-keeps-each-order to " + file + ":30\n",
		})
		printed, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(fixed, printed) || len(printed) <= len(top) {
			t.Errorf("the files of two-checks do not start with the printed text %q", printed)
		}
	})
	t.Run("print writes components, an interface and a term, each node once", func(t *testing.T) {
		t.Parallel()
		approved := fixture(t, "compare", "approved-text", "approved.json")
		dir := t.TempDir()
		components, terms := filepath.Join(dir, "components.yaml"), filepath.Join(dir, "terms.yaml")
		expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "orders-to-billing", "--id", "order", "--id", "billing", "--id", "orders", "--id", "order", "--dir", dir), result{
			stdout: "spec-check: wrote billing to " + components + ":1\n" +
				"spec-check: wrote orders to " + components + ":8\n" +
				"spec-check: wrote orders-to-billing to " + components + ":15\n" +
				"spec-check: wrote order to " + terms + ":1\n",
		})
		fixed, err := os.ReadFile(fixture(t, "compare", "approved-text", "spec", "components.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		documents := splitDocuments(string(fixed))
		want := map[string]string{"components.yaml": strings.Join(documents[:3], ""), "terms.yaml": documents[4]}
		if got := readTexts(t, dir); !maps.Equal(got, want) {
			t.Errorf("print wrote %q, want %q", got, want)
		}
	})
	t.Run("print exits 2 on an id the approved spec does not hold or removed", func(t *testing.T) {
		t.Parallel()
		approved := fixture(t, "compare", "removed-kept", "approved.json")
		dir := t.TempDir()
		expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "acme-keeps-each-order", "--id", "an-order-is-paid-once", "--id", "each-order-gets-one-bill", "--dir", dir), result{
			code: 2,
			stderr: "spec-check: " + approved + `: the approved spec removed node "an-order-is-paid-once", so it has no current text` + "\n" +
				"spec-check: " + approved + `: the approved spec holds no node "each-order-gets-one-bill"` + "\n",
		})
		if got := readTexts(t, dir); len(got) != 0 {
			t.Errorf("print wrote %q, want nothing", got)
		}
	})

	entries, err := os.ReadDir(fixture(t, "compare"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run("a print of each current node of "+entry.Name()+" passes compare, and one word more fails it", func(t *testing.T) {
			t.Parallel()
			approved := fixture(t, "compare", entry.Name(), "approved.json")
			a := readApprovedFile(t, approved)
			current, _ := a.Current()
			args := []string{"print", "--approved", approved}
			for _, id := range slices.Sorted(maps.Keys(a.Nodes)) {
				if !a.Nodes[id].Removed {
					args = append(args, "--id", id)
				}
			}
			dir := t.TempDir()
			if printed := runBinary(t, binary, "", append(args, "--dir", dir)...); printed.code != 0 || printed.stderr != "" {
				t.Fatalf("print: exit %d, stderr %q", printed.code, printed.stderr)
			}
			n := strconv.Itoa(len(current.Statements))
			expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{stdout: passLine(dir, n+" of "+n)})

			tree, problems := specgraph.Parse(readDir(t, dir))
			if len(problems) != 0 || len(tree.Statements) == 0 {
				t.Fatalf("the printed files: %d statements, problems %+v", len(tree.Statements), problems)
			}
			planted := tree.Statements[0]
			path := filepath.Join(dir, planted.Pos.File)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			line := "\n  - " + strings.Split(planted.Text, "\n")[0] + "\n"
			if !strings.Contains(string(content), line) {
				t.Fatalf("%s holds no line %q", path, line)
			}
			writeFile(t, path, []byte(strings.Replace(string(content), line, "\n  - planted "+line[len("\n  - "):], 1)))
			expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{
				code:   1,
				stdout: "spec-check: " + dir + ": the comparison fails on 1 node\n",
				stderr: "spec-check: " + path + ":" + strconv.Itoa(planted.Pos.Line) + ": " + planted.ID + ": " + unapprovedText + "\n",
			})
		})
	}
}

// R3: print writes each node into the files, and tree and compare accept the files of a print of
// each current node (v-print-reads-back). The first control prints the statements only, with their
// checks: the interface that a structure statement is about is not in the files, so print writes
// nothing, and tree refuses the files that the run would leave with the same problem. The second
// control changes one word of a statement in the files: compare refuses it, and tree still takes it.
func TestPrintedFilesPassTheComparison(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	approved := fixture(t, "compare", "one-word", "approved.json")
	a := readApprovedFile(t, approved)
	var all, statements []string
	for _, id := range slices.Sorted(maps.Keys(a.Nodes)) {
		if a.Nodes[id].Removed {
			continue
		}
		all = append(all, "--id", id)
		if strings.HasPrefix(a.Nodes[id].Versions[len(a.Nodes[id].Versions)-1].Document, "---\ntype: requirement\n") {
			statements = append(statements, id)
		}
	}
	if len(all) != 2*10 || len(statements) != 3 {
		t.Fatalf("one-word holds %d current nodes and %d statements, want 10 and 3", len(all)/2, len(statements))
	}

	dir := t.TempDir()
	if got := runBinary(t, binary, "", append([]string{"print", "--approved", approved, "--dir", dir}, all...)...); got.code != 0 || got.stderr != "" {
		t.Fatalf("print of each current node: exit %d, stderr %q", got.code, got.stderr)
	}
	expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
		stdout: "spec-check: " + dir + ": 3 statements, 2 checks, 3 components, 1 interface, 1 term; the tree is valid\n",
		stderr: "spec-check: " + dir + noList,
	})
	expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{stdout: passLine(dir, "3 of 3")})

	// The first control: the statements only.
	only := t.TempDir()
	args := []string{"print", "--approved", approved, "--dir", only}
	for _, id := range statements {
		args = append(args, "--id", id)
	}
	missing := "orders-reach-billing-only-through-the-interface: missing-target: about names \"orders-to-billing\", which the files do not hold"
	got := runBinary(t, binary, "", args...)
	if want := "spec-check: print would leave " + filepath.Join(only, "acme-keeps-each-order.yaml") + ":30: " + missing + "\n"; got.code != 2 || !strings.Contains(got.stderr, want) {
		t.Errorf("print of the statements only: exit %d, stderr %q; want exit 2 and %q", got.code, got.stderr, want)
	}
	if texts := readTexts(t, only); len(texts) != 0 {
		t.Fatalf("print wrote %q, want nothing", texts)
	}
	after, _, refusals := planPrint(only, map[string][]byte{}, a, statements, int64(1<<20))
	if len(refusals) == 0 || after == nil {
		t.Fatalf("planPrint of the statements only: refusals %q, after %v; want the refusal and the files", refusals, after)
	}
	if err := writeFiles(only, nil, after); err != nil {
		t.Fatal(err)
	}
	expect(t, runBinary(t, binary, "", "tree", "--dir", only), result{
		code:   1,
		stdout: "spec-check: " + only + ": 3 statements, 2 checks, 0 components, 0 interfaces, 0 terms; the tree is refused\n",
		stderr: "spec-check: " + only + noList + "spec-check: " + filepath.Join(only, "acme-keeps-each-order.yaml") + ":30: " + missing + "\n",
	})

	// The second control: one word more in the text of a statement.
	file := filepath.Join(dir, "acme-keeps-each-order.yaml")
	replaceIn(t, file, "  - ACME keeps each stored order after the order service starts again.\n", "  - ACME keeps each stored paid order after the order service starts again.\n")
	expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{
		code:   1,
		stdout: "spec-check: " + dir + ": the comparison fails on 1 node\n",
		stderr: "spec-check: " + file + ":7: an-order-survives-a-restart: " + unapprovedText + "\n",
	})
	expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
		stdout: "spec-check: " + dir + ": 3 statements, 2 checks, 3 components, 1 interface, 1 term; the tree is valid\n",
		stderr: "spec-check: " + dir + noList,
	})
}

// A check alone, into a directory that holds no statement: tree would refuse the check
// (check-serves), so print writes nothing and names the statement that the check needs. The
// control prints the check with its statement and that statement's parent, and writes them.
func TestPrintRefusesACheckWithoutItsStatement(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	approved := fixture(t, "compare", "one-word", "approved.json")
	dir := t.TempDir()
	expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "a-restart-keeps-each-order", "--dir", dir), result{
		code: 2,
		stderr: "spec-check: print would write check \"a-restart-keeps-each-order\", whose statement \"an-order-survives-a-restart\" " + dir +
			" does not hold: check-serves: a check serves exactly one statement of the files\n",
	})
	if texts := readTexts(t, dir); len(texts) != 0 {
		t.Fatalf("print wrote %q, want nothing", texts)
	}

	got := runBinary(t, binary, "", "print", "--approved", approved, "--id", "a-restart-keeps-each-order", "--id", "acme-keeps-each-order", "--id", "an-order-survives-a-restart", "--dir", dir)
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("the control: exit %d, stderr %q; want exit 0", got.code, got.stderr)
	}
	texts := readTexts(t, dir)
	documents := splitDocuments(texts["acme-keeps-each-order.yaml"])
	var order []string
	for _, d := range documents {
		order = append(order, strings.TrimPrefix(strings.Split(d, "\n")[2], "id: "))
	}
	if len(texts) != 1 || !slices.Equal(order, []string{"acme-keeps-each-order", "an-order-survives-a-restart", "a-restart-keeps-each-order"}) {
		t.Errorf("print wrote %q, want acme-keeps-each-order.yaml with the three documents in that order", texts)
	}
	expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
		stdout: "spec-check: " + dir + ": 2 statements, 1 check, 0 components, 0 interfaces, 0 terms; the tree is valid\n",
		stderr: "spec-check: " + dir + noList,
	})
}

// A node that two files hold has no single home: print writes nothing and names both places, and
// the directory stays byte for byte. The control, with one of the two files taken out, replaces
// the statement where it stands and writes its two checks after it.
func TestPrintRefusesANodeHeldTwoTimes(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	approved := fixture(t, "compare", "two-checks", "approved.json")
	fixed, err := os.ReadFile(fixture(t, "compare", "two-checks", "spec", "orders.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	documents := splitDocuments(string(fixed))
	top, statement := documents[0], documents[1]
	earlier := strings.Replace(statement, "starts again.", "starts again soon.", 1)
	if earlier == statement {
		t.Fatal("the fixture holds no statement text to change")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.yaml"), []byte(top+earlier))
	writeFile(t, filepath.Join(dir, "b.yaml"), []byte(statement))
	before := readTexts(t, dir)
	expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "an-order-survives-a-restart", "--dir", dir), result{
		code: 2,
		stderr: "spec-check: print would write node \"an-order-survives-a-restart\", which " + filepath.Join(dir, "a.yaml") + ":7 and " +
			filepath.Join(dir, "b.yaml") + ":1 both hold\n",
	})
	if got := readTexts(t, dir); !maps.Equal(got, before) {
		t.Errorf("the directory changed: %q, want %q", got, before)
	}

	if err := os.Remove(filepath.Join(dir, "b.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := runBinary(t, binary, "", "print", "--approved", approved, "--id", "an-order-survives-a-restart", "--dir", dir); got.code != 0 {
		t.Fatalf("the control: exit %d, stderr %q; want exit 0", got.code, got.stderr)
	}
	if got, want := readTexts(t, dir)["a.yaml"], strings.Join(documents[:4], ""); got != want {
		t.Errorf("a.yaml is %q, want the statement replaced and its two checks after it, %q", got, want)
	}
}

// The invariant of print: after each run, each file is within the bound, each node stands in
// exactly one file, no file is empty, the judgment of tree finds no problem, each check that the
// run wrote is in the block of its statement, and a second run changes no byte; a refused run
// writes nothing. The test runs the printer over random approved sets: 2,000 seeds, each up to 40
// runs of 1 to 5 nodes on one directory, with a new approved version of a node between runs, and a
// bound of 4,096 bytes. It replays each run with Layout.Place to name the answer that each node
// got, and requires each answer that print can give to occur: W (written at home), M (moved to the
// next file of its series) and T (refused, its block over the bound) for each kind of node, new and
// replaced, and S and P (refused, its statement or its parent not in the files).
func TestPrintKeepsTheInvariant(t *testing.T) {
	t.Parallel()
	const limit = 4096
	var mu sync.Mutex
	seen := map[string]bool{}
	// The seeds 1 to 2,000, spread over one group for each core, so the test takes the time of
	// 2,000 seeds divided by the cores.
	const seeds = 2000
	groups := min(max(runtime.GOMAXPROCS(0), 1), 64)
	t.Run("seeds", func(t *testing.T) {
		for group := range groups {
			t.Run(strconv.Itoa(group), func(t *testing.T) {
				t.Parallel()
				for seed := uint64(group + 1); seed <= seeds; seed += uint64(groups) {
					answers := printSeed(t, seed, limit)
					mu.Lock()
					for answer := range answers {
						seen[answer] = true
					}
					mu.Unlock()
					if t.Failed() {
						return
					}
				}
			})
		}
	})
	var want []string
	for _, kind := range []string{"statement", "check", "component", "term"} {
		for _, age := range []string{"new", "replaced"} {
			for _, answer := range []string{"W", "M", "T"} {
				want = append(want, kind+" "+age+" "+answer)
			}
		}
	}
	want = append(want, "S", "P")
	for _, answer := range want {
		if !seen[answer] {
			t.Errorf("no run gave the answer %q, so the generator reaches no such case", answer)
		}
	}
}

// printSeed runs the printer over the random approved set of seed, and answers each answer that
// a node got.
func printSeed(t *testing.T, seed uint64, limit int64) map[string]bool {
	r := rand.New(rand.NewPCG(seed, seed))
	g := newPrintSet(r)
	dir := t.TempDir()
	answers := map[string]bool{}
	for range 1 + r.IntN(40) {
		g.change(r)
		a := g.approved()
		var ids []string
		for range 1 + r.IntN(5) {
			ids = append(ids, g.ids[r.IntN(len(g.ids))])
		}
		files := readDir(t, dir)
		current, _ := a.Current()
		replay, _ := specgraph.ReadLayout(files)
		for _, node := range printRun(current, ids) {
			answer, ok := placeAnswer(&replay, node, limit)
			answers[answer] = true
			if !ok {
				break
			}
		}
		after, _, refusals := planPrint(dir, files, a, ids, limit)
		if len(refusals) > 0 {
			if got := readDir(t, dir); !maps.EqualFunc(got, files, bytes.Equal) {
				t.Fatalf("seed %d: a refused run changed the files", seed)
			}
			continue
		}
		if err := writeFiles(dir, files, after); err != nil {
			t.Fatal(err)
		}
		holdsTheInvariant(t, seed, dir, limit, printRun(current, ids))
		again, _, refusals := planPrint(dir, readDir(t, dir), a, ids, limit)
		if len(refusals) > 0 || !maps.EqualFunc(again, readDir(t, dir), bytes.Equal) {
			t.Fatalf("seed %d: a second run with the same arguments changes the files: %q", seed, refusals)
		}
	}
	return answers
}

// placeAnswer places node into l and names its answer: "<kind> new|replaced W|M|T", or S or P;
// ok is false for a refusal, after which print places no more nodes.
func placeAnswer(l *specgraph.Layout, node any, limit int64) (string, bool) {
	id := nodeID(node)
	home, replaced := "", false
	for _, name := range slices.Sorted(maps.Keys(l.Files)) {
		for _, d := range l.Files[name] {
			if d.Node != nil && nodeID(d.Node) == id {
				home, replaced = name, true
			}
		}
	}
	statementFile := func(id string) string {
		for _, name := range slices.Sorted(maps.Keys(l.Files)) {
			for _, d := range l.Files[name] {
				if s, ok := d.Node.(specgraph.Statement); ok && s.ID == id {
					return name
				}
			}
		}
		return ""
	}
	kind, age := "term", "new"
	if replaced {
		age = "replaced"
	}
	switch n := node.(type) {
	case specgraph.Statement:
		kind = "statement"
		if !replaced {
			home = n.ID + ".yaml"
			if n.Parent != "" {
				home = statementFile(n.Parent)
			}
		}
	case specgraph.Check:
		kind = "check"
		if !replaced {
			home = statementFile(n.Serves)
		}
	case specgraph.Component:
		kind = "component"
		if !replaced {
			home = "components.yaml"
		}
	default:
		if !replaced {
			home = "terms.yaml"
		}
	}
	file, err := l.Place(node, limit)
	switch {
	case errors.Is(err, specgraph.ErrNoStatement):
		return "S", false
	case errors.Is(err, specgraph.ErrNoParent):
		return "P", false
	case errors.Is(err, specgraph.ErrTooLarge):
		return kind + " " + age + " T", false
	case err != nil:
		return "error " + err.Error(), false
	case file == home:
		return kind + " " + age + " W", true
	default:
		return kind + " " + age + " M", true
	}
}

// holdsTheInvariant fails the test when the files of dir break the invariant of print after a run
// that wrote the nodes of run.
func holdsTheInvariant(t *testing.T, seed uint64, dir string, limit int64, run []any) {
	t.Helper()
	files := readDir(t, dir)
	for name := range files {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > limit || info.Size() == 0 {
			t.Fatalf("seed %d: %s is %d bytes, want 1 to %d", seed, name, info.Size(), limit)
		}
	}
	tree, problems := specgraph.Parse(files)
	if problems = append(problems, specgraph.Validate(tree, specgraph.Options{})...); len(problems) > 0 {
		t.Fatalf("seed %d: tree refuses the files that print wrote: %+v", seed, problems)
	}
	// The nodes of each file in their order, from the line of each.
	type held struct {
		line int
		node any
	}
	byFile := map[string][]held{}
	count := map[string]int{}
	add := func(pos specgraph.Position, node any) {
		byFile[pos.File] = append(byFile[pos.File], held{pos.Line, node})
		count[nodeID(node)]++
	}
	for _, s := range tree.Statements {
		add(s.Pos, s)
	}
	for _, c := range tree.Checks {
		add(c.Pos, c)
	}
	for _, c := range tree.Components {
		add(c.Pos, c)
	}
	for _, term := range tree.Terms {
		add(term.Pos, term)
	}
	for name, nodes := range byFile {
		slices.SortFunc(nodes, func(a, b held) int { return a.line - b.line })
		for i, h := range nodes {
			c, ok := h.node.(specgraph.Check)
			if !ok || !slices.ContainsFunc(run, func(n any) bool { return nodeID(n) == c.ID }) {
				continue
			}
			j := i - 1
			for j >= 0 {
				prev, isCheck := nodes[j].node.(specgraph.Check)
				if !isCheck || prev.Serves != c.Serves {
					break
				}
				j--
			}
			if s, ok := nodes[max(j, 0)].node.(specgraph.Statement); j < 0 || !ok || s.ID != c.Serves {
				t.Fatalf("seed %d: check %s in %s is not in the block of its statement %s", seed, c.ID, name, c.Serves)
			}
		}
	}
	for id, n := range count {
		if n != 1 {
			t.Fatalf("seed %d: node %s stands in %d places", seed, id, n)
		}
	}
}

// printSet is a random approved set: a forest of statements, the checks of each, components with
// parents, an interface, terms, and the versions of each node.
type printSet struct {
	ids      []string
	versions map[string][]any
	text     func(r *rand.Rand) string
	// spec is the approved spec of versions, written again only after a change, since each run
	// reads it and each version prints once.
	spec specgraph.Approved
}

// printWords are the words of the texts of printSet: plain words, so that no rule of the texts
// refuses a node.
var printWords = strings.Fields("a an the order message claim session listener store keeps each reads after starts again before " +
	"customer service billing page shows sends gets only through part holds every that it of to")

func newPrintSet(r *rand.Rand) *printSet {
	g := &printSet{versions: map[string][]any{}, spec: specgraph.Approved{Nodes: map[string]specgraph.ApprovedNode{}}}
	g.text = func(r *rand.Rand) string {
		lines, width := 1+r.IntN(30), 20+r.IntN(100)
		if r.IntN(100) < 2 {
			lines, width = 50, 100
		}
		var out []string
		for range lines {
			var line []string
			for n := 0; n < width-10; {
				w := printWords[r.IntN(len(printWords))]
				line = append(line, w)
				n += len(w) + 1
			}
			out = append(out, strings.Join(line, " "))
		}
		return strings.Join(out, "\n")
	}
	add := func(node any) {
		id := nodeID(node)
		g.ids = append(g.ids, id)
		g.versions[id] = []any{node}
		g.approve(id, node)
	}
	var plain []string
	for i := range r.IntN(5) {
		c := specgraph.Component{ID: fmt.Sprintf("part-%c", 'a'+i), Name: fmt.Sprintf("Part %c", 'a'+i), Purpose: g.text(r)}
		if len(plain) > 0 && r.IntN(2) == 0 {
			c.Parent = plain[r.IntN(len(plain))]
		}
		if len(plain) >= 2 && r.IntN(3) == 0 {
			c.Parent, c.Interface, c.Joins = "", true, []string{plain[0], plain[1]}
		} else {
			plain = append(plain, c.ID)
		}
		add(c)
	}
	for i := range r.IntN(4) {
		add(specgraph.Term{ID: fmt.Sprintf("word-%c", 'a'+i), Name: fmt.Sprintf("word %c", 'a'+i), Definition: g.text(r)})
	}
	statements := 1 + r.IntN(12)
	parents := make([]string, statements)
	children := map[string]bool{}
	for i := range statements {
		id := fmt.Sprintf("claim-%c", 'a'+i)
		if i > 0 && r.IntN(3) > 0 {
			parents[i] = fmt.Sprintf("claim-%c", 'a'+r.IntN(i))
			children[parents[i]] = true
		}
		add(specgraph.Statement{ID: id, Kind: specgraph.KindBehavior, Parent: parents[i], Text: g.text(r)})
	}
	for i := range statements {
		statement := fmt.Sprintf("claim-%c", 'a'+i)
		checks := r.IntN(4)
		if !children[statement] && checks == 0 {
			checks = 1
		}
		for k := range checks {
			add(specgraph.Check{
				ID: fmt.Sprintf("test-%c-%c", 'a'+i, 'a'+k), Serves: statement, Subject: specgraph.SubjectCode, Procedure: specgraph.ProcedureAgent,
				Claim: "an agent reads the store", ProcedureText: g.text(r), FailingExample: "a store with no order",
			})
		}
	}
	return g
}

// change gives each node a new version with probability 15 in 100: a new text.
func (g *printSet) change(r *rand.Rand) {
	for _, id := range g.ids {
		if r.IntN(100) >= 15 {
			continue
		}
		versions := g.versions[id]
		switch n := versions[len(versions)-1].(type) {
		case specgraph.Statement:
			n.Text = g.text(r)
			g.versions[id] = append(versions, n)
			g.approve(id, n)
		case specgraph.Check:
			n.ProcedureText = g.text(r)
			g.versions[id] = append(versions, n)
			g.approve(id, n)
		case specgraph.Component:
			n.Purpose = g.text(r)
			g.versions[id] = append(versions, n)
			g.approve(id, n)
		case specgraph.Term:
			n.Definition = g.text(r)
			g.versions[id] = append(versions, n)
			g.approve(id, n)
		}
	}
}

// approve adds node, the next version of id, to the approved spec of g, its document as Print
// writes it.
func (g *printSet) approve(id string, node any) {
	document, err := node.(interface{ Print() (string, error) }).Print()
	if err != nil {
		panic(err)
	}
	n := g.spec.Nodes[id]
	n.ID = id
	at := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(n.Versions)) * time.Hour)
	n.Versions = append(slices.Clone(n.Versions), specgraph.ApprovedVersion{Version: len(n.Versions) + 1, At: at, Document: document})
	nodes := maps.Clone(g.spec.Nodes)
	nodes[id] = n
	g.spec = specgraph.Approved{Nodes: nodes}
}

// approved is the approved spec of g: each version of each node.
func (g *printSet) approved() specgraph.Approved {
	return g.spec
}

// readApprovedFile reads the approved spec at path.
func readApprovedFile(t *testing.T, path string) specgraph.Approved {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a, err := specgraph.ReadApproved(f)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// readTexts is the text of each file directly in dir, by name.
func readTexts(t *testing.T, dir string) map[string]string {
	t.Helper()
	texts := map[string]string{}
	for name, content := range readDir(t, dir) {
		texts[name] = string(content)
	}
	return texts
}

// splitDocuments is each document of text, a spec file: the text from each line that is exactly
// "---" to the next.
func splitDocuments(text string) []string {
	var documents []string
	for line := range strings.Lines(text) {
		if line == "---\n" || len(documents) == 0 {
			documents = append(documents, "")
		}
		documents[len(documents)-1] += line
	}
	return documents
}

// printed is the spec file of nodes: the document that Print writes for each, in order.
func printed(t *testing.T, nodes ...interface{ Print() (string, error) }) []byte {
	t.Helper()
	var b strings.Builder
	for _, node := range nodes {
		document, err := node.Print()
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(document)
	}
	return []byte(b.String())
}
