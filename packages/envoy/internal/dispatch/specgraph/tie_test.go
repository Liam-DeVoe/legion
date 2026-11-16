package specgraph

import (
	"slices"
	"strings"
	"testing"
)

// A tie covers a path it names, each file directly in a "<dir>/*" directory and each file under a
// "<dir>/**" directory, and no other path: not a file one level deeper than "<dir>/*", not a path
// that only starts with a tied path, and not a file in a directory that only starts with a tied
// directory's name.
func TestTieCovers(t *testing.T) {
	t.Parallel()
	tie, err := NewTie([]string{"p/testdata/**", "go.work", "p/*", "go.work"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"go.work", "p/*", "p/testdata/**"}; !tie.Known || !slices.Equal(tie.Paths, want) {
		t.Errorf("NewTie = %+v, want known, paths %q", tie, want)
	}
	for path, want := range map[string]bool{
		"go.work":             true,
		"p/a.go":              true,
		"p/testdata/x/y.json": true,
		"p/testdata/z.json":   true,
		"p/q/b.go":            false,
		"go.work.sum":         false,
		"pp/a.go":             false,
		"p":                   false,
		"p/testdata":          true, // a file named testdata directly in p
		"p/testdatax/y.json":  false,
	} {
		if got := tie.Covers(path); got != want {
			t.Errorf("Covers(%q) = %t, want %t", path, got, want)
		}
	}
	for _, path := range []string{"go.work", "p/a.go", "p/testdata/x/y.json", ""} {
		if (Tie{Known: false, Paths: []string{"go.work", "p/**"}}).Covers(path) {
			t.Errorf("a tie that is not known covers %q", path)
		}
	}
}

// NewTie refuses an entry in any form but a repository path, "<dir>/*" and "<dir>/**", and names
// the entry. It refuses a tie of no path, which would read as none (R18: a tie that Dispatch cannot
// derive "reads as unknown, not as none").
func TestNewTieRefusesAnotherForm(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"p/*.go", "p/**/x", "", "/abs", "*", "**", "p/", "p//a.go", "./p/a.go", "p/../q", `p\a.go`} {
		_, err := NewTie([]string{"go.work", entry})
		if err == nil {
			t.Errorf("NewTie accepts %q", entry)
			continue
		}
		if quoted := `"` + strings.ReplaceAll(entry, `\`, `\\`) + `"`; !strings.Contains(err.Error(), quoted) {
			t.Errorf("NewTie(%q): %v, want the entry %s named", entry, err, quoted)
		}
	}
	if _, err := NewTie(nil); err == nil {
		t.Error("NewTie accepts a tie of no path")
	}
}
