package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// answersConfig is the answer census's configuration (gate.answers): the reader every client reads
// an answer's body through and the opaque type it answers, with that type's exact exported method
// set; the standard library's own readers of a response's body; the function that holds another
// party's text as the box (makers), the function that reads an answer's request record (record)
// and the box's method that holds a field with it (recorder); and the functions whose unrecorded
// remote text the census allows, each with why.
type answersConfig struct {
	Reader   []string       `json:"reader"`
	Answer   string         `json:"answer"`
	Methods  []string       `json:"methods"`
	Readers  []string       `json:"readers"`
	Makers   []string       `json:"makers"`
	Record   string         `json:"record"`
	Recorder string         `json:"recorder"`
	Exempt   []answerExempt `json:"exempt"`
}

// answerExempt is a function of one file whose remote text the answer census finds held with no
// request record, and why no answer's request can be echoed there.
type answerExempt struct {
	File     string `json:"file"`
	Function string `json:"function"`
	Why      string `json:"why"`
}

// answerCensus is the answer census over one type-checked package. Whatever answers in a server's
// place can echo the request it was sent, credentials and all, in any byte of its answer, so no
// client of this repository reads an answer's bytes but through redact.ReadAnswer, whose
// redact.Answer decodes them into typed values holding the request's record, prints them only as
// the rule reads them with that record, or streams them to a writer. Outside the box's package:
//
//   - every use of the Body field of a net/http.Response fails, but its Close called (body.Close())
//     and an assignment to it: a read of the bytes, wherever they go next - a decoder, a string, a
//     helper, a value of any type - is a read outside the reader, and the census does not follow it;
//   - every call of the standard library's own readers of a response's body (gate.answers.readers:
//     (*net/http.Response).Write, net/http/httputil.DumpResponse) fails, for the same reason;
//   - each call of the reader is listed, and a census that lists none is UNKNOWN, since it cannot
//     have read a client;
//   - text held as the box (gate.answers.makers) from anything but a constant must be held with
//     the record of the answer it came from: the box's recorder given record(resp)..., or a local
//     set only from it, where resp is the response the text names (a header, the status line);
//     text that names no response, or a record of another response, fails unless
//     gate.answers.exempt names its function with why.
//
// In the box's own package the census checks the reader's type: its exported methods are exactly
// gate.answers.methods, so no method that answers the bytes can be added unseen.
func (g *gate) answerCensus(c *census, dir, path string, fset *token.FileSet, files []*ast.File, info *types.Info, pkg *types.Package) {
	cfg := g.inv.Gate.Answers
	boxPkg, _ := splitTypeName(g.inv.Gate.Box.Type)
	if path == boxPkg {
		g.answerMethods(c, dir, fset, pkg, cfg)
		return
	}
	a := &answerReader{info: info, cfg: cfg, readers: set(cfg.Readers), makers: set(cfg.Makers)}
	for _, f := range files {
		rel, _ := filepath.Rel(dir, fset.Position(f.Pos()).Filename)
		rel = filepath.ToSlash(rel)
		var stack []ast.Node
		var fn []string
		function := func() string {
			if len(fn) == 0 {
				return path + ".<package>"
			}
			return fn[len(fn)-1]
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				if _, ok := stack[len(stack)-1].(*ast.FuncDecl); ok {
					fn = fn[:len(fn)-1]
				}
				if vs, ok := stack[len(stack)-1].(*ast.ValueSpec); ok && len(vs.Names) > 0 && len(stack) == 3 {
					fn = fn[:len(fn)-1]
				}
				stack = stack[:len(stack)-1]
				return true
			}
			parents := stack
			stack = append(stack, n)
			switch n := n.(type) {
			case *ast.FuncDecl:
				fn = append(fn, goFuncName(path, n))
			case *ast.ValueSpec:
				// A package-level var's value (a func literal, a composite literal of them) is read
				// as code of its own, named for the var.
				if len(parents) == 2 && len(n.Names) > 0 {
					fn = append(fn, path+"."+n.Names[0].Name)
				}
			}
			add := func(kind string, ok bool, what, why string) {
				p := fset.Position(n.Pos())
				c.Answers = append(c.Answers, finding{File: rel, Line: p.Line, Function: function(), What: what, Kind: kind, OK: ok, Why: why})
			}
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if !a.responseBody(n) {
					return true
				}
				switch {
				case a.closed(n, parents):
					add("close", true, exprString(n), "the body closed")
				case a.assigned(n, parents):
					add("set", true, exprString(n), "the body set, not read")
				default:
					add("body", false, exprString(n), "a read of an answer's body outside "+readerNames(cfg)+": read it through the reader, whose Answer decodes it into typed values with the request's record, prints it only as the rule reads it, or streams it as data")
				}
			case *ast.CallExpr:
				name := a.calleeName(n)
				switch {
				case slices.Contains(cfg.Reader, name):
					add("read", true, exprString(n), "the answer read through "+shortName(name))
				case a.readers[name]:
					add("body", false, exprString(n), name+" reads the answer's body outside "+readerNames(cfg))
				case a.makers[name] && !a.constant(n):
					ok, why := a.recorded(n, parents)
					add("maker", ok, exprString(n), why)
				}
			}
			return true
		})
	}
}

// answerMethods lists the reader's type with its exported method set, OK only where it is exactly
// gate.answers.methods; a type that is not in pkg is listed as not found.
func (g *gate) answerMethods(c *census, dir string, fset *token.FileSet, pkg *types.Package, cfg answersConfig) {
	_, name := splitTypeName(cfg.Answer)
	obj := pkg.Scope().Lookup(name)
	if obj == nil {
		c.Answers = append(c.Answers, finding{Kind: "methods", What: cfg.Answer, Why: "the reader's type is not declared in its package"})
		return
	}
	var got []string
	for _, t := range []types.Type{obj.Type(), types.NewPointer(obj.Type())} {
		ms := types.NewMethodSet(t)
		for i := range ms.Len() {
			if m := ms.At(i).Obj(); m.Exported() && !slices.Contains(got, m.Name()) {
				got = append(got, m.Name())
			}
		}
	}
	sort.Strings(got)
	want := slices.Clone(cfg.Methods)
	sort.Strings(want)
	p := fset.Position(obj.Pos())
	rel, _ := filepath.Rel(dir, p.Filename)
	f := finding{File: filepath.ToSlash(rel), Line: p.Line, Function: cfg.Answer, What: strings.Join(got, ", "), Kind: "methods"}
	if slices.Equal(got, want) {
		f.OK, f.Why = true, "its exported methods are exactly "+strings.Join(want, ", ")
	} else {
		f.Why = "its exported methods are " + strings.Join(got, ", ") + ", not exactly " + strings.Join(want, ", ") + ": a method that answers the body's bytes lets a client print them"
	}
	c.Answers = append(c.Answers, f)
}

type answerReader struct {
	info            *types.Info
	cfg             answersConfig
	readers, makers map[string]bool
}

// responseBody reports whether sel selects the Body field of a net/http.Response, directly or
// through an embedded one.
func (a *answerReader) responseBody(sel *ast.SelectorExpr) bool {
	s := a.info.Selections[sel]
	if s == nil || s.Kind() != types.FieldVal {
		return false
	}
	v, ok := s.Obj().(*types.Var)
	if !ok || v.Name() != "Body" || v.Pkg() == nil || v.Pkg().Path() != "net/http" {
		return false
	}
	t := s.Recv()
	index := s.Index()
	for i, k := range index {
		t = derefType(t)
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			return false
		}
		if i == len(index)-1 {
			n, ok := types.Unalias(t).(*types.Named)
			return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "net/http" && n.Obj().Name() == "Response"
		}
		t = st.Field(k).Type()
	}
	return false
}

// closed reports whether sel, a response's Body, is the receiver of its Close, called.
func (a *answerReader) closed(sel ast.Expr, parents []ast.Node) bool {
	if len(parents) < 2 {
		return false
	}
	closeSel, ok := parents[len(parents)-1].(*ast.SelectorExpr)
	if !ok || closeSel.X != sel || closeSel.Sel.Name != "Close" {
		return false
	}
	call, ok := parents[len(parents)-2].(*ast.CallExpr)
	return ok && call.Fun == closeSel
}

// assigned reports whether sel, a response's Body, is a target of an assignment.
func (a *answerReader) assigned(sel ast.Expr, parents []ast.Node) bool {
	as, ok := parents[len(parents)-1].(*ast.AssignStmt)
	return ok && slices.Contains(as.Lhs, sel)
}

func (a *answerReader) calleeName(call *ast.CallExpr) string {
	fun := ast.Unparen(call.Fun)
	if ix, ok := fun.(*ast.IndexExpr); ok {
		fun = ix.X
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
		return ""
	}
	return fn.Origin().FullName()
}

// constant reports whether every argument of call is a constant.
func (a *answerReader) constant(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if tv, ok := a.info.Types[arg]; !ok || tv.Value == nil {
			return false
		}
	}
	return true
}

// recorded says whether call, text held as the box, is held with the record of the answer it came
// from: the box's recorder called on it, given record(x)... or a local set only from record(x),
// where x is the response the text names. Text that names no response has no record to be held
// with.
func (a *answerReader) recorded(call *ast.CallExpr, parents []ast.Node) (bool, string) {
	from := a.response(call)
	var recorder *ast.CallExpr
	if len(parents) >= 2 {
		if sel, ok := parents[len(parents)-1].(*ast.SelectorExpr); ok && sel.X == call {
			if c, ok := parents[len(parents)-2].(*ast.CallExpr); ok && c.Fun == sel && a.calleeName(c) == a.cfg.Recorder {
				recorder = c
			}
		}
	}
	switch {
	case recorder == nil:
		return false, "text held as the box with no request record: hold it with " + shortName(a.cfg.Recorder) + "(" + shortName(a.cfg.Record) + "(resp)...), resp the answer it came from"
	case from == nil:
		return false, "text that names no answer, held as the box: no request record can hold it"
	}
	record := a.recordOf(recorder, parents)
	switch {
	case record == nil:
		return false, shortName(a.cfg.Recorder) + " is given no request record: hand it " + shortName(a.cfg.Record) + "(resp)..., or a local set only from it"
	case record != from:
		return false, "held with the record of " + record.Name() + ", another answer than " + from.Name() + ", whose text it is"
	}
	return true, "held with the record of " + from.Name() + ", the answer it came from"
}

// response is the response expr names: the first variable of type *net/http.Response or
// net/http.Response it holds, or nil.
func (a *answerReader) response(expr ast.Expr) types.Object {
	var found types.Object
	ast.Inspect(expr, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || found != nil {
			return found == nil
		}
		if obj := a.info.Uses[id]; obj != nil && isResponse(obj.Type()) {
			found = obj
		}
		return true
	})
	return found
}

func isResponse(t types.Type) bool {
	n, ok := types.Unalias(derefType(t)).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "net/http" && n.Obj().Name() == "Response"
}

// recordOf is the response whose record recorder is given: record(x)... directly, or a local that
// every assignment in the function sets from record(x); nil otherwise.
func (a *answerReader) recordOf(recorder *ast.CallExpr, parents []ast.Node) types.Object {
	if !recorder.Ellipsis.IsValid() || len(recorder.Args) != 1 {
		return nil
	}
	arg := ast.Unparen(recorder.Args[0])
	if c, ok := arg.(*ast.CallExpr); ok && a.calleeName(c) == a.cfg.Record && len(c.Args) == 1 {
		return a.response(c.Args[0])
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return nil
	}
	local := a.info.Uses[id]
	var body ast.Node
	for i := len(parents) - 1; i >= 0 && body == nil; i-- {
		switch p := parents[i].(type) {
		case *ast.FuncDecl:
			body = p
		case *ast.FuncLit:
			body = p
		}
	}
	if local == nil || body == nil {
		return nil
	}
	var from types.Object
	sets, fromRecord := 0, true
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			l, isID := lhs.(*ast.Ident)
			if !isID || (a.info.Defs[l] != local && a.info.Uses[l] != local) {
				continue
			}
			sets++
			if len(as.Lhs) != len(as.Rhs) {
				fromRecord = false
				continue
			}
			c, isCall := ast.Unparen(as.Rhs[i]).(*ast.CallExpr)
			if !isCall || a.calleeName(c) != a.cfg.Record || len(c.Args) != 1 {
				fromRecord = false
				continue
			}
			if r := a.response(c.Args[0]); from == nil || from == r {
				from = r
			} else {
				fromRecord = false
			}
		}
		return true
	})
	if sets == 0 || !fromRecord {
		return nil
	}
	return from
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

// recordedAnswers is how many reads of an answer through the reader the answer census listed.
func recordedAnswers(c census) int {
	n := 0
	for _, a := range c.Answers {
		if a.Kind == "read" {
			n++
		}
	}
	return n
}

// judgeAnswers fails each finding of the answer census that is not OK and that gate.answers.exempt
// does not name, each exemption that names none, a census that listed no read through the reader
// (UNKNOWN), and one that never found the reader's type.
func (g *gate) judgeAnswers(c census) {
	if recordedAnswers(c) == 0 {
		g.fail("stage 2 (answer census) UNKNOWN: it listed %d uses of an answer and no read through %s, so it read no client", len(c.Answers), readerNames(g.inv.Gate.Answers))
	}
	methods := false
	used := map[int]bool{}
	for _, a := range c.Answers {
		if a.Kind == "methods" {
			methods = true
		}
		if a.OK {
			continue
		}
		exempt := false
		for i, e := range g.inv.Gate.Answers.Exempt {
			if a.Kind == "maker" && e.File == a.File && e.Function == a.Function {
				exempt, used[i] = true, true
				fmt.Fprintf(g.out, "EXEMPT answer %s:%d %s in %s: %s\n", a.File, a.Line, a.What, a.Function, e.Why)
			}
		}
		if !exempt {
			g.fail("stage 2 (answer census) %s:%d %s in %s: %s", a.File, a.Line, a.What, a.Function, a.Why)
		}
	}
	if !methods {
		g.fail("stage 2 (answer census) UNKNOWN: the reader's type %s was not found", g.inv.Gate.Answers.Answer)
	}
	for i, e := range g.inv.Gate.Answers.Exempt {
		if !used[i] {
			g.fail("stage 2 (answer census) the exemption of %s in %s matches no text the census failed", e.Function, e.File)
		}
	}
}

// readerNames is the census's readers as a message names them: redact.ReadAnswer or
// redact.StreamAnswer.
func readerNames(cfg answersConfig) string {
	names := make([]string, len(cfg.Reader))
	for i, r := range cfg.Reader {
		names[i] = shortName(r)
	}
	return strings.Join(names, " or ")
}
