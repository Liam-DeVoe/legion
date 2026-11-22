// Command gh-stack-git is the git first on gh-stack's PATH when `legion gh stack` runs it: the
// worker image installs it as /opt/legion/gh-stack-bin/git. It runs the image's git
// (ghstackgit.RealGit) only for an argument list the pinned gh-stack release starts git with,
// none of whose names starts with `-` (ghstackgit.Check), so a branch or revision name gh-stack
// read from a file, from GitHub or from the repository never reaches git as an option; and it
// runs `checkout <name>` as `checkout <name> --`, so the name is never read as a path
// (ghstackgit.Args). It refuses a call the repository's own files would make run a program — a
// submodule in the index, a paused rebase or cherry-pick that does more than pick commits
// (ghstackgit.Refusal) — and gives every call the settings that keep hooks, an fsmonitor and
// submodule recursion off (ghstackgit.Forced). It runs that git with PATH at
// ghstackgit.GitPath, so the git it starts, its hooks among them, finds the image's git and
// never this one.
//
// It exits 125 where it refuses a call, and 126 where it cannot run the image's git: codes git
// itself does not use, since gh-stack reads some of git's own (1 from `merge-base --is-ancestor`,
// 2 from `diff --check`) as answers.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/sjawhar/legion/daemon/internal/ghstackgit"
)

func main() {
	args, err := ghstackgit.Args(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "legion gh-stack git: %v\n", err)
		os.Exit(125)
	}
	env := []string{"PATH=" + ghstackgit.GitPath}
	for _, pair := range os.Environ() {
		if !strings.HasPrefix(pair, "PATH=") {
			env = append(env, pair)
		}
	}
	probe := func(probe ...string) ([]byte, error) {
		cmd := exec.Command(ghstackgit.RealGit, append(append([]string{}, ghstackgit.Forced...), probe...)...)
		cmd.Env = append(env, "LC_ALL=C")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	if err := ghstackgit.Refusal(args, probe, os.ReadFile); err != nil {
		fmt.Fprintf(os.Stderr, "legion gh-stack git: %v\n", err)
		os.Exit(125)
	}
	err = syscall.Exec(ghstackgit.RealGit, append(append([]string{"git"}, ghstackgit.Forced...), args...), env)
	fmt.Fprintf(os.Stderr, "legion gh-stack git: run %s: %v\n", ghstackgit.RealGit, err)
	os.Exit(126)
}
