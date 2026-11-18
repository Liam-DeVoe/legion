package leakguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// A push the destination already holds everything of adds no commit and so has no record but its
// ref name, which is what a push of an existing commit to a new name is.
func TestAPushThatAddsNoCommitReadsNoCommit(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	repo := filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("the file names "+theHost+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", "a commit naming "+theHost)
	head := fixture.Run(repo, "git", "rev-parse", "HEAD")
	// The destination holds that commit at that ref already, so the push moves nothing and the
	// commit's own text is none of its business.
	destinationAt(t, fixture, repo, theURL, head+":refs/heads/main")
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	err := guard.Push(bound(t), PushRequest{
		Dir:     repo,
		Remote:  "origin",
		URL:     theURL,
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: head}},
	})
	if err != nil {
		t.Fatalf("a push that adds no commit: %v", err)
	}
}

// The read of what the destination holds fetches nothing. In a partial clone, as an issue pod's
// fork clones are (--filter=blob:none), git fetches an object the clone lacks from the promisor
// remote the moment anything asks for it. The read of the destination's refs asks for every
// commit they are at (Guard.advertised), and the read of the commits an update adds asks for the
// one its remote side names (Guard.holds): a commit the clone lacks is answered missing instead,
// and holds nothing. The promisor here has a commit the clone never fetched, which the destination
// holds at its branch later; once the push is read, the clone still lacks it, and the push's own
// commit is read and refused. The push moves main, whose remote side is a commit the clone has,
// or later itself, whose remote side is the commit the clone lacks.
func TestTheReadOfWhatTheDestinationHoldsFetchesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		// ref is the ref the push moves, and atLater is whether its remote side is the commit the
		// clone lacks rather than the base.
		ref     string
		atLater bool
	}{
		{name: "a branch of the destination's", ref: "refs/heads/main"},
		{name: "the commit the update's remote side names", ref: "refs/heads/later", atLater: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			promisor, destination := filepath.Join(root, "promisor.git"), filepath.Join(root, "destination.git")
			seed, repo := filepath.Join(root, "seed"), filepath.Join(root, "repo")
			fixture := fixturegit.Isolated(t, home)
			fixture.Run(root, fixturegit.InitBare(promisor, "main")...)
			fixture.Run(promisor, "git", "config", "uploadpack.allowFilter", "true")
			fixture.Run(promisor, "git", "config", "uploadpack.allowAnySHA1InWant", "true")
			fixture.Run(root, fixturegit.InitBare(destination, "main")...)
			fixture.Run(root, fixturegit.InitWorkTree(seed, "main")...)
			commit := func(dir, name, content, message string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				fixture.Run(dir, "git", "add", "-A")
				fixture.Run(dir, "git", "commit", "--quiet", "-m", message)
				return fixture.Run(dir, "git", "rev-parse", "HEAD")
			}
			base := commit(seed, "README.md", "one\n", "the commit the destination holds")
			fixture.Run(seed, "git", "push", "--quiet", promisor, "main")
			fixture.Run(root, "git", "clone", "--quiet", "--filter=blob:none", "file://"+promisor, repo)
			later := commit(seed, "README.md", "one\ntwo\n", "a commit the clone never fetched")
			fixture.Run(seed, "git", "push", "--quiet", promisor, later+":refs/heads/later")
			fixture.Run(seed, "git", "push", "--quiet", destination, base+":refs/heads/main", later+":refs/heads/later")
			reach(t, fixture, repo, theURL, destination)
			// lacks is whether the clone lacks the later commit, asked so that the asking fetches
			// nothing.
			lacks := func() bool {
				_, err := fixture.With("GIT_NO_LAZY_FETCH=1").Try(repo, "git", "cat-file", "-e", later)
				return err != nil
			}
			if !lacks() {
				t.Fatalf("the clone has %s before the push is read, so this test proves nothing", short(later))
			}
			head := commit(repo, "notes.md", "the runner answers at "+theHost+"\n", "a message of no interest")
			remoteSHA := base
			if tc.atLater {
				remoteSHA = later
			}
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     theURL,
				Updates: []Update{{LocalRef: tc.ref, LocalSHA: head, RemoteRef: tc.ref, RemoteSHA: remoteSHA}},
			})
			want := []string{"notes.md: " + theClass}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
				t.Errorf("the report is %q, want %q", refused.Lines, want)
			}
			if !lacks() {
				t.Errorf("the read of the push fetched %s, the commit the destination's later branch is at, into the clone", short(later))
			}
		})
	}
}

// A failure that names a URL carries no credential. The URL git hands a pre-push hook, and the
// upstream a knives registry entry names, can hold a user and a token
// (https://user:token@host/...), and a read of either that fails names the URL and quotes git,
// which names it too: both are written without the user and the token. The upstream is read for
// a push the knives pass would refuse and for one the private-name rules would. A push made to a
// URL rather than to a remote, `git push https://user:token@host/...`, hands the hook that URL as
// the remote's name too, which every failure of the push names (PushName). The stand-in git here
// fails every call with a URL that holds a user, as a git that cannot reach the host does, and
// runs the real git for every other call.
func TestAFailedReadNamesItsURLWithoutItsCredential(t *testing.T) {
	// The userinfo is built apart from the scheme, so no line of this file is a URL that carries
	// one, which the shared check would refuse at the push.
	const user, token = "invented-user", "invented-token"
	userinfo := user + ":" + token + "@"
	credentialed := func(url string) string { return strings.Replace(url, "https://", "https://"+userinfo, 1) }
	bin := t.TempDir()
	writeProgram(t, bin, "git", `for arg; do
  case "$arg" in
  *://*@*) echo "fatal: unable to access '$arg/': Could not resolve host: github.com" >&2; exit 128 ;;
  esac
done
exec `+shQuote(fixturegit.CompiledGit(t))+` "$@"
`)
	unreachable := func(url string) string {
		return " exited 128: fatal: unable to access '" + url + "/': Could not resolve host: github.com"
	}
	for _, tc := range []struct {
		name string
		// remote is the remote's name git hands the hook, "origin" where the case leaves it empty;
		// url is the URL it hands it, and upstream the one the registry entry names. line is the
		// line the pushed commit adds, one holding a knives term where the case leaves it empty.
		remote, url, upstream, line string
		want                        string
	}{
		{
			name:     "the destination",
			url:      credentialed(knivesOrigin),
			upstream: knivesUpstream,
			want:     "LEAK CHECK SETUP ERROR: the push to origin: the destination's refs could not be read: git ls-remote " + knivesOrigin + unreachable(knivesOrigin),
		},
		{
			name:     "the destination named by its URL",
			remote:   credentialed(knivesOrigin),
			url:      credentialed(knivesOrigin),
			upstream: knivesUpstream,
			want:     "LEAK CHECK SETUP ERROR: the push to " + knivesOrigin + ": the destination's refs could not be read: git ls-remote " + knivesOrigin + unreachable(knivesOrigin),
		},
		{
			name:     "the registry entry's upstream",
			url:      knivesOrigin,
			upstream: credentialed(knivesUpstream),
			want:     "LEAK CHECK SETUP ERROR: the push to origin: the refs of the registry entry's upstream could not be read: git ls-remote " + knivesUpstream + unreachable(knivesUpstream),
		},
		{
			name:     "the registry entry's upstream, for a push the private-name rules would refuse",
			url:      knivesOrigin,
			upstream: credentialed(knivesUpstream),
			line:     upstreamNamesAHost,
			want:     "LEAK CHECK SETUP ERROR: the push to origin: the refs of the registry entry's upstream could not be read: git ls-remote " + knivesUpstream + unreachable(knivesUpstream),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := tc.line
			if line == "" {
				line = "the QUILLWORTHY build\n"
			}
			_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": line})
			registry := theKnivesRow
			registry.file = strings.ReplaceAll(theKnivesRegistry, knivesUpstream, tc.upstream)
			path, listed, _ := servingKnives(t, registry)
			path = pathFirst(bin, path)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			remote := tc.remote
			if remote == "" {
				remote = "origin"
			}
			err := guard.Push(bound(t), knivesPush(repo, base, head, remote, tc.url))
			setupFailure(t, err)
			if err.Error() != tc.want {
				t.Errorf("the failure is\n  %s\nwant\n  %s", err, tc.want)
			}
			if strings.Contains(err.Error(), user) || strings.Contains(err.Error(), token) {
				t.Errorf("the failure carries the URL's credential: %v", err)
			}
		})
	}
}

// A forward merge, the default branch merged onto an older tip of a branch the destination
// holds, reads no commit the destination's branches reach. The default branch's commit here adds
// a line the rules refuse, and the destination's main holds it, so the push of the merge does not
// publish it, whichever ref the push moves. A commit that adds the same line on top of the merge
// is on no branch of the destination, so it is read and refused, and it is the whole of the
// report.
func TestAForwardMergeReadsNoCommitTheDestinationHolds(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitBare(remote, "main")...)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(message string) string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
		return fixture.Run(repo, "git", "rev-parse", "HEAD")
	}
	write("README.md", "one\n")
	commit("the first commit")
	fixture.Run(repo, "git", "checkout", "--quiet", "-b", "work")
	write("work.md", "a line of the branch\n")
	tip := commit("the branch's own commit")
	fixture.Run(repo, "git", "checkout", "--quiet", "main")
	write("main.md", "the runner answers at "+theHost+"\n")
	commit("a commit of the default branch")
	// The destination holds both branches: its work is the old tip the push moves, and its main
	// holds the default branch's commit.
	fixture.Run(repo, "git", "remote", "add", "origin", remote)
	fixture.Run(repo, "git", "push", "--quiet", "origin", "main", "work")
	reach(t, fixture, repo, theURL, remote)
	fixture.Run(repo, "git", "checkout", "--quiet", "work")
	fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "-m", "merge main into work", "main")
	merge := fixture.Run(repo, "git", "rev-parse", "HEAD")
	if parents := fixture.Run(repo, "git", "rev-list", "--parents", "-n", "1", merge); len(strings.Fields(parents)) != 3 {
		t.Fatalf("the fixture's merge is %q, want a commit of two parents", parents)
	}
	write("copy.md", "the runner answers at "+theHost+"\n")
	onTop := commit("a commit the destination does not hold")

	for _, tc := range []struct {
		name string
		head string
		// want is the report, and nil where the push passes.
		want []string
	}{
		{name: "the merge alone", head: merge},
		{
			name: "a commit of the same line on top of the merge",
			head: onTop,
			want: []string{"copy.md: " + theClass},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     theURL,
				Updates: []Update{{LocalRef: "refs/heads/work", LocalSHA: tc.head, RemoteRef: "refs/heads/work", RemoteSHA: tip}},
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("the push of the merge alone: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}

// A commit a rehearsal holds is still one the real push publishes. Before the real push of a
// release branch, a role rehearses it on a local bare repository: through a remote of its own, or
// by pointing the pushed remote's push URL at the bare for one run. Either way git writes a
// remote-tracking ref under the pushed remote's own refs/remotes/origin/ — for a remote named
// origin/rehearsal at refs/remotes/origin/rehearsal/*, and for origin's own push URL at
// refs/remotes/origin/* — and that ref says nothing of what the destination holds. The commit
// here adds a line the rules refuse, and the destination, whose main is at the commit before it,
// does not hold it, so the push to origin reads it and is refused. A push to the rehearsal itself
// reads none of what the rehearsal holds.
func TestACommitARehearsalHoldsIsReadAtThePushToTheDestination(t *testing.T) {
	for _, tc := range []struct {
		name string
		// rehearse pushes HEAD to rehearsal's refs/heads/rehearsal, and answers the remote that
		// push named.
		rehearse func(run func(string, ...string) string, repo, rehearsal string) string
		// tracked is the remote-tracking ref the rehearsal's push wrote.
		tracked string
	}{
		{
			// git 2.56 refuses `git remote add` of a name that is another remote's and a slash
			// ("is a subset of existing remote"); a configuration written by hand still holds one.
			name: "a remote named origin/rehearsal",
			rehearse: func(run func(string, ...string) string, repo, rehearsal string) string {
				run(repo, "git", "config", "remote.origin/rehearsal.url", rehearsal)
				run(repo, "git", "config", "remote.origin/rehearsal.fetch", "+refs/heads/*:refs/remotes/origin/rehearsal/*")
				run(repo, "git", "push", "--quiet", "origin/rehearsal", "HEAD:refs/heads/rehearsal")
				return "origin/rehearsal"
			},
			tracked: "refs/remotes/origin/rehearsal/rehearsal",
		},
		{
			name: "origin's push URL pointed at the rehearsal for one push",
			rehearse: func(run func(string, ...string) string, repo, rehearsal string) string {
				run(repo, "git", "remote", "set-url", "--push", "origin", rehearsal)
				run(repo, "git", "push", "--quiet", "origin", "HEAD:refs/heads/rehearsal")
				run(repo, "git", "config", "--unset", "remote.origin.pushurl")
				return "origin"
			},
			tracked: "refs/remotes/origin/rehearsal",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			remote, rehearsal, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "rehearsal.git"), filepath.Join(root, "repo")
			fixture := fixturegit.Isolated(t, home)
			fixture.Run(root, fixturegit.InitBare(remote, "main")...)
			fixture.Run(root, fixturegit.InitBare(rehearsal, "main")...)
			fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
			write := func(content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			commit := func(message string) string {
				t.Helper()
				fixture.Run(repo, "git", "add", "-A")
				fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
				return fixture.Run(repo, "git", "rev-parse", "HEAD")
			}
			write("one\n")
			base := commit("the commit the destination holds")
			fixture.Run(repo, "git", "remote", "add", "origin", remote)
			fixture.Run(repo, "git", "push", "--quiet", "origin", "main")
			reach(t, fixture, repo, theURL, remote)
			write("one\nthe runner answers at " + theHost + "\n")
			head := commit("a message of no interest")
			rehearsed := tc.rehearse(fixture.Run, repo, rehearsal)
			if held := fixture.Run(repo, "git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/"); held != "refs/remotes/origin/main "+base+"\n"+tc.tracked+" "+head {
				t.Fatalf("the fixture's remote-tracking refs are %q, want origin/main at the base and %s at the commit", held, tc.tracked)
			}
			for _, push := range []struct {
				name string
				// remote and url are what git hands the hook; remoteSHA is the commit the push's
				// destination holds at refs/heads/main.
				remote, url, remoteSHA string
				// want is the report, and nil where the push passes.
				want []string
			}{
				{name: "the push to origin", remote: "origin", url: theURL, remoteSHA: base, want: []string{"README.md: " + theClass}},
				{name: "the push to the rehearsal", remote: rehearsed, url: rehearsal, remoteSHA: noSHA},
			} {
				guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
				err := guard.Push(bound(t), PushRequest{
					Dir:     repo,
					Remote:  push.remote,
					URL:     push.url,
					Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: push.remoteSHA}},
				})
				if push.want == nil {
					if err != nil {
						t.Errorf("%s, of a commit the rehearsal holds: %v", push.name, err)
					}
					continue
				}
				if refused := refusal(t, err); !slices.Equal(refused.Lines, push.want) {
					t.Errorf("%s: the report is %q, want %q", push.name, refused.Lines, push.want)
				}
			}
		})
	}
}
