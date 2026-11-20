package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// capturedDryRuns are the dry runs captured from jj's releases, each in a scratch repository with
// a local bare remote: testdata/jj-push-dry-run/<release>/<form>.txt holds on its first line the
// command that made it, and after it that command's standard error, as jj printed it.
const capturedDryRuns = "testdata/jj-push-dry-run"

// capturedBlockLines is each line inside a block of stderr: every indented line after a block's
// heading `Changes to push to <remote>:`, up to the first line that is not.
func capturedBlockLines(stderr string) []string {
	var lines []string
	in := false
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.HasPrefix(line, "Changes to push to ") && strings.HasSuffix(line, ":") {
			in = true
			continue
		}
		if in && strings.HasPrefix(line, "  ") {
			lines = append(lines, line)
			continue
		}
		in = false
	}
	return lines
}

// capturedSendTo is the commit id a line inside a block adds or moves its ref to, in either
// release's shape: the hex after the line's last ` to `, which ends the line or comes before the
// `]` that ends it.
var capturedSendTo = regexp.MustCompile(` to ([0-9a-f]+)\]?$`)

// capturedSends is each commit id that the block lines add or move a ref to (capturedSendTo),
// each once, in the order of the lines. A line that deletes, `[delete from <id>]` or
// `  Delete …`, holds none, and every other line holds one.
func capturedSends(t *testing.T, lines []string) []string {
	t.Helper()
	var sends []string
	for _, line := range lines {
		deletes := strings.Contains(line, "[delete from ") || strings.HasPrefix(line, "  Delete ")
		parts := capturedSendTo.FindStringSubmatch(line)
		switch {
		case deletes && parts != nil:
			t.Fatalf("the delete line %q holds a commit to send, %s", line, parts[1])
		case !deletes && parts == nil:
			t.Fatalf("the line %q holds no commit to send", line)
		case parts != nil && !slices.Contains(sends, parts[1]):
			sends = append(sends, parts[1])
		}
	}
	return sends
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
// and 0.46.0 is read without an error, so the parser places every line of each block: it answers
// the commit ids those lines add or move a ref to, each once, in order, as this test reads them
// from the lines itself (capturedSends), and none for a line that deletes or a dry run that sends
// nothing. jj's refusal of a commit with no author names that commit. That refusal, a conflicted
// bookmark and 0.38's refusal of a second --remote hold neither a block nor `Nothing changed.`,
// and the parser answers an error that says so.
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
				id, _, ok := jjNoAuthorCommit(stderr)
				if want := capturedNoAuthorCommit(t, stderr); !ok || id != want {
					t.Errorf("jjNoAuthorCommit = %q, %v, want %q, the commit jj refused", id, ok, want)
				}
				fallthrough
			case form == "conflicted-bookmark", form == "two-remotes" && len(blocks) == 0:
				if err == nil || !strings.Contains(err.Error(), "neither a block of changes nor `Nothing changed.`") {
					t.Errorf("parseJJDryRun of jj's refusal = %q, %v, want the error that the output has neither a block nor `Nothing changed.`", got, err)
				}
				return
			}
			if _, _, ok := jjNoAuthorCommit(stderr); ok {
				t.Errorf("jjNoAuthorCommit found a refusal in a dry run that holds none")
			}
			if err != nil {
				t.Fatalf("parseJJDryRun: %v\nof:\n%s", err, stderr)
			}
			if len(blocks) == 0 && !strings.Contains(stderr, "\nNothing changed.\n") {
				t.Fatalf("the fixture holds neither a line in a block nor `Nothing changed.`:\n%s", stderr)
			}
			if want := capturedSends(t, blocks); !slices.Equal(got, want) {
				t.Errorf("parseJJDryRun = %q, want %q, the commit ids its block lines add or move a ref to, each once:\n%s", got, want, stderr)
			}
		})
	}
}

// A block heading with no line under it, which jj never prints, is a dry run the parser cannot
// read, so the step prints its one line for it: the error names that heading, whether the block
// is the only one, the first of two, the last of two, or one that a line outside a block follows.
func TestTheDryRunParserNamesABlockWithNoLine(t *testing.T) {
	const heading = "Changes to push to origin:"
	for _, tc := range []struct{ name, stderr string }{
		{name: "the only block", stderr: heading + "\n"},
		{name: "the first of two", stderr: heading + "\nChanges to push to fork:\n  bookmark: legion/K-1 [add to 0123456789ab]\n"},
		{name: "the last of two", stderr: "Changes to push to fork:\n  Add bookmark legion/K-1 to 0123456789ab\n" + heading + "\n"},
		{name: "a line outside a block after it", stderr: heading + "\nNothing changed.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseJJDryRun(tc.stderr)
			if err == nil || !strings.Contains(err.Error(), heading) {
				t.Errorf("parseJJDryRun = %q, %v, want an error naming %q, of:\n%s", got, err, heading, tc.stderr)
			}
		})
	}
}

// The step finds the commit-id symbols of a revision argument as jj's revset grammar reads it
// (jjIDSymbols), in order: each identifier that is lower-case hex as a whole and that jj reads as
// a symbol, before or after an operator (`|`, `&`, `~`, `::`, `..`, `-`, `+`) or inside a
// function's parentheses, and no commit-id symbol inside a string, of a function's name, of a
// pattern, of a keyword argument's name, beside `@`, or of an identifier with a `-`, a `.` or a
// `*` inside it. A number is such a symbol too: the step's read of the argument with the symbol
// replaced (jjStep.refuses) is what leaves it as the role wrote it. A symbol's place is in the
// push's word that holds the argument, past the option's own spelling.
func TestTheRevisionReaderFindsEachCommitIDSymbol(t *testing.T) {
	t.Parallel()
	c, d := "0123456789abcdef0123456789abcdef01234567", "fedcba9876543210fedcba9876543210fedcba98"
	for _, row := range []struct {
		revision string
		want     []string
	}{
		{revision: c, want: []string{c}},
		{revision: c + "|" + d, want: []string{c, d}},
		{revision: c + " | " + d, want: []string{c, d}},
		{revision: "::" + c, want: []string{c}},
		{revision: c + "::", want: []string{c}},
		{revision: c + "::" + d, want: []string{c, d}},
		{revision: c + "-", want: []string{c}},
		{revision: c + "--", want: []string{c}},
		{revision: c + "+", want: []string{c}},
		{revision: c + ".." + d, want: []string{c, d}},
		{revision: "~" + c + " & mutable()", want: []string{c}},
		{revision: "present(" + c + ")", want: []string{c}},
		{revision: "present(" + c[:12] + ")", want: []string{c[:12]}},
		{revision: "commit_id(" + c + ")", want: []string{c}},
		{revision: "ancestors(" + c + ", 2)", want: []string{c, "2"}},
		{revision: "a-b"},
		{revision: "abc-def"},
		{revision: "abc--def"},
		{revision: "abc*"},
		{revision: "abc.def"},
		{revision: `description("abc")`},
		{revision: `description("a \"` + c + `\" b")`},
		{revision: `description('` + c + `')`},
		{revision: "exact:abc"},
		{revision: "description(exact:abc)"},
		{revision: "abc:def"},
		{revision: "f(abc = " + c + ")", want: []string{c}},
		{revision: "abc@origin"},
		{revision: "main@abc"},
		{revision: "abc@"},
		{revision: "@"},
		{revision: "@-"},
		{revision: "kkmpptxzrspxrzommnulwmwkkqwworpl"},
		{revision: "abc(" + c + ")", want: []string{c}},
	} {
		var got []string
		for _, id := range jjIDSymbols(row.revision, 0, 0) {
			got = append(got, row.revision[id.start:id.end])
		}
		if !slices.Equal(got, row.want) {
			t.Errorf("jjIDSymbols(%q) found %q, want %q", row.revision, got, row.want)
		}
	}
	for _, row := range []struct {
		word   string
		offset int
	}{{word: "-c" + c, offset: 2}, {word: "--change=" + c, offset: 9}, {word: "nm=" + c + "-", offset: 3}} {
		if got, want := jjIDSymbols(row.word, 4, row.offset), []jjIDSymbol{{index: 4, start: row.offset, end: row.offset + len(c)}}; !slices.Equal(got, want) {
			t.Errorf("jjIDSymbols(%q, 4, %d) = %v, want %v", row.word, row.offset, got, want)
		}
	}
}
