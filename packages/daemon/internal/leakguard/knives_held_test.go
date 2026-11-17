package leakguard

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// The knives pass leaves out a commit that the fork or the entry's upstream already holds, since
// such a commit is the fork's history or upstream's text (TestACommitTheEntrysUpstreamHoldsIsNotReadForKnivesTerms).
// What says so is a branch or a tag of the fork or of the upstream, as each reports its refs at
// the push. No ref of the repository's own says so, whatever the remote it is under is named and
// whatever URL that remote names: a commit pushed first to a local bare repository, as a cut is
// rehearsed before the real push of the release branch, a ref written by hand, and a ref of a
// remote whose URL names the fork or the upstream but which the fork or the upstream does not
// hold, are still the push's own commits, and the pass reads them.
func TestOnlyWhatTheForkOrTheUpstreamHoldsHoldsACommitForTheKnivesPass(t *testing.T) {
	refused := []string{"notes.md:2: forbidden term 2"}
	for _, tc := range []struct {
		name string
		// hold makes a ref of the repository's, of the fork's or of the upstream's reach head.
		hold func(t *testing.T, run func(dir string, argv ...string) string, repo, head string)
		want []string
	}{
		{
			name: "a commit pushed first to a local bare repository",
			hold: func(t *testing.T, run func(string, ...string) string, repo, _ string) {
				rehearsal := filepath.Join(t.TempDir(), "rehearsal.git")
				run(repo, "git", "init", "--quiet", "--bare", rehearsal)
				run(repo, "git", "remote", "add", "scratch", rehearsal)
				run(repo, "git", "push", "--quiet", "scratch", "HEAD:refs/heads/rehearsal")
			},
			want: refused,
		},
		{
			name: "a commit a remote-tracking ref of no configured remote holds",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "update-ref", "refs/remotes/anything/x", head)
			},
			want: refused,
		},
		{
			name: "a ref written by hand under the fork's own remote",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "remote", "add", "origin", knivesOrigin)
				run(repo, "git", "update-ref", "refs/remotes/origin/x", head)
			},
			want: refused,
		},
		{
			name: "a remote named upstream whose URL names another repository",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "remote", "add", "upstream", "https://github.com/another-org/another-repo")
				run(repo, "git", "update-ref", "refs/remotes/upstream/main", head)
			},
			want: refused,
		},
		{
			name: "a remote of any name whose URL names the entry's upstream in another spelling",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "remote", "add", "mirror", "git@github.com:Upstream-Org/invented-repo.git")
				run(repo, "git", "update-ref", "refs/remotes/mirror/main", head)
			},
			want: refused,
		},
		{
			name: "a remote of any name whose URL names the push's destination in another spelling",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "remote", "add", "fork", "git@github.com:Invented-Org/invented-repo")
				run(repo, "git", "update-ref", "refs/remotes/fork/earlier", head)
			},
			want: refused,
		},
		{
			// git writes the refs of a remote named fork/rehearsal at refs/remotes/fork/rehearsal/*,
			// under the fork's remote's own refs/remotes/fork/, with no ref written by hand. git
			// 2.56 refuses `git remote add` of such a name ("is a subset of existing remote"), and
			// a configuration written by hand still holds one.
			name: "a local bare remote whose name is the fork's remote's name and a slash",
			hold: func(t *testing.T, run func(string, ...string) string, repo, _ string) {
				rehearsal := filepath.Join(t.TempDir(), "rehearsal.git")
				run(repo, "git", "init", "--quiet", "--bare", rehearsal)
				run(repo, "git", "remote", "add", "fork", knivesOrigin)
				run(repo, "git", "config", "remote.fork/rehearsal.url", rehearsal)
				run(repo, "git", "config", "remote.fork/rehearsal.fetch", "+refs/heads/*:refs/remotes/fork/rehearsal/*")
				run(repo, "git", "push", "--quiet", "fork/rehearsal", "HEAD:refs/heads/rehearsal")
			},
			want: refused,
		},
		{
			name: "a local bare remote whose name is the upstream's remote's name and a slash",
			hold: func(t *testing.T, run func(string, ...string) string, repo, _ string) {
				rehearsal := filepath.Join(t.TempDir(), "rehearsal.git")
				run(repo, "git", "init", "--quiet", "--bare", rehearsal)
				run(repo, "git", "remote", "add", "upstream", knivesUpstream)
				run(repo, "git", "config", "remote.upstream/rehearsal.url", rehearsal)
				run(repo, "git", "config", "remote.upstream/rehearsal.fetch", "+refs/heads/*:refs/remotes/upstream/rehearsal/*")
				run(repo, "git", "push", "--quiet", "upstream/rehearsal", "HEAD:refs/heads/rehearsal")
			},
			want: refused,
		},
		{
			// The push of the fork's own remote writes refs/remotes/origin/main, at the commit,
			// while the remote names the local bare; the remote then names the fork.
			name: "the fork's remote made at a local bare, pushed to, and pointed at the fork",
			hold: func(t *testing.T, run func(string, ...string) string, repo, _ string) {
				rehearsal := filepath.Join(t.TempDir(), "rehearsal.git")
				run(repo, "git", "init", "--quiet", "--bare", rehearsal)
				run(repo, "git", "remote", "add", "origin", rehearsal)
				run(repo, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")
				run(repo, "git", "remote", "set-url", "origin", knivesOrigin)
			},
			want: refused,
		},
		{
			name: "the fork holds the commit at a branch of its own",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "push", "--quiet", knivesOrigin, head+":refs/heads/earlier")
			},
		},
		{
			name: "the upstream holds the commit at a branch of its own",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "push", "--quiet", knivesUpstream, head+":refs/heads/main")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a line of ordinary prose\nthe QUILLWORTHY build\n"})
			tc.hold(t, fixture.Run, repo, head)
			path, listed, _ := servingKnives(t, theKnivesRow)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("the push of a commit the fork or its upstream holds was refused: %v", err)
				}
				return
			}
			if got := refusal(t, err); !slices.Equal(got.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", got.Lines, tc.want)
			}
		})
	}
}

// A role rehearses a push to the fork by pointing the fork's remote's push URL at a local bare
// repository for one run (`git remote set-url --push origin <bare>`). That rehearsal is a push to
// the fork: the remote's URL still names it, and a push URL at a local bare names no GitHub
// repository (destinationRepos), so the pass reads the rehearsal and refuses it. git writes
// origin's remote-tracking ref for the rehearsed branch all the same, and that ref holds nothing
// for the real push once the push URL is taken away: the fork does not hold the commit, so that
// push is refused too.
func TestARehearsalOnTheForksPushURLIsReadAndHoldsNothingForTheRealPush(t *testing.T) {
	want := []string{"notes.md:2: forbidden term 2"}
	fixture, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a line of ordinary prose\nthe QUILLWORTHY build\n"})
	rehearsal := filepath.Join(t.TempDir(), "rehearsal.git")
	fixture.Run(repo, fixturegit.InitBare(rehearsal, "main")...)
	fixture.Run(repo, "git", "remote", "add", "origin", knivesOrigin)
	fixture.Run(repo, "git", "remote", "set-url", "--push", "origin", rehearsal)
	path, listed, _ := servingKnives(t, theKnivesRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)

	// git hands the hook the remote and its push URL, and the rehearsal holds no main yet.
	if got := knivesRefusal(t, guard.Push(bound(t), knivesPush(repo, noSHA, head, "origin", rehearsal))); !slices.Equal(got.Lines, want) {
		t.Errorf("the rehearsal's report is %q, want %q", got.Lines, want)
	}
	// The rehearsal goes out all the same, as one made with no hook would, and the push URL is
	// taken away after it.
	fixture.Run(repo, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")
	fixture.Run(repo, "git", "config", "--unset", "remote.origin.pushurl")
	if tracked := fixture.Run(repo, "git", "rev-parse", "refs/remotes/origin/main"); tracked != head {
		t.Fatalf("origin/main is at %s after the rehearsal, want the commit %s", tracked, head)
	}
	if got := knivesRefusal(t, guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))); !slices.Equal(got.Lines, want) {
		t.Errorf("the real push's report is %q, want %q", got.Lines, want)
	}
}

// A read of what the destination or the upstream holds that fails refuses the push as a setup
// failure, naming the remote and the URL read, never a push read as one whose destination holds
// nothing. The destination is read for every push that adds a commit, and the upstream only for a
// push the rules or the knives pass would otherwise refuse, so a push that adds no term and no
// private name passes with its upstream unread. Each URL reaches a path that does not exist
// (url.<base>.insteadOf): the destination is the fork named without .git, which knivesCommit's
// bare is not reached from, and the upstream one the registry names in place of
// theKnivesRegistry's.
func TestAReadOfWhatTheDestinationOrTheUpstreamHoldsThatFailsRefusesThePush(t *testing.T) {
	const fork = "https://github.com/invented-org/invented-repo"
	const upstream = "https://github.com/upstream-org/absent-repo"
	registry := theKnivesRow
	registry.file = strings.ReplaceAll(theKnivesRegistry, knivesUpstream, upstream)
	for _, tc := range []struct {
		name, line, url string
		// want is the failure's beginning, which git's own lines follow, and "" where the push
		// passes.
		want string
	}{
		{
			name: "the destination cannot be read",
			line: "a line of ordinary prose\n",
			url:  fork,
			want: "LEAK CHECK SETUP ERROR: the push to origin: the destination's refs could not be read: git ls-remote " + fork + " exited 128: ",
		},
		{
			name: "the upstream cannot be read, for a push the pass would refuse",
			line: "the QUILLWORTHY build\n",
			url:  knivesOrigin,
			want: "LEAK CHECK SETUP ERROR: the push to origin: the refs of the registry entry's upstream could not be read: git ls-remote " + upstream + " exited 128: ",
		},
		{
			name: "the upstream cannot be read, for a push the rules would refuse",
			line: upstreamNamesAHost,
			url:  knivesOrigin,
			want: "LEAK CHECK SETUP ERROR: the push to origin: the refs of the registry entry's upstream could not be read: git ls-remote " + upstream + " exited 128: ",
		},
		{
			name: "the upstream cannot be read, for a push that adds no term",
			line: "a line of ordinary prose\n",
			url:  knivesOrigin,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, repo, base, head := knivesCommit(t, map[string]string{"notes.md": tc.line})
			for _, url := range []string{fork, upstream} {
				reach(t, fixture, repo, url, filepath.Join(t.TempDir(), "absent.git"))
			}
			path, listed, _ := servingKnives(t, registry)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", tc.url))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("a push that adds no term, its upstream unreadable: %v", err)
				}
				return
			}
			setupFailure(t, err)
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("the failure is\n  %s\nwant it to begin\n  %s", err, tc.want)
			}
		})
	}
}
