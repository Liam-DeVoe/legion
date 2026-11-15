//go:build ignore

// Daemon-routes prints each route a Legion daemon's API package registers, `<METHOD> <path>` one
// to a line in byte order, read out of that package's source (routes):
//
//	go run daemon-routes.go <api package dir>
//
// It is daemon-pane.go's one reading that needs nothing of the daemon but its files, so it is a
// program of its own that imports the standard library alone and belongs to no module:
// daemon-pane.ts runs it with `go run` outside every module, where building it compiles a few
// standard packages and downloads nothing. Run as a command of daemon-pane.go, the same reading
// built the daemon module's whole dependency graph first, and a Go cache that held none of it (a
// child given a HOME of its own on a runner that sets no GOCACHE) spent more than a minute on
// that before reading one file. The build constraint keeps it out of every package build;
// `go run` of the file by name builds it all the same.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: daemon-routes <api package dir>")
		os.Exit(2)
	}
	registered, err := routes(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon-routes: %v\n", err)
		os.Exit(1)
	}
	for _, route := range registered {
		fmt.Println(route)
	}
}

// routes is every route the API package at `dir` registers, each as the pattern its registration
// names (`<METHOD> <path>`), in byte order and once each: the string literal a
// `<mux>.HandleFunc(…)` or `<mux>.Handle(…)` call of the package's own files names first. It is
// read out of the source rather than listed in the rig, so the stand-in of each label answers the
// routes that label's daemon serves (daemon-standin.ts), and a route the daemon adds is one the
// rig reads with no edit. The pattern `/` names no route: it is the mux's remainder, every request
// no other pattern matches, which the daemon's catch-all answers 404 ("no route"), as the stand-in
// answers a route its label does not serve. Any other pattern naming no method is an error, since
// what it serves is every method of a path and the rig reads a route as one method and one path;
// so is a package registering none, never an empty set: the stand-in would answer every route 404
// and health would call that the daemon's own answer.
func routes(dir string) ([]string, error) {
	sources, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("list the daemon's %s: %w", dir, err)
	}
	seen := map[string]bool{}
	var methodless []string
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("read the daemon's %s: %w", source, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall || len(call.Args) != 2 {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector || (selector.Sel.Name != "HandleFunc" && selector.Sel.Name != "Handle") {
				return true
			}
			literal, isLiteral := call.Args[0].(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(literal.Value)
			switch {
			case err != nil || pattern == "/":
			case strings.Contains(pattern, " "):
				seen[pattern] = true
			default:
				methodless = append(methodless, pattern)
			}
			return true
		})
	}
	if len(methodless) > 0 {
		return nil, fmt.Errorf("%s registers %q with no method, a route this reading cannot name as one method and one path", dir, methodless)
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%s registers no route with a HandleFunc or Handle call naming its pattern: the daemon registers its routes some other way now", dir)
	}
	registered := make([]string, 0, len(seen))
	for pattern := range seen {
		registered = append(registered, pattern)
	}
	slices.Sort(registered)
	return registered, nil
}
