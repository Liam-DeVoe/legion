//go:build ignore

// Setting-prints is the Go half of scripts/checks/setting-prints.ts, which runs it: the search for
// every line that prints a value read from a setting, by the value and not by its name.
//
//	go run scripts/checks/setting-prints.go <module directory> <file>...
//
// It reads the packages that hold the named files from source, type-checked together (every other
// package from the export data `go list -export` names), and follows each value read from the
// environment (os.Getenv, os.LookupEnv and those functions handed on as values, os.Environ, the
// settings table a constant names a variable of, os.Args, the flag package) or from a file
// (os.ReadFile, os.Open) through every variable, field, parameter, result, map value, slice,
// function value and composite literal it reaches, to every line that prints it: the message of an
// error (fmt.Errorf, errors.New), a log call (log/slog, log), fmt's printing functions, panic,
// http.Error and a child's command line (os/exec). A variable of a function holds what the
// assignments that can reach the read put there, not every assignment of it. A value of a type
// that cannot hold text ends its path, and so does every function of the redact package, the one
// place that decides what a line shows of a setting's URL. A function of a package it does not read
// hands on the text it is handed only when that package is one of keepsText's (url.Parse's,
// strconv's and pgx's errors quote their input); any such function hands on what a function value it
// is handed answers.
//
// A line it reports is where a setting as read is written into text; a print of text another line
// built is an end of that line. A setting can be a URL when its value reaches a URL taker (urlTakers,
// redact) as the URL or the head of its text, or a function of the module this program does not read.
// It prints one JSON line for each such line of every file of the packages it reads
// ({"finding": …}), one for each line of a setting that reaches no URL ({"quiet": …}), one with what
// its planted shapes gave when any was not as marked ({"blind": […]}), and a summary
// ({"summary": "…"}). The planted shapes are one more file of the first named file's package, read
// with it: a line marked `planted: found` must be found and one marked `planted: quiet` must not.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const planted = `package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/sjawhar/envoy/internal/redact"
)

type plantedBoot struct{ Issuer string }

func plantedRefusal(env settingValues) error {
	value := strings.TrimSpace(env.get("PLANTED_URL"))
	if _, err := url.Parse(value); err == nil {
		return nil
	}
	return fmt.Errorf("PLANTED_URL=%q is refused", value) // planted: found
}

func plantedJudged(env settingValues) error {
	return fmt.Errorf("PLANTED_URL=%q is refused", redact.URL(env.get("PLANTED_URL"))) // planted: quiet
}

func plantedLog(boot plantedBoot) {
	_, _ = url.Parse(boot.Issuer)
	slog.Info("planted", "issuer", boot.Issuer) // planted: found
}

func plantedLength() error {
	return errors.New("PLANTED_URL is " + fmt.Sprint(len(os.Getenv("PLANTED_URL"))) + " long") // planted: quiet
}

func plantedMode(env settingValues) error {
	return fmt.Errorf("PLANTED_MODE=%q is no mode", env.get("PLANTED_MODE")) // planted: quiet
}

func plantedParse(raw string) error {
	_, err := url.Parse(raw)
	return err
}

func plantedCalls() {
	env := processSettings()
	_ = plantedRefusal(env)
	_ = plantedJudged(env)
	plantedLog(plantedBoot{Issuer: env.get("PLANTED_ISSUER")})
	_ = plantedLength()
	_ = plantedMode(env)
	fmt.Println(plantedParse(os.Getenv("PLANTED_PARSE"))) // planted: found
}
`

type listed struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Export     string
	Module     *struct{ Path string }
}

type finding struct {
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Text        string   `json:"text"`
	Hows        []string `json:"hows"`
	Expressions []string `json:"expressions"`
	Sources     []string `json:"sources"`
	Ends        []string `json:"ends"`
}

type label struct {
	kind    string // "source", "written", "function" or "environment function"
	keyed   bool   // a source: the environment as a whole, which a constant key names more closely
	what    string // a source: what it reads, and where
	how     string // a written line: what writes the value into text
	file    string
	line    int
	expr    string
	sources map[int]bool
	leading map[int]bool // a written line: the settings written at the head of its text
	ends    map[string]bool
	fn      any // a function: its *types.Func or *ast.FuncLit
}

type set map[int]bool

func union(sets ...set) set {
	var first set
	var out set
	for _, s := range sets {
		if len(s) == 0 {
			continue
		}
		if first == nil {
			first = s
			continue
		}
		if out == nil {
			out = make(set, len(first)+len(s))
			for id := range first {
				out[id] = true
			}
		}
		for id := range s {
			out[id] = true
		}
	}
	if out != nil {
		return out
	}
	return first
}

type analysis struct {
	root    string
	fset    *token.FileSet
	info    *types.Info
	text    map[string][]byte
	bodies  map[*types.Func]*ast.FuncDecl
	labels  []*label
	ids     map[string]int
	carried map[any]set
	changed bool
	memo    map[ast.Expr]set
	collect bool
	report  map[string]bool
	read    map[string]bool // the import paths of the packages read from source
	module  string          // the module's path
	urls    map[int]bool    // the sources whose value reaches a URL taker
	sites   map[*types.Var]map[token.Pos]*site
	lits    []span // every function literal
	blocks  []span // every block and case body
}

// site is one assignment of a variable of a function: the block it stands in, and what it put.
type site struct {
	block  span
	labels set
}

func (a *analysis) intern(key string, make func() *label) int {
	if id, ok := a.ids[key]; ok {
		return id
	}
	a.labels = append(a.labels, make())
	a.ids[key] = len(a.labels) - 1
	return len(a.labels) - 1
}

func (a *analysis) position(n ast.Node) (string, int) {
	p := a.fset.Position(n.Pos())
	rel, err := filepath.Rel(a.root, p.Filename)
	if err != nil {
		rel = p.Filename
	}
	return rel, p.Line
}

func (a *analysis) source(n ast.Node, what string) set {
	file, line := a.position(n)
	where := fmt.Sprintf("%s:%d", file, line)
	return set{a.intern("s "+where+" "+what, func() *label { return &label{kind: "source", what: what + " (" + where + ")"} }): true}
}

func (a *analysis) fnLabel(kind string, fn any) set {
	return set{a.intern(fmt.Sprintf("f %p %s", fn, kind), func() *label { return &label{kind: kind, fn: fn} }): true}
}

func (a *analysis) exprText(n ast.Node) string {
	start, end := a.fset.Position(n.Pos()), a.fset.Position(n.End())
	src := a.text[start.Filename]
	if src == nil || end.Offset > len(src) {
		return ""
	}
	text := strings.Join(strings.Fields(string(src[start.Offset:end.Offset])), " ")
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}

func (a *analysis) carries(s set) bool {
	for id := range s {
		if a.labels[id].kind == "source" {
			return true
		}
	}
	return false
}

// settingsIn are the settings a value carries: each it holds as read, and each that a line wrote
// into the text it holds.
func (a *analysis) settingsIn(s set) []int {
	var out []int
	seen := map[int]bool{}
	for id := range s {
		l := a.labels[id]
		switch l.kind {
		case "source":
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		case "written":
			for each := range l.sources {
				if !seen[each] {
					seen[each] = true
					out = append(out, each)
				}
			}
		}
	}
	return out
}

// write is the value at n written into text by how. When the value holds a setting as read, the
// line is labelled, and the text it makes carries that label in place of the setting: a line that
// writes that text again is not a second line that writes the setting.
func (a *analysis) write(n ast.Node, how string, value set) set {
	if !a.carries(value) {
		return value
	}
	file, line := a.position(n)
	id := a.intern(fmt.Sprintf("w %s %d %s", file, a.fset.Position(n.Pos()).Offset, how), func() *label {
		return &label{kind: "written", how: how, file: file, line: line, expr: a.exprText(n), sources: map[int]bool{}, leading: map[int]bool{}, ends: map[string]bool{}}
	})
	out := set{id: true}
	for s := range value {
		if a.labels[s].kind == "source" {
			a.labels[id].sources[s] = true
		} else {
			out[s] = true
		}
	}
	return out
}

// writeHead is write for a value written at the head of the text, where a URL's scheme and host
// stand: the left operand of a string +.
func (a *analysis) writeHead(n ast.Node, how string, value set) set {
	out := a.write(n, how, value)
	for id := range out {
		if l := a.labels[id]; l.kind == "written" && !value[id] {
			for s := range value {
				if a.labels[s].kind == "source" {
					l.leading[s] = true
				}
			}
		}
	}
	return out
}

// leadingIn are the settings a value holds as read, and those a line wrote at the head of its text.
func (a *analysis) leadingIn(s set) []int {
	var out []int
	for id := range s {
		switch l := a.labels[id]; l.kind {
		case "source":
			out = append(out, id)
		case "written":
			for each := range l.leading {
				out = append(out, each)
			}
		}
	}
	return out
}

func (a *analysis) grow(key any, value set) {
	if len(value) == 0 || key == nil {
		return
	}
	held := a.carried[key]
	if held == nil {
		held = set{}
		a.carried[key] = held
	}
	for id := range value {
		if !held[id] {
			held[id] = true
			a.changed = true
		}
	}
}

func origin(o types.Object) types.Object {
	switch v := o.(type) {
	case *types.Func:
		return v.Origin()
	case *types.Var:
		return v.Origin()
	}
	return o
}

func (a *analysis) object(id *ast.Ident) types.Object {
	if o := a.info.Uses[id]; o != nil {
		return origin(o)
	}
	if o := a.info.Defs[id]; o != nil {
		return origin(o)
	}
	return nil
}

func packagePath(o types.Object) string {
	if o == nil || o.Pkg() == nil {
		return ""
	}
	return o.Pkg().Path()
}

// calleeOf is the function a call runs when the call names it, or nil.
func (a *analysis) calleeOf(call *ast.CallExpr) *types.Func {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if fn, ok := a.object(f).(*types.Func); ok {
			return fn
		}
	case *ast.SelectorExpr:
		if sel := a.info.Selections[f]; sel != nil {
			if fn, ok := sel.Obj().(*types.Func); ok && sel.Kind() == types.MethodVal {
				return origin(fn).(*types.Func)
			}
			return nil
		}
		if fn, ok := a.object(f.Sel).(*types.Func); ok {
			return fn
		}
	case *ast.IndexExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if fn, ok := a.object(id).(*types.Func); ok {
				return fn
			}
		}
	}
	return nil
}

func sanitizer(fn *types.Func) bool { return strings.HasSuffix(packagePath(fn), "/internal/redact") }

func inert(t types.Type) bool {
	if t == nil {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Info()&types.IsString == 0 && u.Kind() != types.UnsafePointer && u.Kind() != types.Invalid && u.Kind() != types.UntypedNil
	case *types.Signature:
		return true
	}
	return false
}

func (a *analysis) ev(e ast.Expr) set {
	if e == nil {
		return nil
	}
	if v, ok := a.memo[e]; ok {
		return v
	}
	a.memo[e] = nil
	v := a.evaluate(e)
	if len(v) > 0 && inert(a.info.TypeOf(e)) {
		kept := set{}
		if _, fn := a.info.TypeOf(e).Underlying().(*types.Signature); fn {
			for id := range v {
				if k := a.labels[id].kind; k == "function" || k == "environment function" {
					kept[id] = true
				}
			}
		}
		v = kept
	}
	a.memo[e] = v
	return v
}

func (a *analysis) funcValue(o types.Object) set {
	fn, ok := o.(*types.Func)
	if !ok {
		return nil
	}
	if packagePath(fn) == "os" && (fn.Name() == "Getenv" || fn.Name() == "LookupEnv") {
		return a.fnLabel("environment function", fn)
	}
	if a.bodies[fn] != nil {
		return a.fnLabel("function", fn)
	}
	return nil
}

func (a *analysis) evaluate(e ast.Expr) set {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return a.ev(x.X)
	case *ast.Ident:
		o := a.object(x)
		if v, ok := o.(*types.Var); ok {
			if local(v) {
				return a.reaching(v, x.Pos())
			}
			return a.carried[types.Object(v)]
		}
		return a.funcValue(o)
	case *ast.SelectorExpr:
		if sel := a.info.Selections[x]; sel != nil {
			if sel.Kind() == types.FieldVal {
				// A field of a struct this program reads is followed by itself: every write of it is
				// read. One of a value a function it does not read made holds what that value holds.
				if a.ours(a.info.TypeOf(x.X)) {
					return a.carried[origin(sel.Obj())]
				}
				return union(a.carried[origin(sel.Obj())], a.ev(x.X))
			}
			return a.funcValue(origin(sel.Obj()))
		}
		o := a.object(x.Sel)
		if packagePath(o) == "os" && o.Name() == "Args" {
			return a.source(x, "os.Args")
		}
		if v, ok := o.(*types.Var); ok {
			return a.carried[types.Object(v)]
		}
		return a.funcValue(o)
	case *ast.CallExpr:
		return a.evaluateCall(x)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			return union(a.writeHead(x.X, "a string +", a.ev(x.X)), a.write(x.Y, "a string +", a.ev(x.Y)))
		}
		return nil
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return a.ev(x.X)
		}
		return nil
	case *ast.StarExpr:
		return a.ev(x.X)
	case *ast.IndexExpr:
		return a.ev(x.X)
	case *ast.IndexListExpr:
		return a.ev(x.X)
	case *ast.SliceExpr:
		return a.ev(x.X)
	case *ast.TypeAssertExpr:
		return a.ev(x.X)
	case *ast.KeyValueExpr:
		return a.ev(x.Value)
	case *ast.CompositeLit:
		var all []set
		for _, el := range x.Elts {
			all = append(all, a.ev(el))
		}
		return union(all...)
	case *ast.FuncLit:
		return a.fnLabel("function", x)
	}
	return nil
}

var renders = map[string]bool{
	"fmt.Sprintf": true, "fmt.Sprint": true, "fmt.Sprintln": true, "fmt.Errorf": true, "fmt.Appendf": true,
	"strings.Join": true, "strconv.Quote": true, "errors.New": true,
}

// variable is a variable of the environment, the same label wherever it is read.
func (a *analysis) variable(name string) int {
	return a.intern("v "+name, func() *label {
		return &label{kind: "source", what: "the environment variable " + name}
	})
}

// envName is the spelling of a variable of the environment.
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// named is value with the environment as a whole in it named by the constant call is handed that
// is spelled as a variable of the environment, when it is handed one: the variable of that name.
func (a *analysis) named(call *ast.CallExpr, value set) set {
	key := ""
	for _, arg := range call.Args {
		if tv, ok := a.info.Types[arg]; ok && tv.Value != nil && tv.Value.Kind() == constant.String && envName.MatchString(constant.StringVal(tv.Value)) {
			key = constant.StringVal(tv.Value)
			break
		}
	}
	if key == "" {
		return value
	}
	var out set
	for id := range value {
		if l := a.labels[id]; l.kind == "source" && l.keyed {
			if out == nil {
				out = set{}
				for each := range value {
					out[each] = true
				}
			}
			delete(out, id)
			out[a.variable(key)] = true
		}
	}
	if out == nil {
		return value
	}
	return out
}

// ours reports whether t (or what it points at) is a struct type a package this program reads
// declares, whose every field write it reads.
func (a *analysis) ours(t types.Type) bool {
	if t == nil {
		return false
	}
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	_, isStruct := named.Underlying().(*types.Struct)
	return isStruct && a.read[packagePath(named.Obj())]
}

// texty reports whether a value of type t can hand text to a function this program does not
// read: a string, bytes, an error, an empty interface, or a value of a type that function's own
// packages declare (a *url.URL, a pgx config); not a context, and not a struct this program reads.
func (a *analysis) texty(t types.Type) bool {
	if t == nil {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Info()&types.IsString != 0
	case *types.Slice:
		return a.texty(u.Elem())
	case *types.Array:
		return a.texty(u.Elem())
	case *types.Pointer:
		return a.texty(u.Elem())
	case *types.Map:
		return a.texty(u.Key()) || a.texty(u.Elem())
	case *types.Interface:
		if named, ok := t.(*types.Named); ok && packagePath(named.Obj()) == "context" {
			return false
		}
		return true
	case *types.Struct:
		return !a.ours(t)
	}
	return false
}

// environment is the environment as a whole, read at n.
func (a *analysis) environment(n ast.Node, what string) set {
	file, line := a.position(n)
	where := fmt.Sprintf("%s:%d", file, line)
	return set{a.intern("e "+where+" "+what, func() *label {
		return &label{kind: "source", keyed: true, what: what + " (" + where + ")"}
	}): true}
}

func (a *analysis) evaluateCall(call *ast.CallExpr) set {
	if tv, ok := a.info.Types[call.Fun]; ok && tv.IsType() {
		if len(call.Args) == 1 {
			return a.ev(call.Args[0])
		}
		return nil
	}
	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if b, ok := a.info.Uses[id].(*types.Builtin); ok {
			if b.Name() == "append" || b.Name() == "min" || b.Name() == "max" {
				var all []set
				for _, arg := range call.Args {
					all = append(all, a.ev(arg))
				}
				return union(all...)
			}
			return nil
		}
	}
	fn := a.calleeOf(call)
	if fn != nil {
		if sanitizer(fn) {
			return nil
		}
		name := packagePath(fn) + "." + fn.Name()
		switch name {
		case "os.Getenv", "os.LookupEnv", "syscall.Getenv", "os.Environ":
			return a.named(call, a.environment(call, "the environment"))
		case "os.ReadFile", "os.Open", "os.OpenFile":
			return a.source(call, "the text of a file ("+a.exprText(call)+")")
		}
		if packagePath(fn) == "flag" {
			return a.source(call, "a flag ("+a.exprText(call)+")")
		}
		if a.bodies[fn] != nil {
			return a.named(call, a.carried[fn])
		}
	} else {
		var through []set
		for id := range a.ev(call.Fun) {
			switch l := a.labels[id]; l.kind {
			case "function":
				through = append(through, a.carried[l.fn])
			case "environment function":
				through = append(through, a.environment(call, "the environment, through "+a.exprText(call.Fun)))
			}
		}
		if len(through) > 0 {
			return a.named(call, union(through...))
		}
	}
	// A function this program does not read, of a package whose answers and errors hold the text
	// they are handed (keepsText), hands on the text of each argument and receiver that can hold
	// text (texty). Any function hands on what each function it is handed answers, and, handed one,
	// reads the setting a constant names.
	keeps := fn != nil && keepsText[packagePath(fn)]
	var all []set
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok && keeps && a.info.Selections[sel] != nil && a.texty(a.info.TypeOf(sel.X)) {
		all = append(all, a.ev(sel.X))
	}
	handed := false
	for _, arg := range call.Args {
		value := a.ev(arg)
		if keeps && a.texty(a.info.TypeOf(arg)) {
			all = append(all, value)
		}
		for id := range value {
			switch l := a.labels[id]; l.kind {
			case "function":
				all = append(all, a.carried[l.fn])
				handed = true
			case "environment function":
				all = append(all, a.environment(arg, "the environment, through "+a.exprText(arg)))
				handed = true
			}
		}
	}
	value := union(all...)
	if handed {
		value = a.named(call, value)
	}
	if fn != nil && renders[packagePath(fn)+"."+fn.Name()] {
		// The text it makes holds each argument as written into it.
		var written []set
		for _, arg := range call.Args {
			if a.texty(a.info.TypeOf(arg)) {
				written = append(written, a.write(arg, packagePath(fn)+"."+fn.Name(), a.ev(arg)))
			}
		}
		value = union(written...)
	}
	return value
}

// keepsText are the packages whose functions answer with the text they are handed, or fail with
// an error that quotes it: url.Parse's, strconv's and time.ParseDuration's errors hold their input,
// and pgx's ParseConfig names the connection string. A function of any other package this program
// does not read (the module's other packages, NATS, the AWS SDK) is not followed.
var keepsText = map[string]bool{
	"strings": true, "strconv": true, "fmt": true, "errors": true, "bytes": true, "path": true,
	"path/filepath": true, "net": true, "net/url": true, "net/netip": true, "time": true,
	"encoding/json": true, "encoding/base64": true, "encoding/hex": true, "slices": true, "maps": true,
	"unicode/utf8": true, "os": true, "io": true, "bufio": true,
	"github.com/jackc/pgx/v5": true, "github.com/jackc/pgx/v5/pgconn": true,
}

// urlTakers are the functions that read the value they are handed as a URL, or hand it to
// another program.
var urlTakers = map[string]bool{
	"net/url.Parse": true, "net/url.ParseRequestURI": true,
	"net/http.NewRequest": true, "net/http.NewRequestWithContext": true, "net/http.Get": true,
	"net/http.Post": true, "net/http.Head": true, "net/http.PostForm": true,
	"github.com/jackc/pgx/v5.ParseConfig": true, "github.com/jackc/pgx/v5.Connect": true,
	"github.com/jackc/pgx/v5/pgconn.ParseConfig": true, "github.com/jackc/pgx/v5/pgxpool.New": true,
	"github.com/jackc/pgx/v5/pgxpool.ParseConfig": true, "github.com/nats-io/nats.go.Connect": true,
	"os/exec.Command": true, "os/exec.CommandContext": true,
}

type span [2]token.Pos

func (s span) holds(at token.Pos) bool { return s[0] <= at && at < s[1] }

// innermost is the narrowest of spans that holds at, or the zero span.
func innermost(spans []span, at token.Pos) span {
	var best span
	for _, s := range spans {
		if s.holds(at) && (best == span{} || s[1]-s[0] < best[1]-best[0]) {
			best = s
		}
	}
	return best
}

// local reports whether v is a variable of a function (a parameter or a name it declares).
func local(v *types.Var) bool {
	return v.Pkg() != nil && v.Parent() != nil && v.Parent() != v.Pkg().Scope() && !v.IsField()
}

// reaching is what the variable v holds where it is read at at: what each assignment of it that
// can reach that point put there, from the latest assignment before it back to one that stands
// in a block holding at, which every path to at passes; an assignment in a block that does not
// hold at (a branch) is one path, so the search reads on past it. A read inside a function literal
// that v was not declared in can run after any assignment, so it reads them all.
func (a *analysis) reaching(v *types.Var, at token.Pos) set {
	sites := a.sites[v]
	if len(sites) == 0 {
		return a.carried[types.Object(v)]
	}
	all := []set{a.carried[types.Object(v)]}
	if lit := innermost(a.lits, at); lit != (span{}) && !lit.holds(v.Pos()) {
		for _, s := range sites {
			all = append(all, s.labels)
		}
		return union(all...)
	}
	var before []token.Pos
	for pos := range sites {
		if pos < at {
			before = append(before, pos)
		}
	}
	sort.Slice(before, func(i, j int) bool { return before[i] > before[j] })
	for _, pos := range before {
		s := sites[pos]
		all = append(all, s.labels)
		if s.block.holds(at) {
			return union(all[1:]...)
		}
	}
	return union(all...)
}

// assign puts value into the place target names: a variable (for a variable of a function, at
// this assignment), a field (and the value that holds it), an element of a map or a slice, or
// what a pointer points at.
func (a *analysis) assign(target ast.Expr, value set) {
	switch t := ast.Unparen(target).(type) {
	case *ast.Ident:
		if v, ok := a.object(t).(*types.Var); ok {
			if !local(v) {
				a.grow(types.Object(v), value)
				break
			}
			if a.sites[v] == nil {
				a.sites[v] = map[token.Pos]*site{}
			}
			s := a.sites[v][t.Pos()]
			if s == nil {
				s = &site{block: innermost(a.blocks, t.Pos()), labels: set{}}
				a.sites[v][t.Pos()] = s
			}
			for id := range value {
				if !s.labels[id] {
					s.labels[id] = true
					a.changed = true
				}
			}
		}
	case *ast.SelectorExpr:
		if sel := a.info.Selections[t]; sel != nil && sel.Kind() == types.FieldVal {
			a.grow(origin(sel.Obj()), value)
			a.assign(t.X, value)
		} else if v, ok := a.object(t.Sel).(*types.Var); ok {
			a.grow(types.Object(v), value)
		}
	case *ast.IndexExpr:
		a.assign(t.X, value)
	case *ast.StarExpr:
		a.assign(t.X, value)
	case *ast.UnaryExpr:
		if t.Op == token.AND {
			a.assign(t.X, value)
		}
	}
}

// bindCall puts each argument into its parameter of the function the call runs, and the receiver
// into the method's receiver.
func (a *analysis) bindCall(call *ast.CallExpr, sig *types.Signature, receiver ast.Expr) {
	if sig == nil {
		return
	}
	if receiver != nil && sig.Recv() != nil {
		a.grow(types.Object(sig.Recv()), a.ev(receiver))
	}
	params := sig.Params()
	for i, arg := range call.Args {
		if params.Len() == 0 {
			break
		}
		index := i
		if index >= params.Len() {
			index = params.Len() - 1
		}
		a.grow(types.Object(params.At(index)), a.ev(arg))
	}
}

func (a *analysis) sinkOf(call *ast.CallExpr) (string, []ast.Expr) {
	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if b, ok := a.info.Uses[id].(*types.Builtin); ok && b.Name() == "panic" {
			return "panic", call.Args
		}
	}
	fn := a.calleeOf(call)
	if fn == nil || a.bodies[fn] != nil {
		return "", nil
	}
	path, name := packagePath(fn), fn.Name()
	switch {
	case path == "fmt" && name == "Errorf", path == "errors" && name == "New":
		return "the message of " + path + "." + name, call.Args
	case path == "fmt" && (strings.HasPrefix(name, "Print") || strings.HasPrefix(name, "Fprint")):
		if strings.HasPrefix(name, "Fprint") && len(call.Args) > 0 {
			if t := a.info.TypeOf(call.Args[0]); t != nil {
				if s := t.String(); s == "*strings.Builder" || s == "*bytes.Buffer" {
					return "", nil
				}
			}
			return "fmt." + name, call.Args[1:]
		}
		return "fmt." + name, call.Args
	case path == "log/slog", path == "log":
		switch name {
		case "Debug", "Info", "Warn", "Error", "Log", "DebugContext", "InfoContext", "WarnContext", "ErrorContext", "LogAttrs",
			"Print", "Printf", "Println", "Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln":
			return "a log call (" + path + "." + name + ")", call.Args
		}
	case path == "net/http" && name == "Error":
		return "http.Error", call.Args[1:2]
	case path == "os/exec" && (name == "Command" || name == "CommandContext"):
		return "the command line of exec." + name, call.Args
	}
	return "", nil
}

// end is a place that prints arg: a line for the setting it holds as read, and an end for each line
// that wrote one into the text it holds.
func (a *analysis) end(arg ast.Expr, kind string, site ast.Node) {
	value := a.write(arg, kind, a.ev(arg))
	file, line := a.position(site)
	for id := range value {
		if l := a.labels[id]; l.kind == "written" && len(l.sources) > 0 {
			l.ends[fmt.Sprintf("%s at %s:%d", kind, file, line)] = true
		}
	}
}

func (a *analysis) flowCall(call *ast.CallExpr) {
	fn := a.calleeOf(call)
	var receiver ast.Expr
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok && a.info.Selections[sel] != nil {
		receiver = sel.X
	}
	if fn != nil && a.bodies[fn] != nil {
		sig, _ := fn.Type().(*types.Signature)
		a.bindCall(call, sig, receiver)
	} else if fn == nil {
		for id := range a.ev(call.Fun) {
			if l := a.labels[id]; l.kind == "function" {
				var sig *types.Signature
				switch f := l.fn.(type) {
				case *types.Func:
					sig, _ = f.Type().(*types.Signature)
				case *ast.FuncLit:
					sig, _ = a.info.TypeOf(f).(*types.Signature)
				}
				a.bindCall(call, sig, nil)
			}
		}
	} else if packagePath(fn) == "flag" && strings.HasSuffix(fn.Name(), "Var") && len(call.Args) > 0 {
		a.assign(call.Args[0], a.source(call, "a flag ("+a.exprText(call)+")"))
	} else if !sanitizer(fn) && keepsText[packagePath(fn)] {
		// A function of those packages writes the text it is handed into what a pointer it is given
		// points at (json.Unmarshal), and a writer method writes it into its receiver.
		var all []set
		for _, arg := range call.Args {
			if a.texty(a.info.TypeOf(arg)) {
				all = append(all, a.ev(arg))
			}
		}
		value := union(all...)
		if receiver != nil {
			if a.texty(a.info.TypeOf(receiver)) {
				value = union(value, a.ev(receiver))
			}
			switch fn.Name() {
			case "WriteString", "Write", "WriteByte", "WriteRune", "Set", "Add", "Store", "Grow":
				a.assign(receiver, value)
			}
		}
		for _, arg := range call.Args {
			if u, ok := ast.Unparen(arg).(*ast.UnaryExpr); ok && u.Op == token.AND {
				a.assign(u.X, value)
			}
		}
	}
	// A setting can be a URL when its value reaches a function that reads it as one (the URL it is
	// handed, or the head of its text, the base a + puts a path after), redact (the code's own
	// judgement that it is one), a child's command line, or a function of this module that this
	// program does not read, which may use any text it is handed.
	takes := fn != nil && (urlTakers[packagePath(fn)+"."+fn.Name()] || sanitizer(fn))
	unread := fn != nil && strings.HasPrefix(packagePath(fn), a.module+"/") && !a.read[packagePath(fn)]
	if fn != nil && a.bodies[fn] == nil && (takes || unread) {
		for _, arg := range call.Args {
			value := a.ev(arg)
			ids := a.settingsIn(value)
			if takes {
				ids = a.leadingIn(value)
			}
			for _, id := range ids {
				a.urls[id] = true
			}
		}
	}
	if a.collect {
		if kind, args := a.sinkOf(call); kind != "" {
			for _, arg := range args {
				a.end(arg, kind, call)
			}
		}
	}
}

func (a *analysis) flowFile(file *ast.File) {
	var stack []ast.Node
	enclosing := func() any {
		for i := len(stack) - 1; i >= 0; i-- {
			switch f := stack[i].(type) {
			case *ast.FuncDecl:
				if fn, ok := a.info.Defs[f.Name].(*types.Func); ok {
					return fn
				}
				return nil
			case *ast.FuncLit:
				return f
			}
		}
		return nil
	}
	results := func(key any) *types.Tuple {
		switch f := key.(type) {
		case *types.Func:
			return f.Type().(*types.Signature).Results()
		case *ast.FuncLit:
			if sig, ok := a.info.TypeOf(f).(*types.Signature); ok {
				return sig.Results()
			}
		}
		return nil
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) == len(x.Rhs) {
				for i := range x.Lhs {
					value := a.ev(x.Rhs[i])
					if x.Tok == token.ADD_ASSIGN {
						value = union(a.ev(x.Lhs[i]), a.write(x.Rhs[i], "a string +=", value))
					}
					a.assign(x.Lhs[i], value)
				}
			} else if len(x.Rhs) == 1 {
				for _, lhs := range x.Lhs {
					a.assign(lhs, a.ev(x.Rhs[0]))
				}
			}
		case *ast.ValueSpec:
			if len(x.Names) == len(x.Values) {
				for i := range x.Names {
					a.assign(x.Names[i], a.ev(x.Values[i]))
				}
			} else if len(x.Values) == 1 {
				for _, name := range x.Names {
					a.assign(name, a.ev(x.Values[0]))
				}
			}
		case *ast.RangeStmt:
			value := a.ev(x.X)
			// The key of a map is what it is indexed by, which holds none of its values: a
			// settings map's keys are the variables' names. A slice's or a string's key is a number.
			isMap := false
			if t := a.info.TypeOf(x.X); t != nil {
				_, isMap = t.Underlying().(*types.Map)
			}
			if x.Key != nil && !isMap {
				a.assign(x.Key, value)
			}
			if x.Value != nil {
				a.assign(x.Value, value)
			}
		case *ast.ReturnStmt:
			key := enclosing()
			var all []set
			for _, r := range x.Results {
				all = append(all, a.ev(r))
			}
			if tuple := results(key); tuple != nil {
				for i := range tuple.Len() {
					if tuple.At(i).Name() != "" {
						all = append(all, a.reaching(tuple.At(i), x.Pos()))
					}
				}
			}
			a.grow(key, union(all...))
		case *ast.CompositeLit:
			t := a.info.TypeOf(x)
			if t == nil {
				break
			}
			if p, ok := t.Underlying().(*types.Pointer); ok {
				t = p.Elem()
			}
			st, ok := t.Underlying().(*types.Struct)
			if !ok {
				break
			}
			for i, el := range x.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						if field, ok := a.object(id).(*types.Var); ok {
							a.grow(types.Object(field), a.ev(kv.Value))
						}
					}
				} else if i < st.NumFields() {
					a.grow(origin(st.Field(i)), a.ev(el))
				}
			}
		case *ast.SelectorExpr:
			// A method value takes its receiver with it.
			if sel := a.info.Selections[x]; sel != nil && sel.Kind() == types.MethodVal {
				if parent, ok := stack[len(stack)-2].(*ast.CallExpr); !ok || parent.Fun != x {
					if fn, ok := origin(sel.Obj()).(*types.Func); ok && a.bodies[fn] != nil {
						if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
							a.grow(types.Object(sig.Recv()), a.ev(x.X))
						}
					}
				}
			}
		case *ast.CallExpr:
			a.flowCall(x)
		}
		return true
	})
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/checks/setting-prints.go <module directory> <file>...")
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	module := filepath.Join(root, os.Args[1])
	report := map[string]bool{}
	packageDirs := map[string]bool{}
	var firstDir string
	for _, file := range os.Args[2:] {
		abs := filepath.Join(module, file)
		rel, _ := filepath.Rel(root, abs)
		report[rel] = true
		packageDirs[filepath.Dir(abs)] = true
		if firstDir == "" {
			firstDir = filepath.Dir(abs)
		}
	}
	var dirs []string
	for dir := range packageDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	list := exec.Command("go", append([]string{"list", "-deps", "-export", "-json=ImportPath,Dir,GoFiles,Export,Module"}, dirs...)...)
	list.Dir = module
	list.Stderr = os.Stderr
	out, err := list.Output()
	if err != nil {
		fail(fmt.Errorf("go list: %w", err))
	}
	var packages []listed
	decoder := json.NewDecoder(bytes.NewReader(out))
	for decoder.More() {
		var p listed
		if err := decoder.Decode(&p); err != nil {
			fail(err)
		}
		packages = append(packages, p)
	}
	modulePath := ""
	for _, p := range packages {
		if p.Dir == firstDir && p.Module != nil {
			modulePath = p.Module.Path
		}
	}
	if modulePath == "" {
		fail(fmt.Errorf("no package of %s is in a module", firstDir))
	}
	fset := token.NewFileSet()
	exports := map[string]string{}
	for _, p := range packages {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	gc := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	a := &analysis{
		root: root, fset: fset, text: map[string][]byte{}, bodies: map[*types.Func]*ast.FuncDecl{},
		ids: map[string]int{}, carried: map[any]set{}, report: report, read: map[string]bool{},
		sites: map[*types.Var]map[token.Pos]*site{}, module: modulePath, urls: map[int]bool{},
		info: &types.Info{
			Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
			Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
		},
	}
	plantedName := filepath.Join(root, "scripts/checks/setting-prints.planted.go")
	checked := map[string]*types.Package{}
	var files []*ast.File
	var typeErrors []string
	for _, p := range packages {
		if !packageDirs[p.Dir] {
			continue
		}
		var pkgFiles []*ast.File
		for _, name := range p.GoFiles {
			path := filepath.Join(p.Dir, name)
			src, err := os.ReadFile(path)
			if err != nil {
				fail(err)
			}
			f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
			if err != nil {
				fail(err)
			}
			a.text[path] = src
			// Every file of a package it reads is reported, not only the named one: a refusal
			// written in one file of a package is printed by another.
			if rel, err := filepath.Rel(root, path); err == nil {
				report[rel] = true
			}
			pkgFiles = append(pkgFiles, f)
		}
		if p.Dir == firstDir {
			f, err := parser.ParseFile(fset, plantedName, planted, parser.ParseComments)
			if err != nil {
				fail(err)
			}
			a.text[plantedName] = []byte(planted)
			pkgFiles = append(pkgFiles, f)
			report["scripts/checks/setting-prints.planted.go"] = true
		}
		conf := types.Config{
			Importer: importerFunc(func(path string) (*types.Package, error) {
				if pkg := checked[path]; pkg != nil {
					return pkg, nil
				}
				return gc.Import(path)
			}),
			Error: func(err error) {
				if len(typeErrors) < 20 {
					typeErrors = append(typeErrors, err.Error())
				}
			},
		}
		pkg, _ := conf.Check(p.ImportPath, fset, pkgFiles, a.info)
		checked[p.ImportPath] = pkg
		a.read[p.ImportPath] = true
		files = append(files, pkgFiles...)
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				a.lits = append(a.lits, span{x.Pos(), x.End()})
			case *ast.BlockStmt:
				a.blocks = append(a.blocks, span{x.Pos(), x.End()})
			case *ast.CaseClause:
				a.blocks = append(a.blocks, span{x.Colon, x.End()})
			case *ast.CommClause:
				a.blocks = append(a.blocks, span{x.Colon, x.End()})
			}
			return true
		})
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				if fn, ok := a.info.Defs[fd.Name].(*types.Func); ok {
					a.bodies[fn] = fd
				}
			}
		}
	}
	rounds := 0
	for {
		a.changed = false
		a.memo = map[ast.Expr]set{}
		for _, f := range files {
			a.flowFile(f)
		}
		rounds++
		if !a.changed || rounds >= 100 {
			break
		}
	}
	if a.changed {
		fail(fmt.Errorf("no fixed point in %d rounds", rounds))
	}
	a.collect = true
	a.memo = map[ast.Expr]set{}
	sinks := 0
	for _, f := range files {
		a.flowFile(f)
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && a.report[a.relFile(call)] {
				if kind, _ := a.sinkOf(call); kind != "" {
					sinks++
				}
			}
			return true
		})
	}
	byLine := map[string]*finding{}
	urlish := map[string]bool{}
	for _, l := range a.labels {
		if l.kind != "written" || len(l.ends) == 0 || !report[l.file] {
			continue
		}
		key := fmt.Sprintf("%s:%d", l.file, l.line)
		f := byLine[key]
		if f == nil {
			lines := strings.Split(string(a.text[filepath.Join(root, l.file)]), "\n")
			text := ""
			if l.line-1 < len(lines) {
				text = strings.TrimSpace(lines[l.line-1])
			}
			f = &finding{File: l.file, Line: l.line, Text: text}
			byLine[key] = f
		}
		f.Hows = appendNew(f.Hows, l.how)
		f.Expressions = appendNew(f.Expressions, l.expr)
		for s := range l.sources {
			what := a.labels[s].what
			if a.urls[s] {
				urlish[key] = true
			} else {
				what += ", which reaches no URL"
			}
			f.Sources = appendNew(f.Sources, what)
		}
		for e := range l.ends {
			f.Ends = appendNew(f.Ends, e)
		}
	}
	var keys []string
	for key := range byLine {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := byLine[keys[i]], byLine[keys[j]]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	encoder := json.NewEncoder(os.Stdout)
	plantedFound := map[int]bool{}
	reported := 0
	quiet := 0
	for _, key := range keys {
		f := byLine[key]
		sort.Strings(f.Sources)
		sort.Strings(f.Ends)
		switch {
		case !urlish[key]:
			if f.File != "scripts/checks/setting-prints.planted.go" {
				quiet++
				_ = encoder.Encode(map[string]any{"quiet": f})
			}
		case f.File == "scripts/checks/setting-prints.planted.go":
			plantedFound[f.Line] = true
		default:
			reported++
			_ = encoder.Encode(map[string]any{"finding": f})
		}
	}
	var blind []string
	for i, line := range strings.Split(planted, "\n") {
		n := i + 1
		switch {
		case strings.HasSuffix(line, "// planted: found") && !plantedFound[n]:
			blind = append(blind, fmt.Sprintf("the planted line scripts/checks/setting-prints.planted.go:%d was not found", n))
		case strings.HasSuffix(line, "// planted: quiet") && plantedFound[n]:
			blind = append(blind, fmt.Sprintf("the planted line scripts/checks/setting-prints.planted.go:%d was found", n))
		case !strings.HasSuffix(line, "// planted: found") && !strings.HasSuffix(line, "// planted: quiet") && plantedFound[n]:
			blind = append(blind, fmt.Sprintf("the planted line scripts/checks/setting-prints.planted.go:%d, which is not marked, was found", n))
		}
	}
	if len(typeErrors) > 0 {
		blind = append(blind, "the type check of the module's packages failed: "+strings.Join(typeErrors, "; "))
	}
	if len(blind) > 0 {
		_ = encoder.Encode(map[string]any{"blind": blind})
	}
	var read []string
	for path := range a.read {
		read = append(read, path)
	}
	sort.Strings(read)
	_ = encoder.Encode(map[string]any{"summary": fmt.Sprintf("%d files of %s read (the planted one among them), the rest of the module from its export data; %d lines print a setting that can be a URL, and %d more one that reaches no URL, of %d calls that print; %d rounds",
		len(files), strings.Join(read, " and "), reported, quiet, sinks, rounds)})
}

func (a *analysis) relFile(n ast.Node) string {
	file, _ := a.position(n)
	return file
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func appendNew(list []string, item string) []string {
	for _, each := range list {
		if each == item {
			return list
		}
	}
	return append(list, item)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "setting-prints:", err)
	os.Exit(1)
}
