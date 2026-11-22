package ghstackgit

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// What the repository's own files make the git a call runs execute, beside its configuration,
// remotes and gh-stack's stack file, which `legion gh stack` reads before gh-stack starts. These
// can change while gh-stack runs, so cmd/gh-stack-git reads them on each call (Refusal) and
// pins the settings that reach the rest (Forced):
//
//   - hooks, `.git/hooks/<name>`, which git runs on a checkout, a commit, a rebase or a push:
//     core.hooksPath is /dev/null for every call (Forced); the image's git wrapper names its own
//     root-owned pre-push hook for a push after it, which git takes, being the later `-c`;
//   - core.fsmonitor, a program git runs to learn which files changed: false (Forced);
//   - a submodule, any gitlink in the index: `git status`, `add -u`, `add -A`, `commit` and `diff`
//     each run a `git status` inside a populated one, which reads that submodule's own
//     configuration, filters included, with no submodule.* key needed in this repository's
//     (measured with git 2.43 and 2.47.3). A call that reads the work tree or the index is refused
//     while the index holds one, whether or not it is populated (Refusal), and fetch and the rest
//     do not recurse (submodule.recurse and fetch.recurseSubmodules false, Forced);
//   - a paused rebase's state, `rebase-merge/`: `git rebase --continue` runs each line of
//     git-rebase-todo, `exec <command>` included, and the merge strategy named in `strategy` as
//     `git merge-<strategy>` (both measured), so it runs only where every remaining line is
//     `pick <commit>`, no strategy, strategy option or signing is recorded, and no
//     `rebase-apply/` is there, which gh-stack, using the merge backend, never makes;
//   - a paused cherry-pick's state, `sequencer/`: `git cherry-pick --continue` runs only where
//     every remaining line of its todo is `pick <commit>` and its opts name no strategy, strategy
//     option or signing.

// Forced are the settings cmd/gh-stack-git gives every git it runs, ahead of gh-stack's own
// arguments, so a later `-c` of gh-stack's or of the image's git wrapper still wins.
var Forced = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
}

// readsTheWorkTree are the subcommands gh-stack runs that read the work tree or the index, and
// that git runs inside a populated submodule for: each is refused while the index holds a gitlink.
var readsTheWorkTree = []string{"add", "checkout", "cherry-pick", "commit", "diff", "merge", "rebase", "status"}

// todoLine is a line of a rebase's or a cherry-pick's todo that only picks a commit.
var todoLine = regexp.MustCompile(`^(pick|p) [0-9a-f]{4,64}( .*)?$`)

// optsKey is a key of a cherry-pick's opts that names a strategy, a strategy option or signing.
var optsKey = regexp.MustCompile(`(?im)^\s*(strategy|strategy-option|gpg-sign)\s*=`)

// subcommand is args' git subcommand and its arguments, past `-C <dir>` and `-c <setting>`.
func subcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-C", "-c":
			i++
		default:
			return args[i], args[i+1:]
		}
	}
	return "", nil
}

// dirOf is the `-C <dir>` args name, or "".
func dirOf(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		return args[1]
	}
	return ""
}

// Refusal answers why the git args would run something the repository's own files name, or nil.
// git runs the image's git in the call's directory (with its `-C <dir>`) and answers what it
// prints; read reads a file. A probe that fails is a refusal, since what it would have read is
// then unread.
func Refusal(args []string, git func(args ...string) ([]byte, error), read func(string) ([]byte, error)) error {
	sub, rest := subcommand(args)
	at := func(probe ...string) ([]byte, error) {
		if dir := dirOf(args); dir != "" {
			probe = append([]string{"-C", dir}, probe...)
		}
		return git(probe...)
	}
	if slices.Contains(readsTheWorkTree, sub) {
		index, err := at("ls-files", "--stage", "-z")
		if err != nil {
			return fmt.Errorf("cannot read the index before `git %s` (%v), so git does not run", sub, err)
		}
		for _, entry := range strings.Split(string(index), "\x00") {
			if mode, path, ok := strings.Cut(entry, "\t"); ok && strings.HasPrefix(mode, "160000 ") {
				return fmt.Errorf("the index holds the submodule %s, and `git %s` runs a git inside a populated submodule, which reads that submodule's own configuration, so git does not run; gh stack works in a repository with no submodule", path, sub)
			}
		}
	}
	if !slices.Contains(rest, "--continue") {
		return nil
	}
	switch sub {
	case "rebase":
		paths, err := at("rev-parse", "--path-format=absolute", "--git-path", "rebase-merge", "--git-path", "rebase-apply")
		lines := strings.Fields(string(paths))
		if err != nil || len(lines) != 2 {
			return fmt.Errorf("cannot find the paused rebase's state (%v), so git does not run", err)
		}
		if _, err := read(lines[1]); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("a rebase of git's apply backend is paused here (%s), which gh-stack never starts, so git does not run; finish or abort it with git itself", lines[1])
		}
		for _, file := range []string{"strategy", "strategy_opts", "gpg_sign_opt"} {
			if _, err := read(filepath.Join(lines[0], file)); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("the paused rebase records %s (%s), which gh-stack never sets and git would act on, so git does not run; finish or abort it with git itself", file, filepath.Join(lines[0], file))
			}
		}
		return todoRefusal(read, filepath.Join(lines[0], "git-rebase-todo"), "rebase")
	case "cherry-pick":
		paths, err := at("rev-parse", "--path-format=absolute", "--git-path", "sequencer")
		dir := strings.TrimSpace(string(paths))
		if err != nil || dir == "" {
			return fmt.Errorf("cannot find the paused cherry-pick's state (%v), so git does not run", err)
		}
		opts, err := read(filepath.Join(dir, "opts"))
		switch {
		case err == nil && optsKey.Match(opts):
			return fmt.Errorf("the paused cherry-pick records a strategy, a strategy option or signing (%s), which gh-stack never sets and git would act on, so git does not run; finish or abort it with git itself", filepath.Join(dir, "opts"))
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("cannot read the paused cherry-pick's opts (%v), so git does not run", err)
		}
		return todoRefusal(read, filepath.Join(dir, "todo"), "cherry-pick")
	}
	return nil
}

// todoRefusal refuses a todo with a line that does more than pick a commit; none is no refusal.
func todoRefusal(read func(string) ([]byte, error), path, what string) error {
	todo, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read the paused %s's todo (%v), so git does not run", what, err)
	}
	for _, line := range strings.Split(string(todo), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || todoLine.MatchString(line) {
			continue
		}
		return fmt.Errorf("the paused %s's todo (%s) holds %q, which gh-stack never writes and `git %s --continue` would run, so git does not run; finish or abort it with git itself", what, path, line, what)
	}
	return nil
}
