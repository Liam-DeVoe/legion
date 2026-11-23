package testexec

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Unguarded names, as a root-relative path and line ("dir/file.go:12"), sorted, each place under
// root that opens a file for writing by package os rather than by programfile.Write and gives it
// an execute bit, where the file may run while another goroutine of its process forks:
//
//   - in any file that is no test file (_test.go): the code a deployment runs, whose processes
//     start commands from many goroutines, and a test-support package, which is a package whose
//     name begins or ends with "test" (clitest, testbin, capabilitiestest), whose functions tests
//     of any package call;
//   - in a test file of a package whose tests run beside each other, which is a package one of
//     whose test files calls Parallel, on a test or a subtest, and in a test file of a test-support
//     package.
//
// Such a place is a call of os.WriteFile or os.OpenFile whose mode has an execute bit. It is also
// a call of os.WriteFile, os.OpenFile, os.Create or os.CreateTemp whose file the same top-level
// function gives an execute bit with a Chmod: os.Chmod of the path the call names, spelled the
// same way, os.Chmod of the Name of the variable that os.Create, os.OpenFile or os.CreateTemp
// assigned the file to, or that variable's Chmod method. A mode the check cannot read as holding
// no execute bit is named too: one that is neither a constant nor masked by one, as perm&^0o111
// is (withoutExecuteBit). So is a use of os.WriteFile, os.OpenFile, os.Create, os.CreateTemp or
// os.Chmod as a function value, whose file and mode it cannot follow, and a dot import of os,
// whose calls it cannot tell from the file's own functions. The one place left out is
// programfile.Write itself: the files of a package named programfile that are no test files. A
// directory below root that holds a go.mod of its own is another module, which that module's
// check reads, and node_modules, vendor, testdata and dot directories are not Go source of the
// module. A root under which the walk finds no Go file is an error, not a pass: the check read
// nothing.
//
// The check reads each file's syntax alone, with no types, so it cannot see:
//
//   - a Chmod in another function than the open, or of the same path spelled another way, as a
//     variable that holds the filepath.Join the open spelled out;
//   - a file opened by any function but os's four (io/ioutil.WriteFile, syscall.Open, unix.Openat,
//     a helper of another package) or written by another program, such as cp, go build or an
//     archive a command unpacks;
//   - a test file of a package whose tests run one at a time, and is not a test-support package:
//     its test binary runs one test at a time, so only that test's own goroutines can fork beside
//     its write.
func Unguarded(root string) ([]string, error) {
	dirs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			if name == "node_modules" || name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no Go file under %s: the check read nothing", root)
	}
	var offences []string
	for _, paths := range dirs {
		found, err := unguardedInPackage(root, paths)
		if err != nil {
			return nil, err
		}
		offences = append(offences, found...)
	}
	slices.Sort(offences)
	return offences, nil
}

// unguardedInPackage is Unguarded for the Go files of one directory.
func unguardedInPackage(root string, paths []string) ([]string, error) {
	files := token.NewFileSet()
	parsed := map[string]*ast.File{}
	parallel, support, self := false, false, false
	for _, path := range paths {
		file, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		parsed[path] = file
		if strings.HasSuffix(path, "_test.go") {
			parallel = parallel || callsParallel(file)
			continue
		}
		name := file.Name.Name
		support = support || strings.HasPrefix(name, "test") || strings.HasSuffix(name, "test")
		self = self || name == "programfile"
	}
	var offences []string
	for _, path := range paths {
		test := strings.HasSuffix(path, "_test.go")
		if self && !test || test && !parallel && !support {
			continue
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		for _, line := range executableWrites(files, parsed[path]) {
			offences = append(offences, filepath.ToSlash(relative)+":"+strconv.Itoa(line))
		}
	}
	return offences, nil
}

// callsParallel says whether file calls a method Parallel with no arguments: testing.T's, on a
// test or a subtest.
func callsParallel(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && len(call.Args) == 0 {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Parallel" {
				found = true
			}
		}
		return !found
	})
	return found
}

// osWriters are the functions of package os that open a file for writing, and os.Chmod, which can
// give a written file an execute bit. A use of one as a value is named, since the check cannot
// follow the file or the mode it is then called with.
var osWriters = map[string]bool{"WriteFile": true, "OpenFile": true, "Create": true, "CreateTemp": true, "Chmod": true}

// executableWrites are the lines of file that open a file for writing by package os, by whatever
// name the file imports it, and give it an execute bit or a mode that is no constant (Unguarded
// says how), with each line that uses one of osWriters as a value and that dot-imports os.
func executableWrites(files *token.FileSet, file *ast.File) []int {
	osName, lines := "", map[int]bool{}
	for _, spec := range file.Imports {
		if path, err := strconv.Unquote(spec.Path.Value); err != nil || path != "os" {
			continue
		}
		switch {
		case spec.Name == nil:
			osName = "os"
		case spec.Name.Name == ".":
			lines[files.Position(spec.Pos()).Line] = true
		case spec.Name.Name != "_":
			osName = spec.Name.Name
		}
	}
	if osName != "" {
		osFunc := func(expression ast.Expr) string {
			if selector, ok := expression.(*ast.SelectorExpr); ok {
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == osName {
					return selector.Sel.Name
				}
			}
			return ""
		}
		called := map[*ast.SelectorExpr]bool{}
		for _, declaration := range file.Decls {
			for _, open := range executableOpens(declaration, osFunc, called) {
				lines[files.Position(open.Pos()).Line] = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok && !called[selector] && osWriters[osFunc(selector)] {
				lines[files.Position(selector.Pos()).Line] = true
			}
			return true
		})
	}
	return slices.Sorted(maps.Keys(lines))
}

// executableOpens are the calls of one top-level declaration that open a file for writing by
// package os and give it an execute bit or a mode that is no constant: in the call's own mode, or
// by a Chmod the declaration makes of the same file, which Unguarded says how it knows. osFunc
// names the function of package os an expression is, or is "". Each os function the declaration
// calls, rather than uses as a value, joins called.
func executableOpens(declaration ast.Decl, osFunc func(ast.Expr) string, called map[*ast.SelectorExpr]bool) []*ast.CallExpr {
	var opens, chmods []*ast.CallExpr
	byPath := map[string][]*ast.CallExpr{}   // the opens of each path, as the call spells it
	byHandle := map[string][]*ast.CallExpr{} // the opens whose *os.File each variable was given
	assigned := func(target, value ast.Expr) {
		call, isCall := value.(*ast.CallExpr)
		name, isName := target.(*ast.Ident)
		if isCall && isName && name.Name != "_" {
			if function := osFunc(call.Fun); function == "Create" || function == "OpenFile" || function == "CreateTemp" {
				byHandle[name.Name] = append(byHandle[name.Name], call)
			}
		}
	}
	ast.Inspect(declaration, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			if len(node.Lhs) > 0 && len(node.Rhs) == 1 {
				assigned(node.Lhs[0], node.Rhs[0])
			}
		case *ast.ValueSpec:
			if len(node.Names) > 0 && len(node.Values) == 1 {
				assigned(node.Names[0], node.Values[0])
			}
		case *ast.CallExpr:
			function := osFunc(node.Fun)
			if function != "" {
				called[node.Fun.(*ast.SelectorExpr)] = true
			}
			switch {
			case (function == "WriteFile" || function == "OpenFile") && len(node.Args) == 3:
				if !withoutExecuteBit(node.Args[2]) {
					opens = append(opens, node)
				}
				path := types.ExprString(node.Args[0])
				byPath[path] = append(byPath[path], node)
			case function == "Create" && len(node.Args) == 1:
				path := types.ExprString(node.Args[0])
				byPath[path] = append(byPath[path], node)
			case function == "Chmod" && len(node.Args) == 2 && !withoutExecuteBit(node.Args[1]):
				chmods = append(chmods, node)
			case function == "" && len(node.Args) == 1:
				if selector, ok := node.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Chmod" && !withoutExecuteBit(node.Args[0]) {
					chmods = append(chmods, node)
				}
			}
		}
		return true
	})
	for _, chmod := range chmods {
		if osFunc(chmod.Fun) == "Chmod" {
			opens = append(opens, byPath[types.ExprString(chmod.Args[0])]...)
			if handle := nameOf(chmod.Args[0]); handle != "" {
				opens = append(opens, byHandle[handle]...)
			}
		} else if handle, ok := chmod.Fun.(*ast.SelectorExpr).X.(*ast.Ident); ok {
			opens = append(opens, byHandle[handle.Name]...)
		}
	}
	return opens
}

// nameOf is the variable a path is the Name of, as in os.Chmod(file.Name(), 0o755), or "".
func nameOf(path ast.Expr) string {
	call, ok := path.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return ""
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Name" {
		return ""
	}
	if handle, ok := selector.X.(*ast.Ident); ok {
		return handle.Name
	}
	return ""
}

// withoutExecuteBit says whether mode holds no execute bit: a constant that holds none, or any
// mode masked so that it cannot hold one, by & with a constant that holds none (perm&0o666) or
// by &^ with a constant that holds all three (perm&^0o111). A writer whose callers choose its
// mode says so with such a mask.
func withoutExecuteBit(mode ast.Expr) bool {
	if value, constant := modeOf(mode); constant {
		return value&0o111 == 0
	}
	masked, ok := ast.Unparen(mode).(*ast.BinaryExpr)
	if !ok {
		return false
	}
	switch masked.Op {
	case token.AND:
		for _, side := range []ast.Expr{masked.X, masked.Y} {
			if value, constant := modeOf(side); constant && value&0o111 == 0 {
				return true
			}
		}
	case token.AND_NOT:
		value, constant := modeOf(masked.Y)
		return constant && value&0o111 == 0o111
	}
	return false
}

// modeOf is the value of a file mode written as a constant: an integer literal, a conversion of
// one to a FileMode, or literals joined by |, &, ^ or +. Anything else is no constant to it.
func modeOf(expression ast.Expr) (int64, bool) {
	switch node := expression.(type) {
	case *ast.BasicLit:
		if node.Kind != token.INT {
			return 0, false
		}
		value, err := strconv.ParseInt(strings.ReplaceAll(node.Value, "_", ""), 0, 64)
		return value, err == nil
	case *ast.ParenExpr:
		return modeOf(node.X)
	case *ast.CallExpr:
		if len(node.Args) != 1 {
			return 0, false
		}
		switch fun := node.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name != "FileMode" {
				return 0, false
			}
		case *ast.Ident:
			if fun.Name != "FileMode" {
				return 0, false
			}
		default:
			return 0, false
		}
		return modeOf(node.Args[0])
	case *ast.BinaryExpr:
		left, leftConstant := modeOf(node.X)
		right, rightConstant := modeOf(node.Y)
		if !leftConstant || !rightConstant {
			return 0, false
		}
		switch node.Op {
		case token.OR:
			return left | right, true
		case token.AND:
			return left & right, true
		case token.XOR:
			return left ^ right, true
		case token.ADD:
			return left + right, true
		}
	}
	return 0, false
}
