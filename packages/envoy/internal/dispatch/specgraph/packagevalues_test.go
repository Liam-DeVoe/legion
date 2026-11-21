package specgraph

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// writerCalled is the one guard of what code other than the reader can reach of what the reader
// and the sweeps read: a writer of a sweep or of a base, which changedFile, filesWritten and
// editTree each call it with, and the sweeps' setup, which setUp calls it with. who names the
// writer or the setup ("a writer of results.json"), and base is the base that the writer's input
// is made from, or the base that it writes a file of (nil for the setup, which holds no base yet).
// It takes a copy of each variable of the package (packageValuesNow) and of each file of base, then
// builds the writer (build), then calls it (call), then compares each variable and each file with
// its copy (packageValuesKept, filesKept), and gives what call gave. Each caller hands it a
// builder, not a writer, so no code of a writer runs before the copy; and call turns what the
// writer gives and what it leaves (its tree, its files, its copy of the change, the bases that the
// setup built) into bytes of the caller's own before the compare, so no code of a writer runs after
// it: the type of what call gives is plain data (plainType), with no interface, function, channel
// or pointer, where a method of a value that the writer left, such as its MarshalJSON, could run.
// So a writer or a setup that changes a variable of the package or a file of the base, in the code
// that builds it, in the call, or in a method of a value that it leaves in what it gives, fails by
// name, as one that changes a table of the reader (relationKinds, slug, ...) or the base would
// change the verdict of each later input. Code that changes a variable outside every such call
// fails the test process by name (TestMain), and a base that no longer holds what a separate build
// of it holds fails the sweep that reads it by name (sweepAgainst).
func writerCalled[W, R any](t *testing.T, who string, base fstest.MapFS, build func() W, call func(W) R) R {
	t.Helper()
	if at, plain := plainType(reflect.TypeFor[R](), "", map[reflect.Type]bool{}); !plain {
		t.Fatalf("the guard of %s hands back a %v, whose %s is no plain data, so a method of a value that the writer left there could run after the compare", who, reflect.TypeFor[R](), at)
	}
	values, err := packageValuesNow()
	if err != nil {
		t.Fatalf("%s: %v", who, err)
	}
	files := cloneFiles(base)
	got := call(build())
	packageValuesKept(t, who, values)
	filesKept(t, who, base, files)
	return got
}

// filesKept fails the test unless base, the base that who reads or writes a file of, holds what
// before, the copy that writerCalled took before who was built, holds: each file by its name, its
// bytes, and no mode, time or sys where the copy has none, and no other file.
func filesKept(t *testing.T, who string, base, before fstest.MapFS) {
	t.Helper()
	for _, name := range slices.Sorted(maps.Keys(before)) {
		f, held := base[name]
		if b := before[name]; !held || f == nil || !bytes.Equal(f.Data, b.Data) || f.Mode != b.Mode || !f.ModTime.Equal(b.ModTime) || (f.Sys == nil) != (b.Sys == nil) {
			t.Fatalf("%s changed the file %s of the base that it was handed a copy of (held %t), where no writer may change a base", who, name, held)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(base)) {
		if _, held := before[name]; !held {
			t.Fatalf("%s added the file %s to the base that it was handed a copy of, where no writer may change a base", who, name)
		}
	}
}

// TestMain copies each variable of the package (packageValuesNow) before the first test of the
// process runs, and compares each with its copy after the last one has ended, so that code that
// changes one outside every call of the guard (writerCalled), such as the code of a test, a
// cleanup or a goroutine, fails the process by the variable's name, whatever its tests say.
func TestMain(m *testing.M) {
	start, err := packageValuesNow()
	if err != nil {
		fmt.Println("TestMain: " + err.Error())
		os.Exit(1)
	}
	code := m.Run()
	for _, changed := range packageValuesChanged("the test process", start) {
		fmt.Println("TestMain: " + changed)
		code = max(code, 1)
	}
	os.Exit(code)
}

// plainType is whether a value of the type t is plain data, on which no method that a writer
// chose can run: a bool, a number or a string, or a slice, an array, a map or a struct of plain
// data; and else the first place of t that is no plain data (an interface, a function, a channel
// or a pointer), by its path from at. seen holds the structs on the path, so a type that holds
// itself ends.
func plainType(t reflect.Type, at string, seen map[reflect.Type]bool) (string, bool) {
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.String:
		return "", true
	case reflect.Slice, reflect.Array:
		return plainType(t.Elem(), at+"[]", seen)
	case reflect.Map:
		if p, plain := plainType(t.Key(), at+"[key]", seen); !plain {
			return p, false
		}
		return plainType(t.Elem(), at+"[]", seen)
	case reflect.Struct:
		if seen[t] {
			return "", true
		}
		seen[t] = true
		for i := range t.NumField() {
			if p, plain := plainType(t.Field(i).Type, at+"."+t.Field(i).Name, seen); !plain {
				return p, false
			}
		}
		return "", true
	}
	return fmt.Sprintf("place %q, a %v,", at, t), false
}

// packageVariable is one variable of the package, by its name and its address (packageVariables).
type packageVariable struct {
	name string
	at   any
}

// packageVariables is each variable of the package, of its code and of its tests, by its name and
// its address, which packageValuesNow copies, each by the rule of its type (valueCopy).
// TestEachVariableOfThePackageIsGuarded fails for a variable that the list does not name and for a
// variable of a type that no rule covers. Each table that a sweep reads of its own is a function
// that gives a new value at each call (sweepMaps, sweepPaths, namesOfNothing, addableMembers,
// recordKinds, sweepFloors, scenarioFileTypes, builtBases and the patterns of blockTexts), not a
// variable.
func packageVariables() []packageVariable {
	return []packageVariable{
		// The variables of the code.
		{"backtickSpan", &backtickSpan},
		{"fileEnding", &fileEnding},
		{"slashBetweenLetters", &slashBetweenLetters},
		{"endsWithCall", &endsWithCall},
		{"underscoreBetween", &underscoreBetween},
		{"lowerCamel", &lowerCamel},
		{"packageSymbol", &packageSymbol},
		{"productNames", &productNames},
		{"nameRun", &nameRun},
		{"slug", &slug},
		{"positionWord", &positionWord},
		{"gitDiffArgs", &gitDiffArgs},
		{"pointerToken", &pointerToken},
		{"blockAttributes", &blockAttributes},
		{"checkFields", &checkFields},
		{"heading", &heading},
		{"problemKinds", &problemKinds},
		{"ErrUnprintable", &ErrUnprintable},
		{"valueReferences", &valueReferences},
		{"relationKinds", &relationKinds},
		{"sentenceEnd", &sentenceEnd},
		{"scenarioRequired", &scenarioRequired},
		{"scenarioOptional", &scenarioOptional},
		{"ErrSelector", &ErrSelector},
		{"topLevelTestName", &topLevelTestName},
		// The variables of the tests: the check case from which memberScenario builds the base of
		// TestAMemberLeftOutOfAFileIsReadByItsMark, and the tables of the other tests.
		{"checkStateApproved", &checkStateApproved},
		{"checkStateRun", &checkStateRun},
		{"checkStateTop", &checkStateTop},
		{"checkStateLeaf", &checkStateLeaf},
		{"checkStateCheck", &checkStateCheck},
		{"checkStateProduction", &checkStateProduction},
		{"checkStateProductionAgent", &checkStateProductionAgent},
		{"checkStateGraph", &checkStateGraph},
		{"checkStateProductionProgram", &checkStateProductionProgram},
		{"checkStateGraphProgram", &checkStateGraphProgram},
		{"checkStateCodeAgent", &checkStateCodeAgent},
		{"checkStateCodeQuery", &checkStateCodeQuery},
		{"checkStateTie", &checkStateTie},
		{"libraryInputTypes", &libraryInputTypes},
		{"plantedFaults", &plantedFaults},
		{"statementLeafTwo", &statementLeafTwo},
		{"statementLeafTwoCheck", &statementLeafTwoCheck},
		{"statementSupporter", &statementSupporter},
		{"statementSupporterCheck", &statementSupporterCheck},
		{"statementTopCheck", &statementTopCheck},
		{"statementTopProduction", &statementTopProduction},
		{"statementFast", &statementFast},
		{"statementFastCheck", &statementFastCheck},
		{"statementLeafSupporter", &statementLeafSupporter},
		{"statementLeafSupporterCheck", &statementLeafSupporterCheck},
		{"statementSupporterOfSupporter", &statementSupporterOfSupporter},
		{"statementSupporterOfSupporterCheck", &statementSupporterOfSupporterCheck},
		{"longName", &longName},
		{"jsonLongName", &jsonLongName},
		{"none", &none},
	}
}

// packageValue is the copy of one variable of the package that packageValuesNow takes, by the
// variable's name.
type packageValue struct {
	name  string
	value any
}

// packageValuesNow is a copy of each variable of the package (packageVariables), each by the rule
// of its type (valueCopy), as plain data that shares with the variable no list, map or pointer;
// and an error that names the first variable that no rule covers.
func packageValuesNow() ([]packageValue, error) {
	vars := packageVariables()
	values := make([]packageValue, len(vars))
	for i, v := range vars {
		at := reflect.ValueOf(v.at)
		if at.Kind() != reflect.Pointer || at.IsNil() {
			return nil, fmt.Errorf("the entry of %s holds a %T, where it holds the variable's address", v.name, v.at)
		}
		c, err := valueCopy(at.Elem(), v.name, 0)
		if err != nil {
			return nil, fmt.Errorf("the guard has no copy of the variable %s: %w", v.name, err)
		}
		values[i] = packageValue{v.name, c}
	}
	return values, nil
}

// copyDepth is the depth of values at which valueCopy stops, so that a value that holds itself
// fails by its place instead of running on.
const copyDepth = 32

// valueCopy is the copy of v, a variable of the package or a part of one at the place at, by the
// rule of its type, which shares with v no list, map or pointer that code of the package can write
// through; or an error that names the place and the type where no rule covers a type:
//   - a bool, a number or a string, of a type of any package (time.Duration among them): the value,
//     which no code can change through its copy;
//   - a slice or an array: the copy of each element, nil apart from empty; a map: the copy of each
//     entry, by the copy of its key, nil apart from empty;
//   - a struct of this package, or of no name: the copy of each field by its name, unexported ones
//     too;
//   - a pointer to such a value: its address and the copy of what it points to;
//   - an interface: the copy of the value that it holds, by that value's type;
//   - a type of another package that has a rule of its own (foreignCopy): that rule.
//
// A function, a channel, an unsafe pointer, and a struct or a pointer to a struct of another
// package with no rule of its own have no rule, so a variable that holds one fails the guard and
// the census by name.
func valueCopy(v reflect.Value, at string, depth int) (any, error) {
	if depth > copyDepth {
		return nil, fmt.Errorf("%s holds values deeper than %d", at, copyDepth)
	}
	if c, ruled, err := foreignCopy(v, at); ruled {
		return c, err
	}
	t := v.Type()
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	case reflect.Complex64, reflect.Complex128:
		return v.Complex(), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return []any(nil), nil
		}
		c := make([]any, v.Len())
		for i := range v.Len() {
			x, err := valueCopy(v.Index(i), fmt.Sprintf("%s[%d]", at, i), depth+1)
			if err != nil {
				return nil, err
			}
			c[i] = x
		}
		return c, nil
	case reflect.Map:
		if v.IsNil() {
			return map[any]any(nil), nil
		}
		c := make(map[any]any, v.Len())
		for entry := v.MapRange(); entry.Next(); {
			key, err := valueCopy(entry.Key(), at+"[key]", depth+1)
			if err != nil {
				return nil, err
			}
			if !reflect.TypeOf(key).Comparable() {
				return nil, fmt.Errorf("%s is a map whose key, a %v, has no copy that can be a key", at, t.Key())
			}
			x, err := valueCopy(entry.Value(), fmt.Sprintf("%s[%v]", at, key), depth+1)
			if err != nil {
				return nil, err
			}
			c[key] = x
		}
		return c, nil
	case reflect.Struct:
		if t.Name() != "" && t.PkgPath() != ownPackage() {
			break
		}
		c := make(map[string]any, v.NumField())
		for i := range v.NumField() {
			x, err := valueCopy(v.Field(i), at+"."+t.Field(i).Name, depth+1)
			if err != nil {
				return nil, err
			}
			c[t.Field(i).Name] = x
		}
		return c, nil
	case reflect.Pointer:
		if v.IsNil() {
			return pointerCopy{}, nil
		}
		if e := t.Elem(); e.Kind() == reflect.Struct && e.Name() != "" && e.PkgPath() != ownPackage() {
			break
		}
		x, err := valueCopy(v.Elem(), at, depth+1)
		return pointerCopy{v.Pointer(), x}, err
	case reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		return valueCopy(v.Elem(), at, depth+1)
	}
	return nil, fmt.Errorf("%s holds a %v, for which the guard has no rule", at, t)
}

// ownPackage is the import path of this package.
func ownPackage() string { return reflect.TypeFor[Input]().PkgPath() }

// pointerCopy is the copy of a pointer of the package's own: its address, 0 for nil, and the copy
// of what it points to.
type pointerCopy struct {
	at    uintptr
	value any
}

// foreignCopy is the copy of v by the rule of its type where the type is one of another package
// that a variable of the package holds, and whether there is one. Each rule copies the parts of a
// value that a method of the package's code could reach or change, through the storage that the
// type's methods hand out and the fields that its methods change:
//   - a *regexp.Regexp (backtickSpan, slug, heading, ...): its address; its source text (String),
//     which a write of the whole object (*slug = *other) or UnmarshalText changes; a copy of its
//     capture names, since SubexpNames hands out the pattern's own slice, through which a write
//     changes the pattern and not its copy; and its field longest, which Longest sets and no
//     method reads;
//   - a *strings.Replacer (pointerToken, valueReferences): its address; a copy of its field
//     oldnew, the pairs of old and new strings that NewReplacer copies from its arguments and that
//     its first Replace clears when it builds its table from them; and the identity of that table
//     (its field r: the table's type and address), so a write of the whole object, built or not,
//     changes the copy. init builds both tables before the first copy, and a built table is only
//     read; Replace and WriteString hand out no storage;
//   - an error of errors.New (ErrUnprintable, ErrSelector): its address and its text (Error);
//   - a time.Time (checkStateApproved, checkStateRun): its text in RFC 3339 with nanoseconds, and
//     its location's name;
//   - a reflect.Type (the elements of libraryInputTypes): its address and its text (String).
//
// A rule that reads a field fails by the type's name where this Go's type has no such field, and a
// rule that calls a method fails where an unexported field holds the value, which reflect does not
// let it call.
func foreignCopy(v reflect.Value, at string) (any, bool, error) {
	t := v.Type()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.PkgPath() == "" || t.PkgPath() == ownPackage() {
		return nil, false, nil
	}
	name := t.PkgPath() + "." + t.Name()
	if v.Kind() == reflect.Pointer {
		name = "*" + name
	}
	field := func(f string, kind reflect.Kind) (reflect.Value, error) {
		x := v.Elem().FieldByName(f)
		if !x.IsValid() || x.Kind() != kind {
			return x, fmt.Errorf("%s: the rule of %s reads its field %s, a %v, which this Go's type does not have", at, name, f, kind)
		}
		return x, nil
	}
	called := func() error {
		if !v.CanInterface() {
			return fmt.Errorf("%s: the rule of %s reads it through its methods, and an unexported field holds it there", at, name)
		}
		return nil
	}
	switch name {
	case "*regexp.Regexp":
		if v.IsNil() {
			return regexpCopy{}, true, nil
		}
		longest, err := field("longest", reflect.Bool)
		if err == nil {
			err = called()
		}
		if err != nil {
			return nil, true, err
		}
		re := v.Interface().(*regexp.Regexp)
		return regexpCopy{v.Pointer(), re.String(), slices.Clone(re.SubexpNames()), longest.Bool()}, true, nil
	case "*strings.Replacer":
		if v.IsNil() {
			return replacerCopy{}, true, nil
		}
		oldnew, err := field("oldnew", reflect.Slice)
		if err != nil {
			return nil, true, err
		}
		table, err := field("r", reflect.Interface)
		if err != nil {
			return nil, true, err
		}
		c := replacerCopy{at: v.Pointer(), oldnew: make([]string, oldnew.Len())}
		for i := range oldnew.Len() {
			c.oldnew[i] = oldnew.Index(i).String()
		}
		if !table.IsNil() {
			if table.Elem().Kind() != reflect.Pointer {
				return nil, true, fmt.Errorf("%s: the rule of %s reads its table (field r) by its address, and this Go's table is a %v", at, name, table.Elem().Type())
			}
			c.table, c.tableAt = table.Elem().Type().String(), table.Elem().Pointer()
		}
		return c, true, nil
	case "*errors.errorString":
		if v.IsNil() {
			return errorCopy{}, true, nil
		}
		if err := called(); err != nil {
			return nil, true, err
		}
		return errorCopy{v.Pointer(), v.Interface().(error).Error()}, true, nil
	case "time.Time":
		if err := called(); err != nil {
			return nil, true, err
		}
		tm := v.Interface().(time.Time)
		return timeCopy{tm.Format(time.RFC3339Nano), tm.Location().String()}, true, nil
	case "*reflect.rtype":
		if v.IsNil() {
			return typeCopy{}, true, nil
		}
		if err := called(); err != nil {
			return nil, true, err
		}
		return typeCopy{v.Pointer(), v.Interface().(reflect.Type).String()}, true, nil
	}
	return nil, false, nil
}

// regexpCopy, replacerCopy, errorCopy, timeCopy and typeCopy are the copies that foreignCopy makes
// of a value of each type of another package that has a rule.
type regexpCopy struct {
	at      uintptr
	source  string
	names   []string
	longest bool
}

type replacerCopy struct {
	at      uintptr
	oldnew  []string
	table   string
	tableAt uintptr
}

type errorCopy struct {
	at   uintptr
	text string
}

type timeCopy struct{ text, location string }

type typeCopy struct {
	at   uintptr
	text string
}

// init builds the table of each *strings.Replacer of the package (pointerToken, valueReferences)
// before any test runs, and so before TestMain's first copy. A Replacer builds its table at its
// first Replace, under a sync.Once, then clears its pairs (oldnew) and only reads the table; so
// each copy that the guard takes of a Replacer is of the table that each later call reads, and no
// copy reads a Replacer while the first call of a test beside it builds it.
func init() {
	pointerToken.Replace("")
	valueReferences.Replace("")
}

// packageValuesKept fails the test unless each variable of the package holds what before, the copy
// that packageValuesNow took before who (a writer, or the sweeps' setup) was built, holds, so that
// a writer that changes one, for a later call of its own sweep or for a test that runs beside it,
// fails by name.
func packageValuesKept(t *testing.T, who string, before []packageValue) {
	t.Helper()
	if changed := packageValuesChanged(who, before); len(changed) > 0 {
		t.Fatal(strings.Join(changed, "\n"))
	}
}

// packageValuesChanged is a line for each variable of the package that holds now another value
// than before, the copy that packageValuesNow took before who ran, holds, by its name.
func packageValuesChanged(who string, before []packageValue) []string {
	now, err := packageValuesNow()
	if err != nil {
		return []string{who + ": " + err.Error()}
	}
	var changed []string
	for i := range now {
		if !reflect.DeepEqual(now[i].value, before[i].value) {
			changed = append(changed, fmt.Sprintf("%s changed %s, a value of the package: it holds %.300s after it ran, where it held %.300s", who, now[i].name, fmt.Sprintf("%+v", now[i].value), fmt.Sprintf("%+v", before[i].value)))
		}
	}
	return changed
}

// Each variable of the package is in the copy that the guard of the writers takes
// (packageValuesNow), by a rule that copies what code of the package can change of it, so that a
// writer that changes one fails by name: this test reads each file of the package with go/parser
// and fails for each variable declared at its top level that the list (packageVariables) does not
// name, for each name of the list that names no such variable or that the list names twice, and
// when it finds no variable at all, since it then read none. It fails for each variable whose type,
// or the type of a part of it, no rule covers (valueCopy). And it takes two copies and fails for
// each list, map or pointer that both hold (sharedObject): a copy that held a variable's storage,
// such as the capture names that a regexp hands out, would change with the variable, and the guard
// would not see the change.
func TestEachVariableOfThePackageIsGuarded(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	declared := map[string]string{} // the name of each variable, and where the package declares it
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if f.Name.Name != "specgraph" {
			continue
		}
		for _, decl := range f.Decls {
			if g, isVar := decl.(*ast.GenDecl); isVar && g.Tok == token.VAR {
				for _, spec := range g.Specs {
					for _, name := range spec.(*ast.ValueSpec).Names {
						if name.Name != "_" {
							declared[name.Name] = fset.Position(name.Pos()).String()
						}
					}
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("go/parser found no variable declared at the top level of the package, so the test read none")
	}
	listed := map[string]int{}
	for _, v := range packageVariables() {
		listed[v.name]++
		if at := reflect.ValueOf(v.at); at.Kind() != reflect.Pointer || at.IsNil() {
			t.Errorf("packageVariables holds a %T for %s, where it holds the variable's address", v.at, v.name)
		} else if _, err := valueCopy(at.Elem(), v.name, 0); err != nil {
			t.Errorf("%s declares the variable %s, of the type %v, and the guard has no copy of it: %v", declared[v.name], v.name, at.Elem().Type(), err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		if listed[name] == 0 {
			t.Errorf("%s declares the variable %s, and packageVariables does not list it, so a writer that changes it passes", declared[name], name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(listed)) {
		switch {
		case declared[name] == "":
			t.Errorf("packageVariables lists %s, and the package declares no variable of that name", name)
		case listed[name] > 1:
			t.Errorf("packageVariables lists %s %d times", name, listed[name])
		}
	}
	if t.Failed() {
		return
	}
	a, err := packageValuesNow()
	if err != nil {
		t.Fatal(err)
	}
	b, err := packageValuesNow()
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if at, shared := sharedObject(reflect.ValueOf(a[i].value), reflect.ValueOf(b[i].value), ""); shared {
			t.Errorf("two copies of %s hold one object at %q, so a writer that changes the variable there changes its copy too", a[i].name, at)
		}
	}
}

// sharedObject is the first object that a and b, two copies of one variable, both hold, by its
// path, and whether there is one: a map, the array of a slice with room, or the object of a
// pointer. A copy holds only values of this package's copy types and of the language's own types,
// so it enters each of them whole; it does not enter a string, which no code can change through a
// copy of it.
func sharedObject(a, b reflect.Value, at string) (string, bool) {
	if !a.IsValid() || !b.IsValid() || a.Type() != b.Type() {
		return "", false
	}
	switch a.Kind() {
	case reflect.Map:
		if a.IsNil() || b.IsNil() {
			return "", false
		}
		if a.UnsafePointer() == b.UnsafePointer() {
			return at, true
		}
		for _, key := range a.MapKeys() {
			if p, shared := sharedObject(a.MapIndex(key), b.MapIndex(key), fmt.Sprintf("%s[%v]", at, key)); shared {
				return p, true
			}
		}
	case reflect.Slice:
		if a.Cap() > 0 && b.Cap() > 0 && a.UnsafePointer() == b.UnsafePointer() {
			return at, true
		}
		fallthrough
	case reflect.Array:
		for i := range min(a.Len(), b.Len()) {
			if p, shared := sharedObject(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", at, i)); shared {
				return p, true
			}
		}
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return "", false
		}
		if a.Pointer() == b.Pointer() {
			return at, true
		}
		return sharedObject(a.Elem(), b.Elem(), at)
	case reflect.Interface:
		return sharedObject(a.Elem(), b.Elem(), at)
	case reflect.Struct:
		for i := range a.NumField() {
			if p, shared := sharedObject(a.Field(i), b.Field(i), at+"."+a.Type().Field(i).Name); shared {
				return p, true
			}
		}
	}
	return "", false
}
