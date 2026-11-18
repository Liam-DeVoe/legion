package leakguard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// mergeFiles are the paths a merge adds and the lines it adds, read from its files, not from its
// combined diff (the last paragraph says why). What a merge publishes of its own is in the files
// that differ from every parent's: a file one parent has as it is came with that parent, and a
// parent is either a commit this same push adds — read as records of its own, since Guard.commits
// lists every commit the destination does not reach — or a commit the destination already holds.
// Of a file that differs from every parent's, a line that some parent's file at that path holds
// is text that parent brings, wherever the merge puts it, since the rules read a line's text
// alone; a line no parent's file holds is the resolution's own, and is read with its number in
// the merge's file. A path the merge adds is one at which no parent has anything, and it is read
// as a path too.
//
// A file is binary as git's own diff tells one where no attribute says otherwise: a zero byte in
// its first 8000 bytes (xdiff-interface.c's buffer_is_binary). The files are read as blobs, with
// cat-file --batch, which applies no attribute, so a file .gitattributes marks -diff or binary is
// read here as a commit of one parent reads it (diffTree). A binary file of the merge's adds no
// line, as a binary file adds none to a commit of one parent (commitFiles), and a parent's file
// that is binary, absent or a submodule holds none.
//
// The read is bounded, since an issue pod's roles share one memory limit. A text file it takes,
// the merge's own or a parent's at a path the merge changes, is read only where it is at most
// blobBound bytes, and one above that refuses the push as a setup failure naming the file and its
// size. That is the one answer that publishes no line unread: a file passed over would pass every
// line of it, and a file read in part would pass the rest. Of a binary file, the read holds its
// first 8000 bytes alone, whatever its size: cat-file writes the rest, which is passed over
// unkept. The blobs are read path by path as git writes them (blobBatch), the merge's file first
// and then its parents' files there, so what the read holds is one path's: the merge's file, its
// lines no parent's file read so far holds, and the parent's file being read.
//
// A merge is not read from its combined diff (-c), which would be its resolution but for a
// defect: combine-diff.c marks the parent a line was lost from as 1<<parent in an int, so from 33
// parents a "-" lands under parent n-32 and a hunk disagrees with its own counts, and from 64 a
// line only the merge adds is left out (sjawhar/dotfiles 0ab92325). Merges that wide are pushed:
// cybermonklabs/oh-my-pi's release merges on sami have 51 to 53 parents. On its 7ae9bc731cbc, of
// 52 parents, measured 2026-11-17, the combined diff read 2946 added lines in 14 files, where the
// merge's files read none: 14 paths differ from every parent, every line of each is held by some
// parent, and no path is added.
func (g *Guard) mergeFiles(ctx context.Context, dir, merge string, parents []string) ([]string, []Record, error) {
	changes, err := g.mergeChanges(ctx, dir, merge, parents)
	if err != nil {
		return nil, nil, err
	}
	var paths, ids []string
	for _, change := range changes {
		if change.added {
			paths = append(paths, change.path)
		}
		// A path the merge has no file at has no line to read, so its parents' files are not
		// read either.
		if change.blob != "" {
			ids = append(append(ids, change.blob), change.held...)
		}
	}
	batch := g.readBlobs(ctx, dir, ids)
	records, err := mergeLines(batch, merge, changes)
	if err = batch.close(err); err != nil {
		return nil, nil, err
	}
	return paths, records, nil
}

// mergeLines are the lines of merge's files at changes that no parent's file at the same path
// holds, each with its number in the merge's file (mergeFiles), read from batch, which answers
// for each change that has a file of the merge's that file and then its parents' files there.
func mergeLines(batch *blobBatch, merge string, changes []mergeChange) ([]Record, error) {
	ours, theirs := "merge "+short(merge)+"'s", "a parent of merge "+short(merge)+"'s"
	var records []Record
	for _, change := range changes {
		if change.blob == "" {
			continue
		}
		file, ok, err := batch.text(change.path, ours, "merge")
		if err != nil {
			return nil, err
		}
		// own are the merge's lines that no parent's file read so far holds.
		own := map[string]bool{}
		if ok {
			for line := range strings.SplitSeq(file, "\n") {
				if line = strings.TrimSuffix(line, "\r"); strings.TrimSpace(line) != "" {
					own[line] = true
				}
			}
		}
		for range change.held {
			// A parent's file has nothing to take where no line is left, and is passed over.
			if len(own) == 0 {
				if _, _, err := batch.passOver(); err != nil {
					return nil, err
				}
				continue
			}
			of, ok, err := batch.text(change.path, theirs, "merge")
			if err != nil {
				return nil, err
			}
			if ok {
				for line := range strings.SplitSeq(of, "\n") {
					delete(own, strings.TrimSuffix(line, "\r"))
				}
			}
		}
		if len(own) == 0 {
			continue
		}
		number := 0
		for line := range strings.SplitSeq(file, "\n") {
			number++
			// A record's text is a copy, so that the file it was read from is not held for it.
			if text := strings.TrimSuffix(line, "\r"); own[text] {
				records = append(records, Record{Label: change.path, Text: strings.Clone(text), File: change.path, Line: number})
			}
		}
	}
	return records, nil
}

// mergeChange is one path at which a merge's tree differs from every parent's.
type mergeChange struct {
	path string
	// blob is the merge's file there, and "" where the merge has no file there or a submodule,
	// neither of which has a line to read.
	blob string
	// held are the parents' files there, each blob once however many parents have it, of every
	// parent that has a file there that is no submodule.
	held []string
	// added is whether no parent has anything at the path: the diff against each is an A.
	added bool
}

// mergeChanges are the paths at which a merge's tree differs from every parent's, in the order
// the diff against its first parent lists them.
//
// One diff-tree reads every parent, from a line "<merge> <parent>" each on its standard input,
// which it reads as the merge with that parent alone: one git per parent would be 52 for one of
// cybermonklabs/oh-my-pi's release merges. --always writes the merge's id ahead of every parent's
// diff, an empty one too, so the answer splits into one diff per parent in the order they were
// asked for; without it a parent the merge matches would leave no mark, and the next parent's
// diff would be counted as its. Renames are off: a file renamed from a parent's path is a path
// that parent has no file at, and so a path whose every line is read.
func (g *Guard) mergeChanges(ctx context.Context, dir, merge string, parents []string) ([]mergeChange, error) {
	var asked strings.Builder
	for _, parent := range parents {
		asked.WriteString(merge + " " + parent + "\n")
	}
	out, err := g.gitFed(ctx, dir, asked.String(), "diff-tree", "--stdin", "--always", "-r", "--no-renames", "-z")
	if err != nil {
		return nil, err
	}
	// listed is how many of the diffs read so far list a path, counted only while each one has:
	// a path one diff leaves out is one the merge has as that parent has it.
	listed := map[string]int{}
	index := map[string]int{}
	var changes []mergeChange
	diffs := 0
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		switch field := fields[i]; {
		case field == "":
		case !strings.HasPrefix(field, ":"):
			// The raw format's every entry begins with a colon, so this is the id that heads the
			// next parent's diff.
			if field != merge {
				return nil, fmt.Errorf("git diff-tree of merge %s headed a diff with another commit", short(merge))
			}
			diffs++
		default:
			// ":<parent's mode> <merge's mode> <parent's blob> <merge's blob> <status>", then the path.
			raw := strings.Fields(field[1:])
			if diffs == 0 || len(raw) != 5 || i+1 >= len(fields) {
				return nil, fmt.Errorf("git diff-tree of merge %s wrote an entry of a shape it does not write", short(merge))
			}
			i++
			path := fields[i]
			if listed[path] != diffs-1 {
				continue
			}
			listed[path] = diffs
			theirs, err := isBlob(raw[0])
			if err != nil {
				return nil, err
			}
			if diffs == 1 {
				ours, err := isBlob(raw[1])
				if err != nil {
					return nil, err
				}
				change := mergeChange{path: path, added: true}
				if ours {
					change.blob = raw[3]
				}
				index[path] = len(changes)
				changes = append(changes, change)
			}
			change := &changes[index[path]]
			if theirs && !slices.Contains(change.held, raw[2]) {
				change.held = append(change.held, raw[2])
			}
			change.added = change.added && raw[4] == "A"
		}
	}
	if diffs != len(parents) {
		return nil, fmt.Errorf("git diff-tree of merge %s answered %d diffs for its %d parents", short(merge), diffs, len(parents))
	}
	return slices.DeleteFunc(changes, func(change mergeChange) bool { return listed[change.path] != diffs }), nil
}

// The kinds of file a mode diff-tree writes names, its bits under 0o170000: a regular file, and a
// symbolic link, whose target is its content, are each a blob git holds; a gitlink is a
// submodule's commit, of which this repository holds no lines; and 0 is no file, the 000000 of the
// side of a change that has none.
const (
	regularFile  = 0o100000
	symbolicLink = 0o120000
	gitlink      = 0o160000
)

// fileKind is the kind of file a mode of diff-tree's raw format names.
func fileKind(mode string) (uint64, error) {
	bits, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return 0, errors.New("git diff-tree wrote a mode of a shape it does not write")
	}
	return bits & 0o170000, nil
}

// isBlob is whether a mode diff-tree writes is a file git holds as a blob: a regular file, or a
// symbolic link.
func isBlob(mode string) (bool, error) {
	kind, err := fileKind(mode)
	return kind == regularFile || kind == symbolicLink, err
}
