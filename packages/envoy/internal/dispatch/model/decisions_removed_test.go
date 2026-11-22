package model

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// This file is c-decisions-removed of dispatch://INFRA-1098 (dispatch://INFRA-1098/spec#b-r-decision-node
// and #b-r-answers-kept): the decisions document is gone from every copy of the tree, and no
// prompt tells an agent to take a person's settled answer out of the spec. It spells none of the
// four strings its first scan searches for, since it is a production path like any other: each is
// built from two halves.

// documentName is the decisions document's file name, the first of the first scan's strings.
var documentName = "decisions" + ".md"

// spelledStrings are the four strings the first scan searches every production path for: the
// document's name, the artifact role constant, the gated step constant and the design step
// constant.
var spelledStrings = []string{documentName, "ArtifactRole" + "Decisions", "GatedStep" + "Decisions", "DesignStep" + "Decisions"}

// parkedPrefix is what makes a spelling of the document's name no hit: the controller's reference
// about trees moved to backlog carries the name behind it, and has nothing to do with this
// document (dispatch://INFRA-1150's c-spelling-gone reads it the same way).
const parkedPrefix = "parked-"

// refusalFile is the file that holds the one site the first scan excludes rather than a whole
// path: the code that refuses a new document named after the decisions document with
// refusalCode, which dispatch://INFRA-1150 adds and which stays for good. refusalFunction is that
// site's function and refusalConstant the constant its refusal answers.
const (
	refusalFile     = "packages/envoy/internal/dispatch/api/artifacts.go"
	refusalFunction = "decisionsAreNodes"
	refusalConstant = "codeDecisionsAreNodes"
	refusalCode     = "DECISIONS_ARE_NODES"
	refusalCodeFile = "packages/envoy/internal/dispatch/api/handler_errors.go"
)

// repositoryRoot is the checkout this package stands in, five directories above it.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages", "envoy", "go.mod")); err != nil {
		t.Fatalf("%s holds no packages/envoy/go.mod, so it is no checkout of this repository: %v", root, err)
	}
	return root
}

// checkoutFiles is every file of the checkout git does not ignore, tracked or not, by its path
// from root: what a reviewer of the tree reads. A tracked file the working copy deleted is left
// out, since no copy of it stands.
func checkoutFiles(t *testing.T, root string) []string {
	t.Helper()
	listed, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("list the checkout's files with git ls-files: %v", err)
	}
	var files []string
	for _, name := range strings.Split(string(listed), "\x00") {
		if name == "" {
			continue
		}
		if info, err := os.Lstat(filepath.Join(root, name)); err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, name)
	}
	slices.Sort(files)
	return slices.Compact(files)
}

// spelledLine is one line of a file that spells one of spelledStrings.
type spelledLine struct {
	path string
	line int
	text string
}

func (s spelledLine) String() string {
	return fmt.Sprintf("%s:%d: %s", s.path, s.line, strings.TrimSpace(s.text))
}

// spells reports whether line spells one of spelledStrings, a spelling of the document's name
// behind parkedPrefix being none.
func spells(line string) bool {
	for _, spelled := range spelledStrings {
		for at := 0; ; {
			index := strings.Index(line[at:], spelled)
			if index < 0 {
				break
			}
			index += at
			if spelled != documentName || !strings.HasSuffix(line[:index], parkedPrefix) {
				return true
			}
			at = index + len(spelled)
		}
	}
	return false
}

// isBinary reports whether content is no text: it holds a NUL byte in its first 8 KiB, which is
// how git itself tells a binary file.
func isBinary(content []byte) bool {
	return bytes.IndexByte(content[:min(len(content), 8192)], 0) >= 0
}

// spelledLines is every line of files, under root, that spells one of spelledStrings, with how
// many text files it read and how many binary files it passed over.
func spelledLines(t *testing.T, root string, files []string) (found []spelledLine, read, binary int) {
	t.Helper()
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if isBinary(content) {
			binary++
			continue
		}
		read++
		for index, line := range strings.Split(string(content), "\n") {
			if spells(line) {
				found = append(found, spelledLine{path: name, line: index + 1, text: line})
			}
		}
	}
	return found, read, binary
}

// pathExclusion is one of the paths the first scan excludes, with why.
type pathExclusion struct {
	name, reason string
	holds        func(path string) bool
}

// migrationFile matches an applied migration of either store: a SQL file of a migrations
// directory under packages/envoy or packages/daemon.
var migrationFile = regexp.MustCompile(`^packages/(?:envoy|daemon)/(?:.*/)?migrations/[^/]+\.sql$`)

// fixturesPath is the tree's own regression material, which carries the old document.
const fixturesPath = "packages/dispatch/e2e/fixtures/"

// pathExclusions are four of the five exclusions: the migrations, the changelog, the fixtures and
// the test material.
// The fifth is a site rather than a path (refusalSite).
var pathExclusions = []pathExclusion{{
	name: "the migrations that drop those symbols (an applied migration of either store)",
	reason: "a migration the runner has applied is history every boot replays as written and no change edits, " +
		"so the step that drops the role, the gated step and the design step stands in the words it was written in",
	holds: migrationFile.MatchString,
}, {
	name:   "the changelog (each CHANGELOG.md, and the unreleased entries under a .changes directory that a release writes into it)",
	reason: "a changelog records what each release did, the document among it, and a release rewrites none of what it recorded",
	holds: func(name string) bool {
		return path.Base(name) == "CHANGELOG.md" || strings.HasPrefix(name, ".changes/") || strings.Contains(name, "/.changes/")
	},
}, {
	name:   fixturesPath,
	reason: "it holds this tree's own regression material, captures of issues that carry the old document, which the conversion's checks replay",
	holds:  func(name string) bool { return strings.HasPrefix(name, fixturesPath) },
}, {
	name: "test material (a Go or TypeScript test file, a __tests__ or testdata directory, an e2e tree, and scripts/approval-eval/)",
	reason: "a production path is a file a shipped build or a skill reads, and no shipped build and no skill reads a test, " +
		"the end-to-end tree that drives a build from outside, or the evaluation that grades an approval",
	holds: testMaterial.MatchString,
}}

// testMaterial matches a path no shipped build and no skill reads: a Go test file, a TypeScript
// test, a __tests__ or testdata directory, an e2e tree, and the approval evaluation.
var testMaterial = regexp.MustCompile(`_test\.go$|\.test\.tsx?$|(?:^|/)(?:__tests__|testdata|e2e)/|^scripts/approval-eval/`)

// refusalSite is the fifth exclusion: the lines of refusalFunction in refusalFile, with the
// constant its refusal answers.
type refusalSite struct {
	first, last int
	code        string
}

func (s refusalSite) holds(line spelledLine) bool {
	return line.path == refusalFile && line.line >= s.first && line.line <= s.last
}

// readRefusalSite finds refusalFunction in refusalFile under root, its doc comment included, and
// fails unless it answers refusalConstant and that constant is refusalCode: a site that stopped
// refusing the document would be an exclusion hiding a use.
func readRefusalSite(t *testing.T, root string) refusalSite {
	t.Helper()
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, filepath.Join(root, refusalFile), nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", refusalFile, err)
	}
	var site refusalSite
	for _, declared := range parsed.Decls {
		function, ok := declared.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != refusalFunction {
			continue
		}
		start := function.Pos()
		if function.Doc != nil {
			start = function.Doc.Pos()
		}
		site.first, site.last = files.Position(start).Line, files.Position(function.End()).Line
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if named, ok := node.(*ast.Ident); ok && named.Name == refusalConstant {
				site.code = refusalConstant
			}
			return true
		})
	}
	if site.first == 0 {
		t.Fatalf("%s declares no function %s, the site that refuses a new document named %s: the exclusion names a site that is gone", refusalFile, refusalFunction, documentName)
	}
	if site.code == "" {
		t.Fatalf("%s#%s answers no %s: the site the exclusion keeps no longer refuses with %s", refusalFile, refusalFunction, refusalConstant, refusalCode)
	}
	constants, err := os.ReadFile(filepath.Join(root, refusalCodeFile))
	if err != nil {
		t.Fatalf("read %s: %v", refusalCodeFile, err)
	}
	if !regexp.MustCompile(`\b` + refusalConstant + `\s*=\s*"` + refusalCode + `"`).Match(constants) {
		t.Fatalf("%s does not declare %s = %q", refusalCodeFile, refusalConstant, refusalCode)
	}
	return site
}

// literalScan is what the first scan found over a tree: every production line that spells one of
// the strings, every line an exclusion holds, by the exclusion, and what it read.
type literalScan struct {
	hits         []spelledLine
	excluded     map[string][]spelledLine
	read, binary int
	files        map[string]bool
}

// scanLiterally runs the first scan over files under root.
func scanLiterally(t *testing.T, root string, files []string, site refusalSite) literalScan {
	t.Helper()
	found, read, binary := spelledLines(t, root, files)
	scan := literalScan{excluded: map[string][]spelledLine{}, read: read, binary: binary, files: map[string]bool{}}
	for _, name := range files {
		scan.files[name] = true
	}
	for _, line := range found {
		kept := ""
		for _, exclusion := range pathExclusions {
			if exclusion.holds(line.path) {
				kept = exclusion.name
				break
			}
		}
		if kept == "" && site.holds(line) {
			kept = refusalFile + "#" + refusalFunction
		}
		if kept == "" {
			scan.hits = append(scan.hits, line)
			continue
		}
		scan.excluded[kept] = append(scan.excluded[kept], line)
	}
	return scan
}

// TestTheDecisionsDocumentIsGoneFromEveryCopy is c-decisions-removed. Two scans read the tree,
// each failing on a broken state of its own, and a third step reads the daemon's refusal.
//
// The first scan is literal: every production path, a file a shipped build or a skill reads, which
// is every path outside the five it excludes and prints with their reasons, holds none of the
// document's name, ArtifactRole's, GatedStep's and DesignStep's constants for it, a spelling of
// the name behind parkedPrefix being no hit. One exclusion is a site, the refusal of a new
// document of that name, and a spelling anywhere else in its file is a hit. It prints the set it
// must cover, each member found by its symbol and read where it stands.
//
// The second scan reads the instruction rather than the strings: every file an agent is given -
// the skills, the daemon's role prompts and the agents the plugins ship (instructionPaths) - holds
// no sentence with a word for a person's settled answer, a word for taking it away and a word for
// the document it would be taken out of. skills/dispatch-writing-specs/SKILL.md, which carries the
// tree's own prohibition, is excluded by path, and the sentences it would refuse are printed.
//
// Last, the daemon's own design-step list holds no decisions step, and the daemon, built from this
// checkout, refuses a deployment configuration that gates it and asks for spec.
//
// Each scan is run first over a planted instance of its broken state in a scratch tree of its own,
// so a scan that cannot find what it looks for fails rather than passing on nothing.
func TestTheDecisionsDocumentIsGoneFromEveryCopy(t *testing.T) {
	root := repositoryRoot(t)
	site := readRefusalSite(t, root)

	t.Run("the first scan finds a planted spelling and keeps only what its exclusions name", func(t *testing.T) {
		requireTheLiteralScanFindsAPlantedSpelling(t, root, site)
	})
	t.Run("no production path spells the decisions document, its role, its gated step or its design step", func(t *testing.T) {
		requireNoProductionPathSpellsTheDocument(t, root, site)
	})
	t.Run("the second scan refuses a planted instruction and no correct one", func(t *testing.T) {
		requireTheInstructionScanRefusesAPlantedSentence(t)
	})
	t.Run("no skill and no daemon file tells an agent to take an answer out of the spec", func(t *testing.T) {
		requireNoInstructionMovesAnAnswerOutOfTheSpec(t, root)
	})
	t.Run("the design-step lists hold no decisions step", func(t *testing.T) {
		requireNoDecisionsStepInTheStepLists(t, root)
	})
	t.Run("the daemon refuses a deployment that gates decisions and asks for spec", func(t *testing.T) {
		requireTheDaemonRefusesTheDecisionsStep(t, root)
	})
}

// requireTheLiteralScanFindsAPlantedSpelling runs the first scan over a scratch tree holding one
// planted instance of each case: a production line spelling the name, a parked reference that is
// no hit, a copy of the refusal's file with a second use added outside the refusal, which must be a
// hit, a line inside the refusal, a fixture and a migration, each of which an exclusion must keep.
func requireTheLiteralScanFindsAPlantedSpelling(t *testing.T, root string, site refusalSite) {
	planted := t.TempDir()
	refusal, err := os.ReadFile(filepath.Join(root, refusalFile))
	if err != nil {
		t.Fatalf("read %s: %v", refusalFile, err)
	}
	refusalLines := strings.Split(string(refusal), "\n")
	// The line inside the refusal goes into its doc comment, right after its first line, and the
	// second use after the file's last line.
	inside := slices.Concat(refusalLines[:site.first], []string{"// The planted refusal names " + documentName + "."}, refusalLines[site.first:])
	withSecondUse := strings.Join(inside, "\n") + "\n// A second use of " + documentName + " outside the refusal.\n"
	plantedFiles := map[string]string{
		"skills/planted/SKILL.md":     "Move the answered asks into " + documentName + ".\n",
		"skills/planted/parked.md":    "Read skill://legion-controller/references/" + parkedPrefix + documentName + " first.\n",
		refusalFile:                   withSecondUse,
		fixturesPath + "planted.json": `{"name": "` + documentName + `"}` + "\n",
		"packages/envoy/internal/dispatch/store/migrations/20990101000000_planted.up.sql": "-- The step " + spelledStrings[2] + " named.\n",
		"packages/envoy/internal/dispatch/planted/planted_test.go":                        "package planted\n\n// A test names " + documentName + ".\n",
	}
	var names []string
	for name, content := range plantedFiles {
		writeScratchFile(t, planted, name, content)
		names = append(names, name)
	}
	slices.Sort(names)
	plantedSite := site
	plantedSite.last++
	scan := scanLiterally(t, planted, names, plantedSite)
	for _, hit := range scan.hits {
		t.Logf("planted hit %s", hit)
	}
	// The scan reads the files in name order, so the refusal's file comes first.
	want := []string{fmt.Sprintf("%s:%d", refusalFile, len(inside)+1), "skills/planted/SKILL.md:1"}
	var got []string
	for _, hit := range scan.hits {
		got = append(got, fmt.Sprintf("%s:%d", hit.path, hit.line))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the scan over the planted tree %s reports the hits %v, want %v: the planted production line and the second use in the refusal's file, and no parked reference", planted, got, want)
	}
	for _, kept := range []string{pathExclusions[0].name, fixturesPath, pathExclusions[3].name, refusalFile + "#" + refusalFunction} {
		if len(scan.excluded[kept]) != 1 {
			t.Fatalf("the scan over the planted tree keeps %d lines under %q, want the one planted there: %v", len(scan.excluded[kept]), kept, scan.excluded[kept])
		}
	}
	t.Logf("the scan over the planted tree %s reads %d files and reports the planted production line and the second use in %s as hits, keeps the line inside %s, the fixture, the test file and the migration, and passes over the parked reference",
		planted, scan.read, refusalFile, refusalFunction)
}

// writeScratchFile writes content to name under dir, making its directories.
func writeScratchFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requireNoProductionPathSpellsTheDocument runs the first scan over the checkout. It prints each
// exclusion with its reason and every line it kept, then the set the scan must cover, and fails on
// every production line that spells one of the strings.
func requireNoProductionPathSpellsTheDocument(t *testing.T, root string, site refusalSite) {
	files := checkoutFiles(t, root)
	scan := scanLiterally(t, root, files, site)
	t.Logf("the first scan read %d text files of the checkout and passed over %d binary ones, searching for %s, %s, %s and %s (behind %q no hit)",
		scan.read, scan.binary, spelledStrings[0], spelledStrings[1], spelledStrings[2], spelledStrings[3], parkedPrefix)
	if scan.read == 0 {
		t.Fatalf("the first scan read no file of the checkout %s, so it proves nothing", root)
	}
	for _, exclusion := range pathExclusions {
		t.Logf("excluded: %s, because %s; it holds %d lines that spell one", exclusion.name, exclusion.reason, len(scan.excluded[exclusion.name]))
		for _, line := range scan.excluded[exclusion.name] {
			t.Logf("  kept %s", line)
		}
	}
	siteName := refusalFile + "#" + refusalFunction
	t.Logf("excluded: the site %s, lines %d to %d, which refuses a new document named %s with 409 %s (%s), and which stays for good (dispatch://INFRA-1150/spec#b-r-decisions-document-gone); any other line of %s is a production line; it holds %d lines that spell one",
		siteName, site.first, site.last, documentName, refusalCode, refusalConstant, refusalFile, len(scan.excluded[siteName]))
	for _, line := range scan.excluded[siteName] {
		t.Logf("  kept %s", line)
	}

	requireTheCoveredSet(t, root, scan)

	for _, hit := range scan.hits {
		t.Logf("hit %s", hit)
	}
	if len(scan.hits) != 0 {
		byFile := map[string]int{}
		for _, hit := range scan.hits {
			byFile[hit.path]++
		}
		var paths []string
		for name, count := range byFile {
			paths = append(paths, fmt.Sprintf("%s (%d)", name, count))
		}
		slices.Sort(paths)
		t.Errorf("%d production lines in %d files spell %s, %s, %s or %s, each printed above as hit:\n  %s",
			len(scan.hits), len(byFile), spelledStrings[0], spelledStrings[1], spelledStrings[2], spelledStrings[3], strings.Join(paths, "\n  "))
	}
}

// coveredSymbol is one member of the set the first scan must cover that is a declaration: the
// directory its package stands in, the name it is declared under, and a function's receiver
// type where it is a method.
type coveredSymbol struct {
	what, directory, name string
}

// coveredSymbols are the declarations of the set the first scan must cover, each cited by its
// symbol and found in its package: the daemon's readers of the design step, which are the two in
// its API, the reader of a deployment's gates.design, and the three of the workflow.
var coveredSymbols = []coveredSymbol{
	{"the daemon's API reader of the step a registration records", "packages/daemon/internal/api", "registeredStep"},
	{"the daemon's API reader of the document a step names", "packages/daemon/internal/api", "stepDocument"},
	{"the reader of a deployment's gates.design", "packages/daemon/internal/config", "readDesignSteps"},
	{"the workflow reader that hands over a kept decision", "packages/daemon/internal/workflow", "HandOverKept"},
	{"the workflow reader that catches up the trees owed", "packages/daemon/internal/workflow", "CatchUpTheTreesOwed"},
	{"the workflow reader that says whether the architect hears", "packages/daemon/internal/workflow", "ArchitectHears"},
	{"the daemon's design-step list", "packages/daemon/internal/record", "DesignSteps"},
	{"the server's design-gate step list", "packages/envoy/internal/dispatch/model", "GatedStepsKnown"},
}

// readerConstants are the two constants of the decisions read the release before this tree held
// in packages/envoy/internal/dispatch/api/decisions.go, the name and the slug of the document.
var readerConstants = []string{"decisionsDocumentName", "decisionsDocumentSlug"}

// routeDescriptions are the two routes of packages/envoy/internal/dispatch/api/routes_table.go
// whose descriptions spelled the document: the decisions read and the Keep of a decision.
var routeDescriptions = []string{`"/api/v1/issues/{key}/decisions"`, `"/api/v1/artifacts/{id}/blocks/{block_id}/keep"`}

// requireTheCoveredSet prints the set the first scan must cover, each member where it stands,
// and fails on a member the scan did not read, or one that still spells a string.
func requireTheCoveredSet(t *testing.T, root string, scan literalScan) {
	t.Helper()
	covered := func(what, name string, first, last int) {
		t.Helper()
		if !scan.files[name] {
			t.Errorf("covers %s at %s, which the first scan did not read", what, name)
			return
		}
		for _, hit := range scan.hits {
			if hit.path == name && hit.line >= first && hit.line <= last {
				t.Errorf("covers %s at %s:%d-%d, which spells one: %s", what, name, first, last, hit)
				return
			}
		}
		t.Logf("covers %s at %s:%d-%d: read, and spells none", what, name, first, last)
	}
	const readerFile = "packages/envoy/internal/dispatch/api/decisions.go"
	declared := declaredIn(t, filepath.Join(root, filepath.Dir(readerFile)))
	for _, constant := range readerConstants {
		if at, ok := declared[constant]; ok {
			t.Errorf("covers the decisions read's constant %s, which %s still declares", constant, at.file)
		} else {
			t.Logf("covers the decisions read's constant %s: no file of %s declares it", constant, path.Dir(readerFile))
		}
	}
	covered("the decisions read", readerFile, 1, lineCount(t, root, readerFile))
	modelDeclared := declaredIn(t, filepath.Join(root, "packages/envoy/internal/dispatch/model"))
	if at, ok := modelDeclared[spelledStrings[1]]; ok {
		t.Errorf("covers model.%s, which %s still declares", spelledStrings[1], at.file)
	} else {
		t.Logf("covers model.%s: no file of packages/envoy/internal/dispatch/model declares it", spelledStrings[1])
	}
	for _, held := range coveredConstraints {
		migration := firstMigrationWriting(t, root, scan, held.written)
		kept := 0
		for _, line := range scan.excluded[pathExclusions[0].name] {
			if line.path == migration {
				kept++
			}
		}
		t.Logf("covers the constraint %s, which %s writes: read as a migration, which the first scan excludes and in which it kept %d lines; the boot's retirement narrows the constraint",
			held.constraint, migration, kept)
	}
	for _, symbol := range coveredSymbols {
		at, ok := declaredIn(t, filepath.Join(root, symbol.directory))[symbol.name]
		if !ok {
			t.Errorf("covers %s, %s, which no file of %s declares", symbol.what, symbol.name, symbol.directory)
			continue
		}
		covered(symbol.what+" ("+symbol.name+")", at.file, at.first, at.last)
	}
	const routesFile = "packages/envoy/internal/dispatch/api/routes_table.go"
	routes, err := os.ReadFile(filepath.Join(root, routesFile))
	if err != nil {
		t.Fatalf("read %s: %v", routesFile, err)
	}
	for _, route := range routeDescriptions {
		line := 0
		for index, text := range strings.Split(string(routes), "\n") {
			if strings.Contains(text, route) {
				line = index + 1
				break
			}
		}
		if line == 0 {
			t.Logf("covers the route description of %s in %s: the table holds no such route", route, routesFile)
			continue
		}
		covered("the route description of "+route, routesFile, line, line)
	}
}

// declaration is where a top-level name is declared: its file from the checkout's root and its
// lines, the doc comment among them.
type declaration struct {
	file        string
	first, last int
}

// declaredIn is every top-level name the Go files of dir that `go build` reads declare - types,
// functions and methods by their name alone, values - with where each is declared. It reads no
// test file.
func declaredIn(t *testing.T, dir string) map[string]declaration {
	t.Helper()
	root := repositoryRoot(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	files := token.NewFileSet()
	names := map[string]declaration{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		parsed, err := parser.ParseFile(files, full, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", full, err)
		}
		relative, err := filepath.Rel(root, full)
		if err != nil {
			t.Fatal(err)
		}
		at := func(doc *ast.CommentGroup, start, end token.Pos) declaration {
			if doc != nil {
				start = doc.Pos()
			}
			return declaration{file: filepath.ToSlash(relative), first: files.Position(start).Line, last: files.Position(end).Line}
		}
		for _, declared := range parsed.Decls {
			switch declared := declared.(type) {
			case *ast.FuncDecl:
				names[declared.Name.Name] = at(declared.Doc, declared.Pos(), declared.End())
			case *ast.GenDecl:
				for _, spec := range declared.Specs {
					doc := declared.Doc
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						if spec.Doc != nil {
							doc = spec.Doc
						}
						names[spec.Name.Name] = at(doc, spec.Pos(), spec.End())
					case *ast.ValueSpec:
						if spec.Doc != nil {
							doc = spec.Doc
						}
						for _, name := range spec.Names {
							names[name.Name] = at(doc, spec.Pos(), spec.End())
						}
					}
				}
			}
		}
	}
	return names
}

// lineCount is how many lines the file name under root holds.
func lineCount(t *testing.T, root, name string) int {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.Count(string(content), "\n") + 1
}

// coveredConstraints are the two constraints of the set the first scan must cover, each with
// what finds the migration that writes it: the artifact role constraint by its name, and the
// gated steps' check, which its migration leaves unnamed, by its expression.
var coveredConstraints = []struct{ constraint, written string }{
	{"artifacts_role_check", "artifacts_role_check"},
	{"projects_gated_steps_check", "gated_steps <@"},
}

// firstMigrationWriting is the first migration of the server's store, in the runner's order,
// that holds written, or a note that none does.
func firstMigrationWriting(t *testing.T, root string, scan literalScan, written string) string {
	t.Helper()
	const migrations = "packages/envoy/internal/dispatch/store/migrations/"
	var names []string
	for name := range scan.files {
		if strings.HasPrefix(name, migrations) && strings.HasSuffix(name, ".up.sql") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(content), written) {
			return name
		}
	}
	return "no migration of " + migrations
}

// The second scan's three sets of words: one for a person's settled answer, one for taking it
// away, and one for the document it would be taken out of. A sentence is refused only when it
// holds a word of each: a decision word beside a removal word is how Legion's kept-decision relay
// is written, and it says nothing about a spec. The document's name behind parkedPrefix is no
// word for the document, as it is no hit of the first scan: it names the controller's reference
// about trees moved to backlog, which workflow/parked_decision.go cites beside its relay.
var (
	answerWord   = regexp.MustCompile(`(?i)\b(?:decisions?|answered\s+asks?)\b`)
	removalWord  = regexp.MustCompile(`(?i)\b(?:move|moved|copy|copied|relocate|extract)\b|\bout\s+of\s+the\s+spec\b`)
	documentWord = regexp.MustCompile(`(?i)\b(?:spec|specification|primary\s+document)\b`)
)

// namesTheDocument reports whether text holds a word for the document an answer would be taken
// out of: spec, specification, primary document, or the decisions document's name, in any case,
// not behind parkedPrefix.
func namesTheDocument(text string) bool {
	if documentWord.MatchString(text) {
		return true
	}
	lower := strings.ToLower(text)
	for at := 0; ; {
		index := strings.Index(lower[at:], documentName)
		if index < 0 {
			return false
		}
		index += at
		if !strings.HasSuffix(lower[:index], parkedPrefix) {
			return true
		}
		at = index + len(documentName)
	}
}

// excludedInstructionPath is the one path the second scan excludes, by path: the skill that
// carries this tree's own replacement prohibition, which holds a word of each set as a correct
// prohibition does. The first scan still reads it.
const excludedInstructionPath = "skills/dispatch-writing-specs/SKILL.md"

// plantedInstruction is the broken state the second scan fails on: a prompt that still tells an
// agent to move a person's answers out of the spec, with none of the first scan's strings in it.
const plantedInstruction = "Move answered asks out of the spec into the decisions artifact."

// sentence is one sentence of a file, with the line it starts on.
type sentence struct {
	line int
	text string
}

// sentenceEnd is where one sentence of a paragraph ends: a full stop, a question mark or an
// exclamation mark, the closing quotes and brackets after it, and the space after those.
var sentenceEnd = regexp.MustCompile(`[.!?]["'”’)\]*_]*(?:\s+|$)`)

// paragraph is one run of prose: its lines, each with its number.
type paragraph struct {
	lines []sentence
}

// sentencesOf splits each paragraph of content into sentences. A Go file's prose is its comments
// and its string literals, read with the Go scanner, so a sentence never runs on into code; any
// other file's is its lines, a blank line, a heading, a list item, a table row and a change of
// comment marker each starting a paragraph of their own.
func sentencesOf(name string, content []byte) []sentence {
	var paragraphs []paragraph
	if strings.HasSuffix(name, ".go") {
		if read, ok := goParagraphs(content); ok {
			paragraphs = read
		}
	}
	if paragraphs == nil {
		paragraphs = lineParagraphs(string(content))
	}
	var sentences []sentence
	for _, held := range paragraphs {
		// The paragraph's text, with the offset each of its lines starts at.
		var text strings.Builder
		starts := make([]int, len(held.lines))
		for index, line := range held.lines {
			starts[index] = text.Len()
			text.WriteString(strings.TrimSpace(line.text))
			text.WriteByte(' ')
		}
		joined := text.String()
		lineAt := func(offset int) int {
			at := 0
			for index, start := range starts {
				if start <= offset {
					at = index
				}
			}
			return held.lines[at].line
		}
		from := 0
		for _, end := range sentenceEnd.FindAllStringIndex(joined, -1) {
			if words := strings.Join(strings.Fields(joined[from:end[1]]), " "); words != "" {
				sentences = append(sentences, sentence{line: lineAt(from), text: words})
			}
			from = end[1]
		}
		if words := strings.Join(strings.Fields(joined[from:]), " "); words != "" {
			sentences = append(sentences, sentence{line: lineAt(from), text: words})
		}
	}
	return sentences
}

// goParagraphs is a Go file's prose: each run of comments on adjacent lines, and each run of
// string literals joined by +, as one paragraph each. It answers false for a file the Go scanner
// cannot read, which is then read by its lines.
func goParagraphs(content []byte) ([]paragraph, bool) {
	files := token.NewFileSet()
	file := files.AddFile("", files.Base(), len(content))
	var read scanner.Scanner
	failed := false
	read.Init(file, content, func(token.Position, string) { failed = true }, scanner.ScanComments)
	var paragraphs []paragraph
	var current *paragraph
	lastLine, lastKind := -1, token.ILLEGAL
	for {
		at, kind, literal := read.Scan()
		if kind == token.EOF {
			break
		}
		line := files.Position(at).Line
		switch kind {
		case token.COMMENT:
			text := strings.TrimPrefix(strings.TrimPrefix(literal, "//"), "/*")
			text = strings.TrimSuffix(text, "*/")
			if current == nil || lastKind != token.COMMENT || line > lastLine+1 {
				paragraphs = append(paragraphs, paragraph{})
				current = &paragraphs[len(paragraphs)-1]
			}
			for offset, part := range strings.Split(text, "\n") {
				current.lines = append(current.lines, sentence{line: line + offset, text: strings.TrimPrefix(strings.TrimSpace(part), "*")})
			}
			lastLine, lastKind = line+strings.Count(literal, "\n"), token.COMMENT
		case token.STRING:
			value, err := strconv.Unquote(literal)
			if err != nil {
				value = literal
			}
			if current == nil || lastKind != token.ADD {
				paragraphs = append(paragraphs, paragraph{})
				current = &paragraphs[len(paragraphs)-1]
			}
			for offset, part := range strings.Split(value, "\n") {
				current.lines = append(current.lines, sentence{line: line + offset, text: part})
			}
			lastLine, lastKind = line+strings.Count(literal, "\n"), token.STRING
		case token.ADD:
			if lastKind == token.STRING {
				lastKind = token.ADD
			} else {
				lastKind = kind
			}
		case token.SEMICOLON:
			// The scanner inserts a semicolon at a line's end; it breaks no comment run, and a
			// string run goes on only through a +.
			if literal == "\n" && lastKind == token.COMMENT {
				continue
			}
			lastKind = kind
		default:
			lastKind = kind
		}
	}
	return paragraphs, !failed
}

// lineMarker matches what opens a line before its prose: indentation, a comment marker and a
// list item's marker or a heading's.
var lineMarker = regexp.MustCompile(`^\s*(?:(//+|#+|--+|;+|\*+|>+|/\*+|<!--)\s*)?((?:[-*+]|\d+[.)])\s+)?`)

// lineParagraphs reads text by its lines: each line loses its marker, and a blank line, a heading,
// a list item, a table row or a change of marker starts a new paragraph.
func lineParagraphs(text string) []paragraph {
	var paragraphs []paragraph
	previous := "\x00"
	for index, line := range strings.Split(text, "\n") {
		match := lineMarker.FindStringSubmatch(line)
		marker, item := match[1], match[2]
		rest := strings.TrimSpace(line[len(match[0]):])
		if rest == "" || strings.HasPrefix(rest, "|") {
			previous = "\x00"
			if rest != "" {
				paragraphs = append(paragraphs, paragraph{lines: []sentence{{line: index + 1, text: rest}}})
			}
			continue
		}
		heading := strings.HasPrefix(marker, "#") && !strings.HasPrefix(line, "#!") && strings.HasPrefix(strings.TrimSpace(line), marker+" ")
		if marker != previous || item != "" || heading || len(paragraphs) == 0 {
			paragraphs = append(paragraphs, paragraph{})
		}
		held := &paragraphs[len(paragraphs)-1]
		held.lines = append(held.lines, sentence{line: index + 1, text: rest})
		previous = marker
		if heading {
			previous = "\x00"
		}
	}
	return paragraphs
}

// instructionVerdict is what the second scan makes of one sentence: refused, holding a word of
// each set; accepted, holding an answer word and a removal word but no document word, which a
// reader audits; or neither.
func instructionVerdict(text string) (refused, accepted bool) {
	if !answerWord.MatchString(text) || !removalWord.MatchString(text) {
		return false, false
	}
	named := namesTheDocument(text)
	return named, !named
}

// instructionScan is what the second scan found over a set of files.
type instructionScan struct {
	read, binary int
	sentences    int
	refused      []string
	accepted     []string
	excluded     []string
	perFile      []string
}

// scanInstructions runs the second scan over files under root.
func scanInstructions(t *testing.T, root string, files []string) instructionScan {
	t.Helper()
	var scan instructionScan
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if isBinary(content) {
			scan.binary++
			scan.perFile = append(scan.perFile, name+": binary, passed over")
			continue
		}
		scan.read++
		held := sentencesOf(name, content)
		scan.sentences += len(held)
		scan.perFile = append(scan.perFile, fmt.Sprintf("%s: %d sentences", name, len(held)))
		for _, read := range held {
			refused, accepted := instructionVerdict(read.text)
			at := fmt.Sprintf("%s:%d: %s", name, read.line, read.text)
			switch {
			case refused && name == excludedInstructionPath:
				scan.excluded = append(scan.excluded, at)
			case refused:
				scan.refused = append(scan.refused, at)
			case accepted:
				scan.accepted = append(scan.accepted, at)
			}
		}
	}
	return scan
}

// requireTheInstructionScanRefusesAPlantedSentence runs the second scan's verdict over the planted
// instruction, which it must refuse, over a correct prohibition, which it refuses too (and which
// is why its one path is excluded), and over the kept-decision relay's way of writing, which it
// must accept; and then the whole scan over a scratch tree holding the planted instruction in a
// prompt, which it must report with its file and its line.
func requireTheInstructionScanRefusesAPlantedSentence(t *testing.T) {
	for _, held := range []struct {
		text              string
		refused, accepted bool
	}{
		{plantedInstruction, true, false},
		{"An answered ask is never moved out of the spec.", true, false},
		{"The kept decision is moved to the next architect, who hears it once.", false, true},
		{"The spec holds the requirement.", false, false},
		{"A parked decision is moved back by the controller (skill://legion-controller/references/" + parkedPrefix + documentName + ").", false, true},
		{"Move the decision into " + documentName + ".", true, false},
	} {
		refused, accepted := instructionVerdict(held.text)
		t.Logf("verdict on %q: refused %v, accepted %v", held.text, refused, accepted)
		if refused != held.refused || accepted != held.accepted {
			t.Errorf("the verdict on %q is refused %v and accepted %v, want refused %v and accepted %v", held.text, refused, accepted, held.refused, held.accepted)
		}
	}
	planted := t.TempDir()
	prompt := "packages/daemon/internal/prompts/roles/planted.md"
	writeScratchFile(t, planted, prompt, "# Planted\n\nRead the spec first.\nThen "+strings.ToLower(plantedInstruction[:1])+plantedInstruction[1:]+"\nThe kept decision is moved to the next architect.\n")
	code := "packages/daemon/internal/planted/planted.go"
	writeScratchFile(t, planted, code, "package planted\n\n// Planted tells the agent what to do.\n// "+plantedInstruction+"\nconst Planted = \"the spec\"\n")
	scan := scanInstructions(t, planted, []string{prompt, code})
	for _, refused := range scan.refused {
		t.Logf("planted refused %s", refused)
	}
	want := []string{prompt + ":4: Then move answered asks out of the spec into the decisions artifact.", code + ":4: " + plantedInstruction}
	if !slices.Equal(scan.refused, want) {
		t.Fatalf("the second scan over the planted tree %s refuses %q, want %q", planted, scan.refused, want)
	}
}

// instructionPaths match every file an agent is given, which the second scan reads: the skills,
// the daemon's role prompts and the agents the plugins ship, but a test of them, which no agent is
// given.
var instructionPaths = regexp.MustCompile(`^(?:skills/|packages/daemon/internal/prompts/|packages/pi-legion/agents/)`)

// requireNoInstructionMovesAnAnswerOutOfTheSpec runs the second scan over every file an agent is
// given (instructionPaths), printing every file it read, every sentence it accepted, the excluded
// path with its reason and every sentence there it would otherwise refuse, and fails on every
// sentence it refuses elsewhere, each with its file and its line.
func requireNoInstructionMovesAnAnswerOutOfTheSpec(t *testing.T, root string) {
	var files []string
	for _, name := range checkoutFiles(t, root) {
		if instructionPaths.MatchString(name) && !strings.HasSuffix(name, "_test.go") {
			files = append(files, name)
		}
	}
	scan := scanInstructions(t, root, files)
	for _, read := range scan.perFile {
		t.Logf("read %s", read)
	}
	t.Logf("the second scan read %d text files an agent is given, %d sentences, and passed over %d binary files", scan.read, scan.sentences, scan.binary)
	if scan.read == 0 || scan.sentences == 0 {
		t.Fatalf("the second scan read %d files and %d sentences, so it proves nothing", scan.read, scan.sentences)
	}
	if !slices.Contains(files, excludedInstructionPath) {
		t.Errorf("%s, the path the second scan excludes, is no file of the checkout: the exclusion names a path that is gone", excludedInstructionPath)
	}
	t.Logf("excluded from the second scan: %s, because it carries this tree's own replacement prohibition, which holds a word of each set as a correct prohibition does; the first scan still reads it. The %d sentences of it the scan would otherwise refuse:", excludedInstructionPath, len(scan.excluded))
	for _, held := range scan.excluded {
		t.Logf("  would refuse %s", held)
	}
	t.Logf("%d sentences hold a word for a settled answer and a word for taking it away but no word for the document, and are accepted:", len(scan.accepted))
	for _, held := range scan.accepted {
		t.Logf("  accepted %s", held)
	}
	for _, held := range scan.refused {
		t.Logf("refused %s", held)
	}
	if len(scan.refused) != 0 {
		t.Errorf("%d sentences of the files an agent is given hold a word for a person's settled answer, a word for taking it away and a word for the document it would leave, each printed above as refused; if one states the prohibition, exclude its path as %s is excluded, and never widen the words",
			len(scan.refused), excludedInstructionPath)
	}
}

// requireNoDecisionsStepInTheStepLists reads the daemon's design-step list, record.DesignSteps,
// from its source, and the server's, model.GatedStepsKnown, as this package holds it, and prints
// each entry: neither holds a decisions step.
func requireNoDecisionsStepInTheStepLists(t *testing.T, root string) {
	steps := daemonDesignSteps(t, filepath.Join(root, "packages/daemon/internal/record"))
	t.Logf("record.DesignSteps in packages/daemon/internal/record holds %d steps: %v", len(steps), steps)
	t.Logf("model.GatedStepsKnown holds %d steps: %v", len(GatedStepsKnown), GatedStepsKnown)
	if len(steps) == 0 || len(GatedStepsKnown) == 0 {
		t.Fatalf("a step list read empty, so it proves nothing")
	}
	for _, list := range []struct {
		name  string
		steps []string
	}{{"record.DesignSteps", steps}, {"model.GatedStepsKnown", GatedStepsKnown}} {
		for index, step := range list.steps {
			if step == "decisions" {
				t.Errorf("%s[%d] is %q: the decisions step stands in that list", list.name, index, step)
			}
		}
		if !slices.Contains(list.steps, "spec") {
			t.Errorf("%s holds no spec step: %v", list.name, list.steps)
		}
	}
}

// daemonDesignSteps is the value of `var DesignSteps` in the Go files of dir, each entry resolved
// to its string: a string literal, a conversion of one, or a constant of the package.
func daemonDesignSteps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	files := token.NewFileSet()
	constants := map[string]string{}
	var list *ast.CompositeLit
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, filepath.Join(dir, entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declared := range parsed.Decls {
			general, ok := declared.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range value.Names {
					if index >= len(value.Values) {
						continue
					}
					if general.Tok == token.CONST {
						if text, ok := stringValue(value.Values[index], nil); ok {
							constants[name.Name] = text
						}
					}
					if general.Tok == token.VAR && name.Name == "DesignSteps" {
						if composite, ok := value.Values[index].(*ast.CompositeLit); ok {
							list = composite
						}
					}
				}
			}
		}
	}
	if list == nil {
		t.Fatalf("%s declares no var DesignSteps as a composite literal", dir)
	}
	var steps []string
	for _, element := range list.Elts {
		text, ok := stringValue(element, constants)
		if !ok {
			t.Fatalf("an entry of DesignSteps in %s is no string this test can read: %T", dir, element)
		}
		steps = append(steps, text)
	}
	return steps
}

// stringValue resolves expression to a string: a string literal, a conversion of one to a named
// type, or a constant constants holds.
func stringValue(expression ast.Expr, constants map[string]string) (string, bool) {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(expression.Value)
		return text, err == nil
	case *ast.CallExpr:
		if len(expression.Args) == 1 {
			return stringValue(expression.Args[0], constants)
		}
	case *ast.Ident:
		text, ok := constants[expression.Name]
		return text, ok
	}
	return "", false
}

// requireTheDaemonRefusesTheDecisionsStep builds the daemon's legion from this checkout and runs
// `legion config check` over a deployment configuration whose gates.design holds decisions, which
// it must refuse, naming gates.design and asking for spec, and over one that gates spec, which it
// must take, so a daemon that refuses every configuration fails too.
func requireTheDaemonRefusesTheDecisionsStep(t *testing.T, root string) {
	daemon := filepath.Join(root, "packages", "daemon")
	legion := cmdtest.BuildCommand(t, cmdtest.Command{Name: "legion", Dir: daemon, Target: "./cmd/legion"})
	t.Logf("legion built from %s/cmd/legion at %s", daemon, legion)
	work := t.TempDir()
	writeScratchFile(t, work, "config/operator-token", "operator\n")
	if err := os.Chmod(filepath.Join(work, "config", "operator-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The daemon names the agent harness it launches by an absolute path; nothing is launched
	// here, so a stand-in that does nothing is that path.
	writeScratchFile(t, work, "config/omp", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(work, "config", "omp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	check := func(name, steps string) (string, bool) {
		t.Helper()
		configuration := fmt.Sprintf("project: GONE\nport: 1\npostgres_dsn: postgres://legion@127.0.0.1:1/legion\nstate_dir: %s\noperator_token_file: ./operator-token\ngates:\n  design: [%s]\n",
			filepath.Join(work, "state"), steps)
		writeScratchFile(t, work, "config/"+name+".yaml", configuration)
		command := exec.Command(legion, "config", "check", "--config", filepath.Join(work, "config", name+".yaml"))
		command.Dir = filepath.Join(work, "config")
		// Only a path, a home and the stand-in reach it, so no LEGION_ setting of the calling
		// shell decides what it reads.
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "XDG_STATE_HOME=" + filepath.Join(work, "state"),
			"LEGION_OMP_PATH=" + filepath.Join(work, "config", "omp")}
		output, err := command.CombinedOutput()
		t.Logf("legion config check, gates.design: [%s], exited %v:\n%s", steps, err, output)
		return string(output), err == nil
	}
	if output, ok := check("spec", "spec"); !ok || !strings.Contains(output, "Config OK: ") {
		t.Fatalf("legion config check refuses a configuration gating spec, so a refusal of decisions would prove nothing: %s", output)
	}
	output, ok := check("decisions", "decisions")
	if ok {
		t.Fatalf("legion config check takes a configuration whose gates.design holds decisions: %s", output)
	}
	for _, words := range []string{"gates.design", "'decisions'", "gate 'spec'"} {
		if !strings.Contains(output, words) {
			t.Errorf("the refusal of a gates.design holding decisions does not say %q: %s", words, output)
		}
	}
}
