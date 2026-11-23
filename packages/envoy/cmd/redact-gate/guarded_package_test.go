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
func (v Value) Interface() any               { return nil }
`

// libStub stands for a library whose handle the read reflects on: an exported type to name its
// package by, the interface its unexported handle hides behind, and the handle, which keeps the
// bool the read wants in an unexported field, as nats.go's legacy KV handle keeps useDirect.
const libStub = `package lib

type Conn struct{ n int }

type KeyValue interface{ Bucket() string }

type kvs struct{ useDirect bool }
`

// libWithoutFieldStub is a library none of whose struct types has the field the read names.
const libWithoutFieldStub = `package lib

type Conn struct{ n int }

type KeyValue interface{ Bucket() string }
`

// libInterfaceFieldStub keeps the field the read names as an interface, which can hold the box.
const libInterfaceFieldStub = `package lib

type Conn struct{ n int }

type KeyValue interface{ Bucket() string }

type kvs struct{ useDirect any }
`

// libPromotedFieldStub keeps it as an interface in an embedded struct, which FieldByName finds.
const libPromotedFieldStub = `package lib

type Conn struct{ n int }

type KeyValue interface{ Bucket() string }

type options struct{ useDirect []any }

type kvs struct{ options }
`

// libLocalTypeStub declares, inside a function, a struct type that keeps it as an interface: a
// type no export data holds, whose values still name lib as their package.
const libLocalTypeStub = libStub + `
func Wrap(v any) any {
	type held struct{ useDirect any }
	return &held{useDirect: v}
}
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
	var libSource *checkedSource
	for _, stub := range []struct{ path, source string }{{"reflect", reflectStub}, {"example.com/lib", lib}, {"example.com/box", boxStub}} {
		f, err := parser.ParseFile(fset, stub.path+".go", stub.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		stubInfo := &types.Info{Defs: map[*ast.Ident]types.Object{}}
		pkg, err := (&types.Config{Importer: stubs}).Check(stub.path, fset, []*ast.File{f}, stubInfo)
		if err != nil {
			t.Fatal(err)
		}
		stubs[stub.path] = pkg
		if stub.path == "example.com/lib" {
			libSource = &checkedSource{pkg: pkg, files: []*ast.File{f}, info: stubInfo}
		}
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
	// The guarded package as the census type-checks it from source (sourceChecked).
	g.declared = map[string]*checkedSource{"example.com/lib": libSource}
	var c census
	g.structural(&c, "/repo", "example.com/pkg", fset, []*ast.File{f}, info)
	if len(c.Reflect) != 1 {
		t.Fatalf("the census listed %d reflection sites, want the one FieldByName: %+v", len(c.Reflect), c.Reflect)
	}
	return c.Reflect[0]
}

// TestAGuardedPackageEntryPassesOnlyAReadItsGuardHoldsToACleanPackage: a guarded_package entry
// passes main's read of a library handle's unexported field, behind an interface, only where it is
// a FieldByName with a constant name whose result is only a method call's receiver (Elem and
// Interface aside), its guard holds the handle's type to the named package through an unexported
// variable, that package's import closure holds neither the box's package nor an external error
// type's, it declares no generic type, and every struct type it declares with that field types it
// with no interface or type parameter at any depth; every other shape is refused, each with its
// reason, and with no entry the site is refused as before.
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
		{"FieldByIndex in place of FieldByName", edit(`kvs.FieldByName("useDirect")`, "kvs.FieldByIndex([]int{0})"), entry, clean, false, "admits a reflect.Value.FieldByName with a constant name alone"},
		{"a field name that is no constant", edit(`kvs.FieldByName("useDirect")`, "kvs.FieldByName(fieldName)") + "\nvar fieldName = \"useDirect\"\n", entry, clean, false, "whose name is a constant"},
		{"Elem on the result", edit("return useDirect.Bool()", "return useDirect.Elem().Bool()"), entry, clean, false, "its result's Elem reaches what the field holds"},
		{"Interface on the result", edit("return useDirect.Bool()", "return useDirect.Interface() != nil"), entry, clean, false, "its result's Interface reaches what the field holds"},
		{"the result passed on", edit("return useDirect.Bool()", "return use(useDirect)") + "\nfunc use(reflect.Value) bool { return false }\n", entry, clean, false, "used 1 times, 0 of them as a method call's receiver"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := guardedCensus(t, c.source, c.entry, c.closure)
			if got.OK != c.ok || !strings.Contains(got.Why, c.why) {
				t.Fatalf("the census answered ok=%t %q at %s:%d; want ok=%t and %q in its reason", got.OK, got.Why, got.File, got.Line, c.ok, c.why)
			}
		})
	}
	// The guarded package's own types, read from its source: a generic type's instance is lib's to
	// reflect and holds whatever its type argument is, so the guard holds lib.Holder[box.Text] to
	// lib while lib's import closure shows no box; a field the read names that is an interface, or
	// holds one, can hold the box at run time, whether the struct type keeping it declares it or
	// embeds it, at package level or in a function; and a package none of whose struct types has
	// that field leaves the census nothing to read.
	fieldByName := strings.Replace(strings.Replace(genericRead, "kvs.FieldByIndex([]int{0, 0})", `kvs.FieldByName("V")`, 1), "text.IsNil()", "text.Bool()", 1)
	for _, c := range []struct{ name, lib, source, why string }{
		{"a generic type of the guarded package holds the box, read by FieldByIndex", genericLibStub, genericRead, "admits a reflect.Value.FieldByName with a constant name alone"},
		{"a generic type of the guarded package holds the box, read by FieldByName", genericLibStub, fieldByName, "which declares the generic type Holder"},
		{"the guarded package declares a generic type", genericLibStub, guardedRead, "which declares the generic type Holder"},
		{"the field is an interface", libInterfaceFieldStub, guardedRead, "types the field useDirect with an interface or a type parameter, which can hold the box at run time: kvs.useDirect (any)"},
		{"the field holds interfaces, promoted from an embedded struct", libPromotedFieldStub, guardedRead, "kvs.useDirect ([]any)"},
		{"a struct type declared in a function keeps the field as an interface", libLocalTypeStub, guardedRead, "held.useDirect (any)"},
		{"no struct type of the package has the field", libWithoutFieldStub, guardedRead, "no struct type example.com/lib declares has a field useDirect"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := guardedCensusOf(t, c.lib, c.source, entry, clean)
			if got.OK || !strings.Contains(got.Why, c.why) {
				t.Fatalf("the census answered ok=%t %q at %s:%d; want ok=false and %q in its reason", got.OK, got.Why, got.File, got.Line, c.why)
			}
		})
	}
}
