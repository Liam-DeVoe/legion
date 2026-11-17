package leakguard

import (
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// A fork's branches take in its upstream's own commits: the mirror branch is fast-forwarded to
// upstream's main, and a release cut merges upstream's main into each member. Such a commit is
// upstream's text, not the fork's, so the knives pass reads none of it, as `knives audit` does,
// whose base for a branch is its fork point with upstream's trunk, and as K1 of INFRA-1484 reads a
// past update: a commit upstream's main holds adds nothing. Upstream's own lines do hold listed
// terms: on 2026-11-14 can1357/oh-my-pi's c3b7bed169a6 added a line holding one, and a push of it
// to the fork's main, whose remote-tracking ref held its parent, was refused by this pass.
//
// The commit is upstream's where the repository's remote for the entry's upstream holds it on a
// remote-tracking ref, as a fetch of that remote leaves it. Where that ref does not hold it, the
// same commit is the push's own, and is refused.
func TestACommitTheEntrysUpstreamHoldsIsNotReadForKnivesTerms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream func(base, head string) string
		want     []string
	}{
		{name: "the upstream remote's main holds the commit", upstream: func(_, head string) string { return head }},
		{
			name:     "the upstream remote's main holds only the commit before it",
			upstream: func(base, _ string) string { return base },
			want:     []string{"upstream.md:2: forbidden term 2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, repo, base, head := knivesCommit(t, map[string]string{"upstream.md": "a line of upstream's prose\nthe QUILLWORTHY build\n"})
			// The remote the registry entry names as its upstream, fetched: its main at the commit.
			fixture.Run(repo, "git", "remote", "add", "upstream", "https://github.com/upstream-org/invented-repo")
			fixture.Run(repo, "git", "update-ref", "refs/remotes/upstream/main", tc.upstream(base, head))
			path, listed, _ := servingKnives(t, theKnivesRow)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("the push of a commit upstream's main holds was refused: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}
