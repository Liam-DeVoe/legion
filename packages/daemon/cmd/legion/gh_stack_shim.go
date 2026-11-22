package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// gh-stack's git. `legion gh stack` runs gh-stack with PATH set to ghStackPath alone, whose first
// directory holds the image's root-owned `git` script that runs `legion gh-stack-git` (the
// worker image's /opt/legion/gh-stack-bin/git). So every git gh-stack starts passes through
// runGhStackGit, which takes an argument that starts with `-` only where it is one of the option
// words gh-stack and its git client pass (ghStackGitOptions, generated from their sources by
// gh_stack_git_options.sh), and then runs the image's own git wrapper with the arguments
// untouched. A branch or revision name gh-stack read from a file, from GitHub or from the
// repository therefore never reaches git as an option, whatever it read it from. The rest of
// PATH is root-owned image directories, so no git-remote-* helper, editor or pager a role wrote
// runs either.

// ghStackPath is the PATH gh-stack runs with: the directory of the image's git shim, then the
// image's Legion programs (the git and gh wrappers, gh's credential helper) and /usr/bin.
var ghStackPath = []string{"/opt/legion/gh-stack-bin", "/opt/legion/bin", "/usr/bin"}

// ghStackRealGit is the git runGhStackGit runs: the image's git wrapper, which puts the leak
// guard's pre-push hook before every push and runs /usr/bin/git.
var ghStackRealGit = "/opt/legion/bin/git"

// runGhStackGit is `legion gh-stack-git`, the git first on gh-stack's PATH (ghStackPath). An
// argument that starts with `-` and is no option word of ghStackGitOptions, nor starts with one
// of its `--option=` prefixes, is refused, exit 2, naming it; any other call runs
// ghStackRealGit with the same arguments and environment, in this process's place.
func runGhStackGit(_ context.Context, args []string, _, stderr io.Writer) int {
	for _, arg := range args {
		if !ghStackGitWord(arg) {
			fmt.Fprintf(stderr, "legion gh-stack-git: %q starts with `-` and is no option gh-stack %s passes to git, so git does not run; a branch or revision name that starts with `-` cannot be used with `gh stack`\n", arg, ghStackGitOptionsVersion)
			return 2
		}
	}
	err := syscall.Exec(ghStackRealGit, append([]string{"git"}, args...), os.Environ())
	fmt.Fprintf(stderr, "legion gh-stack-git: run %s: %v\n", ghStackRealGit, err)
	return 1
}

// ghStackGitWord says whether git may be handed arg from gh-stack: a word that does not start
// with `-`, one of ghStackGitOptions, or one that starts with one of its `--option=` prefixes.
func ghStackGitWord(arg string) bool {
	if !strings.HasPrefix(arg, "-") || slices.Contains(ghStackGitOptions, arg) {
		return true
	}
	return slices.ContainsFunc(ghStackGitOptions, func(option string) bool {
		return strings.HasSuffix(option, "=") && strings.HasPrefix(arg, option)
	})
}

// ghStackPathIsTheImages refuses to run gh-stack unless every directory of ghStackPath is a
// directory, no symbolic link, ghStackOwner's and writable by no one else, and the first holds
// a `git` that is a regular, executable file of the same kind: so no program a role wrote is one
// gh-stack's PATH finds.
func ghStackPathIsTheImages() error {
	for _, dir := range ghStackPath {
		if err := ghStackRootOwned(dir, true); err != nil {
			return err
		}
	}
	return ghStackRootOwned(filepath.Join(ghStackPath[0], "git"), false)
}

// ghStackRootOwned refuses path unless it is ghStackOwner's, no symbolic link, writable by no one
// else, and a directory where dir says so, else a regular executable file.
func ghStackRootOwned(path string, dir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("`gh stack` runs gh-stack with the image's own PATH, and %v, so it sends nothing; run it in a Legion pod, whose image installs it", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("`gh stack` runs gh-stack with the image's own PATH, and %s is a symbolic link, so it sends nothing", path)
	case !ok || int(stat.Uid) != ghStackOwner:
		return fmt.Errorf("`gh stack` runs gh-stack with the image's own PATH, and %s is not root's, so it sends nothing", path)
	case info.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("`gh stack` runs gh-stack with the image's own PATH, and %s is writable by others than its owner, so it sends nothing", path)
	case dir && !info.IsDir():
		return fmt.Errorf("`gh stack` runs gh-stack with the image's own PATH, and %s is no directory, so it sends nothing", path)
	case !dir && (!info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0):
		return fmt.Errorf("`gh stack` runs gh-stack with the image's own PATH, and %s is no executable file, so it sends nothing", path)
	}
	return nil
}
