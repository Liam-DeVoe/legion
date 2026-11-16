package main

import (
	"flag"
	"io"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// runChanges is "spec-check changes --base <dir> --dir <dir>": it prints one line for each
// statement that changed from the base to the directory, or one line that none did, and answers
// 0; 2 when either tree has a problem, with the problems of both on stderr, or for a directory it
// cannot read or arguments it does not take.
func runChanges(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("changes", flag.ContinueOnError)
	baseFlag := flags.String("base", "", "the directory of spec files of the earlier version")
	dirFlag := flags.String("dir", "", "the directory of spec files of the later version")
	if code, ok := parse(flags, args, stderr, "base", "dir"); !ok {
		return code
	}
	var trees [2]specgraph.Tree
	refused := false
	for i, path := range []string{*baseFlag, *dirFlag} {
		dir, ok := absolute(stderr, path)
		if !ok {
			return 2
		}
		j, err := judge(stderr, dir, specgraph.Options{})
		if err != nil {
			return cannotJudge(stderr, dir, err)
		}
		j.sayProblems(stderr)
		refused = refused || len(j.problems) > 0
		trees[i] = j.tree
	}
	if refused {
		return 2
	}
	changes := specgraph.Changes(trees[0], trees[1])
	if len(changes) == 0 {
		say(stdout, "no statement changed")
	}
	for _, c := range changes {
		say(stdout, "%s: %s", c.ID, strings.Join(c.What, ", "))
	}
	return 0
}
