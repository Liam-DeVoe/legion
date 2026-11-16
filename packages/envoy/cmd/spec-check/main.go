// Command spec-check judges a directory of spec files, a checkout's .dispatch/spec, by the rules
// of package specgraph, lists what changed between two such directories, compares one with an
// approved spec and prints the approved text, checks that no CI file of a repository runs that
// comparison, derives the states of a scenario, names each program check that CI refuses at a
// commit, and lists the files that changed between two commits. It reads the directory as the
// Dispatch server's importer reads .dispatch/architecture: notes.ReadSet takes each .md file
// directly in it, within the size bound, and passes over every other entry. Every rule is
// specgraph's (Parse, Validate, Compare, CIFilesThatCompare, ReadScenario, Derive, WaitCycle,
// GoTies, Citations and GitHistory); the command states none of its own.
//
// Usage:
//
//	spec-check tree --dir <dir> [--allow-empty] [--retired <file>]
//	spec-check changes --base <dir> --dir <dir>
//	spec-check checks --dir <dir> --job <job>
//	spec-check compare --dir <dir> --approved <approved.json>
//	spec-check print --approved <approved.json> --id <id> [--id <id>...]
//	spec-check guard --root <dir>
//	spec-check state --scenario <dir> [--json]
//	spec-check citations --dir <dir> --repo <root> --workflow <file> (--changed <file> | --all-changed)
//	    --reports <dir> --needs <file> [--ties <file>] [--json <file>] [--allow-empty]
//	spec-check changed --repo <root> --base <commit> [--head <commit>]
//
// tree judges one directory. It exits 0 for a valid tree, with the count line "spec-check: <dir>:
// 3 statements, 4 checks, 2 components, 1 interface, 1 term; the tree is valid" on standard output.
// It exits 1 for a refused tree, with one line for each problem on standard error, "spec-check:
// <dir>/<file>:<line>: <node>: <detail>", where the detail begins with the rule, and the count line
// ending "; the tree is refused". With --allow-empty, a directory that does not exist or holds no
// spec file is the empty tree, exit 0, ending "; the tree is empty"; without it, exit 2. --retired
// names a file of the ids the approved spec removed, one id a line, "#" lines and blank lines
// passed over; without it, a line on standard error says that no id was checked against them.
//
// changes prints one line for each statement that only one of the two directories holds, or whose
// text, parent, relations or checks differ between them, "spec-check: <id>: text, checks", or
// "spec-check: no statement changed", and exits 0; a statement that only moved to another file is
// no change. checks prints the program checks that name the job as one line of JSON,
// [{"id","runner","selector","job","procedure_sha256"}], [] when none, and exits 0. Both exit 2,
// with the problems on standard error, when a tree they read has any problem.
//
// compare compares the files of one directory with an approved spec in the form of approved.json
// (specgraph.ReadApproved). It exits 1 when the files hold a node in a text that the approved spec
// never held, with one line for each such node on standard error, "spec-check:
// <dir>/<file>:<line>: <node>: <detail>", and "spec-check: <dir>: the comparison fails on <n>
// nodes" on standard output. It exits 0 otherwise, with "spec-check: <dir>: every text the files
// hold is approved; 5 of 7 approved statements are in the files at their current text" and one
// line for each other approved statement, "spec-check: statement <id>: not in the files" or
// "spec-check: statement <id>: the files hold approved version 1, and version 2 is current". It
// exits 2, and makes no comparison, for files that do not parse or a directory that holds no spec
// file. The comparison does not judge the tree, which is tree's work. No CI job runs compare (R2):
// Dispatch makes the comparison at each push.
//
// print writes the current approved block of each node it is named, in the order named, and after
// a statement the block of each current check that serves it, each block once and the blocks
// separated by one blank line, so that a change copies them into the files. It exits 2, and
// prints nothing, when the approved spec does not hold an id or removed it.
//
// guard reads every regular file under <dir>/.github/workflows, <dir>/.github/actions,
// <dir>/.github/scripts and <dir>/scripts, and names each line that runs the comparison
// (specgraph.CIFilesThatCompare) on standard error, "spec-check: <dir>/<path>:<line>: this line
// runs the comparison of the files with the approved spec, which no CI job does (R2)", with
// "spec-check: <dir>: <n> lines of CI files run the comparison" on standard output, exit 1. It
// exits 1 with "spec-check: <dir>: read no workflow" when <dir>/.github/workflows holds no file,
// since an instrument that read nothing has not passed, and 0 otherwise, with "spec-check: <dir>:
// no line of <k> CI files runs the comparison".
//
// state reads a scenario of The Spec in the format of specgraph.ReadScenario (the directories of
// internal/dispatch/specgraph/testdata/scenarios) and derives the state of each check and each
// statement at each subject of it. For each subject it prints "spec-check: subject <commit>, the
// head of <branch>" or "spec-check: subject <commit>, the commit that production runs at deploy
// <deploy>", then each current approved statement in preorder, "statement <id>: met", "met, partly
// checked", "not built (…)", "not met (…)" or "unknown (…)", each followed by its own checks that
// read the subject, "  check <id>: true (passed, counted from <commit>)", "false (failed, counted
// from <commit>)" or "unknown (<reason>)". With units.json, at the head of main, it prints "wait of
// <unit> on <statement>: ends" or "holds (<own state>)" for each wait, and after the last subject
// "spec-check: cycle of waits: <unit> -> <statement> -> … -> <unit>" or "spec-check: cycle of
// waits: none". --json prints the states as one line of JSON instead, []State, or with units.json
// {"states":[…],"cycle":[…]}. It exits 0 for a scenario it reads, and 2, with one line on standard
// error naming the file and the fault, for one it does not: a file or a field that the format does
// not hold, a missing file, or a spec file that does not parse.
//
// citations names each program check of a directory of spec files that CI refuses at the commit of
// the repository at <root> (specgraph.Citations, R48): its test is not in the code (GoCode, go list
// in <root>), the code cannot be searched for it, its selector picks two tests, its job is not in
// the workflow or spec-citations does not wait for it, or code that its test reads changed and the
// job did not run the test. --changed names the paths that changed since the base commit, in the
// form changed prints; --all-changed says that there is no base commit. --reports holds one
// directory per report artifact of the run, spec-report--<job> or spec-report--<job>--<leg>, each
// with the go test -json report.jsonl; --needs holds toJSON(needs) of the spec-citations job, the
// result of each job it waits for. The tie of each check is what <root>'s
// .github/scripts/go-test-reads.sh says its test reads (GoTies), or with --ties the ties of a file in
// the form of a scenario's ties.json (ReadTies). Each refused check is one line on standard error,
// "spec-check: <dir>/<file>:<line>: <check>: <detail>", where the detail begins with the rule, with
// "spec-check: <dir>: <n> program checks; <k> refused" on standard output, exit 1; none is "spec-check:
// <dir>: <n> program checks; each is cited", exit 0. --json writes the faults to a file as one line of
// JSON, [{"check","rule","detail"}], [] for none, making its directory. With --allow-empty, a
// directory that does not exist or holds no spec file is "spec-check: <dir>: holds no spec file; no
// check to cite", exit 0. A spec file that does not parse is exit 2 with its problems.
//
// changed prints the paths whose content differs between the trees of two commits of the git
// clone at <root>, both paths of a move (GitHistory), sorted, one a line on standard output, with
// "spec-check: <root>: <n> paths changed from <base> to <head>" on standard error; --head is HEAD
// when not given. A commit that git cannot read and a path that holds a line break are exit 2 with
// one line naming them.
//
// Every subcommand exits 2 for a directory it cannot read, a file it cannot read or arguments it
// does not take, with one line naming the case, or the usage. Each entry of a directory that is
// not a spec file is a warning on standard error, since the importer passes over it in silence.
// Each path of a file or a directory the command reads is printed absolute. Every line but the
// usage, the JSON of checks and of state, the blocks of print, the statement, check and wait lines
// of state, and the paths of changed is written by runnerlog.Say
// with the prefix "spec-check: ", so no text a file name, a file or an argument holds starts a line
// of the runner's log or names a workflow command. Each statement, check and wait line starts with
// its fixed word and passes through runnerlog.Printable for the same reason. A JSON line holds no
// raw line break, as encoding/json escapes each control character, and checks and state write the
// "[" of each "##[" in it as \u005b, which a JSON reader reads back as "[". print writes each block
// as the approved spec holds it, since a change copies it byte for byte; no CI job runs print.
// changed writes each path as git gives it, the data of a file, which the citations job writes its
// standard output to.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/sjawhar/envoy/internal/dispatch/architecture/notes"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
	"github.com/sjawhar/envoy/internal/runnerlog"
)

const usage = `usage: spec-check tree --dir <dir> [--allow-empty] [--retired <file>]
       spec-check changes --base <dir> --dir <dir>
       spec-check checks --dir <dir> --job <job>
       spec-check compare --dir <dir> --approved <approved.json>
       spec-check print --approved <approved.json> --id <id> [--id <id>...]
       spec-check guard --root <dir>
       spec-check state --scenario <dir> [--json]
       spec-check citations --dir <dir> --repo <root> --workflow <file> (--changed <file> | --all-changed)
           --reports <dir> --needs <file> [--ties <file>] [--json <file>] [--allow-empty]
       spec-check changed --repo <root> --base <commit> [--head <commit>]`

// prefix is the fixed text that starts every line the command prints but the usage, the JSON of
// checks and of state, the blocks of print, the statement, check and wait lines of state, and the
// paths of changed.
const prefix = "spec-check: "

// subcommands is each subcommand by its name: it takes the arguments after the name and answers
// the exit status.
var subcommands = map[string]func(args []string, stdout, stderr io.Writer) int{
	"tree":      runTree,
	"changes":   runChanges,
	"checks":    runChecks,
	"compare":   runCompare,
	"print":     runPrint,
	"guard":     runGuard,
	"state":     runState,
	"citations": runCitations,
	"changed":   runChanged,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run runs the subcommand args name and answers its exit status, or 2 with the usage for no
// subcommand or one it does not know.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	subcommand, ok := subcommands[args[0]]
	if !ok {
		say(stderr, "unknown subcommand %q", args[0])
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return subcommand(args[1:], stdout, stderr)
}

// say writes one line through runnerlog.Say with the command's prefix.
func say(w io.Writer, format string, args ...any) {
	runnerlog.Say(w, prefix, format, args...)
}

// counts is the count line's "3 statements, 4 checks, 2 components, 1 interface, 1 term": the
// blocks the tree holds, an interface being a component with kind="interface", in the singular
// for one.
func counts(statements, checks, components, interfaces, terms int) string {
	return fmt.Sprintf("%s, %s, %s, %s, %s", count(statements, "statement"), count(checks, "check"),
		count(components, "component"), count(interfaces, "interface"), count(terms, "term"))
}

// count is "<n> <noun>s", with the noun alone for one.
func count(n int, noun string) string {
	return fmt.Sprintf("%d %s", n, plural(n, noun))
}

// plural is "<noun>s", or the noun alone for one.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// parse reads args into flags, the flags of one subcommand, and answers whether the subcommand
// goes on and, when it does not, its exit status: 0 for -h, and 2 for a flag it does not take, an
// argument besides the flags or a required flag left empty, each with one line naming the case,
// then the usage. The flag package would print a parse error with the argument in it as it is, so
// parse prints nothing through it and writes the error through runnerlog.Say.
func parse(flags *flag.FlagSet, args []string, stderr io.Writer, required ...string) (int, bool) {
	flags.SetOutput(io.Discard)
	err := flags.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(stderr, usage)
		return 0, false
	case err != nil:
		say(stderr, "%s: %s", flags.Name(), err)
	case flags.NArg() > 0:
		say(stderr, "%s: takes no argument but its flags, got %q", flags.Name(), flags.Arg(0))
	default:
		missing := slices.IndexFunc(required, func(name string) bool { return flags.Lookup(name).Value.String() == "" })
		if missing < 0 {
			return 0, true
		}
		say(stderr, "%s: --%s is required", flags.Name(), required[missing])
	}
	fmt.Fprintln(stderr, usage)
	return 2, false
}

// absolute is path made absolute, as every path the command prints is, and whether it could be;
// when not, it has written the line that names the path.
func absolute(stderr io.Writer, path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		say(stderr, "%s: cannot be read: %s", path, err)
		return "", false
	}
	return abs, true
}

// isDirectory is whether path is a directory; when not, it has written the line that says why.
func isDirectory(stderr io.Writer, path string) bool {
	switch info, err := os.Stat(path); {
	case errors.Is(err, fs.ErrNotExist):
		say(stderr, "%s: does not exist", path)
	case err != nil:
		say(stderr, "%s: cannot be read: %s", path, err)
	case !info.IsDir():
		say(stderr, "%s: is not a directory", path)
	default:
		return true
	}
	return false
}

// judged is one directory of spec files, read and judged: its tree, every problem of Parse and,
// for judge, of Validate in one list, sorted by file, line, node, rule and detail, and how many
// files it held.
type judged struct {
	dir      string
	tree     specgraph.Tree
	problems []specgraph.Problem
	files    int
}

// judge reads and parses dir as read does, and validates the tree with o.
func judge(stderr io.Writer, dir string, o specgraph.Options) (judged, error) {
	j, err := read(stderr, dir)
	if err != nil {
		return j, err
	}
	j.problems = append(j.problems, specgraph.Validate(j.tree, o)...)
	specgraph.SortProblems(j.problems)
	return j, nil
}

// read reads dir with notes.ReadSet, warns on stderr of each entry that is not a spec file, and
// parses the files it read. The error is ReadSet's: notes.ErrNoDirectory for a directory that
// does not exist, a *notes.SizeRefusal for a file above the bound, and an error naming the path
// for any other failure to read.
func read(stderr io.Writer, dir string) (judged, error) {
	set, err := notes.ReadSet(os.DirFS(dir), dir)
	for _, entry := range set.Rejected {
		say(stderr, "warning: %s: not a spec file (%s); the importer passes over it", entry.Path, entry.Kind)
	}
	if err != nil {
		return judged{dir: dir}, err
	}
	tree, problems := specgraph.Parse(set.Files)
	specgraph.SortProblems(problems)
	return judged{dir: dir, tree: tree, problems: problems, files: len(set.Files)}, nil
}

// sayProblems writes one line of stderr for each problem: "<dir>/<file>:<line>: <node>: <detail>".
func (j judged) sayProblems(stderr io.Writer) {
	for _, p := range j.problems {
		say(stderr, "%s:%d: %s: %s", filepath.Join(j.dir, p.Pos.File), p.Pos.Line, p.Node, p.Detail)
	}
}

// counts is the count line's counts of the blocks the tree holds.
func (j judged) counts() string {
	interfaces := 0
	for _, c := range j.tree.Components {
		if c.Interface {
			interfaces++
		}
	}
	return counts(len(j.tree.Statements), len(j.tree.Checks), len(j.tree.Components)-interfaces, interfaces, len(j.tree.Terms))
}

// cannotJudge writes why judge could not read dir, one line for each file a size refusal names
// and one line naming the directory for any other error, and answers exit 2.
func cannotJudge(stderr io.Writer, dir string, err error) int {
	var refused *notes.SizeRefusal
	if errors.As(err, &refused) {
		for _, problem := range refused.Problems {
			say(stderr, "%s", problem)
		}
		return 2
	}
	say(stderr, "%s: %s", dir, err)
	return 2
}
