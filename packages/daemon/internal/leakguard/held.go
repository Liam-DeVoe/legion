package leakguard

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// commits are the commits an update that is no deletion would add to the destination, oldest
// first: the commits the local side has, less the ones the destination holds already. It holds
// every commit one of held reaches, held being the commits the destination's branches and tags
// are at (Guard.advertised): a commit there is the destination's, so the push does not publish it.
// Where the destination has a commit at the ref and the repository has it too, it also holds that
// commit and everything before it. A commit of held is one the repository has, so the repository
// is asked only of another (Guard.holds), and the push of a ref the destination advertises asks
// nothing more. held counts for a ref the destination has as much as for a new one: without it, a
// forward merge (the default branch merged onto an older tip of the branch) would read every
// commit of the default branch since that tip, not only the merge and the branch's own commits.
// One rev-list answers, fed on its standard input the local side, then ^<sha> for the remote side
// where the repository has it and held does not, and not, ^<sha> for each of held, which
// Guard.pushRecords writes once for the push's every update:
//
//	git rev-list --reverse --stdin
//
// which is `git rev-list --reverse <local> --not [<remote>] <held...>` with the shas off the
// command line, since a repository can advertise thousands of refs. A git that fails is an error.
func (g *Guard) commits(ctx context.Context, dir string, held []string, not string, update Update) ([]string, error) {
	fed := update.LocalSHA + "\n"
	if !slices.Contains(held, update.RemoteSHA) {
		has, err := g.holds(ctx, dir, update.RemoteSHA)
		if err != nil {
			return nil, err
		}
		if has {
			fed += "^" + update.RemoteSHA + "\n"
		}
	}
	out, err := g.gitFed(ctx, dir, nil, fed+not, "rev-list", "--reverse", "--stdin")
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

// readsNoConfigFile is the environment of a read that must read neither the machine's git
// configuration nor the user's (Guard.advertised, Guard.lsRemote): GIT_CONFIG_NOSYSTEM=1 and
// GIT_CONFIG_GLOBAL=/dev/null take away the system file and the user's, the second also the one
// under XDG_CONFIG_HOME, and with them every file either includes.
var readsNoConfigFile = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}

// advertised are the commits the repository at url holds at its branches and tags, of those the
// repository at dir has: what a push to url need not publish again (Guard.commits). The read is
// of url itself, never of a ref of dir's own, which says nothing reliable of what url holds
// (Guard.Push says why):
//
//	git ls-remote --heads --tags -- <url>
//
// run outside dir, in /, a directory only root can write, so that git finds no repository there
// and no configuration of dir's applies to the read. git hands the hook the URL dir's
// configuration has already led the push to, and a rewrite read a second time can lead the read
// to another repository: under url.<base>.pushInsteadOf, which git applies to the push URL alone,
// the URL the hook is handed can be one a url.<base>.insteadOf of dir's rewrites, and a read made
// in dir would list the refs of the repository that rewrite names, not of the one the push writes
// to. The review of cybermonklabs/legion#3946 reproduced that on git 2.47.3: with origin at
// https://example.invalid/fork, url.<real.git>.pushInsteadOf that URL and
// url.<fake.git>.insteadOf <real.git>, the push landed in real.git, the hook was handed real.git,
// and a git ls-remote of it in the clone listed fake.git's refs. So the read keeps none of the
// caller's GIT_DIR, and a URL that is a relative local path is read against dir (againstDir), as
// git read it for the push. A url.<base>.insteadOf set in the caller's environment, the user's
// configuration or the machine's would lead the read to another repository the same way, so it
// reads no git configuration but its own command line's, which a git wrapper of a host's own can
// still add to, the case `legion git --after` runs: of the caller's environment it keeps no
// GIT_CONFIG_COUNT block and no GIT_CONFIG_PARAMETERS (heldReadInherits), and it reads neither
// the system file nor the user's (readsNoConfigFile). So the credential helper a pod sets in its
// GIT_CONFIG_COUNT block does not reach the read, which over https is anonymous where no netrc
// file, the one under HOME or, from curl 8.16, one NETRC names, names the host: a public
// repository on GitHub answers it, and while GitHub asks some Git-over-HTTPS reads to
// authenticate, the read fails and the push is refused as a setup failure, as it is for a
// destination the guard cannot name that wants a credential set in git configuration. No
// transport setting made in git configuration reaches the read either, in the user's file, the
// system's or a GIT_CONFIG_COUNT block: a push that needs a core.sshCommand, an http.proxy or an
// http.sslCAInfo set there to reach its destination is refused as a setup failure the same way,
// while the environment variables that make the same settings reach it (heldReadInherits).
//
// The read runs under the guard's prompt settings (promptsNothing), whose GIT_TERMINAL_PROMPT=0
// and askpass that answers nothing make a read that wants a credential fail rather than prompt,
// and for at most heldReadBound, so that a destination that stops answering refuses the push
// rather than holding it. The `--` keeps a URL that starts with a dash from being read as an
// option. Each sha it prints is kept, an annotated tag's peeled commit (<tag>^{}) too, where dir
// has it as a commit, which one cat-file --batch-check of them all, run in dir, answers. A sha dir
// does not have holds nothing, since rev-list cannot walk from it: where it is a commit ahead of
// dir's, the commits of dir's that it reaches are read, which is the fail-closed direction. The
// cat-file fetches nothing (fetchesNothing), so that in a partial clone, as an issue pod's fork
// clones are (--filter=blob:none), a sha dir lacks is answered missing rather than fetched from
// the promisor remote: the read of what url holds fetches nothing, and a sha dir lacks holds
// nothing there as it does anywhere. An empty answer, a repository with no branch and no tag,
// holds nothing. A git that cannot be run, that exits other than 0, or that its bound stops is an
// error naming url, never a repository read as holding nothing, and the error carries no
// credential: the URL it names and git's standard error it quotes are each written without the
// user and password a URL can carry (withoutUserinfo).
func (g *Guard) advertised(ctx context.Context, dir, url string) ([]string, error) {
	read := "git ls-remote " + withoutUserinfo(url)
	bounded, cancel := context.WithTimeout(ctx, g.heldReadBound)
	defer cancel()
	var stderr strings.Builder
	status, out, err := g.run(bounded, child{
		argv:    gitArgv("ls-remote", "--heads", "--tags", "--", againstDir(dir, url)),
		dir:     "/",
		inherit: heldReadInherits,
		env:     readsNoConfigFile,
		stderr:  &stderr,
	})
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
	types, err := g.gitFed(ctx, dir, fetchesNothing, shas.String(), "cat-file", "--batch-check")
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

// heldReadInherits is which names of the caller's environment the read of what a push's
// destination holds keeps (Guard.advertised): every one but GIT_DIR and the names that start
// GIT_CONFIG. git hands a pre-push hook GIT_DIR where the push was made with `git --git-dir
// <dir>`, which is how `jj git push` makes it, and a git that keeps it reads that repository's
// configuration from any directory. Measured on git 2.47.3 from /: GIT_DIR=<clone>/.git makes git
// read the clone's configuration, and GIT_COMMON_DIR or GIT_WORK_TREE alone does not. The names
// that start GIT_CONFIG are configuration of the environment's own: the GIT_CONFIG_COUNT block
// (GIT_CONFIG_COUNT with its GIT_CONFIG_KEY_<n> and GIT_CONFIG_VALUE_<n>), GIT_CONFIG_PARAMETERS,
// which `git -c` hands the git it starts, GIT_CONFIG_SYSTEM, GIT_CONFIG_GLOBAL and
// GIT_CONFIG_NOSYSTEM, which say which files git reads and the last two of which the read sets
// itself, and GIT_CONFIG, which git config reads. Every other name stays: HOME, where ssh finds
// its keys and libcurl a .netrc, SSH_AUTH_SOCK, GIT_SSH_COMMAND, GIT_ALLOW_PROTOCOL,
// GIT_SSL_CAINFO, GIT_SSL_CAPATH and the proxy variables, so the read reaches a destination over
// ssh, through a proxy or under the certificates the caller trusts as the push did where these
// variables set them; where git configuration sets them instead, they do not reach the read, and
// the push is refused as a setup failure (Guard.advertised).
func heldReadInherits(name string) bool {
	return name != "GIT_DIR" && !strings.HasPrefix(name, "GIT_CONFIG")
}

// againstDir is url as a read made outside dir reaches what git reached from dir: a URL that is a
// relative local path joined to dir, the directory git runs the hook in, which is the one git read
// that path from for the push, and every other URL as it is. A URL is a local path by git's own
// rule (connect.c's url_is_local_not_ssh): it names no scheme (`<scheme>://`), and it has no
// colon, or a slash before its first colon, since `host:path` with the colon first is git's scp
// form of ssh. The path is joined, not cleaned, so that a `..` in it is read from the directory
// itself, as git's own read of it is, rather than from the parent of a symbolic link dir was
// named through.
func againstDir(dir, url string) string {
	if strings.Contains(url, "://") || filepath.IsAbs(url) {
		return url
	}
	if colon := strings.IndexByte(url, ':'); colon >= 0 {
		if slash := strings.IndexByte(url, '/'); slash < 0 || slash > colon {
			return url
		}
	}
	return dir + string(filepath.Separator) + url
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
