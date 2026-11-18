// Command boundary is the census of redact.OAuth2, the one way this repository calls
// golang.org/x/oauth2 and github.com/coreos/go-oidc: it reads, from the libraries' own export data
// at the version go.mod pins, every exported function, method and interface method of
// golang.org/x/oauth2, of each of its subpackages this repository imports, and of go-oidc, that has
// an error result, and every exported package-level variable of a function type with one. Then it
// reads every non-test file of the packages it is given, and fails on each reference to one of them
// that is not the function of a call that is itself the only argument of redact.OAuth2 or of
// oauth2call.Call, or the second argument of redact.OAuth2 beside a value for a call that answers
// an error alone, naming the line. A call through an interface or a type parameter resolves to a
// method this repository or the standard library declares, so it counts as well when that method
// has an error result and an exported type of the libraries implements the receiver's interface or
// the type parameter's constraint (types.Implements): the value it is called on can be the
// library's. So does each reference to a function or method of the libraries that answers an HTTP
// client or transport (*net/http.Client, a net/http.RoundTripper), whose every request calls the
// library and answers its error through net/http: it passes only where a token source the
// boundary made is among its arguments (oauth2.NewClient(ctx, redact.TokenSource(source))), and
// fails where it builds a token source of its own (Config.Client). It fails too on each value
// whose type implements oauth2.TokenSource handed to a function of another module, or set in a
// struct field another module declares, that is not a token source the boundary made
// (redact.TokenSource, or a type of package oauth2call): a library that asks it for a token prints
// the refusal it answers. No list of names is written here: the closed set is the libraries'.
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
	var libTypes []types.Type
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
			libTypes = append(libTypes, libraryTypes(imported)...)
		}
	}
	if len(api) == 0 || tokenSource == nil {
		fmt.Println("UNKNOWN: no golang.org/x/oauth2 package is imported, so there is nothing to check")
		os.Exit(2)
	}
	implementers := map[*types.Interface]string{}

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
				kind, inAPI := api[key]
				via := ""
				if !inAPI {
					if key, via = libraryDispatch(pkg.TypesInfo, stack, libTypes, implementers); key == "" {
						return true
					}
				}
				refs++
				pos := pkg.Fset.Position(id.Pos())
				rel, err := filepath.Rel(root, pos.Filename)
				if err != nil {
					rel = pos.Filename
				}
				verdict, why := "ok", "called across redact.OAuth2"
				switch {
				case strings.HasPrefix(kind, httpClientKind):
					if failure := clientBuilt(pkg.TypesInfo, stack, tokenSource); failure != "" {
						verdict, why, failed = "FAIL", failure, failed+1
					} else if kind != httpClientKind && !throughBoundary(pkg.TypesInfo, stack) {
						verdict, why, failed = "FAIL", "a library HTTP client not called as the only argument of redact.OAuth2 or oauth2call.Call", failed+1
					} else {
						why = "a library HTTP client over a token source the boundary made"
					}
				case !throughBoundary(pkg.TypesInfo, stack):
					verdict, why, failed = "FAIL", "not called as the only argument of redact.OAuth2 or oauth2call.Call", failed+1
				}
				if via != "" {
					why += "; called through " + via
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

// httpClientKind is the kind of a library function or method that answers an HTTP client or
// transport; one that answers an error too is that kind with errorResultSuffix.
const (
	httpClientKind    = "http client"
	errorResultSuffix = " with an error result"
)

// libraryAPI is each exported function, method, interface method and function variable of pkg
// with an error result, and each exported function and method that answers an HTTP client or
// transport (httpClientKind), by its key (objectKey), with its kind.
func libraryAPI(pkg *types.Package) map[string]string {
	out := map[string]string{}
	answersHTTP := httpAnswer(pkg)
	kind := func(t types.Type, plain string) string {
		switch {
		case answersHTTP(t) && errorResult(t):
			return httpClientKind + errorResultSuffix
		case answersHTTP(t):
			return httpClientKind
		case errorResult(t):
			return plain
		}
		return ""
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch obj := obj.(type) {
		case *types.Func:
			if k := kind(obj.Type(), "function"); k != "" {
				out[objectKey(obj)] = k
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
				if m := named.Method(i); m.Exported() {
					if k := kind(m.Type(), "method"); k != "" {
						out[objectKey(m)] = k
					}
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

// httpAnswer is whether a function type answers an HTTP client or transport - a *net/http.Client,
// or a value that implements net/http.RoundTripper - as pkg imports net/http. A library that
// does not import it answers neither.
func httpAnswer(pkg *types.Package) func(types.Type) bool {
	var client types.Type
	var roundTripper *types.Interface
	for _, imported := range pkg.Imports() {
		if imported.Path() != "net/http" {
			continue
		}
		if obj, ok := imported.Scope().Lookup("Client").(*types.TypeName); ok {
			client = types.NewPointer(obj.Type())
		}
		if obj, ok := imported.Scope().Lookup("RoundTripper").(*types.TypeName); ok {
			roundTripper, _ = obj.Type().Underlying().(*types.Interface)
		}
	}
	return func(t types.Type) bool {
		sig, ok := t.Underlying().(*types.Signature)
		if !ok || client == nil || roundTripper == nil {
			return false
		}
		for i := range sig.Results().Len() {
			r := sig.Results().At(i).Type()
			if types.Identical(r, client) || !types.IsInterface(r) && types.Implements(r, roundTripper) || types.Identical(r.Underlying(), roundTripper) {
				return true
			}
		}
		return false
	}
}

// libraryTypes is each exported type pkg declares that is not generic: a value of one can stand
// behind an interface or a type parameter this repository calls a method of.
func libraryTypes(pkg *types.Package) []types.Type {
	var out []types.Type
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if obj, ok := scope.Lookup(name).(*types.TypeName); ok && obj.Exported() && !obj.IsAlias() {
			if named, ok := obj.Type().(*types.Named); ok && named.TypeParams().Len() == 0 {
				out = append(out, named)
			}
		}
	}
	return out
}

// libraryDispatch answers, for the identifier at the top of stack, the method it names and how a
// library's code can answer it, where it names the method of a selector whose receiver is an
// interface or a type parameter, the method has an error result, and an exported type of the
// libraries - the type itself, or a pointer to it - implements that interface or the type
// parameter's constraint (types.Implements); otherwise "". implementers caches each interface's
// first implementing library type ("" for none).
func libraryDispatch(info *types.Info, stack []ast.Node, libTypes []types.Type, implementers map[*types.Interface]string) (key, via string) {
	if len(stack) < 2 {
		return "", ""
	}
	id := stack[len(stack)-1].(*ast.Ident)
	sel, ok := stack[len(stack)-2].(*ast.SelectorExpr)
	if !ok || sel.Sel != id {
		return "", ""
	}
	s := info.Selections[sel]
	if s == nil || s.Kind() == types.FieldVal {
		return "", ""
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok || !errorResult(fn.Type()) {
		return "", ""
	}
	recv := s.Recv()
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	var iface *types.Interface
	what := ""
	if tp, ok := recv.(*types.TypeParam); ok {
		iface, _ = tp.Constraint().Underlying().(*types.Interface)
		what = "the type parameter " + tp.Obj().Name() + " " + types.TypeString(tp.Constraint(), nil)
	} else {
		iface, _ = recv.Underlying().(*types.Interface)
		what = "the interface " + types.TypeString(recv, nil)
	}
	if iface == nil || iface.NumMethods() == 0 {
		return "", ""
	}
	impl, cached := implementers[iface]
	if !cached {
		for _, t := range libTypes {
			if types.Implements(t, iface) || !types.IsInterface(t) && types.Implements(types.NewPointer(t), iface) {
				impl = types.TypeString(t, nil)
				break
			}
		}
		implementers[iface] = impl
	}
	if impl == "" {
		return "", ""
	}
	return fn.FullName(), what + ", which " + impl + " implements"
}

// clientBuilt says why the library HTTP client the identifier at the top of stack names fails, or
// answers "": it must be the function of a call one of whose arguments is a token source and
// every such argument a token source the boundary made, so the client's every request asks the
// boundary for its token. One built from a token source of the library's own (Config.Client), or
// named without a call, answers its refusal raw through net/http.
func clientBuilt(info *types.Info, stack []ast.Node, tokenSource *types.Interface) string {
	i := len(stack) - 1
	fun := ast.Expr(stack[i].(*ast.Ident))
	if i > 0 {
		if sel, ok := stack[i-1].(*ast.SelectorExpr); ok && sel.Sel == fun {
			fun, i = sel, i-1
		}
	}
	if i < 1 {
		return "a library HTTP client named without a call"
	}
	call, ok := stack[i-1].(*ast.CallExpr)
	if !ok || call.Fun != fun {
		return "a library HTTP client named without a call"
	}
	sources := 0
	for _, arg := range call.Args {
		tv, ok := info.Types[arg]
		if !ok || tv.Type == nil || !types.Implements(tv.Type, tokenSource) {
			continue
		}
		sources++
		if !boundaryMade(tv.Type) {
			return fmt.Sprintf("a library HTTP client over %s, a token source the boundary did not make", types.ExprString(arg))
		}
	}
	if sources == 0 {
		return "a library HTTP client built over a token source of the library's own, whose refusal its requests answer raw through net/http"
	}
	return ""
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
