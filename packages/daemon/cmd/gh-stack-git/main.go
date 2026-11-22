// Command gh-stack-git is the git first on gh-stack's PATH when `legion gh stack` runs it: the
// worker image installs it as /opt/legion/gh-stack-bin/git. It runs the image's git
// (ghstackgit.RealGit) only for an argument list the pinned gh-stack release starts git with,
// none of whose values starts with `-` (ghstackgit.Check), so a branch or revision name gh-stack
// read from a file, from GitHub or from the repository never reaches git as an option. It runs
// that git with PATH at ghstackgit.GitPath, so the git it starts, its hooks among them, finds the
// image's git and never this one. Any other call exits 2 and runs nothing.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/sjawhar/legion/daemon/internal/ghstackgit"
)

func main() {
	args := os.Args[1:]
	if err := ghstackgit.Check(args); err != nil {
		fmt.Fprintf(os.Stderr, "legion gh-stack git: %v\n", err)
		os.Exit(2)
	}
	env := []string{"PATH=" + ghstackgit.GitPath}
	for _, pair := range os.Environ() {
		if !strings.HasPrefix(pair, "PATH=") {
			env = append(env, pair)
		}
	}
	err := syscall.Exec(ghstackgit.RealGit, append([]string{"git"}, args...), env)
	fmt.Fprintf(os.Stderr, "legion gh-stack git: run %s: %v\n", ghstackgit.RealGit, err)
	os.Exit(1)
}
