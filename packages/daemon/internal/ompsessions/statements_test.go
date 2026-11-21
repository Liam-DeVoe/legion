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

// helperName is the one function of this package that runs a statement: inTransaction, whose
// transaction pins search_path before anything else runs in it.
const helperName = "inTransaction"

// driverPackage reports whether path is a package whose methods and functions the check watches:
// pgx and every package under it (pgconn, pgxpool, pgtype, pgproto3), and database/sql with its
// driver package. Every method or function of these packages runs a statement, or may, unless
// nonStatementMethods or nonStatementFunctions lists it: the check denies by default, so a
// method pgx adds, or one it already has and nobody listed (LoadType, Prepare, ExecStatement,
// StartPipeline, Hijack), counts as a statement.
func driverPackage(path string) bool {
	return path == "github.com/jackc/pgx/v5" || strings.HasPrefix(path, "github.com/jackc/pgx/v5/") ||
		path == "database/sql" || path == "database/sql/driver"
}

// nonStatementMethods are the methods of those packages, by name, that send no statement the
// server resolves names in, and so may be called outside the helper: ending a handle (Close,
// Release, Commit and Rollback, whose COMMIT and ROLLBACK name nothing), reading what a handle is
// (Config, TypeMap, PID, TxStatus, ParameterStatus, IsClosed), handing out the handle under
// another (Conn, PgConn: a statement on what they hand out is a method of its own, which the
// check reads), and reading a result a statement already returned (Next, Scan, Values, RawValues,
// Err, FieldDescriptions, CommandTag, RowsAffected and the command tag's Insert, Update, Delete and
// Select, and an error's Error, SQLState and Unwrap). Ping is not one: it sends a statement. The
// list matches a method's name alone, so a name on it that a driver type uses for something else
// is denied by that type (rawConnection).
var nonStatementMethods = []string{
	"Close", "Release", "Commit", "Rollback", "Config", "TypeMap", "PID", "TxStatus", "ParameterStatus", "IsClosed",
	"Conn", "PgConn", "Next", "Scan", "Values", "RawValues", "Err", "FieldDescriptions", "CommandTag", "RowsAffected",
	"Insert", "Update", "Delete", "Select", "String", "Error", "SQLState", "Unwrap",
}

// rawConnection is the one method nonStatementMethods names that hands out no driver handle:
// pgconn.PgConn's Conn, the raw net.Conn under the connection, on which any bytes of the protocol
// can be written, and whose package the check does not watch. It is denied as Hijack is.
const rawConnection = "github.com/jackc/pgx/v5/pgconn.PgConn.Conn"

// nonStatementFunctions are the package functions of those packages that run no statement: pgx's
// Connect, ConnectConfig and ParseConfig, which read a URL and open a connection with its startup
// packet alone, and database/sql's Open, which opens nothing until a statement runs.
var nonStatementFunctions = []string{"Connect", "ConnectConfig", "ParseConfig", "Open"}

// connectionTypes are the handles that run a statement on their own connection rather than in a
// transaction. A statement on one runs outside the helper's transaction even inside a function
// literal handed to the helper, so only the transaction is the literal's to use.
var connectionTypes = []string{
	"github.com/jackc/pgx/v5.Conn", "github.com/jackc/pgx/v5/pgxpool.Pool", "github.com/jackc/pgx/v5/pgxpool.Conn",
	"github.com/jackc/pgx/v5/pgconn.PgConn", "database/sql.DB", "database/sql.Conn",
}

// statementsOutside is every place in files, which info type-checked, where a statement can run
// outside helper, in source order, and how many places it found where one runs inside it. A place
// is a call of, or a method value taken from, a method or a function of a driver package
// (driverPackage) that no allowlist above names; a field and a type are no place. Inside is the
// helper's own body, and, in the body of a function literal handed to a call of the helper, a
// method whose receiver expression starts with a parameter of that literal or a variable it
// declares, which is the transaction the helper opened and what was read from it, unless the
// method is a connection type's, which runs beside that transaction. A method on anything else
// there, a caller's transaction say, runs outside the helper's transaction, and so does a
// function, which runs on whatever handle it is given. A helper of nil is none, so every place
// is outside.
//
// What it cannot see is a driver value converted to an interface of the package's own and run
// through that, whose method no driver package declares, and a literal's variable assigned a
// handle from outside the literal; nothing in the package does either.
func statementsOutside(fset *token.FileSet, files []*ast.File, info *types.Info, helper *types.Func) (outside []string, inside int) {
	var body *ast.BlockStmt
	var literals []*ast.FuncLit
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if helper != nil && info.Defs[node.Name] == helper {
					body = node.Body
				}
			case *ast.CallExpr:
				if helper == nil || calledFunc(info, node) != helper {
					return true
				}
				for _, argument := range node.Args {
					if literal, ok := ast.Unparen(argument).(*ast.FuncLit); ok {
						literals = append(literals, literal)
					}
				}
			}
			return true
		})
	}
	within := func(node ast.Node, pos token.Pos) bool { return node.Pos() <= pos && pos < node.End() }
	inHelper := func(pos token.Pos) bool { return body != nil && within(body, pos) }
	inLiteral := func(pos token.Pos) bool {
		return slices.ContainsFunc(literals, func(literal *ast.FuncLit) bool { return within(literal.Body, pos) })
	}
	// The literals' own objects: each parameter, and each name a literal's body declares.
	own := map[types.Object]bool{}
	for ident, object := range info.Defs {
		if object != nil && slices.ContainsFunc(literals, func(literal *ast.FuncLit) bool {
			return within(literal.Type.Params, ident.Pos()) || within(literal.Body, ident.Pos())
		}) {
			own[object] = true
		}
	}
	type place struct {
		pos  token.Pos
		what string
	}
	var places []place
	for selector, selection := range info.Selections {
		method, ok := selection.Obj().(*types.Func)
		if !ok || method.Pkg() == nil || !driverPackage(method.Pkg().Path()) {
			continue
		}
		key, name := declaringType(method, selection)
		if slices.Contains(nonStatementMethods, method.Name()) && key+"."+method.Name() != rawConnection {
			continue
		}
		receiver := leftmost(selector.X)
		literalOwn := receiver != nil && own[info.Uses[receiver]]
		if inHelper(selector.Pos()) || inLiteral(selector.Pos()) && literalOwn && !slices.Contains(connectionTypes, key) {
			inside++
			continue
		}
		places = append(places, place{selector.Sel.Pos(), fmt.Sprintf("%s (a method of %s)", types.ExprString(selector), name)})
	}
	for ident, object := range info.Uses {
		function, ok := object.(*types.Func)
		if !ok || function.Pkg() == nil || !driverPackage(function.Pkg().Path()) || slices.Contains(nonStatementFunctions, function.Name()) {
			continue
		}
		if function.Type().(*types.Signature).Recv() != nil {
			continue
		}
		// A function takes the handle it runs on as an argument, which may be the connection
		// beside the helper's transaction, so only the helper's own body may call one.
		if inHelper(ident.Pos()) {
			inside++
			continue
		}
		places = append(places, place{ident.Pos(), fmt.Sprintf("%s.%s (a function of %s)", function.Pkg().Name(), function.Name(), function.Pkg().Name())})
	}
	slices.SortFunc(places, func(a, b place) int { return cmp.Compare(a.pos, b.pos) })
	name := "<no helper>"
	if helper != nil {
		name = helper.Name()
	}
	for _, p := range places {
		outside = append(outside, describePlace(fset.Position(p.pos), p.what, name))
	}
	return outside, inside
}

// leftmost is the identifier a receiver expression starts from: tx in tx.QueryRow(…).Scan, conn
// in conn.PgConn(). It answers nil for an expression that starts from no identifier.
func leftmost(expression ast.Expr) *ast.Ident {
	for {
		switch e := ast.Unparen(expression).(type) {
		case *ast.Ident:
			return e
		case *ast.SelectorExpr:
			expression = e.X
		case *ast.CallExpr:
			expression = e.Fun
		case *ast.IndexExpr:
			expression = e.X
		case *ast.IndexListExpr:
			expression = e.X
		case *ast.StarExpr:
			expression = e.X
		case *ast.TypeAssertExpr:
			expression = e.X
		default:
			return nil
		}
	}
}

// describePlace is how statementsOutside names a place where a statement runs outside helper:
// its file and line, what runs there, and the helper's name.
func describePlace(position token.Position, what, helper string) string {
	return fmt.Sprintf("%s:%d: %s runs a statement outside %s", filepath.Base(position.Filename), position.Line, what, helper)
}

// calledFunc is the function call names, or nil where it names none.
func calledFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	var ident *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		ident = fun
	case *ast.SelectorExpr:
		ident = fun.Sel
	default:
		return nil
	}
	function, _ := info.Uses[ident].(*types.Func)
	return function
}

// declaringType is the named type that declares method, as a key of connectionTypes and as a
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
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
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

// Every statement the package runs runs inside inTransaction, whose transaction pins search_path
// before anything else runs in it (TestEveryPoolPinsItsSearchPath reads that pin), so the pin
// covers every statement: outside that helper no method or function of pgx's or database/sql's
// packages is called, or taken as a value, but the few nonStatementMethods and
// nonStatementFunctions name, and inside a literal handed to the helper a method runs only on the
// transaction the literal was handed or what the literal read from it (statementsOutside). Which
// methods are watched is decided by the package that declares them, never by their name; which
// are allowed is decided by their name, but for the one name a driver type uses for a raw socket
// (rawConnection). The check type-checks the package's own files, test files aside, since those
// seed fixtures. Its controls are the fixtures of testdata/outside, each type-checked alone with a
// helper of its own: a pgx call outside the helper, a database/sql call, a pool's connection
// acquired and run, and the other ways a transaction the helper did not open runs a statement. The
// check must name exactly the lines their want comments mark and none of the calls inside their
// helper, and it must find statements inside the helper in each, or it read nothing.
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
		helper := helperOf(pkg)
		if helper == nil {
			t.Errorf("the package declares no function %s, the helper every statement runs through", helperName)
		}
		outside, inside := statementsOutside(fset, parsed, info, helper)
		for _, place := range outside {
			t.Error(place)
		}
		if helper != nil && inside == 0 {
			t.Errorf("UNKNOWN: the check found no statement inside %s in %v, so it read none of the package's statements", helperName, files)
		}
		t.Logf("%d statements inside %s in %v", inside, helperName, files)
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
			helper := helperOf(pkg)
			if helper == nil {
				t.Fatalf("the fixture declares no function %s", helperName)
			}
			want := wanted(fset, parsed[0])
			if len(want) == 0 {
				t.Fatal("the fixture carries no want comment, so it controls nothing")
			}
			outside, inside := statementsOutside(fset, parsed, info, helper)
			if !slices.Equal(outside, want) {
				t.Errorf("the check named\n\t%s\nwant\n\t%s", strings.Join(outside, "\n\t"), strings.Join(want, "\n\t"))
			}
			if inside == 0 {
				t.Errorf("the check found no statement inside the fixture's %s, which runs some", helperName)
			}
		})
	}
}
