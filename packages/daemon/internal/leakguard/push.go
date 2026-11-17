package leakguard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// gitPins is the configuration every git this package starts reads last: no hook and no
// file-system monitor runs, whatever the repository's own configuration names; no replacement
// object stands in for a commit, so the records read are the commits the push would send; and a
// path is printed as it is, rather than with its non-ASCII bytes escaped, so that what the rules
// read is the path the destination would hold.
var gitPins = []string{
	"--no-replace-objects",
	"-c", "core.quotePath=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
}

// gitArgv is one git command with the pins in front of it, which is how every git of this
// package is started: a shape written out twice is one a later pin can be added to once.
func gitArgv(args ...string) []string {
	return append(append([]string{"git"}, gitPins...), args...)
}

// Update is one line of what git hands a pre-push hook on standard input: the ref being pushed,
// the commit it is at, the ref it is being pushed to, and the commit the destination holds there.
// A sha of all zeros means the ref is not there: on the local side a deletion, on the remote side
// a ref the push creates.
type Update struct {
	LocalRef  string
	LocalSHA  string
	RemoteRef string
	RemoteSHA string
}

// Line is the update as git writes it to a pre-push hook, which is how the shared check reads it.
func (u Update) Line() string {
	return strings.Join([]string{u.LocalRef, u.LocalSHA, u.RemoteRef, u.RemoteSHA}, " ")
}

// ref is the name the destination would hold, which is the name a push publishes. A deletion
// names only the remote ref; a push of a local ref to no remote ref at all can only report the
// local one.
func (u Update) ref() string {
	if u.RemoteRef != "" {
		return u.RemoteRef
	}
	return u.LocalRef
}

// deletion is whether the update deletes its ref: the local side names no commit, so it adds
// none.
func (u Update) deletion() bool {
	return u.LocalSHA == "" || zeroSHA(u.LocalSHA)
}

// ParseUpdates reads git's pre-push lines, four fields a line, as a `legion leak-check` standing
// in for a pre-push hook reads them from its own standard input. A line with another field count
// is refused rather than guessed at: a check that misread its input would scan the wrong commits.
func ParseUpdates(r io.Reader) ([]Update, error) {
	var updates []Update
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("a pre-push line holds %d fields, not the four git writes", len(fields))
		}
		updates = append(updates, Update{LocalRef: fields[0], LocalSHA: fields[1], RemoteRef: fields[2], RemoteSHA: fields[3]})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

// PushRequest is one push: the repository it is made from, the remote it is made to, and what it
// would move there.
type PushRequest struct {
	// Dir is the repository the push is made from, which the shared check and the git commands
	// that read the records both run in.
	Dir string
	// Remote is the remote's name, as git hands it to a pre-push hook ("origin").
	Remote string
	// URL is the remote's URL, as git hands it to a pre-push hook. The destination repository is
	// read from it (RepoFromURL).
	URL string
	// Updates is what the push would move, one entry per ref.
	Updates []Update
}

// PushName is what a refusal and a setup failure of a push call the write: "the push to origin",
// and "the push" where the caller has no remote's name. It is exported because a caller that
// reports a failure of its own before the guard runs — `legion leak-check pre-push` reading
// git's update lines — must name the write in the same words the guard would, and two copies of
// one sentence are two that drift apart.
func PushName(remote string) string {
	if remote == "" {
		return "the push"
	}
	return "the push to " + remote
}

// Push checks a push before it is made, in three passes, and the first that refuses it is the
// answer. The shared check runs in its pre-push shape — the update lines on its standard input,
// the repository as its working directory. Then, for a destination GitHub reports public, two
// passes read the push's records (pushRecords): this repository's own private-name rules, and,
// over the records of the files the push adds, the forbidden lists of the knives registry entries
// that name the push's destination (Guard.knives). A refusal of the rules is the answer before any
// registry is read.
//
// Neither pass reads a commit the destination already holds, which is a commit one of its
// branches or tags reaches as the destination itself reports them at the push: one git ls-remote
// of the URL git hands the hook (Guard.advertised), made where an update moves a ref to a commit
// and read by both passes. A read that fails refuses the push as a setup failure naming the URL.
// No ref of the repository's own says what the destination holds: git moves a remote's
// refs/remotes/<remote>/* for a push to whatever URL the remote pushes to at the time, a local
// bare one a role points it at for one run (`git remote set-url --push origin <bare>`) included,
// and a ref can be written by hand, so a commit only such a ref reaches is still one the push
// publishes (the architect's rule of 2026-11-17).
//
// The third pass is the check knives makes before a push to a fork, `knives audit`, which was a
// step an agent had to remember to run: the records of the oh-my-pi fork's pushes on 2026-11-17
// show most of them went out without one. As a pass of the push, it needs nobody to remember it.
func (g *Guard) Push(ctx context.Context, req PushRequest) error {
	what := PushName(req.Remote)
	check, err := g.Resolve()
	if err != nil {
		return &SetupFailure{What: what, Why: whyNoCheck, Err: err}
	}
	var lines strings.Builder
	for _, update := range req.Updates {
		lines.WriteString(update.Line())
		lines.WriteString("\n")
	}
	// bash runs the check rather than the check running itself: that is the shape git's own
	// pre-push hook calls it in, and the one the check documents for a push.
	if err := g.shared(ctx, what, child{
		argv:  []string{"bash", check, req.Remote, req.URL},
		dir:   req.Dir,
		stdin: strings.NewReader(lines.String()),
	}); err != nil {
		return err
	}
	public, err := g.scanThrough(ctx, what, RepoFromURL(req.URL))
	if err != nil {
		return err
	}
	if !public {
		return nil
	}
	// held are the commits the destination holds, read once for both passes, and not at all for a
	// push of deletions alone, which adds no commit.
	var held []string
	if slices.ContainsFunc(req.Updates, func(update Update) bool { return !update.deletion() }) {
		if held, err = g.advertised(ctx, req.Dir, req.URL); err != nil {
			return &SetupFailure{What: what, Why: "the destination's refs could not be read", Err: err}
		}
	}
	records, err := g.pushRecords(ctx, req, held)
	if err != nil {
		return &SetupFailure{What: what, Why: "the push's commits could not be read", Err: err}
	}
	if err := g.rules(ctx, what, records); err != nil {
		return err
	}
	return g.knives(ctx, what, req, records, held)
}

// pushRecords is everything a push would publish, as records, which the second and the third of
// Push's passes both read, the third the records of files alone: one per ref name, and for each
// commit the push would add, one for the author identity, one for the committer identity, one for
// the message, one for each added or renamed path and one for each added line, each carrying the
// commit's sha (Record.Commit). The commits an update adds are those its local side reaches and
// neither its remote side nor any of held does (Guard.commits), held being the commits the
// destination holds at its branches and tags (Guard.advertised). A commit reached by two of the
// push's refs is read once, and a deletion adds no commit.
func (g *Guard) pushRecords(ctx context.Context, req PushRequest, held []string) ([]Record, error) {
	var records []Record
	read := map[string]bool{}
	for _, update := range req.Updates {
		if ref := update.ref(); ref != "" {
			records = append(records, RefRecord(ref))
		}
		if update.deletion() {
			continue
		}
		commits, err := g.commits(ctx, req.Dir, held, update)
		if err != nil {
			return nil, err
		}
		for _, commit := range commits {
			if read[commit] {
				continue
			}
			read[commit] = true
			of, err := g.commitRecords(ctx, req.Dir, commit)
			if err != nil {
				return nil, err
			}
			for i := range of {
				of[i].Commit = commit
			}
			records = append(records, of...)
		}
	}
	return records, nil
}

// commits are the commits an update that is no deletion would add to the destination, oldest
// first: the commits the local side has, less the ones the destination holds already. It holds
// every commit one of held reaches, held being the commits the destination's branches and tags
// are at (Guard.advertised): a commit there is the destination's, so the push does not publish
// it. Where the destination has a commit at the ref and the repository has it too (Guard.holds),
// it also holds that commit and everything before it. held counts for a ref the destination has
// as much as for a new one: without it, a forward merge (the default branch merged onto an older
// tip of the branch) would read every commit of the default branch since that tip, not only the
// merge and the branch's own commits. One rev-list answers, fed on its standard input, a line
// each, the local side, then ^<sha> for the remote side where the repository has it and for each
// of held:
//
//	git rev-list --reverse --stdin
//
// which is `git rev-list --reverse <local> --not [<remote>] <held...>` with the shas off the
// command line, since a repository can advertise thousands of refs.
func (g *Guard) commits(ctx context.Context, dir string, held []string, update Update) ([]string, error) {
	var fed strings.Builder
	fed.WriteString(update.LocalSHA)
	fed.WriteString("\n")
	has, err := g.holds(ctx, dir, update.RemoteSHA)
	if err != nil {
		return nil, err
	}
	if has {
		fed.WriteString("^")
		fed.WriteString(update.RemoteSHA)
		fed.WriteString("\n")
	}
	for _, sha := range held {
		fed.WriteString("^")
		fed.WriteString(sha)
		fed.WriteString("\n")
	}
	out, err := g.gitFed(ctx, dir, fed.String(), "rev-list", "--reverse", "--stdin")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// holds is whether the repository has the commit sha names: false for a ref the push creates
// (a sha of all zeros), and false for a remote sha this repository has never fetched, where the
// destination's other refs alone say what it holds (Guard.commits).
func (g *Guard) holds(ctx context.Context, dir, sha string) (bool, error) {
	if sha == "" || zeroSHA(sha) {
		return false, nil
	}
	status, _, err := g.run(ctx, child{argv: gitArgv("cat-file", "-e", sha+"^{commit}"), dir: dir})
	if err != nil {
		return false, err
	}
	return status == 0, nil
}

// advertised are the commits the repository at url holds at its branches and tags, of those the
// repository at dir has: what a push to url, or a fork's push of url's own commits, need not
// publish again (Guard.commits, Guard.knives). The read is of url itself, never of a ref of dir's
// own, which says nothing reliable of what url holds (Guard.Push says why):
//
//	git ls-remote --heads --tags -- <url>
//
// run in dir, so that its configuration's url.<base>.insteadOf leads the read where it leads a
// push, and under the guard's prompt settings (promptsNothing), whose GIT_TERMINAL_PROMPT=0 and
// askpass that answers nothing make a read that wants a credential fail rather than prompt. The
// `--` keeps a URL that starts with a dash from being read as an option. Each sha it prints is
// kept, an annotated tag's peeled commit (<tag>^{}) too, where dir has it as a commit, which one
// cat-file --batch-check of them all answers. A sha dir does not have holds nothing, since
// rev-list cannot walk from it: where it is a commit ahead of dir's, the commits of dir's that it
// reaches are read, which is the fail-closed direction. An empty answer, a repository with no
// branch and no tag, holds nothing. A git that cannot be run, or that exits other than 0, is an
// error naming url, never a repository read as holding nothing.
func (g *Guard) advertised(ctx context.Context, dir, url string) ([]string, error) {
	read := "git ls-remote " + url
	var stderr strings.Builder
	status, out, err := g.run(ctx, child{argv: gitArgv("ls-remote", "--heads", "--tags", "--", url), dir: dir, stderr: &stderr})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", read, err)
	}
	if status != 0 {
		return nil, fmt.Errorf("%s exited %d: %s", read, status, strings.TrimSpace(stderr.String()))
	}
	// "<sha>\t<ref>" a line; a tag and its peeled commit are two lines, and two refs at one commit
	// ask for it once.
	var shas strings.Builder
	asked := map[string]bool{}
	for line := range strings.Lines(out) {
		sha, _, _ := strings.Cut(strings.TrimRight(line, "\r\n"), "\t")
		if sha == "" || asked[sha] {
			continue
		}
		asked[sha] = true
		shas.WriteString(sha)
		shas.WriteString("\n")
	}
	if len(asked) == 0 {
		return nil, nil
	}
	types, err := g.gitFed(ctx, dir, shas.String(), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	var commits []string
	for line := range strings.Lines(types) {
		// "<sha> <type> <size>", and "<sha> missing" for an object dir does not have.
		if fields := strings.Fields(line); len(fields) == 3 && fields[1] == "commit" {
			commits = append(commits, fields[0])
		}
	}
	return commits, nil
}

// zeroSHA is whether sha is git's "this ref is not there": all zeros, of whatever length the
// repository's hash is.
func zeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

// commitRecords is one commit as records. The identities and the message come from the commit
// itself; the added and renamed paths and the added lines come from its diff where it has at
// most one parent (diffTree), and from its files where it is a merge (mergeFiles). The records
// come in one order whatever the shape, so that a report lists its lines alike from one run to
// the next: the identities, the message, the paths in path order, then the lines in path order
// and, within a file, line order.
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
		if paths, added, err = g.mergeFiles(ctx, dir, commit, parents); err != nil {
			return nil, err
		}
	} else {
		if paths, err = g.addedPaths(ctx, dir, commit, len(parents)); err != nil {
			return nil, err
		}
		if added, err = g.addedLines(ctx, dir, commit, len(parents)); err != nil {
			return nil, err
		}
	}
	slices.Sort(paths)
	for _, path := range paths {
		records = append(records, Record{Label: path, Text: path, File: path})
	}
	// Stable, so that the lines of one file keep the order they were read in, which is theirs.
	slices.SortStableFunc(added, func(a, b Record) int { return strings.Compare(a.File, b.File) })
	return append(records, added...), nil
}

// diffTree is a diff-tree of a commit of at most one parent: --root for a root commit, whose
// diff is otherwise empty. A merge is read from its files instead (mergeFiles).
//
// A git that refuses the diff answers a non-zero status, which Guard.git makes an error of and
// Guard.Push a setup failure: a diff that could not be read refuses the push, and is never a
// commit read as clean.
func diffTree(parents int, args ...string) []string {
	argv := []string{"diff-tree", "-r", "-M", "--no-commit-id"}
	if parents == 0 {
		argv = append(argv, "--root")
	}
	return append(argv, args...)
}

// addedPaths are the paths a commit of at most one parent adds or renames a file to, which are
// the paths the destination would hold that it does not hold already.
func (g *Guard) addedPaths(ctx context.Context, dir, commit string, parents int) ([]string, error) {
	out, err := g.git(ctx, dir, diffTree(parents, "--name-status", "--diff-filter=AR", "-z", commit)...)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var paths []string
	for i := 0; i < len(fields); i++ {
		switch status := fields[i]; {
		case status == "":
		case strings.HasPrefix(status, "R"):
			// A rename is three fields: the status, the path it was at, and the path it is at.
			// The destination learns the new path alone.
			if i+2 < len(fields) {
				paths = append(paths, fields[i+2])
			}
			i += 2
		default:
			if i+1 < len(fields) {
				paths = append(paths, fields[i+1])
			}
			i++
		}
	}
	return paths, nil
}

// addedLines are the lines a commit of at most one parent adds, each as a record of the file it
// is added to and of its number in the commit's version of that file. The diff is read with no
// context lines, so every line of it that a patch marks as added is one, and the n-th of a hunk
// headed "@@ -a[,b] +c[,d] @@" is line c+n-1. The "+++ b/<path>" that names the file is read
// only where a header stands, between a "diff --" line and the hunk that follows it, so a line
// of content that reads like one is not mistaken for the name of a file.
func (g *Guard) addedLines(ctx context.Context, dir, commit string, parents int) ([]Record, error) {
	out, err := g.git(ctx, dir, diffTree(parents, "--unified=0", "--no-color", commit)...)
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
// A file is text as git's own diff tells one where no attribute says otherwise: no zero byte in
// its first 8000 bytes (xdiff-interface.c's buffer_is_binary). A binary file of the merge's adds
// no line, as a binary diff adds none, and a parent's file that is binary, absent or a submodule
// holds none.
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
	var ids []string
	asked := map[string]bool{}
	ask := func(id string) {
		if !asked[id] {
			asked[id] = true
			ids = append(ids, id)
		}
	}
	for _, change := range changes {
		// A path the merge has no file at has no line to read, so its parents' files are not
		// read either.
		if change.blob != "" {
			ask(change.blob)
			for _, id := range change.held {
				ask(id)
			}
		}
	}
	blobs, err := g.blobs(ctx, dir, ids)
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	var records []Record
	for _, change := range changes {
		if change.added {
			paths = append(paths, change.path)
		}
		file := blobs[change.blob]
		if change.blob == "" || binary(file) {
			continue
		}
		held := map[string]bool{}
		for _, id := range change.held {
			if of := blobs[id]; !binary(of) {
				for line := range strings.SplitSeq(of, "\n") {
					held[strings.TrimSuffix(line, "\r")] = true
				}
			}
		}
		number := 0
		for line := range strings.SplitSeq(file, "\n") {
			number++
			if text := strings.TrimSuffix(line, "\r"); strings.TrimSpace(text) != "" && !held[text] {
				records = append(records, Record{Label: change.path, Text: text, File: change.path, Line: number})
			}
		}
	}
	return paths, records, nil
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

// isBlob is whether a mode of diff-tree's raw format is a file git holds as a blob: a regular
// file, or a symbolic link, whose target is its content. 000000 is no file and 160000 a
// submodule's commit, of which this repository holds no lines.
func isBlob(mode string) (bool, error) {
	bits, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return false, errors.New("git diff-tree wrote a mode of a shape it does not write")
	}
	kind := bits & 0o170000
	return kind == 0o100000 || kind == 0o120000, nil
}

// binary is whether a file is binary as git's diff tells one with no attribute to say: a zero
// byte in its first 8000 bytes.
func binary(content string) bool {
	return strings.IndexByte(content[:min(len(content), 8000)], 0) >= 0
}

// blobs are the contents of the blobs ids name, read with one cat-file --batch for all of them:
// a merge of 52 parents can ask for hundreds, and a git apiece would be hundreds of processes. A
// blob cat-file does not answer whole is an error, never a file read as empty.
func (g *Guard) blobs(ctx context.Context, dir string, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := g.gitFed(ctx, dir, strings.Join(ids, "\n")+"\n", "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	contents := make(map[string]string, len(ids))
	for _, id := range ids {
		// "<id> blob <size>", the content, and a newline; "<id> missing" for no such object.
		header, rest, ok := strings.Cut(out, "\n")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[0] != id || fields[1] != "blob" {
			return nil, fmt.Errorf("git cat-file read no blob %s", id)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size >= len(rest) || rest[size] != '\n' {
			return nil, fmt.Errorf("git cat-file answered blob %s cut short", id)
		}
		contents[id] = rest[:size]
		out = rest[size+1:]
	}
	return contents, nil
}

// short is a commit as a report names it.
func short(commit string) string {
	return commit[:min(len(commit), 7)]
}

// git runs one git command in dir and answers its standard output and error together, failing
// where git itself does: a record this package could not read is a setup failure, never a push
// read as clean.
func (g *Guard) git(ctx context.Context, dir string, args ...string) (string, error) {
	argv := gitArgv(args...)
	status, printed, err := g.run(ctx, child{argv: argv, dir: dir})
	if err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", args[0], status, strings.TrimSpace(printed))
	}
	return printed, nil
}

// gitFed is git with input on its standard input, answering its standard output alone. What it
// answers is read byte for byte, a blob's content or a diff split on zero bytes, so a warning
// git writes to its standard error must not land in it; that error is what a failure carries.
func (g *Guard) gitFed(ctx context.Context, dir, input string, args ...string) (string, error) {
	var stderr strings.Builder
	status, out, err := g.run(ctx, child{argv: gitArgv(args...), dir: dir, stdin: strings.NewReader(input), stderr: &stderr})
	if err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", args[0], status, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
