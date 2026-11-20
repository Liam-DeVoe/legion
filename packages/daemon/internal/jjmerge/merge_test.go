package jjmerge

import (
	"context"
	"strings"
	"testing"
)

// A path is one file to jj whatever it holds: its quotes and backslashes are escaped inside the
// fileset's string, a control character in it, as a newline, is jj's escape of it, so the fileset
// is one line, and nothing else of it is read as fileset syntax.
func TestRootFileNamesOnePath(t *testing.T) {
	for path, want := range map[string]string{
		"f":                `root-file:"f"`,
		"-odd (path) x":    `root-file:"-odd (path) x"`,
		`quote " and \ it`: `root-file:"quote \" and \\ it"`,
		"odd\npath\t\x01":  `root-file:"odd\npath\t\x01"`,
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
