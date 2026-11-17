package specgraph

import (
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
	Paths []string `input:"zero: a tie that is not known has no path; Input.check refuses a known tie with none"`
	Known bool     `input:"zero: a tie that is not known, which carries no result (R18)"`
	Why   string   `input:"zero: no reason is given; no rule reads it"`
}

// NewTie is the known tie of paths, sorted and unique. It refuses an entry in any other form, naming
// it: an entry is relative, its parts are separated by "/", none of them is empty, "." or "..",
// and none holds "\" or "*", except a last part "*" or "**" after a directory. It also refuses a
// tie of no path, which would read as none: a test reads at least its own file.
func NewTie(paths []string) (Tie, error) {
	if err := tiePathsFault(paths); err != nil {
		return Tie{}, err
	}
	return Tie{Paths: slices.Compact(slices.Sorted(slices.Values(paths))), Known: true}, nil
}

// tiePathsFault is the fault of paths that NewTie refuses, or nil. The entry of the derivation
// refuses a known Tie by it too, since a known tie with no path would cover no change.
func tiePathsFault(paths []string) error {
	if len(paths) == 0 {
		return errors.New("specgraph: a tie holds no path")
	}
	for _, entry := range paths {
		if !isTieEntry(entry) {
			return fmt.Errorf("specgraph: tie entry %s is not a repository path, <dir>/* or <dir>/**", quoted(entry))
		}
	}
	return nil
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
// format, so it refuses what that reader refuses, and names commitTiesForm and the place of a value
// of another type, such as an array in the place of a commit's object, which is ties by check
// alone. It refuses a commit of no letter and no digit (hasWord), as the scenario reader refuses
// such a commit of a subject, since it names no commit, and it names the commit and the check of a
// tie that NewTie refuses.
func ReadCommitTies(r io.Reader) (CommitTies, error) {
	var paths map[string]map[string][]string
	if err := decodeJSONFrom(r, &paths, commitTiesForm); err != nil {
		return nil, err
	}
	ties := make(CommitTies, len(paths))
	for _, commit := range slices.Sorted(maps.Keys(paths)) {
		if !hasWord(commit) {
			return nil, fmt.Errorf("commit %s: a commit of no letter and no digit names no commit", quoted(commit))
		}
		ties[commit] = make(map[string]Tie, len(paths[commit]))
		for _, check := range slices.Sorted(maps.Keys(paths[commit])) {
			tie, err := NewTie(paths[commit][check])
			if err != nil {
				return nil, fmt.Errorf("commit %s: check %s: %w", quoted(commit), quoted(check), err)
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
