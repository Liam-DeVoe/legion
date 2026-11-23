package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// reflectStub is the part of package reflect a guarded read calls, so the census reads the same
// callee names (types.Func.FullName) it reads in a real tree.
const reflectStub = `package reflect

type Kind uint

const (
	Invalid Kind = iota
	Pointer
	Struct
)

type Type interface{ PkgPath() string }

type Value struct{ p *int }

func ValueOf(i any) Value                    { return Value{} }
func TypeFor[T any]() Type                   { return nil }
func (v Value) Kind() Kind                   { return Invalid }
func (v Value) IsNil() bool                  { return false }
func (v Value) Elem() Value                  { return v }
func (v Value) Type() Type                   { return nil }
func (v Value) FieldByName(name string) Value { return v }
func (v Value) FieldByIndex(index []int) Value { return v }
func (v Value) Bool() bool                   { return false }
`

// libStub stands for a library whose handle the read reflects on: an exported type to name its
// package by, and the interface its unexported handle hides behind.
const libStub = `package lib

type Conn struct{ n int }

type KeyValue interface{ Bucket() string }
`

// genericLibStub is libStub with one generic type, as listenerwire declares APIErrorAs[Text]: a
// type of the guarded package whose instance holds whatever its type argument is.
const genericLibStub = libStub + `
type Holder[T any] struct{ V T }
`

// boxStub stands for package redact: the box keeps its text in an unexported field.
const boxStub = `package box

type Text struct{ text *string }
`

// genericRead holds a box in the guarded package's generic type and reads the box's storage under
// the guard main's readsDirect uses: an instance of lib.Holder[box.Text] is lib's to reflect, so
// the guard holds it to lib's package, while lib's import closure holds no box.
const genericRead = `package pkg

import (
	"reflect"

	"example.com/box"
	"example.com/lib"
)

var libPackage = reflect.TypeFor[lib.Conn]().PkgPath()

func readsDirect(held lib.Holder[box.Text]) bool {
	kvs := reflect.ValueOf(held)
	if kvs.Kind() != reflect.Struct || kvs.Type().PkgPath() != libPackage {
		return true
	}
	text := kvs.FieldByIndex([]int{0, 0})
	return text.IsNil()
}
`

// guardedRead is main's readsDirect (internal/bus/kv.go) over the stubs: the handle's type is
// held to lib's package by its PkgPath guard before its unexported field is read.
const guardedRead = `package pkg

import (
	"reflect"

	"example.com/lib"
)

var libPackage = reflect.TypeFor[lib.Conn]().PkgPath()

func readsDirect(legacy lib.KeyValue) bool {
	handle := reflect.ValueOf(legacy)
	if handle.Kind() != reflect.Pointer || handle.IsNil() {
		return true
	}
	kvs := handle.Elem()
	if kvs.Kind() != reflect.Struct || kvs.Type().PkgPath() != libPackage {
		return true
	}
	useDirect := kvs.FieldByName("useDirect")
	return useDirect.Bool()
}
`

// stubImporter answers the stub packages by path.
type stubImporter map[string]*types.Package

func (s stubImporter) Import(path string) (*types.Package, error) {
	if p, ok := s[path]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("no stub of %s", path)
}

// guardedCensus runs the census over source, package example.com/pkg in /repo/pkg/kv.go, with
// entry its one reflection entry (none where nil) and closure lib's import closure (none recorded
// where nil), and answers the one reflection finding it lists. lib is libStub.
func guardedCensus(t *testing.T, source string, entry *reflectionExempt, closure map[string]bool) finding {
	t.Helper()
	return guardedCensusOf(t, libStub, source, entry, closure)
}

// guardedCensusOf is guardedCensus with lib's source given.
func guardedCensusOf(t *testing.T, lib, source string, entry *reflectionExempt, closure map[string]bool) finding {
	t.Helper()
	fset := token.NewFileSet()
	stubs := stubImporter{}
	for _, stub := range []struct{ path, source string }{{"reflect", reflectStub}, {"example.com/lib", lib}, {"example.com/box", boxStub}} {
		f, err := parser.ParseFile(fset, stub.path+".go", stub.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := (&types.Config{Importer: stubs}).Check(stub.path, fset, []*ast.File{f}, nil)
		if err != nil {
			t.Fatal(err)
		}
		stubs[stub.path] = pkg
	}
	f, err := parser.ParseFile(fset, "/repo/pkg/kv.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Types:      map[ast.Expr]types.TypeAndValue{},
		Instances:  map[*ast.Ident]types.Instance{},
	}
	if _, err := (&types.Config{Importer: stubs}).Check("example.com/pkg", fset, []*ast.File{f}, info); err != nil {
		t.Fatalf("the case does not type-check: %v", err)
	}
	g := &gate{}
	g.inv.Gate.Box = boxConfig{Type: "example.com/box.Text", Field: "text"}
	g.inv.Gate.External = []string{"example.com/oauth.RetrieveError"}
	if entry != nil {
		g.inv.Gate.Reflection = []reflectionExempt{*entry}
	}
	if closure != nil {
		g.imports = map[string]map[string]bool{"example.com/lib": closure}
	}
	var c census
	g.structural(&c, "/repo", "example.com/pkg", fset, []*ast.File{f}, info)
	if len(c.Reflect) != 1 {
		t.Fatalf("the census listed %d reflection sites, want the one FieldByName: %+v", len(c.Reflect), c.Reflect)
	}
	return c.Reflect[0]
}

// TestAGuardedPackageEntryPassesOnlyAReadItsGuardHoldsToACleanPackage: a guarded_package entry
// passes main's read of a library handle's unexported field, behind an interface, only where its
// guard holds the handle's type to the named package, that package's import closure holds
// neither the box's package nor an external error type's, and it declares no generic type; every
// other shape is refused, each with its reason, and with no entry the site is refused as before.
func TestAGuardedPackageEntryPassesOnlyAReadItsGuardHoldsToACleanPackage(t *testing.T) {
	entry := &reflectionExempt{File: "pkg/kv.go", Function: "example.com/pkg.readsDirect", GuardedPackage: "example.com/lib", Why: "test"}
	clean := map[string]bool{"example.com/lib": true, "fmt": true}
	edit := func(old, new string) string {
		if !strings.Contains(guardedRead, old) {
			t.Fatalf("the case's edit finds no %q in the read", old)
		}
		return strings.Replace(guardedRead, old, new, 1)
	}
	const guard = "kvs.Kind() != reflect.Struct || kvs.Type().PkgPath() != libPackage"
	for _, c := range []struct {
		name    string
		source  string
		entry   *reflectionExempt
		closure map[string]bool
		ok      bool
		why     string
	}{
		{"the guard through a package variable", guardedRead, entry, clean, true, "its guard holds its value's type to example.com/lib"},
		{"the guard in place", edit("!= libPackage", "!= reflect.TypeFor[lib.Conn]().PkgPath()"), entry, clean, true, "its guard holds its value's type to example.com/lib"},
		{"no entry", guardedRead, nil, clean, false, "no inventory entry names this site"},
		{"no PkgPath guard", edit(guard, "kvs.Kind() != reflect.Struct"), entry, clean, false, "does not run only under a guard"},
		{"the guard does not return", edit("!= libPackage {\n\t\treturn true\n\t}", "!= libPackage {\n\t\tprintln()\n\t}"), entry, clean, false, "does not run only under a guard"},
		{"the receiver stored again after the guard", edit("\tuseDirect :=", "\tkvs = handle\n\tuseDirect :="), entry, clean, false, "stored to 2 times"},
		{"the receiver's address taken", edit("\tuseDirect :=", "\t_ = &kvs\n\tuseDirect :="), entry, clean, false, "stored to 2 times"},
		{"the guard's variable stored again", guardedRead + "\nfunc init() { libPackage = \"example.com/lib\" }\n", entry, clean, false, "does not run only under a guard"},
		{"the guard's variable is exported, so another package can store to it", strings.ReplaceAll(guardedRead, "libPackage", "LibPackage"), entry, clean, false, "does not run only under a guard"},
		{"a label in the function", edit("\treturn useDirect.Bool()", "\tif false {\n\t\tgoto done\n\t}\ndone:\n\treturn useDirect.Bool()"), entry, clean, false, "holds a label"},
		{"a pointer type names no package", edit("reflect.TypeFor[lib.Conn]()", "reflect.TypeFor[*lib.Conn]()"), entry, clean, false, "does not run only under a guard"},
		{"the entry names another package", guardedRead, &reflectionExempt{File: entry.File, Function: entry.Function, GuardedPackage: "example.com/other", Why: "test"}, clean, false, "the guard here holds the value's type to example.com/lib"},
		{"the closure holds the box", guardedRead, entry, map[string]bool{"example.com/lib": true, "example.com/box": true}, false, "holds the box's package example.com/box"},
		{"the closure holds an external error type's package", guardedRead, entry, map[string]bool{"example.com/lib": true, "example.com/oauth": true}, false, "the external error type example.com/oauth.RetrieveError"},
		{"no closure listed", guardedRead, entry, nil, false, "whose import closure no go list of these builds names"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := guardedCensus(t, c.source, c.entry, c.closure)
			if got.OK != c.ok || !strings.Contains(got.Why, c.why) {
				t.Fatalf("the census answered ok=%t %q at %s:%d; want ok=%t and %q in its reason", got.OK, got.Why, got.File, got.Line, c.ok, c.why)
			}
		})
	}
	// A generic type of the guarded package: an instance is lib's to reflect and holds whatever its
	// type argument is, so the guard holds lib.Holder[box.Text] to lib while lib's import closure
	// shows no box. A package that declares one is refused for every read under its guard.
	for _, c := range []struct{ name, source string }{
		{"a generic type of the guarded package holds the box", genericRead},
		{"the guarded package declares a generic type", guardedRead},
	} {
		t.Run(c.name, func(t *testing.T) {
			const why = "which declares the generic type Holder"
			got := guardedCensusOf(t, genericLibStub, c.source, entry, clean)
			if got.OK || !strings.Contains(got.Why, why) {
				t.Fatalf("the census answered ok=%t %q at %s:%d; want ok=false and %q in its reason", got.OK, got.Why, got.File, got.Line, why)
			}
		})
	}
}
