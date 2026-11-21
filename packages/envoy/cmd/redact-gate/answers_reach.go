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

// The answer census's rules for the ways an answer's words leave it other than a read of a
// response's fields (answerCensus), each a shape a program can check from the types alone:
//
//   - a reference to one of the standard library's readers of a response's body, or to a maker of
//     the box, other than its call: a function value or a method value, which the call rules
//     cannot follow, since a call through a value resolves to no function;
//   - a decode, outside Answer.Decode, into a value that holds the box (gate.answers.decoders, or
//     a type's own UnmarshalJSON or UnmarshalText called directly): a box filled so holds no record
//     of the request its answer answers. A decode inside a type's own UnmarshalJSON or
//     UnmarshalText is that type's part of the decode that calls it, which the census holds at that
//     call;
//   - net/http.ReadResponse given no request: the answer it reads has no record of what the
//     request carried, so no credential of it is redacted from the answer's words;
//   - a response handed whole to a parameter of interface type, which fmt, log, slog and
//     encoding/json take: printed so, it is the status line the answering party wrote and every
//     header it sent, as they came.
//
// In the box's own package, the exported functions that take the reader's type are exactly
// gate.answers.functions, as its methods are exactly gate.answers.methods (answerMethods): a
// function that answers the body's bytes lets a client print them as a method would.

// reference is the full name of the reader or maker n names, an identifier or a selector, where n
// is not the function a call calls, and whether it is one.
func (a *answerReader) reference(n ast.Node, parents []ast.Node) (string, bool) {
	var obj types.Object
	switch n := n.(type) {
	case *ast.Ident:
		if len(parents) > 0 {
			if sel, ok := parents[len(parents)-1].(*ast.SelectorExpr); ok && sel.Sel == n {
				return "", false
			}
		}
		obj = a.info.Uses[n]
	case *ast.SelectorExpr:
		if s := a.info.Selections[n]; s != nil {
			obj = s.Obj()
		} else {
			obj = a.info.Uses[n.Sel]
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return "", false
	}
	name := fn.Origin().FullName()
	if !a.readers[name] && !a.makers[name] || called(n, parents) {
		return "", false
	}
	return name, true
}

// called reports whether n is the function a call calls, through parentheses and a generic
// instantiation.
func called(n ast.Node, parents []ast.Node) bool {
	child := n
	for i := len(parents) - 1; i >= 0; i-- {
		switch p := parents[i].(type) {
		case *ast.ParenExpr:
			child = p
			continue
		case *ast.IndexExpr:
			if p.X == child {
				child = p
				continue
			}
		case *ast.IndexListExpr:
			if p.X == child {
				child = p
				continue
			}
		case *ast.CallExpr:
			return p.Fun == child
		}
		return false
	}
	return false
}

// decodedIntoBox is why call, a decoder's or a type's own UnmarshalJSON or UnmarshalText called
// directly, fills a value that holds the box outside Answer.Decode, or "". function is the code
// the call is in.
func (a *answerReader) decodedIntoBox(call *ast.CallExpr, function string) string {
	if unmarshalMethod(function) {
		return ""
	}
	name := a.calleeName(call)
	var into []ast.Expr
	switch {
	case a.decoders[name]:
		into = call.Args
	case unmarshalMethod(name):
		if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
			into = []ast.Expr{sel.X}
		}
	}
	for _, arg := range into {
		if t := a.info.TypeOf(arg); t != nil && a.holdsBox(t, map[types.Type]bool{}) {
			return fmt.Sprintf("%s fills a %s, which holds the box, outside %s.Decode: a box filled so holds no record of the request its answer answers; decode the answer with Answer.Decode",
				shortName(name), types.TypeString(t, func(p *types.Package) string { return p.Name() }), shortName(a.cfg.Answer))
		}
	}
	return ""
}

// unmarshalMethod reports whether name, a function's full name, is a type's UnmarshalJSON or
// UnmarshalText.
func unmarshalMethod(name string) bool {
	return strings.HasPrefix(name, "(") && (strings.HasSuffix(name, ").UnmarshalJSON") || strings.HasSuffix(name, ").UnmarshalText"))
}

// holdsBox reports whether a value of t holds the box anywhere: the box itself, or through a
// pointer, a field, a slice, an array, a map's key or value, or a channel.
func (a *answerReader) holdsBox(t types.Type, seen map[types.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		if t.Obj().Pkg() != nil && t.Obj().Pkg().Path() == a.boxPkg && t.Obj().Name() == a.boxName {
			return true
		}
		return a.holdsBox(t.Underlying(), seen)
	case *types.Pointer:
		return a.holdsBox(t.Elem(), seen)
	case *types.Slice:
		return a.holdsBox(t.Elem(), seen)
	case *types.Array:
		return a.holdsBox(t.Elem(), seen)
	case *types.Map:
		return a.holdsBox(t.Key(), seen) || a.holdsBox(t.Elem(), seen)
	case *types.Chan:
		return a.holdsBox(t.Elem(), seen)
	case *types.Struct:
		for i := range t.NumFields() {
			if a.holdsBox(t.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

// readsNoRequest reports whether call is net/http.ReadResponse given no request.
func (a *answerReader) readsNoRequest(call *ast.CallExpr) bool {
	return a.calleeName(call) == "net/http.ReadResponse" && len(call.Args) == 2 && isNil(call.Args[1])
}

// wholeResponse is the argument of call that is a response, or a pointer to one, handed to a
// parameter of interface type, or nil.
func (a *answerReader) wholeResponse(call *ast.CallExpr) ast.Expr {
	if tv, ok := a.info.Types[call.Fun]; !ok || tv.IsType() {
		return nil
	}
	sig, ok := a.info.TypeOf(call.Fun).Underlying().(*types.Signature)
	if !ok {
		return nil
	}
	params := sig.Params()
	for i, arg := range call.Args {
		var param types.Type
		switch last := params.Len() - 1; {
		case sig.Variadic() && i >= last:
			param = params.At(last).Type()
			if !call.Ellipsis.IsValid() {
				param = param.(*types.Slice).Elem()
			}
		case i < params.Len():
			param = params.At(i).Type()
		default:
			continue
		}
		if t := a.info.TypeOf(arg); t != nil && types.IsInterface(param) && isResponse(t) {
			return arg
		}
	}
	return nil
}

// answerFunctions lists the exported package-level functions of the box's package that take the
// reader's type, OK only where they are exactly gate.answers.functions.
func (g *gate) answerFunctions(c *census, dir string, fset *token.FileSet, pkg *types.Package, cfg answersConfig) {
	_, name := splitTypeName(cfg.Answer)
	answer, ok := pkg.Scope().Lookup(name).(*types.TypeName)
	if !ok {
		return
	}
	var got []string
	for _, n := range pkg.Scope().Names() {
		fn, ok := pkg.Scope().Lookup(n).(*types.Func)
		if !ok || !fn.Exported() {
			continue
		}
		params := fn.Type().(*types.Signature).Params()
		for i := range params.Len() {
			if mentions(params.At(i).Type(), answer, map[types.Type]bool{}) {
				got = append(got, fn.Name())
				break
			}
		}
	}
	sort.Strings(got)
	want := slices.Clone(cfg.Functions)
	sort.Strings(want)
	p := fset.Position(answer.Pos())
	rel, _ := filepath.Rel(dir, p.Filename)
	f := finding{File: filepath.ToSlash(rel), Line: p.Line, Function: cfg.Answer, What: strings.Join(got, ", "), Kind: "functions"}
	if slices.Equal(got, want) {
		f.OK, f.Why = true, "the exported functions that take it are exactly "+strings.Join(want, ", ")
	} else {
		f.Why = "the exported functions that take it are " + strings.Join(got, ", ") + ", not exactly " + strings.Join(want, ", ") + ": a function that answers the body's bytes lets a client print them"
	}
	c.Answers = append(c.Answers, f)
}

// mentions reports whether t is the named type of name, or reaches it through a pointer, a slice,
// an array, a map, a channel, a struct's field or a function's parameter or result.
func mentions(t types.Type, name *types.TypeName, seen map[types.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		return t.Obj() == name || mentions(t.Underlying(), name, seen)
	case *types.Pointer:
		return mentions(t.Elem(), name, seen)
	case *types.Slice:
		return mentions(t.Elem(), name, seen)
	case *types.Array:
		return mentions(t.Elem(), name, seen)
	case *types.Map:
		return mentions(t.Key(), name, seen) || mentions(t.Elem(), name, seen)
	case *types.Chan:
		return mentions(t.Elem(), name, seen)
	case *types.Struct:
		for i := range t.NumFields() {
			if mentions(t.Field(i).Type(), name, seen) {
				return true
			}
		}
	case *types.Signature:
		for _, tuple := range []*types.Tuple{t.Params(), t.Results()} {
			for i := range tuple.Len() {
				if mentions(tuple.At(i).Type(), name, seen) {
					return true
				}
			}
		}
	}
	return false
}
