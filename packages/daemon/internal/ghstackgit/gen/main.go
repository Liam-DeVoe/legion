// Command gen writes the git call shapes of a gh-stack release to standard output, as the Go
// source of package ghstackgit's shapes.go (generate.sh runs it). It reads every call by which
// gh-stack's internal/git starts git, through cli/cli's git.Client (Command and
// AuthenticatedCommand) or its own helpers (run, runRaw, runSilent, runInteractive, command,
// path, runRebaseCommand), in the functions gh-stack's other packages can reach (reachable), and
// every call of go-gh's internal/git Exec, and writes each as a shape: the argument list it
// passes, each argument a literal, a literal prefix (an argument built as a string that starts
// with `-` and a value appended), a format (an fmt.Sprintf of a literal format, each %s and %d
// numbered by its argument), or a value, with the optional, repeated and alternative parts the
// code builds under an if, a loop or an if and else. A value stands for an argument gh-stack
// computes; ghstackgit admits a git call only where it fits a shape and no value of it starts
// with `-`.
//
// It fails, naming the code, wherever a call builds its arguments in a way it does not read, so a
// release that adds one makes generate.sh fail rather than write a shape that admits too much.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// tok is one argument of a shape, or a group of them.
type tok struct {
	kind  byte // 'L' literal, 'P' prefix, 'F' format, 'V' value, 'A' literal alternatives, 'O' optional, 'R' repeated, 'X' alternative groups
	s     string
	alts  []string
	group []tok
	seqs  [][]tok
}

func lit(s string) tok    { return tok{kind: 'L', s: s} }
func value() tok          { return tok{kind: 'V'} }
func prefix(s string) tok { return tok{kind: 'P', s: s} }

// encode writes toks as ghstackgit's shape words: "=lit", "^prefix", "%format", "$", "|a|b",
// "[" … "]", "{" … "}", "(" … "/" … ")".
func encode(toks []tok) []string {
	var out []string
	for _, t := range toks {
		switch t.kind {
		case 'L':
			out = append(out, "="+t.s)
		case 'P':
			out = append(out, "^"+t.s)
		case 'F':
			out = append(out, "%"+t.s)
		case 'V':
			out = append(out, "$")
		case 'A':
			out = append(out, "|"+strings.Join(t.alts, "|"))
		case 'O':
			out = append(append(append(out, "["), encode(t.group)...), "]")
		case 'R':
			out = append(append(append(out, "{"), encode(t.group)...), "}")
		case 'X':
			out = append(out, "(")
			for i, seq := range t.seqs {
				if i > 0 {
					out = append(out, "/")
				}
				out = append(out, encode(seq)...)
			}
			out = append(out, ")")
		}
	}
	return out
}

func equal(a, b []tok) bool { return slices.Equal(encode(a), encode(b)) }

// files is a set of parsed Go files and the functions they declare.
type files struct {
	fset  *token.FileSet
	files []*ast.File
	funcs []*ast.FuncDecl
}

func parseDir(fset *token.FileSet, dir string, skip func(name string) bool) (*files, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fs := &files{fset: fset}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || skip(name) {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		fs.files = append(fs.files, file)
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				fs.funcs = append(fs.funcs, fn)
			}
		}
	}
	return fs, nil
}

// ctx is where an expression is read: its function, and the position of the call it feeds.
type ctx struct {
	fs    *files
	fn    *ast.FuncDecl
	pos   token.Pos
	depth int
}

func (c ctx) fail(node ast.Node, format string, args ...any) {
	panic(fmt.Sprintf("%s: %s", c.fs.fset.Position(node.Pos()), fmt.Sprintf(format, args...)))
}

func strLit(expr ast.Expr) (string, bool) {
	basic, ok := expr.(*ast.BasicLit)
	if !ok || basic.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(basic.Value)
	return s, err == nil
}

// argTok reads one argument expression.
func (c ctx) argTok(expr ast.Expr) tok {
	if s, ok := strLit(expr); ok {
		return lit(s)
	}
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return c.argTok(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return value()
		}
		left := ast.Expr(e)
		for {
			binary, ok := left.(*ast.BinaryExpr)
			if !ok || binary.Op != token.ADD {
				break
			}
			left = binary.X
		}
		if s, ok := strLit(left); ok && strings.HasPrefix(s, "-") {
			return prefix(s)
		}
		if t := c.argTok(left); t.kind == 'L' && strings.HasPrefix(t.s, "-") {
			return prefix(t.s)
		}
		return value()
	case *ast.CallExpr:
		// fmt.Sprintf with a literal format: the format itself, each %s and %d written as %<n>, the
		// number of its argument among the call's distinct ones, so a format that repeats a name
		// (`refs/heads/%s:refs/heads/%s` of b and b) is held to one name (ghstackgit reads each
		// piece as no `:`, no space and no leading `-`). A format with any other verb fails the
		// generation.
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" && len(e.Args) > 0 {
			if format, ok := strLit(e.Args[0]); ok {
				if other := strings.NewReplacer("%s", "", "%d", "").Replace(format); strings.Contains(other, "%") {
					c.fail(e, "a Sprintf format with a verb other than %%s and %%d: %q", format)
				}
				var distinct []string
				var numbered strings.Builder
				arg := 1
				for rest := format; ; {
					i := strings.IndexByte(rest, '%')
					if i < 0 {
						numbered.WriteString(rest)
						break
					}
					if arg >= len(e.Args) {
						c.fail(e, "a Sprintf format with more verbs than arguments: %q", format)
					}
					text := types.ExprString(e.Args[arg])
					n := slices.Index(distinct, text)
					if n < 0 {
						distinct, n = append(distinct, text), len(distinct)
					}
					if n > 8 {
						c.fail(e, "a Sprintf format with more than nine distinct arguments: %q", format)
					}
					numbered.WriteString(rest[:i] + "%" + strconv.Itoa(n+1))
					rest, arg = rest[i+2:], arg+1
				}
				return tok{kind: 'F', s: numbered.String()}
			}
		}
		return value()
	case *ast.Ident:
		return c.identTok(e)
	case *ast.SelectorExpr:
		return c.fieldTok(e)
	}
	return value()
}

// identTok reads a string variable: the literals it is assigned in its function; the element of a
// list it ranges over, where that list is built in the function of one kind of element; or, for a
// parameter of an unexported function, the literals every call in these files passes it; else a
// value.
func (c ctx) identTok(id *ast.Ident) tok {
	if c.depth > 8 {
		return value()
	}
	var found []tok
	ast.Inspect(c.fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) != len(s.Rhs) {
				return true
			}
			for i, lhs := range s.Lhs {
				if name, ok := lhs.(*ast.Ident); ok && name.Name == id.Name {
					found = append(found, ctx{c.fs, c.fn, c.pos, c.depth + 1}.argTok(s.Rhs[i]))
				}
			}
		case *ast.RangeStmt:
			v, ok := s.Value.(*ast.Ident)
			list, isIdent := s.X.(*ast.Ident)
			if !ok || v.Name != id.Name || !isIdent {
				return true
			}
			if _, param := paramIndex(c.fn, list.Name); param {
				found = append(found, value())
				return true
			}
			built := ctx{c.fs, c.fn, s.Pos(), c.depth + 1}.built(list.Name)
			if len(built) == 1 && built[0].kind == 'R' && len(built[0].group) == 1 {
				found = append(found, built[0].group[0])
			} else {
				found = append(found, value())
			}
		}
		return true
	})
	if len(found) == 0 {
		// A parameter of an unexported function, which nothing outside these files calls: the
		// literals its calls here pass.
		if index, ok := paramIndex(c.fn, id.Name); ok && !ast.IsExported(c.fn.Name.Name) {
			for _, call := range c.fs.callsOf(c.fn) {
				if index < len(call.call.Args) {
					found = append(found, ctx{c.fs, call.fn, call.call.Pos(), c.depth + 1}.argTok(call.call.Args[index]))
				}
			}
		}
	}
	return literals(found)
}

// literals is one literal, or the alternatives of several, where every tok found is a literal;
// the one tok found, where they are all one format; else a value.
func literals(found []tok) tok {
	if len(found) > 0 && found[0].kind == 'F' && !slices.ContainsFunc(found, func(t tok) bool { return t.kind != 'F' || t.s != found[0].s }) {
		return found[0]
	}
	var alts []string
	for _, t := range found {
		if t.kind != 'L' {
			return value()
		}
		if !slices.Contains(alts, t.s) {
			alts = append(alts, t.s)
		}
	}
	switch len(alts) {
	case 0:
		return value()
	case 1:
		return lit(alts[0])
	}
	slices.Sort(alts)
	return tok{kind: 'A', alts: alts}
}

// fieldTok reads x.f where x ranges over a composite literal of structs in the function: the
// literals at f of every element.
func (c ctx) fieldTok(sel *ast.SelectorExpr) tok {
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return value()
	}
	var found []tok
	ast.Inspect(c.fn.Body, func(n ast.Node) bool {
		loop, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if v, ok := loop.Value.(*ast.Ident); !ok || v.Name != x.Name {
			return true
		}
		list, ok := loop.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		array, ok := list.Type.(*ast.ArrayType)
		if !ok {
			return true
		}
		structType, ok := array.Elt.(*ast.StructType)
		if !ok {
			return true
		}
		index, i := -1, 0
		for _, field := range structType.Fields.List {
			for _, name := range field.Names {
				if name.Name == sel.Sel.Name {
					index = i
				}
				i++
			}
		}
		for _, elt := range list.Elts {
			if element, ok := elt.(*ast.CompositeLit); ok && index >= 0 && index < len(element.Elts) {
				found = append(found, c.argTok(element.Elts[index]))
			}
		}
		return true
	})
	return literals(found)
}

func paramIndex(fn *ast.FuncDecl, name string) (int, bool) {
	i := 0
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				return i, true
			}
			i++
		}
	}
	return 0, false
}

func isVariadicParam(fn *ast.FuncDecl, name string) bool {
	list := fn.Type.Params.List
	if len(list) == 0 {
		return false
	}
	last := list[len(list)-1]
	_, variadic := last.Type.(*ast.Ellipsis)
	return variadic && slices.ContainsFunc(last.Names, func(n *ast.Ident) bool { return n.Name == name })
}

// site is a call and the function it is in.
type site struct {
	fn   *ast.FuncDecl
	call *ast.CallExpr
}

// callsOf is every call of fn in these files: of a method by its name on any receiver, of a
// function by its name.
func (fs *files) callsOf(fn *ast.FuncDecl) []site {
	var sites []site
	for _, caller := range fs.funcs {
		ast.Inspect(caller.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fn.Recv != nil && f.Sel.Name == fn.Name.Name {
					sites = append(sites, site{caller, call})
				}
			case *ast.Ident:
				if fn.Recv == nil && f.Name == fn.Name.Name {
					sites = append(sites, site{caller, call})
				}
			}
			return true
		})
	}
	return sites
}

func (fs *files) function(name string) *ast.FuncDecl {
	for _, fn := range fs.funcs {
		if fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// seq reads an expression that is a list of arguments: a []string literal, an append, a call of
// a function of these files that returns one, a slice of one, or a variable built in the
// function before the call.
func (c ctx) seq(expr ast.Expr) []tok {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		var toks []tok
		for _, elt := range e.Elts {
			toks = append(toks, c.argTok(elt))
		}
		return toks
	case *ast.CallExpr:
		name, _ := e.Fun.(*ast.Ident)
		switch {
		case name != nil && name.Name == "append":
			toks := slices.Clone(c.seq(e.Args[0]))
			for i, arg := range e.Args[1:] {
				if e.Ellipsis.IsValid() && i == len(e.Args)-2 {
					toks = append(toks, c.seq(arg)...)
				} else {
					toks = append(toks, c.argTok(arg))
				}
			}
			return toks
		case name != nil && name.Name == "make":
			return nil
		case name != nil:
			if fn := c.fs.function(name.Name); fn != nil {
				return c.returned(fn)
			}
		}
	case *ast.SliceExpr:
		low, ok := strLitInt(e.Low)
		toks := c.seq(e.X)
		if !ok || e.High != nil || low > len(toks) {
			c.fail(e, "a slice of an argument list this reads only as [n:]")
		}
		for _, t := range toks[:low] {
			if t.kind != 'L' && t.kind != 'V' && t.kind != 'P' && t.kind != 'A' {
				c.fail(e, "a slice that drops a group")
			}
		}
		return toks[low:]
	case *ast.Ident:
		if _, ok := paramIndex(c.fn, e.Name); ok {
			// A list the function is given: its values, any number of them.
			return []tok{{kind: 'R', group: []tok{value()}}}
		}
		return c.built(e.Name)
	}
	c.fail(expr, "an argument list this does not read")
	return nil
}

func strLitInt(expr ast.Expr) (int, bool) {
	basic, ok := expr.(*ast.BasicLit)
	if !ok || basic.Kind != token.INT {
		return 0, false
	}
	n, err := strconv.Atoi(basic.Value)
	return n, err == nil
}

// returned is the list fn returns.
func (c ctx) returned(fn *ast.FuncDecl) []tok {
	var ret *ast.ReturnStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
			ret = r
		}
		return true
	})
	if ret == nil {
		c.fail(fn, "%s returns no argument list", fn.Name.Name)
	}
	return ctx{c.fs, fn, ret.Pos(), c.depth + 1}.seq(ret.Results[0])
}

// built is the list the variable name holds at the call, built by the statements of the function
// before it.
func (c ctx) built(name string) []tok {
	b := builder{c: c, name: name}
	toks, _ := b.block(c.fn.Body.List, nil)
	return toks
}

// builder follows the statements that build one list variable.
type builder struct {
	c    ctx
	name string
}

// block applies the statements of list before the call to toks; reached says the call is in it.
func (b builder) block(list []ast.Stmt, toks []tok) ([]tok, bool) {
	for _, stmt := range list {
		if stmt.Pos() > b.c.pos {
			return toks, true
		}
		if stmt.End() > b.c.pos {
			// The statement holding the call: the statements before it in its block.
			switch s := stmt.(type) {
			case *ast.IfStmt:
				if s.Body.Pos() <= b.c.pos && b.c.pos < s.Body.End() {
					return b.block(s.Body.List, toks)
				}
				if block, ok := s.Else.(*ast.BlockStmt); ok {
					return b.block(block.List, toks)
				}
			case *ast.RangeStmt:
				return b.block(s.Body.List, toks)
			case *ast.ForStmt:
				return b.block(s.Body.List, toks)
			case *ast.BlockStmt:
				return b.block(s.List, toks)
			}
			return toks, true
		}
		toks = b.stmt(stmt, toks)
	}
	return toks, false
}

func (b builder) stmt(stmt ast.Stmt, toks []tok) []tok {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		for i, lhs := range s.Lhs {
			if i >= len(s.Rhs) {
				break
			}
			switch l := lhs.(type) {
			case *ast.Ident:
				if l.Name == b.name {
					toks = b.self(toks).seq(s.Rhs[i])
				}
			case *ast.IndexExpr:
				if x, ok := l.X.(*ast.Ident); ok && x.Name == b.name {
					toks = append(slices.Clone(toks), b.c.argTok(s.Rhs[i]))
				}
			}
		}
	case *ast.IfStmt:
		then := b.delta(s.Body.List, toks)
		var other []tok
		if s.Else != nil {
			switch e := s.Else.(type) {
			case *ast.BlockStmt:
				other = b.delta(e.List, toks)
			case *ast.IfStmt:
				other = b.delta([]ast.Stmt{e}, toks)
			}
		}
		switch {
		case len(then) == 0 && len(other) == 0:
		case s.Else == nil:
			toks = append(slices.Clone(toks), tok{kind: 'O', group: then})
		case equal(then, other):
			toks = append(slices.Clone(toks), then...)
		default:
			toks = append(slices.Clone(toks), tok{kind: 'X', seqs: [][]tok{then, other}})
		}
	case *ast.RangeStmt:
		if delta := b.delta(s.Body.List, toks); len(delta) > 0 {
			toks = append(slices.Clone(toks), tok{kind: 'R', group: delta})
		}
	case *ast.ForStmt:
		if delta := b.delta(s.Body.List, toks); len(delta) > 0 {
			toks = append(slices.Clone(toks), tok{kind: 'R', group: delta})
		}
	case *ast.BlockStmt:
		for _, inner := range s.List {
			toks = b.stmt(inner, toks)
		}
	}
	return toks
}

// delta is what list appends to toks; a list that does anything else to it fails.
func (b builder) delta(list []ast.Stmt, toks []tok) []tok {
	after := toks
	for _, stmt := range list {
		after = b.stmt(stmt, after)
	}
	if len(after) < len(toks) || !equal(after[:len(toks)], toks) {
		b.c.fail(list[0], "a branch that does more than append to %s", b.name)
	}
	return after[len(toks):]
}

// self is a ctx whose reads of the variable being built give toks, its value so far.
func (b builder) self(toks []tok) selfCtx { return selfCtx{b, toks} }

type selfCtx struct {
	b    builder
	toks []tok
}

func (s selfCtx) seq(expr ast.Expr) []tok {
	if id, ok := expr.(*ast.Ident); ok && id.Name == s.b.name {
		return s.toks
	}
	if call, ok := expr.(*ast.CallExpr); ok {
		if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "append" {
			toks := slices.Clone(s.seq(call.Args[0]))
			for i, arg := range call.Args[1:] {
				if call.Ellipsis.IsValid() && i == len(call.Args)-2 {
					toks = append(toks, s.seq(arg)...)
				} else {
					toks = append(toks, s.b.c.argTok(arg))
				}
			}
			return toks
		}
	}
	return s.b.c.seq(expr)
}

// runners are the calls in gh-stack that start git, by the name they are called by.
var runners = map[string]runner{
	"run": {0, false}, "runRaw": {0, false}, "runSilent": {0, false}, "runInteractive": {0, false},
	"command": {0, false}, "path": {0, false},
	"Command": {1, false}, "AuthenticatedCommand": {2, false},
	"runRebaseCommand": {0, true},
}

// runner is a call that starts git: the index of its first git argument, and whether its git
// arguments are one list rather than a variadic.
type runner struct {
	first int
	list  bool
}

// runnerOf says whether call starts git: in gh-stack, a call of one of runners by its method
// name (Command on anything but os/exec); in go-gh, a call of its Exec.
func runnerOf(call *ast.CallExpr, goGh bool) (string, runner, bool) {
	if goGh {
		if f, ok := call.Fun.(*ast.Ident); ok && f.Name == "Exec" {
			return f.Name, runner{}, true
		}
		return "", runner{}, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", runner{}, false
	}
	if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" {
		return "", runner{}, false
	}
	r, ok := runners[sel.Sel.Name]
	return sel.Sel.Name, r, ok
}

// shapes reads every call of fs that starts git, in the functions reach admits (all, where nil).
func shapes(fs *files, goGh bool, credential []tok, reach map[*ast.FuncDecl]bool) [][]tok {
	var out [][]tok
	for _, fn := range fs.funcs {
		if reach != nil && !reach[fn] {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, r, ok := runnerOf(call, goGh)
			if !ok || len(call.Args) <= r.first {
				return true
			}
			c := ctx{fs: fs, fn: fn, pos: call.Pos()}
			args := call.Args[r.first:]
			// A runner that hands its own arguments on, as runRaw hands them to command: no call of
			// its own.
			if _, isRunner := runners[fn.Name.Name]; isRunner && !goGh {
				if id, ok := args[len(args)-1].(*ast.Ident); ok {
					if _, param := paramIndex(fn, id.Name); param {
						return true
					}
				}
			}
			var toks []tok
			if r.list {
				toks = c.seq(args[0])
			} else {
				for i, arg := range args {
					if call.Ellipsis.IsValid() && i == len(args)-1 {
						toks = append(toks, c.seq(arg)...)
					} else {
						toks = append(toks, c.argTok(arg))
					}
				}
			}
			if name == "AuthenticatedCommand" {
				toks = append(slices.Clone(credential), toks...)
			}
			if !goGh {
				// cli/cli's git.Client.Command puts `-C <RepoDir>` first where RepoDir is set.
				toks = append([]tok{{kind: 'O', group: []tok{lit("-C"), value()}}}, toks...)
			}
			out = append(out, toks)
			return true
		})
	}
	return out
}

// reachable is every function of stack gh-stack can run: those its other packages call by name
// (a selector's method or function name, gh-stack calling git.X and an Ops value's X), and every
// function of stack those call by name, and so on. A function nothing calls, such as one kept for
// an older command, starts no git gh-stack makes, so its call is no shape.
func reachable(stack *files, root string) map[*ast.FuncDecl]bool {
	called := map[string]bool{}
	gitDir := filepath.Join(root, "internal", "git")
	check(filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (path == gitDir || entry.Name() == "scripts" || entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					called[sel.Sel.Name] = true
				}
			}
			return true
		})
		return nil
	}))
	reach := map[*ast.FuncDecl]bool{}
	var visit func(fn *ast.FuncDecl)
	visit = func(fn *ast.FuncDecl) {
		if reach[fn] {
			return
		}
		reach[fn] = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				name = f.Sel.Name
			case *ast.Ident:
				name = f.Name
			}
			for _, other := range stack.funcs {
				if other.Name.Name == name {
					visit(other)
				}
			}
			return true
		})
	}
	for _, fn := range stack.funcs {
		if ast.IsExported(fn.Name.Name) && called[fn.Name.Name] {
			visit(fn)
		}
	}
	return reach
}

func main() {
	ghStack := flag.String("gh-stack", "", "gh-stack's source, at the release")
	cli := flag.String("cli", "", "cli/cli's source, at the version gh-stack's go.mod names")
	goGh := flag.String("go-gh", "", "go-gh's source, at the version gh-stack's go.mod names")
	version := flag.String("version", "", "the gh-stack release")
	flag.Parse()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "gen:", r)
			os.Exit(1)
		}
	}()

	// cli/cli's client builds these two prefixes (git/client.go, Command and AuthenticatedCommand,
	// with AllMatchingCredentialsPattern, the one gh-stack passes, and GhPath left empty, which
	// gh-stack never sets, so the helper is `!"gh" auth git-credential`); they are checked here, so
	// a release that builds them otherwise fails.
	client, err := os.ReadFile(filepath.Join(*cli, "git", "client.go"))
	check(err)
	for _, want := range []string{
		`args = append([]string{"-C", c.RepoDir}, args...)`,
		`c.GhPath = "gh"`,
		`credHelper := fmt.Sprintf("!%q auth git-credential", c.GhPath)`,
		`preArgs = []string{"-c", "credential.helper="}`,
		`preArgs = append(preArgs, "-c", fmt.Sprintf("credential.helper=%s", credHelper))`,
	} {
		if !strings.Contains(string(client), want) {
			panic(fmt.Sprintf("cli/cli's git/client.go no longer holds %q", want))
		}
	}
	check(filepath.WalkDir(*ghStack, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		source, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(source), "GhPath") {
			panic(fmt.Sprintf("%s names GhPath, so the credential helper may be another than gh", path))
		}
		return err
	}))
	credential := []tok{lit("-c"), lit("credential.helper="), lit("-c"), lit(`credential.helper=!"gh" auth git-credential`)}

	fset := token.NewFileSet()
	stack, err := parseDir(fset, filepath.Join(*ghStack, "internal", "git"), func(name string) bool { return strings.HasPrefix(name, "mock") })
	check(err)
	gogh, err := parseDir(fset, filepath.Join(*goGh, "internal", "git"), func(string) bool { return false })
	check(err)

	all := append(shapes(stack, false, credential, reachable(stack, *ghStack)), shapes(gogh, true, nil, nil)...)
	seen := map[string]bool{}
	var lines []string
	for _, toks := range all {
		words := encode(toks)
		for _, word := range words {
			if (word[0] == '=' || word[0] == '^') && strings.ContainsAny(word[1:], "\n") {
				panic(fmt.Sprintf("a shape word holds a newline: %q", word))
			}
			if word[0] == '|' && strings.Contains(word, "||") {
				panic(fmt.Sprintf("an alternative holds an empty literal: %q", word))
			}
		}
		quoted := make([]string, len(words))
		for i, word := range words {
			quoted[i] = strconv.Quote(word)
		}
		line := "\t{" + strings.Join(quoted, ", ") + "},"
		if !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	slices.Sort(lines)
	if len(lines) < 40 {
		panic(fmt.Sprintf("read only %d shapes", len(lines)))
	}
	fmt.Printf("// Code generated by generate.sh from github/gh-stack v%s, with cli/cli and go-gh at the versions its go.mod names; DO NOT EDIT.\n\n", *version)
	fmt.Printf("package ghstackgit\n\n")
	fmt.Printf("// Version is the gh-stack release shapes was read from.\nconst Version = %q\n\n", *version)
	fmt.Printf("// shapes are the argument lists gh-stack v%s starts git with (gen/main.go says how they read).\nvar shapes = [][]string{\n%s\n}\n", *version, strings.Join(lines, "\n"))
}

func check(err error) {
	if err != nil {
		panic(err.Error())
	}
}
