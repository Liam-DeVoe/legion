package tests3

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// scriptBound is one bound of one-collection.sh, by the text that sets it, which occurs in the
// script once, and the case of TestOneCollectionFailsWhenTheRuleDoesNotHold that fails when it is
// cut: the case's wait or request never ends, so with the bound gone the case meets runScript's
// deadline, or fails later and by another message.
type scriptBound struct{ text, caseName string }

// scriptBounds lists every bound of one-collection.sh.
var scriptBounds = []scriptBound{
	{"timeout -s KILL 60 weed shell", "a shell that hangs: prepare fails by its bound"},
	{"timeout -s KILL 10 wget", "a master that never answers: prepare fails at the end of its wait for a volume server"},
	{"deadline=$(( $(date +%s) + seconds ))", "the grow refused: prepare fails at the end of its tries"},
	{"  registered 60\n", "a master that never answers: prepare fails at the end of its wait for a volume server"},
	{"  registered 0\n", "no volume server listed: check fails by name at once"},
	{`within 10 "the master grew no volumes`, "the grow refused: prepare fails at the end of its tries"},
	{`within 60 "the filer did not put the rule`, "the rule never in force: prepare fails at the end of its wait"},
}

// goBound is one function of seaweedfs.go that sets a deadline, and the case or test that fails
// when that deadline is cut.
type goBound struct{ function, caseName string }

// goBounds lists every function of seaweedfs.go that sets a deadline. testcontainers' own wait
// strategy (startServer's WithStartupTimeout) is not one of them: it ends at a default of its own
// when its bound is cut, so no case can tell the cut apart.
var goBounds = []goBound{
	{"runScriptWithin", "a step that never ends: runScript fails it by its deadline"},
	{"request", "TestARequestThatIsNeverAnsweredEndsAtItsDeadline"},
}

// Every wait and every check of one-collection.sh, and every deadline of seaweedfs.go, has a case
// that fails when it is cut, and this is the program that says so, by reading both files:
//
//   - The script asks the store things only in shell and master, each line of them under
//     `timeout -s KILL <seconds>`; a timeout by any other signal is refused, since weed shell
//     ends 0 on TERM in the SeaweedFS image.
//   - The script waits (until, while, sleep, wait) only in within, which ends by its seconds.
//   - Every bound the script sets, `timeout ... <seconds>`, `within <seconds>` and
//     `registered <seconds>`, lies in a text of scriptBounds, whose case exists.
//   - Every message the script fails by is the want of a case of storeCases, so a check or a
//     wait with no case fails here, and no `::error::` is printed but by fail and usage.
//   - In seaweedfs.go, every function that sets a deadline is in goBounds, whose case exists, and
//     every function that sends a request (Exec, Do) sets one.
//
// The last subtest plants each kind of fault and wants this program to name it, so a rule that
// cannot see what it is for fails too.
func TestEveryWaitAndCheckOfTheScriptHasACase(t *testing.T) {
	cases := caseNames(t, storeCases)
	source, err := os.ReadFile("seaweedfs.go")
	if err != nil {
		t.Fatalf("read seaweedfs.go: %v", err)
	}
	t.Run("one-collection.sh", func(t *testing.T) {
		for _, finding := range scriptFindings(oneCollection, storeCases, scriptBounds, cases) {
			t.Error(finding)
		}
	})
	t.Run("seaweedfs.go", func(t *testing.T) {
		for _, finding := range goFindings(string(source), goBounds, cases) {
			t.Error(finding)
		}
	})
	t.Run("a planted request, wait, bound, check or deadline with no case fails it", func(t *testing.T) {
		script := func(old, replacement string) func() []string {
			return func() []string {
				return scriptFindings(plant(t, oneCollection, old, replacement), storeCases, scriptBounds, cases)
			}
		}
		gosource := func(old, replacement string) func() []string {
			return func() []string {
				return goFindings(plant(t, string(source), old, replacement), goBounds, cases)
			}
		}
		for _, c := range []struct {
			name     string
			findings func() []string
			want     string
		}{
			{"a request with no bound", script("prepare() {\n", "prepare() {\n  wget -q -O /dev/null http://127.0.0.1:9333/\n"), "asks the store outside shell and master"},
			{"a shell bounded by TERM", script("timeout -s KILL 60 weed", "timeout 60 weed"), "a timeout not by KILL"},
			{"a bound changed", script("timeout -s KILL 10 wget", "timeout -s KILL 5 wget"), "timeout -s KILL 5"},
			{"a wait outside within", script("prepare() {\n", "prepare() {\n  until listed; do sleep 1; done\n"), "waits outside within"},
			{"a bound with no case", script("\n  grow\n  configure\n", "\n  grow\n  within 30 \"the store took no rule\" \"$work/x\" true\n  configure\n"), "within 30"},
			{"a check with no case", script("\n  grow\n  configure\n", "\n  grow\n  [ -d /data ] || fail \"the store has no data directory\"\n  configure\n"), "the store has no data directory"},
			{"an error printed outside fail", script("\n  grow\n  configure\n", "\n  grow\n  echo \"::error::the store is odd\"\n  configure\n"), "prints ::error:: outside fail"},
			{"a case taken away", func() []string {
				kept := slices.DeleteFunc(slices.Clone(storeCases), func(c storeCase) bool { return strings.HasPrefix(c.name, "the grow refused") })
				return scriptFindings(oneCollection, kept, scriptBounds, caseNames(t, kept))
			}, "the master grew no volumes for $collection"},
			{"a deadline with no case", gosource("\tt.Helper()\n\tcontainer, err := testcontainers.Run(", "\tt.Helper()\n\tctx, cancel := context.WithTimeout(ctx, time.Minute)\n\tdefer cancel()\n\tcontainer, err := testcontainers.Run("), "startServer sets a deadline"},
			{"a request with no deadline", gosource("\n// call sends", "\nfunc ping(c testcontainers.Container) {\n\t_, _, _ = c.Exec(context.Background(), []string{\"true\"})\n}\n\n// call sends"), "ping sends a request with no deadline"},
			{"a deadline cut", gosource("\tctx, cancel := context.WithTimeout(ctx, deadline)\n\tdefer cancel()\n\tsent, err", "\tsent, err"), "request sends a request with no deadline"},
		} {
			findings := c.findings()
			if !slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, c.want) }) {
				t.Errorf("%s: the program found %q, want a finding saying %q", c.name, findings, c.want)
			}
		}
	})
}

// A request to a server that takes the connection and never answers ends at its deadline, by
// name, rather than holding the test to go test's -timeout: the case of request's deadline.
func TestARequestThatIsNeverAnsweredEndsAtItsDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the server that never answers: %v", err)
	}
	var held []net.Conn
	accepting := make(chan struct{})
	go func() {
		defer close(accepting)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			held = append(held, conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-accepting
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	url := "http://" + listener.Addr().String() + "/bucket"
	_, _, err = request(context.Background(), http.MethodPut, url, "", time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "PUT "+url) {
		t.Fatalf("request to a server that never answers: %v, want an error naming PUT %s and its deadline", err, url)
	}
}

// caseNames answers the names of cases and of this package's Test functions, read from its test
// files, which a bound may name as its case.
func caseNames(t *testing.T, cases []storeCase) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, c := range cases {
		names[c.name] = true
	}
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("list this package's test files: %v (%d found)", err, len(files))
	}
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				names[fn.Name.Name] = true
			}
		}
	}
	return names
}

// plant answers text with old replaced by replacement, which must occur in it once.
func plant(t *testing.T, text, old, replacement string) string {
	t.Helper()
	if n := strings.Count(text, old); n != 1 {
		t.Fatalf("the planted text's anchor occurs %d times, not once: %q", n, old)
	}
	return strings.Replace(text, old, replacement, 1)
}

var (
	scriptFunction = regexp.MustCompile(`^([a-z_]+)\(\) \{$`)
	quoted         = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'[^']*'`)
	storeRequest   = regexp.MustCompile(`\b(weed|wget|curl|nc)\b`)
	boundedRequest = regexp.MustCompile(`timeout -s KILL [0-9]+ (weed|wget|curl|nc)\b`)
	anyTimeout     = regexp.MustCompile(`\btimeout\b`)
	killTimeout    = regexp.MustCompile(`^timeout -s KILL [0-9]+ `)
	scriptWait     = regexp.MustCompile(`\b(until|while|sleep|wait)\b`)
	scriptBoundSet = regexp.MustCompile(`\btimeout(?: -s [A-Z]+)? [0-9]+|\bwithin [0-9]+|\bregistered [0-9]+`)
	failMessage    = regexp.MustCompile(`\bfail "((?:[^"\\]|\\.)*)"|\bwithin (?:[0-9]+|"[^"]*") "((?:[^"\\]|\\.)*)"`)
	onlyVariable   = regexp.MustCompile(`^\$[a-z_0-9]+$`)
	variable       = regexp.MustCompile(`\\\$\{?[a-z_]+\}?`)
)

// scriptFindings answers what one-collection.sh (script) breaks of the rules above, against
// cases, bounds and the names a bound may give as its case.
func scriptFindings(script string, cases []storeCase, bounds []scriptBound, names map[string]bool) []string {
	var findings []string
	type span struct{ start, end int }
	var listed []span
	for _, b := range bounds {
		if n := strings.Count(script, b.text); n != 1 {
			findings = append(findings, fmt.Sprintf("one-collection.sh: the bound %q occurs %d times, not once", b.text, n))
			continue
		}
		if !names[b.caseName] {
			findings = append(findings, fmt.Sprintf("one-collection.sh: the bound %q names a case that does not exist: %q", b.text, b.caseName))
		}
		start := strings.Index(script, b.text)
		listed = append(listed, span{start, start + len(b.text)})
	}
	var wants []string
	for _, c := range cases {
		wants = append(wants, c.want)
	}
	function := ""
	offset := 0
	for number, line := range strings.SplitAfter(script, "\n") {
		at := fmt.Sprintf("one-collection.sh:%d", number+1)
		lineStart := offset
		offset += len(line)
		line = strings.TrimSuffix(line, "\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := scriptFunction.FindStringSubmatch(line); m != nil {
			function = m[1]
			continue
		}
		if line == "}" {
			function = ""
			continue
		}
		code := quoted.ReplaceAllString(line, `""`)
		if storeRequest.MatchString(code) && (function != "shell" && function != "master" || !boundedRequest.MatchString(code)) {
			findings = append(findings, at+": asks the store outside shell and master, or with no `timeout -s KILL` of its own: "+trimmed)
		}
		for _, loc := range anyTimeout.FindAllStringIndex(code, -1) {
			if !killTimeout.MatchString(code[loc[0]:]) {
				findings = append(findings, at+": a timeout not by KILL, which weed shell answers with exit 0: "+trimmed)
			}
		}
		if scriptWait.MatchString(code) && function != "within" {
			findings = append(findings, at+": waits outside within, which ends by its seconds: "+trimmed)
		}
		if strings.Contains(line, "::error::") && function != "fail" && function != "usage" {
			findings = append(findings, at+": prints ::error:: outside fail, so with no case: "+trimmed)
		}
		for _, loc := range scriptBoundSet.FindAllStringIndex(line, -1) {
			position := lineStart + loc[0]
			if !slices.ContainsFunc(listed, func(s span) bool { return s.start <= position && position < s.end }) {
				findings = append(findings, fmt.Sprintf("%s: the bound %q is in no text of scriptBounds, so it has no case", at, line[loc[0]:loc[1]]))
			}
		}
		for _, m := range failMessage.FindAllStringSubmatch(line, -1) {
			message := m[1] + m[2]
			if onlyVariable.MatchString(message) {
				continue
			}
			pattern := regexp.MustCompile("^::error::" + variable.ReplaceAllString(regexp.QuoteMeta(message), ".+") + "$")
			if !slices.ContainsFunc(wants, pattern.MatchString) {
				findings = append(findings, fmt.Sprintf("%s: the message %q is the want of no case, so nothing fails when its check or wait is cut", at, message))
			}
		}
	}
	return findings
}

// goFindings answers what seaweedfs.go (source) breaks of the rules above, against bounds and the
// names a bound may give as its case.
func goFindings(source string, bounds []goBound, names map[string]bool) []string {
	parsed, err := parser.ParseFile(token.NewFileSet(), "seaweedfs.go", source, 0)
	if err != nil {
		return []string{fmt.Sprintf("seaweedfs.go: parse: %v", err)}
	}
	deadlines := map[string]bool{}
	var findings []string
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		sets, sends := false, false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := selector.Sel.Name
			if pkg, ok := selector.X.(*ast.Ident); ok && (pkg.Name == "context" && strings.HasPrefix(name, "With") && (strings.Contains(name, "Timeout") || strings.Contains(name, "Deadline")) ||
				pkg.Name == "time" && slices.Contains([]string{"After", "AfterFunc", "NewTimer", "NewTicker", "Tick", "Sleep"}, name)) {
				sets = true
			} else if name == "Exec" || name == "Do" {
				sends = true
			}
			return true
		})
		listed := slices.ContainsFunc(bounds, func(b goBound) bool { return b.function == fn.Name.Name })
		switch {
		case sets && !listed:
			findings = append(findings, "seaweedfs.go: "+fn.Name.Name+" sets a deadline that no case is listed for in goBounds")
		case sends && !sets:
			findings = append(findings, "seaweedfs.go: "+fn.Name.Name+" sends a request with no deadline of its own")
		}
		if sets {
			deadlines[fn.Name.Name] = true
		}
	}
	for _, b := range bounds {
		if !deadlines[b.function] {
			findings = append(findings, "seaweedfs.go: goBounds lists "+b.function+", which sets no deadline")
		}
		if !names[b.caseName] {
			findings = append(findings, fmt.Sprintf("seaweedfs.go: the deadline of %s names a case that does not exist: %q", b.function, b.caseName))
		}
	}
	return findings
}
