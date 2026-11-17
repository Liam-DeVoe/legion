package leakguard

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
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
//
// A push to a URL rather than to a remote, `git push https://user:token@github.com/...`, hands the
// hook that URL as the remote's name, and every refusal and setup failure of the push is printed
// for whoever reads the log, so the name is written without the user and password a URL can carry
// (withoutUserinfo), as a failure that names the URL itself is.
func PushName(remote string) string {
	if remote == "" {
		return "the push"
	}
	return "the push to " + withoutUserinfo(remote)
}

// Push checks a push before it is made, in three passes, and the first that refuses it is the
// answer. The shared check runs in its pre-push shape — the update lines on its standard input,
// the repository as its working directory. Then, for a destination GitHub reports public, or one
// the guard cannot name, such as a local path (scanThrough), two passes read the push's records
// (pushRecords): this repository's own private-name rules, and,
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
	return g.knives(ctx, what, req, records)
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
// destination's other refs alone say what it holds (Guard.commits). It asks as the read of what
// the destination holds asks (Guard.advertised), fetching nothing (fetchesNothing): in a partial
// clone, git fetches a commit the clone lacks from the promisor remote the moment cat-file asks
// for it, so a remote sha the clone never fetched would be fetched to be answered.
func (g *Guard) holds(ctx context.Context, dir, sha string) (bool, error) {
	if sha == "" || zeroSHA(sha) {
		return false, nil
	}
	status, _, err := g.run(ctx, child{argv: gitArgv("cat-file", "-e", sha+"^{commit}"), dir: dir, env: fetchesNothing})
	if err != nil {
		return false, err
	}
	return status == 0, nil
}

// fetchesNothing is the environment of a read of what a repository has that must fetch nothing
// to answer: GIT_NO_LAZY_FETCH=1 (git 2.44 and later), under which an object a partial clone
// lacks is answered missing rather than fetched from its promisor remote. The reads of what the
// destination holds run so (Guard.advertised, Guard.holds); the reads of a commit's files do not,
// since a commit read in a partial clone needs the blobs it lacks fetched (blobBatch).
var fetchesNothing = []string{"GIT_NO_LAZY_FETCH=1"}

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
// reaches are read, which is the fail-closed direction. The cat-file fetches nothing
// (fetchesNothing), so that in a partial clone, as an issue pod's fork clones are
// (--filter=blob:none), a sha dir lacks is answered missing rather than fetched from the promisor
// remote: the read of what url holds fetches nothing, and a sha dir lacks holds nothing there as
// it does anywhere. An empty answer, a repository with no branch and no tag, holds nothing. A git
// that cannot be run, or that exits other than 0, is an error naming url, never a repository read
// as holding nothing, and the error carries no credential: the URL it names and git's standard
// error it quotes are each written without the user and password a URL can carry
// (withoutUserinfo).
func (g *Guard) advertised(ctx context.Context, dir, url string) ([]string, error) {
	read := "git ls-remote " + withoutUserinfo(url)
	var stderr strings.Builder
	status, out, err := g.run(ctx, child{argv: gitArgv("ls-remote", "--heads", "--tags", "--", url), dir: dir, stderr: &stderr})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", read, err)
	}
	if status != 0 {
		return nil, fmt.Errorf("%s exited %d: %s", read, status, withoutUserinfo(strings.TrimSpace(stderr.String())))
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
	stderr.Reset()
	status, types, err := g.run(ctx, child{
		argv:   gitArgv("cat-file", "--batch-check"),
		dir:    dir,
		env:    fetchesNothing,
		stdin:  strings.NewReader(shas.String()),
		stderr: &stderr,
	})
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("git cat-file exited %d: %s", status, strings.TrimSpace(stderr.String()))
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

// userinfo is the user and password a URL with a scheme can carry, the "user:token@" of
// https://user:token@host/path: what follows "://" up to the last "@" before the first "/", "?",
// "#", quote or space, which end the host.
var userinfo = regexp.MustCompile(`://[^/?#\s'"]*@`)

// withoutUserinfo is text with the user and password of each URL in it taken out, so that
// https://user:token@host/path reads https://host/path. It is what a failure that names a URL, or
// that quotes git's own account of one, prints: the URL git hands a pre-push hook, and an upstream
// a registry names, can carry a token, and a failure is printed for whoever reads the log. A URL
// in git's scp form (user@host:path) names a user and no password, and is left as it is.
func withoutUserinfo(text string) string {
	return userinfo.ReplaceAllLiteralString(text, "://")
}

// zeroSHA is whether sha is git's "this ref is not there": all zeros, of whatever length the
// repository's hash is.
func zeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

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
// 8000 bytes (binary), whatever an attribute says, and a binary file, the commit's or the
// parent's, is read no further than those bytes and diffed not at all, whatever its size: the
// commit's adds no line. A text file of the commit's is read only where it is at most blobBound
// bytes, and one above that refuses the push as a setup failure naming the file and its size, as
// a merge's does (mergeFiles says why); so does a text file of the parent's that a file of the
// commit's is diffed against, whose removed lines the diff would write. Of the rest:
//
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
// rename that is not diffed: a binary file, a submodule, and a file read whole. Where none is left
// out, it is the diff of the whole commit, and where nothing is diffed, no diff runs. The paths
// are written on its command line, since no diff command of git reads pathspecs from its standard
// input (--pathspec-from-file is git add's, git commit's and their kin's), and they are as many as
// the modifications and renames of binary files and submodules, which a commit seldom has many of.
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
	// there or a submodule. submodule is the commit the commit's submodule at path is at, and ""
	// where it has none there or renames the parent's submodule at that same commit: git's diff
	// writes a submodule as the one line that names its commit, and writes no line of such a
	// rename.
	blob, submodule string
	// parent is the parent's file at from that the commit's file is diffed against: a blob of the
	// same kind, a regular file or a symbolic link, at a modification or a rename. It is "" for
	// every other change, which git's diff writes as a file of the commit's own, and for a rename
	// whose path, or the path it is from, lies under a path another modification or rename names:
	// a pathspec names a directory's files too, so the one that leaves that other change out of the
	// diff would leave this rename out with it (addedLines).
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
			change.blob = raw[3]
			if change.paired() && theirs == ours {
				change.parent = raw[2]
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
	rest, head, err := batch.head()
	if err != nil {
		return false, "", err
	}
	binaryParent, parentSize := binary(head), int64(len(head))+rest
	if err := batch.skip(rest); err != nil {
		return false, "", err
	}
	if rest, head, err = batch.head(); err != nil {
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
// line of it, and a file read in part would pass the rest. A binary file is read no further than
// its first 8000 bytes, whatever its size. The blobs are read path by path as git writes them
// (blobBatch), the merge's file first and then its parents' files there, so what the read holds
// is one path's: the merge's file, its lines no parent's file read so far holds, and the
// parent's file being read.
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
				if _, err := batch.isBinary(); err != nil {
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

// fileKind is the kind of file a mode diff-tree writes, in its raw format or in a patch's header,
// names.
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

// binaryHead is how much of a file git's diff reads to tell whether it is binary
// (xdiff-interface.c's FIRST_FEW_BYTES), and so how much of a blob blobBatch.head reads first.
const binaryHead = 8000

// binary is whether a file is binary as git's diff tells one with no attribute to say, from head,
// the file's first bytes: a zero byte in its first binaryHead bytes.
func binary(head []byte) bool {
	return bytes.IndexByte(head[:min(len(head), binaryHead)], 0) >= 0
}

// fileBytes is the largest text file the read of a commit's files takes, a merge's (mergeFiles)
// or a commit of one parent's (commitFiles): 32 MiB. A merge's read holds at most two such files
// at once, the merge's and one parent's, and a set of the merge's distinct lines, which is under
// 100 MiB at the bound for lines of ordinary length and some hundreds of MiB for a file of nothing
// but short distinct lines: a small part of the 19 GiB an issue pod's roles share at the default
// issue reservation (docs/kubernetes.md). It is over eight times the largest text file this
// repository tracks, a test fixture of 3.7 MiB
// (packages/envoy/internal/dispatch/pmdoc/testdata/fixtures.json, measured 2026-11-17), so that a
// lock file or a fixture a commit changes is far below it, and under the 50 MiB above which
// GitHub warns of a file a push adds.
const fileBytes = 32 << 20

// blobBound is fileBytes, held in a variable so that a test can lower it.
var blobBound int64 = fileBytes

// aboveBound is the failure of a text file above blobBound: the file at path, of size bytes, that
// whose names the owner of ("merge 1a2b3c4's"), in the read of a commit of the kind named
// ("merge", "commit"). It names the file and its size, and the file is not read (mergeFiles says
// why).
func aboveBound(whose, path string, size int64, kind string) error {
	return fmt.Errorf("%s file %s is %d bytes, more than the %d bytes a %s's file is read up to", whose, maskPath(path), size, blobBound, kind)
}

// blobBatch is one git cat-file --batch over the blobs ids names, read a blob at a time as git
// writes them, so that what is held is the blob being read and never the batch: one git answers a
// commit's hundreds of blobs, where a git apiece would be hundreds of processes. The blobs are
// read in the order of ids, each one's first binaryHead bytes first (head). A blob cat-file does
// not answer whole is an error, never a file read as empty, and the batch ends with close, which
// stops git where the reader stopped early.
type blobBatch struct {
	// ids are the blobs asked for, and read is how many of them have been read.
	ids  []string
	read int
	// out is git's standard output, through pipe, whose reading end close shuts.
	out  *bufio.Reader
	pipe *io.PipeReader
	// stop ends the context git runs under, and ran answers how git ended.
	stop context.CancelFunc
	ran  chan error
}

// readBlobs starts the batch of ids, read in dir. Where ids is empty it starts no git.
func (g *Guard) readBlobs(ctx context.Context, dir string, ids []string) *blobBatch {
	ctx, stop := context.WithCancel(ctx)
	pipe, into := io.Pipe()
	batch := &blobBatch{ids: ids, out: bufio.NewReader(pipe), pipe: pipe, stop: stop, ran: make(chan error, 1)}
	if len(ids) == 0 {
		into.Close()
		batch.ran <- nil
		return batch
	}
	go func() {
		var stderr strings.Builder
		status, _, err := g.run(ctx, child{
			argv:   gitArgv("cat-file", "--batch"),
			dir:    dir,
			stdin:  strings.NewReader(strings.Join(ids, "\n") + "\n"),
			stdout: into,
			stderr: &stderr,
		})
		if err == nil && status != 0 {
			err = fmt.Errorf("git cat-file exited %d: %s", status, strings.TrimSpace(stderr.String()))
		}
		// A read past the last thing git wrote meets git's own failure, where it failed.
		into.CloseWithError(err)
		batch.ran <- err
	}()
	return batch
}

// head reads the header of the next blob and its first binaryHead bytes, or all of it where it is
// shorter, which say whether it is binary (binary), and answers how many bytes of it are left.
func (b *blobBatch) head() (rest int64, head []byte, err error) {
	if b.read == len(b.ids) {
		return 0, nil, errors.New("git cat-file was asked for fewer blobs than were read")
	}
	id := b.ids[b.read]
	b.read++
	// "<id> blob <size>", and "<id> missing" for no such object.
	header, err := b.out.ReadSlice('\n')
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return 0, nil, err
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 || fields[0] != id || fields[1] != "blob" {
		return 0, nil, fmt.Errorf("git cat-file read no blob %s", id)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return 0, nil, fmt.Errorf("git cat-file read no blob %s", id)
	}
	head = make([]byte, min(size, binaryHead))
	if _, err := io.ReadFull(b.out, head); err != nil {
		return 0, nil, b.cut(err)
	}
	return size - int64(len(head)), head, nil
}

// isBinary reads the next blob as far as it takes to say whether it is binary, and passes over
// the rest of it.
func (b *blobBatch) isBinary() (bool, error) {
	rest, head, err := b.head()
	if err != nil {
		return false, err
	}
	return binary(head), b.skip(rest)
}

// text reads the next blob, which is the file at path that whose names the owner of ("merge
// 1a2b3c4's") in the read of a commit of the kind named ("merge"), and answers its content and
// true where it is text; a binary one it passes over, answering false. A text file above
// blobBound is an error that names it, its owner and its size, and is not read (aboveBound).
func (b *blobBatch) text(path, whose, kind string) (string, bool, error) {
	rest, head, err := b.head()
	if err != nil {
		return "", false, err
	}
	if binary(head) {
		return "", false, b.skip(rest)
	}
	if size := int64(len(head)) + rest; size > blobBound {
		return "", false, aboveBound(whose, path, size, kind)
	}
	file, err := b.whole(head, rest)
	return file, err == nil, err
}

// whole reads the rest of the blob head read, rest bytes of it, and answers the blob whole.
func (b *blobBatch) whole(head []byte, rest int64) (string, error) {
	var content strings.Builder
	content.Grow(len(head) + int(rest))
	content.Write(head)
	if _, err := io.CopyN(&content, b.out, rest); err != nil {
		return "", b.cut(err)
	}
	return content.String(), b.end()
}

// skip passes over the rest of the blob head read, rest bytes of it, unread.
func (b *blobBatch) skip(rest int64) error {
	if _, err := io.CopyN(io.Discard, b.out, rest); err != nil {
		return b.cut(err)
	}
	return b.end()
}

// end reads the newline git writes after each blob's content, which is where its size says the
// content ends.
func (b *blobBatch) end() error {
	end, err := b.out.ReadByte()
	if err == nil && end != '\n' {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return b.cut(err)
	}
	return nil
}

// cut is the error of a blob whose content git did not write whole: git's own failure where git
// failed, and else the blob cut short.
func (b *blobBatch) cut(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("git cat-file answered blob %s cut short", b.ids[b.read-1])
	}
	return err
}

// close ends the batch and answers how it ended: with failed, the error the reader stopped at,
// where it stopped at one, after it stops git; and else with git's own failure, where git failed,
// once what git wrote after the last blob read is passed over.
func (b *blobBatch) close(failed error) error {
	defer b.stop()
	if failed == nil {
		_, failed = io.Copy(io.Discard, b.out)
	} else {
		b.stop()
		b.pipe.CloseWithError(failed)
	}
	if ran := <-b.ran; failed == nil {
		return ran
	}
	return failed
}

// short is a commit as a report names it.
func short(commit string) string {
	return commit[:min(len(commit), 7)]
}

// git is gitFed with nothing on its standard input. Each of its callers reads what git answers
// as data: git show's identities and message split on zero bytes, and diff-tree's list of a
// commit's changes and its patch.
func (g *Guard) git(ctx context.Context, dir string, args ...string) (string, error) {
	return g.gitFed(ctx, dir, "", args...)
}

// gitFed runs one git command in dir with input on its standard input, and answers its standard
// output alone, failing where git itself does: a record this package could not read is a setup
// failure, never a push read as clean. What it answers is read as data — a list of commits, a
// list of changes split on zero bytes, a patch — so a warning git writes to its standard error
// must not land in it: a warning written in the middle of a patch's line would cut the line in
// two, and the half after it would be read as no added line at all. That error is what a failure
// carries. Its pathspecs are read as they are written (pathspecsAsWritten).
func (g *Guard) gitFed(ctx context.Context, dir, input string, args ...string) (string, error) {
	var stderr strings.Builder
	status, out, err := g.run(ctx, child{
		argv:   gitArgv(args...),
		dir:    dir,
		env:    pathspecsAsWritten,
		stdin:  strings.NewReader(input),
		stderr: &stderr,
	})
	if err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", args[0], status, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// pathspecsAsWritten is the environment under which git reads a pathspec this package writes as
// it is written, whatever the caller's environment says of pathspecs. GIT_LITERAL_PATHSPECS=1
// would read the magic of :(top,exclude,literal)<path> as part of a path no file is at, so that a
// diff meant to leave one file out would name none and write nothing (addedLines), and
// GIT_ICASE_PATHSPECS=1 would leave out a file whose path differs from one left out in case alone.
var pathspecsAsWritten = []string{"GIT_LITERAL_PATHSPECS=0", "GIT_ICASE_PATHSPECS=0"}
