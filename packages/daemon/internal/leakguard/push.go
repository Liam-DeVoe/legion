package leakguard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
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
	return namesNoCommit(u.LocalSHA)
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
	// that read the records both run in, and which a URL that is a relative local path is read
	// against (Guard.advertised).
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

// Push checks a push before it is made. The shared check runs in its pre-push shape — the update
// lines on its standard input, the repository as its working directory — and then, for a
// destination GitHub reports public, or one the guard cannot name, such as a local path
// (scanThrough), this repository's own private-name rules read the push's records (pushRecords):
// each ref name, and for each commit the destination does not already hold, the author, the
// committer, the message, each added or renamed path, and each added line.
//
// The rules read no commit the destination already holds, which is a commit one of its branches
// or tags reaches as the destination itself reports them at the push: one git ls-remote of the
// URL git hands the hook, run outside the repository so that none of its configuration leads the
// read elsewhere, and bounded (Guard.advertised), made where an update moves a ref to a commit.
// A read that fails, or that its bound stops, refuses the push as a setup failure naming the URL.
// No ref of the repository's own says what the destination holds: git moves a remote's
// refs/remotes/<remote>/* for a push to whatever URL the remote pushes to at the time, a local
// bare one a role points it at for one run (`git remote set-url --push origin <bare>`) included,
// and a ref can be written by hand, so a commit only such a ref reaches is still one the push
// publishes.
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
	// held are the commits the destination holds, not read at all for a push of deletions alone,
	// which adds no commit.
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
	return g.rules(ctx, what, records)
}

// pushRecords is everything a push would publish, as records: one per ref name, and for each
// commit the push would add, one for the author identity, one for the committer identity, one for
// the message, one for each added or renamed path and one for each added line. The commits an
// update adds are those its local side reaches and neither its remote side nor any of held does
// (Guard.commits), held being the commits the destination holds at its branches and tags
// (Guard.advertised). A commit reached by two of the push's refs is read once, and a deletion adds
// no commit.
func (g *Guard) pushRecords(ctx context.Context, req PushRequest, held []string) ([]Record, error) {
	// not is held as rev-list's negative revisions, a line ^<sha> each, written once for every
	// update's read of the commits it adds (Guard.commits).
	var not strings.Builder
	for _, sha := range held {
		not.WriteString("^")
		not.WriteString(sha)
		not.WriteString("\n")
	}
	var records []Record
	read := map[string]bool{}
	for _, update := range req.Updates {
		if ref := update.ref(); ref != "" {
			records = append(records, RefRecord(ref))
		}
		if update.deletion() {
			continue
		}
		commits, err := g.commits(ctx, req.Dir, held, not.String(), update)
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
			records = append(records, of...)
		}
	}
	return records, nil
}

// short is a commit as a report names it.
func short(commit string) string {
	return commit[:min(len(commit), 7)]
}

// git is gitFed with nothing of its own in git's environment and nothing on its standard input.
// Each of its callers reads what git answers as data: git show's identities and message split on
// zero bytes, and diff-tree's list of a commit's changes.
func (g *Guard) git(ctx context.Context, dir string, args ...string) (string, error) {
	return g.gitFed(ctx, dir, nil, "", args...)
}

// gitFed runs one git command in dir under env, with input on its standard input, none where
// input is empty, and answers its standard output alone, failing where git itself does: a record
// this package could not read is a setup failure, never a push read as clean. What it answers is
// read as data — a list of commits, a list of changes split on zero bytes — so a warning git
// writes to its standard error must not land in it: a warning written in the middle of a path
// diff-tree lists would cut the path in two, and the lines added to that file would be read as
// lines of a file of another name. That error is what a failure carries, quoted as git wrote it
// less the user and password of a URL in it: a lazy fetch of a partial clone that fails names its
// promisor remote, whose URL can carry a token (withoutUserinfo). The read of which advertised
// shas are commits dir has runs under an env that fetches nothing (Guard.advertised); every other
// caller passes none.
func (g *Guard) gitFed(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
	var stdin io.Reader
	if input != "" {
		stdin = strings.NewReader(input)
	}
	var stderr strings.Builder
	status, out, err := g.run(ctx, child{
		argv:   gitArgv(args...),
		dir:    dir,
		env:    env,
		stdin:  stdin,
		stderr: &stderr,
	})
	if err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", args[0], status, withoutUserinfo(strings.TrimSpace(stderr.String())))
	}
	return out, nil
}
