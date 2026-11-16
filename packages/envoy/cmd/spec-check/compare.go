package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// ciVariables are the variables whose value true says that a process runs in a CI job: GitHub
// Actions sets GITHUB_ACTIONS and CI in every step of every job, and most other CI systems set CI.
var ciVariables = []string{"GITHUB_ACTIONS", "CI"}

// runCompare is "spec-check compare --dir <dir> --approved <approved.json>": it compares the
// files of dir with the approved spec and answers 0 when every text they hold is approved, 1 when
// one is not, and 2 for files that do not parse, a directory or a file it cannot read or
// arguments it does not take.
//
// It answers 2, before it reads an argument, in a CI job: no CI job compares the files, since
// Dispatch compares them at each push (R2). The refusal is the command's and not specgraph's,
// whose Compare the Dispatch server calls and whose tests run in CI. It reads the environment of
// its own process, so it covers each route that passes the runner's environment on to the
// command: a step, a package script, a make target, a script in any directory, or a link to one.
// It does not cover a route that drops that environment, such as sudo with its default env_reset
// or a container that docker run starts without -e for the two variables: there the command
// compares (TestTheBuiltBinary, "compare compares behind a boundary that drops the environment").
// spec-check guard names such a line in the workflows, actions and scripts that it reads.
func runCompare(args []string, stdout, stderr io.Writer) int {
	for _, variable := range ciVariables {
		if os.Getenv(variable) == "true" {
			say(stderr, "compare does not run in a CI job (%s=true): Dispatch compares the files with the approved spec at each push (R2)", variable)
			return 2
		}
	}
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	dirFlag := flags.String("dir", "", "the directory of spec files")
	approvedFlag := flags.String("approved", "", "the approved spec, in the form of approved.json")
	if code, ok := parse(flags, args, stderr, "dir", "approved"); !ok {
		return code
	}
	dir, ok := absolute(stderr, *dirFlag)
	if !ok {
		return 2
	}
	_, a, ok := readApproved(stderr, *approvedFlag)
	if !ok {
		return 2
	}
	j, err := read(stderr, dir)
	switch {
	case err != nil:
		return cannotJudge(stderr, dir, err)
	case j.files == 0:
		// An instrument that read no file has not passed: every text of no file is approved.
		say(stderr, "%s: holds no spec file", dir)
		return 2
	case len(j.problems) > 0:
		// A block that does not parse is missing from the tree, so a comparison of the tree would
		// pass it.
		j.sayProblems(stderr)
		say(stderr, "%s: the files do not parse, so no comparison was made", dir)
		return 2
	}

	c := specgraph.Compare(j.tree, a)
	if len(c.Problems) > 0 {
		judged{dir: dir, problems: c.Problems}.sayProblems(stderr)
		say(stdout, "%s: the comparison fails on %s", dir, count(len(c.Problems), "node"))
		return 1
	}
	current, _ := a.Current() // ReadApproved refused every block that would be a problem here
	var others []string
	for _, s := range current.Statements {
		h := c.Holds[s.ID]
		switch {
		case !h.Present:
			others = append(others, fmt.Sprintf("statement %s: not in the files", s.ID))
		case !h.Current:
			versions := a.Nodes[s.ID].Versions
			others = append(others, fmt.Sprintf("statement %s: the files hold approved version %d, and version %d is current", s.ID, h.Version, versions[len(versions)-1].Version))
		}
	}
	held, all := len(current.Statements)-len(others), len(current.Statements)
	verb, pronoun := "are", "their"
	if held == 1 {
		verb, pronoun = "is", "its"
	}
	// The files may hold an earlier approved text or a text of a removed node, so the line says
	// that the approved spec held each text, not that each text is approved now.
	say(stdout, "%s: the approved spec held every text that the files hold; %d of %d approved %s %s in the files at %s current text",
		dir, held, all, plural(all, "statement"), verb, pronoun)
	for _, line := range others {
		say(stdout, "%s", line)
	}
	for _, line := range removedLines(j.tree, c.Removed) {
		say(stdout, "%s", line)
	}
	return 0
}

// removedLines is one line for each node of tree whose id is in removed, the nodes that the
// approved spec removed and the files still hold (R28: "the files hold one that is no longer
// approved"): "<kind> <id>: the files hold it, and the approved spec removed it", statements,
// then checks, components and interfaces, then terms, each by id, and an id once.
func removedLines(tree specgraph.Tree, removed []string) []string {
	var lines []string
	named := map[string]bool{} // an id the files hold two times is one line
	add := func(kind, id string) {
		if slices.Contains(removed, id) && !named[id] {
			named[id] = true
			lines = append(lines, kind+" "+id+": the files hold it, and the approved spec removed it")
		}
	}
	for _, s := range tree.Statements {
		add("statement", s.ID)
	}
	for _, c := range tree.Checks {
		add("check", c.ID)
	}
	for _, c := range tree.Components {
		if c.Interface {
			add("interface", c.ID)
		} else {
			add("component", c.ID)
		}
	}
	for _, t := range tree.Terms {
		add("term", t.ID)
	}
	return lines
}

// readApproved reads the approved spec at path with specgraph.ReadApproved, and answers the path
// made absolute and whether it could read it; when not, it has written the line that names the
// file and the fault.
func readApproved(stderr io.Writer, path string) (string, specgraph.Approved, bool) {
	path, ok := absolute(stderr, path)
	if !ok {
		return path, specgraph.Approved{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		say(stderr, "%s: cannot be read: %s", path, err)
		return path, specgraph.Approved{}, false
	}
	defer f.Close()
	a, err := specgraph.ReadApproved(f)
	if err != nil {
		say(stderr, "%s: %s", path, err)
		return path, specgraph.Approved{}, false
	}
	return path, a, true
}
