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

// statementMethods are, by the type that declares them, the methods that run a statement on the
// server or hand out something that does: pgx's connections, pools and transactions, the PgConn
// under a pgx connection, and database/sql's handles. A receiver's type decides, never the
// method's name.
var statementMethods = map[string][]string{
	"github.com/jackc/pgx/v5.Conn":          {"Exec", "Query", "QueryRow", "SendBatch", "CopyFrom", "Begin", "BeginTx"},
	"github.com/jackc/pgx/v5.Tx":            {"Exec", "Query", "QueryRow", "SendBatch", "CopyFrom", "Begin"},
	"github.com/jackc/pgx/v5/pgxpool.Pool":  {"Exec", "Query", "QueryRow", "SendBatch", "CopyFrom", "Begin", "BeginTx", "Acquire", "AcquireFunc"},
	"github.com/jackc/pgx/v5/pgxpool.Conn":  {"Exec", "Query", "QueryRow", "SendBatch", "CopyFrom", "Begin", "BeginTx"},
	"github.com/jackc/pgx/v5/pgxpool.Tx":    {"Exec", "Query", "QueryRow", "SendBatch", "CopyFrom", "Begin"},
	"github.com/jackc/pgx/v5/pgconn.PgConn": {"Exec", "ExecParams", "ExecPrepared", "ExecBatch", "CopyFrom", "CopyTo"},
	"database/sql.DB":                       {"QueryContext", "ExecContext", "QueryRowContext", "BeginTx", "PrepareContext", "Query", "Exec", "QueryRow", "Begin", "Prepare"},
	"database/sql.Conn":                     {"QueryContext", "ExecContext", "QueryRowContext", "BeginTx", "PrepareContext"},
	"database/sql.Tx":                       {"QueryContext", "ExecContext", "QueryRowContext", "PrepareContext", "Query", "Exec", "QueryRow", "Prepare"},
}

// statementFunctions are the package functions that open a transaction of their own.
var statementFunctions = map[string][]string{"github.com/jackc/pgx/v5": {"BeginFunc", "BeginTxFunc"}}

// statementsOutside is every place in files, which info type-checked, where a statement can run
// outside helper, in source order, and how many places it found where one runs inside it. Inside
// is the helper's own body, and a method of a parameter of a function literal handed to a call of
// the helper: the transaction the helper opened and gave it. A helper of nil is none, so every
// place is outside.
//
// What it cannot see is a pgx or database/sql value passed as an interface of the package's own
// and run through that, whose methods no type above declares; nothing in the package does that.
func statementsOutside(fset *token.FileSet, files []*ast.File, info *types.Info, helper *types.Func) (outside []string, inside int) {
	var body *ast.BlockStmt
	opened := map[types.Object]bool{}
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
					literal, ok := ast.Unparen(argument).(*ast.FuncLit)
					if !ok {
						continue
					}
					for _, field := range literal.Type.Params.List {
						for _, name := range field.Names {
							opened[info.Defs[name]] = true
						}
					}
				}
			}
			return true
		})
	}
	inHelper := func(pos token.Pos) bool { return body != nil && body.Pos() <= pos && pos < body.End() }
	type place struct {
		pos  token.Pos
		what string
	}
	var places []place
	for selector, selection := range info.Selections {
		method, ok := selection.Obj().(*types.Func)
		if !ok {
			continue
		}
		key, name := declaringType(method, selection)
		if !slices.Contains(statementMethods[key], method.Name()) {
			continue
		}
		receiver, isIdent := ast.Unparen(selector.X).(*ast.Ident)
		if inHelper(selector.Pos()) || isIdent && opened[info.Uses[receiver]] {
			inside++
			continue
		}
		places = append(places, place{selector.Sel.Pos(), fmt.Sprintf("%s (a method of %s)", types.ExprString(selector), name)})
	}
	for ident, object := range info.Uses {
		function, ok := object.(*types.Func)
		if !ok || function.Pkg() == nil || !slices.Contains(statementFunctions[function.Pkg().Path()], function.Name()) {
			continue
		}
		if function.Type().(*types.Signature).Recv() != nil {
			continue
		}
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
		position := fset.Position(p.pos)
		outside = append(outside, fmt.Sprintf("%s:%d: %s runs a statement outside %s", filepath.Base(position.Filename), position.Line, p.what, name))
	}
	return outside, inside
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

// declaringType is the named type that declares method, as a key of statementMethods and as a
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
				position := fset.Position(comment.Pos())
				want = append(want, fmt.Sprintf("%s:%d: %s runs a statement outside %s", filepath.Base(position.Filename), position.Line, match[1], helperName))
			}
		}
	}
	return want
}

// Every statement the package runs runs inside inTransaction, whose transaction pins search_path
// before anything else runs in it (TestEveryPoolPinsItsSearchPath reads that pin), so the pin
// covers every statement: outside that helper no method that runs a statement is called, or
// taken as a value, on a receiver whose type is pgx's or database/sql's, but on the transaction
// the helper opened and handed to a function literal, and pgx.BeginFunc and pgx.BeginTxFunc are
// not called. The receiver's type decides, never the method's name. The check type-checks the
// package's own files, test files aside, since those seed fixtures. Its controls are the
// fixtures of testdata/outside, each type-checked alone with a helper of its own: a pgx call
// outside the helper, a database/sql call, a pool's connection acquired and run, and the other
// ways a transaction the helper did not open runs a statement. The check must name exactly the
// lines their want comments mark and none of the calls inside their helper, and it must find
// statements inside the helper in each, or it read nothing.
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
