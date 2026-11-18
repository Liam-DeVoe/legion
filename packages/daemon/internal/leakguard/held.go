package leakguard

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// commits are the commits an update that is no deletion would add to the destination, oldest
// first: the commits the local side has, less the ones the destination holds already. It holds
// every commit one of held reaches, held being the commits the destination's branches and tags
// are at (Guard.advertised) and excluded their lines of rev-list's input (notLines): a commit
// there is the destination's, so the push does not publish it. Where the destination has a
// commit at the ref and the repository has it too, it also holds that commit and everything
// before it. A commit of held is one the repository has, so the repository is asked only of
// another (Guard.holds), and the push of a ref the destination advertises asks nothing more.
// held counts for a ref the destination has as much as for a new one: without it, a forward
// merge (the default branch merged onto an older tip of the branch) would read every commit of
// the default branch since that tip, not only the merge and the branch's own commits. One
// rev-list answers, fed on its standard input the local side, then ^<sha> for the remote side
// where the repository has it and for each of held:
//
//	git rev-list --reverse --stdin
//
// which is `git rev-list --reverse <local> --not [<remote>] <held...>` with the shas off the
// command line, since a repository can advertise thousands of refs. A git that fails is an error.
func (g *Guard) commits(ctx context.Context, dir string, held []string, excluded string, update Update) ([]string, error) {
	has := slices.Contains(held, update.RemoteSHA)
	if !has {
		var err error
		if has, err = g.holds(ctx, dir, update.RemoteSHA); err != nil {
			return nil, err
		}
	}
	var fed strings.Builder
	fed.WriteString(update.LocalSHA)
	fed.WriteString("\n")
	if has {
		fed.WriteString("^")
		fed.WriteString(update.RemoteSHA)
		fed.WriteString("\n")
	}
	fed.WriteString(excluded)
	out, err := g.gitFed(ctx, dir, fed.String(), "rev-list", "--reverse", "--stdin")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// notLines are the lines ^<sha> of each of shas, which rev-list reads on its standard input as
// `--not <shas...>` (Guard.commits).
func notLines(shas []string) string {
	var lines strings.Builder
	for _, sha := range shas {
		lines.WriteString("^")
		lines.WriteString(sha)
		lines.WriteString("\n")
	}
	return lines.String()
}

// holds is whether the repository has the commit sha names: false for a ref the push creates
// (a sha of all zeros), and false for a remote sha this repository has never fetched, where the
// destination's other refs alone say what it holds (Guard.commits). It asks as the read of what
// the destination holds asks (Guard.advertised), fetching nothing (fetchesNothing): in a partial
// clone, git fetches a commit the clone lacks from the promisor remote the moment cat-file asks
// for it, so a remote sha the clone never fetched would be fetched to be answered.
func (g *Guard) holds(ctx context.Context, dir, sha string) (bool, error) {
	if namesNoCommit(sha) {
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
// repository at dir has: what a push to url need not publish again (Guard.commits). The read is
// of url itself, never of a ref of dir's own, which says nothing reliable of what url holds
// (Guard.Push says why):
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
	types, err := g.gitFedWith(ctx, dir, fetchesNothing, shas.String(), "cat-file", "--batch-check")
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

// userinfo is the user and password a URL with a scheme can carry, the "user:token@" of
// https://user:token@host/path: what follows "://" up to the last "@" before the first "/", "?",
// "#", quote or space, which end the host.
var userinfo = regexp.MustCompile(`://[^/?#\s'"]*@`)

// withoutUserinfo is text with the user and password of each URL in it taken out, so that
// https://user:token@host/path reads https://host/path. It is what a failure that names a URL, or
// that quotes git's own account of one, prints: the URL git hands a pre-push hook can carry a
// token, and a failure is printed for whoever reads the log. A URL in git's scp form
// (user@host:path) names a user and no password, and is left as it is.
func withoutUserinfo(text string) string {
	return userinfo.ReplaceAllLiteralString(text, "://")
}

// namesNoCommit is whether sha names no commit: empty, or git's "this ref is not there", all
// zeros of whatever length the repository's hash is.
func namesNoCommit(sha string) bool {
	return strings.Trim(sha, "0") == ""
}
