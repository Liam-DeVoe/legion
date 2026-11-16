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

// ReadTies reads ties in the form of a scenario's ties.json, {"<check id>":["<path or glob>", …]},
// each tie as NewTie takes it. A check that the form does not name has no entry, which reads as a
// tie that is not known. It reads the JSON with decodeJSON, the package's one reader of a JSON
// file of a format, so it refuses what that reader refuses, a check that the object names two
// times among them, and it names the check of a tie that NewTie refuses.
func ReadTies(r io.Reader) (map[string]Tie, error) {
	var paths map[string][]string
	if err := decodeJSONFrom(r, &paths); err != nil {
		return nil, err
	}
	ties := make(map[string]Tie, len(paths))
	for _, check := range slices.Sorted(maps.Keys(paths)) {
		tie, err := NewTie(paths[check])
		if err != nil {
			return nil, fmt.Errorf("check %q: %w", check, err)
		}
		ties[check] = tie
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
