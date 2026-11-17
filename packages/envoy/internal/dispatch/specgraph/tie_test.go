package specgraph

import (
	"encoding/json/v2"
	"errors"
	"io"
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

// ReadTies reads the ties form of a scenario's ties.json: one known tie for each check it names,
// none for a check it does not, and a refusal of what decodeJSON refuses (text after the object, a
// check named two times) and of a tie that NewTie refuses, naming the check.
func TestReadTies(t *testing.T) {
	t.Parallel()
	ties, err := ReadTies(strings.NewReader(`{"check-b":["b/*"],"check-a":["go.work","a/**"]}` + "\n"))
	if err != nil || len(ties) != 2 || !ties["check-a"].Known || !slices.Equal(ties["check-a"].Paths, []string{"a/**", "go.work"}) || !ties["check-b"].Covers("b/x.go") {
		t.Fatalf("ReadTies = %+v, %v; want the two known ties", ties, err)
	}
	for _, row := range refusalRows(`{"check-a":`, `}`) {
		_, err := ReadTies(strings.NewReader(row.text))
		row.check(t, "ReadTies", err)
	}
}

// ReadCommitTies reads ties by commit, the form of spec-check citations --ties: the ties of each
// commit it names, a check that a commit does not name having none, and the refusals of ReadTies,
// each one naming the commit too, and a commit key with no letter and no digit.
func TestReadCommitTies(t *testing.T) {
	t.Parallel()
	ties, err := ReadCommitTies(strings.NewReader(`{"c2":{"check-a":["a/*"]},"c1":{"check-a":["go.work","a/**"],"check-b":["b/*"]}}` + "\n"))
	if err != nil || len(ties) != 2 || !slices.Equal(ties["c1"]["check-a"].Paths, []string{"a/**", "go.work"}) ||
		!ties["c1"]["check-b"].Covers("b/x.go") || !ties["c2"]["check-a"].Covers("a/x.go") || len(ties["c2"]) != 1 {
		t.Fatalf("ReadCommitTies = %+v, %v; want the ties of c1 and c2", ties, err)
	}
	for _, row := range refusalRows(`{"c1":{"check-a":`, `}}`) {
		_, err := ReadCommitTies(strings.NewReader(row.text))
		if strings.HasPrefix(row.ours, "check ") {
			row.ours = `commit "c1": ` + row.ours
		}
		row.check(t, "ReadCommitTies", err)
	}
	if _, err := ReadCommitTies(strings.NewReader(`{"--":{"check-a":["a/*"]}}`)); err == nil || !strings.Contains(err.Error(), `commit "--": names no commit`) {
		t.Errorf(`ReadCommitTies of the commit "--" = %v; want the refusal that names it`, err)
	}
}

// refusalRow is a text that a reader of ties refuses, and how to know the refusal: by the words
// that this package's code writes (ours), or by the kind of a library's error (is). A library's
// own words are never matched: encoding/json/v2 writes "cannot" or "unable to" by a choice made
// once per process, on purpose.
type refusalRow struct {
	text, ours string
	is         func(error) bool
	kind       string
}

func (r refusalRow) check(t *testing.T, reader string, err error) {
	t.Helper()
	switch {
	case err == nil:
		t.Errorf("%s(%q) = nil error, want a refusal", reader, r.text)
	case r.is != nil && !r.is(err):
		t.Errorf("%s(%q) = %v, want an error that is %s", reader, r.text, err, r.kind)
	case r.is == nil && !strings.Contains(err.Error(), r.ours):
		t.Errorf("%s(%q) = %v, want an error holding %q", reader, r.text, err, r.ours)
	}
}

// refusalRows are the refusals of the ties of check-a inside prefix and suffix, the shape of one
// reader, beside the texts that are no such shape at all.
func refusalRows(prefix, suffix string) []refusalRow {
	semanticArray := func(err error) bool {
		var semantic *json.SemanticError
		return errors.As(err, &semantic) && semantic.JSONKind == '['
	}
	return []refusalRow{
		{text: prefix + `["a/*"]` + suffix + ` {}`, ours: "text after the JSON value"},
		{text: strings.Replace(prefix, `"check-a":`, `"check-a":["a/*"],"check-a":`, 1) + `["b/*"]` + suffix, ours: `is in one object two times`},
		{text: prefix + `["a/*.go"]` + suffix, ours: `check "check-a": specgraph: tie entry "a/*.go"`},
		{text: prefix + `[]` + suffix, ours: `check "check-a": specgraph: a tie holds no path`},
		{text: `["a/*"]`, is: semanticArray, kind: "a *json.SemanticError of a JSON array"},
		{text: prefix + `["a/*"]`, is: func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }, kind: "io.ErrUnexpectedEOF"},
	}
}
