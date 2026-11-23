package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests in this file are those of what jj prints on standard error for a command of Legion's
// jj's own: the pod's wrapper's reads (packages/daemon/docker/jj's once_only) and the pane's read
// of the aliases and the step's snapshot (jjOnceOnly). Each prints the warnings jj prints once and
// drops those jj prints on every command, which the command after it prints itself. The rows of
// the two forms run through the shared fixture (jj_push_fixture_test.go), as a role runs a
// command; the last test reads jjOnceOnly itself, so this file names what the change adds, as
// jj_expand_read_test.go does.

// rolePushCopiedWarning starts the warning that jj prints for the first command in a copy of a
// repository that has a configuration of its own, and for no command after it
// (lib/src/secure_config.rs).
const rolePushCopiedWarning = "Your repo appears to have been copied"

// rolePushOverridesWarning starts the warning that jj prints on every command of an alias that has
// the name of a built-in command (cli/src/cli_util.rs).
const rolePushOverridesWarning = "Cannot define an alias that overrides the built-in command"

// A warning that jj prints once reaches the role once: in a copy, made with `cp -a`, of a
// repository with a configuration of its own, which jj's secure configuration needs to tell that
// the repository was copied, the first `jj st` through the form's jj prints "Your repo appears to
// have been copied" once, as jj alone prints it for its first `jj st` in another such copy, and a
// second `jj st` there prints it no more; the same for `jj status`. `st` is jj's alias of `status`:
// the pod's wrapper and the pane's legion jj each read it first, and that read is the first command
// in the copy, which takes the warning. `status` is a built-in command, which neither reads.
func TestAWarningJJPrintsOnceReachesTheRoleOnce(t *testing.T) {
	t.Parallel()
	impl := rolePushImplementer
	for _, word := range []string{"st", "status"} {
		t.Run("jj "+word, func(t *testing.T) {
			t.Parallel()
			rolePushEachForm(t, func(t *testing.T, form rolePushForm) {
				f := newRolePushFixture(t, form, "")
				f.configure("[ui]\neditor = \"true\"")
				copyOf := func(name string) string {
					t.Helper()
					dir := filepath.Join(f.root, name)
					f.runner(f.setupPath, impl).Run(f.root, "cp", "-a", f.repo, dir)
					return dir
				}
				code, _, stderr := f.aloneIn(copyOf("alone"), impl, nil, word)
				if n := strings.Count(stderr, rolePushCopiedWarning); code != 0 || n != 1 {
					t.Fatalf("jj alone's first `jj %s` in a copy exited %d and printed %q %d times, want 0 and once:\n%s", word, code, rolePushCopiedWarning, n, stderr)
				}
				dir := copyOf("copy")
				for i, want := range []int{1, 0} {
					code, _, stderr := f.runIn(f.path, dir, impl, nil, word)
					n := strings.Count(stderr, rolePushCopiedWarning)
					t.Logf("`jj %s` number %d in the copy printed %q %d times", word, i+1, rolePushCopiedWarning, n)
					if code != 0 || n != want {
						t.Errorf("`jj %s` number %d in the copy exited %d and printed %q %d times, want 0 and %d times, as jj alone prints it:\n%s", word, i+1, code, rolePushCopiedWarning, n, want, stderr)
					}
				}
			})
		})
	}
}

// A warning that jj prints on every command prints as often as jj alone prints it for the line the
// form runs: with aliases.log = ['log', '-r', '@'] in the repository's configuration, `jj st`, `jj
// status` and `jj git push -b b`, a push of C, a commit the other role made and the implementer
// described with its own trailer, print "Cannot define an alias that overrides the built-in command"
// as many times as jj alone prints it in a repository made the same way: with the git wrapper's
// first layer in front of the line under the pod's form, which puts that layer on every line it
// runs, and on which jj prints such a warning twice (warn_if_args_mismatch), and on the line as
// written under the tmux form. The push sends C with the implementer's author after the step's one
// snapshot, so the step's snapshot is among the commands that print nothing of that warning, and
// so, under the pod's form, is the wrapper's read of git.executable-path; under both forms, the
// read of `st`. The test unsets the alias after the push, so that its own reads of what the push
// left, which read jj's standard error with its standard output, read no warning.
func TestAWarningJJPrintsOnEveryCommandPrintsAsOftenAsJJAlonePrintsIt(t *testing.T) {
	t.Parallel()
	impl := rolePushImplementer
	const toml = "[aliases]\nlog = [\"log\", \"-r\", \"@\"]"
	for _, row := range []struct {
		name string
		args []string
		push bool
	}{
		{name: "jj st", args: []string{"st"}},
		{name: "jj status", args: []string{"status"}},
		{name: "jj git push -b b", args: []string{"git", "push", "-b", "b"}, push: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			rolePushEachForm(t, func(t *testing.T, form rolePushForm) {
				twin := newRolePushFixture(t, form, "")
				twin.configure(toml)
				twin.workAndB()
				line := row.args
				if form == rolePushPod {
					line = slices.Concat([]string{twin.podForces[0], "--config-file=/dev/null"}, row.args)
				}
				code, _, aloneErr := twin.alone(impl, line...)
				alone := strings.Count(aloneErr, rolePushOverridesWarning)
				if code != 0 || alone == 0 {
					t.Fatalf("jj alone exited %d on %q and printed %q %d times, want 0 and at least once:\n%s", code, line, rolePushOverridesWarning, alone, aloneErr)
				}
				f := newRolePushFixture(t, form, rolePushRecords)
				f.configure(toml)
				c := f.workAndB()
				divergent := f.divergence()
				code, _, stderr := f.push(impl, row.args...)
				n := strings.Count(stderr, rolePushOverridesWarning)
				t.Logf("jj alone printed %q %d times for %q, and the form's jj %d times for %q", rolePushOverridesWarning, alone, line, n, row.args)
				if n != alone {
					t.Errorf("the command printed %q %d times, want %d, as jj alone prints it for %q:\n%s", rolePushOverridesWarning, n, alone, line, stderr)
				}
				if !row.push {
					if code != 0 {
						t.Errorf("the command exited %d, want 0:\n%s", code, stderr)
					}
					return
				}
				want := rolePushSends("b", c, "implementer", impl)
				want.snapshots = rolePushTakesOneSnapshot
				f.jj(impl, "config", "unset", "--repo", "aliases.log")
				f.check(want, row.args, code, stderr, divergent, 0)
			})
		})
	}
}

// jjOnceOnlyBlocks are blocks of jj's standard error as jj 0.38.0, 0.45.1-sami and 0.46.0 print
// them, each with whether jjOnceOnly keeps it: a warning jj prints once, a line of jj's of no
// heading, and a warning jj prints on every command, each warning with the lines jj prints as its
// parts, and some with jj's colours. They are in an order that puts a block that jjOnceOnly keeps,
// one with no heading among them, right after each that it drops.
var jjOnceOnlyBlocks = []struct {
	name, text string
	kept       bool
}{
	{name: "the copied repository", kept: true,
		text: "Warning: Your repo appears to have been copied from /a/repo/.jj/repo to /b/repo/.jj/repo. The corresponding repo config file has also been copied.\n"},
	{name: "an alias named for a built-in command, with jj 0.45's full stop",
		text: "Warning: Cannot define an alias that overrides the built-in command 'log'.\n"},
	{name: "the snapshot's line of the descendants it rebased", kept: true,
		text: "Rebased 1 descendant commits onto updated working copy.\n"},
	{name: "an alias named for a built-in command, with no full stop as in jj 0.38",
		text: "Warning: Cannot define an alias that overrides the built-in command 'log'\n"},
	{name: "the repository configuration not found", kept: true,
		text: "Warning: Per-repo config not found. Generating an empty one.\nPer-repo config is stored in the same directory as your user config for security reasons.\nIf you work across multiple computers, you may want to keep your user config directory in sync.\n"},
	{name: "a revset alias that does not parse",
		text: "Warning: Failed to load `revset-aliases.foo(`:  --> 1:5\n  |\n1 | foo(\n  |     ^---\n  |\n  = expected <strict_identifier>\n"},
	{name: "the migrated configuration", kept: true,
		text: "Warning: Your config file has been migrated from /b/repo/.jj/repo/config.toml to /home/.config/jj/repos/0123/config.toml. You can edit the new file with `jj config edit`\n"},
	{name: "an alias that does not load, with jj 0.46's causes",
		text: "Warning: Failed to load `aliases.x`: Expected a string list or a table with a `definition`\nCaused by:\n1: Invalid type or value for aliases.x\n2: invalid type: string \"git push\", expected a sequence\n\n"},
	{name: "a file the snapshot refused, with its hint", kept: true,
		text: "Warning: Refused to snapshot some files:\n  large.bin: 2.0MiB (2097152 bytes); the maximum size allowed is 1.0MiB (1048576 bytes)\nHint: This is to prevent large files from being added by accident. To fix this:\n  * Add the file(s) to `.gitignore`\n"},
	{name: "a deprecated setting",
		text: "Warning: Deprecated user-level config: ui.old-name is renamed to ui.new-name\n"},
	{name: "a template alias that does not load",
		text: "Warning: Failed to load `template-aliases.x`: Expected a string or a table with a `definition` string key, but is integer\n"},
	{name: "aliases that -R, --config or --config-file would define",
		text: "Warning: Command aliases cannot be loaded from -R/--repository path or --config/--config-file arguments.\n"},
	{name: "a concurrent operation", kept: true,
		text: "Concurrent modification detected, resolving automatically.\n"},
	{name: "a trunk() that does not resolve, with its second line and its hint",
		text: "Warning: Failed to resolve `revset-aliases.trunk()`: Revision `nosuch` doesn't exist\nThe `trunk()` alias is temporarily set to `root()`.\nHint: Use `jj config edit --repo` to adjust the `trunk()` alias.\n"},
	{name: "the snapshot's line of the descendants it rebased, after trunk()'s hint", kept: true,
		text: "Rebased 2 descendant commits onto updated working copy.\n"},
	{name: "coloured, an alias named for a built-in command",
		text: "\x1b[1m\x1b[38;5;3mWarning: \x1b[39mCannot define an alias that overrides the built-in command 'log'.\x1b[0m\n"},
	{name: "coloured, the copied repository", kept: true,
		text: "\x1b[1m\x1b[38;5;3mWarning: \x1b[39mYour repo appears to have been copied from /a/repo/.jj/repo to /c/repo/.jj/repo. The corresponding repo config file has also been copied.\x1b[0m\n"},
	{name: "coloured, a trunk() that does not resolve, with its second line and its hint",
		text: "\x1b[1m\x1b[38;5;3mWarning: \x1b[39mFailed to resolve `revset-aliases.trunk()`: Revision `nosuch` doesn't exist\x1b[0m\n\x1b[1m\x1b[39mThe `trunk()` alias is temporarily set to `root()`.\x1b[0m\n\x1b[1m\x1b[38;5;6mHint: \x1b[0m\x1b[39mUse `jj config edit --repo` to adjust the `trunk()` alias.\x1b[39m\n"},
	{name: "the trunk() hint alone, as jj 0.38 prints it after a transaction",
		text: "Hint: Use `jj config edit --repo` to adjust the `trunk()` alias.\n"},
	{name: "a hint of no warning jj prints on every command", kept: true,
		text: "Hint: Prefix the expression with `all:` to allow any number of revisions.\n"},
}

// jjOnceOnlyErrors are jj's errors, each with its causes and hints, as jj prints them last.
var jjOnceOnlyErrors = []string{
	"Error: There is no jj repo in \".\"\n",
	"Config error: Configuration cannot be parsed as TOML document\nCaused by: TOML parse error at line 1, column 9\n  |\n1 | [aliases\n  |         ^\nunclosed table, expected `]`\n\nHint: Check the config file: /home/.config/jj/repos/0123/config.toml\nFor help, see https://docs.jj-vcs.dev/latest/config/ or use `jj help -k config`.\n",
	"Internal error: Unexpected error from backend\nCaused by:\n1: Newly-created commit 0123456789abcdef already exists\n",
	"\x1b[1m\x1b[38;5;1mError: \x1b[39mThere is no jj repo in \".\"\x1b[0m\n",
}

// The filter of what a jj command of legion jj's own prints on standard error drops each warning jj
// prints on every command, each with the lines jj prints as its parts, and keeps every other line:
// each block of jjOnceOnlyBlocks alone, and all of them in one text, give what the block's kept
// says, a block with jj's colours read as it reads with none, and the colours of a block it keeps
// kept. A line of no heading right after a warning it drops, such as the snapshot's line of the
// descendants it rebased, is kept. jj's error, each of jjOnceOnlyErrors after those blocks, and
// every line after it, are dropped where the caller asks for no error, and kept where it asks for
// the error.
func TestTheFilterOfJJsOutputDropsOnlyTheWarningsJJPrintsOnEveryCommand(t *testing.T) {
	t.Parallel()
	var all, kept strings.Builder
	for _, block := range jjOnceOnlyBlocks {
		want := ""
		if block.kept {
			want = block.text
			kept.WriteString(block.text)
		}
		all.WriteString(block.text)
		for _, withError := range []bool{false, true} {
			if got := jjOnceOnly(block.text, withError); got != want {
				t.Errorf("%s, with error %v: the filter gives %q, want %q", block.name, withError, got, want)
			}
		}
	}
	for _, withError := range []bool{false, true} {
		if got := jjOnceOnly(all.String(), withError); got != kept.String() {
			t.Errorf("every block in one text, with error %v: the filter gives\n%q\nwant\n%q", withError, got, kept.String())
		}
	}
	for _, failure := range jjOnceOnlyErrors {
		text := all.String() + failure
		if got := jjOnceOnly(text, false); got != kept.String() {
			t.Errorf("with no error: the filter gives\n%q\nwant what it keeps of the blocks before jj's error, and nothing of the error\n%q", got, kept.String())
		}
		if got, want := jjOnceOnly(text, true), kept.String()+failure; got != want {
			t.Errorf("with the error: the filter gives\n%q\nwant what it keeps of the blocks, then jj's error whole\n%q", got, want)
		}
	}
}
