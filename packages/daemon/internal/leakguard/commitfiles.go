package leakguard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// commitRecords is one commit as records. The identities and the message come from the commit
// itself; the added and renamed paths and the added lines come from its files and its diff where
// it has at most one parent (commitFiles), and from its files where it is a merge (mergeFiles).
// The records come in one order whatever the shape, so that a report lists its lines alike from
// one run to the next: the identities, the message, the paths in path order, then the lines in
// path order and, within a file, line order.
//
// A record about a file carries that file's path as its label and the line or the path as its
// text, so that the engine reads the path as a path: the rule file's one path-scoped allowlist
// is anchored on a whole path, and a label carrying anything else would take it away.
func (g *Guard) commitRecords(ctx context.Context, dir, commit string) ([]Record, error) {
	// git writes the separator for itself, as %x00 in the format: an argument of this process's
	// own holding a zero byte is a child that cannot be started at all. %P comes before the
	// body, which is the one field that can hold a newline, so the parents are read without a
	// second git.
	const separator = "%x00"
	out, err := g.git(ctx, dir, "show", "--no-patch", "--no-color",
		"--format="+separator+"%an <%ae>"+separator+"%cn <%ce>"+separator+"%P"+separator+"%B"+separator, commit)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(out, "\x00")
	if len(parts) < 5 {
		return nil, fmt.Errorf("git show %s answered no identity, parents and message", short(commit))
	}
	parents := strings.Fields(parts[3])
	records := []Record{
		{Label: "commit " + short(commit) + " author", Text: parts[1]},
		{Label: "commit " + short(commit) + " committer", Text: parts[2]},
		{Label: "commit " + short(commit) + " message", Text: parts[4]},
	}
	var paths []string
	var added []Record
	if len(parents) > 1 {
		paths, added, err = g.mergeFiles(ctx, dir, commit, parents)
	} else {
		paths, added, err = g.commitFiles(ctx, dir, commit, len(parents))
	}
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	for _, path := range paths {
		records = append(records, Record{Label: path, Text: path, File: path})
	}
	// Stable, so that the lines of one file keep the order they were read in, which is theirs.
	slices.SortStableFunc(added, func(a, b Record) int { return strings.Compare(a.File, b.File) })
	return append(records, added...), nil
}

// diffTree is a diff-tree of a commit of at most one parent, which both lists its changes and
// writes the patch of those it diffs (commitFiles): --root for a root commit, whose diff is
// otherwise empty. Renames are found (-M), so that the diff of a renamed file is of the lines the
// rename changed, and the list and the patch, which both read this shape, find the same ones. A
// merge is read from its files instead (mergeFiles).
//
// Every file's patch is written as text, whatever an attribute says of the file: --text, since a
// file .gitattributes marks -diff or binary, a .gitattributes the pushed commit adds itself
// included, is otherwise "Binary files ... differ" and no line; --no-textconv, so that no diff
// driver's textconv stands another text in for the file's own; and --no-ext-diff, so that no
// external diff program writes the patch. Whether a file is binary is then its own content's to
// say, as it is for a merge's, and a binary file is in no patch (commitFiles).
//
// A git that refuses the diff answers a non-zero status, which Guard.git makes an error of and
// Guard.Push a setup failure: a diff that could not be read refuses the push, and is never a
// commit read as clean.
func diffTree(parents int, args ...string) []string {
	argv := []string{"diff-tree", "-r", "-M", "--no-commit-id", "--text", "--no-textconv", "--no-ext-diff"}
	if parents == 0 {
		argv = append(argv, "--root")
	}
	return append(argv, args...)
}

// commitFiles are the paths a commit of at most one parent adds or renames a file to, which are
// the paths the destination would hold that it does not hold already, and the lines it adds, each
// as a record of the file it is added to and of its number in the commit's version of that file.
//
// Its changes are listed first (commitChanges), and each file a line could be read from is
// classed before any is diffed, from one cat-file --batch that answers the files a blob at a time
// as git writes them (blobBatch). A file is binary as a merge's is, by a zero byte in its first
// 8000 bytes (binary), whatever an attribute says, and of a binary file, the commit's or the
// parent's, the guard holds those bytes alone, whatever its size: cat-file writes the rest, which
// is passed over unkept, and the file is diffed not at all, the commit's adding no line. A text
// file of the commit's is read only where it is at most blobBound bytes, and one above that
// refuses the push as a setup failure naming the file and its size, as a merge's does (mergeFiles
// says why); so does a text file of the parent's that a file of the commit's is diffed against,
// whose removed lines the diff would write. Of the rest:
//
//   - a modification or a rename that leaves the parent's blob as it is, which changes the file's
//     mode or its path alone, adds no line, and nothing of the file is read;
//   - a file the parent has no text of to diff it against — an added file, the far side of a
//     change of type, which git's diff writes as one file removed and another added, and a file
//     that replaces a binary one — adds every line it has, which the batch reads;
//   - a file the commit modifies or renames from a text file of the parent's is diffed, and adds
//     the lines the diff marks as added (addedLines);
//   - and a submodule at a commit the parent's is not at adds the one line git's diff writes of
//     it, "Subproject commit <sha>", as line 1.
//
// One diff answers for every file it diffs, whatever their number, and it diffs nothing else. It
// is of the commit's modifications and renames alone (--diff-filter=MR), so that no file added,
// removed or changed in type is in it, and it leaves out, by pathspec, each modification and
// rename that is not diffed: a binary file, a submodule, a file read whole, and a file left as
// the parent has it, which git's diff would read whole to write no line of. Where none is left
// out, it is the diff of the whole commit, and where nothing is diffed, no diff runs. The paths
// are written on its command line, since no diff command of git reads pathspecs from its standard
// input (--pathspec-from-file is git add's, git commit's and their kin's), and they are as many as
// the modifications and renames that are not diffed: of binary files and submodules, which a
// commit seldom has many of, and of files left as they are, which a commit that moves a directory
// has one of for each file it moves.
func (g *Guard) commitFiles(ctx context.Context, dir, commit string, parents int) ([]string, []Record, error) {
	changes, err := g.commitChanges(ctx, dir, commit, parents)
	if err != nil {
		return nil, nil, err
	}
	var paths, ids []string
	for _, change := range changes {
		if change.status == 'A' || change.status == 'R' {
			paths = append(paths, change.path)
		}
		if change.parent != "" {
			ids = append(ids, change.parent)
		}
		if change.blob != "" {
			ids = append(ids, change.blob)
		}
	}
	batch := g.readBlobs(ctx, dir, ids)
	records, unread, diffed, err := commitLines(batch, commit, changes)
	if err = batch.close(err); err != nil {
		return nil, nil, err
	}
	if diffed {
		lines, err := g.addedLines(ctx, dir, commit, parents, unread)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, lines...)
	}
	return paths, records, nil
}

// commitChange is one path at which a commit of at most one parent differs from its parent.
type commitChange struct {
	// path is where the commit has the file, and from is where the parent has the file it
	// changes: the same path but for a rename.
	path, from string
	// status is diff-tree's letter for the change: A, D, M, R or T.
	status byte
	// blob is the commit's file at path where it is a blob, and "" where the commit has no file
	// there or a submodule, and where a modification or a rename leaves the parent's blob as it
	// is, changing the file's mode or its path alone: such a change adds no line, so nothing of it
	// is read. submodule is the commit the commit's submodule at path is at, and "" where it has
	// none there or renames the parent's submodule at that same commit: git's diff writes a
	// submodule as the one line that names its commit, and writes no line of such a rename.
	blob, submodule string
	// parent is the parent's file at from that the commit's file is diffed against: a blob of the
	// same kind, a regular file or a symbolic link, at a modification or a rename. It is "" for
	// every other change, which git's diff writes as a file of the commit's own, for a change that
	// leaves the parent's blob as it is (blob), and for a rename whose path, or the path it is
	// from, lies under a path another modification or rename names: a pathspec names a
	// directory's files too, so the one that leaves that other change out of the diff would leave
	// this rename out with it (addedLines).
	parent string
}

// paired is whether git's diff writes the change as the parent's file and the commit's together,
// which is what a modification and a rename are, and what the diff of a commit's lines reads
// (addedLines).
func (c commitChange) paired() bool {
	return c.status == 'M' || c.status == 'R'
}

// commitChanges are the paths at which a commit of at most one parent differs from its parent,
// as one diff-tree writes them in its raw format, with its renames found as the diff of the
// commit's lines finds them (diffTree):
//
//	git diff-tree -r -M --no-commit-id -z [--root] <commit>
func (g *Guard) commitChanges(ctx context.Context, dir, commit string, parents int) ([]commitChange, error) {
	out, err := g.git(ctx, dir, diffTree(parents, "-z", commit)...)
	if err != nil {
		return nil, err
	}
	var changes []commitChange
	// named are the paths a modification or a rename is at or comes from.
	named := map[string]bool{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			continue
		}
		// ":<parent's mode> <commit's mode> <parent's blob> <commit's blob> <status>[<score>]",
		// then the path, and for a rename or a copy the path it is from first.
		raw := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		paths := 1
		if len(raw) == 5 && (raw[4][0] == 'R' || raw[4][0] == 'C') {
			paths = 2
		}
		if !strings.HasPrefix(fields[i], ":") || len(raw) != 5 || i+paths >= len(fields) {
			return nil, fmt.Errorf("git diff-tree of commit %s wrote an entry of a shape it does not write", short(commit))
		}
		change := commitChange{from: fields[i+1], path: fields[i+paths], status: raw[4][0]}
		i += paths
		theirs, err := fileKind(raw[0])
		if err != nil {
			return nil, err
		}
		ours, err := fileKind(raw[1])
		if err != nil {
			return nil, err
		}
		switch ours {
		case regularFile, symbolicLink:
			switch {
			case !change.paired() || theirs != ours:
				change.blob = raw[3]
			case raw[2] != raw[3]:
				change.blob, change.parent = raw[3], raw[2]
			}
		case gitlink:
			if !change.paired() || theirs != gitlink || raw[2] != raw[3] {
				change.submodule = raw[3]
			}
		}
		if change.paired() {
			named[change.path], named[change.from] = true, true
		}
		changes = append(changes, change)
	}
	for i, change := range changes {
		if change.status == 'R' && (under(named, change.path) || under(named, change.from)) {
			changes[i].parent = ""
		}
	}
	return changes, nil
}

// under is whether path lies in a directory that paths names, or in one of that directory's own.
func under(paths map[string]bool, path string) bool {
	for i := range len(path) {
		if path[i] == '/' && paths[path[:i]] {
			return true
		}
	}
	return false
}

// commitLines are the lines of commit's files at changes that are read from batch rather than
// diffed (commitFiles), and the paths the diff of the rest leaves out, unread: each modification
// and rename that is not diffed, a rename by both its paths. diffed is whether a change is left to
// the diff. batch answers, for each change with a blob of the commit's, the parent's file first
// where the commit's is diffed against one (commitChange.parent), and then the commit's file.
func commitLines(batch *blobBatch, commit string, changes []commitChange) (records []Record, unread []string, diffed bool, err error) {
	ours, theirs := "commit "+short(commit)+"'s", "the parent of commit "+short(commit)+"'s"
	for _, change := range changes {
		var file string
		switch {
		case change.submodule != "":
			records = append(records, Record{Label: change.path, Text: "Subproject commit " + change.submodule, File: change.path, Line: 1})
		case change.blob == "":
		case change.parent == "":
			if file, _, err = batch.text(change.path, ours, "commit"); err != nil {
				return nil, nil, false, err
			}
		default:
			var diff bool
			if diff, file, err = readPair(batch, change, ours, theirs); err != nil {
				return nil, nil, false, err
			}
			if diff {
				diffed = true
				continue
			}
		}
		records = append(records, fileLines(change.path, file)...)
		if change.paired() {
			unread = append(unread, change.path)
			if change.from != change.path {
				unread = append(unread, change.from)
			}
		}
	}
	return records, unread, diffed, nil
}

// readPair reads the next two blobs of batch, the parent's file at change and then the commit's,
// whose owners theirs and ours name. It answers true where both are text, which the diff of the
// commit's lines then reads (addedLines), and the commit's file where the parent's is binary,
// which holds no line, so that every line of the commit's file is its own. A binary file of the
// commit's answers neither, and a text file of either above blobBound is an error naming it
// (commitFiles).
func readPair(batch *blobBatch, change commitChange, ours, theirs string) (bool, string, error) {
	binaryParent, parentSize, err := batch.passOver()
	if err != nil {
		return false, "", err
	}
	rest, head, err := batch.head()
	if err != nil {
		return false, "", err
	}
	switch size := int64(len(head)) + rest; {
	case binary(head):
		return false, "", batch.skip(rest)
	case size > blobBound:
		return false, "", aboveBound(ours, change.path, size, "commit")
	case binaryParent:
		file, err := batch.whole(head, rest)
		return false, file, err
	case parentSize > blobBound:
		return false, "", aboveBound(theirs, change.from, parentSize, "commit")
	}
	return true, "", batch.skip(rest)
}

// fileLines are the lines of file, the commit's file at path, each as a record with its number in
// the file: what the diff of the file against no file at all marks as added, every line but a
// blank one, with its line ending off as a patch's added line has it off (addedLines).
func fileLines(path, file string) []Record {
	var records []Record
	number := 0
	for line := range strings.SplitSeq(file, "\n") {
		number++
		if text := strings.TrimRight(line, "\r"); strings.TrimSpace(text) != "" {
			records = append(records, Record{Label: path, Text: text, File: path, Line: number})
		}
	}
	return records
}

// addedLines are the lines the diff of a commit of at most one parent adds, of the files
// commitFiles leaves to it, each as a record of the file it is added to and of its number in the
// commit's version of that file. The diff is read with no context lines, so every line of it that
// a patch marks as added is one, and the n-th of a hunk headed "@@ -a[,b] +c[,d] @@" is line
// c+n-1. The "+++ b/<path>" that names the file is read only where a header stands, between a
// "diff --" line and the hunk that follows it, so a line of content that reads like one is not
// mistaken for the name of a file.
//
// The diff is of the commit's modifications and renames alone (--diff-filter=MR), each of unread
// left out by the pathspec :(top,exclude,literal)<path>, which reads the path from the top of the
// repository and as it is written, whatever directory the git runs in and whatever the
// environment says of pathspecs (pathspecsAsWritten). That pathspec leaves out the files of a
// directory of that name too, which are files no modification and no rename this diff reads is
// at (commitChange.parent).
func (g *Guard) addedLines(ctx context.Context, dir, commit string, parents int, unread []string) ([]Record, error) {
	args := diffTree(parents, "--unified=0", "--no-color", "--diff-filter=MR", commit)
	if len(unread) > 0 {
		args = append(args, "--")
		for _, path := range unread {
			args = append(args, ":(top,exclude,literal)"+path)
		}
	}
	out, err := g.git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var records []Record
	path, header, next := "", false, 0
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "diff --"):
			path, header = "", true
		case header && strings.HasPrefix(line, "+++ "):
			path = diffPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@"):
			header = false
			if next, err = hunkStart(line); err != nil {
				return nil, err
			}
		case header || path == "":
		case strings.HasPrefix(line, "+"):
			if text := line[1:]; strings.TrimSpace(text) != "" {
				records = append(records, Record{Label: path, Text: text, File: path, Line: next})
			}
			next++
		}
	}
	return records, nil
}

// hunkStart is c of a hunk header "@@ -a[,b] +c[,d] @@": the number of the line of the commit's
// file that the hunk's first added line is. A header of any other shape is an error rather than a
// line numbered by guess, and the error does not quote it: what follows the second "@@" is a line
// of the file, which no message of this package carries.
func hunkStart(header string) (int, error) {
	fields := strings.Fields(header)
	if len(fields) >= 3 && fields[0] == "@@" && strings.HasPrefix(fields[2], "+") {
		start, _, _ := strings.Cut(fields[2][1:], ",")
		if n, err := strconv.Atoi(start); err == nil {
			return n, nil
		}
	}
	return 0, errors.New("git diff-tree wrote a hunk header of a shape it does not write")
}

// diffPath is the path a patch's "+++" line names, with the b/ prefix off and the C quoting a
// path with a byte git will not print raw undone. A deletion's /dev/null names no file.
func diffPath(field string) string {
	if field == "/dev/null" {
		return ""
	}
	if unquoted, err := strconv.Unquote(field); err == nil && strings.HasPrefix(field, `"`) {
		field = unquoted
	}
	return strings.TrimPrefix(field, "b/")
}
