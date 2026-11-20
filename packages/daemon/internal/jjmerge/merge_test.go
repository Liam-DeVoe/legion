package jjmerge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The Legion-Merge trailer joins the description's last paragraph when that paragraph is a block
// of trailers, as a pane's overlay writes one, and is a paragraph of its own otherwise. The
// subject is never a trailer block, though a conventional subject reads as `Key: value`: a
// trailer joined to it would become part of the subject.
func TestWithTrailerPutsTheLineLast(t *testing.T) {
	const line = "Legion-Merge: 0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct{ description, want string }{
		{"Merge main\n\nLegion-Role: implementer\n", "Merge main\n\nLegion-Role: implementer\n" + line + "\n"},
		{"Merge main\n\nOmp-Session: s-1\nLegion-Role: tester\n", "Merge main\n\nOmp-Session: s-1\nLegion-Role: tester\n" + line + "\n"},
		{"Merge main\n", "Merge main\n\n" + line + "\n"},
		{"fix: merge main\n", "fix: merge main\n\n" + line + "\n"},
		{"Merge main\n\nThe body says why.\n", "Merge main\n\nThe body says why.\n\n" + line + "\n"},
		{"Merge main\n\nLegion-Role: implementer\nnot a trailer\n", "Merge main\n\nLegion-Role: implementer\nnot a trailer\n\n" + line + "\n"},
	} {
		if got := withTrailer(tc.description, line); got != tc.want {
			t.Errorf("withTrailer(%q) = %q, want %q", tc.description, got, tc.want)
		}
	}
}

// The run's line leaves the description as jj would have written it with no such line: the
// trailers jj added beside it stay in their paragraph, and a paragraph that held the line alone,
// as jj writes it with no trailer configured, goes with it. Any other paragraph stays as it is.
func TestWithoutLineTakesTheRunsLineOut(t *testing.T) {
	const line = "Legion-Merge-Run: legion-merge-default-0123456789abcdef-"
	for _, tc := range []struct{ description, want string }{
		{"Merge main\n\n" + line + "\nLegion-Role: implementer\n", "Merge main\n\nLegion-Role: implementer\n"},
		{"Merge main\n\n" + line + "\n", "Merge main\n"},
		{"Merge main\n\nThe body says why.\n\n" + line + "\n", "Merge main\n\nThe body says why.\n"},
		{"Merge main\n\nLegion-Role: implementer\n", "Merge main\n\nLegion-Role: implementer\n"},
	} {
		if got := withoutLine(tc.description, line); got != tc.want {
			t.Errorf("withoutLine(%q) = %q, want %q", tc.description, got, tc.want)
		}
	}
}

// A path is one file to jj whatever it holds: its quotes and backslashes are escaped inside the
// fileset's string, and nothing else of it is read as fileset syntax.
func TestRootFileNamesOnePath(t *testing.T) {
	for path, want := range map[string]string{
		"f":                `root-file:"f"`,
		"-odd (path) x":    `root-file:"-odd (path) x"`,
		`quote " and \ it`: `root-file:"quote \" and \\ it"`,
	} {
		if got := RootFile(path); got != want {
			t.Errorf("RootFile(%q) = %s, want %s", path, got, want)
		}
	}
}

// runnerFunc is a Runner that answers each command with a function's answer.
type runnerFunc func(c Command) ([]byte, error)

func (f runnerFunc) Run(_ context.Context, c Command) ([]byte, error) { return f(c) }

// git merge-tree exits 1 also where it cannot merge at all, as for jj's root commit as a parent,
// and then prints no merge. The refusal names what git said, not only that its output is empty.
func TestMergeTreeRefusalNamesWhatGitSaid(t *testing.T) {
	const said = "merge-tree: 0000000000000000000000000000000000000000 - not something we can merge"
	m := &merger{gitDir: "/repo/.git", request: Request{Workspace: "/repo"}, run: runnerFunc(func(c Command) ([]byte, error) {
		return nil, &ExitError{Args: c.Args, Code: 1, Stderr: said + "\n"}
	})}
	_, err := m.mergeTree(context.Background(), [2]string{strings.Repeat("0", 40), strings.Repeat("a", 40)})
	if err == nil || !strings.Contains(err.Error(), said) {
		t.Errorf("mergeTree = %v, want a refusal that names what git said, %q", err, said)
	}
}

// A child that a signal ends is named with the signal, in the words of Go's process state, and
// not as an exit status of -1 with nothing after it.
func TestExecNamesTheSignalThatEndedACommand(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), Command{Dir: t.TempDir(), Args: []string{"sh", "-c", "kill -TERM $$"}})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Signal != "signal: terminated" {
		t.Fatalf("Exec.Run of a command a signal ends = %#v, want an *ExitError whose Signal is %q", err, "signal: terminated")
	}
	if got := err.Error(); got != "sh -c kill -TERM $$ ended with signal: terminated" {
		t.Errorf("the error reads %q, want it to name the signal", got)
	}
}
