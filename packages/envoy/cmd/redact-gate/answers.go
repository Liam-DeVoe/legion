package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
)

// answersConfig is the answer census's configuration (gate.answers): the functions that hold
// another party's text as the box (makers), the decoders that fill a value from text through its
// address, the function that reads an answer's request record (record), the box's method that
// holds a field with that record (recorder), the box's methods that answer no text (comparisons),
// and the functions whose unrecorded use of a remote field the census allows, each with why.
type answersConfig struct {
	Makers      []string       `json:"makers"`
	Decoders    []string       `json:"decoders"`
	Record      string         `json:"record"`
	Recorder    string         `json:"recorder"`
	Comparisons []string       `json:"comparisons"`
	Exempt      []answerExempt `json:"exempt"`
}

// answerExempt is a function of one file whose remote fields the answer census finds used with
// no request record, and why no answer's request can be echoed there.
type answerExempt struct {
	File     string `json:"file"`
	Function string `json:"function"`
	Why      string `json:"why"`
}

// answerCensus is the answer census over one type-checked package: every client of this
// repository that reads another party's words prints them through the record of the request it
// sent (redact.Carried), since whatever answers in the server's place can echo that request's
// credentials in any word of its answer. A remote field is made where text is held as the box
// (gate.answers.makers, with an argument that is no constant) and where a decoder fills a value
// that holds the box through its address (gate.answers.decoders, an encoding/json decode); a
// local variable a remote field is stored into holds one too, and so does a parameter a call of
// the package hands one to, and a value a function of the package decodes into through a
// parameter of an interface type, at each call that hands it one by address. Each use of a remote
// field must be one that prints none of it or one that holds it with the record:
//
//   - a comparison of the box (gate.answers.comparisons), which answers no text;
//   - the box's recorder (RemoteText.WithoutTokens) given the record, `record(resp)...` or a local
//     set only from it, whose answer prints with every credential the request carried redacted;
//   - a store into a local variable, a range over it, or a decode into it, whose own uses are read;
//   - a value that holds the box handed whole to a function or method of the package, whose
//     parameter is then read as a remote field.
//
// Any other use - a field handed to another function, built into a value, returned, converted,
// stored where the census cannot follow it, or read by a method of the box that answers its text -
// fails, unless gate.answers.exempt names its function with why. The box's own package, which
// defines the record and the box, is the field census's and the fixture's. A census that listed no
// print through the record is UNKNOWN, since it cannot have read a client.
func (g *gate) answerCensus(c *census, dir, path string, fset *token.FileSet, files []*ast.File, info *types.Info) {
	boxPkg, boxName := splitTypeName(g.inv.Gate.Box.Type)
	if path == boxPkg {
		return
	}
	cfg := g.inv.Gate.Answers
	a := &answerReader{
		info: info, fset: fset, boxPkg: boxPkg, boxName: boxName,
		makers: set(cfg.Makers), decoders: set(cfg.Decoders), record: cfg.Record, recorder: cfg.Recorder,
		comparisons: set(cfg.Comparisons), decls: map[*types.Func]*answerFunc{},
	}
	var order []*answerFunc
	for _, f := range files {
		rel, _ := filepath.Rel(dir, fset.Position(f.Pos()).Filename)
		rel = filepath.ToSlash(rel)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				fn, _ := info.Defs[d.Name].(*types.Func)
				if d.Body == nil || fn == nil {
					continue
				}
				af := &answerFunc{node: d, body: d.Body, file: rel, name: goFuncName(path, d), fn: fn,
					tainted: map[types.Object]bool{}, fills: map[int]bool{}}
				a.decls[fn] = af
				order = append(order, af)
			case *ast.GenDecl:
				// A function literal a package-level variable holds (var gcpUserinfo = func...) is
				// a client's code too; it is called through the variable, so no call hands it
				// a remote field the census can follow, and it is read on its own.
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, value := range vs.Values {
						if lit, ok := value.(*ast.FuncLit); ok && i < len(vs.Names) {
							order = append(order, &answerFunc{node: lit, body: lit.Body, file: rel, name: path + "." + vs.Names[i].Name,
								tainted: map[types.Object]bool{}, fills: map[int]bool{}})
						}
					}
				}
			}
		}
	}
	// The taint of each function grows until no pass of the package adds to it: a store, a decode,
	// a holder handed to a function of the package, a fill through an interface parameter.
	for changed := true; changed; {
		changed = false
		for _, af := range order {
			if a.taint(af) {
				changed = true
			}
		}
	}
	for _, af := range order {
		c.Answers = append(c.Answers, a.classify(af)...)
	}
}

type answerReader struct {
	info             *types.Info
	fset             *token.FileSet
	boxPkg, boxName  string
	makers, decoders map[string]bool
	record, recorder string
	comparisons      map[string]bool
	decls            map[*types.Func]*answerFunc
}

// answerFunc is one function of the package as the answer census reads it - a declaration, or a
// function literal a package-level variable holds (fn nil) - with the objects that hold a remote
// field in it, and the parameters, by index, whose interface value it decodes into.
type answerFunc struct {
	node       ast.Node
	body       *ast.BlockStmt
	fn         *types.Func
	file, name string
	tainted    map[types.Object]bool
	fills      map[int]bool
}

func (a *answerReader) isBox(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == a.boxPkg && n.Obj().Name() == a.boxName
}

// holds reports whether a value of type t holds the box anywhere: itself, or through a pointer, a
// field, a slice, an array, a map or a channel.
func (a *answerReader) holds(t types.Type) bool { return a.holdsSeen(t, map[types.Type]bool{}) }

func (a *answerReader) holdsSeen(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		return a.isBox(t) || a.holdsSeen(t.Underlying(), seen)
	case *types.Pointer:
		return a.holdsSeen(t.Elem(), seen)
	case *types.Slice:
		return a.holdsSeen(t.Elem(), seen)
	case *types.Array:
		return a.holdsSeen(t.Elem(), seen)
	case *types.Map:
		return a.holdsSeen(t.Key(), seen) || a.holdsSeen(t.Elem(), seen)
	case *types.Chan:
		return a.holdsSeen(t.Elem(), seen)
	case *types.Struct:
		for i := range t.NumFields() {
			if a.holdsSeen(t.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

func (a *answerReader) typeOf(e ast.Expr) types.Type {
	if tv, ok := a.info.Types[e]; ok {
		return tv.Type
	}
	if id, ok := e.(*ast.Ident); ok {
		if obj := a.object(id); obj != nil {
			return obj.Type()
		}
	}
	return nil
}

func (a *answerReader) object(id *ast.Ident) types.Object {
	if obj := a.info.Uses[id]; obj != nil {
		return obj
	}
	return a.info.Defs[id]
}

// callee is the function or method call calls by name, its generic origin, or nil for a
// conversion, a builtin or a call of a function value.
func (a *answerReader) callee(call *ast.CallExpr) *types.Func {
	fun := ast.Unparen(call.Fun)
	switch x := fun.(type) {
	case *ast.IndexExpr:
		fun = x.X
	case *ast.IndexListExpr:
		fun = x.X
	}
	var fn *types.Func
	switch x := fun.(type) {
	case *ast.Ident:
		fn, _ = a.info.Uses[x].(*types.Func)
	case *ast.SelectorExpr:
		if sel := a.info.Selections[x]; sel != nil {
			fn, _ = sel.Obj().(*types.Func)
		} else {
			fn, _ = a.info.Uses[x.Sel].(*types.Func)
		}
	}
	if fn == nil {
		return nil
	}
	return fn.Origin()
}

func (a *answerReader) calleeName(call *ast.CallExpr) string {
	if fn := a.callee(call); fn != nil {
		return fn.FullName()
	}
	return ""
}

// made reports whether call holds text that is no constant as the box (a maker).
func (a *answerReader) made(call *ast.CallExpr) bool {
	if !a.makers[a.calleeName(call)] {
		return false
	}
	for _, arg := range call.Args {
		if tv, ok := a.info.Types[arg]; !ok || tv.Value == nil {
			return true
		}
	}
	return false
}

// remote reports whether e is a remote field in af, or reaches one through a field, an index, a
// dereference, an address, a slice or a type assertion.
func (a *answerReader) remote(af *answerFunc, e ast.Expr) bool {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return af.tainted[a.object(e)]
	case *ast.SelectorExpr:
		if sel := a.info.Selections[e]; sel != nil && sel.Kind() == types.FieldVal {
			return a.remote(af, e.X)
		}
	case *ast.IndexExpr:
		return a.remote(af, e.X)
	case *ast.StarExpr:
		return a.remote(af, e.X)
	case *ast.UnaryExpr:
		return e.Op == token.AND && a.remote(af, e.X)
	case *ast.SliceExpr:
		return a.remote(af, e.X)
	case *ast.TypeAssertExpr:
		return a.remote(af, e.X)
	case *ast.CallExpr:
		return a.made(e)
	}
	return false
}

// root is the variable an lvalue or an address names, through its fields, indexes and
// dereferences; nil for any other expression.
func (a *answerReader) root(e ast.Expr) types.Object {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return a.object(e)
	case *ast.SelectorExpr:
		if sel := a.info.Selections[e]; sel != nil && sel.Kind() == types.FieldVal {
			return a.root(e.X)
		}
	case *ast.IndexExpr:
		return a.root(e.X)
	case *ast.StarExpr:
		return a.root(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return a.root(e.X)
		}
	}
	return nil
}

// local reports whether obj is a variable af declares: a parameter, a result or a local of its
// own or of a function literal inside it.
func local(af *answerFunc, obj types.Object) bool {
	v, ok := obj.(*types.Var)
	return ok && !v.IsField() && af.node.Pos() <= v.Pos() && v.Pos() < af.node.End()
}

func (a *answerReader) mark(af *answerFunc, obj types.Object) bool {
	if obj == nil || !local(af, obj) || af.tainted[obj] {
		return false
	}
	af.tainted[obj] = true
	return true
}

// param is the parameter (or the receiver, for index -1) of callee a call hands its index-th
// argument to.
func param(callee *answerFunc, index int) types.Object {
	if callee.fn == nil {
		return nil
	}
	sig := callee.fn.Type().(*types.Signature)
	if index < 0 {
		if sig.Recv() == nil {
			return nil
		}
		return sig.Recv()
	}
	n := sig.Params().Len()
	if n == 0 {
		return nil
	}
	return sig.Params().At(min(index, n-1))
}

// taint runs one pass of af, adding what it finds to its own taint, its fills and its callees'
// parameters, and answers whether it added anything.
func (a *answerReader) taint(af *answerFunc) bool {
	changed := false
	ast.Inspect(af.body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			full := a.calleeName(n)
			if a.decoders[full] && len(n.Args) > 0 {
				dest := n.Args[len(n.Args)-1]
				if t := a.typeOf(dest); a.holds(t) {
					changed = a.mark(af, a.root(dest)) || changed
				} else if t != nil && types.IsInterface(t) {
					if obj := a.root(dest); obj != nil && af.fn != nil {
						sig := af.fn.Type().(*types.Signature)
						for i := range sig.Params().Len() {
							if sig.Params().At(i) == obj && !af.fills[i] {
								af.fills[i], changed = true, true
							}
						}
					}
				}
			}
			callee := a.decls[a.callee(n)]
			if callee == nil {
				return true
			}
			for i, arg := range n.Args {
				t := a.typeOf(arg)
				switch {
				case callee.fills[i] && a.holds(t):
					// A function of the package decodes into this argument.
					changed = a.mark(af, a.root(arg)) || changed
				case a.remote(af, arg) && a.holds(t) && !a.isBox(t):
					if p := param(callee, i); p != nil && !callee.tainted[p] {
						callee.tainted[p], changed = true, true
					}
				}
			}
			if sel, ok := ast.Unparen(n.Fun).(*ast.SelectorExpr); ok && a.info.Selections[sel] != nil && a.remote(af, sel.X) {
				if p := param(callee, -1); p != nil && !callee.tainted[p] {
					callee.tainted[p], changed = true, true
				}
			}
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, rhs := range n.Rhs {
					if a.remote(af, rhs) && a.holds(a.typeOf(rhs)) {
						changed = a.mark(af, a.root(n.Lhs[i])) || changed
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, value := range n.Values {
					if a.remote(af, value) && a.holds(a.typeOf(value)) {
						changed = a.mark(af, a.object(n.Names[i])) || changed
					}
				}
			}
		case *ast.RangeStmt:
			if a.remote(af, n.X) {
				for _, e := range []ast.Expr{n.Key, n.Value} {
					if e != nil && a.holds(a.typeOf(e)) {
						changed = a.mark(af, a.root(e)) || changed
					}
				}
			}
		}
		return true
	})
	return changed
}

// records reports whether call, a call of the recorder, is given the record: `record(x)...`, or a
// local of af that every assignment sets from the record.
func (a *answerReader) records(af *answerFunc, call *ast.CallExpr) bool {
	if !call.Ellipsis.IsValid() || len(call.Args) != 1 {
		return false
	}
	arg := ast.Unparen(call.Args[0])
	if c, ok := arg.(*ast.CallExpr); ok {
		return a.calleeName(c) == a.record
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	obj := a.object(id)
	if !local(af, obj) {
		return false
	}
	sets, fromRecord := 0, true
	ast.Inspect(af.node, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			if ok {
				for _, lhs := range as.Lhs {
					if l, isID := lhs.(*ast.Ident); isID && a.object(l) == obj {
						sets, fromRecord = sets+1, false
					}
				}
			}
			return true
		}
		for i, lhs := range as.Lhs {
			if l, isID := lhs.(*ast.Ident); isID && a.object(l) == obj {
				sets++
				c, isCall := ast.Unparen(as.Rhs[i]).(*ast.CallExpr)
				fromRecord = fromRecord && isCall && a.calleeName(c) == a.record
			}
		}
		return true
	})
	return sets > 0 && fromRecord
}

// classify lists each use of a remote field in af with its verdict.
func (a *answerReader) classify(af *answerFunc) []finding {
	var found []finding
	seen := map[string]bool{}
	add := func(at ast.Node, what ast.Expr, kind string, ok bool, why string) {
		p := a.fset.Position(at.Pos())
		key := fmt.Sprintf("%d:%d:%s", p.Line, p.Column, kind)
		if seen[key] {
			return
		}
		seen[key] = true
		found = append(found, finding{File: af.file, Line: p.Line, Function: af.name, What: exprString(what), Kind: kind, OK: ok, Why: why})
	}
	var stack []ast.Node
	ast.Inspect(af.body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		parents := stack
		stack = append(stack, n)
		if call, ok := n.(*ast.CallExpr); ok && a.decoders[a.calleeName(call)] && len(call.Args) > 0 {
			if dest := call.Args[len(call.Args)-1]; a.holds(a.typeOf(dest)) {
				add(call, dest, "decoded", true, "a decode into a value that holds the box: each use of it is read")
			}
		}
		e, ok := n.(ast.Expr)
		if !ok || !a.holds(a.typeOf(e)) || !a.remote(af, e) {
			return true
		}
		if _, isParen := e.(*ast.ParenExpr); isParen {
			return true
		}
		var parent, grand ast.Node
		for i := len(parents) - 1; i >= 0; i-- {
			if _, isParen := parents[i].(*ast.ParenExpr); isParen {
				continue
			}
			parent = parents[i]
			for j := i - 1; j >= 0; j-- {
				if _, isParen := parents[j].(*ast.ParenExpr); !isParen {
					grand = parents[j]
					break
				}
			}
			break
		}
		fail := func(why string) { add(e, e, "unrecorded", false, why) }
		switch p := parent.(type) {
		case *ast.SelectorExpr:
			if p.Sel == e {
				return true
			}
			sel := a.info.Selections[p]
			if sel == nil || sel.Kind() == types.FieldVal {
				return true
			}
			m, _ := sel.Obj().(*types.Func)
			if m == nil {
				return true
			}
			recv := m.Type().(*types.Signature).Recv()
			if recv != nil && a.isBox(derefType(recv.Type())) {
				call, called := grand.(*ast.CallExpr)
				called = called && call.Fun == ast.Expr(p)
				switch {
				case a.comparisons[m.Name()] && called:
					add(e, e, "compared", true, "read by "+m.Name()+", which answers no text")
				case a.comparisons[m.Name()]:
					add(e, e, "compared", true, "the method value "+m.Name()+", which answers no text")
				case m.Origin().FullName() == a.recorder && called && a.records(af, call):
					add(e, e, "recorded", true, "held with the record of the request it answers ("+shortName(a.record)+")")
				case m.Origin().FullName() == a.recorder:
					fail(m.Name() + " is given no request record: hand it " + shortName(a.record) + "(resp)..., or a local set only from it")
				default:
					fail(m.Name() + " answers the field's text, and the field holds no request record: hold it with " + shortName(a.recorder) + "(" + shortName(a.record) + "(resp)...) first")
				}
				return true
			}
			if callee := a.decls[m.Origin()]; callee != nil {
				add(e, e, "handed", true, "the receiver of "+shortName(callee.name)+", whose uses of it are read")
				return true
			}
			fail("the receiver of " + m.Origin().FullName() + ", a method the census does not read, with no request record")
		case *ast.IndexExpr:
			if p.X != e {
				fail("an index")
			}
		case *ast.StarExpr, *ast.SliceExpr, *ast.TypeAssertExpr:
		case *ast.UnaryExpr:
			if p.Op != token.AND {
				fail("an operand of " + p.Op.String())
			}
		case *ast.AssignStmt:
			if slices.Contains(p.Lhs, e) {
				return true
			}
			i := slices.Index(p.Rhs, e)
			if i >= 0 && len(p.Lhs) == len(p.Rhs) && local(af, a.root(p.Lhs[i])) {
				return true
			}
			fail("stored where the census cannot follow it, with no request record")
		case *ast.ValueSpec:
		case *ast.RangeStmt:
			if p.X != e {
				fail("a range's key or value")
			}
		case *ast.CallExpr:
			if p.Fun == e {
				return true
			}
			fn := a.callee(p)
			if fn == nil {
				if tv, isType := a.info.Types[p.Fun]; isType && tv.IsType() {
					fail("converted, with no request record")
				} else {
					fail("handed to a function value the census does not read, with no request record")
				}
				return true
			}
			if a.decoders[fn.FullName()] && p.Args[len(p.Args)-1] == e {
				return true
			}
			if callee := a.decls[fn]; callee != nil && !a.isBox(a.typeOf(e)) {
				add(e, e, "handed", true, "handed to "+shortName(callee.name)+", whose uses of it are read")
				return true
			}
			fail("handed to " + fn.FullName() + " with no request record: hand it " + shortName(a.recorder) + "(" + shortName(a.record) + "(resp)...)")
		default:
			fail(describe(parent) + ", with no request record")
		}
		return true
	})
	return found
}

// derefType is t, or what it points to.
func derefType(t types.Type) types.Type {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// shortName is a function's full name without its package path: redact.Carried, (RemoteText).WithoutTokens.
func shortName(full string) string {
	open := strings.HasPrefix(full, "(")
	body := strings.TrimPrefix(full, "(")
	star := strings.HasPrefix(body, "*")
	body = strings.TrimPrefix(body, "*")
	if slash := strings.LastIndex(body, "/"); slash >= 0 {
		body = body[slash+1:]
	}
	switch {
	case open && star:
		return "(*" + body
	case open:
		return "(" + body
	}
	return body
}
