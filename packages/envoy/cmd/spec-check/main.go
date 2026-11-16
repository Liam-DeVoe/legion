// Command spec-check judges a directory of spec files, a checkout's .dispatch/spec, by the rules
// of package specgraph, lists what changed between two such directories, compares one with an
// approved spec and prints the approved text, and checks that no CI file of a repository runs that
// comparison. It reads the directory as the Dispatch server's importer reads
// .dispatch/architecture: notes.ReadSet takes each .md file directly in it, within the size
// bound, and passes over every other entry. Every rule is specgraph's (Parse, Validate, Compare and
// CIFilesThatCompare); the command states none of its own.
//
// Usage:
//
//	spec-check tree --dir <dir> [--allow-empty] [--retired <file>]
//	spec-check changes --base <dir> --dir <dir>
//	spec-check checks --dir <dir> --job <job>
//	spec-check compare --dir <dir> --approved <approved.json> [--allow-empty]
//	spec-check print --approved <approved.json> --id <id> [--id <id>...]
//	spec-check guard --root <dir>
//
// tree judges one directory. It exits 0 for a valid tree, with the count line "spec-check: <dir>:
// 3 statements, 4 checks, 2 components, 1 interface, 1 term; the tree is valid" on standard output.
// It exits 1 for a refused tree, with one line for each problem on standard error, "spec-check:
// <dir>/<file>:<line>: <node>: <detail>", where the detail begins with the rule, and the count line
// ending "; the tree is refused". A file above the size bound gives the line "spec-check: file
// too large: <dir>/<file> is <n> bytes (limit 1048576)" in place of the problems, and the counts
// are 0: no file is parsed. With --allow-empty, a directory that does not exist or holds no
// spec file is the empty tree, exit 0, ending "; the tree is empty"; without it, exit 2. --retired
// names a file of the ids the approved spec removed, one id a line, "#" lines and blank lines
// passed over; without it, a line on standard error says that no id was checked against them.
//
// changes prints one line for each statement that only one of the two directories holds, or whose
// text, parent, relations or checks differ between them, "spec-check: <id>: text, checks", or
// "spec-check: no statement changed", and exits 0; a statement that only moved to another file is
// no change. checks prints the program checks that name the job as one line of JSON,
// [{"id","runner","selector","job","procedure_sha256"}], [] when none, and exits 0. Both exit 2,
// with the problems on standard error, when a tree they read has any problem. Both read a
// directory that does not exist as the empty tree, with a line on standard error that says so,
// since git keeps no empty directory: the first change that adds a spec file lists each of its
// statements as added, and a checkout with no spec file has no program check.
//
// compare compares the files of one directory with an approved spec in the form of approved.json
// (specgraph.ReadApproved). It exits 1 when the files hold a node in a text that the approved spec
// never held, with one line for each such node on standard error, "spec-check:
// <dir>/<file>:<line>: <node>: <detail>", and "spec-check: <dir>: the comparison fails on <n>
// nodes" on standard output. It exits 0 otherwise, with "spec-check: <dir>: the approved spec held
// every text that the files hold; 5 of 7 approved statements are in the files at their current
// text", which counts statements only, the verb and its pronoun in the singular for 1 held and
// the noun for 1 of 1. Then come one line for each other current approved node, of each kind,
// "spec-check: <kind> <id>: not in the files" or "spec-check: <kind> <id>: the files hold approved
// version 1, and version 2 is current", and one line for each node that the files hold and the
// approved spec removed, "spec-check: <kind> <id>: the files hold it, and the approved spec removed
// it" (R28). The kind is statement, check, component, interface or term. It exits 2, and makes no
// comparison, for files that do not parse. A directory that does not exist or holds no spec file
// is exit 2 too, with a line that names --allow-empty. With --allow-empty, compare reads it as the
// empty tree, with a line on standard error that says so, since git keeps no empty directory and a
// revert of the last spec file leaves none. The comparison does not judge the tree, which is tree's
// work. R2 gives the comparison at each push to Dispatch, and no CI job runs compare. So compare
// exits 2, before it reads an argument, when GITHUB_ACTIONS is "true", with the one line
// "spec-check: compare does not run in a CI job (GITHUB_ACTIONS=true): R2 gives the comparison of
// the files with the approved spec at each push to Dispatch". GitHub sets GITHUB_ACTIONS=true in
// each step of each job. CI=true alone, as an agent's shell sets it, is no CI job, and compare runs
// there. The refusal covers each route that passes the runner's environment on to the command. A
// route that drops it, such as sudo or a container that docker run starts, does not pass
// GITHUB_ACTIONS, and the command compares there: only guard names such a line, in the files that
// it reads.
//
// print writes the current approved block of each node it is named, in the order named, and after
// a statement the block of each current check that serves it, each block once and the blocks
// separated by one blank line, so that a change copies them into the files. It exits 2, and
// prints nothing, when the approved spec does not hold an id or removed it.
//
// guard reads every regular file under <dir>/.github/workflows, <dir>/.github/actions,
// <dir>/.github/scripts and <dir>/scripts, and names each line that runs the comparison
// (specgraph.CIFilesThatCompare: each line as the shell reads it, and each run: value of a YAML
// file as YAML reads it) on standard error, "spec-check: <dir>/<path>:<line>: this line runs the
// comparison of the files with the approved spec, which no CI job does (R2)", with "spec-check:
// <dir>: <n> lines of CI files run the comparison" on standard output, exit 1. It exits 1 with
// "spec-check: <dir>: read no workflow" when <dir>/.github/workflows holds no file, since an
// instrument that read nothing has not passed, and 0 otherwise, with "spec-check: <dir>: no line
// of <k> CI files runs the comparison". A step can run the comparison from a file it does not
// read, such as a package script or a make target: compare's refusal covers that route when the
// step passes the runner's environment on, and nothing covers it when the step does not.
//
// Every subcommand exits 2 for a directory it cannot read, a file it cannot read or arguments it
// does not take, with one line naming the case, or the usage. Each entry of a directory that is
// not a spec file is a warning on standard error, since the importer passes over it in silence.
// Paths are printed absolute. Every line but the usage, the JSON of checks and the blocks of print
// is written by runnerlog.Say with the prefix "spec-check: ", so no text a file name, a file or an
// argument holds starts a line of the runner's log or names a workflow command. The JSON line holds
// no raw line break, as encoding/json escapes each control character, and checks writes the "[" of
// each "##[" in it as \u005b, which a JSON reader reads back as "[". print writes each block as the
// approved spec holds it, since a change copies it byte for byte; no CI job runs print.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
       spec-check compare --dir <dir> --approved <approved.json> [--allow-empty]
       spec-check print --approved <approved.json> --id <id> [--id <id>...]
       spec-check guard --root <dir>`

// prefix is the fixed text that starts every line the command prints but the usage, the JSON of
// checks and the blocks of print.
const prefix = "spec-check: "

// subcommands is each subcommand by its name: it takes the arguments after the name and answers
// the exit status.
var subcommands = map[string]func(args []string, stdout, stderr io.Writer) int{
	"tree":    runTree,
	"changes": runChanges,
	"checks":  runChecks,
	"compare": runCompare,
	"print":   runPrint,
	"guard":   runGuard,
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
