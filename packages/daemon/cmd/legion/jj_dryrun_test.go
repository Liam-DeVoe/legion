package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// capturedDryRuns are the dry runs captured from jj's releases, each in a scratch repository with
// a local bare remote: testdata/jj-push-dry-run/<release>/<form>.txt holds on its first line the
// command that made it, and after it that command's standard error, as jj printed it.
const capturedDryRuns = "testdata/jj-push-dry-run"

// capturedBlockLine is one line of a captured dry run inside a block `Changes to push to <remote>:`.
type capturedBlockLine struct{ remote, line string }

// capturedBlockLines is each line inside a block of stderr: every indented line after a block's
// heading, up to the first line that is not.
func capturedBlockLines(stderr string) []capturedBlockLine {
	var lines []capturedBlockLine
	remote := ""
	for line := range strings.SplitSeq(stderr, "\n") {
		if heading, ok := strings.CutPrefix(line, "Changes to push to "); ok && strings.HasSuffix(heading, ":") {
			remote = strings.TrimSuffix(heading, ":")
			continue
		}
		if remote != "" && strings.HasPrefix(line, "  ") {
			lines = append(lines, capturedBlockLine{remote: remote, line: line})
			continue
		}
		remote = ""
	}
	return lines
}

// capturedNoAuthorCommit is the commit id jj's refusal of a commit with no author names, the word
// after `Error: Won't push commit `.
func capturedNoAuthorCommit(t *testing.T, stderr string) string {
	t.Helper()
	for line := range strings.SplitSeq(stderr, "\n") {
		if rest, ok := strings.CutPrefix(line, "Error: Won't push commit "); ok {
			id, _, _ := strings.Cut(rest, " ")
			return id
		}
	}
	t.Fatalf("the fixture holds no refusal of a commit with no author:\n%s", stderr)
	return ""
}

// The step reads what a push sends from its dry run, in jj 0.45's shape and in jj 0.38's
// (packages/daemon/cmd/legion/jj_dryrun.go). Every dry run captured from jj 0.45.1-sami, 0.38.0
// and 0.46.0 is read without an error, one ref for each line of each block, each of the block's
// remote, naming the line's bookmark or tag and the commits the line names; a dry run that sends
// nothing reads as Nothing. jj's refusal of a commit with no author names that commit; a
// conflicted bookmark and 0.38's refusal of a second --remote give an error or no ref.
func TestTheDryRunParserPlacesEveryCapturedLine(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(capturedDryRuns, "*", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no captured dry run under %s", capturedDryRuns)
	}
	for _, file := range files {
		release, form := filepath.Base(filepath.Dir(file)), strings.TrimSuffix(filepath.Base(file), ".txt")
		t.Run(release+"/"+form, func(t *testing.T) {
			read, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			command, stderr, _ := strings.Cut(string(read), "\n")
			if !strings.HasPrefix(command, "jj git push --dry-run --ignore-working-copy --color=never") {
				t.Fatalf("the fixture's first line is %q, want the dry run that made it", command)
			}
			blocks := capturedBlockLines(stderr)
			got, err := parseJJDryRun(stderr)
			switch {
			case form == "no-author":
				id, ok := jjNoAuthorCommit(stderr)
				if want := capturedNoAuthorCommit(t, stderr); !ok || id != want {
					t.Errorf("jjNoAuthorCommit = %q, %v, want %q, the commit jj refused", id, ok, want)
				}
				if err == nil && len(got.Refs) != 0 {
					t.Errorf("parseJJDryRun of jj's refusal = %+v, want an error or no ref", got)
				}
				return
			case form == "conflicted-bookmark", form == "two-remotes" && len(blocks) == 0:
				if err == nil && len(got.Refs) != 0 {
					t.Errorf("parseJJDryRun of jj's error = %+v, want an error or no ref", got)
				}
				return
			}
			if _, ok := jjNoAuthorCommit(stderr); ok {
				t.Errorf("jjNoAuthorCommit found a refusal in a dry run that holds none")
			}
			if err != nil {
				t.Fatalf("parseJJDryRun: %v\nof:\n%s", err, stderr)
			}
			if len(got.Refs) != len(blocks) {
				t.Fatalf("parseJJDryRun read %d refs, %+v, want %d, one for each line of each block:\n%s", len(got.Refs), got.Refs, len(blocks), stderr)
			}
			if want := len(blocks) == 0; got.Nothing != want || want && !strings.Contains(stderr, "\nNothing changed.\n") {
				t.Errorf("parseJJDryRun read Nothing = %v, want %v:\n%s", got.Nothing, want, stderr)
			}
			for i, block := range blocks {
				checkCapturedRef(t, got.Refs[i], block)
			}
		})
	}
}

// checkCapturedRef checks the ref the parser read from one line of a block against that line:
// its remote, its kind, a name, an action and commit ids that the line holds, and a commit to send
// for every action but a delete, from the commit it moves or deletes from for every action but an
// add.
func checkCapturedRef(t *testing.T, ref jjPushRef, block capturedBlockLine) {
	t.Helper()
	line, words := block.line, strings.Fields(block.line)
	if ref.Remote != block.remote {
		t.Errorf("%q read as remote %q, want %q", line, ref.Remote, block.remote)
	}
	tag := slices.Contains(words, "tag:") || slices.Contains(words, "tag")
	if ref.Tag != tag {
		t.Errorf("%q read as a tag line %v, want %v", line, ref.Tag, tag)
	}
	if ref.Name == "" || !slices.Contains(words, ref.Name) && !slices.Contains(words, ref.Name+":") {
		t.Errorf("%q read with name %q, want one of its words", line, ref.Name)
	}
	actions := []string{"add", "move forward", "move backward", "move sideways", "delete"}
	if !slices.Contains(actions, ref.Action) || !strings.Contains(strings.ToLower(line), ref.Action) {
		t.Errorf("%q read with action %q, want the line's own, one of %q", line, ref.Action, actions)
	}
	if (ref.Action == "delete") != (ref.To == "") || (ref.Action == "add") != (ref.From == "") {
		t.Errorf("%q read as %s from %q to %q: a delete sends no commit, and an add moves from none", line, ref.Action, ref.From, ref.To)
	}
	for _, id := range []string{ref.From, ref.To} {
		if id != "" && (strings.Trim(id, "0123456789abcdef") != "" || !strings.Contains(line, id)) {
			t.Errorf("%q read with commit %q, want hex that the line holds", line, id)
		}
	}
}
