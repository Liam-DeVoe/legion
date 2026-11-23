package main

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file read the filter of what jj prints on standard error for a command of
// legion jj's own (jjOnceOnly, jj_reads.go), and hold the pod's wrapper's filter
// (packages/daemon/docker/jj's once_only) to it, over what jj's releases print
// (testdata/jj-stderr) and over hand-typed texts. They name the change's own Go names, as
// jj_expand_read_test.go does; the tests of what each form of Legion's jj prints are in
// jj_warnings_test.go, which names nothing the change adds.

// capturedStderrs are what jj prints on standard error, captured from jj 0.38.0, 0.45.1-sami and
// 0.46.0, each in a scratch repository with a local bare remote and a configuration that makes
// one shape: testdata/jj-stderr/<release>/<shape>.txt holds on its first line the command that made
// it, then that command's standard error, then a line `--- exit <status>`.
const capturedStderrs = "testdata/jj-stderr"

// capturedStderrReleases are the releases the captures are of, one directory each.
var capturedStderrReleases = []string{"0.38.0", "0.45.1-sami", "0.46.0"}

// capturedStderr is one capture's command, standard error and exit status.
func capturedStderr(t *testing.T, path string) (command, stderr string, code int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	command, rest, _ := strings.Cut(string(data), "\n")
	at := strings.LastIndex(rest, "--- exit ")
	if at < 0 || (at > 0 && rest[at-1] != '\n') {
		t.Fatalf("%s holds no line `--- exit <status>` after its standard error", path)
	}
	code, err = strconv.Atoi(strings.TrimSpace(rest[at+len("--- exit "):]))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return command, rest[:at], code
}

// capturedKeeps is what jjOnceOnly keeps of a capture of one shape.
type capturedKeeps int

const (
	// capturedKept is every line: a warning jj prints once, or a line of a snapshot's.
	capturedKept capturedKeeps = iota
	// capturedDropped is no line: a warning jj prints on every command, or the note of a key no
	// configuration sets.
	capturedDropped
	// capturedRebased is the snapshot's line of the descendants it rebased, which starts
	// `Rebased `, and every line after it, after a warning jj prints on every command.
	capturedRebased
	// capturedFailure is jj's error, which jj prints last with its causes and hints: every line
	// where the caller asks for jj's error, and no line where not. Its command exits non-zero, and
	// every other shape's exits 0.
	capturedFailure
)

// want is what jjOnceOnly gives of stderr, a capture of a shape that keeps k, where the caller
// asks for jj's error (withError) or not.
func (k capturedKeeps) want(stderr string, withError bool) string {
	switch {
	case k == capturedKept, k == capturedFailure && withError:
		return stderr
	case k == capturedRebased:
		if at := strings.Index(stderr, "\nRebased "); at >= 0 {
			return stderr[at+1:]
		}
		return "a line that starts `Rebased ` after the first"
	}
	return ""
}

// capturedStderrShape is one shape of the captures: kept, what jjOnceOnly keeps of a capture of
// it, and releases, the releases that print it, every one where it is nil.
type capturedStderrShape struct {
	kept     capturedKeeps
	releases []string
}

// capturedStderrShapes are the shapes of the captures, by file name: the inventory of what jj
// prints on standard error for the commands legion jj and the pod's wrapper run, each kept or
// dropped. Each capture is of the pane's read of the repository's configuration before a push
// (`config list -T '""'`), of the step's snapshot (`log --no-graph -r root() -T commit_id` with no
// --ignore-working-copy), or of a read the pod's wrapper makes; a shape a read does not print is
// of the snapshot.
//
// Kept, the warnings jj prints once and the lines of a snapshot: a copy made with `cp -a` of a
// repository with a configuration of its own (copied); a repository whose configuration jj's
// secure configuration has no file for, as under another HOME (per-repo-config-not-found); a
// .jj/repo/config.toml that jj moves to its secure configuration (migrated-config); a new file
// over snapshot.max-new-file-size (refused-snapshot); and a `git commit` in the colocated
// repository, which the snapshot imports (git-import).
//
// Dropped, the warnings jj prints on every command and the note of a key no configuration sets:
// an alias named log in the user's configuration (builtin-alias), with ui.color = "always" there
// too (builtin-alias-color), under which jj colours it although the read carries --color=never;
// an alias that is a string, which jj 0.46.0 alone warns of on a command that does not name it
// (failed-alias); a revset alias and a template alias that do not load (failed-revset-alias,
// failed-template-alias); a deprecated key, core.fsmonitor in jj 0.38.0 and a fix tool's
// line-range-arg in jj 0.46.0, jj 0.45.1-sami having none (deprecated); git.auto-local-bookmark in
// jj 0.38.0, whose warning goes on in two lines of no heading, `Example: …` and `For details,
// see: …` (deprecated-multiline); an alias of --config that differs from the working directory's
// (aliases-from-args); a trunk() that resolves to no commit (trunk); and a read of a key no
// configuration sets (unset-key).
//
// Both: the snapshot's line of the descendants it rebased, right after the warning of an alias
// named log (rebased-after-warning). jj's error, with its causes and hints: a revset that does not
// parse (error), and a configuration that does not parse (config-error).
var capturedStderrShapes = map[string]capturedStderrShape{
	"copied":                    {kept: capturedKept},
	"per-repo-config-not-found": {kept: capturedKept},
	"migrated-config":           {kept: capturedKept},
	"refused-snapshot":          {kept: capturedKept},
	"git-import":                {kept: capturedKept},
	"builtin-alias":             {kept: capturedDropped},
	"builtin-alias-color":       {kept: capturedDropped},
	"failed-alias":              {kept: capturedDropped, releases: []string{"0.46.0"}},
	"failed-revset-alias":       {kept: capturedDropped},
	"failed-template-alias":     {kept: capturedDropped},
	"deprecated":                {kept: capturedDropped, releases: []string{"0.38.0", "0.46.0"}},
	"deprecated-multiline":      {kept: capturedDropped, releases: []string{"0.38.0"}},
	"aliases-from-args":         {kept: capturedDropped},
	"trunk":                     {kept: capturedDropped},
	"unset-key":                 {kept: capturedDropped},
	"rebased-after-warning":     {kept: capturedRebased},
	"error":                     {kept: capturedFailure},
	"config-error":              {kept: capturedFailure},
}

// capturedStderrCases are the cases of every capture (capturedStderrs), each without jj's error
// and with it, wanting what its shape's entry says (capturedStderrShapes). A release directory
// that is not one of capturedStderrReleases, a capture of no shape the table names or of a release
// the shape's entry does not name, a shape a release prints that it holds no capture of, a
// capture with no standard error, and a capture whose exit status is not its shape's (non-zero
// for jj's error alone) fail the test: the table is the inventory of the shapes.
func capturedStderrCases(t *testing.T) []jjOnceOnlyCase {
	t.Helper()
	releases, err := os.ReadDir(capturedStderrs)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, release := range releases {
		names = append(names, release.Name())
	}
	if !slices.Equal(names, capturedStderrReleases) {
		t.Errorf("%s holds the releases %q, want %q", capturedStderrs, names, capturedStderrReleases)
	}
	var cases []jjOnceOnlyCase
	for _, release := range capturedStderrReleases {
		files, err := filepath.Glob(filepath.Join(capturedStderrs, release, "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		held := map[string]bool{}
		for _, path := range files {
			name := strings.TrimSuffix(filepath.Base(path), ".txt")
			held[name] = true
			shape, known := capturedStderrShapes[name]
			if !known {
				t.Errorf("%s is of no shape that capturedStderrShapes names", path)
				continue
			}
			if shape.releases != nil && !slices.Contains(shape.releases, release) {
				t.Errorf("%s is a capture of jj %s, which capturedStderrShapes says does not print %s", path, release, name)
			}
			command, stderr, code := capturedStderr(t, path)
			switch failure := shape.kept == capturedFailure; {
			case !strings.HasPrefix(command, "jj "):
				t.Errorf("%s's first line is %q, want the jj command that made it", path, command)
			case stderr == "":
				t.Errorf("%s holds no standard error", path)
			case failure && code == 0:
				t.Errorf("%s exited 0, want non-zero, after jj's error", path)
			case !failure && code != 0:
				t.Errorf("%s exited %d, want 0", path, code)
			}
			for _, withError := range []bool{false, true} {
				cases = append(cases, jjOnceOnlyCase{path, stderr, withError, shape.kept.want(stderr, withError)})
			}
		}
		for _, name := range slices.Sorted(maps.Keys(capturedStderrShapes)) {
			if shape := capturedStderrShapes[name]; !held[name] && (shape.releases == nil || slices.Contains(shape.releases, release)) {
				t.Errorf("%s holds no capture of %s, which jj %s prints", filepath.Join(capturedStderrs, release), name, release)
			}
		}
	}
	return cases
}

// jjOnceOnlyBlocks are blocks of jj's standard error as jj 0.38.0, 0.45.1-sami and 0.46.0 print
// them, each with whether jjOnceOnly keeps it: a warning jj prints once, a line of jj's of no
// heading, a warning jj prints on every command, each warning with the lines jj prints as its
// parts, and some with jj's colours, and jj's note of a key a read names and no configuration
// sets. They are in an order that puts a block that jjOnceOnly keeps, one with no heading among
// them, right after each that it drops.
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
	{name: "a key a read names and no configuration sets, as jj 0.45 and 0.46 note it",
		text: "Warning: No matching config key for: aliases.nosuch\n"},
	{name: "a key a read names and no configuration sets, with no colon as in jj 0.38",
		text: "Warning: No matching config key for ui.default-command\n"},
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
// `Internal error:` is the one no capture holds, since no command makes it on demand.
var jjOnceOnlyErrors = []string{
	"Error: There is no jj repo in \".\"\n",
	"Config error: Configuration cannot be parsed as TOML document\nCaused by: TOML parse error at line 1, column 9\n  |\n1 | [aliases\n  |         ^\nunclosed table, expected `]`\n\nHint: Check the config file: /home/.config/jj/repos/0123/config.toml\nFor help, see https://docs.jj-vcs.dev/latest/config/ or use `jj help -k config`.\n",
	"Internal error: Unexpected error from backend\nCaused by:\n1: Newly-created commit 0123456789abcdef already exists\n",
	"\x1b[1m\x1b[38;5;1mError: \x1b[39mThere is no jj repo in \".\"\x1b[0m\n",
}

// jjOnceOnlyCase is a text of jj's standard error, whether the filter is to keep jj's error, and
// what the filter gives of it.
type jjOnceOnlyCase struct {
	name, text string
	withError  bool
	want       string
}

// jjOnceOnlyCases are the texts that each filter of jj's standard error is read over, without jj's
// error and with it: each capture of capturedStderrs, which gives what its shape's entry says
// (capturedStderrCases); each block of jjOnceOnlyBlocks alone, which gives the block where its
// kept says so and nothing where not; all of them in one text, which gives the blocks it keeps;
// and all of them followed by each of jjOnceOnlyErrors, which gives the blocks it keeps and, with
// the error, the error whole after them.
func jjOnceOnlyCases(t *testing.T) []jjOnceOnlyCase {
	t.Helper()
	cases := capturedStderrCases(t)
	var all, kept strings.Builder
	for _, block := range jjOnceOnlyBlocks {
		want := ""
		if block.kept {
			want = block.text
			kept.WriteString(block.text)
		}
		all.WriteString(block.text)
		cases = append(cases, jjOnceOnlyCase{block.name, block.text, false, want}, jjOnceOnlyCase{block.name, block.text, true, want})
	}
	cases = append(cases,
		jjOnceOnlyCase{"every block in one text", all.String(), false, kept.String()},
		jjOnceOnlyCase{"every block in one text", all.String(), true, kept.String()})
	for _, failure := range jjOnceOnlyErrors {
		heading, _, _ := strings.Cut(failure, "\n")
		name := "every block, then jj's error " + heading
		cases = append(cases,
			jjOnceOnlyCase{name, all.String() + failure, false, kept.String()},
			jjOnceOnlyCase{name, all.String() + failure, true, kept.String() + failure})
	}
	return cases
}

// The filter of what a jj command of legion jj's own prints on standard error drops each warning jj
// prints on every command, each with the lines jj prints as its parts, and jj's note of a key a
// read names and no configuration sets, and keeps every other line: each of jjOnceOnlyCases gives
// what it wants, every capture of jj 0.38.0, 0.45.1-sami and 0.46.0 among them, a block with jj's
// colours read as it reads with none, and the colours of a block it keeps kept. A line of no
// heading right after a line it drops, such as the snapshot's line of the descendants it rebased,
// is kept. jj's error, each of jjOnceOnlyErrors after those blocks, and every line after it, are
// dropped where the caller asks for no error, and kept where it asks for the error.
func TestTheFilterOfJJsOutputDropsOnlyTheWarningsJJPrintsOnEveryCommand(t *testing.T) {
	t.Parallel()
	for _, c := range jjOnceOnlyCases(t) {
		if got := jjOnceOnly(c.text, c.withError); got != c.want {
			t.Errorf("%s, with error %v: the filter gives\n%q\nwant\n%q", c.name, c.withError, got, c.want)
		}
	}
}

// The pod's wrapper's filter of what jj prints on standard error for one of its reads
// (packages/daemon/docker/jj's once_only) is legion jj's (jjOnceOnly), one rule in two languages:
// read out of the script, from `once_only() {` to the `}` that closes it, with the line that sets
// the escape character it reads and with `set -u`, as the wrapper runs it, and run under /bin/sh
// without the argument `error` and with it, it prints for each of jjOnceOnlyCases, every capture
// of jj's releases among them, what jjOnceOnly gives for the same text, and what the case wants.
func TestThePodWrappersFilterOfJJsOutputIsLegionJJs(t *testing.T) {
	t.Parallel()
	read, err := os.ReadFile(filepath.Join("..", "..", "docker", "jj"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(read), "\n")
	esc := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "esc=") })
	start := slices.Index(lines, "once_only() {")
	if esc < 0 || start < 0 {
		t.Fatalf("packages/daemon/docker/jj holds no line that sets esc (%d) or no `once_only() {` (%d)", esc, start)
	}
	end := slices.Index(lines[start:], "}")
	if end < 0 {
		t.Fatal("packages/daemon/docker/jj holds no `}` that closes once_only")
	}
	program := strings.Join(slices.Concat([]string{"set -u", lines[esc]}, lines[start:start+end+1], []string{`once_only "$1"`}), "\n")
	for _, c := range jjOnceOnlyCases(t) {
		mode := ""
		if c.withError {
			mode = "error"
		}
		cmd := exec.Command("/bin/sh", "-c", program, "once_only", mode)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		cmd.Stdin = strings.NewReader(c.text)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: once_only %q failed: %v\n%s", c.name, mode, err, stderr.String())
		}
		got := string(out)
		if want := jjOnceOnly(c.text, c.withError); got != want || got != c.want {
			t.Errorf("%s: once_only %q prints\n%q\nwant what jjOnceOnly gives\n%q\nand the case wants\n%q", c.name, mode, got, want, c.want)
		}
	}
}
