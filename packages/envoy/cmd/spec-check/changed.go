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
// the paths whose content differs between the trees of the two commits of the git clone at root,
// specgraph.GitHistory's answer (both paths of a move), sorted, one a line, the form that
// citations --changed reads, and answers 0; 2 for a commit that git cannot read, a path that holds
// a line break, a root it cannot read or arguments it does not take, each with one line naming it.
//
// The paths are data for a file, as the citations job writes them, and do not pass through
// runnerlog.Say, which would change them; the count line on stderr does.
func runChanged(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("changed", flag.ContinueOnError)
	repoFlag := flags.String("repo", "", "the root of the git clone")
	base := flags.String("base", "", "the commit the change starts from")
	head := flags.String("head", "HEAD", "the commit the change ends at")
	if code, ok := parse(flags, args, stderr, "repo", "base", "head"); !ok {
		return code
	}
	repo, ok := absolute(stderr, *repoFlag)
	if !ok || !isDirectory(stderr, repo) {
		return 2
	}
	changed, ok := specgraph.GitHistory{Dir: repo}.Changed(*base, *head)
	if !ok {
		for _, commit := range []string{*base, *head} {
			// --end-of-options makes git read an id that starts with "-" as a revision, as
			// GitHistory does.
			if exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{commit}").Run() != nil {
				say(stderr, "%s: git cannot read commit %s", repo, commit)
				return 2
			}
		}
		say(stderr, "%s: git diff from %s to %s failed", repo, *base, *head)
		return 2
	}
	for _, path := range changed {
		if strings.ContainsAny(path, "\n\r") {
			say(stderr, "%s: the path %q holds a line break, which a list of one path a line cannot hold", repo, path)
			return 2
		}
	}
	for _, path := range changed {
		fmt.Fprintln(stdout, path)
	}
	say(stderr, "%s: %s changed from %s to %s", repo, count(len(changed), "path"), *base, *head)
	return 0
}
