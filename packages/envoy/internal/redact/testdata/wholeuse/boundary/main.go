// Command boundary is the census of redact.OAuth2, the one way this repository calls
// golang.org/x/oauth2 and github.com/coreos/go-oidc: it reads, from the libraries' own export data
// at the version go.mod pins, every exported function, method and interface method of
// golang.org/x/oauth2, of each of its subpackages this repository imports, and of go-oidc, that has
// an error result, and every exported package-level variable of a function type with one. Then it
// reads every non-test file of the packages it is given, and fails on each reference to one of them
// that is not the function of a call that is itself the only argument of redact.OAuth2 or of
// oauth2call.Call, or the second argument of redact.OAuth2 beside a value for a call that answers
// an error alone, naming the line. It fails too on each value whose type implements
// oauth2.TokenSource handed to a function of another module, or set in a struct field another
// module declares, that is not a token source the boundary made (redact.TokenSource, or a type of
// package oauth2call): a library that asks it for a token prints the refusal it answers. No list of
// names is written here: the closed set is the libraries'.
//
// Usage, from this directory: GOWORK=off go run ./boundary -dir ../../../.. ./...
package main

import (
	"cmp"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const (
	oauth2Path      = "golang.org/x/oauth2"
	goOIDCPath      = "github.com/coreos/go-oidc"
	modulePath      = "github.com/sjawhar/envoy"
	redactPath      = modulePath + "/internal/redact"
	oauth2callPath  = modulePath + "/oauth2call"
	boundaryWrapper = "BoundTokenSource"
)

// library reports whether path is a package of a library whose calls cross the boundary.
func library(path string) bool {
	return path == oauth2Path || strings.HasPrefix(path, oauth2Path+"/") || path == goOIDCPath || strings.HasPrefix(path, goOIDCPath+"/")
}

func main() {
	dir := flag.String("dir", ".", "the module directory to load from")
	listAPI := flag.Bool("api", false, "print each library function the census holds calls of to the boundary")
	flag.Parse()
	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:  *dir,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "boundary:", err)
		os.Exit(2)
	}
	if packages.PrintErrors(pkgs) > 0 {
		fmt.Fprintln(os.Stderr, "boundary: the packages do not load cleanly; nothing is checked")
		os.Exit(2)
	}

	// The closed set: each library package any loaded package imports, read from its types.
	api := map[string]string{}
	perPackage := map[string]int{}
	seen := map[string]bool{}
	var tokenSource *types.Interface
	for _, pkg := range pkgs {
		for _, imported := range pkg.Types.Imports() {
			path := imported.Path()
			if path == oauth2Path && tokenSource == nil {
				if obj, ok := imported.Scope().Lookup("TokenSource").(*types.TypeName); ok {
					tokenSource, _ = obj.Type().Underlying().(*types.Interface)
				}
			}
			if !library(path) || seen[path] {
				continue
			}
			seen[path] = true
			for name, kind := range libraryAPI(imported) {
				api[name] = kind
				perPackage[path]++
			}
		}
	}
	if len(api) == 0 || tokenSource == nil {
		fmt.Println("UNKNOWN: no golang.org/x/oauth2 package is imported, so there is nothing to check")
		os.Exit(2)
	}

	root, _ := filepath.Abs(*dir)
	var lines []string
	refs, handoffs, failed, files := 0, 0, 0, 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			name := pkg.Fset.Position(file.Pos()).Filename
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			files++
			var stack []ast.Node
			ast.Inspect(file, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				if failure, checked := handoff(pkg.TypesInfo, n, tokenSource); checked {
					handoffs++
					pos := pkg.Fset.Position(n.Pos())
					rel, err := filepath.Rel(root, pos.Filename)
					if err != nil {
						rel = pos.Filename
					}
					verdict := "ok"
					if failure != "" {
						verdict, failed = "FAIL", failed+1
					}
					lines = append(lines, fmt.Sprintf("%s\t%s:%d:%d\ttoken source handed to another module\t%s", verdict, rel, pos.Line, pos.Column, cmp.Or(failure, "a token source the boundary made")))
				}
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				key := objectKey(pkg.TypesInfo.Uses[id])
				if _, inAPI := api[key]; !inAPI {
					return true
				}
				refs++
				pos := pkg.Fset.Position(id.Pos())
				rel, err := filepath.Rel(root, pos.Filename)
				if err != nil {
					rel = pos.Filename
				}
				verdict, why := "ok", "called across redact.OAuth2"
				if !throughBoundary(pkg.TypesInfo, stack) {
					verdict, why, failed = "FAIL", "not called as the only argument of redact.OAuth2 or oauth2call.Call", failed+1
				}
				lines = append(lines, fmt.Sprintf("%s\t%s:%d:%d\t%s\t%s", verdict, rel, pos.Line, pos.Column, key, why))
				return true
			})
		}
	}
	sort.Strings(lines)
	for _, line := range lines {
		fmt.Println(line)
	}
	var paths []string
	for path := range perPackage {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fmt.Printf("API\t%s\t%d functions, methods and function variables with an error result\n", path, perPackage[path])
	}
	if *listAPI {
		var names []string
		for name, kind := range api {
			names = append(names, "LIBRARY\t"+kind+"\t"+name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Println(name)
		}
	}
	fmt.Printf("SUMMARY\tlibrary API=%d\tpackages=%d\tnon-test files=%d\treferences=%d\ttoken-source hand-offs=%d\tfailures=%d\n", len(api), len(pkgs), files, refs, handoffs, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

// libraryAPI is each exported function, method, interface method and function variable of pkg
// with an error result, by its key (objectKey), with its kind.
func libraryAPI(pkg *types.Package) map[string]string {
	out := map[string]string{}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch obj := obj.(type) {
		case *types.Func:
			if errorResult(obj.Type()) {
				out[objectKey(obj)] = "function"
			}
		case *types.Var:
			if errorResult(obj.Type()) {
				out[objectKey(obj)] = "function variable"
			}
		case *types.TypeName:
			named, ok := obj.Type().(*types.Named)
			if !ok {
				continue
			}
			for i := range named.NumMethods() {
				if m := named.Method(i); m.Exported() && errorResult(m.Type()) {
					out[objectKey(m)] = "method"
				}
			}
			if iface, ok := named.Underlying().(*types.Interface); ok {
				for i := range iface.NumMethods() {
					if m := iface.Method(i); m.Exported() && errorResult(m.Type()) {
						out[objectKey(m)] = "interface method"
					}
				}
			}
		}
	}
	return out
}

// errorResult reports whether t is a function type one of whose results is error.
func errorResult(t types.Type) bool {
	sig, ok := t.Underlying().(*types.Signature)
	if !ok {
		return false
	}
	errorType := types.Universe.Lookup("error").Type()
	for i := range sig.Results().Len() {
		if types.Identical(sig.Results().At(i).Type(), errorType) {
			return true
		}
	}
	return false
}

// objectKey names obj the same way whichever package's type check produced it.
func objectKey(obj types.Object) string {
	switch obj := obj.(type) {
	case *types.Func:
		return obj.Origin().FullName()
	case *types.Var:
		if obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope() {
			return obj.Pkg().Path() + "." + obj.Name()
		}
	}
	return ""
}

// throughBoundary reports whether the identifier at the top of stack is the function of a call
// that is the only argument of redact.OAuth2 or of oauth2call.Call, or the second argument of one
// of them beside a value.
func throughBoundary(info *types.Info, stack []ast.Node) bool {
	i := len(stack) - 1
	id := stack[i].(*ast.Ident)
	fun := ast.Expr(id)
	if i > 0 {
		if sel, ok := stack[i-1].(*ast.SelectorExpr); ok && sel.Sel == id {
			fun, i = sel, i-1
		}
	}
	if i < 1 {
		return false
	}
	call, ok := stack[i-1].(*ast.CallExpr)
	if !ok || call.Fun != fun || i < 2 {
		return false
	}
	outer, ok := stack[i-2].(*ast.CallExpr)
	if !ok || !(len(outer.Args) == 1 && outer.Args[0] == call || len(outer.Args) == 2 && outer.Args[1] == call) {
		return false
	}
	fn := calleeFunc(info, outer)
	return fn != nil && fn.Pkg() != nil &&
		(fn.Pkg().Path() == redactPath && fn.Name() == "OAuth2" || fn.Pkg().Path() == oauth2callPath && fn.Name() == "Call")
}

// calleeFunc is the function call calls, past an explicit instantiation, or nil.
func calleeFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	callee := ast.Unparen(call.Fun)
	if index, ok := callee.(*ast.IndexExpr); ok {
		callee = index.X
	}
	var name *ast.Ident
	switch c := callee.(type) {
	case *ast.Ident:
		name = c
	case *ast.SelectorExpr:
		name = c.Sel
	}
	if name == nil {
		return nil
	}
	fn, _ := info.Uses[name].(*types.Func)
	return fn
}

// handoff checks n where it hands a value to another module: a call of a function another module
// declares, a composite literal's field another module declares, or an assignment to one. It
// answers whether n is such a hand-off of a value whose type implements tokenSource, and, where
// that value is not a token source the boundary made, why it fails.
func handoff(info *types.Info, n ast.Node, tokenSource *types.Interface) (failure string, checked bool) {
	check := func(value ast.Expr, to string) {
		tv, ok := info.Types[value]
		if !ok || tv.IsType() || tv.Type == nil || !types.Implements(tv.Type, tokenSource) {
			return
		}
		checked = true
		if !boundaryMade(tv.Type) {
			failure = fmt.Sprintf("%s (%s) handed to %s, not through redact.TokenSource", types.ExprString(value), tv.Type, to)
		}
	}
	switch n := n.(type) {
	case *ast.CallExpr:
		fn := calleeFunc(info, n)
		if fn == nil || fn.Pkg() == nil || strings.HasPrefix(fn.Pkg().Path(), modulePath) {
			return "", false
		}
		for _, arg := range n.Args {
			check(arg, fn.FullName())
		}
	case *ast.KeyValueExpr:
		if key, ok := n.Key.(*ast.Ident); ok {
			if field, ok := info.Uses[key].(*types.Var); ok && foreignField(field) {
				check(n.Value, "the field "+field.Pkg().Path()+"."+field.Name())
			}
		}
	case *ast.AssignStmt:
		for i, lhs := range n.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || i >= len(n.Rhs) {
				continue
			}
			if field, ok := info.Uses[sel.Sel].(*types.Var); ok && foreignField(field) {
				check(n.Rhs[i], "the field "+field.Pkg().Path()+"."+field.Name())
			}
		}
	}
	return failure, checked
}

// foreignField reports whether v is a struct field another module declares.
func foreignField(v *types.Var) bool {
	return v.IsField() && v.Pkg() != nil && !strings.HasPrefix(v.Pkg().Path(), modulePath)
}

// boundaryMade reports whether t is a token source the boundary made: *redact.BoundTokenSource, or
// a type of package oauth2call.
func boundaryMade(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	path := named.Obj().Pkg().Path()
	return path == redactPath && named.Obj().Name() == boundaryWrapper || path == oauth2callPath
}
