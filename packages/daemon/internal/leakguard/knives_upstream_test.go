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
// to the fork's main, which held its parent, was refused by this pass.
//
// The commit is upstream's where a branch or a tag of the entry's upstream reaches it, as the
// upstream reports its refs at the push, an annotated tag by the commit it peels to, and whether
// or not this clone fetched that ref. Where none reaches it, the same commit is the push's own,
// and is refused.
func TestACommitTheEntrysUpstreamHoldsIsNotReadForKnivesTerms(t *testing.T) {
	for _, tc := range []struct {
		name string
		// upstream are the refs the entry's upstream holds, as `git push` names them, over the
		// repository's two commits and its annotated tag v1 of the second.
		upstream func(base, head string) []string
		want     []string
	}{
		{name: "the upstream's main holds the commit", upstream: func(_, head string) []string { return []string{head + ":refs/heads/main"} }},
		{name: "an annotated tag of the upstream's holds the commit", upstream: func(string, string) []string { return []string{"refs/tags/v1"} }},
		{
			name:     "the upstream's main holds only the commit before it",
			upstream: func(base, _ string) []string { return []string{base + ":refs/heads/main"} },
			want:     []string{"upstream.md:2: forbidden term 2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, repo, base, head := knivesCommit(t, map[string]string{"upstream.md": "a line of upstream's prose\nthe QUILLWORTHY build\n"})
			fixture.Run(repo, "git", "tag", "--annotate", "--message", "a release of upstream's", "v1", head)
			fixture.Run(repo, append([]string{"git", "push", "--quiet", knivesUpstream}, tc.upstream(base, head)...)...)
			path, listed, _ := servingKnives(t, theKnivesRow)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("the push of a commit upstream's refs hold was refused: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}
