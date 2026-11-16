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
	Commits []FixtureCommit `json:"commits"`
}

// FixtureCommit is one commit of a FixtureHistory: its id, its parent and its tree.
type FixtureCommit struct {
	ID     string            `json:"id"`
	Parent string            `json:"parent"`
	Files  map[string]string `json:"files"`
}

// ReadFixtureHistory reads a history.json. It refuses what decodeJSON refuses, a commit with no
// id, and an id on two commits.
func ReadFixtureHistory(r io.Reader) (FixtureHistory, error) {
	var h FixtureHistory
	if err := decodeJSONFrom(r, &h); err != nil {
		return FixtureHistory{}, fmt.Errorf("specgraph: history: %w", err)
	}
	seen := map[string]bool{}
	for _, c := range h.Commits {
		if c.ID == "" {
			return FixtureHistory{}, errors.New("specgraph: history: a commit has no id")
		}
		if seen[c.ID] {
			return FixtureHistory{}, fmt.Errorf("specgraph: history: commit %q is in the history two times", c.ID)
		}
		seen[c.ID] = true
	}
	return h, nil
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
