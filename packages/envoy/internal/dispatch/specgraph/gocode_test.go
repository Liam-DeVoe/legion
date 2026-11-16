package specgraph

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// citationsRepo is the Go module of the tests below: package a holds TestA, package b holds TestB in
// both package b and package b_test, and package c imports a. GOWORK=off, because the repository's
// go.work, which the go command finds above this directory, does not use the module.
var citationsRepo = GoCode{Dir: "testdata/citations/repo", Env: []string{"GOWORK=off"}}

// Find counts the top-level tests of the selector's name that the real go command lists in the
// selector's package (R48: "a check whose test is not in the code at that commit").
func TestGoCodeFindsTests(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		selector string
		want     int
	}{
		// A method of the same name in a_test.go is no test: go test runs top-level funcs only.
		{"example.com/citations/a TestA", 1},
		{"example.com/citations/a TestRenamed", 0},
		{"example.com/citations/b TestB", 2},
		// c has no test file.
		{"example.com/citations/c TestA", 0},
	} {
		got, err := citationsRepo.Find(Test{Runner: "go", Selector: row.selector, Job: "go-checks"})
		if err != nil || got != row.want {
			t.Errorf("Find(%q) = %d, %v; want %d, nil", row.selector, got, err, row.want)
		}
	}

	// The control that the setting matters: without GOWORK=off the go command reads the
	// repository's go.work, which does not use the module, so it finds no package and Find fails
	// rather than reading a count of 0.
	if got, err := (GoCode{Dir: citationsRepo.Dir}).Find(Test{Runner: "go", Selector: "example.com/citations/a TestA", Job: "go-checks"}); err == nil {
		t.Errorf("Find without GOWORK=off = %d, nil; want an error", got)
	}
}

// A package that the module does not hold, a pattern of several packages, a runner other than go
// and a selector whose path starts with "-" each give an error and no count. Run in package a's own
// directory, the selector "-e TestA" would count a's TestA if the go command read "-e" as its flag.
func TestGoCodeFindRefusesWhatItCannotLookUp(t *testing.T) {
	t.Parallel()
	for _, selector := range []string{"example.com/citations/gone TestA", "example.com/citations/... TestA"} {
		if got, err := citationsRepo.Find(Test{Runner: "go", Selector: selector, Job: "go-checks"}); err == nil {
			t.Errorf("Find(%q) = %d, nil; want an error", selector, got)
		}
	}
	if got, err := citationsRepo.Find(Test{Runner: "bun", Selector: "src/a.test.ts", Job: "go-checks"}); !errors.Is(err, ErrSelector) {
		t.Errorf("Find with runner bun = %d, %v; want an error wrapping ErrSelector", got, err)
	}
	inA := GoCode{Dir: "testdata/citations/repo/a", Env: []string{"GOWORK=off"}}
	if got, err := inA.Find(Test{Runner: "go", Selector: "-e TestA", Job: "go-checks"}); err == nil {
		t.Errorf("Find(%q) = %d, nil; want an error: go read the path as a flag", "-e TestA", got)
	}
}

// Find counts a func of the test's name only when go test would run it: one parameter *T or
// *<package>.T and no result, as cmd/go's isTestFunc reads it.
func TestGoTestSignature(t *testing.T) {
	t.Parallel()
	const source = `package a

import (
	"testing"
	tt "testing"
)

func TestA(t *testing.T)            {}
func TestB(t *tt.T)                 {}
func TestC(*testing.T)              {}
func TestD(t *testing.B)            {}
func TestE(t testing.T)             {}
func TestF(t *testing.T, n int)     {}
func TestG(t, u *testing.T)         {}
func TestH(t *testing.T) error      { return nil }
func TestI()                        {}
`
	file, err := parser.ParseFile(token.NewFileSet(), "a_test.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"TestA": true, "TestB": true, "TestC": true}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if got := goTestSignature(fn); got != want[fn.Name.Name] {
				t.Errorf("goTestSignature(%s) = %v; want %v", fn.Name.Name, got, want[fn.Name.Name])
			}
		}
	}
}
