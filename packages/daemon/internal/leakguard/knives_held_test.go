package leakguard

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// The knives pass leaves out a commit that the fork or the entry's upstream already holds, since
// such a commit is the fork's history or upstream's text (TestACommitTheEntrysUpstreamHoldsIsNotReadForKnivesTerms).
// What says so is a remote-tracking ref of a remote whose URL names the push's destination or the
// registry entry's upstream, the URL compared as the pass compares the destination. A ref of any
// other remote says nothing of either repository: a commit pushed first to a local bare repository,
// as a cut is rehearsed before the real push of the release branch, and a ref of no configured
// remote at all, are still the push's own commits, and the pass reads them.
func TestOnlyTheForksAndTheUpstreamsRemoteTrackingRefsHoldACommitForTheKnivesPass(t *testing.T) {
	refused := []string{"notes.md:2: forbidden term 2"}
	for _, tc := range []struct {
		name string
		// hold makes a remote-tracking ref of the repository reach head.
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
		},
		{
			name: "a remote of any name whose URL names the push's destination in another spelling",
			hold: func(_ *testing.T, run func(string, ...string) string, repo, head string) {
				run(repo, "git", "remote", "add", "fork", "git@github.com:Invented-Org/invented-repo")
				run(repo, "git", "update-ref", "refs/remotes/fork/earlier", head)
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
