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
			rig.github.setPull(70, eachHeadH2, "main", baseRevisionA)
			entered, release := rig.github.holdPullCommitsAnswer(t, 70)
			first := settleInTheBackground(rig.settler, rig.issue)
			ciAwait(t, "the settle's read of #70's commits", entered)
			if row.moved {
				rig.github.setPullCommits(70, after...)
				rig.github.setPull(70, eachHeadH2, "main", baseRevisionB)
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
				rig.github.setPull(70, eachHeadH2, "main", baseRevisionB)
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

// TestAMembershipAnswerAboutRevisionsDispatchHasNotStoredAppliesNothing: the tester recorded k1
// passing at H1, and Dispatch stores #70 at H2 with its base branch, main, at baseRevisionA, so H1
// is #70's candidate. GitHub has moved #70 on where Dispatch has not stored it yet: its base branch
// is at baseRevisionB, or its head is H3; or GitHub's whole list of #70's commits is of an earlier
// head than the one its read of #70 names. The answer a read makes there is about revisions the
// claim did not name, whichever way it answered and whether GitHub's list of #70's commits is
// whole or capped: it applies nothing, H1 stays a candidate and its obligation stays, read again
// after the failure backoff, not a second on, since no read can apply until a write stores what
// GitHub names. Once Dispatch stores what GitHub names, or GitHub's list catches up, the next
// settle reads #70 again and applies what GitHub answers then. The neighbour's GitHub stands at
// the revisions Dispatch stores, and its answer applies at once.
func TestAMembershipAnswerAboutRevisionsDispatchHasNotStoredAppliesNothing(t *testing.T) {
	capped := make([]string, 251)
	for index := range capped {
		capped[index] = fmt.Sprintf("%040x", 0xd0000+index)
	}
	for _, row := range []struct {
		name string
		// head is the head Dispatch stores for #70; pull, base and listed are GitHub's: the head
		// and base revision its read of #70 names, and the commits its list names.
		head, pull, base string
		listed           []string
		// caught is what moves Dispatch, or GitHub, to agree: a write of #70 at the head and base
		// revision GitHub names, or GitHub's list of #70's commits at the head it names.
		caught func(t *testing.T, rig *checkStatesRig)
		// applied is whether the first settle's answer applies; want is k1's state at H1 once both
		// agree.
		applied bool
		want    string
	}{
		{name: "GitHub stands at the stored revisions", head: eachHeadH2, pull: eachHeadH2, base: baseRevisionA,
			listed: []string{eachHeadH1, eachHeadH2}, applied: true, want: model.PassStandsYes},
		{name: "the base branch moved on and the whole list leaves H1 out", head: eachHeadH2, pull: eachHeadH2, base: baseRevisionB,
			listed: []string{eachHeadH2}, want: "",
			caught: func(t *testing.T, rig *checkStatesRig) { storeBaseRevision(t, rig, 70, eachHeadH2, baseRevisionB) }},
		{name: "the base branch moved on and the whole list names H1", head: eachHeadH2, pull: eachHeadH2, base: baseRevisionB,
			listed: []string{eachHeadH1, eachHeadH2}, want: model.PassStandsYes,
			caught: func(t *testing.T, rig *checkStatesRig) { storeBaseRevision(t, rig, 70, eachHeadH2, baseRevisionB) }},
		{name: "the base branch moved on and the capped list leaves H1 to the comparisons", head: capped[250], pull: capped[250], base: baseRevisionB,
			listed: capped, want: "",
			caught: func(t *testing.T, rig *checkStatesRig) { storeBaseRevision(t, rig, 70, capped[250], baseRevisionB) }},
		{name: "the head moved on", head: eachHeadH2, pull: eachHeadH3, base: baseRevisionA,
			listed: []string{eachHeadH3}, want: "",
			caught: func(t *testing.T, rig *checkStatesRig) { storeBaseRevision(t, rig, 70, eachHeadH3, baseRevisionA) }},
		{name: "the whole list is of an earlier head than the read of #70 names", head: eachHeadH2, pull: eachHeadH2, base: baseRevisionA,
			listed: []string{eachHeadH1}, want: model.PassStandsYes,
			caught: func(t *testing.T, rig *checkStatesRig) { rig.github.setPullCommits(70, eachHeadH1, eachHeadH2) }},
	} {
		t.Run(row.name, func(t *testing.T) {
			rig := newCheckStatesRig(t, checkStatesTwoChecks)
			eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", eachHeadH1)
			storeBaseRevision(t, rig, 70, row.head, baseRevisionA)
			if got := candidatesOf(t, rig); !maps.Equal(got, map[string]bool{eachHeadH1: false}) {
				t.Fatalf("#70 holds the former heads %v, want H1 unverified: this test holds nothing", got)
			}
			rig.github.setPullCommits(70, row.listed...)
			rig.github.setPull(70, row.pull, "main", row.base)
			// Where the capped list leaves H1 out, the comparisons decide it: GitHub's head holds
			// H1 and so does the base revision it names, so H1 is outside.
			rig.github.setCompare(row.pull, eachHeadH1, ciJobCompare{status: "behind"})
			rig.github.setCompare(row.base, eachHeadH1, ciJobCompare{status: "behind"})
			settleOwed(t, rig)
			got := candidatesOf(t, rig)
			if row.applied {
				if !maps.Equal(got, map[string]bool{eachHeadH1: true}) {
					t.Fatalf("#70 holds the former heads %v once GitHub answered at the stored revisions, want H1 verified", got)
				}
			} else {
				if !maps.Equal(got, map[string]bool{eachHeadH1: false}) {
					t.Fatalf("#70 holds the former heads %v once GitHub answered at %s onto %s, which Dispatch does not store, want H1 unverified still: that answer applies nothing", got, row.pull[:4], row.base[:4])
				}
				if marks := fenceMarksOf(t, rig, rig.issue); len(marks) == 0 {
					t.Fatalf("%s holds no mark a handoff waits for once the answer about revisions Dispatch does not store applied nothing, want its obligation kept", rig.issue)
				}
				var failures int
				var due float64
				if err := rig.database.Pool.QueryRow(context.Background(), `
					select coalesce(min(failures), 0), coalesce(min(extract(epoch from due_at - now())), 0)::float8
					from check_states_owed where issue_key = $1`, rig.issue).Scan(&failures, &due); err != nil {
					t.Fatalf("read the marks of %s: %v", rig.issue, err)
				}
				if failures < 1 || due < 20 {
					t.Fatalf("%s's marks count %d failures and fall due in %.1fs once the answer about revisions Dispatch does not store applied nothing, want a failure counted and the 30 s backoff: a read a second on answers about the same revisions", rig.issue, failures, due)
				}
				row.caught(t, rig)
			}
			if _, err := rig.database.Pool.Exec(context.Background(), `select check_states_owe($1)`, rig.issue); err != nil {
				t.Fatalf("owe %s a settle: %v", rig.issue, err)
			}
			settleEveryMark(t, rig)
			if got := stateAt(t, rig, rig.issue, eachHeadH1, "k1"); got != row.want {
				t.Fatalf("k1 at H1 is published %q, want %q: an answer applies only at the revisions the claim named", got, row.want)
			}
			verified, held := candidatesOf(t, rig)[eachHeadH1]
			if held != (row.want != "") || held && !verified {
				t.Fatalf("#70 holds H1 as held=%v verified=%v once Dispatch and GitHub agree, want it verified where GitHub's answer names it and gone where it does not", held, verified)
			}
		})
	}
}
