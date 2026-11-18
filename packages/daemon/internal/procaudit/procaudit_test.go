package procaudit

// The audit: every test unit of a Go module that can list the process table of the machine's own
// /proc — directly, or through a function, a package variable or a struct field whose value is
// "/proc" unless something else is set — and, for each, whether it is given a /proc of its own or
// a `host-proc:` comment on its way says why it needs the machine's.
//
// A listing is os.ReadDir, ioutil.ReadDir, filepath.Walk or filepath.WalkDir of a root, an os.Open
// of a root whose file is listed (ReadDir, Readdir, Readdirnames), or filepath.Glob of a pattern
// under /proc. A read of one process's entry (/proc/<pid>/...) is no listing: it names its process,
// so no other process changes what it reads.
//
// The root is traced back through SSA, flow-insensitively, to the string "/proc": through
// parameters (every call site the call graph knows), closures' free variables, package variables
// and struct fields (every store to them in the module), phis, variadic arrays, cmp.Or and the
// results of module functions. Each way from "/proc" to a listing is a host path, with the
// variables, fields and parameters it went through (its carriers). A "/proc" that is cmp.Or's
// fallback carries the values it falls back from as carriers too, since setting one of them
// overrides it.
//
// A test unit is a top-level test or a closure passed to (*testing.T).Run. A unit reaches a host
// path when it reaches the listing, the function that holds the "/proc" (a package initializer is
// reached by every unit) and every function that reads one of the path's variables or fields.
// Reach is a call graph over the module: static calls, every function a function refers to (a
// closure it makes or a function value it passes, which it is taken to call), interface calls to
// every module method that implements them, and calls through a package variable or a struct field
// to every function stored there. A test that reaches a listing only through one of its subtests
// is not listed for it; the subtest is. A function whose doc or body holds
// `host-proc gate NAME: <why>` passes reach on only for units that set the package variable NAME.
//
// A unit is OWN ROOT for a host path when it, a _test.go function it reaches, the functions it is
// started from (a subtest's t.Run caller and what that is written in) or its package's TestMain
// stores into one of the path's variables or fields a value no "/proc" reaches; a sibling
// subtest's store does not count. It is ANNOTATED when a comment line `host-proc: <why>` is on or
// directly above the listing or the "/proc", or in the doc or body of a test-file function on its
// way to the listing. Anything else is NOT GIVEN.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// report is what one audit read and decided. unknown is set, and nothing else is, when the audit
// read nothing it could judge: no module, a module with load errors, no test or no listing.
type report struct {
	unknown string
	rows    []row
	// units is each listed unit's worst status: NOT GIVEN over ANNOTATED over OWN ROOT.
	units map[string]string
	text  strings.Builder
}

type row struct{ status, test, line string }

// notGiven is every listed unit that is NOT GIVEN.
func (r *report) notGiven() []string {
	var out []string
	for unit, status := range r.units {
		if status == "NOT GIVEN" {
			out = append(out, unit)
		}
	}
	sort.Strings(out)
	return out
}

type hostPath struct {
	constPos token.Pos
	constFn  *ssa.Function
	carriers []string
	via      []*ssa.Function // functions that read one of the carriers
}

func (p hostPath) key() string {
	return fmt.Sprintf("%d|%s", p.constPos, strings.Join(p.carriers, "<-"))
}

// subtest is how a closure passed to (*testing.T).Run is started: by the function that calls Run
// (runner), under the name Run is given (name, "?" when it is no constant). A subtest is judged as
// a test of its own: one subtest of a test can read the machine's /proc where another gives its
// own sweep a root, and the second must not cover the first.
type subtest struct {
	runner *ssa.Function
	name   string
}

type store struct {
	val   ssa.Value
	instr ssa.Instruction
}

type site struct {
	call  ssa.CallInstruction
	fn    *ssa.Function
	what  string
	paths []hostPath
}

type audit struct {
	prog     *ssa.Program
	fset     *token.FileSet
	module   string
	funcs    []*ssa.Function
	out, in  map[*ssa.Function]map[*ssa.Function]bool
	stores   map[string][]store
	sites    map[*ssa.Function][]ssa.CallInstruction
	closures map[*ssa.Function][]*ssa.MakeClosure
	memo     map[ssa.Value][]hostPath
	busy     map[ssa.Value]bool
	// scope, while set, is one test's side: a parameter of a function in it is traced through the
	// call sites in it alone, so a helper another test hands "/proc" still gives this test's own
	// value. Its traces are memoized apart (scopeMemo) and forgotten with the scope.
	scope     map[*ssa.Function]bool
	scopeMemo map[ssa.Value][]hostPath
	cycles    int                       // values met while still being traced, so far
	sets      map[string]bool           // "test|name" -> whether the test's side sets that package variable
	notes     map[string]map[int]string // file -> line -> reason of a host-proc: line
	gateLines map[string]map[int]string // file -> line -> variable of a host-proc gate line
	gates     map[*ssa.Function]string  // function -> variable its gate names
	listings  int
	tests     map[*ssa.Function]bool
	subtests  map[*ssa.Function]*subtest // a closure passed to (*testing.T).Run -> how it is started
	testMains map[string][]*ssa.Function // import path (without _test) -> TestMain functions
	reached   map[*ssa.Function]map[*ssa.Function]bool
	sides     map[*ssa.Function]map[*ssa.Function]bool
}

// runAudit audits the module at dir, its packages and their tests built under tags, with the go
// command run in env (os.Environ when nil).
func runAudit(dir, tags string, env []string) *report {
	r := &report{units: map[string]string{}}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedTypes | packages.NeedTypesSizes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		Tests: true,
		Dir:   dir,
		Env:   env,
	}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags=" + tags}
	}
	loaded, err := packages.Load(cfg, "./...")
	if err != nil {
		r.unknown = fmt.Sprintf("load %s: %v", dir, err)
		return r
	}
	var initial []*packages.Package
	loadErrors := 0
	module := ""
	for _, p := range loaded {
		if strings.HasSuffix(p.ID, ".test") {
			continue
		}
		for _, e := range p.Errors {
			loadErrors++
			fmt.Fprintf(&r.text, "load error: %s: %v\n", p.ID, e)
		}
		if p.Module != nil && module == "" {
			module = p.Module.Path
		}
		initial = append(initial, p)
	}
	if len(initial) == 0 || module == "" {
		r.unknown = "no package of a module read under " + dir
		return r
	}
	if loadErrors > 0 {
		r.unknown = fmt.Sprintf("%d load errors under %s; the audit would be of part of the module", loadErrors, dir)
		return r
	}
	prog, _ := ssautil.Packages(initial, ssa.InstantiateGenerics)
	prog.Build()
	a := &audit{
		prog: prog, fset: prog.Fset, module: module,
		out: map[*ssa.Function]map[*ssa.Function]bool{}, in: map[*ssa.Function]map[*ssa.Function]bool{},
		stores: map[string][]store{}, sites: map[*ssa.Function][]ssa.CallInstruction{},
		closures: map[*ssa.Function][]*ssa.MakeClosure{}, memo: map[ssa.Value][]hostPath{},
		busy: map[ssa.Value]bool{}, notes: map[string]map[int]string{}, gateLines: map[string]map[int]string{},
		gates: map[*ssa.Function]string{}, tests: map[*ssa.Function]bool{}, testMains: map[string][]*ssa.Function{},
		reached: map[*ssa.Function]map[*ssa.Function]bool{}, sides: map[*ssa.Function]map[*ssa.Function]bool{},
		sets: map[string]bool{}, subtests: map[*ssa.Function]*subtest{},
	}
	for _, p := range initial {
		a.readNotes(p)
	}
	for fn := range ssautil.AllFunctions(prog) {
		if a.own(fn) {
			a.funcs = append(a.funcs, fn)
		}
	}
	// Two variants of one package hold functions of the same name; the package's ID orders them,
	// so every run walks the module in one order.
	ids := map[*types.Package]string{}
	for _, p := range initial {
		ids[p.Types] = p.ID
	}
	sort.Slice(a.funcs, func(i, j int) bool {
		if a.funcs[i].String() != a.funcs[j].String() {
			return a.funcs[i].String() < a.funcs[j].String()
		}
		return ids[a.pkgOf(a.funcs[i]).Pkg] < ids[a.pkgOf(a.funcs[j]).Pkg]
	})
	a.index()
	a.edges()
	a.findSubtests()
	for _, fn := range a.funcs {
		if a.isTest(fn) {
			a.tests[fn] = true
		}
		if fn.Parent() == nil && fn.Name() == "TestMain" && a.inTestFile(fn) {
			path := strings.TrimSuffix(a.pkgOf(fn).Pkg.Path(), "_test")
			a.testMains[path] = append(a.testMains[path], fn)
		}
		if fn.Parent() == nil {
			if name, _ := a.funcLines(fn, a.gateLines); name != "" {
				a.gates[fn] = name
			}
		}
	}

	var found []*site
	for _, fn := range a.funcs {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if s := a.listing(fn, instr); s != nil {
					found = append(found, s)
				}
			}
		}
	}
	fmt.Fprintf(&r.text, "read: %d packages (test variants included), %d module functions, %d test units, %d listings of a root, %d of them can list the machine's /proc, %d gates\n",
		len(initial), len(a.funcs), len(a.tests), a.listings, len(found), len(a.gates))
	for _, fn := range sortedFuncs(keys(a.gates)) {
		fmt.Fprintf(&r.text, "gate: %s (%s) passes reach on only for units that set %s\n", fn, a.pos(fn.Pos()), a.gates[fn])
	}
	if len(a.tests) == 0 || a.listings == 0 {
		r.unknown = fmt.Sprintf("%d test units and %d listings read under %s; nothing was audited", len(a.tests), a.listings, dir)
		return r
	}

	// A package and its test variant are two SSA packages of the same files, so one listing and
	// one host path can be found twice: each is kept once, by its position and its carriers.
	rows := map[string]row{}
	shown := map[string]bool{}
	for _, s := range found {
		if header := fmt.Sprintf("\nlisting %s in %s: %s", a.pos(s.call.Pos()), s.fn, s.what); !shown[header] {
			shown[header] = true
			fmt.Fprintln(&r.text, header)
		}
		for _, p := range s.paths {
			need := []*ssa.Function{s.fn}
			if !a.isInit(p.constFn) {
				need = append(need, p.constFn)
			}
			need = append(need, p.via...)
			reach := a.testsReaching(need[0])
			for _, fn := range need[1:] {
				other := a.testsReaching(fn)
				next := map[*ssa.Function]bool{}
				for t := range reach {
					if other[t] {
						next[t] = true
					}
				}
				reach = next
			}
			if line := fmt.Sprintf("  host path: \"/proc\" at %s in %s, through %s; %d test units reach it", a.pos(p.constPos), p.constFn, carriersText(p.carriers), len(reach)); !shown[line] {
				shown[line] = true
				fmt.Fprintln(&r.text, line)
			}
			for _, t := range sortedFuncs(reach) {
				status, why := a.judge(t, s, p)
				key := fmt.Sprintf("%s|%s|%s", t, a.pos(s.call.Pos()), p.key())
				if old, ok := rows[key]; ok && old.status != "NOT GIVEN" {
					continue
				}
				rows[key] = row{status, a.unitName(t), fmt.Sprintf("%-10s %s (%s): listing %s, \"/proc\" at %s: %s", status, a.unitName(t), a.pos(t.Pos()), a.pos(s.call.Pos()), a.pos(p.constPos), why)}
			}
		}
	}
	for _, row := range rows {
		r.rows = append(r.rows, row)
	}
	sort.Slice(r.rows, func(i, j int) bool { return r.rows[i].line < r.rows[j].line })
	fmt.Fprintln(&r.text)
	for _, row := range r.rows {
		fmt.Fprintln(&r.text, row.line)
		if worst := r.units[row.test]; row.status == "NOT GIVEN" || worst == "" || worst == "OWN ROOT" {
			r.units[row.test] = row.status
		}
	}
	units := map[string]int{}
	for _, status := range r.units {
		units[status]++
	}
	fmt.Fprintf(&r.text, "\n%d test units listed: %d OWN ROOT, %d ANNOTATED, %d NOT GIVEN (a unit's worst row); %d (unit, host path) rows\n",
		len(r.units), units["OWN ROOT"], units["ANNOTATED"], units["NOT GIVEN"], len(r.rows))
	return r
}

// judge says whether test t is given its own root for host path p of listing s, or annotated.
func (a *audit) judge(t *ssa.Function, s *site, p hostPath) (string, string) {
	carriers := map[string]bool{}
	for _, c := range p.carriers {
		if !strings.HasPrefix(c, "param:") {
			carriers[c] = true
		}
	}
	side := a.testSide(t)
	for _, fn := range sortedFuncs(side) {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				st, ok := instr.(*ssa.Store)
				if !ok {
					continue
				}
				key := a.carrierOfAddr(st.Addr)
				if !carriers[key] {
					continue
				}
				a.scope, a.scopeMemo = side, map[ssa.Value][]hostPath{}
				own := len(a.valueOrigins(st.Val, st)) == 0
				a.scope, a.scopeMemo = nil, nil
				if own {
					return "OWN ROOT", fmt.Sprintf("%s stores %s at %s", fn, shown(key), a.pos(st.Pos()))
				}
			}
		}
	}
	if why, at := a.lineNote(s.call.Pos()); why != "" {
		return "ANNOTATED", fmt.Sprintf("%s (%s)", why, at)
	}
	if why, at := a.lineNote(p.constPos); why != "" {
		return "ANNOTATED", fmt.Sprintf("%s (%s)", why, at)
	}
	toListing := a.reachersOf(s.fn)
	for _, fn := range sortedFuncs(side) {
		if fn != t && !toListing[fn] {
			continue
		}
		if why, at := a.funcLines(fn, a.notes); why != "" {
			return "ANNOTATED", fmt.Sprintf("%s (%s)", why, at)
		}
	}
	return "NOT GIVEN", "no own root through " + carriersText(p.carriers) + ", and no host-proc: line"
}

// testSide is what a test unit runs of the test files: the unit, every function of a _test.go file
// it reaches, and its package's TestMain with what that reaches of the same kind. A subtest's side
// holds the functions that start it as well (the t.Run caller and the functions around it, where
// a parent test sets up what its subtests share), and no other subtest: a sibling's own root is
// not this subtest's. A top-level test's side holds none of its subtests, which are units of their
// own.
func (a *audit) testSide(t *ssa.Function) map[*ssa.Function]bool {
	if got, ok := a.sides[t]; ok {
		return got
	}
	starts := []*ssa.Function{t}
	if sub := a.subtests[t]; sub != nil {
		// The t.Run caller and every function it is written inside, up to the top-level one.
		for fn := sub.runner; fn != nil; fn = fn.Parent() {
			starts = append(starts, fn)
		}
	}
	seen := map[*ssa.Function]bool{}
	var queue []*ssa.Function
	for _, fn := range starts {
		if !seen[fn] {
			seen[fn] = true
			queue = append(queue, fn)
		}
	}
	for _, m := range a.testMains[strings.TrimSuffix(a.pkgOf(t).Pkg.Path(), "_test")] {
		if !seen[m] {
			seen[m] = true
			queue = append(queue, m)
		}
	}
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		for g := range a.out[fn] {
			if a.subtests[g] != nil {
				continue // another subtest: a unit of its own
			}
			if !seen[g] && a.inTestFile(g) {
				seen[g] = true
				queue = append(queue, g)
			}
		}
	}
	a.sides[t] = seen
	return seen
}

// setsGlobal says whether test t's side stores into the package variable named name.
func (a *audit) setsGlobal(t *ssa.Function, name string) bool {
	key := t.String() + "|" + name
	if got, ok := a.sets[key]; ok {
		return got
	}
	got := false
	for fn := range a.testSide(t) {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if st, ok := instr.(*ssa.Store); ok {
					if g, ok := st.Addr.(*ssa.Global); ok && g.Name() == name {
						got = true
					}
				}
			}
		}
	}
	a.sets[key] = got
	return got
}

// reachersOf is every function from which target is reached, gates ignored.
func (a *audit) reachersOf(target *ssa.Function) map[*ssa.Function]bool {
	seen := map[*ssa.Function]bool{target: true}
	queue := []*ssa.Function{target}
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		for g := range a.in[fn] {
			if !seen[g] {
				seen[g] = true
				queue = append(queue, g)
			}
		}
	}
	return seen
}

// testsReaching is every test that reaches target, through each gate only when it sets the gate's
// variable. Only a whole answer is memoized: one computed inside a gate's own crossing leaves
// that gate out.
func (a *audit) testsReaching(target *ssa.Function) map[*ssa.Function]bool {
	if got, ok := a.reached[target]; ok {
		return got
	}
	got := a.reach([]*ssa.Function{target}, map[*ssa.Function]bool{})
	a.reached[target] = got
	return got
}

func (a *audit) reach(starts []*ssa.Function, crossing map[*ssa.Function]bool) map[*ssa.Function]bool {
	got := map[*ssa.Function]bool{}
	seen := map[*ssa.Function]bool{}
	var queue []*ssa.Function
	for _, fn := range starts {
		if !seen[fn] {
			seen[fn] = true
			queue = append(queue, fn)
		}
	}
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if a.tests[fn] {
			got[fn] = true
		}
		if a.subtests[fn] != nil {
			// A subtest is a unit of its own: the test that starts it reaches the listing only
			// through it, and is not counted for that.
			continue
		}
		if name := a.gates[fn]; name != "" {
			if !crossing[fn] {
				crossing[fn] = true
				for t := range a.reach(sortedFuncs(a.in[fn]), crossing) {
					if a.setsGlobal(t, name) {
						got[t] = true
					}
				}
				delete(crossing, fn)
			}
			continue
		}
		for _, g := range sortedFuncs(a.in[fn]) {
			if !seen[g] {
				seen[g] = true
				queue = append(queue, g)
			}
		}
	}
	return got
}

// listing answers instr as a listing whose root can be "/proc", or nil.
func (a *audit) listing(fn *ssa.Function, instr ssa.Instruction) *site {
	call, ok := instr.(ssa.CallInstruction)
	if !ok {
		return nil
	}
	callee := call.Common().StaticCallee()
	if callee == nil {
		return nil
	}
	name := qualified(callee)
	args := call.Common().Args
	var root ssa.Value
	switch name {
	case "os.ReadDir", "io/ioutil.ReadDir", "path/filepath.Walk", "path/filepath.WalkDir":
		root = args[0]
	case "os.Open", "os.OpenFile":
		if !a.listed(call) {
			return nil
		}
		root = args[0]
	case "path/filepath.Glob":
		a.listings++
		if c, ok := args[0].(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
			pattern := constant.StringVal(c.Value)
			rest, under := strings.CutPrefix(pattern, "/proc/")
			if under && !strings.HasPrefix(rest, "self/") && strings.ContainsAny(strings.SplitN(rest, "/", 2)[0], "*?[") {
				return &site{call: call, fn: fn, what: name + "(" + c.Value.String() + ")", paths: []hostPath{{constPos: call.Pos(), constFn: fn}}}
			}
		}
		return nil
	default:
		return nil
	}
	a.listings++
	paths := dedupe(a.valueOrigins(root, call))
	if len(paths) == 0 {
		return nil
	}
	return &site{call: call, fn: fn, what: name, paths: paths}
}

func dedupe(paths []hostPath) []hostPath {
	seen := map[string]bool{}
	var out []hostPath
	for _, p := range paths {
		if !seen[p.key()] {
			seen[p.key()] = true
			out = append(out, p)
		}
	}
	return out
}

// listed says whether the file an os.Open call answers is listed by one of its methods.
func (a *audit) listed(call ssa.CallInstruction) bool {
	v, ok := call.(*ssa.Call)
	if !ok || v.Referrers() == nil {
		return false
	}
	for _, r := range *v.Referrers() {
		ex, ok := r.(*ssa.Extract)
		if !ok || ex.Index != 0 || ex.Referrers() == nil {
			continue
		}
		for _, u := range *ex.Referrers() {
			c, ok := u.(ssa.CallInstruction)
			if !ok {
				continue
			}
			if g := c.Common().StaticCallee(); g != nil {
				switch g.Name() {
				case "ReadDir", "Readdir", "Readdirnames":
					if len(c.Common().Args) > 0 && c.Common().Args[0] == ex {
						return true
					}
				}
			}
		}
	}
	return false
}

// valueOrigins is every host path from the string "/proc" to v, read at user.
func (a *audit) valueOrigins(v ssa.Value, user ssa.Instruction) []hostPath {
	if c, ok := v.(*ssa.Const); ok {
		if c.Value != nil && c.Value.Kind() == constant.String {
			s := constant.StringVal(c.Value)
			if s == "/proc" || s == "/proc/" {
				return []hostPath{{constPos: positioned(user), constFn: user.Parent()}}
			}
		}
		return nil
	}
	memo := a.memo
	if a.scope != nil {
		memo = a.scopeMemo
	}
	if got, ok := memo[v]; ok {
		return got
	}
	if a.busy[v] {
		a.cycles++
		return nil
	}
	a.busy[v] = true
	before := a.cycles
	got := a.origins(v)
	delete(a.busy, v)
	// A trace that met a value still being traced is that value's part answer: it is kept only
	// where no such value was met, so a later trace of it starts whole.
	if a.cycles == before {
		memo[v] = got
	}
	return got
}

// positioned is user's position, or that of the first instruction after it in its block that has
// one: a store into a variadic call's array has none, and the call that follows it does.
func positioned(user ssa.Instruction) token.Pos {
	if user.Pos().IsValid() || user.Block() == nil {
		return user.Pos()
	}
	after := false
	for _, instr := range user.Block().Instrs {
		if instr == user {
			after = true
			continue
		}
		if after && instr.Pos().IsValid() {
			return instr.Pos()
		}
	}
	return user.Pos()
}

func (a *audit) origins(v ssa.Value) []hostPath {
	var out []hostPath
	through := func(carrier string, reader *ssa.Function, paths []hostPath) {
		for _, p := range paths {
			via := p.via
			if reader != nil {
				via = append([]*ssa.Function{reader}, via...)
			}
			out = append(out, hostPath{constPos: p.constPos, constFn: p.constFn, carriers: append([]string{carrier}, p.carriers...), via: via})
		}
	}
	switch x := v.(type) {
	case *ssa.Parameter:
		fn := x.Parent()
		idx := -1
		for i, p := range fn.Params {
			if p == x {
				idx = i
			}
		}
		carrier := fmt.Sprintf("param:%s#%s", fn, x.Name())
		for _, call := range a.sites[fn] {
			if a.scope != nil && a.scope[fn] && !a.scope[call.Parent()] {
				continue
			}
			common := call.Common()
			var arg ssa.Value
			switch {
			case common.IsInvoke() && idx == 0:
				arg = common.Value
			case common.IsInvoke():
				if idx-1 < len(common.Args) {
					arg = common.Args[idx-1]
				}
			case idx < len(common.Args):
				arg = common.Args[idx]
			}
			if arg != nil {
				through(carrier, nil, a.valueOrigins(arg, call))
			}
		}
	case *ssa.FreeVar:
		fn := x.Parent()
		idx := -1
		for i, f := range fn.FreeVars {
			if f == x {
				idx = i
			}
		}
		for _, mc := range a.closures[fn] {
			if idx >= 0 && idx < len(mc.Bindings) {
				out = append(out, a.valueOrigins(mc.Bindings[idx], mc)...)
			}
		}
	case *ssa.UnOp:
		if x.Op != token.MUL {
			return nil
		}
		key := a.carrierOfAddr(x.X)
		if key == "" {
			return nil
		}
		for _, st := range a.stores[key] {
			if strings.HasPrefix(key, "local:") {
				out = append(out, a.valueOrigins(st.val, st.instr)...)
			} else {
				through(key, x.Parent(), a.valueOrigins(st.val, st.instr))
			}
		}
	case *ssa.Slice:
		// A variadic call's arguments: an array the caller allocated and stored each into.
		if alloc, ok := x.X.(*ssa.Alloc); ok {
			for _, st := range a.stores[fmt.Sprintf("local:%p", alloc)] {
				out = append(out, a.valueOrigins(st.val, st.instr)...)
			}
		}
	case *ssa.Field:
		key := fieldKey(x.X.Type(), x.Field)
		for _, st := range a.stores[key] {
			through(key, x.Parent(), a.valueOrigins(st.val, st.instr))
		}
	case *ssa.Phi:
		for i, e := range x.Edges {
			out = append(out, a.valueOrigins(e, a.edgeUser(x, i))...)
		}
	case *ssa.Call:
		callee := x.Call.StaticCallee()
		if callee == nil {
			return nil
		}
		if qualified(callee) == "cmp.Or" {
			return a.fallback(x)
		}
		out = append(out, a.results(callee, 0)...)
	case *ssa.Extract:
		if call, ok := x.Tuple.(*ssa.Call); ok {
			if callee := call.Call.StaticCallee(); callee != nil {
				out = append(out, a.results(callee, x.Index)...)
			}
		}
	case *ssa.MakeInterface:
		return a.valueOrigins(x.X, x)
	case *ssa.ChangeType:
		return a.valueOrigins(x.X, x)
	case *ssa.Convert:
		return a.valueOrigins(x.X, x)
	case *ssa.ChangeInterface:
		return a.valueOrigins(x.X, x)
	case *ssa.TypeAssert:
		return a.valueOrigins(x.X, x)
	}
	return out
}

// fallback is every host path through cmp.Or(values...): each value's own, and a "/proc" among
// the values with the variables and fields the values before it read as its carriers, since
// setting one of them to a non-empty value overrides it.
func (a *audit) fallback(call *ssa.Call) []hostPath {
	var values []ssa.Value
	for _, arg := range call.Call.Args {
		if s, ok := arg.(*ssa.Slice); ok {
			if alloc, ok := s.X.(*ssa.Alloc); ok {
				for _, st := range a.stores[fmt.Sprintf("local:%p", alloc)] {
					values = append(values, st.val)
				}
				continue
			}
		}
		values = append(values, arg)
	}
	var out []hostPath
	var before []string
	var readers []*ssa.Function
	for _, v := range values {
		for _, p := range a.valueOrigins(v, call) {
			if _, isConst := v.(*ssa.Const); isConst {
				p.carriers = append(append([]string{}, before...), p.carriers...)
				p.via = append(append([]*ssa.Function{}, readers...), p.via...)
			}
			out = append(out, p)
		}
		switch x := v.(type) {
		case *ssa.UnOp:
			if key := a.carrierOfAddr(x.X); key != "" && !strings.HasPrefix(key, "local:") {
				before = append(before, key)
				readers = append(readers, x.Parent())
			}
		case *ssa.Field:
			if key := fieldKey(x.X.Type(), x.Field); key != "" {
				before = append(before, key)
				readers = append(readers, x.Parent())
			}
		case *ssa.Parameter:
			before = append(before, fmt.Sprintf("param:%s#%s", x.Parent(), x.Name()))
		}
	}
	return out
}

// results is every host path to callee's result i, for a module function.
func (a *audit) results(callee *ssa.Function, i int) []hostPath {
	if !a.own(callee) {
		return nil
	}
	var out []hostPath
	for _, b := range callee.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		if ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok && i < len(ret.Results) {
			out = append(out, a.valueOrigins(ret.Results[i], ret)...)
		}
	}
	return out
}

// edgeUser is an instruction standing for phi's edge i: the last of its predecessor's that has a
// position, so a "/proc" that comes in on that edge is named where it was written.
func (a *audit) edgeUser(phi *ssa.Phi, i int) ssa.Instruction {
	pred := phi.Block().Preds[i]
	for j := len(pred.Instrs) - 1; j >= 0; j-- {
		if pred.Instrs[j].Pos().IsValid() {
			return pred.Instrs[j]
		}
	}
	return phi
}

// index records every store and every closure made.
func (a *audit) index() {
	for _, fn := range a.funcs {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch x := instr.(type) {
				case *ssa.Store:
					if key := a.carrierOfAddr(x.Addr); key != "" {
						a.stores[key] = append(a.stores[key], store{val: x.Val, instr: x})
					}
				case *ssa.MakeClosure:
					if g, ok := x.Fn.(*ssa.Function); ok {
						a.closures[g] = append(a.closures[g], x)
					}
				}
			}
		}
	}
}

// edges builds the call graph and every module function's call sites: static calls, function
// references, interface calls to every module method that implements them, and calls through
// package variables and struct fields to every function stored there.
func (a *audit) edges() {
	implementers := map[string][]*ssa.Function{}
	var named []types.Type
	for _, fn := range a.funcs {
		if recv := fn.Signature.Recv(); recv != nil && fn.Parent() == nil {
			named = append(named, recv.Type())
		}
	}
	add := func(f, g *ssa.Function) {
		if g == nil || !a.own(g) {
			return
		}
		if a.out[f] == nil {
			a.out[f] = map[*ssa.Function]bool{}
		}
		if a.in[g] == nil {
			a.in[g] = map[*ssa.Function]bool{}
		}
		a.out[f][g] = true
		a.in[g][f] = true
	}
	invoke := func(common *ssa.CallCommon) []*ssa.Function {
		iface := common.Value.Type()
		key := types.TypeString(iface, nil) + "." + common.Method.Name()
		if got, ok := implementers[key]; ok {
			return got
		}
		it, ok := iface.Underlying().(*types.Interface)
		var got []*ssa.Function
		if ok {
			seen := map[*ssa.Function]bool{}
			for _, t := range named {
				if !types.Implements(t, it) {
					continue
				}
				sel := a.prog.MethodSets.MethodSet(t).Lookup(common.Method.Pkg(), common.Method.Name())
				if sel == nil {
					continue
				}
				if g := a.prog.MethodValue(sel); g != nil && !seen[g] {
					seen[g] = true
					got = append(got, g)
				}
			}
		}
		implementers[key] = got
		return got
	}
	stored := func(v ssa.Value) []*ssa.Function {
		var key string
		switch x := v.(type) {
		case *ssa.UnOp:
			if x.Op == token.MUL {
				key = a.carrierOfAddr(x.X)
			}
		case *ssa.Field:
			key = fieldKey(x.X.Type(), x.Field)
		}
		if key == "" || strings.HasPrefix(key, "local:") {
			return nil
		}
		var got []*ssa.Function
		for _, st := range a.stores[key] {
			if g := funcOf(st.val); g != nil {
				got = append(got, g)
			}
		}
		return got
	}
	for _, f := range a.funcs {
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				if call, ok := instr.(ssa.CallInstruction); ok {
					common := call.Common()
					var callees []*ssa.Function
					switch {
					case common.StaticCallee() != nil:
						callees = []*ssa.Function{common.StaticCallee()}
					case common.IsInvoke():
						callees = invoke(common)
					default:
						callees = stored(common.Value)
					}
					for _, g := range callees {
						add(f, g)
						if a.own(g) {
							a.sites[g] = append(a.sites[g], call)
						}
					}
				}
				for _, op := range instr.Operands(nil) {
					if op == nil || *op == nil {
						continue
					}
					if g, ok := (*op).(*ssa.Function); ok {
						add(f, g)
					}
				}
			}
		}
	}
}

func funcOf(v ssa.Value) *ssa.Function {
	for {
		switch x := v.(type) {
		case *ssa.Function:
			return x
		case *ssa.MakeClosure:
			g, _ := x.Fn.(*ssa.Function)
			return g
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeType:
			v = x.X
		default:
			return nil
		}
	}
}

// carrierOfAddr is the key of what addr stores into: a package variable, a struct field or a
// local. A package and its test variant are two packages of the same source, so a key carries the
// object it names (@%p) besides its text: a trace stays inside the variant it started in.
func (a *audit) carrierOfAddr(addr ssa.Value) string {
	switch x := addr.(type) {
	case *ssa.Global:
		return fmt.Sprintf("global:%s@%p", x.String(), x)
	case *ssa.FieldAddr:
		return fieldKey(x.X.Type(), x.Field)
	case *ssa.Alloc:
		return fmt.Sprintf("local:%p", x)
	case *ssa.IndexAddr:
		if alloc, ok := x.X.(*ssa.Alloc); ok {
			return fmt.Sprintf("local:%p", alloc)
		}
	}
	return ""
}

func fieldKey(t types.Type, i int) string {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok || i >= st.NumFields() {
		return ""
	}
	return fmt.Sprintf("field:%s.%s@%p", types.TypeString(t, nil), st.Field(i).Name(), st.Field(i))
}

var objectSuffix = regexp.MustCompile(`@0x[0-9a-f]+`)

// shown is a key as a person reads it: without the object it names.
func shown(key string) string {
	return objectSuffix.ReplaceAllString(key, "")
}

func carriersText(cs []string) string {
	if len(cs) == 0 {
		return "nothing (the string is written at the listing's own call)"
	}
	return shown(strings.Join(cs, " <- "))
}

func qualified(fn *ssa.Function) string {
	origin := fn
	if o := fn.Origin(); o != nil {
		origin = o
	}
	if origin.Pkg == nil {
		return origin.Name()
	}
	if recv := origin.Signature.Recv(); recv != nil {
		return types.TypeString(recv.Type(), nil) + "." + origin.Name()
	}
	return origin.Pkg.Pkg.Path() + "." + origin.Name()
}

func (a *audit) pkgOf(fn *ssa.Function) *ssa.Package {
	for f := fn; f != nil; f = f.Parent() {
		if f.Pkg != nil {
			return f.Pkg
		}
		if o := f.Origin(); o != nil && o.Pkg != nil {
			return o.Pkg
		}
	}
	return nil
}

func (a *audit) own(fn *ssa.Function) bool {
	p := a.pkgOf(fn)
	if p == nil {
		return false
	}
	path := p.Pkg.Path()
	return path == a.module || strings.HasPrefix(path, a.module+"/")
}

func (a *audit) inTestFile(fn *ssa.Function) bool {
	return strings.HasSuffix(a.fset.Position(fn.Pos()).Filename, "_test.go")
}

var testName = regexp.MustCompile(`^(Test|Fuzz)([^a-z].*)?$`)

func (a *audit) isTest(fn *ssa.Function) bool {
	if fn.Parent() != nil || fn.Signature.Recv() != nil || !a.inTestFile(fn) || !testName.MatchString(fn.Name()) || fn.Name() == "TestMain" {
		return false
	}
	params := fn.Signature.Params()
	if params.Len() != 1 {
		return false
	}
	t := types.TypeString(params.At(0).Type(), nil)
	return t == "*testing.T" || t == "*testing.F"
}

func (a *audit) isInit(fn *ssa.Function) bool {
	return fn.Parent() == nil && fn.Signature.Recv() == nil && (fn.Name() == "init" || strings.HasPrefix(fn.Name(), "init#"))
}

func (a *audit) pos(p token.Pos) string {
	if !p.IsValid() {
		return "(no position)"
	}
	pos := a.fset.Position(p)
	file := pos.Filename
	if i := strings.Index(file, "/packages/daemon/"); i >= 0 {
		file = file[i+len("/packages/daemon/"):]
	}
	return fmt.Sprintf("%s:%d", file, pos.Line)
}

var (
	noteLine = regexp.MustCompile(`^//\s*host-proc:\s*(.*\S)`)
	gateLine = regexp.MustCompile(`^//\s*host-proc gate (\w+):\s*\S`)
)

func (a *audit) readNotes(p *packages.Package) {
	for _, f := range p.Syntax {
		file := p.Fset.Position(f.Pos()).Filename
		for _, group := range f.Comments {
			for _, c := range group.List {
				line := p.Fset.Position(c.Pos()).Line
				if m := noteLine.FindStringSubmatch(c.Text); m != nil {
					if a.notes[file] == nil {
						a.notes[file] = map[int]string{}
					}
					a.notes[file][line] = m[1]
				}
				if m := gateLine.FindStringSubmatch(c.Text); m != nil {
					if a.gateLines[file] == nil {
						a.gateLines[file] = map[int]string{}
					}
					a.gateLines[file][line] = m[1]
				}
			}
		}
	}
}

// lineNote is a host-proc: line on the line of p or in the comment lines directly above it.
func (a *audit) lineNote(p token.Pos) (string, string) {
	if !p.IsValid() {
		return "", ""
	}
	pos := a.fset.Position(p)
	notes := a.notes[pos.Filename]
	where := func(line int) string {
		at := a.pos(p)
		return fmt.Sprintf("%s:%d", at[:strings.LastIndex(at, ":")], line)
	}
	if why, ok := notes[pos.Line]; ok {
		return why, where(pos.Line)
	}
	src, err := os.ReadFile(pos.Filename)
	if err != nil {
		return "", ""
	}
	lines := strings.Split(string(src), "\n")
	for l := pos.Line - 1; l >= 1 && l <= len(lines); l-- {
		if !strings.HasPrefix(strings.TrimSpace(lines[l-1]), "//") {
			break
		}
		if why, ok := notes[l]; ok {
			return why, where(l)
		}
	}
	return "", ""
}

// funcLines is the first of found's lines in the doc or the body of fn's top-level declaration.
func (a *audit) funcLines(fn *ssa.Function, found map[string]map[int]string) (string, string) {
	top := fn
	for top.Parent() != nil {
		top = top.Parent()
	}
	decl, ok := top.Syntax().(*ast.FuncDecl)
	if !ok {
		return "", ""
	}
	start := decl.Pos()
	if decl.Doc != nil {
		start = decl.Doc.Pos()
	}
	from, to := a.fset.Position(start), a.fset.Position(decl.End())
	lines := found[from.Filename]
	for l := from.Line; l <= to.Line; l++ {
		if got, ok := lines[l]; ok {
			at := a.pos(start)
			return got, fmt.Sprintf("%s:%d", at[:strings.LastIndex(at, ":")], l)
		}
	}
	return "", ""
}

func keys(m map[*ssa.Function]string) map[*ssa.Function]bool {
	out := map[*ssa.Function]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedFuncs(set map[*ssa.Function]bool) []*ssa.Function {
	out := make([]*ssa.Function, 0, len(set))
	for fn := range set {
		out = append(out, fn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// findSubtests records every closure of a _test.go file passed to (*testing.T).Run as a test unit.
func (a *audit) findSubtests() {
	for _, fn := range a.funcs {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				call, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				callee := call.Common().StaticCallee()
				if callee == nil || qualified(callee) != "*testing.T.Run" || len(call.Common().Args) != 3 {
					continue
				}
				g := funcOf(call.Common().Args[2])
				if g == nil || g.Parent() == nil || !a.inTestFile(g) {
					continue
				}
				name := "?"
				if c, ok := call.Common().Args[1].(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
					name = constant.StringVal(c.Value)
				}
				a.subtests[g] = &subtest{runner: fn, name: name}
				a.tests[g] = true
			}
		}
	}
}

// unitName is a test unit as a person finds it: a top-level test by its function, a subtest by the
// test it is written in and the name it runs under.
func (a *audit) unitName(t *ssa.Function) string {
	sub := a.subtests[t]
	if sub == nil {
		return t.String()
	}
	top := sub.runner
	for top.Parent() != nil {
		top = top.Parent()
	}
	return top.String() + "/" + sub.name + " (" + t.Name() + ")"
}
