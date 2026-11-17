package specgraph

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
)

// History is the paths whose content differs between the trees of two commits, both paths of a
// move included, as `git diff --no-renames --name-only -z from to --` gives them; ok is false only
// when a commit cannot be read. It reads trees, never ancestry: main's commits are squash commits,
// so the commit of a result on a branch is no ancestor of the main commit that holds its change.
type History interface {
	Changed(from, to string) ([]string, bool)
}

// FixtureHistory is the history of a scenario, in the form of its history.json:
//
//	{"commits":[{"id","parent","files":{"<repository path>":"<content label>"}}]}
//
// Files is the whole tree of each commit, as a label of each path's content. Parent is for the
// reader, and Changed never reads it.
type FixtureHistory struct {
	Commits []FixtureCommit `json:"commits" input:"present: no commit, so no result carries; a file sets [] for none"`
}

// FixtureCommit is one commit of a FixtureHistory: its id, its parent and its tree.
type FixtureCommit struct {
	ID     string            `json:"id" input:"nonzero"`
	Parent string            `json:"parent" input:"zero: a commit with no parent; Changed never reads it"`
	Files  map[string]string `json:"files" input:"nonzero"`
}

// historyForm is the form of history.json, with the type of each value, as ReadFixtureHistory
// names it for a value of another type.
const historyForm = `{"commits":[{"id":"<commit>","parent":"<commit>","files":{"<repository path>":"<content label>"}}, …]}`

// ReadFixtureHistory reads a history.json. It refuses what decodeJSON refuses, a value of another
// type than historyForm needs named by its place among them, a file that does not set commits
// (unsetFault), and what FixtureHistory.check refuses.
func ReadFixtureHistory(r io.Reader) (FixtureHistory, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return FixtureHistory{}, fmt.Errorf("specgraph: history: %w", err)
	}
	var h FixtureHistory
	if err := decodeJSON(data, &h, historyForm); err != nil {
		return FixtureHistory{}, fmt.Errorf("specgraph: history: %w", err)
	}
	if err := unsetFault(data, &h, historyForm); err != nil {
		return FixtureHistory{}, fmt.Errorf("specgraph: history: %w", err)
	}
	if err := h.check(); err != nil {
		return FixtureHistory{}, err
	}
	return h, nil
}

// check is the first fault of h that ReadFixtureHistory and the entry of the derivation refuse,
// or nil: a commit with no id, an id on two commits, and then a commit that sets no files, whose
// tree would read as empty, so that two such commits would read as one tree and a result would
// carry between them (R8). A commit whose files are {} holds the empty tree.
func (h FixtureHistory) check() error {
	seen := map[string]bool{}
	for _, c := range h.Commits {
		if c.ID == "" {
			return errors.New("specgraph: history: a commit has no id")
		}
		if seen[c.ID] {
			return fmt.Errorf("specgraph: history: commit %s is in the history two times", quoted(c.ID))
		}
		seen[c.ID] = true
	}
	for _, c := range h.Commits {
		if c.Files == nil {
			return fmt.Errorf("specgraph: history: commit %s: it sets no files", quoted(c.ID))
		}
	}
	return nil
}

// historyFault is the fault of h that FixtureHistory.check gives when h is a FixtureHistory, an
// error for a nil *FixtureHistory, whose Changed would panic where a result carries, and nil for any
// other History, such as GitHistory, which reads the trees of a clone, and for no History, with
// which nothing carries.
func historyFault(h History) error {
	switch h := h.(type) {
	case FixtureHistory:
		return h.check()
	case *FixtureHistory:
		if h == nil {
			return errors.New("specgraph: history: a nil *FixtureHistory holds no commit; an Input with no history sets no History")
		}
		return h.check()
	}
	return nil
}

// Changed is the sorted paths whose label differs between the trees of from and to, or that one
// of the trees does not hold, so a move gives both of its paths; ok is false when the history
// holds no commit with one of the two ids.
func (h FixtureHistory) Changed(from, to string) ([]string, bool) {
	a, okFrom := h.tree(from)
	b, okTo := h.tree(to)
	if !okFrom || !okTo {
		return nil, false
	}
	var changed []string
	for path, label := range a {
		if other, held := b[path]; !held || other != label {
			changed = append(changed, path)
		}
	}
	for path := range b {
		if _, held := a[path]; !held {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	return changed, true
}

// tree is the files of the commit id, and whether the history holds it.
func (h FixtureHistory) tree(id string) (map[string]string, bool) {
	i := slices.IndexFunc(h.Commits, func(c FixtureCommit) bool { return c.ID == id })
	if i < 0 {
		return nil, false
	}
	return h.Commits[i].Files, true
}

// GitHistory is the history of the git clone at Dir.
type GitHistory struct {
	Dir string
}

// Changed runs `git -C <Dir> diff --no-renames --name-only -z --end-of-options <from> <to> --` and
// gives its paths, sorted; ok is false when git exits with an error, as for a commit that the clone
// does not hold. --end-of-options makes git read an id that starts with "-" as a revision, never as
// an option.
func (g GitHistory) Changed(from, to string) ([]string, bool) {
	out, err := exec.Command("git", "-C", g.Dir, "diff", "--no-renames", "--name-only", "-z", "--end-of-options", from, to, "--").Output()
	if err != nil {
		return nil, false
	}
	changed := strings.Split(string(bytes.TrimSuffix(out, []byte{0})), "\x00")
	if len(out) == 0 {
		changed = nil
	}
	slices.Sort(changed)
	return changed, true
}
