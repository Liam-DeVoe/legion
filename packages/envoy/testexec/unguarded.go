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
// same way, or of a path an os.Rename of the function moved it to, or the Chmod method of the
// variable or field the open assigned its file to, or os.Chmod of that one's Name(). The open may
// be the value assigned or an argument of a call that wraps it, as must(os.CreateTemp(dir, ""))
// does. A mode the check cannot read as holding no execute bit, in the open or in the Chmod, is
// named too: one that is neither a constant nor masked by one, as perm&^0o111 is
// (withoutExecuteBit). So is a use of os.WriteFile, os.OpenFile, os.Create, os.CreateTemp or
// os.Chmod as a function value, whose file and mode it cannot follow, and a dot import of os,
// whose calls it cannot tell from the file's own functions. The one place left out is
// programfile.Write itself: the function Write of the package WriterPackage, in a file that is no
// test file; every other function of that package is read, and so is a package of that name at
// any other import path. A directory below root that holds a go.mod of its own is another module,
// which that module's check reads, and node_modules, vendor, testdata and dot directories are not
// Go source of the module.
//
// Unguarded fails, naming root, when root holds no go.mod, as the parent of a module's directory
// or one of its subdirectories does, and when it read no test file under root: a root that holds
// none, or whose every directory that holds one is another module. A check of such a root would
// name no write and pass while it read nothing, or read only part of its module.
//
// The check reads each file's syntax alone, with no types, so it cannot see:
//
//   - a Chmod in another function than the open, or of the same path spelled another way, as a
//     variable that holds the filepath.Join the open spelled out, or the file's Name() kept in a
//     variable;
//   - a file opened inside a function that returns it, such as a helper that calls os.CreateTemp
//     and returns the file, which its caller then gives an execute bit;
//   - a file written by another function of package os (os.CopyFS) or by a function of another
//     package (io/ioutil.WriteFile, syscall.Open, a helper of a package that is neither of the two
//     kinds above);
//   - a test file of a package whose tests run one at a time, and is not a test-support package:
//     its test binary runs one test at a time, so only that test's own goroutines can fork beside
//     its write.
//
// A file a command such as cp writes is open in that command's process, not in the test's, so no
// fork of the test copies its descriptor, and the check has nothing to name there.
func Unguarded(root string) ([]string, error) {
	module, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read no module at %s: it holds no go.mod, so it is not the root of the module whose writes the check reads: %w", root, err)
	}
	dirs, tests := map[string][]string{}, 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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
			if strings.HasSuffix(path, "_test.go") {
				tests++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if tests == 0 {
		return nil, fmt.Errorf("read no Go test file under %s: it holds none, or each directory below it that holds one has a go.mod of its own and is another module, so no write a test makes was read", root)
	}
	var offences []string
	for dir, paths := range dirs {
		relative, err := filepath.Rel(root, dir)
		if err != nil {
			return nil, err
		}
		importPath := module
		if relative != "." {
			importPath += "/" + filepath.ToSlash(relative)
		}
		found, err := unguardedInPackage(root, importPath == WriterPackage, paths)
		if err != nil {
			return nil, err
		}
		offences = append(offences, found...)
	}
	slices.Sort(offences)
	return offences, nil
}

// WriterPackage is the import path of the one package whose Write Unguarded leaves out: the writer
// every other place calls.
const WriterPackage = "github.com/sjawhar/envoy/programfile"

// modulePath is the module path the go.mod at path declares.
func modulePath(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(body)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			if module, err := strconv.Unquote(strings.TrimSpace(rest)); err == nil {
				return module, nil
			}
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("%s declares no module", path)
}

// unguardedInPackage is Unguarded for the Go files of one directory. writer says the directory is
// WriterPackage's, whose function Write it leaves out.
func unguardedInPackage(root string, writer bool, paths []string) ([]string, error) {
	files := token.NewFileSet()
	parsed := map[string]*ast.File{}
	parallel, support := false, false
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
	}
	var offences []string
	for _, path := range paths {
		test := strings.HasSuffix(path, "_test.go")
		if test && !parallel && !support {
			continue
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		for _, line := range executableWrites(files, parsed[path], writer && !test) {
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
// says how), with each line that uses one of osWriters as a value and that dot-imports os. writer
// leaves out the file's function Write, programfile.Write itself.
func executableWrites(files *token.FileSet, file *ast.File, writer bool) []int {
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
		var read []ast.Decl
		for _, declaration := range file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && writer && function.Recv == nil && function.Name.Name == "Write" {
				continue
			}
			read = append(read, declaration)
		}
		for _, declaration := range read {
			for _, open := range executableOpens(declaration, osFunc, called) {
				lines[files.Position(open.Pos()).Line] = true
			}
		}
		for _, declaration := range read {
			ast.Inspect(declaration, func(node ast.Node) bool {
				if selector, ok := node.(*ast.SelectorExpr); ok && !called[selector] && osWriters[osFunc(selector)] {
					lines[files.Position(selector.Pos()).Line] = true
				}
				return true
			})
		}
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
	// byPath keys the opens of each file by how the declaration names its path: as an open or an
	// os.Rename spells it, or <file>.Name() for the file an open assigned to <file>. byHandle keys
	// the opens by the variable or field each assigned its *os.File to.
	byPath, byHandle := map[string][]*ast.CallExpr{}, map[string][]*ast.CallExpr{}
	assigned := func(target, value ast.Expr) {
		open := fileOpen(value, osFunc)
		if handle := types.ExprString(target); open != nil && handle != "_" {
			byHandle[handle] = append(byHandle[handle], open)
			byPath[handle+".Name()"] = append(byPath[handle+".Name()"], open)
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
			case function == "Rename" && len(node.Args) == 2:
				// The opens of the old path, as far as the declaration has made them, are now
				// opens of the new one.
				from, to := types.ExprString(node.Args[0]), types.ExprString(node.Args[1])
				byPath[to] = append(byPath[to], byPath[from]...)
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
		} else {
			opens = append(opens, byHandle[types.ExprString(chmod.Fun.(*ast.SelectorExpr).X)]...)
		}
	}
	return opens
}

// fileOpen is the call of os.Create, os.CreateTemp or os.OpenFile whose *os.File value holds: the
// value itself, or one argument of a call that wraps it, as must(os.CreateTemp(dir, "")) does. It
// is nil for any other value.
func fileOpen(value ast.Expr, osFunc func(ast.Expr) string) *ast.CallExpr {
	call, ok := value.(*ast.CallExpr)
	if !ok {
		return nil
	}
	candidates := append([]ast.Expr{call}, call.Args...)
	for _, candidate := range candidates {
		if open, ok := candidate.(*ast.CallExpr); ok {
			if function := osFunc(open.Fun); function == "Create" || function == "CreateTemp" || function == "OpenFile" {
				return open
			}
		}
	}
	return nil
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
