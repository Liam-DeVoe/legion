package specgraph

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// Tie is the files that a check's test reads (R18), as repository paths in three forms: a path,
// "<dir>/*" (the files directly in dir) and "<dir>/**" (every file under dir). Known is false when
// no tie could be derived, and Why says why (R18: "A tie that Dispatch cannot derive reads as
// unknown, not as none").
type Tie struct {
	Paths []string
	Known bool
	Why   string
}

// NewTie is the known tie of paths, sorted and unique. It refuses an entry in any other form, naming
// it: an entry is relative, its parts are separated by "/", none of them is empty, "." or "..",
// and none holds "\" or "*", except a last part "*" or "**" after a directory. It also refuses a
// tie of no path, which would read as none: a test reads at least its own file.
func NewTie(paths []string) (Tie, error) {
	if len(paths) == 0 {
		return Tie{}, errors.New("specgraph: a tie holds no path")
	}
	for _, entry := range paths {
		if !isTieEntry(entry) {
			return Tie{}, fmt.Errorf("specgraph: tie entry %q is not a repository path, <dir>/* or <dir>/**", entry)
		}
	}
	return Tie{Paths: slices.Compact(slices.Sorted(slices.Values(paths))), Known: true}, nil
}

// CommitTies is the tie of each check at each commit, by commit and then by check id, since a tie
// is a fact of a commit (R18: "At each commit, Dispatch derives four ties"). A check that the map
// of a commit does not hold has a tie that is not known at that commit, and so has each check at a
// commit that it does not hold.
type CommitTies map[string]map[string]Tie

// commitTiesForm is the form of a scenario's ties.json, which ReadCommitTies reads.
const commitTiesForm = `{"<commit>":{"<check id>":["<path or pattern>", …]}}`

// ReadCommitTies reads ties in the form of a scenario's ties.json (commitTiesForm), each tie as
// NewTie takes it. It reads the JSON with decodeJSON, the package's one reader of a JSON file of a
// format, so it refuses what that reader refuses, but a value of another type, such as an array
// in the place of a commit's object, which is ties by check alone, it names by the form and the
// place of the value, "it is not in the form <form>: the value at <JSON pointer> is of another
// type", since the Go type that encoding/json/v2 names says nothing to the writer of the file. It
// refuses a commit of no letter and no digit (hasWord), as the scenario reader refuses such a
// commit of a subject, since it names no commit, and it names the commit and the check of a tie
// that NewTie refuses.
func ReadCommitTies(r io.Reader) (CommitTies, error) {
	var paths map[string]map[string][]string
	if err := decodeJSONFrom(r, &paths); err != nil {
		var semantic *json.SemanticError
		switch {
		case !errors.As(err, &semantic) || semantic.GoType == nil:
			return nil, err
		case semantic.JSONPointer == "":
			return nil, fmt.Errorf("it is not in the form %s", commitTiesForm)
		}
		return nil, fmt.Errorf("it is not in the form %s: the value at %q is of another type", commitTiesForm, semantic.JSONPointer)
	}
	ties := make(CommitTies, len(paths))
	for _, commit := range slices.Sorted(maps.Keys(paths)) {
		if !hasWord(commit) {
			return nil, fmt.Errorf("commit %q: a commit of no letter and no digit names no commit", commit)
		}
		ties[commit] = make(map[string]Tie, len(paths[commit]))
		for _, check := range slices.Sorted(maps.Keys(paths[commit])) {
			tie, err := NewTie(paths[commit][check])
			if err != nil {
				return nil, fmt.Errorf("commit %q: check %q: %w", commit, check, err)
			}
			ties[commit][check] = tie
		}
	}
	return ties, nil
}

// isTieEntry is whether entry has one of the three forms of a tie.
func isTieEntry(entry string) bool {
	parts := strings.Split(entry, "/")
	if last := parts[len(parts)-1]; last == "*" || last == "**" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `*\`) {
			return false
		}
	}
	return true
}

// Covers is whether the tie holds path: an entry equal to it, a "<dir>/*" entry of the directory
// that holds it, or a "<dir>/**" entry of a directory above it. It is false when the tie is not
// known.
func (t Tie) Covers(path string) bool {
	if !t.Known {
		return false
	}
	for _, entry := range t.Paths {
		dir, last := entry, ""
		if i := strings.LastIndexByte(entry, '/'); i >= 0 {
			dir, last = entry[:i], entry[i+1:]
		}
		rest, under := strings.CutPrefix(path, dir+"/")
		switch {
		case last == "**" && under && rest != "":
			return true
		case last == "*" && under && rest != "" && !strings.Contains(rest, "/"):
			return true
		case entry == path:
			return true
		}
	}
	return false
}
