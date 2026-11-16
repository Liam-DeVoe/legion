package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// runCompare is "spec-check compare --dir <dir> --approved <approved.json>": it compares the
// files of dir with the approved spec and answers 0 when every text they hold is approved, 1 when
// one is not, and 2 for files that do not parse, a directory or a file it cannot read or
// arguments it does not take.
func runCompare(args []string, stdout, stderr io.Writer) int {
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
	say(stdout, "%s: every text the files hold is approved; %d of %d approved %s %s in the files at %s current text",
		dir, held, all, plural(all, "statement"), verb, pronoun)
	for _, line := range others {
		say(stdout, "%s", line)
	}
	return 0
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
