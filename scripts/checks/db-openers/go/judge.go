//go:build ignore

package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
)

// analysis is the search over one module's files: the opener forms, the wrappers found so far and
// the calls listed.
type analysis struct {
	m     *module
	forms map[string]form
	// wrappers holds each wrapper with the indices of its parameters that reach a connection
	// argument of an opener.
	wrappers map[*types.Func]map[int]bool
	// decls holds the declaration of each function of the module, by its object.
	decls  map[*types.Func]*ast.FuncDecl
	calls  []*call
	rounds int
}

// call is one listed call: of a form, of a wrapper, or a form or wrapper named as a value.
type call struct {
	at       token.Pos
	function string
	// in is the function whose body holds the call; nil in a package variable's declaration.
	in       *types.Func
	callee   string
	wrapped  *types.Func
	argument string
	kind     string // "end", "wrapper", "value" or "uncalled"
	verdict  verdict
}

// verdict is what a judge found of an argument: judged (with why), or a value of the parameters
// it names (a wrapper's), or unjudged (with why).
type verdict struct {
	judged bool
	params map[int]bool
	why    string
}

func newAnalysis(m *module) *analysis {
	a := &analysis{m: m, forms: map[string]form{}, wrappers: map[*types.Func]map[int]bool{}, decls: map[*types.Func]*ast.FuncDecl{}}
	for _, f := range m.files {
		for _, decl := range f.Decls {
			if d, ok := decl.(*ast.FuncDecl); ok && d.Body != nil {
				if fn, ok := m.info.Defs[d.Name].(*types.Func); ok {
					a.decls[fn] = d
				}
			}
		}
	}
	a.readForms()
	return a
}

// answer is the verdict on what fn, a function of the module, answers first: judged when every
// return statement of its body (not those of a function literal in it) answers a value judged
// without its parameters, so that no call of it can hand it a setting; ok is false otherwise.
func (a *analysis) answer(fn *types.Func, depth int) (verdict, bool) {
	decl := a.decls[fn]
	if decl == nil || depth > 4 {
		return verdict{}, false
	}
	params := map[types.Object]int{}
	index := 0
	for _, field := range decl.Type.Params.List {
		if len(field.Names) == 0 {
			index++
		}
		for _, n := range field.Names {
			params[a.m.info.Defs[n]] = index
			index++
		}
	}
	var whys []string
	ok := true
	returns := 0
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			returns++
			if len(x.Results) == 0 {
				ok = false
				return false
			}
			v := a.judge(decl, params, x.Results[0], x.Pos(), depth+1)
			if !v.judged || len(v.params) > 0 {
				ok = false
			}
			whys = append(whys, v.why)
		}
		return true
	})
	if !ok || returns == 0 {
		return verdict{}, false
	}
	return verdict{judged: true, why: fmt.Sprintf("the answer of %s, whose every return is judged: %s", fn.Name(), strings.Join(whys, "; "))}, true
}

// run lists every call, finding wrappers again until a round finds none that is new.
func (a *analysis) run() {
	for {
		a.rounds++
		a.calls = a.calls[:0]
		grew := false
		for _, f := range a.m.files {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Body != nil && a.walk(d, d.Body) {
						grew = true
					}
				case *ast.GenDecl:
					// A package variable's initializer is read as a function of its own with no
					// parameters.
					if a.walk(nil, d) {
						grew = true
					}
				}
			}
		}
		if !grew {
			break
		}
	}
	called := map[*types.Func]bool{}
	for _, c := range a.calls {
		if c.wrapped != nil && c.kind != "value" {
			called[c.wrapped] = true
		}
	}
	for _, c := range a.calls {
		if c.kind == "wrapper" && c.in != nil && !called[c.in] {
			c.kind = "uncalled"
			c.verdict = verdict{why: "a wrapper that no file read calls: its callers are tests"}
		}
	}
	sort.SliceStable(a.calls, func(i, j int) bool {
		pi, pj := a.m.fset.Position(a.calls[i].at), a.m.fset.Position(a.calls[j].at)
		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}
		return pi.Offset < pj.Offset
	})
}

// walk lists the calls in node, the body of fd (nil for a package variable's declaration), and
// reports whether it found a wrapper parameter that is new.
func (a *analysis) walk(fd *ast.FuncDecl, node ast.Node) bool {
	info := a.m.info
	var self *types.Func
	params := map[types.Object]int{}
	name := "(package variable)"
	if fd != nil {
		self, _ = info.Defs[fd.Name].(*types.Func)
		name = fd.Name.Name
		index := 0
		for _, field := range fd.Type.Params.List {
			if len(field.Names) == 0 {
				index++
			}
			for _, n := range field.Names {
				params[info.Defs[n]] = index
				index++
			}
		}
	}
	grew := false
	calledFun := map[ast.Expr]bool{}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			fun := ast.Unparen(x.Fun)
			calledFun[fun] = true
			if index, ok := fun.(*ast.IndexExpr); ok {
				calledFun[ast.Unparen(index.X)] = true
			}
			fn := calleeOf(x, info)
			if fn == nil {
				return true
			}
			callee := funcName(fn)
			var positions []int
			if f, ok := a.forms[callee]; ok {
				positions = []int{f.Argument}
			} else if indices, ok := a.wrappers[fn]; ok {
				for i := range indices {
					positions = append(positions, i)
				}
				sort.Ints(positions)
			} else {
				return true
			}
			if fd != nil && a.inFormatter(fd) {
				// The formatter's own parse: pgsetting is the one reading.
				a.calls = append(a.calls, &call{at: x.Pos(), function: name, in: self, callee: callee, argument: a.args(x, positions), kind: "end",
					verdict: verdict{judged: true, why: "the formatter's own parse (pgsetting)"}})
				return true
			}
			var v verdict
			for i, at := range positions {
				if at >= len(x.Args) {
					continue
				}
				one := a.judge(fd, params, x.Args[at], x.Pos(), 0)
				if i == 0 {
					v = one
				} else {
					v = combine(v, one)
				}
			}
			kind := "end"
			if len(v.params) > 0 {
				kind = "wrapper"
				if self != nil {
					if a.wrappers[self] == nil {
						a.wrappers[self] = map[int]bool{}
					}
					for i := range v.params {
						if !a.wrappers[self][i] {
							a.wrappers[self][i] = true
							grew = true
						}
					}
				}
			}
			var wrapped *types.Func
			if _, isWrapper := a.wrappers[fn]; isWrapper {
				wrapped = fn
			}
			a.calls = append(a.calls, &call{at: x.Pos(), function: name, in: self, callee: callee, wrapped: wrapped, argument: a.args(x, positions), kind: kind, verdict: v})
		case *ast.SelectorExpr:
			// The selector is the name; its Sel is not read again.
			a.value(name, self, x, x.Sel, calledFun)
			ast.Inspect(x.X, visit)
			return false
		case *ast.Ident:
			a.value(name, self, x, x, calledFun)
		}
		return true
	}
	ast.Inspect(node, visit)
	return grew
}

// value lists expr, which names id, when it names a form or a wrapper and is not called.
func (a *analysis) value(function string, self *types.Func, expr ast.Expr, id *ast.Ident, calledFun map[ast.Expr]bool) {
	if calledFun[expr] {
		return
	}
	fn, ok := a.m.info.Uses[id].(*types.Func)
	if !ok {
		return
	}
	_, isForm := a.forms[funcName(fn)]
	_, isWrapper := a.wrappers[fn]
	if !isForm && !isWrapper {
		return
	}
	var wrapped *types.Func
	if isWrapper {
		wrapped = fn
	}
	a.calls = append(a.calls, &call{at: expr.Pos(), function: function, in: self, callee: funcName(fn), wrapped: wrapped, argument: a.text(expr), kind: "value",
		verdict: verdict{why: "named as a value, so what it is later handed is not read"}})
}

// inFormatter reports whether fd is a function of the formatter itself.
func (a *analysis) inFormatter(fd *ast.FuncDecl) bool {
	fn, ok := a.m.info.Defs[fd.Name].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == pgsetting
}

// judge is the verdict on e, an argument in the connection place of a call at pos in fd (nil for
// a package variable), whose parameters are params.
func (a *analysis) judge(fd *ast.FuncDecl, params map[types.Object]int, e ast.Expr, pos token.Pos, depth int) verdict {
	info := a.m.info
	e = ast.Unparen(e)
	switch x := e.(type) {
	case *ast.StarExpr:
		return a.judge(fd, params, x.X, pos, depth)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return a.judge(fd, params, x.X, pos, depth)
		}
	}
	if depth > 8 {
		return verdict{why: "a value read through more than 8 assignments"}
	}
	if line, how := a.readFirst(fd, e, pos); line > 0 {
		return verdict{judged: true, why: fmt.Sprintf("read by %s first, at line %d", how, line)}
	}
	if tv, ok := info.Types[e]; ok && tv.Value != nil {
		return verdict{judged: true, why: "a constant"}
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		return verdict{judged: true, why: "a constant"}
	case *ast.CompositeLit:
		if len(x.Elts) == 0 {
			return verdict{judged: true, why: "a zero value"}
		}
	case *ast.Ident:
		switch obj := info.Uses[x].(type) {
		case *types.Const:
			return verdict{judged: true, why: "a constant"}
		case *types.Nil:
			return verdict{judged: true, why: "nil"}
		case *types.Var:
			if i, ok := params[obj]; ok {
				return verdict{params: map[int]bool{i: true}, why: "a parameter"}
			}
			if fd == nil || obj.Pos() < fd.Pos() || obj.Pos() >= fd.End() {
				return verdict{why: "the package variable " + x.Name}
			}
			values, unknown := a.assigned(fd, obj)
			if unknown != "" {
				return verdict{why: unknown}
			}
			v := verdict{judged: true, why: "a zero value"}
			for i, value := range values {
				one := verdict{judged: true, why: "a zero value"}
				if value != nil {
					one = a.judge(fd, params, value, value.Pos(), depth+1)
				}
				if i == 0 {
					v = one
				} else {
					v = combine(v, one)
				}
			}
			return v
		}
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			return combine(a.judge(fd, params, x.X, pos, depth+1), a.judge(fd, params, x.Y, pos, depth+1))
		}
	case *ast.CallExpr:
		if tv, ok := info.Types[x.Fun]; ok && tv.IsType() && len(x.Args) == 1 {
			return a.judge(fd, params, x.Args[0], pos, depth+1)
		}
		fn := calleeOf(x, info)
		if fn == nil {
			return verdict{why: "the answer of " + a.text(x.Fun)}
		}
		callee := funcName(fn)
		switch {
		case formatterAnswers[callee]:
			return verdict{judged: true, why: "read through " + strings.TrimPrefix(callee, "github.com/sjawhar/envoy/")}
		case a.forms[callee].Callee != "":
			return verdict{judged: true, why: fmt.Sprintf("the configuration the parse at line %d answered, which is listed itself", a.m.fset.Position(x.Pos()).Line)}
		case fn.Name() == "Copy" && configType(recvType(fn)) != "":
			if sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr); ok {
				return a.judge(fd, params, sel.X, pos, depth+1)
			}
		case fn.Name() == "Config" && openConnection(recvType(fn)):
			return verdict{judged: true, why: "the configuration of a pool or connection already open, whose own opener is listed"}
		}
		if v, ok := a.answer(fn, depth); ok {
			return v
		}
		return verdict{why: "the answer of " + strings.TrimPrefix(callee, a.m.path+"/")}
	case *ast.SelectorExpr:
		return verdict{why: "the field " + a.text(x)}
	}
	return verdict{why: "the value " + a.text(e)}
}

// combine is the verdict on a value built from two: unjudged when either is, a wrapper's when
// either names a parameter, judged otherwise.
func combine(x, y verdict) verdict {
	switch {
	case !x.judged && len(x.params) == 0:
		return x
	case !y.judged && len(y.params) == 0:
		return y
	case len(x.params) > 0 || len(y.params) > 0:
		params := map[int]bool{}
		for i := range x.params {
			params[i] = true
		}
		for i := range y.params {
			params[i] = true
		}
		return verdict{params: params, why: "a parameter"}
	}
	if x.why == y.why {
		return x
	}
	return verdict{judged: true, why: x.why + "; " + y.why}
}

// assigned is every value fd assigns to obj, a variable declared in it (nil for its zero value),
// or why they cannot all be read: its address is taken, or a range statement writes it.
func (a *analysis) assigned(fd *ast.FuncDecl, obj types.Object) ([]ast.Expr, string) {
	info := a.m.info
	var values []ast.Expr
	unknown := ""
	is := func(e ast.Expr) bool {
		id, ok := ast.Unparen(e).(*ast.Ident)
		return ok && (info.Defs[id] == obj || info.Uses[id] == obj)
	}
	ast.Inspect(fd, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				if !is(lhs) {
					continue
				}
				switch {
				case x.Tok != token.ASSIGN && x.Tok != token.DEFINE:
					unknown = "a variable an operator assigns"
				case len(x.Rhs) == len(x.Lhs):
					values = append(values, x.Rhs[i])
				default:
					values = append(values, x.Rhs[0])
				}
			}
		case *ast.ValueSpec:
			for i, n := range x.Names {
				if info.Defs[n] != obj {
					continue
				}
				switch {
				case len(x.Values) == 0:
					values = append(values, nil)
				case len(x.Values) == len(x.Names):
					values = append(values, x.Values[i])
				default:
					values = append(values, x.Values[0])
				}
			}
		case *ast.RangeStmt:
			if (x.Key != nil && is(x.Key)) || (x.Value != nil && is(x.Value)) {
				unknown = "a variable a range statement writes"
			}
		case *ast.UnaryExpr:
			if x.Op == token.AND && is(x.X) {
				unknown = "a variable whose address is taken, so another line can write it"
			}
		}
		return true
	})
	if unknown == "" && len(values) == 0 {
		unknown = "a variable no line of its function assigns"
	}
	return values, unknown
}

// readFirst is the line of a call of the formatter (pgsetting.Refuse, ParseConfig or
// ParsePoolConfig) in fd, before pos, that reads e, and which call it is; 0 when none does.
func (a *analysis) readFirst(fd *ast.FuncDecl, e ast.Expr, pos token.Pos) (int, string) {
	if fd == nil {
		return 0, ""
	}
	line, how := 0, ""
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || line > 0 || c.Pos() >= pos || len(c.Args) < 2 {
			return line == 0
		}
		fn := calleeOf(c, a.m.info)
		if fn == nil || !formatterReads[funcName(fn)] || !a.same(c.Args[1], e) {
			return true
		}
		line, how = a.m.fset.Position(c.Pos()).Line, "pgsetting."+fn.Name()
		return false
	})
	return line, how
}

// same reports whether x and y name the same variable, or the same field path of one.
func (a *analysis) same(x, y ast.Expr) bool {
	x, y = ast.Unparen(x), ast.Unparen(y)
	switch xx := x.(type) {
	case *ast.Ident:
		yy, ok := y.(*ast.Ident)
		if !ok {
			return false
		}
		ox, oy := a.m.info.Uses[xx], a.m.info.Uses[yy]
		return ox != nil && ox == oy
	case *ast.SelectorExpr:
		yy, ok := y.(*ast.SelectorExpr)
		return ok && xx.Sel.Name == yy.Sel.Name && a.same(xx.X, yy.X)
	case *ast.StarExpr:
		yy, ok := y.(*ast.StarExpr)
		return ok && a.same(xx.X, yy.X)
	}
	return false
}

func recvType(fn *types.Func) types.Type {
	if recv := fn.Type().(*types.Signature).Recv(); recv != nil {
		return recv.Type()
	}
	return nil
}

// openConnection reports whether t is a pool or a connection of pgx, pgxpool or pgconn.
func openConnection(t types.Type) bool {
	if t == nil {
		return false
	}
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || !strings.HasPrefix(named.Obj().Pkg().Path(), "github.com/jackc/") {
		return false
	}
	switch named.Obj().Pkg().Name() + "." + named.Obj().Name() {
	case "pgxpool.Pool", "pgx.Conn", "pgconn.PgConn":
		return true
	}
	return false
}

// calleeOf is the function or method c calls, when the type checker names one.
func calleeOf(c *ast.CallExpr, info *types.Info) *types.Func {
	switch fun := ast.Unparen(c.Fun).(type) {
	case *ast.Ident:
		f, _ := info.Uses[fun].(*types.Func)
		return f
	case *ast.SelectorExpr:
		if sel := info.Selections[fun]; sel != nil {
			f, _ := sel.Obj().(*types.Func)
			return f
		}
		f, _ := info.Uses[fun.Sel].(*types.Func)
		return f
	case *ast.IndexExpr:
		if id, ok := fun.X.(*ast.Ident); ok {
			f, _ := info.Uses[id].(*types.Func)
			return f
		}
	}
	return nil
}

func (a *analysis) args(c *ast.CallExpr, positions []int) string {
	var parts []string
	for _, at := range positions {
		if at < len(c.Args) {
			parts = append(parts, a.text(c.Args[at]))
		}
	}
	return strings.Join(parts, ", ")
}

// text is n's source, its white space folded, at most 120 bytes.
func (a *analysis) text(n ast.Node) string {
	start, end := a.m.fset.Position(n.Pos()), a.m.fset.Position(n.End())
	src := a.m.sources[start.Filename]
	if end.Offset > len(src) || start.Offset > end.Offset {
		return "?"
	}
	t := strings.Join(strings.Fields(string(src[start.Offset:end.Offset])), " ")
	if len(t) > 120 {
		t = t[:120] + "…"
	}
	return t
}

// record is a call as the program prints it.
type record struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Callee   string `json:"callee"`
	Argument string `json:"argument"`
	Kind     string `json:"kind"`
	Verdict  string `json:"verdict"`
	Why      string `json:"why"`
}

func (a *analysis) record(c *call) record {
	p := a.m.fset.Position(c.at)
	rel, err := filepath.Rel(a.m.root, p.Filename)
	if err != nil {
		rel = p.Filename
	}
	verdict := "unjudged"
	switch {
	case c.kind == "wrapper":
		verdict = "wrapper"
	case c.verdict.judged:
		verdict = "judged"
	}
	return record{File: filepath.ToSlash(rel), Line: p.Line, Function: c.function, Callee: c.callee, Argument: c.argument, Kind: c.kind, Verdict: verdict, Why: c.verdict.why}
}

// summary is what the run read and listed.
func (a *analysis) summary() map[string]any {
	counts := map[string]int{}
	for _, c := range a.calls {
		counts[a.record(c).Verdict]++
	}
	packages := 0
	for p := range a.m.packages {
		if p == a.m.path || strings.HasPrefix(p, a.m.path+"/") {
			packages++
		}
	}
	return map[string]any{
		"module": a.m.path, "files": len(a.m.files), "packages": packages, "skipped": a.m.skipped,
		"forms": len(a.forms), "calls": len(a.calls), "judged": counts["judged"], "wrapper": counts["wrapper"],
		"unjudged": counts["unjudged"], "wrappers": len(a.wrappers), "rounds": a.rounds, "typeErrors": a.m.typeErrors,
	}
}
