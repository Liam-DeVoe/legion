package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// noList is the line tree writes after the directory when it reads no --retired list: the
// instrument says what it did not read.
const noList = ": no --retired list, so no id is checked against the ids the approved spec removed\n"

// plantedFaults is each fixture tree of specgraph's TestEachPlantedFaultIsNamed with the one
// problem the command must name, and the counts of the blocks it holds. The counts come from the
// opening lines of each fixture, not from the command. The retired-id tree is its own row of
// TestTheBuiltBinary, since it needs --retired.
var plantedFaults = []struct {
	dir, file    string
	line         int
	node, detail string
	counts       string
}{
	{"about-a-missing-component", "spec.md", 1, "a-claim-survives-a-restart", `missing-target: about names "legion", which the files do not hold`, "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"bad-id", "spec.md", 1, "step-3", `bad-id: "step-3" is lower-case words joined by hyphens, with no word that states a position`, "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"check-serves-two-statements", "spec.md", 21, "each-restart-keeps-the-claim", "check-serves: a check serves exactly one statement of the files", "2 statements, 3 checks, 0 components, 0 interfaces, 0 terms"},
	{"check-subject-not-allowed", "spec.md", 5, "a-restart-keeps-the-claim", `bad-subject: "the logs" is not code, production or graph`, "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"check-without-failing-example", "spec.md", 5, "a-restart-keeps-the-claim", "no-failing-example: the check has no failing example", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"claim-names-a-path", "spec.md", 5, "a-restart-keeps-the-claim", "code-in-text: claim names code: cmd/listener/main.go", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"component-names-a-package", "spec.md", 1, "dispatch", "code-in-text: purpose names code: internal/dispatch/pmdoc", "0 statements, 0 checks, 1 component, 0 interfaces, 0 terms"},
	{"component-no-purpose", "spec.md", 1, "dispatch", "bad-component: a component has a name and one sentence of purpose", "0 statements, 0 checks, 1 component, 0 interfaces, 0 terms"},
	{"doubled-id", "b.md", 1, "a-claim-survives-a-restart", "doubled-id: also at a.md:1", "2 statements, 2 checks, 0 components, 0 interfaces, 0 terms"},
	{"go-selector-two-words-missing", "spec.md", 5, "a-restart-keeps-the-claim", `bad-selector: a go selector is "<import path> <TopLevelTestName>"`, "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"interface-joins-a-missing-component", "spec.md", 5, "legion-to-dispatch", `bad-interface: joins "legion", which is no component`, "0 statements, 0 checks, 1 component, 1 interface, 0 terms"},
	{"interface-joins-one", "spec.md", 9, "legion-to-dispatch", "bad-interface: an interface joins two or more components", "0 statements, 0 checks, 2 components, 1 interface, 0 terms"},
	{"leaf-without-check", "spec.md", 1, "a-claim-survives-a-restart", "leaf-without-check: no statement is under it and no check serves it", "1 statement, 0 checks, 0 components, 0 interfaces, 0 terms"},
	{"missing-parent", "spec.md", 1, "a-claim-survives-a-restart", `no-parent: the files hold no statement "legion-keeps-each-claim"`, "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"parent-and-serves-loop", "spec.md", 1, "a-claim-survives-a-restart", "loop: a-claim-survives-a-restart -[serves]-> legion-keeps-each-claim -[parent]-> a-claim-survives-a-restart", "2 statements, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"parent-loop", "spec.md", 1, "a-claim-survives-a-restart", "loop: a-claim-survives-a-restart -[parent]-> legion-keeps-each-claim -[parent]-> a-claim-survives-a-restart", "2 statements, 0 checks, 0 components, 0 interfaces, 0 terms"},
	{"program-check-without-job", "spec.md", 5, "a-restart-keeps-the-claim", "no-job: a program check names the job that runs its test", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"program-check-without-test-part", "spec.md", 5, "a-restart-keeps-the-claim", "no-test: a program check names its runner and its selector", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"program-procedure-names-a-test", "spec.md", 5, "a-restart-keeps-the-claim", "code-in-text: procedure names code: TestARestart_LosesNoClaim", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"query-check-with-test-part", "spec.md", 5, "each-claim-is-in-the-graph", "test-on-non-program: a query check names no runner, selector or job", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"statement-names-a-package", "spec.md", 1, "a-document-keeps-its-blocks", "code-in-text: text names code: internal/dispatch/pmdoc", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"statement-without-kind", "spec.md", 1, "a-claim-survives-a-restart", `bad-kind: "" is not behavior, structure or property`, "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
	{"structure-statement-without-about", "spec.md", 1, "legion-reads-issues-only-through-dispatch", "no-about: a statement of kind structure names its components or interfaces", "1 statement, 1 check, 0 components, 0 interfaces, 0 terms"},
}

// TestTheBuiltBinary builds the command once and runs it as a process over specgraph's fixture
// trees, each row asserting the exit status and both streams byte for byte.
func TestTheBuiltBinary(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	valid := fixture(t, "trees", "valid")
	validLine := func(dir string) string {
		return "spec-check: " + dir + ": 3 statements, 4 checks, 2 components, 1 interface, 1 term; the tree is valid\n"
	}

	t.Run("tree names each planted fault with its file, line, node and rule", func(t *testing.T) {
		t.Parallel()
		entries, err := os.ReadDir(fixture(t, "trees"))
		if err != nil {
			t.Fatal(err)
		}
		covered := map[string]bool{"valid": true, "program-check-test-part": true, "retired-id": true}
		for _, row := range plantedFaults {
			covered[row.dir] = true
			t.Run(row.dir, func(t *testing.T) {
				t.Parallel()
				dir := fixture(t, "trees", row.dir)
				expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
					code:   1,
					stdout: "spec-check: " + dir + ": " + row.counts + "; the tree is refused\n",
					stderr: "spec-check: " + dir + noList + "spec-check: " + filepath.Join(dir, row.file) + ":" + strconv.Itoa(row.line) + ": " + row.node + ": " + row.detail + "\n",
				})
			})
		}
		for _, entry := range entries {
			if entry.IsDir() && !covered[entry.Name()] {
				t.Errorf("testdata/trees/%s is in no row", entry.Name())
			}
		}
	})
	t.Run("tree over the valid tree exits 0", func(t *testing.T) {
		t.Parallel()
		expect(t, runBinary(t, binary, "", "tree", "--dir", valid), result{stdout: validLine(valid), stderr: "spec-check: " + valid + noList})
	})
	t.Run("tree over a program check with its test part exits 0", func(t *testing.T) {
		t.Parallel()
		dir := fixture(t, "trees", "program-check-test-part")
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
			stdout: "spec-check: " + dir + ": 1 statement, 1 check, 0 components, 0 interfaces, 0 terms; the tree is valid\n",
			stderr: "spec-check: " + dir + noList,
		})
	})
	t.Run("tree over a CRLF copy of the valid tree prints the same count line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, name := range []string{"claims.md", "interfaces.md"} {
			content, err := os.ReadFile(filepath.Join(valid, name))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, name), []byte(strings.ReplaceAll(string(content), "\n", "\r\n")))
		}
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{stdout: validLine(dir), stderr: "spec-check: " + dir + noList})
	})
	t.Run("tree refuses a file that is not UTF-8 or holds a NUL byte, naming the file and the line", func(t *testing.T) {
		t.Parallel()
		for name, content := range map[string]string{
			"Latin-1": ":::term{#cafe-term name=\"cafe\"}\nA caf\xe9 is a place where a session waits.\n:::\n",
			"NUL":     ":::term{#nul-term name=\"nul term\"}\nA nul\x00term is a word.\n:::\n",
		} {
			dir := copyOf(t, valid)
			writeFile(t, filepath.Join(dir, "bytes.md"), []byte(content))
			got := runBinary(t, binary, "", "tree", "--dir", dir)
			if want := (result{
				code:   1,
				stdout: "spec-check: " + dir + ": 3 statements, 4 checks, 2 components, 1 interface, 2 terms; the tree is refused\n",
				stderr: "spec-check: " + dir + noList + "spec-check: " + filepath.Join(dir, "bytes.md") + ":2: -: bad-encoding: a spec file is UTF-8 text with no NUL byte\n",
			}); got != want {
				t.Errorf("%s: exit %d, stdout %q, stderr %q\nwant exit %d, stdout %q, stderr %q", name, got.code, got.stdout, got.stderr, want.code, want.stdout, want.stderr)
			}
		}
	})
	t.Run("tree with --retired names a retired id", func(t *testing.T) {
		t.Parallel()
		dir := fixture(t, "trees", "retired-id")
		retired := filepath.Join(t.TempDir(), "retired.txt")
		writeFile(t, retired, []byte("# The ids the approved spec removed.\n\n  gone-statement  \nanother-gone-statement\n"))
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir, "--retired", retired), result{
			code:   1,
			stdout: "spec-check: " + dir + ": 1 statement, 1 check, 0 components, 0 interfaces, 0 terms; the tree is refused\n",
			stderr: "spec-check: " + filepath.Join(dir, "spec.md") + ":1: gone-statement: retired-id: the approved spec removed this id\n",
		})
	})
	t.Run("tree without --retired says so and checks no id", func(t *testing.T) {
		t.Parallel()
		dir := fixture(t, "trees", "retired-id")
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
			stdout: "spec-check: " + dir + ": 1 statement, 1 check, 0 components, 0 interfaces, 0 terms; the tree is valid\n",
			stderr: "spec-check: " + dir + noList,
		})
	})
	t.Run("tree with a --retired file it cannot read exits 2", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "retired.txt")
		got := runBinary(t, binary, "", "tree", "--dir", valid, "--retired", missing)
		want := "spec-check: " + missing + ": cannot be read: open " + missing + ": no such file or directory\n"
		expect(t, got, result{code: 2, stderr: want})
	})
	t.Run("tree over an empty directory exits 2 without --allow-empty", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{code: 2, stderr: "spec-check: " + dir + ": holds no spec file\n"})
	})
	t.Run("tree over an empty directory with --allow-empty is the empty tree", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		expect(t, runBinary(t, binary, "", "tree", "--allow-empty", "--dir", dir), result{
			stdout: "spec-check: " + dir + ": 0 statements, 0 checks, 0 components, 0 interfaces, 0 terms; the tree is empty\n",
			stderr: "spec-check: " + dir + noList,
		})
	})
	t.Run("tree over a missing directory exits 2 without --allow-empty", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "missing")
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{code: 2, stderr: "spec-check: " + dir + ": does not exist\n"})
	})
	t.Run("tree over a missing directory with --allow-empty is the empty tree", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "missing")
		expect(t, runBinary(t, binary, "", "tree", "--allow-empty", "--dir", dir), result{
			stdout: "spec-check: " + dir + ": 0 statements, 0 checks, 0 components, 0 interfaces, 0 terms; the tree is empty\n",
			stderr: "spec-check: " + dir + noList,
		})
	})
	t.Run("tree warns of each entry that is not a spec file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, name := range []string{"claims.md", "interfaces.md"} {
			content, err := os.ReadFile(filepath.Join(valid, name))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, name), content)
		}
		writeFile(t, filepath.Join(dir, "notes.txt"), []byte("Not a spec file.\n"))
		if err := os.Mkdir(filepath.Join(dir, "nested.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
			stdout: validLine(dir),
			stderr: "spec-check: warning: " + filepath.Join(dir, "nested.md") + ": not a spec file (directory); the importer passes over it\n" +
				"spec-check: warning: " + filepath.Join(dir, "notes.txt") + ": not a spec file (file); the importer passes over it\n" +
				"spec-check: " + dir + noList,
		})
	})
	t.Run("tree names a relative directory by its absolute path", func(t *testing.T) {
		t.Parallel()
		parent := filepath.Dir(valid)
		expect(t, runBinary(t, binary, parent, "tree", "--dir", "valid"), result{stdout: validLine(valid), stderr: "spec-check: " + valid + noList})
	})
	t.Run("tree refuses a parse problem and a rule problem together, in file and line order", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "a.md"), []byte(":::requirement{#a-claim-survives-a-restart kind=\"behavior\"}\nLegion keeps each claim.\n:::\nA line outside a block.\n"))
		expect(t, runBinary(t, binary, "", "tree", "--dir", dir), result{
			code:   1,
			stdout: "spec-check: " + dir + ": 1 statement, 0 checks, 0 components, 0 interfaces, 0 terms; the tree is refused\n",
			stderr: "spec-check: " + dir + noList +
				"spec-check: " + filepath.Join(dir, "a.md") + ":1: a-claim-survives-a-restart: leaf-without-check: no statement is under it and no check serves it\n" +
				"spec-check: " + filepath.Join(dir, "a.md") + ":4: -: text-outside-block: a spec file holds only blocks, headings and blank lines\n",
		})
	})

	t.Run("changes lists no statement for a move to another file", func(t *testing.T) {
		t.Parallel()
		expect(t, runBinary(t, binary, "", "changes", "--base", fixture(t, "changes", "move", "base"), "--dir", fixture(t, "changes", "move", "head")),
			result{stdout: "spec-check: no statement changed\n"})
	})
	t.Run("changes lists the statement whose text gained one word", func(t *testing.T) {
		t.Parallel()
		expect(t, runBinary(t, binary, "", "changes", "--base", fixture(t, "changes", "one-word", "base"), "--dir", fixture(t, "changes", "one-word", "head")),
			result{stdout: "spec-check: a-claim-survives-a-restart: text\n"})
	})
	t.Run("changes lists each part that changed", func(t *testing.T) {
		t.Parallel()
		expect(t, runBinary(t, binary, "", "changes", "--base", fixture(t, "changes", "relations", "base"), "--dir", fixture(t, "changes", "relations", "head")),
			result{stdout: "spec-check: a-claim-survives-a-restart: relations\nspec-check: a-lost-claim-is-reported: text, parent\n"})
	})
	// Git keeps no empty directory, so a version with no spec file has no .dispatch/spec, and the
	// first change that adds a spec file, or the change that removes the last one, compares a tree
	// with a directory that does not exist.
	t.Run("changes reads a base that does not exist as the empty tree", func(t *testing.T) {
		t.Parallel()
		base := filepath.Join(t.TempDir(), "missing")
		expect(t, runBinary(t, binary, "", "changes", "--base", base, "--dir", valid), result{
			stdout: "spec-check: a-claim-survives-a-restart: added\nspec-check: legion-keeps-each-claim-in-dispatch: added\nspec-check: legion-reads-issues-only-through-dispatch: added\n",
			stderr: "spec-check: " + base + ": does not exist, so it is read as the empty tree\n",
		})
	})
	t.Run("changes reads a directory that does not exist as the empty tree", func(t *testing.T) {
		t.Parallel()
		head := filepath.Join(t.TempDir(), "missing")
		expect(t, runBinary(t, binary, "", "changes", "--base", valid, "--dir", head), result{
			stdout: "spec-check: a-claim-survives-a-restart: removed\nspec-check: legion-keeps-each-claim-in-dispatch: removed\nspec-check: legion-reads-issues-only-through-dispatch: removed\n",
			stderr: "spec-check: " + head + ": does not exist, so it is read as the empty tree\n",
		})
	})
	t.Run("changes reads an empty base directory as the empty tree", func(t *testing.T) {
		t.Parallel()
		expect(t, runBinary(t, binary, "", "changes", "--base", t.TempDir(), "--dir", valid), result{
			stdout: "spec-check: a-claim-survives-a-restart: added\nspec-check: legion-keeps-each-claim-in-dispatch: added\nspec-check: legion-reads-issues-only-through-dispatch: added\n",
		})
	})
	t.Run("changes exits 2 when a tree has a problem", func(t *testing.T) {
		t.Parallel()
		base := fixture(t, "trees", "leaf-without-check")
		expect(t, runBinary(t, binary, "", "changes", "--base", base, "--dir", valid), result{
			code:   2,
			stderr: "spec-check: " + filepath.Join(base, "spec.md") + ":1: a-claim-survives-a-restart: leaf-without-check: no statement is under it and no check serves it\n",
		})
	})

	t.Run("checks prints the program checks of a job", func(t *testing.T) {
		t.Parallel()
		tree, problems := specgraph.Parse(readDir(t, valid))
		if len(problems) != 0 {
			t.Fatal(problems)
		}
		atTheLimit, _ := tree.CheckByID("a-claim-at-the-time-limit-stays")
		restart, _ := tree.CheckByID("a-restart-keeps-the-claim")
		want := `[{"id":"a-claim-at-the-time-limit-stays","runner":"go","selector":"example.com/acme/daemon TestAClaimAtTheTimeLimitStays","job":"go-checks","procedure_sha256":"` + atTheLimit.Digest() + `"},` +
			`{"id":"a-restart-keeps-the-claim","runner":"go","selector":"example.com/acme/daemon TestARestartKeepsTheClaim","job":"go-checks","procedure_sha256":"` + restart.Digest() + `"}]` + "\n"
		expect(t, runBinary(t, binary, "", "checks", "--dir", valid, "--job", "go-checks"), result{stdout: want})
	})
	t.Run("checks prints [] for a job that no check names", func(t *testing.T) {
		t.Parallel()
		expect(t, runBinary(t, binary, "", "checks", "--dir", valid, "--job", "other"), result{stdout: "[]\n"})
	})
	t.Run("checks reads a directory that does not exist as the empty tree", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "missing")
		expect(t, runBinary(t, binary, "", "checks", "--dir", dir, "--job", "go-checks"), result{
			stdout: "[]\n",
			stderr: "spec-check: " + dir + ": does not exist, so it is read as the empty tree\n",
		})
	})
	t.Run("checks exits 2 when the tree has a problem", func(t *testing.T) {
		t.Parallel()
		dir := fixture(t, "trees", "program-check-without-job")
		expect(t, runBinary(t, binary, "", "checks", "--dir", dir, "--job", "go-checks"), result{
			code:   2,
			stderr: "spec-check: " + filepath.Join(dir, "spec.md") + ":5: a-restart-keeps-the-claim: no-job: a program check names the job that runs its test\n",
		})
	})
	t.Run("checks writes no workflow command a selector holds", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "spec.md"), []byte(":::requirement{#a-claim-survives-a-restart kind=\"behavior\"}\nLegion keeps the claim of an issue after the daemon starts again.\n:::\n\n"+
			":::check{#a-restart-keeps-the-claim serves=\"a-claim-survives-a-restart\" subject=\"code\" procedure=\"program\" runner=\"bun\" selector=\"daemon.test.ts ##[error]forged\" job=\"bun-checks\"}\n"+
			"Claim: The daemon reads the claim of an issue after it starts again.\nProcedure: The program starts the daemon again and reads the claims.\nFailing example: A daemon that keeps its claims in memory only.\n:::\n"))
		got := runBinary(t, binary, "", "checks", "--dir", dir, "--job", "bun-checks")
		if got.code != 0 || got.stderr != "" || strings.Contains(got.stdout, "##[") {
			t.Fatalf("exit %d, stdout %q, stderr %q: want exit 0 and no \"##[\"", got.code, got.stdout, got.stderr)
		}
		var entries []struct{ ID, Selector string }
		if err := json.Unmarshal([]byte(got.stdout), &entries); err != nil || len(entries) != 1 || entries[0].Selector != "daemon.test.ts ##[error]forged" {
			t.Errorf("stdout %q reads as %+v, %v; want the selector as written", got.stdout, entries, err)
		}
	})

	t.Run("compare", func(t *testing.T) {
		t.Parallel()
		testCompare(t, binary)
	})
	t.Run("print", func(t *testing.T) {
		t.Parallel()
		testPrint(t, binary)
	})
	t.Run("guard", func(t *testing.T) {
		t.Parallel()
		testGuard(t, binary)
	})

	t.Run("a bad argument exits 2 with the line that names it and the usage", func(t *testing.T) {
		t.Parallel()
		approved := fixture(t, "compare", "approved-text", "approved.json")
		for _, row := range []struct {
			args []string
			line string
		}{
			{[]string{"tree"}, "tree: --dir is required"},
			{[]string{"tree", "--dir", valid, "extra"}, `tree: takes no argument but its flags, got "extra"`},
			{[]string{"tree", "--bogus"}, "tree: flag provided but not defined: -bogus"},
			{[]string{"changes", "--dir", valid}, "changes: --base is required"},
			{[]string{"checks", "--dir", valid}, "checks: --job is required"},
			{[]string{"compare", "--dir", valid}, "compare: --approved is required"},
			{[]string{"compare", "--approved", approved}, "compare: --dir is required"},
			{[]string{"print", "--approved", approved}, "print: --id is required"},
			{[]string{"print", "--id", "order"}, "print: --approved is required"},
			{[]string{"guard"}, "guard: --root is required"},
			{[]string{"guard", "--root", valid, "extra"}, `guard: takes no argument but its flags, got "extra"`},
		} {
			expect(t, runBinary(t, binary, "", row.args...), result{code: 2, stderr: "spec-check: " + row.line + "\n" + usage + "\n"})
		}
	})
}

// The Detail of each rule of specgraph.Compare, as the command prints it.
const (
	unapprovedID   = "unapproved-id: the approved spec never held this id"
	unapprovedText = "unapproved-text: the approved spec never held this text"
	removedID      = "removed-id: the approved spec removed this id and never held this text for it"
)

// testCompare runs compare over each fixture of specgraph's comparison tests (testdata/compare),
// each the approved spec and the files of one change: a node in a text that the approved spec
// never held fails the comparison, and the command names it with its file and line; the files
// that hold only approved texts pass, and the command names each approved statement that the
// files do not hold at its current text.
func testCompare(t *testing.T, binary string) {
	covered := map[string]bool{}
	for _, row := range []struct {
		fixture, file string
		line          int
		node, detail  string
	}{
		{"one-word", "orders.md", 7, "an-order-survives-a-restart", unapprovedText},
		{"unapproved-statement", "orders.md", 29, "each-order-gets-one-bill", unapprovedID},
		{"check-text", "orders.md", 11, "a-restart-keeps-each-order", unapprovedText},
		{"component-purpose", "components.md", 7, "orders", unapprovedText},
		{"interface-joins", "components.md", 11, "orders-to-billing", unapprovedText},
		{"term-definition", "components.md", 19, "order", unapprovedText},
		{"removed-id-reused", "orders.md", 29, "an-order-is-paid-once", removedID},
		{"one-of-four", "orders.md", 29, "each-order-gets-one-bill", unapprovedText},
	} {
		covered[row.fixture] = true
		t.Run(row.fixture+" fails on one node", func(t *testing.T) {
			t.Parallel()
			dir, approved := fixture(t, "compare", row.fixture, "spec"), fixture(t, "compare", row.fixture, "approved.json")
			expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{
				code:   1,
				stdout: "spec-check: " + dir + ": the comparison fails on 1 node\n",
				stderr: "spec-check: " + filepath.Join(dir, row.file) + ":" + strconv.Itoa(row.line) + ": " + row.node + ": " + row.detail + "\n",
			})
		})
	}
	for _, row := range []struct {
		fixture, held string
		others        []string
	}{
		{"approved-text", "3 of 3", nil},
		{"two-checks", "3 of 3", nil},
		// R28: the files hold a statement and its check that the approved spec removed, in a text
		// that it held. The comparison passes (R2) and names each of them.
		{"removed-kept", "3 of 3", []string{
			"statement an-order-is-paid-once: the files hold it, and the approved spec removed it",
			"check a-second-payment-is-refused: the files hold it, and the approved spec removed it",
		}},
		{"earlier-text", "2 of 3", []string{"statement an-order-survives-a-restart: the files hold approved version 1, and version 2 is current"}},
		{"not-in-files", "2 of 3", []string{"statement orders-reach-billing-only-through-the-interface: not in the files"}},
	} {
		covered[row.fixture] = true
		t.Run(row.fixture+" passes", func(t *testing.T) {
			t.Parallel()
			dir, approved := fixture(t, "compare", row.fixture, "spec"), fixture(t, "compare", row.fixture, "approved.json")
			want := passLine(dir, row.held)
			for _, line := range row.others {
				want += "spec-check: " + line + "\n"
			}
			expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{stdout: want})
		})
	}
	entries, err := os.ReadDir(fixture(t, "compare"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !covered[entry.Name()] {
			t.Errorf("testdata/compare/%s is in no row", entry.Name())
		}
	}

	t.Run("compare names a removed component, interface and term that the files hold", func(t *testing.T) {
		t.Parallel()
		content, err := os.ReadFile(fixture(t, "compare", "approved-text", "approved.json"))
		if err != nil {
			t.Fatal(err)
		}
		var file struct {
			Nodes []map[string]any `json:"nodes"`
		}
		if err := json.Unmarshal(content, &file); err != nil {
			t.Fatal(err)
		}
		removed := 0
		for _, n := range file.Nodes {
			if id := n["id"]; id == "shipping" || id == "orders-to-billing" || id == "order" {
				n["removed"] = true
				removed++
			}
		}
		if removed != 3 {
			t.Fatalf("the fixture holds %d of the three nodes, want 3", removed)
		}
		changed, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		approved := filepath.Join(t.TempDir(), "approved.json")
		writeFile(t, approved, changed)
		dir := fixture(t, "compare", "approved-text", "spec")
		expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{stdout: passLine(dir, "3 of 3") +
			"spec-check: interface orders-to-billing: the files hold it, and the approved spec removed it\n" +
			"spec-check: component shipping: the files hold it, and the approved spec removed it\n" +
			"spec-check: term order: the files hold it, and the approved spec removed it\n"})
	})

	t.Run("two nodes in texts never approved are two lines, in file and line order", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, name := range []string{"orders.md", "components.md"} {
			content, err := os.ReadFile(fixture(t, "compare", "approved-text", "spec", name))
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Replace(string(content), "It keeps the orders that customers send.", "It keeps the orders that customers send and cancel.", 1)
			writeFile(t, filepath.Join(dir, name), []byte(strings.Replace(text, "order service starts again.", "order service starts again fast.", 1)))
		}
		expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", fixture(t, "compare", "approved-text", "approved.json")), result{
			code:   1,
			stdout: "spec-check: " + dir + ": the comparison fails on 2 nodes\n",
			stderr: "spec-check: " + filepath.Join(dir, "components.md") + ":7: orders: " + unapprovedText + "\n" +
				"spec-check: " + filepath.Join(dir, "orders.md") + ":7: an-order-survives-a-restart: " + unapprovedText + "\n",
		})
	})
	t.Run("compare makes no comparison of files that do not parse", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "a.md"), []byte("A line outside a block.\n"))
		expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", fixture(t, "compare", "approved-text", "approved.json")), result{
			code: 2,
			stderr: "spec-check: " + filepath.Join(dir, "a.md") + ":1: -: text-outside-block: a spec file holds only blocks, headings and blank lines\n" +
				"spec-check: " + dir + ": the files do not parse, so no comparison was made\n",
		})
	})
	t.Run("compare refuses a directory that holds no spec file", func(t *testing.T) {
		t.Parallel()
		empty, missing := t.TempDir(), filepath.Join(t.TempDir(), "missing")
		approved := fixture(t, "compare", "approved-text", "approved.json")
		expect(t, runBinary(t, binary, "", "compare", "--dir", empty, "--approved", approved), result{code: 2, stderr: "spec-check: " + empty + ": holds no spec file\n"})
		expect(t, runBinary(t, binary, "", "compare", "--dir", missing, "--approved", approved), result{code: 2, stderr: "spec-check: " + missing + ": does not exist\n"})
	})
	t.Run("compare refuses an approved spec it cannot read", func(t *testing.T) {
		t.Parallel()
		dir := fixture(t, "compare", "approved-text", "spec")
		missing := filepath.Join(t.TempDir(), "approved.json")
		expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", missing), result{
			code:   2,
			stderr: "spec-check: " + missing + ": cannot be read: open " + missing + ": no such file or directory\n",
		})
		bad := filepath.Join(t.TempDir(), "approved.json")
		writeFile(t, bad, []byte(`{"nodes":[{"id":"order","versions":[]}]}`))
		expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", bad), result{
			code:   2,
			stderr: "spec-check: " + bad + `: specgraph: approved node "order": it has no version` + "\n",
		})
	})
	t.Run("compare does not run in a CI job", func(t *testing.T) {
		t.Parallel()
		dir, approved := fixture(t, "compare", "approved-text", "spec"), fixture(t, "compare", "approved-text", "approved.json")
		for _, variable := range []string{"GITHUB_ACTIONS", "CI"} {
			expect(t, runBinaryIn(t, binary, "", []string{variable + "=true"}, "compare", "--dir", dir, "--approved", approved), result{
				code:   2,
				stderr: "spec-check: compare does not run in a CI job (" + variable + "=true): Dispatch compares the files with the approved spec at each push (R2)\n",
			})
		}
		// Only "true" names a CI job: the comparison runs where the variable holds another value.
		expect(t, runBinaryIn(t, binary, "", []string{"CI=false"}, "compare", "--dir", dir, "--approved", approved), result{
			stdout: passLine(dir, "3 of 3"),
		})
	})
	// The second acceptance run's D3, held as the code is: the refusal reads the command's own
	// environment, so a step that starts the command behind a boundary that drops the runner's
	// environment compares. sudo -n with its default env_reset and a container that docker run
	// starts without -e both give the command neither variable. This row holds that fact with env
	// -i, the same boundary, since a test cannot count on docker, an image or sudo: the step's own
	// environment holds both variables, and env -i starts the command with none. A change that
	// makes the command refuse behind env -i changes this row; a refusal keyed to sudo or to a
	// container alone would not, so the row pins env -i and not those two.
	t.Run("compare compares behind a boundary that drops the environment", func(t *testing.T) {
		t.Parallel()
		envPath, err := exec.LookPath("env")
		if err != nil {
			t.Fatalf("no env on PATH: %v", err)
		}
		dir, approved := fixture(t, "compare", "approved-text", "spec"), fixture(t, "compare", "approved-text", "approved.json")
		expect(t, runBinaryIn(t, envPath, "", []string{"GITHUB_ACTIONS=true", "CI=true"}, "-i", binary, "compare", "--dir", dir, "--approved", approved), result{
			stdout: passLine(dir, "3 of 3"),
		})
		// The same step without the boundary is refused, so the pass above is the boundary's.
		got := runBinaryIn(t, envPath, "", []string{"GITHUB_ACTIONS=true", "CI=true"}, binary, "compare", "--dir", dir, "--approved", approved)
		if got.code != 2 || !strings.Contains(got.stderr, "compare does not run in a CI job") {
			t.Errorf("compare in the step without env -i: exit %d, stderr %q; want the refusal", got.code, got.stderr)
		}
	})
	t.Run("every other subcommand runs in a CI job", func(t *testing.T) {
		t.Parallel()
		ci := []string{"GITHUB_ACTIONS=true", "CI=true"}
		approved := fixture(t, "compare", "approved-text", "approved.json")
		if got := runBinaryIn(t, binary, "", ci, "print", "--approved", approved, "--id", "order"); got.code != 0 || got.stderr != "" {
			t.Errorf("print in a CI job: exit %d, stderr %q; want exit 0", got.code, got.stderr)
		}
		clean := fixture(t, "ci-files", "clean")
		expect(t, runBinaryIn(t, binary, "", ci, "guard", "--root", clean), result{stdout: "spec-check: " + clean + ": no line of 4 CI files runs the comparison\n"})
		valid := fixture(t, "trees", "valid")
		if got := runBinaryIn(t, binary, "", ci, "tree", "--dir", valid); got.code != 0 {
			t.Errorf("tree in a CI job: exit %d, stderr %q; want exit 0", got.code, got.stderr)
		}
	})
}

// testPrint runs print over the fixtures of testdata/compare: it writes the current approved block
// of each node it is named, and of each check of a statement it is named, byte for byte as the
// files hold them. A copy of what it writes passes the comparison, and the same copy with one word
// more fails it on that statement (v-two-specs: "A change copies the text that Dispatch prints for
// an approved statement and its checks, and it passes the comparison. The same text with one word
// changed fails it").
func testPrint(t *testing.T, binary string) {
	t.Run("print writes a statement and its two checks as the files hold them", func(t *testing.T) {
		t.Parallel()
		approved := fixture(t, "compare", "two-checks", "approved.json")
		want := `:::requirement{#an-order-survives-a-restart kind="behavior" parent="acme-keeps-each-order"}
ACME keeps each stored order after the order service starts again.
:::

:::check{#a-crash-keeps-each-order serves="an-order-survives-a-restart" subject="code" procedure="program" runner="go" selector="example.com/acme/orders TestACrashKeepsEachOrder" job="go-checks"}
Claim: The order service reads each stored order after a crash stops it and it starts again.
Procedure: The program stores one order, stops the order service with a kill signal, starts it again and reads the orders.
It must find the order one time.
Failing example: An order service that writes its orders to disk only when it stops normally.
:::

:::check{#a-restart-keeps-each-order serves="an-order-survives-a-restart" subject="code" procedure="program" runner="go" selector="example.com/acme/orders TestARestartKeepsEachOrder" job="go-checks"}
Claim: The order service reads each stored order after it starts again.
Procedure: The program stores one order, starts the order service again and reads the orders.
It must find the order one time.
Failing example: An order service that keeps its orders in memory only.
:::
`
		expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "an-order-survives-a-restart"), result{stdout: want})
		files, err := os.ReadFile(fixture(t, "compare", "two-checks", "spec", "orders.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(files), want) {
			t.Errorf("the files of two-checks do not hold the printed text %q", want)
		}
	})
	t.Run("print writes a component, an interface and a term, and each block once", func(t *testing.T) {
		t.Parallel()
		approved := fixture(t, "compare", "approved-text", "approved.json")
		want := ":::component{#orders-to-billing name=\"Orders to billing\" kind=\"interface\" joins=\"billing orders\"}\nOrders sends each order to billing through it.\n:::\n\n" +
			":::term{#order name=\"order\"}\nAn order is a request of a customer for one or more products.\n:::\n\n" +
			":::component{#billing name=\"Billing\"}\nIt sends a bill for each order.\n:::\n"
		expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "orders-to-billing", "--id", "order", "--id", "billing", "--id", "order"), result{stdout: want})
	})
	t.Run("print exits 2 on an id the approved spec does not hold or removed", func(t *testing.T) {
		t.Parallel()
		approved := fixture(t, "compare", "removed-kept", "approved.json")
		expect(t, runBinary(t, binary, "", "print", "--approved", approved, "--id", "acme-keeps-each-order", "--id", "an-order-is-paid-once", "--id", "each-order-gets-one-bill"), result{
			code: 2,
			stderr: "spec-check: " + approved + `: the approved spec removed node "an-order-is-paid-once", so it has no current text` + "\n" +
				"spec-check: " + approved + `: the approved spec holds no node "each-order-gets-one-bill"` + "\n",
		})
	})

	entries, err := os.ReadDir(fixture(t, "compare"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run("a copy of the printed text of "+entry.Name()+" passes compare, and one word more fails it", func(t *testing.T) {
			t.Parallel()
			approved := fixture(t, "compare", entry.Name(), "approved.json")
			content, err := os.ReadFile(approved)
			if err != nil {
				t.Fatal(err)
			}
			a, err := specgraph.ReadApproved(strings.NewReader(string(content)))
			if err != nil {
				t.Fatal(err)
			}
			current, _ := a.Current()
			args := []string{"print", "--approved", approved}
			for id, n := range a.Nodes {
				if !n.Removed {
					args = append(args, "--id", id)
				}
			}
			printed := runBinary(t, binary, "", args...)
			if printed.code != 0 || printed.stderr != "" {
				t.Fatalf("print: exit %d, stderr %q", printed.code, printed.stderr)
			}
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "spec.md"), []byte(printed.stdout))
			n := strconv.Itoa(len(current.Statements))
			expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{
				stdout: passLine(dir, n+" of "+n),
			})

			lines := strings.Split(printed.stdout, "\n")
			opening := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, ":::requirement{#") })
			if opening < 0 {
				t.Fatal("the printed text holds no statement")
			}
			id, _, _ := strings.Cut(strings.TrimPrefix(lines[opening], ":::requirement{#"), " ")
			lines[opening+1] = "planted " + lines[opening+1]
			writeFile(t, filepath.Join(dir, "spec.md"), []byte(strings.Join(lines, "\n")))
			expect(t, runBinary(t, binary, "", "compare", "--dir", dir, "--approved", approved), result{
				code:   1,
				stdout: "spec-check: " + dir + ": the comparison fails on 1 node\n",
				stderr: "spec-check: " + filepath.Join(dir, "spec.md") + ":" + strconv.Itoa(opening+1) + ": " + id + ": " + unapprovedText + "\n",
			})
		})
	}
}

// testGuard runs guard over specgraph's CI-file fixtures and over trees it makes: it names each
// line of a CI file that runs the comparison of the files with the approved spec (R2: "No CI job
// compares the files"), and an instrument that read no workflow does not pass.
func testGuard(t *testing.T, binary string) {
	t.Run("guard passes CI files that do not run the comparison", func(t *testing.T) {
		t.Parallel()
		root := fixture(t, "ci-files", "clean")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{stdout: "spec-check: " + root + ": no line of 4 CI files runs the comparison\n"})
	})
	t.Run("guard names each line that runs the comparison, from a workflow, an action and a script", func(t *testing.T) {
		t.Parallel()
		root := fixture(t, "ci-files", "planted")
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 3 lines of CI files run the comparison\n",
			stderr: "spec-check: " + filepath.Join(root, ".github", "actions", "compare", "action.yml") + ":9" + line +
				"spec-check: " + filepath.Join(root, ".github", "workflows", "compare.yaml") + ":13" + line +
				"spec-check: " + filepath.Join(root, "scripts", "compare.sh") + ":4" + line,
		})
	})
	t.Run("guard names one line in the singular", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, ".github", "workflows", "spec.yaml"), []byte("on: pull_request\njobs:\n  spec:\n    steps:\n      - run: |\n          spec-check \\\n            compare --dir .dispatch/spec --approved a.json\n"))
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 1 line of CI files runs the comparison\n",
			stderr: "spec-check: " + filepath.Join(root, ".github", "workflows", "spec.yaml") + ":6: this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n",
		})
	})
	t.Run("guard reads a run value as YAML and a word as the shell reads them", func(t *testing.T) {
		t.Parallel()
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		for _, row := range []struct{ name, step string }{
			{"a quoted subcommand", "      - run: spec-check \"compare\" --dir .dispatch/spec --approved a.json\n"},
			{"a plain scalar over two lines", "      - run: spec-check\n          compare --dir .dispatch/spec --approved a.json\n"},
			{"a folded scalar over two lines", "      - run: >-\n          spec-check\n          compare --dir .dispatch/spec --approved a.json\n"},
		} {
			t.Run(row.name, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
					t.Fatal(err)
				}
				workflow := filepath.Join(root, ".github", "workflows", "spec.yaml")
				writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+row.step))
				expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
					code:   1,
					stdout: "spec-check: " + root + ": 1 line of CI files runs the comparison\n",
					stderr: "spec-check: " + workflow + ":5" + line,
				})
			})
		}
	})
	// The second acceptance run's D3: sudo -n and docker run start the command in a CI step. The
	// guard names each such line in a workflow, which is the part of R2 that covers them.
	t.Run("guard names a sudo or a container step that runs the comparison", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		workflow := filepath.Join(root, ".github", "workflows", "spec.yaml")
		writeFile(t, workflow, []byte("on: pull_request\njobs:\n  spec:\n    steps:\n"+
			"      - run: sudo -n spec-check compare --dir .dispatch/spec --approved a.json\n"+
			"      - run: docker run --rm -v \"$PWD:/w\" -w /w -v \"$RUNNER_TEMP/spec-check:/usr/local/bin/spec-check:ro\" debian:bookworm-slim spec-check compare --dir .dispatch/spec --approved a.json\n"))
		line := ": this line runs the comparison of the files with the approved spec, which no CI job does (R2)\n"
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   1,
			stdout: "spec-check: " + root + ": 2 lines of CI files run the comparison\n",
			stderr: "spec-check: " + workflow + ":5" + line + "spec-check: " + workflow + ":6" + line,
		})
	})
	t.Run("guard does not pass when it reads no workflow", func(t *testing.T) {
		t.Parallel()
		root := fixture(t, "ci-files", "no-workflow")
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{code: 1, stderr: "spec-check: " + root + ": read no workflow\n"})
		// A workflows directory that holds only an empty directory, beside an action: no workflow.
		empty := t.TempDir()
		for _, dir := range []string{filepath.Join(".github", "workflows", "nested"), filepath.Join(".github", "actions", "spec")} {
			if err := os.MkdirAll(filepath.Join(empty, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(empty, ".github", "actions", "spec", "action.yml"), []byte("runs:\n  using: composite\n"))
		expect(t, runBinary(t, binary, "", "guard", "--root", empty), result{code: 1, stderr: "spec-check: " + empty + ": read no workflow\n"})
	})
	t.Run("guard names a relative root by its absolute path", func(t *testing.T) {
		t.Parallel()
		root := fixture(t, "ci-files", "clean")
		expect(t, runBinary(t, binary, filepath.Dir(root), "guard", "--root", "clean"), result{stdout: "spec-check: " + root + ": no line of 4 CI files runs the comparison\n"})
	})
	t.Run("guard exits 2 on a root it cannot read", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "missing")
		expect(t, runBinary(t, binary, "", "guard", "--root", missing), result{code: 2, stderr: "spec-check: " + missing + ": does not exist\n"})
		file := filepath.Join(t.TempDir(), "file")
		writeFile(t, file, []byte("A file.\n"))
		expect(t, runBinary(t, binary, "", "guard", "--root", file), result{code: 2, stderr: "spec-check: " + file + ": is not a directory\n"})
	})
	t.Run("guard exits 2 on a CI directory it cannot read", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory of mode 000")
		}
		root := t.TempDir()
		workflows := filepath.Join(root, ".github", "workflows")
		if err := os.MkdirAll(workflows, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(workflows, "spec.yaml"), []byte("on: pull_request\n"))
		locked := filepath.Join(root, "scripts")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		expect(t, runBinary(t, binary, "", "guard", "--root", root), result{
			code:   2,
			stderr: "spec-check: " + locked + ": cannot be read: open " + locked + ": permission denied\n",
		})
	})
}

// TestUsage runs the command with no subcommand and with one it does not know, each of which exits
// 2 with the usage, and with -h after a subcommand, which exits 0 with the usage.
func TestUsage(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "spec-check")
	expect(t, runBinary(t, binary, ""), result{code: 2, stderr: usage + "\n"})
	expect(t, runBinary(t, binary, "", "nope"), result{code: 2, stderr: "spec-check: unknown subcommand \"nope\"\n" + usage + "\n"})
	expect(t, runBinary(t, binary, "", "tree", "-h"), result{stderr: usage + "\n"})
}

// result is what one run of the binary did.
type result struct {
	code           int
	stdout, stderr string
}

// runBinary runs binary with args in dir, the test's own directory when dir is empty, in the
// environment of childEnv with no variable added, and answers its exit status and output.
func runBinary(t *testing.T, binary, dir string, args ...string) result {
	t.Helper()
	return runBinaryIn(t, binary, dir, nil, args...)
}

// runBinaryIn is runBinary with the variables extra, each "NAME=value", added to the environment.
func runBinaryIn(t *testing.T, binary, dir string, extra []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = childEnv(extra...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return result{code: cmd.ProcessState.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
}

// passLine is the first line of a comparison that passes over dir, with held "<m> of <n>": the
// approved spec held every text that the files hold, which may be an earlier text or a text of a
// removed node, so the line does not say that each text is approved now.
func passLine(dir, held string) string {
	return "spec-check: " + dir + ": the approved spec held every text that the files hold; " + held + " approved statements are in the files at their current text\n"
}

// childEnv is this process's environment without GITHUB_ACTIONS and CI, then extra. These tests
// run in a CI job, where compare refuses to run, so every child starts outside one unless a row
// sets one of the two itself.
func childEnv(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(v string) bool {
		return strings.HasPrefix(v, "GITHUB_ACTIONS=") || strings.HasPrefix(v, "CI=")
	})
	return append(env, extra...)
}

// expect fails the test unless got is want.
func expect(t *testing.T, got, want result) {
	t.Helper()
	if got != want {
		t.Errorf("exit %d, stdout %q, stderr %q\nwant exit %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr, want.code, want.stdout, want.stderr)
	}
}

// fixture is the absolute path of internal/dispatch/specgraph/testdata/<parts>, the fixture trees
// specgraph's own tests read.
func fixture(t *testing.T, parts ...string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "internal", "dispatch", "specgraph", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// readDir reads each file directly in dir, by name, as specgraph.Parse takes them.
func readDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = content
	}
	return files
}

// writeFile writes content to path.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// copyOf is a copy of the files of dir in a new temporary directory.
func copyOf(t *testing.T, dir string) string {
	t.Helper()
	copied := filepath.Join(t.TempDir(), filepath.Base(dir))
	if err := os.CopyFS(copied, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	return copied
}
