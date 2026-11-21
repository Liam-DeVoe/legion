// account_routes_test.go holds account_powers_test.go's rows to the broker's route table: every
// route the table authenticates with accountAuth has a row there, and every row names a route the
// table has. The rows are written by hand, so without this a new named-caller route would pass
// the powers walk unchecked. It reads the table as source, as cmd/cipher-refgen does, since
// routes() is internal/cipher/api's own and unexported.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// routesTable is the broker's route table, relative to this package's directory, where go test
// runs it.
var routesTable = filepath.Join("..", "..", "internal", "cipher", "api", "routes_table.go")

// TestEveryAccountRouteHasARow: the set of "<method> <pattern>" the route table authenticates with
// accountAuth equals the set accountRoutes names, so a route added to the table without a row, and
// a row left behind by a route the table no longer has, each fail here naming it.
func TestEveryAccountRouteHasARow(t *testing.T) {
	table := accountAuthRoutes(t, routesTable)
	if len(table) == 0 {
		t.Fatalf("%s yields no accountAuth route at all: this test read nothing, so it can say nothing", routesTable)
	}
	rows := map[string]bool{}
	for _, route := range accountRoutes {
		rows[route.method+" "+route.pattern] = true
	}
	for _, route := range slices.Sorted(maps.Keys(table)) {
		if !rows[route] {
			t.Errorf("%s authenticates %s with accountAuth, and accountRoutes (account_powers_test.go) has no row for it: add one naming its caller and its own answer", routesTable, route)
		}
	}
	for _, route := range slices.Sorted(maps.Keys(rows)) {
		if !table[route] {
			t.Errorf("accountRoutes has a row for %s, which %s does not authenticate with accountAuth", route, routesTable)
		}
	}
}

// accountAuthRoutes is every "<method> <pattern>" of a row of path's routes() whose handler is an
// accountAuth call, each row being {http.Method<X>, "<pattern>", accountAuth(…)}. A row of that
// handler it cannot read fails t, so the set is never short of a row it skipped.
func accountAuthRoutes(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	routes := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		row, ok := n.(*ast.CompositeLit)
		if !ok || len(row.Elts) != 3 {
			return true
		}
		call, ok := row.Elts[2].(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); !ok || name.Name != "accountAuth" {
			return true
		}
		method, methodOK := row.Elts[0].(*ast.SelectorExpr)
		pattern, patternOK := row.Elts[1].(*ast.BasicLit)
		if !methodOK || !patternOK || pattern.Kind != token.STRING || !strings.HasPrefix(method.Sel.Name, "Method") {
			t.Fatalf("%s: an accountAuth row this test cannot read (want {http.Method<X>, \"<pattern>\", accountAuth(…)})", path)
		}
		text, err := strconv.Unquote(pattern.Value)
		if err != nil {
			t.Fatalf("%s: the pattern %s: %v", path, pattern.Value, err)
		}
		routes[httpMethod(t, method.Sel.Name)+" "+text] = true
		return false
	})
	return routes
}

// httpMethod is the method net/http's Method<X> constant named by name spells.
func httpMethod(t *testing.T, name string) string {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace} {
		if strings.EqualFold("Method"+method, name) {
			return method
		}
	}
	t.Fatalf("the route table names http.%s, which is no net/http method constant", name)
	return ""
}
