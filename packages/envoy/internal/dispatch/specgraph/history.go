package specgraph

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"slices"
	"strings"
)

// History is the paths whose content differs between the trees of two commits, both paths of a
// move included, as `git diff --no-renames --name-only -z from to --` gives them with no setting of
// the clone or of the machine (GitHistory); ok is false only when a commit cannot be read. It reads
// trees, never ancestry: main's commits are squash commits, so the commit of a result on a branch is
// no ancestor of the main commit that holds its change.
type History interface {
	Changed(from, to string) ([]string, bool)
}

// FixtureHistory is the history of a scenario, in the form of its history.json:
//
//	{"commits":[{"id","parent","files":{"<repository path>":"<content label>"}}]}
//
// Files is the whole tree of each commit, as a label of each path's content: a path that the commit
// does not hold is no key. It is set at each commit and has each label nonzero, as its mark says
// for a map (memberMark): a label has at least one character, since one with none names no
// content, and two commits with "" at a path would read as one content. By its own rule, a tree
// also holds at least one path (FixtureHistory.check), since a test reads at least one file, and two
// commits with no path would read as one tree. Parent is for the reader, and Changed never reads it.
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
// tree holds no path, or whose tree has a content label with no character, by the order of its
// paths. Each would read as a tree or a content that another such commit shares, so a result would
// carry between them (R8).
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
		switch {
		case c.Files == nil:
			return fmt.Errorf("specgraph: history: commit %s: it sets no files", quoted(c.ID))
		case len(c.Files) == 0:
			return fmt.Errorf("specgraph: history: commit %s: its files hold no path, and the tree of a commit holds at least one file", quoted(c.ID))
		}
		empty, found := "", false
		for path, label := range c.Files {
			if label == "" && (!found || path < empty) {
				empty, found = path, true
			}
		}
		if found {
			return fmt.Errorf("specgraph: history: commit %s: the content label of %s is empty, and a label has at least one character", quoted(c.ID), quoted(empty))
		}
	}
	return nil
}

// historyFault is the fault of h that the entry of the derivation refuses, or nil: a nil value of
// any type that implements History, whose Changed could panic where a result carries, checked once
// for the value whatever its type; a FixtureHistory that FixtureHistory.check refuses; and a
// GitHistory with no Dir, with which git would read the clone of the working directory of the
// process. No History is no history, with which nothing carries.
func historyFault(h History) error {
	if h == nil {
		return nil
	}
	switch v := reflect.ValueOf(h); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		if v.IsNil() {
			return fmt.Errorf("specgraph: history: a nil %T is no history; an Input with no history sets no History", h)
		}
	}
	switch h := h.(type) {
	case FixtureHistory:
		return h.check()
	case *FixtureHistory:
		return h.check()
	case GitHistory:
		return h.dirFault()
	case *GitHistory:
		return h.dirFault()
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
	Dir string `input:"nonzero"`
}

// dirFault is the refusal of g with no Dir, or nil.
func (g GitHistory) dirFault() error {
	if g.Dir == "" {
		return errors.New("specgraph: history: a GitHistory sets no Dir, so git would read the clone of the working directory of the process")
	}
	return nil
}

// gitDiffArgs are the arguments of git that Changed runs before its two commits, each one there so
// that no setting of the clone or of the machine changes the answer (TestGitHistoryIsTheSameUnderEachSetting):
//
//   - --no-replace-objects: a replace ref would put another commit, and its tree, in the place of one;
//   - --no-renames: with diff.renames, a move would give its new path alone;
//   - --ignore-submodules=none: ignore = all for a submodule in .gitmodules or in the config, or
//     diff.ignoreSubmodules, would leave out a moved submodule;
//   - --no-relative: with diff.relative, a Dir under the top of the clone would give only its own
//     paths, relative to it;
//   - --name-only -z: each path as the tree writes it, whatever core.quotePath, with no quote of a
//     letter that is not ASCII, a blank, a quote or a line feed;
//   - --end-of-options: an id that starts with "-" is a revision, never an option.
//
// It passes no flag for diff.external, a textconv of .gitattributes or color: git runs none of them
// for a list of names, which a row of the test holds. Changed also runs git with no variable GIT_*
// of the environment that it would inherit (exec.Cmd.Environ), such as GIT_DIR, which would make git
// read another clone than Dir.
var gitDiffArgs = []string{"--no-replace-objects", "diff", "--no-renames", "--ignore-submodules=none", "--no-relative",
	"--name-only", "-z", "--end-of-options"}

// Changed runs `git -C <Dir> <gitDiffArgs> <from> <to> --` and gives its paths, sorted; ok is false
// when git exits with an error, as for a commit that the clone does not hold, such as a commit
// beyond the depth of a shallow clone.
func (g GitHistory) Changed(from, to string) ([]string, bool) {
	cmd := exec.Command("git", append(append([]string{"-C", g.Dir}, gitDiffArgs...), from, to, "--")...)
	cmd.Env = slices.DeleteFunc(cmd.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_") })
	out, err := cmd.Output()
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
