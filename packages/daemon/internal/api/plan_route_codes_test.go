package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The sources that say what register_plan and stop_task answer of their own: plan.go, the routes'
// own file, whose functions write the refusals of the two routes (writeFailure), the workflow,
// whose planGiven and taskStopped decide each fact the routes commit, and intake, whose
// constructors write each refusal the workflow answers with. The refusals of the helpers every
// architect route shares (the grant's redemption, the body's decoding, the fact's intake) are no
// part of this census: they are no answer of these two routes alone.
const (
	planRouteFile     = "plan.go"
	planRouteWorkflow = "../workflow/*.go"
	planRouteIntake   = "../intake/*.go"
)

// historyEntry opens an entry of version.go's contract history: `// <n>: ` at the start of a
// comment line.
var historyEntry = regexp.MustCompile(`(?m)^// \d+: `)

// Every refusal code register_plan and stop_task answer of their own is named in an entry of the
// daemon API contract history (version.go), which is where a plugin's author reads what a route
// answers. The codes are read from the source, not copied: each code a function of plan.go
// reachable from the two routes writes, and each code of a refusal that the workflow's planGiven
// or taskStopped, or a function of the workflow they reach, answers the fact with. A code a change
// adds to either route fails here until an entry names it. The control is the newest code taken
// out of its entry, which this test then names.
func TestEveryCodeThePlanRoutesAnswerIsInTheContractHistory(t *testing.T) {
	intakeCodes := refusalCodesByFunc(t, parseSource(t, planRouteIntake))
	route := reachedCodes(t, parseSource(t, planRouteFile), []string{"registerPlan", "stopTask"}, intakeCodes)
	workflow := reachedCodes(t, parseSource(t, planRouteWorkflow), []string{"planGiven", "taskStopped"}, intakeCodes)
	if !slices.Contains(workflow, "TREE_LINGERING") || !slices.Contains(route, "VERSION_NOT_IN_DISPATCH") {
		t.Fatalf("the reading of the source found %v in the routes and %v in the workflow, without a code each must hold: the reading itself is broken, and a census of nothing passes nothing",
			route, workflow)
	}
	source, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatal(err)
	}
	first := historyEntry.FindIndex(source)
	if first == nil {
		t.Fatal("version.go holds no `// <n>: ` entry: the contract history could not be read")
	}
	history := string(source[first[0]:])
	codes := slices.Compact(slices.Sorted(slices.Values(append(route, workflow...))))
	for _, code := range codes {
		if !regexp.MustCompile(`\b` + code + `\b`).MatchString(history) {
			t.Errorf("register_plan or stop_task can answer %s, and no entry of version.go's contract history names it", code)
		}
	}
	t.Logf("codes the two routes answer of their own, each held to the history: %v", codes)
}

// planSource is the non-test source a pattern matches: each function and method by its name, and
// the names its files import packages under.
type planSource struct {
	funcs   map[string][]*ast.FuncDecl
	imports map[string]bool
}

// parseSource reads the non-test Go files pattern matches.
func parseSource(t *testing.T, pattern string) planSource {
	t.Helper()
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	read := planSource{funcs: map[string][]*ast.FuncDecl{}, imports: map[string]bool{}}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, spec := range parsed.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			name := filepath.Base(path)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			read.imports[name] = true
		}
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				read.funcs[fn.Name.Name] = append(read.funcs[fn.Name.Name], fn)
			}
		}
	}
	if len(read.funcs) == 0 {
		t.Fatalf("%s holds no function: the census read nothing there", pattern)
	}
	return read
}

// refusalCodesByFunc is, for each function of source, the codes of the refusals it builds.
func refusalCodesByFunc(t *testing.T, source planSource) map[string][]string {
	t.Helper()
	codes := map[string][]string{}
	for name, decls := range source.funcs {
		for _, decl := range decls {
			ast.Inspect(decl.Body, func(node ast.Node) bool {
				if code := literalCode(node); code != "" {
					codes[name] = append(codes[name], code)
				}
				return true
			})
		}
	}
	if len(codes) == 0 {
		t.Fatal("no function of intake builds a refusal with a code: the census read nothing there")
	}
	return codes
}

// reachedCodes is every code a function of source reachable from start writes: a string literal
// passed for a parameter named code (writeFailure's, the workflow's refused), a Failure or Refusal
// literal's Code, and the codes of each intake constructor it calls (intakeCodes).
func reachedCodes(t *testing.T, source planSource, start []string, intakeCodes map[string][]string) []string {
	t.Helper()
	seen := map[string]bool{}
	queue := slices.Clone(start)
	var codes []string
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		decls, ok := source.funcs[name]
		if !ok {
			t.Fatalf("no function %s in the source read", name)
		}
		for _, decl := range decls {
			ast.Inspect(decl.Body, func(node ast.Node) bool {
				if code := literalCode(node); code != "" {
					codes = append(codes, code)
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					callee = fun.Name
					if callee == "writeFailure" && len(call.Args) == 4 {
						if code, ok := stringLiteral(call.Args[2]); ok {
							codes = append(codes, code)
						}
					}
				case *ast.SelectorExpr:
					x, ok := fun.X.(*ast.Ident)
					switch {
					case ok && x.Name == "intake":
						codes = append(codes, intakeCodes[fun.Sel.Name]...)
					case ok && source.imports[x.Name]:
					default:
						callee = fun.Sel.Name
					}
				}
				if callees, local := source.funcs[callee]; local {
					codes = append(codes, codeArguments(call, callees)...)
					queue = append(queue, callee)
				}
				return true
			})
		}
	}
	return codes
}

// codeArguments is each string literal call passes for a parameter its callee names code.
func codeArguments(call *ast.CallExpr, callees []*ast.FuncDecl) []string {
	var codes []string
	for _, callee := range callees {
		at := 0
		for _, param := range callee.Type.Params.List {
			for _, name := range param.Names {
				if name.Name == "code" && at < len(call.Args) {
					if code, ok := stringLiteral(call.Args[at]); ok {
						codes = append(codes, code)
					}
				}
				at++
			}
		}
	}
	return codes
}

// literalCode is the Code of a Failure, PlanRefusal or Refusal literal (intake's or this
// package's), and "" for any other node.
func literalCode(node ast.Node) string {
	literal, ok := node.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	var typeName string
	switch typ := literal.Type.(type) {
	case *ast.Ident:
		typeName = typ.Name
	case *ast.SelectorExpr:
		typeName = typ.Sel.Name
	}
	if typeName != "Failure" && typeName != "PlanRefusal" && typeName != "Refusal" {
		return ""
	}
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := field.Key.(*ast.Ident); ok && key.Name == "Code" {
			if code, ok := stringLiteral(field.Value); ok {
				return code
			}
		}
	}
	return ""
}

// stringLiteral is expr's value where it is a string literal.
func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}
