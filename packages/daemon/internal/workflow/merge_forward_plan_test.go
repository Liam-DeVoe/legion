package workflow

// A merge forward that lands asks the reviewer to read the merge only where the issue's task has a
// reviewer step that already ran (mergeForwardLanded). A task whose plan has none stands on no
// review of the old head, and no reviewer step runs for it, so nobody would ever approve the new
// head: the reviewer is asked nothing, and the merger goes on at the new head, told no wait for an
// approval, both by the landed message that reaches it in merging and by the task it is started
// with when it enters merging after the landing. The control is the same issue on a plan with a
// reviewer step, where the review is asked and both tell the merger to wait.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// approvalWait is the words that hold the merger's merge to the reviewer's approval of the head a
// merge forward left, in the landed message (record.MergeForwardMessage) and in its task (task).
const approvalWait = "only once the reviewer's approval of"

// plannedConflictIssue is seedConflictIssue on a run with plan rows: the owner's plan whose steps
// are steps, with task a running at the place of at: the step whose role runs as at, its retro or
// its merge.
func plannedConflictIssue(t *testing.T, at phase.Phase, steps ...string) *pgxpool.Pool {
	t.Helper()
	pool := seedConflictIssue(t, at, "retro", settledGreen("lint"))
	seedStepsPlan(t, pool, "LEGION-208", 1, steps...)
	place := record.TaskPlace{Kind: record.PlaceRetro}
	switch at {
	case phase.Merging:
		place = record.TaskPlace{Kind: record.PlaceMerge}
	case phase.Retro:
	default:
		roles := plan.BuiltinRoles()
		for i, word := range steps {
			if role, _, _ := strings.Cut(word, ":"); roles[role].Phase == at {
				place = record.TaskPlace{Kind: record.PlaceStep, Position: i + 1}
			}
		}
		if place.Kind != record.PlaceStep {
			t.Fatalf("the steps %v hold no step that runs as %s", steps, at)
		}
	}
	seedRecord(t, pool, func(tx pgx.Tx) error {
		return record.NewStore().MoveTask(t.Context(), tx, "LEGION-208", 1, "a", place, record.TaskRunning)
	})
	return pool
}

// toldWho is each merge-forward message as the role it goes to and its step, in the order written.
func toldWho(messages []record.MergeForwardMessage) []string {
	told := make([]string, len(messages))
	for i, message := range messages {
		told[i] = string(message.Role) + " " + string(message.Step)
	}
	return told
}

func TestAMergeForwardAsksTheReviewerOnlyOfATaskWithAReviewerStep(t *testing.T) {
	requiresCI := &record.BaseRules{Required: []string{"ci"}}
	for _, tc := range []struct {
		name   string
		steps  []string
		review bool
	}{
		{name: "a plan with no reviewer step", steps: []string{"implementer", "tester"}, review: false},
		{name: "control: a plan with a reviewer step", steps: []string{"implementer", "reviewer"}, review: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("the merger in merging", func(t *testing.T) {
				pool := plannedConflictIssue(t, phase.Merging, tc.steps...)
				confirmsConflict(t, pool, requiresCI)
				asked := len(mergeForwardMessages(t, pool))
				landsMergeForward(t, pool)

				got := mergeForwardMessages(t, pool)[asked:]
				want := []string{"merger landed"}
				if tc.review {
					want = append(want, "reviewer review")
				}
				if !reflect.DeepEqual(toldWho(got), want) {
					t.Fatalf("the landing queued %v, want %v", toldWho(got), want)
				}
				// The text is composed as the outbox composes it at delivery: from the pull request
				// as the record holds it (the daemon's mergeForwardMessage).
				recorded := recordedPR(t, pool, "LEGION-208")
				if told := got[0].Message("LEGION-208", recorded); strings.Contains(told, approvalWait) != tc.review {
					t.Fatalf("the merger was told %q; want a wait for the reviewer's approval: %t", told, tc.review)
				}
				if mf := recorded.MergeForward; mf == nil || mf.Landed != forwardHead || mf.ReviewAsked != tc.review {
					t.Fatalf("the record's merge forward = %+v, want it landed at %s with the review asked: %t", mf, forwardHead, tc.review)
				}
				if got := issuePhase(t, pool); got != phase.Merging {
					t.Fatalf("after the merge landed the issue is in %s, want merging", got)
				}
			})

			t.Run("a merger entering merging after the landing", func(t *testing.T) {
				pool := plannedConflictIssue(t, phase.Retro, tc.steps...)
				confirmsConflict(t, pool, requiresCI)
				asked := len(mergeForwardMessages(t, pool))
				landsMergeForward(t, pool)
				want := []string{}
				if tc.review {
					want = append(want, "reviewer review")
				}
				if got := toldWho(mergeForwardMessages(t, pool)[asked:]); !reflect.DeepEqual(got, want) {
					t.Fatalf("the landing in retro queued %v, want %v", got, want)
				}

				applyRefusingNothing(t, pool, readsAfter(4*time.Minute), intake.HandoffComplete{Generation: 1, Issue: "LEGION-208",
					Role: claim.RoleImplementer, Claim: "implement-claim", Summary: "retro done", Commit: forwardHead})
				if got := issuePhase(t, pool); got != phase.Merging {
					t.Fatalf("after retro's completion the issue is in %s, want merging", got)
				}
				if task := startTask(t, pool, claim.RoleMerger); task == "" || strings.Contains(task, approvalWait) != tc.review {
					t.Fatalf("the merger's task reads %q; want a wait for the reviewer's approval: %t", task, tc.review)
				}
			})
		})
	}
}

// A landed merge forward asks the reviewer to approve the new head only where the reviewer step
// of the issue's task already ran: it stands before the place the issue stands at. Such an issue
// stands on a review of the old head. A reviewer step still to come reviews the head the issue
// stands at when it runs, so it is asked nothing. So under `implementer acceptance:owner reviewer`
// a landing in accepting asks no review, and under `implementer reviewer tester` a landing in
// testing asks one, and the tester is told the landing as before. The controls are the other
// order of each pair.
func TestAMergeForwardAsksTheReviewerOnlyWhereItsStepAlreadyRan(t *testing.T) {
	requiresCI := &record.BaseRules{Required: []string{"ci"}}
	for _, tc := range []struct {
		name  string
		at    phase.Phase
		steps []string
		want  []string
	}{
		{name: "accepting before the reviewer step", at: phase.Accepting,
			steps: []string{"implementer", "acceptance:owner", "reviewer"}, want: []string{}},
		{name: "control: accepting after the reviewer step", at: phase.Accepting,
			steps: []string{"implementer", "reviewer", "acceptance:owner"}, want: []string{"reviewer review"}},
		{name: "testing after the reviewer step", at: phase.Testing,
			steps: []string{"implementer", "reviewer", "tester"}, want: []string{"tester landed", "reviewer review"}},
		{name: "control: testing before the reviewer step", at: phase.Testing,
			steps: []string{"implementer", "tester", "reviewer"}, want: []string{"tester landed"}},
		{name: "retro after a reviewer step that follows the acceptance", at: phase.Retro,
			steps: []string{"implementer", "acceptance:owner", "reviewer"}, want: []string{"reviewer review"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := plannedConflictIssue(t, tc.at, tc.steps...)
			confirmsConflict(t, pool, requiresCI)
			asked := len(mergeForwardMessages(t, pool))
			if asked == 0 {
				t.Fatalf("the confirmed conflict in %s asked no merge forward, want one", tc.at)
			}
			landsMergeForward(t, pool)

			if got := toldWho(mergeForwardMessages(t, pool)[asked:]); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the landing in %s under %v queued %v, want %v", tc.at, tc.steps, got, tc.want)
			}
			review := len(tc.want) > 0 && tc.want[len(tc.want)-1] == "reviewer review"
			if mf := recordedPR(t, pool, "LEGION-208").MergeForward; mf == nil || mf.Landed != forwardHead || mf.ReviewAsked != review {
				t.Fatalf("the record's merge forward = %+v, want it landed at %s with the review asked: %t", mf, forwardHead, review)
			}
			if got := issuePhase(t, pool); got != tc.at {
				t.Fatalf("after the merge landed the issue is in %s, want %s", got, tc.at)
			}
		})
	}
}

// The merge is the next step of an issue in accepting only where no step of its task follows the
// acceptance (classify.MergeComesNext). There a confirmed conflict asks for the merge forward
// whatever the checks say (record.ConflictMergeNext). Where a step follows, the conflict is decided
// as it is in testing and reviewing: with every check the base requires green on the head, nothing
// is asked, and the next step works at that head. The merge forward is asked once the issue reaches
// its retro. The controls are the acceptance as the task's last step, and the retro of a task whose
// acceptance a step follows.
func TestAConflictInAcceptingAsksForTheMergeOnlyWhereTheMergeComesNext(t *testing.T) {
	requiresLint := &record.BaseRules{Required: []string{"lint"}}
	for _, tc := range []struct {
		name  string
		at    phase.Phase
		steps []string
		why   record.ConflictWhy
	}{
		{name: "accepting before a reviewer step", at: phase.Accepting,
			steps: []string{"implementer", "acceptance:owner", "reviewer"}},
		{name: "accepting before a tester step", at: phase.Accepting,
			steps: []string{"implementer", "reviewer", "acceptance:owner", "tester"}},
		{name: "control: accepting as the last step", at: phase.Accepting,
			steps: []string{"implementer", "reviewer", "acceptance:owner"}, why: record.ConflictMergeNext},
		{name: "control: the retro of a task whose acceptance a step follows", at: phase.Retro,
			steps: []string{"implementer", "acceptance:owner", "reviewer"}, why: record.ConflictMergeNext},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := plannedConflictIssue(t, tc.at, tc.steps...)
			confirmsConflict(t, pool, requiresLint)
			if tc.why == "" {
				if asked := recordedPR(t, pool, "LEGION-208").MergeForward; asked != nil {
					t.Fatalf("the record's merge forward = %+v, want none asked", asked)
				}
				assertNothingActed(t, pool, tc.at)
				return
			}
			askedOnce(t, pool, tc.at, "retro", tc.why, "")
		})
	}
}

// The reviewer's approval of a head held in a confirmed conflict ends its round and moves the
// issue to the task's next step (reviewRound). The move asks for the merge forward only where the
// merge comes next there (mergeForwardOnEntry): into accepting as the task's last step, and not
// into accepting that a tester step follows, where the tester works at that head first.
func TestAnApprovalOfAConflictingHeadAsksForTheMergeOnlyWhereTheMergeComesNext(t *testing.T) {
	every := &record.BaseRules{Required: []string{"lint"}}
	for _, tc := range []struct {
		name  string
		steps []string
		asks  bool
	}{
		{name: "into accepting before a tester step", steps: []string{"implementer", "reviewer", "acceptance:owner", "tester"}},
		{name: "control: into accepting as the last step", steps: []string{"implementer", "reviewer", "acceptance:owner"}, asks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := plannedConflictIssue(t, phase.Reviewing, tc.steps...)
			confirmsConflict(t, pool, every)
			assertNothingActed(t, pool, phase.Reviewing)
			applyRefusingNothing(t, pool, readsAfter(2*time.Minute), intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 1,
				State: "approved", CommitID: conflictHead, HeadSHA: conflictHead, Author: "a-writer", AuthorCanWrite: true, Body: "ship it",
				SubmittedAt: firstConflictRead.Add(2 * time.Minute)})
			if got := issuePhase(t, pool); got != phase.Accepting {
				t.Fatalf("after the approval the issue is in %s, want accepting: an approval of a conflicting head ends its round", got)
			}
			want := []record.MergeForwardMessage{}
			if tc.asks {
				want = append(want, record.MergeForwardMessage{Role: claim.RoleImplementer, Step: record.MergeForwardAsk, Head: conflictHead,
					Base: "main", Phase: phase.Accepting, PullRequest: 42, Why: record.ConflictMergeNext})
			}
			if got := mergeForwardMessages(t, pool); !reflect.DeepEqual(got, want) {
				t.Fatalf("merge-forward messages %+v, want %+v", got, want)
			}
		})
	}
}
