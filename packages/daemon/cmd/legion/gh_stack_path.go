package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/sjawhar/legion/daemon/internal/ghstackgit"
)

// gh-stack's PATH. `legion gh stack` runs gh-stack with PATH set to ghStackPath alone. Its first
// directory holds the image's gh-stack git (cmd/gh-stack-git, the worker image's
// /opt/legion/gh-stack-bin/git), so every git gh-stack starts runs only where its argument list
// is one the pinned gh-stack release starts git with and none of its names starts with `-`
// (ghstackgit.Check), whatever gh-stack read the names from: a file, GitHub or the repository.
// That git runs the image's git wrapper with the rest of the PATH, so the git it starts, the
// pre-push guard among them, finds the image's git. Every directory is root's and writable by no
// one else, so no git-remote-* helper, editor or pager a role wrote runs either.

// ghStackPath is the PATH gh-stack runs with: the directory of the image's gh-stack git, then the
// image's Legion programs (the git and gh wrappers, gh's credential helper) and /usr/bin.
var ghStackPath = append([]string{ghstackgit.ShimDir}, filepath.SplitList(ghstackgit.GitPath)...)

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
