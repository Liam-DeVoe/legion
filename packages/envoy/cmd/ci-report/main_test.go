package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
)

// stubDispatch is the stand-in for the dispatch command that the tests hand ci-report through
// --dispatch-cli. It is this test's own program, not the real command. Each call writes its
// arguments, NUL-separated, to <n>.args, its standard input to <n>.stdin and the value of
// AGENT_SECRETS_MACHINE_IDENTITY to <n>.identity, in the directory CI_REPORT_STUB_DIR names, n
// counting from 0. With CI_REPORT_STUB_MODE=refuse it writes the broker's refusal of an exchange
// on standard error and exits 1, as the real command does when the broker refuses the job's token.
// Otherwise it prints a stored row as the real command prints it, whole and over many lines, with
// the id row-<check> and the address of a person in predicate.recorder.person, and exits 0.
const stubDispatch = `#!/bin/sh
calls="$CI_REPORT_STUB_DIR"
n=0
while [ -e "$calls/$n.args" ]; do n=$((n + 1)); done
printf '%s\0' "$@" > "$calls/$n.args"
cat > "$calls/$n.stdin"
printf '%s' "${AGENT_SECRETS_MACHINE_IDENTITY-unset}" > "$calls/$n.identity"
check=
while [ $# -gt 0 ]; do
  if [ "$1" = --check ]; then check=$2; fi
  shift
done
if [ "$CI_REPORT_STUB_MODE" = refuse ]; then
  echo "` + brokerRefusal + `" >&2
  exit 1
fi
cat <<EOF
{
  "id": "row-$check",
  "seq": $n,
  "kind": "run",
  "attestation": {
    "_type": "https://in-toto.io/Statement/v1",
    "predicate": {
      "project": "ACME",
      "check": "$check",
      "recorder": {
        "session": "ci-results:example-org/example-repo/.github/workflows/ci-results.yaml",
        "principal": "workflow-results:example-org/example-repo/.github/workflows/ci-results.yaml",
        "person": "` + person + `"
      }
    }
  }
}
EOF
`

// brokerRefusal is what the stub writes on standard error in refuse mode: the broker's refusal of
// an exchange for a ref that no result entry admits.
const brokerRefusal = "BROKER_EXCHANGE_REFUSED: the broker refused the exchange of the job's token: invalid_request: no result entry admits ref refs/heads/a/b/c/d"

// person is the address of a person that every row the stub prints holds.
const person = "ada@example.com"

// otherSHA is a commit that is not the checkout's, set as GITHUB_SHA.
const otherSHA = "0123456789abcdef0123456789abcdef01234567"

// line is one JSON line that ci-report prints, with exit null for a check with no call.
type line struct {
	Check    string `json:"check"`
	Test     string `json:"test"`
	Outcome  string `json:"outcome"`
	Exit     *int   `json:"exit"`
	ResultID string `json:"result_id"`
	Why      string `json:"why"`
}

// call is one call of the stub: its arguments, its standard input and its identity variable.
type call struct {
	args            []string
	stdin, identity string
}

// TestTheBuiltBinary runs ci-report as a process over a checkout of testdata/spec and the report
// of testdata/report.jsonl, with the stub as its dispatch command.
func TestTheBuiltBinary(t *testing.T) {
	t.Parallel()
	binary := cmdtest.Build(t, "ci-report")
	ran := readReport(t, "report.jsonl")
	output := func(pkg, test string) string {
		for _, o := range ran {
			if o.Package == "example.com/acme/probe/"+pkg && o.Test == test {
				return o.Output
			}
		}
		t.Fatalf("the report holds no run of %s %s", pkg, test)
		return ""
	}
	report, err := filepath.Abs(filepath.Join("testdata", "report.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		notRun    = "the test did not run at this commit"
		notEnded  = "the test did not end: go test stopped it at its time limit, or the job ended first"
		bunRunner = `the test did not run at this commit: specgraph: selector is not <import path> <TopLevelTestName> for runner go: runner "bun"`
	)
	// lines is every line ci-report prints for testdata/spec at a run where each call stored its
	// result, with each recorded check's exit and result id given.
	lines := func(exits map[string]*int, ids map[string]string, whys map[string]string) []line {
		all := []line{
			{Check: "a-read-of-many-claims-ends-in-time", Test: "example.com/acme/probe/slow TestTheSlowReadEnds", Outcome: "could_not_run", Why: notEnded},
			{Check: "the-build-reads-the-claim", Test: "example.com/acme/probe/broken TestTheBuildReadsTheClaim", Outcome: "absent", Why: notRun},
			{Check: "the-claim-has-one-name", Test: "example.com/acme/probe/twice TestTheNameRunsTwice", Outcome: "two_tests", Why: "the report holds 2 runs of the test, and a result is the result of one test"},
			{Check: "the-claim-stays-after-a-restart", Test: "example.com/acme/probe/claims TestTheClaimStaysAfterARestart", Outcome: "pass"},
			{Check: "the-daemon-waits-for-the-database", Test: "example.com/acme/probe/claims TestTheWaitIsSkipped", Outcome: "skip", Why: "the test skipped at this commit"},
			{Check: "the-lock-stays-after-a-restart", Test: "example.com/acme/probe/claims TestTheLockStaysAfterARestart", Outcome: "absent", Why: notRun},
			{Check: "the-owner-stays-after-a-restart", Test: "example.com/acme/probe/claims TestTheOwnerStaysAfterARestart", Outcome: "fail"},
			{Check: "the-page-shows-the-claim", Test: "packages/web/src/claims.test.ts", Outcome: "absent", Why: bunRunner},
		}
		for i := range all {
			if code, ok := exits[all[i].Check]; ok {
				all[i].Exit, all[i].ResultID = code, ids[all[i].Check]
				if why, ok := whys[all[i].Check]; ok {
					all[i].Why = why
				}
			}
		}
		return all
	}
	recordedChecks := []string{"a-read-of-many-claims-ends-in-time", "the-claim-stays-after-a-restart", "the-owner-stays-after-a-restart"}
	// wantCalls is the one call for each test that ran: pass, fail and could not run, in the
	// tree's order, each with the test's output on standard input.
	wantCalls := func(commit string) []call {
		args := func(check, verdict string) []string {
			return []string{"result", "--project", "ACME", "--check", check, "--verdict", verdict, "--commit", commit, "--observed-file", "-"}
		}
		return []call{
			{args: args("a-read-of-many-claims-ends-in-time", "could_not_run"), stdin: output("slow", "TestTheSlowReadEnds"), identity: "unset"},
			{args: args("the-claim-stays-after-a-restart", "pass"), stdin: output("claims", "TestTheClaimStaysAfterARestart"), identity: "unset"},
			{args: args("the-owner-stays-after-a-restart", "fail"), stdin: output("claims", "TestTheOwnerStaysAfterARestart"), identity: "unset"},
		}
	}

	t.Run("each test that ran writes one result, at the commit git names, and no other check is called", func(t *testing.T) {
		t.Parallel()
		dir, commit := checkout(t, filepath.Join("testdata", "spec"))
		stub := newStub(t)
		got := runReporter(t, binary, dir, stub.env("store"), "--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME", "--dispatch-cli", stub.path)
		if got.code != 0 {
			t.Fatalf("exit %d, want 0; stderr:\n%s", got.code, got.stderr)
		}
		ids, exits := map[string]string{}, map[string]*int{}
		for _, check := range recordedChecks {
			ids[check], exits[check] = "row-"+check, new(0)
		}
		expectLines(t, got.stdout, lines(exits, ids, nil))
		if calls := stub.calls(t); !slices.EqualFunc(calls, wantCalls(commit), equalCall) {
			t.Errorf("the stub got %+v\nwant %+v", calls, wantCalls(commit))
		}
		if want := fmt.Sprintf("ci-report: the commit is %s, which git rev-parse HEAD names; GITHUB_SHA is %s\n", commit, otherSHA); !strings.Contains(got.stderr, want) {
			t.Errorf("stderr holds no line %q:\n%s", want, got.stderr)
		}
		if want := fmt.Sprintf("ci-report: 8 checks of %s at %s name job %s; 3 results written, 0 calls failed\n", filepath.Join(dir, ".dispatch", "spec"), commit, job); !strings.HasSuffix(got.stderr, want) {
			t.Errorf("stderr does not end with %q:\n%s", want, got.stderr)
		}
	})

	t.Run("no line holds the address of a person from the stored row, only the row's id", func(t *testing.T) {
		t.Parallel()
		dir, _ := checkout(t, filepath.Join("testdata", "spec"))
		stub := newStub(t)
		got := runReporter(t, binary, dir, stub.env("store"), "--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME", "--dispatch-cli", stub.path)
		if len(stub.calls(t)) == 0 {
			t.Fatal("the stub got no call, so no row reached the command")
		}
		for name, stream := range map[string]string{"stdout": got.stdout, "stderr": got.stderr} {
			for _, text := range strings.SplitAfter(stream, "\n") {
				if strings.Contains(text, person) {
					t.Errorf("a line of %s holds %s: %q", name, person, text)
				}
			}
		}
		for _, l := range parseLines(t, got.stdout) {
			if slices.Contains(recordedChecks, l.Check) && l.ResultID != "row-"+l.Check {
				t.Errorf("%s: result_id %q, want the row's id %q", l.Check, l.ResultID, "row-"+l.Check)
			}
		}
	})

	t.Run("a refused exchange prints the broker's words on each recorded check and exits 1", func(t *testing.T) {
		t.Parallel()
		dir, commit := checkout(t, filepath.Join("testdata", "spec"))
		stub := newStub(t)
		got := runReporter(t, binary, dir, stub.env("refuse"), "--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME", "--dispatch-cli", stub.path)
		if got.code != 1 {
			t.Errorf("exit %d, want 1; stderr:\n%s", got.code, got.stderr)
		}
		exits, whys := map[string]*int{}, map[string]string{}
		for _, check := range recordedChecks {
			exits[check], whys[check] = new(1), brokerRefusal
		}
		expectLines(t, got.stdout, lines(exits, nil, whys))
		if calls := stub.calls(t); !slices.EqualFunc(calls, wantCalls(commit), equalCall) {
			t.Errorf("the stub got %+v\nwant %+v", calls, wantCalls(commit))
		}
		if want := "; 0 results written, 3 calls failed\n"; !strings.HasSuffix(got.stderr, want) {
			t.Errorf("stderr does not end with %q:\n%s", want, got.stderr)
		}
	})

	t.Run("a dispatch command that cannot start fails each recorded check and exits 1", func(t *testing.T) {
		t.Parallel()
		dir, _ := checkout(t, filepath.Join("testdata", "spec"))
		missing := filepath.Join(t.TempDir(), "no-dispatch")
		got := runReporter(t, binary, dir, nil, "--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME", "--dispatch-cli", missing)
		if got.code != 1 {
			t.Errorf("exit %d, want 1; stderr:\n%s", got.code, got.stderr)
		}
		for _, l := range parseLines(t, got.stdout) {
			if !slices.Contains(recordedChecks, l.Check) {
				continue
			}
			if l.Exit == nil || *l.Exit != -1 || l.ResultID != "" || !strings.Contains(l.Why, missing) {
				t.Errorf("%s: exit %v, result_id %q, why %q; want exit -1, no id and a why naming %s", l.Check, l.Exit, l.ResultID, l.Why, missing)
			}
		}
	})

	t.Run("a refused tree writes no result and exits 2", func(t *testing.T) {
		t.Parallel()
		spec := copyOf(t, filepath.Join("testdata", "spec"))
		writeFile(t, filepath.Join(spec, "lonely.md"), ":::requirement{#a-lonely-statement kind=\"behavior\"}\nThe daemon keeps one claim.\n:::\n")
		dir, _ := checkout(t, spec)
		stub := newStub(t)
		got := runReporter(t, binary, dir, stub.env("store"), "--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME", "--dispatch-cli", stub.path)
		if got.code != 2 || got.stdout != "" {
			t.Errorf("exit %d, stdout %q; want exit 2 and no line", got.code, got.stdout)
		}
		problem := "ci-report: " + filepath.Join(dir, ".dispatch", "spec", "lonely.md") + ":1: a-lonely-statement: leaf-without-check: no statement is under it and no check serves it\n"
		if !strings.Contains(got.stderr, problem) {
			t.Errorf("stderr holds no line %q:\n%s", problem, got.stderr)
		}
		if calls := stub.calls(t); len(calls) != 0 {
			t.Errorf("the stub got %d calls, want none", len(calls))
		}
	})

	t.Run("a report that holds no event writes no result and exits 2", func(t *testing.T) {
		t.Parallel()
		dir, _ := checkout(t, filepath.Join("testdata", "spec"))
		broken := filepath.Join(t.TempDir(), "report.jsonl")
		writeFile(t, broken, "ok  \texample.com/acme/probe/claims\t0.01s\n")
		stub := newStub(t)
		got := runReporter(t, binary, dir, stub.env("store"), "--dir", ".dispatch/spec", "--job", job, "--report", broken, "--project", "ACME", "--dispatch-cli", stub.path)
		if got.code != 2 || got.stdout != "" {
			t.Errorf("exit %d, stdout %q; want exit 2 and no line", got.code, got.stdout)
		}
		if !strings.Contains(got.stderr, "ci-report: "+broken+": specgraph: the go test report holds no event at byte") {
			t.Errorf("stderr names no unreadable report:\n%s", got.stderr)
		}
		if calls := stub.calls(t); len(calls) != 0 {
			t.Errorf("the stub got %d calls, want none", len(calls))
		}
	})

	t.Run("a checkout with no spec directory has no check, and says so", func(t *testing.T) {
		t.Parallel()
		dir, commit := checkout(t, "")
		stub := newStub(t)
		got := runReporter(t, binary, dir, stub.env("store"), "--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME", "--dispatch-cli", stub.path)
		spec := filepath.Join(dir, ".dispatch", "spec")
		if got.code != 0 || got.stdout != "" {
			t.Errorf("exit %d, stdout %q; want exit 0 and no line", got.code, got.stdout)
		}
		for _, want := range []string{
			"ci-report: " + spec + ": does not exist, so it is read as the empty tree\n",
			fmt.Sprintf("ci-report: 0 checks of %s at %s name job %s; 0 results written, 0 calls failed\n", spec, commit, job),
		} {
			if !strings.Contains(got.stderr, want) {
				t.Errorf("stderr holds no line %q:\n%s", want, got.stderr)
			}
		}
		if calls := stub.calls(t); len(calls) != 0 {
			t.Errorf("the stub got %d calls, want none", len(calls))
		}
	})

	t.Run("each required flag left out exits 2 with the usage", func(t *testing.T) {
		t.Parallel()
		all := []string{"--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME"}
		for i := 0; i < len(all); i += 2 {
			args := slices.Concat(all[:i], all[i+2:])
			got := runReporter(t, binary, t.TempDir(), nil, args...)
			want := "ci-report: " + all[i] + " is required\n" + usage + "\n"
			if got.code != 2 || got.stdout != "" || got.stderr != want {
				t.Errorf("without %s: exit %d, stdout %q, stderr %q; want exit 2 and stderr %q", all[i], got.code, got.stdout, got.stderr, want)
			}
		}
	})

	t.Run("a dispatch command of no word exits 2 with the usage", func(t *testing.T) {
		t.Parallel()
		got := runReporter(t, binary, t.TempDir(), nil, "--dir", ".dispatch/spec", "--job", job, "--report", report, "--project", "ACME", "--dispatch-cli", " ")
		if want := "ci-report: --dispatch-cli names no command\n" + usage + "\n"; got.code != 2 || got.stdout != "" || got.stderr != want {
			t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 and stderr %q", got.code, got.stdout, got.stderr, want)
		}
	})
}

// result is what one run of the binary did.
type result struct {
	code           int
	stdout, stderr string
}

// runReporter runs binary with args in dir, with this process's environment less any
// AGENT_SECRETS_MACHINE_IDENTITY, which the action sets and the command must not, GITHUB_SHA set
// to otherSHA, and env added.
func runReporter(t *testing.T, binary, dir string, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = slices.DeleteFunc(os.Environ(), func(variable string) bool {
		return strings.HasPrefix(variable, "AGENT_SECRETS_MACHINE_IDENTITY=")
	})
	cmd.Env = append(append(cmd.Env, "GITHUB_SHA="+otherSHA), env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitError *exec.ExitError
	if err != nil && !errors.As(err, &exitError) {
		t.Fatal(err)
	}
	return result{code: cmd.ProcessState.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
}

// stub is one copy of stubDispatch, with a directory of its own for its calls.
type stub struct {
	path, dir string
}

// newStub writes stubDispatch into a new directory, beside an empty directory for its calls.
func newStub(t *testing.T) stub {
	t.Helper()
	root := t.TempDir()
	s := stub{path: filepath.Join(root, "dispatch"), dir: filepath.Join(root, "calls")}
	if err := os.Mkdir(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte(stubDispatch), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// env is the environment that points ci-report's calls of the stub at its directory, in mode.
func (s stub) env(mode string) []string {
	return []string{"CI_REPORT_STUB_DIR=" + s.dir, "CI_REPORT_STUB_MODE=" + mode}
}

// calls is each call the stub got, in order.
func (s stub) calls(t *testing.T) []call {
	t.Helper()
	var calls []call
	for n := 0; ; n++ {
		args, err := os.ReadFile(filepath.Join(s.dir, fmt.Sprintf("%d.args", n)))
		if errors.Is(err, os.ErrNotExist) {
			return calls
		} else if err != nil {
			t.Fatal(err)
		}
		stdin, err := os.ReadFile(filepath.Join(s.dir, fmt.Sprintf("%d.stdin", n)))
		if err != nil {
			t.Fatal(err)
		}
		identity, err := os.ReadFile(filepath.Join(s.dir, fmt.Sprintf("%d.identity", n)))
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call{args: strings.Split(strings.TrimSuffix(string(args), "\x00"), "\x00"), stdin: string(stdin), identity: string(identity)})
	}
}

// equalCall reports whether two calls are the same.
func equalCall(a, b call) bool {
	return slices.Equal(a.args, b.args) && a.stdin == b.stdin && a.identity == b.identity
}

// parseLines reads stdout as one JSON line for each check, refusing any other text.
func parseLines(t *testing.T, stdout string) []line {
	t.Helper()
	var lines []line
	for _, text := range strings.SplitAfter(stdout, "\n") {
		if text == "" {
			continue
		}
		var l line
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&l); err != nil || !strings.HasSuffix(text, "\n") {
			t.Fatalf("stdout holds a line that is not one JSON line of a check: %q (%v)", text, err)
		}
		lines = append(lines, l)
	}
	return lines
}

// expectLines fails the test unless stdout is exactly want, one JSON line each.
func expectLines(t *testing.T, stdout string, want []line) {
	t.Helper()
	got := parseLines(t, stdout)
	if !slices.EqualFunc(got, want, func(a, b line) bool {
		return a.Check == b.Check && a.Test == b.Test && a.Outcome == b.Outcome && a.ResultID == b.ResultID && a.Why == b.Why &&
			(a.Exit == nil) == (b.Exit == nil) && (a.Exit == nil || *a.Exit == *b.Exit)
	}) {
		t.Errorf("stdout:\n%s\nwant the lines of %s", stdout, describe(want))
	}
}

// describe is lines as JSON, one a line, for a failure message.
func describe(lines []line) string {
	var b strings.Builder
	for _, l := range lines {
		encoded, _ := json.Marshal(l)
		b.Write(encoded)
		b.WriteByte('\n')
	}
	return b.String()
}

// checkout is a new git repository with one commit whose .dispatch/spec is a copy of spec (no
// such directory when spec is empty), and that commit.
func checkout(t *testing.T, spec string) (dir, commit string) {
	t.Helper()
	dir = t.TempDir()
	if spec != "" {
		if err := os.CopyFS(filepath.Join(dir, ".dispatch", "spec"), os.DirFS(spec)); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "the probe checks")
	return dir, strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
}

// git runs git with args in dir, with no configuration of this machine's, and answers its output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=ACME CI", "-c", "user.email=ci@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// copyOf is a copy of the files of dir in a new temporary directory.
func copyOf(t *testing.T, dir string) string {
	t.Helper()
	copied := filepath.Join(t.TempDir(), filepath.Base(dir))
	if err := os.CopyFS(copied, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	return copied
}

// writeFile writes content to path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
