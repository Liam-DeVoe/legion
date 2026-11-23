package corpustest_test

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// corpusVariables are the variables that hand a corpus test of internal/dispatch its directory of
// markdown documents.
var corpusVariables = []string{
	"DISPATCH_RENDER_CORPUS",
	"DISPATCH_TEST_SPEC_CORPUS",
	"DISPATCH_TOKEN_CORPUS",
	"PMDOC_ANCHOR_CORPUS",
	"PMDOC_WALK_CORPUS",
}

// helperReads are the calls of package corpustest that read a corpus variable.
var helperReads = []string{"Open", "Optional"}

// TestCorpusVariablesAreReadThroughTheHelper lists every _test.go file of the module that names a
// corpus variable and what each does with it, and fails for a file that reads one with os.Getenv
// or os.LookupEnv rather than through corpustest.Open or corpustest.Optional: such a test would
// pick its own documents and leave out what it likes. A Benchmark in a file whose first 15 lines
// carry the tool line (`legion: tool - `) reads the variable its runner hands it and is listed as
// a tool. It also fails when a corpus variable reaches no test through the helper.
func TestCorpusVariablesAreReadThroughTheHelper(t *testing.T) {
	root := moduleRoot(t)
	files := 0
	throughHelper := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (name == "testdata" || name == "node_modules" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(corpusVariables, func(variable string) bool { return bytes.Contains(source, []byte(variable)) }) {
			return nil
		}
		files++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, use := range corpusUses(t, path, source) {
			if use.via == "helper" {
				throughHelper[use.variable] = true
			}
			if use.problem != "" {
				t.Errorf("%s: %s", rel, use.problem)
				continue
			}
			t.Logf("%s: %s", rel, use.describe())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	t.Logf("%d test files name a corpus variable", files)
	for _, variable := range corpusVariables {
		if !throughHelper[variable] {
			t.Errorf("no test reads %s through corpustest.Open or corpustest.Optional", variable)
		}
	}
}

// corpusUse is what one test file does with one corpus variable: reads it through the helper, in
// a tool's Benchmark, names it alone, or reads it some other way (problem).
type corpusUse struct {
	variable string
	via      string // "helper", "tool" or "named"
	function string
	problem  string
}

func (use corpusUse) describe() string {
	switch use.via {
	case "helper":
		return fmt.Sprintf("%s read through the helper in %s", use.variable, use.function)
	case "tool":
		return fmt.Sprintf("%s read by %s, a Benchmark of a tool file", use.variable, use.function)
	}
	return fmt.Sprintf("%s named, never read", use.variable)
}

// corpusUses parses one test file and lists what it does with each corpus variable it names.
func corpusUses(t *testing.T, path string, source []byte) []corpusUse {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	constants := packageConstants(t, filepath.Dir(path))
	helper := ""
	for _, spec := range file.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		if strings.HasSuffix(importPath, "/internal/dispatch/corpustest") {
			helper = "corpustest"
			if spec.Name != nil {
				helper = spec.Name.Name
			}
		}
	}
	tool := toolFile(source)
	// name is the variable a call's argument spells, a string literal or a constant of the
	// package; known is false for any other expression.
	name := func(argument ast.Expr) (string, bool) {
		switch argument := argument.(type) {
		case *ast.BasicLit:
			value, err := strconv.Unquote(argument.Value)
			return value, err == nil
		case *ast.Ident:
			value, ok := constants[argument.Name]
			return value, ok
		}
		return "", false
	}
	var uses []corpusUse
	read := map[string]bool{}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			at := fileSet.Position(call.Pos()).Line
			switch {
			case receiver.Name == "os" && (selector.Sel.Name == "Getenv" || selector.Sel.Name == "LookupEnv") && len(call.Args) == 1:
				variable, known := name(call.Args[0])
				switch {
				case !known:
					uses = append(uses, corpusUse{function: function.Name.Name, problem: fmt.Sprintf(
						"line %d: %s reads os.%s of a variable it does not spell, in a file that names a corpus variable",
						at, function.Name.Name, selector.Sel.Name)})
				case !slices.Contains(corpusVariables, variable):
				case tool && strings.HasPrefix(function.Name.Name, "Benchmark"):
					read[variable] = true
					uses = append(uses, corpusUse{variable: variable, via: "tool", function: function.Name.Name})
				default:
					read[variable] = true
					uses = append(uses, corpusUse{variable: variable, function: function.Name.Name, problem: fmt.Sprintf(
						"line %d: %s reads %s with os.%s, not through corpustest.Open or corpustest.Optional",
						at, function.Name.Name, variable, selector.Sel.Name)})
				}
			case helper != "" && receiver.Name == helper && slices.Contains(helperReads, selector.Sel.Name) && len(call.Args) >= 2:
				if variable, known := name(call.Args[1]); known && slices.Contains(corpusVariables, variable) {
					read[variable] = true
					uses = append(uses, corpusUse{variable: variable, via: "helper", function: function.Name.Name})
				}
			}
			return true
		})
	}
	for _, variable := range corpusVariables {
		if !read[variable] && bytes.Contains(source, []byte(variable)) {
			uses = append(uses, corpusUse{variable: variable, via: "named"})
		}
	}
	return uses
}

// toolFile reports whether a file's first 15 lines carry the tool line.
func toolFile(source []byte) bool {
	lines := bytes.SplitN(source, []byte("\n"), 16)
	for _, line := range lines[:min(len(lines), 15)] {
		if bytes.Contains(line, []byte("legion: tool - ")) {
			return true
		}
	}
	return false
}

// packageConstants are the string constants the Go files of a directory declare, by name.
func packageConstants(t *testing.T, directory string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	constants := map[string]string{}
	fileSet := token.NewFileSet()
	for _, path := range paths {
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				values := spec.(*ast.ValueSpec)
				for index, constant := range values.Names {
					if index >= len(values.Values) {
						continue
					}
					if literal, ok := values.Values[index].(*ast.BasicLit); ok && literal.Kind == token.STRING {
						if value, err := strconv.Unquote(literal.Value); err == nil {
							constants[constant.Name] = value
						}
					}
				}
			}
		}
	}
	return constants
}

// moduleRoot is the directory of the go.mod above the test's own directory: packages/envoy.
func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("no go.mod above the test's directory")
		}
		directory = parent
	}
}
