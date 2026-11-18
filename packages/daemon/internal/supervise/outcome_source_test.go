package supervise

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	gotoken "go/token"
	"go/types"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/testtypes"
)

// The policy that decides whether a claim's failure charges a budget (Charges) has a chokepoint:
// the charge functions — chargePrompt, chargeDeath and chargeLaunch, through which
// relaunchAfterFailure counts — each ask it, so no site that reaches one can charge what the
// policy waives. But the Go type system cannot require that a failing path call one of them at
// all, and a busy path that decides the busy answer itself, from the agent's words, never reaches
// a charge function. Nor can a type keep a site from reading those words: they reach the daemon as
// an error and as strings, which it must hold to log a failure and to keep a delivery's last send
// error, and the text of either is open to any function that holds it. Of the Claude Code
// bridge's busy refusal, whose words the bridge keeps for a daemon rolled back to the release
// before this one, the compiler holds one part: the words are a constant the bridge does not
// export (claudebridge's busyWords), so no other package can name them. It cannot keep a package
// from writing them out, nor the bridge from using its constant for anything but that refusal.
// So this test reads the source and holds the five things the compiler cannot: every member of
// the set (runtime.Outcome, declared in internal/runtime) has a row in the policy; no site
// matches a refusal's words — not in this package, and not in internal/stream, which mints the
// outcome where the frame is read, or internal/claudebridge, which writes the busy refusal; no
// site holds the busy refusal's words but the refusal itself — none in this package, the stream
// or the runtime, and none in the bridge but its constant and the refusal it writes; every charge
// names a declared member; and no function but a charge function writes a budget counter except
// to reset it to zero (budgetResets names each that may). Delete a check here when the compiler
// comes to hold it.
//
// It type-checks each package's non-test source against the compiler's export data for its
// dependencies (compiledDependencies, as TestProcessEndsListsEveryEndDeclared does), so it reads
// what the compiler resolved — which constant, which field, which function — rather than how a
// line spells it.
func TestOutcomeSourceHoldsThePolicyAtItsSites(t *testing.T) {
	sources := checkOutcomeSources(t)
	src, set := sources[supervisePath], sources[runtimePath]

	t.Run("every declared outcome has a row in Charges", func(t *testing.T) {
		declared := set.declaredOutcomes(t)
		rows := map[string]bool{}
		policy, _ := src.pkg.Scope().Lookup("Charges").(*types.Func)
		charges := src.funcs()[policy]
		if charges == nil {
			t.Fatal("the package declares no function Charges, which this test reads as the policy")
		}
		switches, defaults := 0, 0
		ast.Inspect(charges.Body, func(node ast.Node) bool {
			sw, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			switches++
			for _, stmt := range sw.Body.List {
				clause := stmt.(*ast.CaseClause)
				if clause.List == nil {
					defaults++
				}
				for _, expr := range clause.List {
					value := src.info.Types[expr].Value
					if value == nil || !isOutcome(src.info.Types[expr].Type) {
						t.Errorf("Charges has a row %s that is no constant Outcome", src.pos(expr))
						continue
					}
					rows[value.ExactString()] = true
				}
			}
			return false
		})
		if switches != 1 {
			t.Fatalf("Charges (%s) holds %d switch statements, want the one switch over the set", src.pos(charges), switches)
		}
		// The zero value's row is the default, which is also the row of every value the set does
		// not hold; every other member has a case of its own, so a member added with none fails
		// here by name rather than falling into the default unseen.
		zero := constant.MakeString("").ExactString()
		if defaults != 1 {
			t.Errorf("Charges has %d default clauses, want one: the default is the row of OutcomeUnrecognised and of every value the set does not hold", defaults)
		}
		if rows[zero] {
			t.Errorf("Charges has a case for OutcomeUnrecognised beside its default, which is that row: delete the case")
		}
		for _, member := range declared {
			if member.value != zero && !rows[member.value] {
				t.Errorf("the outcome %s (%s, %s) has no row in Charges: decide whether it charges", member.name, member.value, member.pos)
			}
		}
	})

	t.Run("no site reads a refusal's words", func(t *testing.T) {
		for _, path := range []string{supervisePath, streamPath, claudebridgePath} {
			pkg := sources[path]
			taint := pkg.refusalWords(t)
			for _, file := range pkg.files {
				for _, decl := range file.Decls {
					for _, match := range taint.matches(decl) {
						t.Errorf("%s, in %s, %s matches a refusal's words: the outcome is minted from the frame's code where the frame is read (stream.PromptRefusalOutcome), never from its words",
							pkg.pos(match.at), pkg.declName(decl), match.what)
					}
				}
			}
		}
	})

	t.Run("no site holds the Claude Code bridge's busy words but its busy refusal", func(t *testing.T) {
		bridge := sources[claudebridgePath]
		busy, _ := bridge.pkg.Scope().Lookup("busyWords").(*types.Const)
		if busy == nil || busy.Val().Kind() != constant.String || constant.StringVal(busy.Val()) == "" {
			t.Fatal("internal/claudebridge declares no string constant busyWords, whose words this test holds every other site from holding")
		}
		words, refusal := constant.StringVal(busy.Val()), bridge.busyRefusal(busy)
		for _, path := range []string{supervisePath, streamPath, claudebridgePath, runtimePath} {
			pkg := sources[path]
			var allowed map[ast.Expr]bool
			if path == claudebridgePath {
				allowed = refusal
			}
			for _, file := range pkg.files {
				for _, decl := range file.Decls {
					for _, held := range pkg.busyHolders(decl, words, allowed) {
						t.Errorf("%s, in %s, holds the Claude Code bridge's busy words (claudebridge's busyWords) or a piece of them: only the bridge's busy refusal carries them, for a daemon rolled back to the release before this one, and this one reads a busy agent from the refusal's code (shimwire.CodeSessionBusy), never from its words",
							pkg.pos(held), pkg.declName(decl))
					}
				}
			}
		}
	})

	t.Run("every charge names a declared outcome", func(t *testing.T) {
		members := map[string]bool{}
		for _, member := range set.declaredOutcomes(t) {
			members[member.value] = true
		}
		judge := &outcomeJudge{src: src, members: members, judged: map[types.Object]string{}}
		calls := map[string]int{}
		for _, file := range src.files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				fn := src.callee(call)
				if fn == nil || fn.Pkg() != src.pkg || !slices.Contains(chargeFunctions, fn.Name()) {
					return true
				}
				calls[fn.Name()]++
				for i, arg := range call.Args {
					if !isOutcome(src.info.Types[arg].Type) {
						continue
					}
					if why := judge.expr(arg); why != "" {
						t.Errorf("%s calls %s with argument %d, which is no outcome the set declares: %s", src.pos(call), fn.Name(), i, why)
					}
				}
				return true
			})
		}
		for _, name := range chargeFunctions {
			if calls[name] == 0 {
				t.Errorf("no site calls %s: this test reads the package's charge functions by name, and that one is gone", name)
			}
		}
	})

	t.Run("no function but a charge function or a reset writes a budget counter", func(t *testing.T) {
		counters := src.budgetCounters(t)
		wrote := map[string]bool{}
		for _, file := range src.files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				for _, write := range src.budgetWrites(fn.Body, counters) {
					wrote[fn.Name.Name] = true
					where := fmt.Sprintf("%s (in %s)", src.pos(write.target), fn.Name.Name)
					switch {
					case slices.Contains(chargeFunctions, fn.Name.Name):
					case budgetResets[fn.Name.Name] != "":
						if !write.reset {
							t.Errorf("%s writes a budget counter other than to zero: %s may only reset the counters, and a charge is the charge functions' alone, which ask the policy (Charges)", where, fn.Name.Name)
						}
					default:
						t.Errorf("%s writes a budget counter: only the charge functions (%s) count against a budget, each asking the policy (Charges), and only the resets listed in budgetResets set one back to zero", where, strings.Join(chargeFunctions, ", "))
					}
				}
			}
		}
		for name := range budgetResets {
			if !wrote[name] {
				t.Errorf("budgetResets lists %s, which writes no budget counter: remove it", name)
			}
		}
		for _, name := range chargeFunctions {
			if !wrote[name] && name != "relaunchAfterFailure" {
				t.Errorf("the charge function %s writes no budget counter: this test reads the charge functions by name, and that one has moved", name)
			}
		}
	})
}

// chargeFunctions are the functions that charge a budget. Each asks Charges itself but
// relaunchAfterFailure, which counts through chargeLaunch and writes no counter of its own.
var chargeFunctions = []string{"chargePrompt", "chargeDeath", "relaunchAfterFailure", "chargeLaunch"}

// budgetResets are the functions besides the charge functions that may write a budget counter, and
// each may write one only to zero: a reset is the end of what a budget measures, which is no event
// the policy is asked about.
var budgetResets = map[string]string{
	"confirm":       "a started turn of the task resets the prompt failures and the prompt retirements (Budgets, PromptFailures and PromptRetires)",
	"ready":         "the agent's ready resets the launch failures, since the launch it measured came up (Budgets, LaunchFailures)",
	"turnEnded":     "a turn that ends normally resets the deaths, since the agent got through the work they were charged against (Budgets, Deaths)",
	"retirePending": "the task's retirement resets the deaths charged against it, in the same write (Budgets, Deaths)",
	"retry":         "an operator's retry of a failed claim starts every budget again from nothing (RequestRetry)",
}

// The packages this test reads, by import path.
const (
	internalPath     = "github.com/sjawhar/legion/daemon/internal/"
	supervisePath    = internalPath + "supervise"
	runtimePath      = internalPath + "runtime"
	streamPath       = internalPath + "stream"
	claudebridgePath = internalPath + "claudebridge"
)

// budgetCounters is the fields of Budgets, as the type checker holds them.
func (s *goSource) budgetCounters(t *testing.T) map[types.Object]bool {
	t.Helper()
	named, _ := s.pkg.Scope().Lookup("Budgets").(*types.TypeName)
	if named == nil {
		t.Fatal("the package declares no type Budgets, whose fields this test reads as the budget counters")
	}
	fields := named.Type().Underlying().(*types.Struct)
	counters := map[types.Object]bool{}
	for i := range fields.NumFields() {
		counters[fields.Field(i)] = true
	}
	return counters
}

// budgetWrite is one statement writing a budget counter: target is what it writes, and reset
// whether it writes zero by plain assignment.
type budgetWrite struct {
	target ast.Expr
	reset  bool
}

// budgetWrites is every write in body, function literals included, of a budget counter: an
// assignment, an op-assignment or an increment or decrement whose target is a field of Budgets
// through any selector chain or pointer, or a whole value of type Budgets.
func (s *goSource) budgetWrites(body *ast.BlockStmt, counters map[types.Object]bool) []budgetWrite {
	writes := func(target ast.Expr) bool {
		target = ast.Unparen(target)
		if selector, ok := target.(*ast.SelectorExpr); ok {
			if selection := s.info.Selections[selector]; selection != nil && counters[selection.Obj()] {
				return true
			}
		}
		named, ok := s.info.Types[target].Type.(*types.Named)
		return ok && named.Obj().Pkg() == s.pkg && named.Obj().Name() == "Budgets"
	}
	var found []budgetWrite
	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.IncDecStmt:
			if writes(node.X) {
				found = append(found, budgetWrite{target: node.X})
			}
		case *ast.AssignStmt:
			for i, target := range node.Lhs {
				if !writes(target) {
					continue
				}
				reset := false
				if node.Tok == gotoken.ASSIGN && len(node.Lhs) == len(node.Rhs) {
					reset = s.isZero(node.Rhs[i])
				}
				found = append(found, budgetWrite{target: target, reset: reset})
			}
		}
		return true
	})
	return found
}

// isZero is whether expr is the constant zero or a composite literal with no elements.
func (s *goSource) isZero(expr ast.Expr) bool {
	expr = ast.Unparen(expr)
	if value := s.info.Types[expr].Value; value != nil {
		return value.ExactString() == "0"
	}
	literal, ok := expr.(*ast.CompositeLit)
	return ok && len(literal.Elts) == 0
}

// goSource is one package's non-test source, type-checked (testtypes.CheckPackage), with its files
// in the order of their names (testtypes.Package.Files), so the test reads them, and reports what
// it finds, in the same order on every run.
type goSource struct {
	fileset *gotoken.FileSet
	files   []*ast.File
	pkg     *types.Package
	info    *types.Info
	// given is what assignments answers, read once on its first call: both taints and the judge
	// read it, and it walks every file of the package.
	given map[types.Object][]ast.Expr
}

// checkOutcomeSources type-checks the packages this test reads — this one, the runtime that
// declares the set, the stream that mints its members and the Claude Code bridge — by import path.
func checkOutcomeSources(t *testing.T) map[string]*goSource {
	t.Helper()
	dirs := map[string]string{supervisePath: ".", runtimePath: "../runtime", streamPath: "../stream", claudebridgePath: "../claudebridge"}
	fileset := gotoken.NewFileSet()
	imports := importer.ForCompiler(fileset, "gc", compiledDependencies(t, "../stream", "../claudebridge"))
	sources := map[string]*goSource{}
	for path, dir := range dirs {
		checked, err := testtypes.CheckPackage(fileset, imports, path, dir)
		if err != nil {
			t.Fatal(err)
		}
		sources[path] = &goSource{fileset: fileset, files: checked.Files, pkg: checked.Types, info: checked.Info}
	}
	return sources
}

func (s *goSource) pos(node ast.Node) string { return s.fileset.Position(node.Pos()).String() }

// declName names decl in a failure: the package and the function it declares, a method by its
// receiver's type, or the package's declarations outside any function.
func (s *goSource) declName(decl ast.Decl) string {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return "a package-level declaration of " + s.pkg.Name()
	}
	if fn.Recv == nil {
		return s.pkg.Name() + "." + fn.Name.Name
	}
	receiver := types.TypeString(s.info.Defs[fn.Name].(*types.Func).Signature().Recv().Type(), types.RelativeTo(s.pkg))
	if strings.HasPrefix(receiver, "*") {
		receiver = "(" + receiver + ")"
	}
	return s.pkg.Name() + "." + receiver + "." + fn.Name.Name
}

// isOutcome is whether typ is runtime.Outcome, read from its source or from its export data.
func isOutcome(typ types.Type) bool {
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == runtimePath && named.Obj().Name() == "Outcome"
}

type declaredOutcome struct{ name, value, pos string }

// declaredOutcomes is every constant of type Outcome the package's source declares, however it is
// spelled.
func (s *goSource) declaredOutcomes(t *testing.T) []declaredOutcome {
	t.Helper()
	var declared []declaredOutcome
	for ident, object := range s.info.Defs {
		if c, ok := object.(*types.Const); ok && isOutcome(c.Type()) {
			declared = append(declared, declaredOutcome{name: ident.Name, value: c.Val().ExactString(), pos: s.pos(ident)})
		}
	}
	if len(declared) == 0 {
		t.Fatalf("found no constant of type Outcome in the source of %s, which this test reads as the set", s.pkg.Path())
	}
	slices.SortFunc(declared, func(a, b declaredOutcome) int { return strings.Compare(a.name, b.name) })
	return declared
}

// callee is the function or method a call resolves to, or nil for a conversion, a builtin or a
// call of a function value.
func (s *goSource) callee(call *ast.CallExpr) *types.Func {
	var ident *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		ident = fun
	case *ast.SelectorExpr:
		ident = fun.Sel
	default:
		return nil
	}
	fn, _ := s.info.Uses[ident].(*types.Func)
	return fn
}

// funcs is every function and method the package declares, by its object, with its declaration.
func (s *goSource) funcs() map[*types.Func]*ast.FuncDecl {
	decls := map[*types.Func]*ast.FuncDecl{}
	for _, file := range s.files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				decls[s.info.Defs[fn.Name].(*types.Func)] = fn
			}
		}
	}
	return decls
}

// params is the objects of a declaration's parameters, in order.
func (s *goSource) params(decl *ast.FuncDecl) []types.Object {
	var objects []types.Object
	for _, field := range decl.Type.Params.List {
		if len(field.Names) == 0 {
			objects = append(objects, nil)
		}
		for _, name := range field.Names {
			objects = append(objects, s.info.Defs[name])
		}
	}
	return objects
}

// assignments is every expression the package's source gives each variable: by := and =, by a
// var declaration, by a range — its value, and its key where the key is an element too — and,
// for a parameter, by each call of its function. A value no expression stands for alone — one of
// several results of one call, an element of what a loop ranges over — is recorded as that call
// or as what it ranges over. It is read once per package (given).
func (s *goSource) assignments() map[types.Object][]ast.Expr {
	if s.given != nil {
		return s.given
	}
	given := map[types.Object][]ast.Expr{}
	object := func(expr ast.Expr) types.Object {
		ident, ok := ast.Unparen(expr).(*ast.Ident)
		if !ok {
			return nil
		}
		if def := s.info.Defs[ident]; def != nil {
			return def
		}
		return s.info.Uses[ident]
	}
	pair := func(lhs, rhs []ast.Expr) {
		for i, left := range lhs {
			target := object(left)
			if target == nil {
				continue
			}
			if len(lhs) == len(rhs) {
				given[target] = append(given[target], rhs[i])
			} else if len(rhs) == 1 {
				given[target] = append(given[target], rhs[0])
			}
		}
	}
	give := func(target, expr ast.Expr) {
		if object := object(target); object != nil {
			given[object] = append(given[object], expr)
		}
	}
	decls := s.funcs()
	for _, file := range s.files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				pair(node.Lhs, node.Rhs)
			case *ast.ValueSpec:
				lhs := make([]ast.Expr, len(node.Names))
				for i, name := range node.Names {
					lhs[i] = name
				}
				pair(lhs, node.Values)
			case *ast.RangeStmt:
				// The key is an index of a string, a slice, an array or an integer's count, and no
				// part of what the loop ranges over; of a map, a channel or an iterator it is an
				// element, as the value is.
				if node.Value != nil {
					give(node.Value, node.X)
				}
				if typ := s.info.Types[node.X].Type; node.Key != nil && typ != nil {
					switch typ.Underlying().(type) {
					case *types.Map, *types.Chan, *types.Signature:
						give(node.Key, node.X)
					}
				}
			case *ast.CallExpr:
				fn := s.callee(node)
				decl, declared := decls[fn]
				if !declared {
					return true
				}
				for i, param := range s.params(decl) {
					if param != nil && i < len(node.Args) {
						given[param] = append(given[param], node.Args[i])
					}
				}
			}
			return true
		})
	}
	s.given = given
	return given
}

// refusalTaint is every variable of the package a refusal's words reach, through assignments,
// through the loops that range over them and through the parameters of the package's own
// functions, since a helper can take the words as its parameter.
type refusalTaint struct {
	src *goSource
	// fields is each field that holds a refusal's words, and methods the names of the methods and
	// functions that return them.
	fields  func(field *types.Var) bool
	methods []string
	tainted map[types.Object]bool
}

// refusalWords is the taint of the words a refusal carries in the package. In this package they
// are the Error of a late refusal and of a prompt result, the Err of a refused send, the send
// error a delivery records, and whatever runtime.RefusalReason or a RefusalReason method returns.
// In the stream and the bridge, which handle the frames themselves, they are every string field
// named Error, Reason or Message — the words of Oh My Pi's response and of its prompt_result's
// error, of a shim's own answer, and of the stream's events — and whatever a Reason or
// RefusalReason method returns.
func (s *goSource) refusalWords(t *testing.T) *refusalTaint {
	t.Helper()
	taint := &refusalTaint{src: s, tainted: map[types.Object]bool{}}
	if s.pkg.Path() == supervisePath {
		fields := map[types.Object]bool{}
		for _, field := range []struct{ typ, field string }{
			{"StreamLateRefusal", "Error"}, {"StreamPromptResult", "Error"}, {"PromptRefused", "Err"}, {"Delivery", "LastSendError"},
		} {
			named, _ := s.pkg.Scope().Lookup(field.typ).(*types.TypeName)
			if named == nil {
				t.Fatalf("the package declares no type %s, whose %s this test reads as a refusal's words", field.typ, field.field)
			}
			object, _, _ := types.LookupFieldOrMethod(named.Type(), false, s.pkg, field.field)
			if object == nil {
				t.Fatalf("%s has no field %s, which this test reads as a refusal's words", field.typ, field.field)
			}
			fields[object] = true
		}
		taint.fields = func(field *types.Var) bool { return fields[field] }
		taint.methods = []string{"RefusalReason"}
	} else {
		taint.fields = func(field *types.Var) bool {
			basic, ok := field.Type().Underlying().(*types.Basic)
			return ok && basic.Kind() == types.String && slices.Contains([]string{"Error", "Reason", "Message"}, field.Name())
		}
		taint.methods = []string{"Reason", "RefusalReason"}
	}
	taint.spread()
	return taint
}

// spread taints every variable that the package gives an expression the words reach
// (assignments), until no more is.
func (r *refusalTaint) spread() {
	given := r.src.assignments()
	for changed := true; changed; {
		changed = false
		for target, exprs := range given {
			if r.tainted[target] {
				continue
			}
			for _, expr := range exprs {
				if r.reaches(expr) {
					r.tainted[target], changed = true, true
					break
				}
			}
		}
	}
}

// busyHolders is every constant expression in decl that holds the Claude Code bridge's busy words
// (holdsBusyWords), however it is spelled — the sentence or a piece of it written out, alone, in
// a longer text or in a pattern, a constant of the package's own, the bridge's constant — and
// wherever it stands: an argument, a key or an element of a literal, a list a loop ranges over, a
// variable's value. It reads no flow: nothing in the daemon but the bridge's busy refusal has a
// reason to hold the words, so holding them is the fault, whatever is done with them. Of words
// spelled out in pieces it reports the outermost constant, once. allowed is what may hold them
// (busyRefusal).
func (s *goSource) busyHolders(decl ast.Decl, words string, allowed map[ast.Expr]bool) []ast.Expr {
	var found []ast.Expr
	ast.Inspect(decl, func(node ast.Node) bool {
		expr, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		if allowed[expr] {
			return false
		}
		if value := s.info.Types[expr].Value; value != nil && value.Kind() == constant.String && holdsBusyWords(constant.StringVal(value), words) {
			found = append(found, expr)
			return false
		}
		return true
	})
	return found
}

// busyRefusal is where the bridge may hold its busy words: the value its declaration gives
// busyWords, and busyWords as the Error of a shimwire.Response the bridge hands to its respond
// method, which is the busy refusal it writes.
func (s *goSource) busyRefusal(busy *types.Const) map[ast.Expr]bool {
	allowed := map[ast.Expr]bool{}
	for _, file := range s.files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.ValueSpec:
				for i, name := range node.Names {
					if s.info.Defs[name] == busy && i < len(node.Values) {
						allowed[node.Values[i]] = true
					}
				}
			case *ast.CallExpr:
				fn := s.callee(node)
				if fn == nil || fn.Pkg() != s.pkg || fn.Name() != "respond" || fn.Signature().Recv() == nil {
					return true
				}
				for _, arg := range node.Args {
					literal, ok := ast.Unparen(arg).(*ast.CompositeLit)
					if !ok {
						continue
					}
					named, ok := s.info.Types[literal].Type.(*types.Named)
					if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != internalPath+"shimwire" || named.Obj().Name() != "Response" {
						continue
					}
					for _, elt := range literal.Elts {
						field, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, isKey := field.Key.(*ast.Ident)
						value, isValue := ast.Unparen(field.Value).(*ast.Ident)
						if isKey && key.Name == "Error" && isValue && s.info.Uses[value] == busy {
							allowed[field.Value] = true
						}
					}
				}
			}
			return true
		})
	}
	return allowed
}

// busyRun is the fewest bytes a constant must share with the busy words, in one run, to hold
// them: "Agent is already", the opening Oh My Pi's own busy sentence shares, is sixteen. A shorter
// run is a phrase other texts share with the words, such as " a turn; the " or " is already ",
// and tells nothing of the refusal.
const busyRun = 16

// bridgeName is the bridge's name as the busy words carry it. Each of the bridge's own messages
// carries it too, so a run of the words that is a piece of it tells nothing of the refusal.
const bridgeName = " the Claude Code bridge "

// holdsBusyWords is whether value shares with words a run of at least busyRun bytes that is no
// piece of the bridge's name (bridgeName): the words whole, a prefix or any other piece of them,
// either alone or inside a longer text.
func holdsBusyWords(value, words string) bool {
	for i := range len(value) {
		for j := range len(words) {
			n := 0
			for i+n < len(value) && j+n < len(words) && value[i+n] == words[j+n] {
				n++
			}
			if n >= busyRun && !strings.Contains(bridgeName, words[j:j+n]) {
				return true
			}
		}
	}
	return false
}

// reaches is whether a refusal's words reach expr.
func (r *refusalTaint) reaches(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.SelectorExpr:
			if selection := r.src.info.Selections[node]; selection != nil && selection.Kind() == types.FieldVal {
				if field, ok := selection.Obj().(*types.Var); ok && r.fields(field) {
					found = true
				}
			}
		case *ast.Ident:
			if r.tainted[r.src.info.Uses[node]] {
				found = true
			}
		case *ast.CallExpr:
			if fn := r.src.callee(node); fn != nil && slices.Contains(r.methods, fn.Name()) {
				found = true
			}
		}
		return !found
	})
	return found
}

// wordMatch is one place a refusal's words are matched: at, and what the match is.
type wordMatch struct {
	at   ast.Node
	what string
}

// textMatchers are the functions of packages strings and bytes that match one text against
// another, or split it by another: what a site learns from one of them about a refusal's words is
// a reading of them. TrimPrefix and TrimSuffix are none: a trimmed text is still the words, so a
// site that compares one, with the text it was trimmed from or with anything else, compares the
// words (matches), and a site that only trims them changes what a log line shows.
var textMatchers = []string{
	"Compare", "Contains", "ContainsAny", "ContainsFunc", "ContainsRune", "Count", "Cut", "CutPrefix",
	"CutSuffix", "Equal", "EqualFold", "Fields", "FieldsFunc", "FieldsFuncSeq", "FieldsSeq", "HasPrefix",
	"HasSuffix", "Index", "IndexAny", "IndexByte", "IndexFunc", "IndexRune", "LastIndex", "LastIndexAny",
	"LastIndexByte", "LastIndexFunc", "Lines", "Split", "SplitAfter", "SplitAfterN", "SplitAfterSeq",
	"SplitN", "SplitSeq",
}

// elementMatchers are the functions of packages slices and maps that compare one value with
// another, by package: whether a list holds a text, where, and whether two lists or maps are
// alike. They compare values of any type, so one compares a refusal's words only where it is
// handed them as a text or among a list's or a map's texts (holdsText).
var elementMatchers = map[string][]string{
	"slices": {"BinarySearch", "BinarySearchFunc", "Compare", "CompareFunc", "Contains", "ContainsFunc", "Equal", "EqualFunc", "Index", "IndexFunc"},
	"maps":   {"Equal", "EqualFunc"},
}

// textMatcher is whether fn matches one text against another: one of textMatchers in package
// strings or package bytes, package regexp's Match functions, or a method of a regexp.Regexp that
// matches a text, finds in it or splits it. errors.Is and errors.As are none: they compare an
// error with another by identity or by type, which reads none of its words, as sendOutcome reads
// the refusal a send returned.
func textMatcher(fn *types.Func) bool {
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	method := fn.Signature().Recv() != nil
	switch fn.Pkg().Path() {
	case "strings", "bytes":
		return !method && slices.Contains(textMatchers, fn.Name())
	case "regexp":
		return strings.HasPrefix(fn.Name(), "Match") || method && (strings.HasPrefix(fn.Name(), "Find") || fn.Name() == "Split")
	}
	return false
}

// elementMatcher is whether fn is one of elementMatchers.
func elementMatcher(fn *types.Func) bool {
	return fn != nil && fn.Pkg() != nil && fn.Signature().Recv() == nil && slices.Contains(elementMatchers[fn.Pkg().Path()], fn.Name())
}

// holdsText is whether a value of typ is a text or holds texts: a plain string, or a slice, an
// array or a map whose elements or keys hold one. A text is a plain string for the reason a
// comparison's is (matches).
func holdsText(typ types.Type) bool {
	if basic, ok := typ.(*types.Basic); ok {
		return basic.Info()&types.IsString != 0
	}
	switch under := typ.Underlying().(type) {
	case *types.Slice:
		return holdsText(under.Elem())
	case *types.Array:
		return holdsText(under.Elem())
	case *types.Map:
		return holdsText(under.Key()) || holdsText(under.Elem())
	}
	return false
}

// matches is every place in node that matches the words: a call of a text matcher (textMatcher)
// with them among its arguments or as its receiver; a call of an element matcher
// (elementMatcher) with them among its arguments as a text or among a list's or a map's texts;
// a lookup in a map keyed by a text they reach, alone or comma-ok; and an == or !=, or a switch's
// tag against a case, that compares a text they reach with anything but "" or nil. Asking
// whether there are words at all reads none of them. A text is a plain string: a value of a
// named type — an Outcome a call minted from a tainted error — is a member, which is compared by
// design, and an error compared with == is compared by identity, which reads none of its words.
func (r *refusalTaint) matches(node ast.Node) []wordMatch {
	text := func(expr ast.Expr) bool {
		basic, ok := r.src.info.Types[expr].Type.(*types.Basic)
		return ok && basic.Info()&types.IsString != 0 && r.reaches(expr)
	}
	texts := func(expr ast.Expr) bool {
		typ := r.src.info.Types[expr].Type
		return typ != nil && holdsText(typ) && r.reaches(expr)
	}
	empty := func(expr ast.Expr) bool {
		tv := r.src.info.Types[expr]
		return tv.IsNil() || tv.Value != nil && tv.Value.Kind() == constant.String && constant.StringVal(tv.Value) == ""
	}
	compares := func(a, b ast.Expr) bool { return text(a) && !empty(b) || text(b) && !empty(a) }
	var found []wordMatch
	ast.Inspect(node, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			fn := r.src.callee(node)
			reached := r.reaches
			switch {
			case textMatcher(fn):
			case elementMatcher(fn):
				reached = texts
			default:
				return true
			}
			operands := slices.Clone(node.Args)
			if selector, ok := ast.Unparen(node.Fun).(*ast.SelectorExpr); ok && r.src.info.Selections[selector] != nil {
				operands = append(operands, selector.X)
			}
			if slices.ContainsFunc(operands, reached) {
				found = append(found, wordMatch{at: node, what: "a call of " + fn.FullName()})
			}
		case *ast.IndexExpr:
			if typ := r.src.info.Types[node.X].Type; typ != nil {
				if _, isMap := typ.Underlying().(*types.Map); isMap && text(node.Index) {
					found = append(found, wordMatch{at: node, what: "a map lookup keyed by a text"})
				}
			}
		case *ast.BinaryExpr:
			if (node.Op == gotoken.EQL || node.Op == gotoken.NEQ) && compares(node.X, node.Y) {
				found = append(found, wordMatch{at: node, what: "a comparison with " + node.Op.String()})
			}
		case *ast.SwitchStmt:
			if node.Tag == nil {
				return true
			}
			for _, stmt := range node.Body.List {
				if slices.ContainsFunc(stmt.(*ast.CaseClause).List, func(c ast.Expr) bool { return compares(node.Tag, c) }) {
					found = append(found, wordMatch{at: node, what: "a switch"})
					break
				}
			}
		}
		return true
	})
	return found
}

// outcomeJudge says whether an expression of type Outcome is a member the set declares: a
// constant whose value a declared member holds; a field of type Outcome, which the stream filled
// with the member it minted where it read the frame (StreamLateRefusal.Outcome); the member a
// refusal carries, which is the minter's (runtime.RefusalOutcome, read by sendOutcome); a variable
// every expression given it is one of these; or a call of the package's own function every return
// of which is. A conversion to Outcome of anything but a member's constant is none of them.
type outcomeJudge struct {
	src     *goSource
	members map[string]bool
	given   map[types.Object][]ast.Expr
	decls   map[*types.Func]*ast.FuncDecl
	// judged holds each variable or function already judged, and "" for one being judged now, so
	// a cycle — relaunchAfterFailure's retry passes its own parameter back to itself — is judged
	// by the other paths into it.
	judged map[types.Object]string
}

func (j *outcomeJudge) expr(expr ast.Expr) string {
	expr = ast.Unparen(expr)
	if j.given == nil {
		j.given, j.decls = j.src.assignments(), j.src.funcs()
	}
	tv := j.src.info.Types[expr]
	if tv.Value != nil {
		if tv.Value.Kind() != constant.String || !j.members[tv.Value.ExactString()] {
			return fmt.Sprintf("%s is the constant %s, which no member holds", j.src.pos(expr), tv.Value.ExactString())
		}
		return ""
	}
	switch expr := expr.(type) {
	case *ast.Ident:
		object := j.src.info.Uses[expr]
		variable, ok := object.(*types.Var)
		if !ok {
			return fmt.Sprintf("%s names %s, which is no variable", j.src.pos(expr), expr.Name)
		}
		return j.variable(variable, expr)
	case *ast.SelectorExpr:
		selection := j.src.info.Selections[expr]
		if selection == nil {
			return fmt.Sprintf("%s is no field or method of a value", j.src.pos(expr))
		}
		if selection.Kind() == types.FieldVal {
			return ""
		}
		return fmt.Sprintf("%s is a method value, not a member", j.src.pos(expr))
	case *ast.CallExpr:
		if j.src.info.Types[expr.Fun].IsType() {
			return fmt.Sprintf("%s converts a value that is no member's constant to Outcome", j.src.pos(expr))
		}
		fn := j.src.callee(expr)
		if fn == nil {
			return fmt.Sprintf("%s calls a function value whose answer this test cannot read", j.src.pos(expr))
		}
		if fn.Pkg() != nil && fn.Pkg().Path() == runtimePath && fn.Name() == "RefusalOutcome" {
			return ""
		}
		decl, declared := j.decls[fn]
		if !declared {
			return fmt.Sprintf("%s calls %s, which this package does not declare", j.src.pos(expr), fn.Name())
		}
		return j.function(fn, decl)
	}
	return fmt.Sprintf("%s is an expression this test cannot read as a member", j.src.pos(expr))
}

func (j *outcomeJudge) variable(variable *types.Var, at ast.Node) string {
	if why, done := j.judged[variable]; done {
		return why
	}
	j.judged[variable] = ""
	exprs := j.given[variable]
	why := ""
	if len(exprs) == 0 {
		why = fmt.Sprintf("%s reads %s, which nothing in the package gives a value", j.src.pos(at), variable.Name())
	}
	for _, given := range exprs {
		if reason := j.expr(given); reason != "" {
			why = fmt.Sprintf("%s is given %s", variable.Name(), reason)
			break
		}
	}
	j.judged[variable] = why
	return why
}

func (j *outcomeJudge) function(fn *types.Func, decl *ast.FuncDecl) string {
	if why, done := j.judged[fn]; done {
		return why
	}
	j.judged[fn] = ""
	why := ""
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		if _, literal := node.(*ast.FuncLit); literal || why != "" {
			return false
		}
		if ret, ok := node.(*ast.ReturnStmt); ok {
			for _, result := range ret.Results {
				if isOutcome(j.src.info.Types[result].Type) {
					if reason := j.expr(result); reason != "" {
						why = fmt.Sprintf("%s returns %s", fn.Name(), reason)
					}
				}
			}
		}
		return true
	})
	j.judged[fn] = why
	return why
}
