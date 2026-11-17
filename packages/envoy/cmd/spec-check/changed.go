package main

import (
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// runChanged is "spec-check changed --repo <root> --base <commit> [--head <commit>]": it prints
// the line "base <object name of base>", then the paths whose content differs between the trees of
// the two commits of the git clone at root, specgraph.GitHistory's answer (both paths of a move),
// sorted, one a line: the one form that citations --changed reads, and answers 0; 2 for a commit
// that git cannot read, a path that holds a line break, a root it cannot read or arguments it does
// not take, each with one line naming it.
//
// The base is the commit where each result that the head carries stands, which for a change's
// branch is its merge base with the default branch and never the branch's push before: a list from
// that push holds no change that the push before it made, so a test that has not run since such a
// change would read as cited. citations reads the tie of a check at the base commit through the
// base line, so a file that the change deleted or moved out of the tie still counts.
//
// The paths are data for a file, as the citations job writes them, and do not pass through
// runnerlog.Say, which would change them; the count line on stderr does.
func runChanged(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("changed", flag.ContinueOnError)
	repoFlag := flags.String("repo", "", "the root of the git clone")
	base := flags.String("base", "", "the commit where each carried result stands: the merge base with the default branch")
	head := flags.String("head", "HEAD", "the commit the change ends at")
	if code, ok := parse(flags, args, stderr, "repo", "base", "head"); !ok {
		return code
	}
	repo, ok := absolute(stderr, *repoFlag)
	if !ok || !isDirectory(stderr, repo) {
		return 2
	}
	// --end-of-options makes git read an id that starts with "-" as a revision, as GitHistory does.
	baseName, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "--end-of-options", *base+"^{commit}").Output()
	if err != nil {
		say(stderr, "%s: git cannot read commit %s", repo, *base)
		return 2
	}
	if exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "--end-of-options", *head+"^{commit}").Run() != nil {
		say(stderr, "%s: git cannot read commit %s", repo, *head)
		return 2
	}
	changed, ok := specgraph.GitHistory{Dir: repo}.Changed(*base, *head)
	if !ok {
		say(stderr, "%s: git diff from %s to %s failed", repo, *base, *head)
		return 2
	}
	for _, path := range changed {
		if strings.ContainsAny(path, "\n\r") {
			say(stderr, "%s: the path %q holds a line break, which a list of one path a line cannot hold", repo, path)
			return 2
		}
	}
	fmt.Fprintf(stdout, "%s%s\n", baseLine, strings.TrimSpace(string(baseName)))
	for _, path := range changed {
		fmt.Fprintln(stdout, path)
	}
	say(stderr, "%s: %s changed from %s to %s", repo, count(len(changed), "path"), *base, *head)
	return 0
}
