package tests3

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
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
	"mvdan.cc/sh/v3/syntax"
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
// that fails when it is cut, every command of the workflow steps around the browser suite's file
// store ends by itself or under a bound these rules read, and this is the program that says so,
// by reading the three files. It reads the script, and the run: text of each store step, through
// the shell reader of shellread_test.go, which parses them as POSIX shell and answers every
// command the shell runs, wherever it runs it, with its words as the shell resolves them. What
// the reader cannot read fails the program by name: a file that does not parse, a kind of shell
// other than those the files use, a command whose name is not plain text (a variable, a
// substitution, a file-name pattern), and a trap whose command is not plain text. It does not
// read what a listed command reads: a cat or a grep given a file that never ends, or no file at
// all, passes it; both places that run the script and the steps give them a standard input
// already at its end.
//
//   - The script asks the store things (weed, wget, curl, nc) only in shell and master, each as
//     the command of `timeout -s KILL <seconds>` with seconds above 0; a timeout by any other
//     signal is refused, since weed shell ends 0 on TERM in the SeaweedFS image, and so is one of
//     0 seconds, which turns the bound off.
//   - The script waits (until, while, sleep, wait) only in within, which ends by its seconds,
//     and sleeps only as `sleep 1`.
//   - The script runs its own functions and the commands of scriptCommands alone, and so do the
//     commands that within, timeout and a trap run, so a command that can wait with no bound
//     (`read`, `nc -l`, `.`) is named. A command named by a path is read by its base name. tail
//     runs only as `tail -n <lines> <file>...`: any other word of it (`-f`, `-F`, `--follow`, or
//     a word the shell splits or matches to file names, which may give one) can follow a file for
//     ever. No function runs itself again, at once or through others.
//   - No check ends the script with no message for a case to want: exit runs only in fail and
//     usage, return only as `return "$status"`, and no test (`[`, `test`, `grep`) runs where
//     nothing acts on its failure (an if, until or while, || or &&), which `set -e` turns into an
//     exit with no message.
//   - Every bound the script sets, `timeout ... <seconds>`, `within <seconds>` and
//     `registered <seconds>`, gives its seconds as a whole number and lies in a text of
//     scriptBounds, whose case exists; only registered's own within takes them as "$1".
//   - Every message the script fails by, fail's and within's, is the want of a case of
//     storeCases, each variable in it standing for any text, so a check or a wait with no case
//     fails here; a message that is a variable alone, but within's own, or that holds any other
//     expansion, is refused, and no `::error::` is printed but by fail and usage.
//   - In seaweedfs.go, every function that sets a deadline is in goBounds, whose case exists, and
//     every function that sends a request (Exec, Do) sets one.
//   - In the shard job of .github/workflows/dispatch-browser.yaml, the steps of storeSteps, which
//     start the browser suite's file store, wait for it and check it, run their own function s3
//     and the commands of stepCommands alone, so a request or a wait by any other command
//     (`wget`, `weed shell`, `tail`, `.`) is named, and no function runs itself again. They ask
//     the store things only through the wait step's s3, whose one kind of curl is
//     `curl -sf --max-time "$seconds" "$@"`, where seconds is s3's first argument: every s3 call
//     gives a whole number of them above 0 (curl's 0 is no limit), and gives curl only -o, -X,
//     --data-binary and a URL, none of which sets a time. s3 ends the step by name, with exit 1,
//     on curl's timeout (exit 28). They start weed server only by `nohup weed server ... &`, in
//     the background of the step that starts the store. They wait only in an until that is a
//     statement of its own right after `deadline=$(( $(date +%s) + <seconds> ))`, whose body ends
//     the step by name, with exit 1, once the deadline passes, and sleeps only as `sleep 1`. No
//     while or for stands anywhere, and no sleep outside such a wait. No case in this package
//     starts those steps, so these rules are their check.
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
		// inPrepare plants code in prepare, between two steps that no other plant touches.
		inPrepare := func(code string) func() []string {
			return script("  wait_in_force\n  alone_before_writes\n", "  wait_in_force\n"+code+"  alone_before_writes\n")
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
		// inWait plants code in the wait step, before the line that names the bucket.
		inWait := func(code string) func() []string {
			return wait("store=http://", code+"store=http://")
		}
		start := func(old, replacement string) func() []string {
			return func() []string {
				return stepFindings(storeSteps[0], replaceOnce(t, "the start step", steps[storeSteps[0]], old, replacement))
			}
		}
		const (
			tail     = "a tail that is not `tail -n <lines> <file>...`"
			waits    = "waits outside within"
			s3Zero   = "an s3 call whose seconds are not a whole number above 0"
			s3Curl   = "an s3 call that gives curl "
			stepCurl = "a curl outside s3, or other than s3's own"
			ends     = `a wait that does not end the step by name`
			noDue    = "a wait with no deadline"
			itself   = "a function that runs itself again"
		)
		// One wait of the wait step, read by the program, after its deadline: shape is what
		// stands around it.
		readyWait := `until s3 10 "the root" -o /dev/null http://127.0.0.1:18333/; do [ "$(date +%s)" -lt "$deadline" ] || { echo "::error::no root"; exit 1; }; sleep 1; done`
		for _, c := range []struct {
			name     string
			findings func() []string
			want     string
		}{
			{"a request with no bound", script("prepare() {\n", "prepare() {\n  wget -q -O /dev/null http://127.0.0.1:9333/\n"), "asks the store outside shell and master"},
			{"a shell bounded by TERM", script("timeout -s KILL 60 weed", "timeout 60 weed"), "a timeout not by KILL"},
			{"a shell bounded by a TERM it names", script("timeout -s KILL 60 weed", "timeout -s TERM 60 weed"), "a timeout not by KILL"},
			{"a bound changed", script("timeout -s KILL 10 wget", "timeout -s KILL 5 wget"), `the bound "timeout -s KILL 5" is in no text`},
			{"a wait outside within", script("prepare() {\n", "prepare() {\n  until listed; do sleep 1; done\n"), waits},
			{"a wait with no sleep outside within", script("prepare() {\n", "prepare() {\n  while ! listed; do date > /dev/null; done\n"), waits},
			{"a bound with no case", script("\n  grow\n  configure\n", "\n  grow\n  within 30 \"the store took no rule\" \"$work/x\" true\n  configure\n"), `the bound "within 30" is in no text`},
			{"a bound whose seconds are not a number", inPrepare("  within \"$((60))\" \"the filer did not put the rule for $collection in force within a minute\" \"$work/rule.txt\" grep -q x \"$log\"\n"), "a bound whose seconds this program cannot read"},
			{"a check with no case", script("\n  grow\n  configure\n", "\n  grow\n  [ -d /data ] || fail \"the store has no data directory\"\n  configure\n"), `the message "the store has no data directory" is the want of no case`},
			{"a message that is a variable", script("  registered 0\n  list\n", "  [ -d /data ] || fail \"$collection\"\n  registered 0\n  list\n"), "a message this program cannot read"},
			{"a message that holds a substitution", script("  registered 0\n  list\n", "  [ -d /data ] || fail \"the store holds $(cat \"$work/x\")\"\n  registered 0\n  list\n"), "a message this program cannot read"},
			{"a check that exits with no message", script("  registered 0\n  list\n", "  [ -d /data ] || exit 1\n  registered 0\n  list\n"), "exits outside fail and usage"},
			{"a wait that follows the log", inPrepare("  tail -f \"$log\" | grep -q -m1 procesing\n"), tail},
			{"a tail that follows after its lines", inPrepare("  tail -n 200 -f \"$log\" > /dev/null\n"), tail},
			{"a tail whose follow is quoted", inPrepare("  tail -n 200 \"-f\" \"$log\" > /dev/null\n"), tail},
			{"a tail whose follow is on the next line", inPrepare("  tail -n 1 \\\n    -f \"$log\" > /dev/null\n"), tail},
			{"a tail whose follow a variable gives", inPrepare("  follow=-f\n  tail -n 200 $follow \"$log\" > /dev/null\n"), tail},
			{"a tail whose words \"$@\" gives", inPrepare("  tail -n 200 \"$@\" > /dev/null\n"), tail},
			{"a tail given a file-name pattern", inPrepare("  tail -n 200 [-]f \"$log\" > /dev/null\n"), tail},
			{"a tail that follows by name", inPrepare("  tail -F \"$log\" > /dev/null\n"), tail},
			{"a tail named by its path", inPrepare("  /usr/bin/tail -f \"$log\" | grep -q -m1 procesing\n"), tail},
			{"a tail whose word is escaped", inPrepare("  \\tail -f \"$log\" > /dev/null\n"), tail},
			{"a tail whose word is partly quoted", inPrepare("  t'ai'l -f \"$log\" > /dev/null\n"), tail},
			{"a tail after a redirection", inPrepare("  > /dev/null tail -f \"$log\"\n"), tail},
			{"a command given by a pattern", inPrepare("  t?il -f \"$log\" > /dev/null\n"), "runs t?il, which is not in scriptCommands"},
			{"a tail in backticks", inPrepare("  seen=`tail -f \"$log\" | grep -m1 procesing`\n"), tail},
			{"a tail given to within", script(`"$work/rule.txt" grep -q "procesing.*$marker" "$log"`, `"$work/rule.txt" tail -n 1 -f "$log"`), tail},
			{"a tail given to a within whose seconds are unquoted", script(`within "$1" "the master lists no volume server, so the store cannot take a write" "$work/status.txt" listed`, `within $1 "the master lists no volume server, so the store cannot take a write" "$work/status.txt" tail -n 1 -f "$log"`), tail},
			{"a tail given to a within whose message is bare", inPrepare("  within \"$((60))\" rule \"$work/rule.txt\" tail -f \"$log\"\n"), tail},
			{"a tail given to timeout", inPrepare("  timeout -s KILL 10 tail -f \"$log\" > /dev/null\n"), tail},
			{"a sleep in a quoted substitution", inPrepare("  echo \"$(sleep 100000)\"\n"), waits},
			{"a sleep in arithmetic", inPrepare("  x=$(( $(sleep 100000) + 1 ))\n"), waits},
			{"a sleep in a substitution inside another", inPrepare("  x=$(echo $(sleep 100000))\n"), waits},
			{"a sleep in a redirection", inPrepare("  echo ready > \"$work/$(sleep 100000)\"\n"), waits},
			{"a sleep in a here-document", inPrepare("  cat <<EOF\n$(sleep 100000)\nEOF\n"), waits},
			{"a sleep in a parameter's default", inPrepare("  echo \"${collection:-$(sleep 100000)}\"\n"), waits},
			{"a sleep in a trap", script(`trap 'rm -rf "$work"' EXIT`, `trap 'sleep 100000; rm -rf "$work"' EXIT`), waits},
			{"a sleep in a trap past its --", script(`trap 'rm -rf "$work"' EXIT`, `trap -- 'sleep 100000; rm -rf "$work"' EXIT`), waits},
			{"a tail in a trap of one escaped word", script(`trap 'rm -rf "$work"' EXIT`, `trap tail\ -f\ /dev/null EXIT`), tail},
			{"a trap whose command holds a variable", script(`trap 'rm -rf "$work"' EXIT`, `trap "sleep 100000; rm -rf \"$work\"" EXIT`), "a trap whose command is not plain text"},
			{"a sleep that holds within past its seconds", script("    sleep 1\n  done\n", "    sleep 100000\n  done\n"), "a sleep other than `sleep 1`"},
			{"a command given by a variable", inPrepare("  follow=tail; $follow -f \"$log\" > /dev/null\n"), "runs $follow, which is not in scriptCommands"},
			{"a check that returns with no message", script("  registered 0\n  list\n", "  [ -d /data ] || return 1\n  registered 0\n  list\n"), `returns a status other than "$status"`},
			{"a test that nothing acts on", script("  registered 0\n  list\n", "  [ -d /data ]\n  registered 0\n  list\n"), "a test whose failure nothing acts on"},
			{"a shell bounded by 0 seconds", script("timeout -s KILL 60 weed", "timeout -s KILL 0 weed"), "or not of whole seconds above 0"},
			{"a command that can wait", inPrepare("  read answer\n"), "runs read, which is not in scriptCommands"},
			{"a file the script sources", inPrepare("  printf 'tail -f %s\\n' \"$log\" > \"$work/follow\"\n  . \"$work/follow\"\n"), "runs ., which is not in scriptCommands"},
			{"a script run by its path", inPrepare("  ./follow.sh > /dev/null\n"), "runs follow.sh, which is not in scriptCommands"},
			{"a wait given to within", script(`"$work/rule.txt" grep -q "procesing.*$marker" "$log"`, `"$work/rule.txt" inotifywait -q -e modify "$log"`), "runs inotifywait, which is not in scriptCommands"},
			{"a function that runs itself", script("\nprepare() {\n", "\npoll() {\n  listed || poll\n}\n\nprepare() {\n"), itself},
			{"a subshell", inPrepare("  ( sleep 1 )\n"), "runs a Subshell, a kind of shell this program does not read"},
			{"a script that does not parse", script("prepare() {\n  registered 60\n", "prepare() {\n  if registered 60\n"), "does not parse as POSIX shell"},
			{"an error printed outside fail", script("\n  grow\n  configure\n", "\n  grow\n  echo \"::error::the store is odd\"\n  configure\n"), "prints ::error:: outside fail"},
			{"a case taken away", func() []string {
				kept := slices.DeleteFunc(slices.Clone(storeCases), func(c storeCase) bool { return strings.HasPrefix(c.name, "the grow refused") })
				return scriptFindings(oneCollection, kept, scriptBounds, caseNames(t, kept))
			}, `the message "the master grew no volumes for $collection" is the want of no case`},
			{"a deadline with no case", gosource("\tt.Helper()\n\tcontainer, err := testcontainers.Run(", "\tt.Helper()\n\tctx, cancel := context.WithTimeout(ctx, time.Minute)\n\tdefer cancel()\n\tcontainer, err := testcontainers.Run("), "startServer sets a deadline"},
			{"a request with no deadline", gosource("\n// call sends", "\nfunc ping(c testcontainers.Container) {\n\t_, _, _ = c.Exec(context.Background(), []string{\"true\"})\n}\n\n// call sends"), "ping sends a request with no deadline"},
			{"a deadline cut", gosource("\tctx, cancel := context.WithTimeout(ctx, deadline)\n\tdefer cancel()\n\tsent, err", "\tsent, err"), "request sends a request with no deadline"},
			{"a curl with no bound in the wait step", inWait("curl -sf -o /dev/null http://127.0.0.1:18333/\n"), "a curl outside s3"},
			{"the wait step's s3 with its bound cut", wait(`curl -sf --max-time "$seconds" "$@"`, `curl -sf "$@"`), stepCurl},
			{"a second curl in s3 with no bound", wait(`curl -sf --max-time "$seconds" "$@"`, `curl -sf --max-time "$seconds" "$@" || curl -sf "$@"`), stepCurl},
			{"s3's curl with its bound turned off after it", wait(`curl -sf --max-time "$seconds" "$@"`, `curl -sf --max-time "$seconds" "$@" --max-time 0`), stepCurl},
			{"s3 that tries again a request that timed out", wait(`if [ "$status" = 28 ]; then`, `if false; then`), "on curl's timeout"},
			{"an s3 call whose seconds are a variable", wait(`s3 10 "the bucket"`, `s3 "$seconds" "the bucket"`), "an s3 call whose seconds are not a whole number"},
			{"an s3 call of 0 seconds", wait(`s3 10 "the bucket"`, `s3 0 "the bucket"`), s3Zero},
			{"an s3 call of 0 seconds after two spaces", wait(`s3 10 "the bucket"`, `s3  0 "the bucket"`), s3Zero},
			{"an s3 call whose 0 seconds are on the next line", wait(`s3 10 "the bucket"`, "s3 \\\n  0 \"the bucket\""), s3Zero},
			{"an escaped s3 call of 0 seconds", wait(`s3 10 "the readiness object's delete"`, `\s3 0 "the readiness object's delete"`), s3Zero},
			{"an s3 call that turns curl's bound off", wait(`s3 10 "the bucket" -X PUT`, `s3 10 "the bucket" --max-time 0 -X PUT`), s3Curl + "--max-time"},
			{"an s3 call that turns curl's bound off by -m", wait(`s3 10 "the bucket" -X PUT`, `s3 10 "the bucket" -m 0 -X PUT`), s3Curl + "-m"},
			{"an s3 call that has curl try again", wait(`s3 10 "the bucket" -X PUT`, `s3 10 "the bucket" --retry 100000 --retry-all-errors -X PUT`), s3Curl + "--retry"},
			{"s3 with seconds of its own", wait(`seconds=$1 what=$2`, `seconds=0 what=$2`), "s3 takes its seconds from other than its first argument"},
			{"a wait with no deadline in the wait step", wait("deadline=$(( $(date +%s) + 60 ))\nuntil s3 15", "until s3 15"), noDue},
			{"a wait in braces after its deadline", inWait("deadline=$(( $(date +%s) + 60 ))\n{ " + readyWait + "; }\n"), noDue},
			{"a wait after && after its deadline", inWait("deadline=$(( $(date +%s) + 60 ))\n[ -d / ] && " + readyWait + "\n"), noDue},
			{"a deadline that ends nothing", wait(`[ "$(date +%s)" -lt "$deadline" ] || { echo "::error::the file store took no object"; cat "$RUNNER_TEMP/weed.log"; exit 1; }`, "true"), ends},
			{"a wait by tries in the wait step", inWait("for try in $(seq 1 60); do s3 10 \"the root\" -o /dev/null http://127.0.0.1:18333/ && break; done\n"), "a loop that is not a wait with a deadline"},
			{"a loop in a quoted substitution", inWait("x=\"$(while :; do :; done)\"\n"), "a loop that is not a wait with a deadline"},
			{"a sleep outside a wait", inWait("sleep 5\n"), "a sleep outside a wait"},
			{"a request by wget in the wait step", inWait("wget -q -O /dev/null http://127.0.0.1:18333/\n"), "runs wget, which is not in stepCommands"},
			{"a request by a command a variable gives", inWait("fetch=wget; $fetch -q -O /dev/null http://127.0.0.1:18333/\n"), "runs $fetch, which is not in stepCommands"},
			{"a weed shell in the wait step", inWait("printf 'collection.list\\n' | weed shell -master=127.0.0.1:9333 > \"$RUNNER_TEMP/collections.txt\"\n"), "runs weed, which is not in stepCommands"},
			{"a tail in the wait step", inWait("tail -f \"$RUNNER_TEMP/weed.log\" | grep -q -m1 \"Start Seaweed S3\"\n"), "runs tail, which is not in stepCommands"},
			{"a file the wait step sources", inWait(". \"$RUNNER_TEMP/more.sh\"\n"), "runs ., which is not in stepCommands"},
			{"a function of the wait step that runs itself", inWait("poll() {\n  s3 10 \"the root\" -o /dev/null http://127.0.0.1:18333/ || poll\n}\npoll\n"), itself},
			{"a wait on one line whose deadline ends nothing", inWait("deadline=$(( $(date +%s) + 60 ))\nuntil s3 10 \"the root\" -o /dev/null http://127.0.0.1:18333/; do sleep 1; done\n"), ends},
			{"a long sleep in a wait", wait("  sleep 1\ndone\npackages/envoy/internal/tests3/one-collection.sh prepare", "  sleep 100000\ndone\npackages/envoy/internal/tests3/one-collection.sh prepare"), "a sleep in a wait other than its sleep 1"},
			{"a wait whose done is never found", wait("one-collection.sh check", "one-collection.sh check dispatch-e2e-objects \"$RUNNER_TEMP/weed.log\"\ndeadline=$(( $(date +%s) + 60 ))\nuntil s3 10 \"the root\" -o /dev/null http://127.0.0.1:18333/; do\n  sleep 1\n#"), "does not parse as POSIX shell"},
			{"a store started in the foreground", start("2>&1 &", "2>&1"), "a nohup that is not `nohup weed server ... &`"},
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
// program names it wherever the script runs it; tail is held to `tail -n <lines> <file>...`,
// sleep to `sleep 1`, exit to fail and usage and return to `return "$status"`, by rules of their
// own.
var scriptCommands = map[string]bool{
	"[": true, "basename": true, "cat": true, "date": true, "echo": true, "exit": true, "grep": true,
	"mktemp": true, "printf": true, "return": true, "rm": true, "set": true, "shift": true,
	"sleep": true, "tail": true, "timeout": true, "tr": true, "trap": true,
}

// storeRequests are the commands that can ask the store things, which the script runs only
// under a timeout in shell and master.
var storeRequests = map[string]bool{"weed": true, "wget": true, "curl": true, "nc": true}

// messageWord gives, for each command of the script that fails by a message, which of its words
// the message is: fail <message> [file...] and within <seconds> <message> <file> <command>...
var messageWord = map[string]int{"fail": 0, "within": 1}

var (
	wholeSeconds = regexp.MustCompile(`^[1-9][0-9]*$`)
	digits       = regexp.MustCompile(`^[0-9]+$`)
)

// killTimeout answers whether the words of a timeout are -s KILL <seconds above 0> <command>...
func killTimeout(args []shellWord) bool {
	return len(args) > 3 && args[0].is("-s") && args[1].is("KILL") && args[2].literal && wholeSeconds.MatchString(args[2].text)
}

// tailReadsLines answers whether the words of a tail are -n <lines> <file>..., and no others: any
// other option of tail (-f, -F, --follow) can follow a file for ever, and so can a word the
// shell splits, which may give one.
func tailReadsLines(args []shellWord) bool {
	if len(args) < 3 || !args[0].is("-n") || !args[1].literal || !digits.MatchString(args[1].text) {
		return false
	}
	for _, file := range args[2:] {
		if !file.single || strings.HasPrefix(file.text, "-") {
			return false
		}
	}
	return true
}

// boundSeconds answers the word of a bound that gives its seconds, timeout's, within's or
// registered's, and the bound's text as a finding names it.
func boundSeconds(name string, args []shellWord) (shellWord, string) {
	i := 0
	if name == "timeout" && len(args) > 2 && args[0].is("-s") {
		i = 2
	}
	if i >= len(args) {
		return shellWord{}, name
	}
	words := []string{name}
	for _, w := range args[:i+1] {
		words = append(words, w.form())
	}
	return args[i], strings.Join(words, " ")
}

// messagePattern answers the pattern of every text a message of fail or within can be, each
// plain variable in it standing for any text, and whether the message is a variable alone; ok is
// false when it holds any other expansion, which no pattern says.
func messagePattern(message shellWord) (pattern string, only, ok bool) {
	var b strings.Builder
	var read func(part syntax.WordPart, quoted bool) bool
	read = func(part syntax.WordPart, quoted bool) bool {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(regexp.QuoteMeta(unescape(p.Value, quoted)))
		case *syntax.SglQuoted:
			b.WriteString(regexp.QuoteMeta(p.Value))
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				if !read(inner, true) {
					return false
				}
			}
		case *syntax.ParamExp:
			if !plainVariable(p) {
				return false
			}
			b.WriteString(".+")
		default:
			return false
		}
		return true
	}
	for _, part := range message.word.Parts {
		if !read(part, false) {
			return "", false, false
		}
	}
	return b.String(), b.String() == ".+", true
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
	inListed := func(offset int) bool {
		return offset >= 0 && slices.ContainsFunc(listed, func(s span) bool { return s.start <= offset && offset < s.end })
	}
	var wants []string
	for _, c := range cases {
		wants = append(wants, c.want)
	}
	lines := strings.Split(script, "\n")
	finding := func(line int, what string) {
		shown := ""
		if line >= 1 && line <= len(lines) {
			shown = strings.TrimSpace(lines[line-1])
		}
		findings = append(findings, fmt.Sprintf("one-collection.sh:%d: %s: %s", line, what, shown))
	}
	read := readShell(script)
	for _, refused := range read.refused {
		finding(refused.line, refused.what)
	}
	selfRunning := read.selfRunning()
	for _, name := range slices.Sorted(maps.Keys(selfRunning)) {
		finding(int(read.functions[name].Pos().Line()), "a function that runs itself again ("+selfRunning[name]+"), so nothing but a bound ends it")
	}
	for _, loop := range read.loops {
		if loop.kind != "for" && loop.function != "within" {
			finding(loop.line, "waits outside within, which ends by its seconds")
		}
	}
	for _, c := range read.commands {
		if !c.name.literal {
			// within's "$@" is the command each call of within gives it, read at that call.
			if c.function != "within" || c.name.source != `"$@"` {
				finding(c.line, "runs "+c.name.source+", which is not in scriptCommands, so it may wait with no bound")
			}
			continue
		}
		name := c.name.text
		switch {
		case storeRequests[name]:
			if !c.timed || c.function != "shell" && c.function != "master" {
				finding(c.line, "asks the store outside shell and master, or with no `timeout -s KILL` of its own")
			}
		case name == "timeout" && !killTimeout(c.args):
			finding(c.line, "a timeout not by KILL, which weed shell answers with exit 0, or not of whole seconds above 0, since 0 turns the bound off")
		case name == "exit" && c.function != "fail" && c.function != "usage":
			finding(c.line, "exits outside fail and usage, so with no message for a case to want")
		case name == "return" && (len(c.args) != 1 || c.args[0].source != `"$status"`):
			finding(c.line, `returns a status other than "$status", which under set -e ends the script with no message for a case to want`)
		case name == "tail" && !tailReadsLines(c.args):
			finding(c.line, "a tail that is not `tail -n <lines> <file>...`, which can follow a file for ever")
		case !scriptCommands[name] && read.functions[name] == nil:
			finding(c.line, "runs "+name+", which is not in scriptCommands, so it may wait with no bound")
		}
		if (name == "sleep" || name == "wait") && c.function != "within" {
			finding(c.line, "waits outside within, which ends by its seconds")
		}
		if name == "sleep" && (len(c.args) != 1 || !c.args[0].is("1")) {
			finding(c.line, "a sleep other than `sleep 1`, which holds a wait past its seconds")
		}
		if slices.Contains([]string{"[", "test", "grep"}, name) && !c.acted {
			finding(c.line, "a test whose failure nothing acts on (an if, until or while, || or &&), which under set -e ends the script with no message for a case to want")
		}
		if c.function != "fail" && c.function != "usage" && slices.ContainsFunc(c.args, func(w shellWord) bool { return strings.Contains(w.text, "::error::") }) {
			finding(c.line, "prints ::error:: outside fail, so with no case")
		}
		if name == "timeout" || name == "within" || name == "registered" {
			switch seconds, text := boundSeconds(name, c.args); {
			case seconds.literal && digits.MatchString(seconds.text):
				if !inListed(c.offset) {
					finding(c.line, fmt.Sprintf("the bound %q is in no text of scriptBounds, so it has no case", text))
				}
			case name == "within" && c.function == "registered" && seconds.source == `"$1"`:
				// registered's own seconds, each call of which is a bound of its own
			default:
				finding(c.line, fmt.Sprintf("a bound whose seconds this program cannot read (%s), so no case can be listed for it", text))
			}
		}
		if i, gives := messageWord[name]; gives && i < len(c.args) {
			message := c.args[i]
			switch pattern, only, ok := messagePattern(message); {
			case only && c.function == "within":
				// within's own fail "$message": the message each call of within gives it, read at
				// that call.
			case !ok || only:
				finding(c.line, "a message this program cannot read, so no case can want it")
			case !slices.ContainsFunc(wants, regexp.MustCompile("^::error::"+pattern+"$").MatchString):
				finding(c.line, fmt.Sprintf("the message %q is the want of no case, so nothing fails when its check or wait is cut", message.text))
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

// stepCommands are the commands the store steps may run besides their own function s3: each
// ends by itself, or under a bound a rule of stepFindings reads. curl runs only in s3, under
// --max-time "$seconds"; one-collection.sh's own waits are held by the script's rules; nohup only
// starts weed server in the background, in the step that starts the store; sleep runs only as a
// wait's sleep 1. Any other command is named, so a request or a wait by any other command
// (wget, nc, tail, weed shell, .) fails the program.
var stepCommands = map[string]bool{
	"[": true, "cat": true, "curl": true, "date": true, "echo": true, "exit": true, "mkdir": true,
	"nohup": true, "one-collection.sh": true, "return": true, "shift": true, "sleep": true,
}

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

// s3Curl answers whether the words of a curl are s3's own: -sf --max-time "$seconds" "$@".
func s3Curl(args []shellWord) bool {
	want := []string{"-sf", "--max-time", `"$seconds"`, `"$@"`}
	return slices.EqualFunc(args, want, func(w shellWord, form string) bool { return w.form() == form })
}

// s3CurlWord answers the first word of an s3 call (s3 <seconds> <what> <curl word>...) that it
// gives curl and that is none of -o <file>, -X <method>, --data-binary <data> and a URL, so it
// may set curl's time (--max-time 0, -m 0, --retry); ok is true when there is none.
func s3CurlWord(args []shellWord) (word string, ok bool) {
	for i := 2; i < len(args); i++ {
		w := args[i]
		switch {
		case (w.is("-o") || w.is("--data-binary")) && i+1 < len(args) && args[i+1].single:
			i++
		case w.is("-X") && i+1 < len(args) && args[i+1].literal:
			i++
		case w.single && !strings.HasPrefix(w.text, "-"):
			// a URL
		default:
			return w.form(), false
		}
	}
	return "", true
}

// isDeadline answers whether stmt is deadline=$(( $(date +%s) + <seconds> )), alone.
func isDeadline(stmt *syntax.Stmt) bool {
	if stmt == nil || stmt.Negated || stmt.Background || len(stmt.Redirs) > 0 {
		return false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) > 0 || len(call.Assigns) != 1 {
		return false
	}
	a := call.Assigns[0]
	if a.Name == nil || a.Name.Value != "deadline" || a.Value == nil || len(a.Value.Parts) != 1 {
		return false
	}
	arithmetic, ok := a.Value.Parts[0].(*syntax.ArithmExp)
	if !ok {
		return false
	}
	sum, ok := arithmetic.X.(*syntax.BinaryArithm)
	if !ok || sum.Op != syntax.Add {
		return false
	}
	now, ok := sum.X.(*syntax.Word)
	if !ok || len(now.Parts) != 1 {
		return false
	}
	date, ok := now.Parts[0].(*syntax.CmdSubst)
	if !ok || len(date.Stmts) != 1 || !callIs(date.Stmts[0], "date", "+%s") {
		return false
	}
	seconds, ok := sum.Y.(*syntax.Word)
	return ok && digits.MatchString(seconds.Lit())
}

// failsByName answers whether stmts, a block's, print an ::error:: line first and end with
// exit 1.
func failsByName(stmts []*syntax.Stmt) bool {
	if len(stmts) < 2 || !callIs(stmts[len(stmts)-1], "exit", "1") {
		return false
	}
	echo, ok := stmts[0].Cmd.(*syntax.CallExpr)
	return ok && len(echo.Args) == 2 && readWord(echo.Args[0]).is("echo") && strings.HasPrefix(readWord(echo.Args[1]).text, "::error::")
}

// endsWait answers whether stmt is the statement that ends a wait of the steps by name once its
// deadline passes: [ "$(date +%s)" -lt "$deadline" ] || { echo "::error::..."; ...; exit 1; }.
func endsWait(stmt *syntax.Stmt) bool {
	or, ok := stmt.Cmd.(*syntax.BinaryCmd)
	if !ok || or.Op != syntax.OrStmt || !callIs(or.X, "[", `"$(date +%s)"`, "-lt", `"$deadline"`, "]") {
		return false
	}
	block, ok := or.Y.Cmd.(*syntax.Block)
	return ok && failsByName(block.Stmts)
}

// endsOnTimeout answers whether s3 (decl) ends the step by name, with exit 1, on curl's timeout:
// if [ "$status" = 28 ]; then echo "::error::..."; ...; exit 1; fi.
func endsOnTimeout(decl *syntax.FuncDecl) bool {
	found := false
	if decl != nil {
		syntax.Walk(decl, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.IfClause); ok && len(c.Cond) == 1 && callIs(c.Cond[0], "[", `"$status"`, "=", "28", "]") && failsByName(c.Then) {
				found = true
			}
			return !found
		})
	}
	return found
}

// stepFindings answers what the run: text (run) of the shard job's step name breaks of the rules
// above for the workflow's store steps.
func stepFindings(name, run string) []string {
	var findings []string
	lines := strings.Split(run, "\n")
	finding := func(line int, what string) {
		shown := ""
		if line >= 1 && line <= len(lines) {
			shown = strings.TrimSpace(lines[line-1])
		}
		findings = append(findings, fmt.Sprintf("dispatch-browser.yaml, step %q, line %d: %s: %s", name, line, what, shown))
	}
	read := readShell(run)
	for _, refused := range read.refused {
		finding(refused.line, refused.what)
	}
	selfRunning := read.selfRunning()
	for _, function := range slices.Sorted(maps.Keys(selfRunning)) {
		finding(int(read.functions[function].Pos().Line()), "a function that runs itself again ("+selfRunning[function]+"), so nothing but a bound ends it")
	}
	for _, loop := range read.loops {
		if loop.kind != "until" {
			finding(loop.line, "a loop that is not a wait with a deadline (an until right after its deadline)")
			continue
		}
		if !isDeadline(loop.before) {
			finding(loop.line, "a wait with no deadline=$(( $(date +%s) + <seconds> )) just before it, in the same list of statements")
		}
		if !slices.ContainsFunc(loop.body, endsWait) {
			finding(loop.line, "a wait that does not end the step by name, with exit 1, once its deadline passes")
		}
	}
	for _, a := range read.assigns {
		if a.name == "seconds" && (a.function != "s3" || a.value.source != "$1") {
			finding(a.line, "s3 takes its seconds from other than its first argument, so no call's seconds say its bound")
		}
	}
	for _, c := range read.commands {
		if !c.name.literal {
			finding(c.line, "runs "+c.name.source+", which is not in stepCommands, so it may ask the store or wait with no bound")
			continue
		}
		switch command := c.name.text; {
		case command == "s3" && read.functions[command] != nil:
			if len(c.args) == 0 || !c.args[0].literal || !wholeSeconds.MatchString(c.args[0].text) {
				finding(c.line, "an s3 call whose seconds are not a whole number above 0, while curl's --max-time 0 is no limit")
			}
			if word, ok := s3CurlWord(c.args); !ok {
				finding(c.line, "an s3 call that gives curl "+word+", which is none of -o, -X, --data-binary and a URL, so it may turn the bound off")
			}
		case read.functions[command] != nil:
		case !stepCommands[command]:
			finding(c.line, "runs "+command+", which is not in stepCommands, so it may ask the store or wait with no bound")
		case command == "curl" && (c.function != "s3" || !s3Curl(c.args)):
			finding(c.line, `a curl outside s3, or other than s3's own curl -sf --max-time "$seconds" "$@", so with no bound or one turned off`)
		case command == "nohup" && (name != storeSteps[0] || !c.background || len(c.args) < 2 || !c.args[0].is("weed") || !c.args[1].is("server")):
			finding(c.line, "a nohup that is not `nohup weed server ... &` in the step that starts the store, so it may hold the step")
		case command == "sleep" && !c.waiting:
			finding(c.line, "a sleep outside a wait with a deadline")
		case command == "sleep" && (len(c.args) != 1 || !c.args[0].is("1")):
			finding(c.line, "a sleep in a wait other than its sleep 1, which holds the wait past its deadline")
		}
	}
	if name == waitStep && !endsOnTimeout(read.functions["s3"]) {
		findings = append(findings, fmt.Sprintf("dispatch-browser.yaml, step %q: s3 does not end the step by name, with exit 1, on curl's timeout (exit 28)", name))
	}
	return findings
}
