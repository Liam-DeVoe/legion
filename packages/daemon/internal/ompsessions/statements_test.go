package ompsessions

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/testtypes"
)

// helperName is the one function of this package that opens a transaction: inTransaction, whose
// transaction pins search_path, and the rest, before anything else runs in it.
const helperName = "inTransaction"

// pinnedName is the type of the transaction the helper hands its function: only the helper builds
// one, and only its methods run a statement on it.
const pinnedName = "pinned"

// driverPackage reports whether path is a package whose methods and functions the check watches:
// pgx and every package under it (pgconn, pgxpool, pgtype, pgproto3), and database/sql with its
// driver package. Every method or function of these packages runs a statement, or may, unless
// allowedMethods or allowedFunctions names it: the check denies by default, so a method pgx adds,
// or one it has and nobody listed (LoadType, Prepare, ExecStatement, StartPipeline, Hijack, a large
// object's Close), counts as a statement.
func driverPackage(path string) bool {
	return path == "github.com/jackc/pgx/v5" || strings.HasPrefix(path, "github.com/jackc/pgx/v5/") ||
		path == "database/sql" || path == "database/sql/driver"
}

// allowedMethods are the methods of those packages, each by the type that declares it, that send
// no statement the server resolves a name in, and so may be called anywhere: ending a handle
// (closing a connection, releasing a pooled one, ending a transaction with COMMIT or ROLLBACK,
// which name nothing, closing a result set or a result reader), handing out the PgConn under a
// connection (a statement on it is a method of its own, which the check reads), and reading a
// result a statement already returned. A method of the same name on another type is not allowed:
// a large object's Close runs select lo_close, and a PgConn's Conn is the raw socket.
var allowedMethods = []string{
	"github.com/jackc/pgx/v5.Conn.Close",
	"github.com/jackc/pgx/v5.Conn.PgConn",
	"github.com/jackc/pgx/v5.Tx.Commit",
	"github.com/jackc/pgx/v5.Tx.Rollback",
	"github.com/jackc/pgx/v5.Row.Scan",
	"github.com/jackc/pgx/v5.Rows.Next",
	"github.com/jackc/pgx/v5.Rows.Scan",
	"github.com/jackc/pgx/v5.Rows.Close",
	"github.com/jackc/pgx/v5.Rows.Err",
	"github.com/jackc/pgx/v5/pgconn.CommandTag.RowsAffected",
	"github.com/jackc/pgx/v5/pgconn.MultiResultReader.Close",
	"github.com/jackc/pgx/v5/pgconn.ResultReader.Close",
	"github.com/jackc/pgx/v5/pgxpool.Conn.Release",
	"database/sql.Tx.Commit",
	"database/sql.Tx.Rollback",
	"database/sql.Row.Scan",
	"database/sql.Rows.Close",
}

// allowedFunctions are the package functions of those packages that run no statement: pgx's
// Connect, ConnectConfig and ParseConfig, which read a URL and open a connection with its startup
// packet alone, and database/sql's Open, which opens nothing until a statement runs.
var allowedFunctions = []string{
	"github.com/jackc/pgx/v5.Connect",
	"github.com/jackc/pgx/v5.ConnectConfig",
	"github.com/jackc/pgx/v5.ParseConfig",
	"database/sql.Open",
}

// place is where statementsOutside found a statement can run outside the helper, and what runs.
type place struct {
	pos  token.Pos
	what string
}

// statementsOutside is every place in files, which info type-checked as pkg, where a statement
// can run outside the helper, in source order, and how many places it found where one runs inside
// it. A place is:
//
//   - a call of, or a method value taken from, a method or a function of a driver package
//     (driverPackage) that allowedMethods or allowedFunctions does not name, outside the helper's
//     own body and the bodies of pinned's methods;
//   - a pinned built, by a composite literal or a conversion, outside the helper's body, since
//     only the transaction the helper pinned may be one;
//   - an interface, declared or written as a constraint anywhere in the files, that a type of a
//     driver package implements: a driver handle held in one runs its statements through methods
//     no driver package declares, which the first rule cannot see.
//
// Inside is a watched call in the helper's body or a method of pinned. With no helper every place
// is outside.
func statementsOutside(fset *token.FileSet, files []*ast.File, info *types.Info, pkg *types.Package) (outside []string, inside int) {
	helper := helperOf(pkg)
	pinnedType, _ := pkg.Scope().Lookup(pinnedName).(*types.TypeName)
	var helperBody *ast.BlockStmt
	var bodies []*ast.BlockStmt
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			if helper != nil && function.Recv == nil && info.Defs[function.Name] == helper {
				helperBody = function.Body
				bodies = append(bodies, function.Body)
			}
			if pinnedType != nil && function.Recv != nil && len(function.Recv.List) == 1 {
				receiver := info.Types[function.Recv.List[0].Type].Type
				if pointer, ok := receiver.(*types.Pointer); ok {
					receiver = pointer.Elem()
				}
				if named, ok := receiver.(*types.Named); ok && named.Obj() == pinnedType {
					bodies = append(bodies, function.Body)
				}
			}
		}
	}
	within := func(body *ast.BlockStmt, pos token.Pos) bool {
		return body != nil && body.Pos() <= pos && pos < body.End()
	}
	inBodies := func(pos token.Pos) bool {
		return slices.ContainsFunc(bodies, func(body *ast.BlockStmt) bool { return within(body, pos) })
	}
	var places []place
	for selector, selection := range info.Selections {
		method, ok := selection.Obj().(*types.Func)
		if !ok || method.Pkg() == nil || !driverPackage(method.Pkg().Path()) {
			continue
		}
		key, name := declaringType(method, selection)
		if slices.Contains(allowedMethods, key+"."+method.Name()) {
			continue
		}
		if inBodies(selector.Pos()) {
			inside++
			continue
		}
		places = append(places, place{selector.Sel.Pos(), fmt.Sprintf("%s (a method of %s)", types.ExprString(selector), name)})
	}
	for ident, object := range info.Uses {
		function, ok := object.(*types.Func)
		if !ok || function.Pkg() == nil || !driverPackage(function.Pkg().Path()) || function.Type().(*types.Signature).Recv() != nil {
			continue
		}
		if slices.Contains(allowedFunctions, function.Pkg().Path()+"."+function.Name()) {
			continue
		}
		if inBodies(ident.Pos()) {
			inside++
			continue
		}
		places = append(places, place{ident.Pos(), fmt.Sprintf("%s.%s (a function of %s)", function.Pkg().Name(), function.Name(), function.Pkg().Name())})
	}
	handles := driverTypes(pkg)
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CompositeLit, *ast.CallExpr:
				expression := node.(ast.Expr)
				built := info.Types[expression].Type
				if call, ok := node.(*ast.CallExpr); ok {
					if !info.Types[call.Fun].IsType() {
						return true
					}
				}
				if named, ok := built.(*types.Named); ok && pinnedType != nil && named.Obj() == pinnedType && !within(helperBody, node.Pos()) {
					places = append(places, place{node.Pos(), fmt.Sprintf("%s (a %s transaction built outside %s)", types.ExprString(expression), pinnedName, helperName)})
				}
			case *ast.InterfaceType:
				iface, ok := info.Types[node].Type.Underlying().(*types.Interface)
				if !ok || iface.NumMethods() == 0 {
					return true
				}
				if slices.ContainsFunc(handles, func(handle types.Type) bool { return types.Implements(handle, iface) }) {
					places = append(places, place{node.Pos(), "an interface a driver handle implements"})
				}
			}
			return true
		})
	}
	slices.SortFunc(places, func(a, b place) int { return cmp.Compare(a.pos, b.pos) })
	for _, p := range places {
		outside = append(outside, describePlace(fset.Position(p.pos), p.what, helperName))
	}
	return outside, inside
}

// driverTypes is every named type the driver packages pkg imports, directly or not, declare, and
// a pointer to each that is no interface: what a driver handle can be.
func driverTypes(pkg *types.Package) []types.Type {
	var handles []types.Type
	seen := map[*types.Package]bool{}
	var walk func(*types.Package)
	walk = func(imported *types.Package) {
		if seen[imported] {
			return
		}
		seen[imported] = true
		if driverPackage(imported.Path()) {
			scope := imported.Scope()
			for _, name := range scope.Names() {
				typeName, ok := scope.Lookup(name).(*types.TypeName)
				if !ok || typeName.IsAlias() {
					continue
				}
				named, ok := typeName.Type().(*types.Named)
				if !ok || named.TypeParams().Len() > 0 {
					continue
				}
				handles = append(handles, named)
				if !types.IsInterface(named) {
					handles = append(handles, types.NewPointer(named))
				}
			}
		}
		for _, next := range imported.Imports() {
			walk(next)
		}
	}
	walk(pkg)
	return handles
}

// describePlace is how statementsOutside names a place where a statement runs outside helper:
// its file and line, what runs there, and the helper's name.
func describePlace(position token.Position, what, helper string) string {
	return fmt.Sprintf("%s:%d: %s runs a statement outside %s", filepath.Base(position.Filename), position.Line, what, helper)
}

// declaringType is the named type that declares method, as a key of allowedMethods and as a
// reader writes it (*pgx.Conn, pgx.Tx). A method an interface declares without naming it is read
// from the selection's receiver.
func declaringType(method *types.Func, selection *types.Selection) (key, name string) {
	receiver := method.Type().(*types.Signature).Recv().Type()
	if _, ok := types.Unalias(receiver).(*types.Interface); ok {
		receiver = selection.Recv()
	}
	pointer := ""
	if p, ok := types.Unalias(receiver).(*types.Pointer); ok {
		receiver, pointer = p.Elem(), "*"
	}
	named, ok := types.Unalias(receiver).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", ""
	}
	object := named.Obj()
	return object.Pkg().Path() + "." + object.Name(), pointer + object.Pkg().Name() + "." + object.Name()
}

// typeCheck parses files and type-checks them as one package, reading what they import from
// where the compiler left it for each of patterns (testtypes.CompiledDependencies).
func typeCheck(t *testing.T, path string, files []string, patterns ...string) (*token.FileSet, []*ast.File, *types.Info, *types.Package) {
	t.Helper()
	var lookups []importer.Lookup
	for _, pattern := range patterns {
		lookup, err := testtypes.CompiledDependencies(pattern)
		if err != nil {
			t.Fatal(err)
		}
		lookups = append(lookups, lookup)
	}
	lookup := func(path string) (io.ReadCloser, error) {
		var errs []string
		for _, lookup := range lookups {
			opened, err := lookup(path)
			if err == nil {
				return opened, nil
			}
			errs = append(errs, err.Error())
		}
		return nil, fmt.Errorf("no compiled types for %s: %s", path, strings.Join(errs, "; "))
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	for _, file := range files {
		syntax, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		parsed = append(parsed, syntax)
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	config := types.Config{Importer: importer.ForCompiler(fset, "gc", lookup)}
	pkg, err := config.Check(path, fset, parsed, info)
	if err != nil {
		t.Fatalf("type-check %s: %v", strings.Join(files, ", "), err)
	}
	return fset, parsed, info, pkg
}

// helperOf is pkg's helper, nil where pkg declares none.
func helperOf(pkg *types.Package) *types.Func {
	helper, _ := pkg.Scope().Lookup(helperName).(*types.Func)
	return helper
}

// wantComment is a fixture's mark of a line the check must name: `// want "<what>"`, what being
// the place as statementsOutside describes it.
var wantComment = regexp.MustCompile(`^// want "(.*)"$`)

// wanted is what a fixture's want comments say the check names, each with its file and line, as
// statementsOutside writes a place.
func wanted(fset *token.FileSet, file *ast.File) []string {
	var want []string
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if match := wantComment.FindStringSubmatch(comment.Text); match != nil {
				want = append(want, describePlace(fset.Position(comment.Pos()), match[1], helperName))
			}
		}
	}
	return want
}

// Every statement the package runs runs inside inTransaction, whose transaction pins search_path,
// and the rest, before anything else runs in it (TestEveryTransactionPinsBeforeItsWork holds that
// order, and TestEveryPoolPinsItsSearchPath reads the pin), so the pin covers every statement.
// The helper hands its function a pinned, the one type whose methods run a statement, and only the
// helper builds one. So outside the helper's body and pinned's methods no method or function of
// pgx's or database/sql's packages is called, or taken as a value, but the few allowedMethods,
// each named with its type, and allowedFunctions name; no pinned is built; and no interface the
// package declares or writes is one a driver handle implements (statementsOutside). The check
// type-checks the package's own files, test files aside, since those seed fixtures. Its controls
// are the fixtures of testdata/outside, each type-checked alone with a helper and a pinned of its
// own: a pgx call outside the helper, a database/sql call, a pool's connection acquired and run,
// the other ways a transaction the helper did not open runs a statement, and the Go shapes that
// carry one into the helper's function or a driver handle past the check. The check must name
// exactly the lines their want comments mark and none of the calls inside their helper, and it
// must find statements inside the helper in each, or it read nothing.
func TestNoStatementRunsOutsideTheHelper(t *testing.T) {
	t.Run("this package", func(t *testing.T) {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}
		var files []string
		for _, entry := range entries {
			if name := entry.Name(); strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				files = append(files, name)
			}
		}
		if !slices.Contains(files, "ompsessions.go") {
			t.Fatalf("UNKNOWN: the check read %v, which holds no ompsessions.go: it runs in the package's own directory", files)
		}
		const path = "github.com/sjawhar/legion/daemon/internal/ompsessions"
		fset, parsed, info, pkg := typeCheck(t, path, files, path)
		if helperOf(pkg) == nil {
			t.Errorf("the package declares no function %s, the helper every statement runs through", helperName)
		}
		if _, ok := pkg.Scope().Lookup(pinnedName).(*types.TypeName); !ok {
			t.Errorf("the package declares no type %s, the transaction %s hands its function", pinnedName, helperName)
		}
		outside, inside := statementsOutside(fset, parsed, info, pkg)
		for _, place := range outside {
			t.Error(place)
		}
		if inside == 0 {
			t.Errorf("UNKNOWN: the check found no statement inside %s or %s's methods in %v, so it read none of the package's statements", helperName, pinnedName, files)
		}
		t.Logf("%d statements inside %s and %s's methods in %v", inside, helperName, pinnedName, files)
	})

	fixtures, err := filepath.Glob(filepath.Join("testdata", "outside", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("UNKNOWN: testdata/outside holds no fixture, so no control ran")
	}
	for _, fixture := range fixtures {
		t.Run("control "+filepath.Base(fixture), func(t *testing.T) {
			fset, parsed, info, pkg := typeCheck(t, "fixture", []string{fixture},
				"github.com/jackc/pgx/v5/pgxpool", "database/sql")
			if helperOf(pkg) == nil {
				t.Fatalf("the fixture declares no function %s", helperName)
			}
			want := wanted(fset, parsed[0])
			if len(want) == 0 {
				t.Fatal("the fixture carries no want comment, so it controls nothing")
			}
			outside, inside := statementsOutside(fset, parsed, info, pkg)
			if !slices.Equal(outside, want) {
				t.Errorf("the check named\n\t%s\nwant\n\t%s", strings.Join(outside, "\n\t"), strings.Join(want, "\n\t"))
			}
			if inside == 0 {
				t.Errorf("the check found no statement inside the fixture's %s, which runs some", helperName)
			}
		})
	}
}
