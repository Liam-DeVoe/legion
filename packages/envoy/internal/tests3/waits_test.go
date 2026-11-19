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

	"gopkg.in/yaml.v3"
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
// that fails when it is cut, every request and wait of the workflow steps around the browser
// suite's file store has a bound, and this is the program that says so, by reading the three
// files:
//
//   - The script asks the store things only in shell and master, each line of them under
//     `timeout -s KILL <seconds>`; a timeout by any other signal is refused, since weed shell
//     ends 0 on TERM in the SeaweedFS image.
//   - The script waits (until, while, sleep, wait) only in within, which ends by its seconds.
//   - The script runs its own functions and the commands of scriptCommands alone, and so does
//     within, so a command that can wait with no bound (`tail -f`, `read`, `nc -l`) is named:
//     tail only as `tail -n <lines>`, and exit only in fail and usage, so no check ends the
//     script with no message for a case to want.
//   - Every bound the script sets, `timeout ... <seconds>`, `within <seconds>` and
//     `registered <seconds>`, lies in a text of scriptBounds, whose case exists.
//   - Every message the script fails by is the want of a case of storeCases, so a check or a
//     wait with no case fails here, and no `::error::` is printed but by fail and usage.
//   - In seaweedfs.go, every function that sets a deadline is in goBounds, whose case exists, and
//     every function that sends a request (Exec, Do) sets one.
//   - In the shard job of .github/workflows/dispatch-browser.yaml, the steps of storeSteps, which
//     start the browser suite's file store, wait for it and check it, ask the store things only
//     through the wait step's s3, by a curl under `--max-time "$seconds"`; s3 ends the step by
//     name, with exit 1, on curl's timeout (exit 28); and every s3 call gives a whole number of
//     seconds. They wait only in an until right after `deadline=$(( $(date +%s) + <seconds> ))`,
//     whose body ends the step by name, with exit 1, once the deadline passes; no while, for or
//     sleep stands outside such a wait. No case in this package starts those steps, so these
//     rules are their check.
//
// The last subtest plants each kind of fault and wants this program to name it, so a rule that
// cannot see what it is for fails too.
func TestEveryWaitAndCheckOfTheScriptHasACase(t *testing.T) {
	cases := caseNames(t, storeCases)
	source, err := os.ReadFile("seaweedfs.go")
	if err != nil {
		t.Fatalf("read seaweedfs.go: %v", err)
	}
	steps := storeStepRuns(t)
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
	t.Run("dispatch-browser.yaml", func(t *testing.T) {
		for _, name := range storeSteps {
			for _, finding := range stepFindings(name, steps[name]) {
				t.Error(finding)
			}
		}
	})
	t.Run("a planted request, wait, bound, check or deadline with no case fails it", func(t *testing.T) {
		script := func(old, replacement string) func() []string {
			return func() []string {
				return scriptFindings(replaceOnce(t, "one-collection.sh", oneCollection, old, replacement), storeCases, scriptBounds, cases)
			}
		}
		gosource := func(old, replacement string) func() []string {
			return func() []string {
				return goFindings(replaceOnce(t, "seaweedfs.go", string(source), old, replacement), goBounds, cases)
			}
		}
		wait := func(old, replacement string) func() []string {
			return func() []string {
				return stepFindings(waitStep, replaceOnce(t, "the wait step", steps[waitStep], old, replacement))
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
			{"a check that exits with no message", script("  registered 0\n  list\n", "  [ -d /data ] || exit 1\n  registered 0\n  list\n"), "exits outside fail and usage"},
			{"a wait that follows the log", script("  wait_in_force\n  alone_before_writes\n", "  wait_in_force\n  tail -f \"$log\" | grep -q -m1 procesing\n  alone_before_writes\n"), "a tail that is not `tail -n <lines>`"},
			{"a command that can wait", script("  wait_in_force\n  alone_before_writes\n", "  wait_in_force\n  read answer\n  alone_before_writes\n"), "runs read, which is not in scriptCommands"},
			{"a wait given to within", script(`"$work/rule.txt" grep -q "procesing.*$marker" "$log"`, `"$work/rule.txt" inotifywait -q -e modify "$log"`), "runs inotifywait, which is not in scriptCommands"},
			{"an error printed outside fail", script("\n  grow\n  configure\n", "\n  grow\n  echo \"::error::the store is odd\"\n  configure\n"), "prints ::error:: outside fail"},
			{"a case taken away", func() []string {
				kept := slices.DeleteFunc(slices.Clone(storeCases), func(c storeCase) bool { return strings.HasPrefix(c.name, "the grow refused") })
				return scriptFindings(oneCollection, kept, scriptBounds, caseNames(t, kept))
			}, "the master grew no volumes for $collection"},
			{"a deadline with no case", gosource("\tt.Helper()\n\tcontainer, err := testcontainers.Run(", "\tt.Helper()\n\tctx, cancel := context.WithTimeout(ctx, time.Minute)\n\tdefer cancel()\n\tcontainer, err := testcontainers.Run("), "startServer sets a deadline"},
			{"a request with no deadline", gosource("\n// call sends", "\nfunc ping(c testcontainers.Container) {\n\t_, _, _ = c.Exec(context.Background(), []string{\"true\"})\n}\n\n// call sends"), "ping sends a request with no deadline"},
			{"a deadline cut", gosource("\tctx, cancel := context.WithTimeout(ctx, deadline)\n\tdefer cancel()\n\tsent, err", "\tsent, err"), "request sends a request with no deadline"},
			{"a curl with no bound in the wait step", wait("store=http://", "curl -sf -o /dev/null http://127.0.0.1:18333/\nstore=http://"), "a curl outside s3"},
			{"the wait step's s3 with its bound cut", wait(`curl -sf --max-time "$seconds" "$@"`, `curl -sf "$@"`), `a curl outside s3, or with no --max-time "$seconds"`},
			{"s3 that tries again a request that timed out", wait(`if [ "$status" = 28 ]; then`, `if false; then`), "on curl's timeout"},
			{"an s3 call whose seconds are a variable", wait(`s3 10 "the bucket"`, `s3 "$seconds" "the bucket"`), "an s3 call whose seconds are not a whole number"},
			{"a wait with no deadline in the wait step", wait("deadline=$(( $(date +%s) + 60 ))\nuntil s3 15", "until s3 15"), "a wait with no deadline"},
			{"a deadline that ends nothing", wait(`[ "$(date +%s)" -lt "$deadline" ] || { echo "::error::the file store took no object"; cat "$RUNNER_TEMP/weed.log"; exit 1; }`, "true"), "a wait that does not end the step by name"},
			{"a wait by tries in the wait step", wait("store=http://", "for try in $(seq 1 60); do s3 10 \"the root\" -o /dev/null http://127.0.0.1:18333/ && break; done\nstore=http://"), "a loop that is not a wait with a deadline"},
			{"a sleep outside a wait", wait("store=http://", "sleep 5\nstore=http://"), "a sleep outside a wait"},
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

// scriptCommands are the commands one-collection.sh may run besides its own functions: shell
// builtins, and programs that read what they are given and end, or that run under a bound the
// rules above hold (timeout, sleep). A command that can wait with no bound is left out, so the
// program names it where the script or within runs it; tail is held to `tail -n <lines>`, and exit
// to fail and usage, by rules of their own.
var scriptCommands = map[string]bool{
	"[": true, "basename": true, "cat": true, "date": true, "echo": true, "exit": true, "grep": true,
	"mktemp": true, "printf": true, "return": true, "set": true, "shift": true, "sleep": true,
	"tail": true, "timeout": true, "tr": true, "trap": true,
}

var (
	scriptFunction   = regexp.MustCompile(`^([a-z_][a-z0-9_]*)\(\) \{$`)
	quoted           = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'[^']*'`)
	storeRequest     = regexp.MustCompile(`\b(weed|wget|curl|nc)\b`)
	boundedRequest   = regexp.MustCompile(`timeout -s KILL [0-9]+ (weed|wget|curl|nc)\b`)
	anyTimeout       = regexp.MustCompile(`\btimeout\b`)
	killTimeout      = regexp.MustCompile(`^timeout -s KILL [0-9]+ `)
	scriptWait       = regexp.MustCompile(`\b(until|while|sleep|wait)\b`)
	scriptBoundSet   = regexp.MustCompile(`\btimeout(?: -s [A-Z]+)? [0-9]+|\bwithin [0-9]+|\bregistered [0-9]+`)
	failMessage      = regexp.MustCompile(`\bfail "((?:[^"\\]|\\.)*)"|\bwithin (?:[0-9]+|"[^"]*") "((?:[^"\\]|\\.)*)"`)
	onlyVariable     = regexp.MustCompile(`^\$[a-z_0-9]+$`)
	variable         = regexp.MustCompile(`\\\$\{?[a-z_]+\}?`)
	commandSeparator = regexp.MustCompile(`[;&|(){}]`)
	substitution     = regexp.MustCompile(`\$\(([^()]*)`)
	withinCommand    = regexp.MustCompile(`\bwithin (?:[0-9]+|"") "" "" ([^;&|]*)`)
	commandName      = regexp.MustCompile(`^(?:[a-z_][a-z0-9_]*|\[)$`)
	assignment       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	tailLines        = regexp.MustCompile(`^-n [0-9]+(?:\s|$)`)
)

// commands answers the commands that shell code runs, each as its name and the words after it:
// code, its quoted text blanked, is cut at every ; & | ( ) { }, and each piece is read past the
// keywords that lead a command and the assignments before it. A piece led by case or for runs no
// word of its own, and a word that is no name, such as "$@", is no command this program can name.
func commands(code string) [][2]string {
	var found [][2]string
	for _, piece := range commandSeparator.Split(code, -1) {
		words := strings.Fields(piece)
		for len(words) > 0 && (slices.Contains([]string{"if", "then", "else", "elif", "do", "until", "while", "!"}, words[0]) || assignment.MatchString(words[0])) {
			words = words[1:]
		}
		if len(words) == 0 || slices.Contains([]string{"case", "for", "esac", "done", "fi"}, words[0]) || !commandName.MatchString(words[0]) {
			continue
		}
		found = append(found, [2]string{words[0], strings.Join(words[1:], " ")})
	}
	return found
}

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
	functions := map[string]bool{}
	for _, line := range strings.Split(script, "\n") {
		if m := scriptFunction.FindStringSubmatch(line); m != nil {
			functions[m[1]] = true
		}
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
		// What the line runs: its own commands, those of each $(...), which its quoted text can
		// hold, and the command within is given to run until it succeeds.
		run := commands(code)
		for _, m := range substitution.FindAllStringSubmatch(line, -1) {
			run = append(run, commands(quoted.ReplaceAllString(m[1], `""`))...)
		}
		for _, m := range withinCommand.FindAllStringSubmatch(code, -1) {
			run = append(run, commands(m[1])...)
		}
		for _, command := range run {
			switch name := command[0]; {
			case name == "exit" && function != "fail" && function != "usage":
				findings = append(findings, at+": exits outside fail and usage, so with no message for a case to want: "+trimmed)
			case name == "tail" && !tailLines.MatchString(command[1]):
				findings = append(findings, at+": a tail that is not `tail -n <lines>`, which can follow a file for ever: "+trimmed)
			case !scriptCommands[name] && !functions[name]:
				findings = append(findings, at+": runs "+name+", which is not in scriptCommands, so it may wait with no bound: "+trimmed)
			}
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

// workflow is the workflow whose shard job starts the browser suite's file store, read from this
// package's directory; waitStep is that job's step that waits for the store, and storeSteps every
// step of it that starts the store, waits for it or checks it.
const (
	workflow = "../../../../.github/workflows/dispatch-browser.yaml"
	waitStep = "Wait for the S3-compatible server"
)

var storeSteps = []string{"Start an S3-compatible server for uploaded files", waitStep, "Check that no write grew the file store"}

var (
	stepDeadline = regexp.MustCompile(`^deadline=\$\(\( \$\(date \+%s\) \+ [0-9]+ \)\)$`)
	stepWaitEnds = regexp.MustCompile(`^\[ "\$\(date \+%s\)" -lt "\$deadline" \] \|\| \{ echo "::error::[^"]+"; .*\bexit 1; \}$`)
	stepS3Call   = regexp.MustCompile(`(?:^|[\s;|&(!])s3 ([^ ]+) `)
	wholeNumber  = regexp.MustCompile(`^[0-9]+$`)
	stepLoop     = regexp.MustCompile(`\b(while|for)\b`)
	stepCurl     = regexp.MustCompile(`\bcurl\b`)
	stepSleep    = regexp.MustCompile(`\bsleep\b`)
	stepTimeout  = regexp.MustCompile(`if \[ "\$status" = 28 \]; then\n\s*echo "::error::[^"]*"\n(?:\s*cat "\$RUNNER_TEMP/weed\.log"\n)?\s*exit 1\n`)
)

// storeStepRuns answers the run: text of each step of storeSteps, read from workflow's shard job.
func storeStepRuns(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("read %s: %v", workflow, err)
	}
	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse %s: %v", workflow, err)
	}
	runs := map[string]string{}
	for _, step := range parsed.Jobs["shard"].Steps {
		if slices.Contains(storeSteps, step.Name) {
			runs[step.Name] = step.Run
		}
	}
	for _, name := range storeSteps {
		if runs[name] == "" {
			t.Fatalf("%s: the shard job has no step %q that runs a script", workflow, name)
		}
	}
	return runs
}

// stepFindings answers what the run: text (run) of the shard job's step name breaks of the rules
// above for the workflow's store steps.
func stepFindings(name, run string) []string {
	var findings []string
	function := ""
	deadline, waiting, ends := false, false, false
	for number, line := range strings.Split(run, "\n") {
		at := fmt.Sprintf("dispatch-browser.yaml, step %q, line %d", name, number+1)
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
		// The deadline must be the line just before its until.
		afterDeadline := deadline
		deadline = stepDeadline.MatchString(trimmed)
		code := quoted.ReplaceAllString(line, `""`)
		// Read on the line itself, not on code: a curl inside "$(...)" is quoted text.
		if stepCurl.MatchString(line) && (function != "s3" || !strings.Contains(line, `curl -sf --max-time "$seconds" `)) {
			findings = append(findings, at+`: a curl outside s3, or with no --max-time "$seconds" of its own: `+trimmed)
		}
		for _, m := range stepS3Call.FindAllStringSubmatch(code, -1) {
			if !wholeNumber.MatchString(m[1]) {
				findings = append(findings, at+": an s3 call whose seconds are not a whole number: "+trimmed)
			}
		}
		switch {
		case strings.HasPrefix(trimmed, "until "):
			if !afterDeadline {
				findings = append(findings, at+": a wait with no deadline=$(( $(date +%s) + <seconds> )) just before it: "+trimmed)
			}
			waiting, ends = true, false
		case waiting && trimmed == "done":
			if !ends {
				findings = append(findings, at+": a wait that does not end the step by name, with exit 1, once its deadline passes")
			}
			waiting = false
		case waiting && stepWaitEnds.MatchString(trimmed):
			ends = true
		}
		if stepLoop.MatchString(code) {
			findings = append(findings, at+": a loop that is not a wait with a deadline (an until after its deadline): "+trimmed)
		}
		if stepSleep.MatchString(code) && !waiting {
			findings = append(findings, at+": a sleep outside a wait with a deadline: "+trimmed)
		}
	}
	if name == waitStep && !stepTimeout.MatchString(run) {
		findings = append(findings, fmt.Sprintf("dispatch-browser.yaml, step %q: s3 does not end the step by name, with exit 1, on curl's timeout (exit 28)", name))
	}
	return findings
}
