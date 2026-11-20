// Package jjmerge makes the merge commit of two commits in a jj workspace so that it holds git's
// merge (`git merge-tree --write-tree`), because jj merges two commits that have several merge
// bases wrongly and silently (jj-vcs/jj#6369). On each path git conflicts in, the merge holds jj's
// merge of git's own three versions of that path, over one merge base.
package jjmerge

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// gitMerge is what `git merge-tree --write-tree -z` printed for one merge.
type gitMerge struct {
	// Tree is the id of git's merged tree. On a path git conflicts in, it holds what git left
	// there, a file with git's conflict markers as plain text among it, so the merge takes those
	// paths from Conflicts instead.
	Tree string
	// Conflicts are the index entries of every path git conflicts in, in the order git printed
	// them. Empty for a clean merge.
	Conflicts []entry
	// Messages are git's informational messages, in the order git printed them. git prints none
	// for a clean merge unless it is asked to.
	Messages []Message
}

// entry is one conflicted index entry: the version of Path that one of the three commits gives.
// A commit that has no version of the path, as the side that deletes it in a modify/delete
// conflict, has no entry.
type entry struct {
	// Mode is the entry's mode as git prints it: 100644, 100755, 120000 or 160000.
	Mode string
	// OID is the entry's object id: a blob's, or a commit's for a submodule.
	OID string
	// Stage is 1 for the merge base's version, 2 for the first commit's, 3 for the second's.
	Stage int
	Path  string
}

// pathStage names one conflicted index entry: a path and one of its stages.
type pathStage struct {
	path  string
	stage int
}

// Message is one of git's informational messages about the merge.
type Message struct {
	// Paths are the paths the message is about; there is at least one.
	Paths []string
	// Type is git's short name of the message, as "Auto-merging" or "CONFLICT (contents)".
	Type string
	// Text is the message as git words it, without the newline git ends every message with. A
	// path in the text can hold a newline of its own, so only that one newline is taken off.
	Text string
}

// conflictedPaths is each path a conflict record names, once, in the order git first names it:
// the paths where the merge does not take git's tree.
func (m gitMerge) conflictedPaths() []string {
	var paths []string
	seen := make(map[string]bool, len(m.Conflicts))
	for _, e := range m.Conflicts {
		if !seen[e.Path] {
			seen[e.Path] = true
			paths = append(paths, e.Path)
		}
	}
	return paths
}

// parseMergeTree reads the output of `git merge-tree --write-tree -z`. Under -z git quotes no
// path and ends every field with a NUL, so a NUL is the only byte that ends a field, and a path
// can hold any other byte, a tab and a newline included:
//
//	<tree> NUL
//	<mode> SP <oid> SP <stage> TAB <path> NUL                one per conflicted index entry
//	NUL                                                      where the messages begin
//	<count> NUL <path> NUL ... <type> NUL <text> NUL         one per message, with count paths
//
// git prints the tree alone for a clean merge. It prints the empty field before the messages
// whenever it prints messages, a clean merge's too. Anything else is refused, naming what is
// wrong: the merge builds its commits from these entries, and a misread one would put a wrong
// version of a path in them.
func parseMergeTree(out []byte) (gitMerge, error) {
	if len(out) == 0 {
		return gitMerge{}, malformed("it is empty, with no tree id")
	}
	fields := strings.Split(string(out), "\x00")
	// Every field ends with a NUL, so the split's last element is what follows the last NUL.
	if rest := fields[len(fields)-1]; rest != "" {
		return gitMerge{}, malformed("it ends with bytes that no NUL ends: %q", rest)
	}
	fields = fields[:len(fields)-1]

	result := gitMerge{Tree: fields[0]}
	if !isObjectID(result.Tree) {
		return gitMerge{}, malformed("the tree id %q is not an object id", result.Tree)
	}
	// A path's stage names one version of it, so a stage of a path git gives twice is refused:
	// the merge could not tell which version the commit it builds should hold.
	given := make(map[pathStage]bool)
	i := 1
	for ; i < len(fields) && fields[i] != ""; i++ {
		number := len(result.Conflicts) + 1
		e, err := parseEntry(fields[i])
		if err != nil {
			return gitMerge{}, malformed("conflict record %d %q: %v", number, fields[i], err)
		}
		key := pathStage{e.Path, e.Stage}
		if given[key] {
			return gitMerge{}, malformed("conflict record %d %q gives stage %d of %q a second time",
				number, fields[i], e.Stage, e.Path)
		}
		given[key] = true
		result.Conflicts = append(result.Conflicts, e)
	}
	// Skip the empty field that begins the messages, where there is one.
	i++
	for i < len(fields) {
		number := len(result.Messages) + 1
		count, err := strconv.Atoi(fields[i])
		if err != nil || count < 1 {
			return gitMerge{}, malformed("message %d: the path count %q is not a number above 0",
				number, fields[i])
		}
		// After the count come count paths, the type and the text.
		if count > len(fields)-i-3 {
			return gitMerge{}, malformed("message %d: its path count %d runs past the end of the output",
				number, count)
		}
		// The paths share fields' array; its capacity ends at the last path, so an append to
		// them makes a copy rather than writing over the type and the text.
		paths := fields[i+1 : i+1+count : i+1+count]
		result.Messages = append(result.Messages, Message{
			Paths: paths,
			Type:  fields[i+1+count],
			Text:  strings.TrimSuffix(fields[i+2+count], "\n"),
		})
		i += count + 3
	}
	return result, nil
}

// parseEntry reads one conflict record, `<mode> <oid> <stage>\t<path>`. The record is split at
// its first tab, since a path may hold a tab and the fields before it hold none.
func parseEntry(record string) (entry, error) {
	head, path, ok := strings.Cut(record, "\t")
	if !ok {
		return entry{}, errors.New("it has no tab before its path")
	}
	if path == "" {
		return entry{}, errors.New("its path is empty")
	}
	fields := strings.Split(head, " ")
	if len(fields) != 3 {
		return entry{}, fmt.Errorf("%q is not <mode> <oid> <stage>", head)
	}
	mode, oid, stage := fields[0], fields[1], fields[2]
	switch mode {
	case "100644", "100755", "120000", "160000":
	default:
		return entry{}, fmt.Errorf("the mode %q is not one an index entry has", mode)
	}
	if !isObjectID(oid) {
		return entry{}, fmt.Errorf("the object id %q is not an object id", oid)
	}
	switch stage {
	case "1", "2", "3":
	default:
		return entry{}, fmt.Errorf("the stage %q is not 1, 2 or 3", stage)
	}
	return entry{Mode: mode, OID: oid, Stage: int(stage[0] - '0'), Path: path}, nil
}

// isObjectID reports whether s is an object id as git prints one: 40 lowercase hexadecimal
// digits, or 64 in a SHA-256 repository.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// malformed is the error for output that is not what git merge-tree prints.
func malformed(format string, args ...any) error {
	return fmt.Errorf("git merge-tree output: %s", fmt.Sprintf(format, args...))
}
