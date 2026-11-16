// Command ci-report is the last step of a CI job: it writes one result to Dispatch for each check
// of the spec files whose test part names the job and whose test ran in the job's go test report.
// A check whose test did not run gets no result (R12: nobody writes a result for work that it did
// not do). The composite action .github/actions/dispatch-results runs it.
//
// Usage:
//
//	ci-report --dir <dir> --job <job> --report <file> --project <key> [--dispatch-cli <argv>]
//
// It reads the commit with `git rev-parse HEAD` in its working directory, the checkout, and never
// from GITHUB_SHA, which it prints beside it. It reads the spec directory as spec-check does
// (notes.ReadSet, then specgraph.Parse and specgraph.Validate), a directory that does not exist
// being the empty tree, and the report with specgraph.ReadGoTestJSON. planReports decides the
// outcome of each check of the job. For each pass, fail and could_not_run it runs
//
//	<dispatch-cli> result --project <key> --check <id> --verdict <outcome> --commit <sha> --observed-file -
//
// with what the test wrote on the command's standard input, so no text of a test reaches the
// argument list, where one that starts with "-" would read as a flag. <dispatch-cli> is split at
// white space, with no quoting, and is "dispatch" when the flag is left out. A skip, a test the
// report does not hold and a test that ran twice get no call.
//
// It prints one JSON line on standard output for each check of the job, in the tree's order:
// {"check","test","outcome","exit","result_id","why"}. exit is the command's exit status, -1 when
// it could not start or a signal ended it, and null for a check with no call. result_id is the id
// of the stored result, the one part of a stored call's output that it reads and prints: the
// stored row also holds predicate.recorder.person, the address of a person. why says why a check
// got no call, and holds what the command printed on both streams when a call failed, which stored
// nothing: the command prints a refusal on standard output. The line holds no raw line break, as
// encoding/json escapes each control character, and the "[" of each "##[" in it is written as
// \u005b, which a JSON reader reads back as "[", so the line starts no workflow command of a
// runner's log. Every other line goes to standard error through runnerlog.Say with the prefix
// "ci-report: ", ending with "<n> checks of <dir> at <sha> name job <job>; <n> results written,
// <n> calls failed".
//
// It exits 0 when each call stored its result, 1 when a call failed, and 2, with no call, for
// arguments it does not take, a commit it cannot read, a spec directory or a report it cannot
// read, and a tree with any problem.
package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/architecture/notes"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
	"github.com/sjawhar/envoy/internal/runnerlog"
)

const usage = `usage: ci-report --dir <dir> --job <job> --report <file> --project <key> [--dispatch-cli <argv>]`

// prefix is the fixed text that starts every line the command prints but the usage and the JSON
// lines.
const prefix = "ci-report: "

// commitSHA is the form of the commit a result is for, as Dispatch takes it: 40 lowercase hex.
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// checkLine is the JSON line printed for one check.
type checkLine struct {
	Check    string `json:"check"`
	Test     string `json:"test"`
	Outcome  string `json:"outcome"`
	Exit     *int   `json:"exit"`
	ResultID string `json:"result_id"`
	Why      string `json:"why"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the command with args, answering its exit status.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ci-report", flag.ContinueOnError)
	dirFlag := flags.String("dir", "", "the directory of spec files")
	job := flags.String("job", "", "the job whose checks to report")
	reportFlag := flags.String("report", "", "the job's go test -json report")
	project := flags.String("project", "", "the key of the Dispatch project the results are for")
	cliFlag := flags.String("dispatch-cli", "dispatch", "the dispatch command, split at white space")
	if code, ok := parse(flags, args, stderr, "dir", "job", "report", "project"); !ok {
		return code
	}
	cli := strings.Fields(*cliFlag)
	if len(cli) == 0 {
		say(stderr, "--dispatch-cli names no command")
		fmt.Fprintln(stderr, usage)
		return 2
	}
	dir, err := filepath.Abs(*dirFlag)
	if err != nil {
		say(stderr, "%s: cannot be read: %s", *dirFlag, err)
		return 2
	}
	commit, err := headCommit()
	if err != nil {
		say(stderr, "%s", err)
		return 2
	}
	githubSHA, set := os.LookupEnv("GITHUB_SHA")
	if !set {
		githubSHA = "not set"
	}
	say(stderr, "the commit is %s, which git rev-parse HEAD names; GITHUB_SHA is %s", commit, githubSHA)
	tree, ok := readTree(stderr, dir)
	if !ok {
		return 2
	}
	ran, err := loadReport(*reportFlag)
	if err != nil {
		say(stderr, "%s", err)
		return 2
	}

	planned := planReports(tree, *job, ran)
	written, failed := 0, 0
	for _, p := range planned {
		l := checkLine{Check: p.Check, Test: p.Test, Outcome: p.Outcome, Why: p.Why}
		if recorded(p.Outcome) {
			code, id, why := record(cli, *project, commit, p)
			l.Exit, l.ResultID = &code, id
			if why != "" {
				l.Why = why
				failed++
			} else {
				written++
			}
		}
		if err := writeLine(stdout, l); err != nil {
			say(stderr, "%s: %s", p.Check, err)
			return 2
		}
	}
	say(stderr, "%d checks of %s at %s name job %s; %d results written, %d calls failed", len(planned), dir, commit, *job, written, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// say writes one line through runnerlog.Say with the command's prefix.
func say(w io.Writer, format string, args ...any) {
	runnerlog.Say(w, prefix, format, args...)
}

// parse reads args into flags and answers whether the command goes on and, when it does not, its
// exit status: 0 for -h, and 2 for a flag it does not take, an argument besides the flags or a
// required flag left empty, each with one line naming the case, then the usage. The flag package
// would print a parse error with the argument in it as it is, so parse prints nothing through it
// and writes the error through runnerlog.Say.
func parse(flags *flag.FlagSet, args []string, stderr io.Writer, required ...string) (int, bool) {
	flags.SetOutput(io.Discard)
	err := flags.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(stderr, usage)
		return 0, false
	case err != nil:
		say(stderr, "%s", err)
	case flags.NArg() > 0:
		say(stderr, "takes no argument but its flags, got %q", flags.Arg(0))
	default:
		missing := slices.IndexFunc(required, func(name string) bool { return flags.Lookup(name).Value.String() == "" })
		if missing < 0 {
			return 0, true
		}
		say(stderr, "--%s is required", required[missing])
	}
	fmt.Fprintln(stderr, usage)
	return 2, false
}

// headCommit is the commit `git rev-parse HEAD` names in the working directory.
func headCommit() (string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	commit := strings.TrimSpace(string(out))
	if !commitSHA.MatchString(commit) {
		return "", fmt.Errorf("git rev-parse HEAD printed %q, not a commit of 40 lowercase hex characters", commit)
	}
	return commit, nil
}

// readTree reads dir as spec-check reads it, and answers its tree and whether it is one to report
// on: a valid tree, or the empty tree of a directory that does not exist. It warns of each entry
// that is not a spec file, and writes each problem of a refused tree, and why it could not read
// one, on stderr.
func readTree(stderr io.Writer, dir string) (specgraph.Tree, bool) {
	set, err := notes.ReadSet(os.DirFS(dir), dir)
	for _, entry := range set.Rejected {
		say(stderr, "warning: %s: not a spec file (%s); the importer passes over it", entry.Path, entry.Kind)
	}
	var refused *notes.SizeRefusal
	switch {
	case errors.Is(err, notes.ErrNoDirectory):
		// Git keeps no empty directory, so a checkout with no spec file has no directory, and
		// the importer reads a missing directory as the empty set: it has no check.
		say(stderr, "%s: does not exist, so it is read as the empty tree", dir)
		return specgraph.Tree{}, true
	case errors.As(err, &refused):
		for _, problem := range refused.Problems {
			say(stderr, "%s", problem)
		}
		say(stderr, "%s: the tree is refused, so no result is written", dir)
		return specgraph.Tree{}, false
	case err != nil:
		say(stderr, "%s: %s", dir, err)
		return specgraph.Tree{}, false
	}
	tree, problems := specgraph.Parse(set.Files)
	problems = append(problems, specgraph.Validate(tree, specgraph.Options{})...)
	if len(problems) == 0 {
		return tree, true
	}
	slices.SortFunc(problems, func(a, b specgraph.Problem) int {
		return cmp.Or(cmp.Compare(a.Pos.File, b.Pos.File), cmp.Compare(a.Pos.Line, b.Pos.Line), cmp.Compare(a.Node, b.Node),
			cmp.Compare(a.Rule, b.Rule), cmp.Compare(a.Detail, b.Detail))
	})
	for _, p := range problems {
		say(stderr, "%s:%d: %s: %s", filepath.Join(dir, p.Pos.File), p.Pos.Line, p.Node, p.Detail)
	}
	say(stderr, "%s: the tree is refused, so no result is written", dir)
	return specgraph.Tree{}, false
}

// loadReport is the go test -json report at path, as specgraph.ReadGoTestJSON reads it. A report
// that holds anything but events is an error, so an unreadable report never reads as a run of no
// tests.
func loadReport(path string) ([]specgraph.TestOutcome, error) {
	report, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer report.Close()
	ran, err := specgraph.ReadGoTestJSON(report)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ran, nil
}

// record runs the dispatch command cli once for the check of p, and answers its exit status, the
// id of the stored result, and why the call failed, "" when it stored the result. A call that
// failed stored nothing, and its words are what it wrote on both streams: the command prints a
// refusal of Dispatch or of the broker on standard output (`print` in
// packages/envoy-client/src/dispatch-cli.ts), and its warnings on standard error. Of a call that
// stored its result it reads only the stored row's id, and it never prints the rest.
func record(cli []string, project, commit string, p Planned) (code int, id, why string) {
	args := append(slices.Clone(cli[1:]), "result", "--project", project, "--check", p.Check, "--verdict", p.Outcome,
		"--commit", commit, "--observed-file", "-")
	cmd := exec.Command(cli[0], args...)
	cmd.Stdin = strings.NewReader(p.Observed)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitError *exec.ExitError
	switch {
	case err != nil && !errors.As(err, &exitError):
		return -1, "", err.Error()
	case cmd.ProcessState.ExitCode() != 0:
		words := strings.TrimSpace(strings.TrimSpace(stderr.String()) + "\n" + strings.TrimSpace(stdout.String()))
		if words == "" {
			words = fmt.Sprintf("the dispatch command ended with %s and printed nothing", cmd.ProcessState)
		}
		return cmd.ProcessState.ExitCode(), "", words
	}
	var row struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(&stdout).Decode(&row); err != nil || row.ID == "" {
		return 0, "", "the dispatch command exited 0 and printed no stored result with an id"
	}
	return 0, row.ID, ""
}

// writeLine writes l to w as one JSON line. encoding/json writes each control character of a
// value as an escape, so no line break reaches the log, and the line starts with "{", so it starts
// no workflow command; the "[" of each "##[", which can only be in a value, is written as \u005b.
func writeLine(w io.Writer, l checkLine) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(l); err != nil {
		return err
	}
	_, err := io.WriteString(w, strings.ReplaceAll(encoded.String(), "##[", `##\u005b`))
	return err
}
