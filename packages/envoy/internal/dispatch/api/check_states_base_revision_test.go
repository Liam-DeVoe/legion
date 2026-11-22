// check_states_base_revision_test.go holds the settler to the base revision of GitHub's answer
// about which commits are a pull request's own: the base branch can move with no change of its
// name, so the answer is about the head and base revisions Dispatch stores for the pull request
// when the read is claimed, and applies, positive or negative, only where the pull request still
// names them; an answer about revisions since stored anew applies nothing, keeps the candidate
// unverified and its obligation, and the next settle reads again. The rig is check_states_test.go's.
package api

import (
	"context"
	"fmt"
	"maps"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// storeBaseRevision writes #number again at sha, open and attributed to the rig's issue as
// upsertHead writes it, with its base branch at baseSHA, as a read of the pull request that found
// the base branch there writes it ("" for a read that named none).
func storeBaseRevision(t *testing.T, rig *checkStatesRig, number int, sha, baseSHA string) {
	t.Helper()
	attributed := rig.issue
	if err := delivery.UpsertPullRequestHead(context.Background(), rig.database.Pool, nil, delivery.PullRequestHead{
		Repo: ciJobRepository, Number: number, HeadSHA: sha, HeadRef: "legion/work", BaseRef: "main", BaseSHA: baseSHA,
		State: "open", IssueKey: &attributed, AttributionSource: "title", AttributionComplete: true, Title: "A change",
		URL: fmt.Sprintf("https://github.com/%s/pull/%d", ciJobRepository, number),
	}); err != nil {
		t.Fatalf("write #%d at %s with its base branch at %q: %v", number, sha, baseSHA, err)
	}
}

// The base revisions of the tests below.
const (
	baseRevisionA = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	baseRevisionB = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
)

// TestAMembershipAnswerAboutABaseRevisionSinceStoredAppliesNothing: the tester recorded k1 passing
// at H1, and #70 stands at H2 with its base branch, main, at one commit, so H1 is #70's candidate.
// A settle reads #70's commits and is held after GitHub answered and before it applies the
// answer. Meanwhile main moves on to another commit and Dispatch stores it, by a write of #70's
// row with the same head and the same branch name, and GitHub's list of #70's commits changes
// with it. An answer about the earlier base revision applies nothing, positive or negative: H1
// stays a candidate, its obligation stays, and the next settle reads #70 again and decides it by
// the list that holds now. The neighbours write #70's row again with the base revision unchanged,
// and the held answer applies.
//
// Positive: GitHub listed H1 before the move and lists it no more after (main took H1), so k1 is
// published nowhere where main moved, and yes at H1 where it did not. Negative: GitHub left H1 out
// before the move and lists it after, so k1 is published yes at H1 where main moved, and nothing
// where it did not.
func TestAMembershipAnswerAboutABaseRevisionSinceStoredAppliesNothing(t *testing.T) {
	for _, row := range []struct {
		name          string
		listed, moved bool
	}{
		{name: "a positive answer, the base revision unchanged", listed: true},
		{name: "a positive answer, the base revision moved since", listed: true, moved: true},
		{name: "a negative answer, the base revision unchanged"},
		{name: "a negative answer, the base revision moved since", moved: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			rig := newCheckStatesRig(t, checkStatesTwoChecks)
			eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
			storeBaseRevision(t, rig, 70, eachHeadH2, baseRevisionA)
			if got := candidatesOf(t, rig); !maps.Equal(got, map[string]bool{eachHeadH1: false}) {
				t.Fatalf("#70 holds the former heads %v, want H1 unverified: this test holds nothing", got)
			}
			before, after := []string{eachHeadH2}, []string{eachHeadH1, eachHeadH2}
			if row.listed {
				before, after = after, before
			}
			rig.github.setPullCommits(70, before...)
			entered, release := rig.github.holdPullCommitsAnswer(t, 70)
			first := settleInTheBackground(rig.settler, rig.issue)
			ciAwait(t, "the settle's read of #70's commits", entered)
			if row.moved {
				rig.github.setPullCommits(70, after...)
				storeBaseRevision(t, rig, 70, eachHeadH2, baseRevisionB)
			} else {
				storeBaseRevision(t, rig, 70, eachHeadH2, baseRevisionA)
			}
			release()
			ciAwait(t, "the held settle to end", first)
			if row.moved {
				if got := candidatesOf(t, rig); !maps.Equal(got, map[string]bool{eachHeadH1: false}) {
					t.Fatalf("#70 holds the former heads %v once the answer about the earlier base revision was applied, want H1 unverified still: that answer applies nothing", got)
				}
				if marks := fenceMarksOf(t, rig, rig.issue); len(marks) == 0 {
					t.Fatalf("%s holds no mark a handoff waits for once the answer about the earlier base revision applied nothing, want its obligation kept", rig.issue)
				}
			}
			if _, err := rig.database.Pool.Exec(context.Background(), `select check_states_owe($1)`, rig.issue); err != nil {
				t.Fatalf("owe %s a settle: %v", rig.issue, err)
			}
			settleEveryMark(t, rig)
			want := ""
			if row.listed != row.moved {
				want = model.PassStandsYes
			}
			if got := stateAt(t, rig, rig.issue, eachHeadH1, "k1"); got != want {
				t.Fatalf("k1 at H1 is published %q, want %q: an answer applies only to the base revision it was read about", got, want)
			}
		})
	}
}

// TestAnAnswerAboutARowWithNoStoredBaseRevisionAppliesUntilAWriteStoresOne: #70's row holds no base
// revision, as a row a release before this one wrote, or one an envelope that named none wrote.
// GitHub lists H1 among #70's commits, and the settle that reads them is held before it applies
// the answer. Where nothing changes meanwhile, the answer applies, so k1 is published yes at H1 and
// the read is made once. Where a write meanwhile stores the base revision GitHub now names, and
// GitHub's list leaves H1 out with it, the answer applies nothing, and the settle after reads #70
// once more and drops H1: two reads in all, and no obligation left, so a row whose revision is not
// known neither applies an answer across a stored change nor reads for ever.
func TestAnAnswerAboutARowWithNoStoredBaseRevisionAppliesUntilAWriteStoresOne(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "nothing is stored meanwhile"
		if stored {
			name = "a write stores the base revision meanwhile"
		}
		t.Run(name, func(t *testing.T) {
			rig := newCheckStatesRig(t, checkStatesTwoChecks)
			eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
			storeBaseRevision(t, rig, 70, eachHeadH2, "")
			if got := candidatesOf(t, rig); !maps.Equal(got, map[string]bool{eachHeadH1: false}) {
				t.Fatalf("#70 holds the former heads %v, want H1 unverified: this test holds nothing", got)
			}
			rig.github.setPullCommits(70, eachHeadH1, eachHeadH2)
			entered, release := rig.github.holdPullCommitsAnswer(t, 70)
			first := settleInTheBackground(rig.settler, rig.issue)
			ciAwait(t, "the settle's read of #70's commits", entered)
			if stored {
				rig.github.setPullCommits(70, eachHeadH2)
				storeBaseRevision(t, rig, 70, eachHeadH2, baseRevisionB)
			} else {
				storeBaseRevision(t, rig, 70, eachHeadH2, "")
			}
			release()
			ciAwait(t, "the held settle to end", first)
			settleEveryMark(t, rig)
			want, reads := model.PassStandsYes, 1
			if stored {
				want, reads = "", 2
			}
			if got := stateAt(t, rig, rig.issue, eachHeadH1, "k1"); got != want {
				t.Fatalf("k1 at H1 is published %q, want %q", got, want)
			}
			if got := rig.github.pullCommitReadsOf(70); got != reads {
				t.Fatalf("#70's commits were read %d times, want %d", got, reads)
			}
			if left := marksOf(t, rig.database, rig.issue); left != 0 {
				t.Fatalf("%s holds %d marks after the settles, want none: nothing is left to read", rig.issue, left)
			}
		})
	}
}
