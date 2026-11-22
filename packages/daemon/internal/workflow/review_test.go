package workflow

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/classify"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The reviewer's verdict decides the round with its completion, and the completion records its
// findings (the handoff record the next role reads): changes returns the issue to implementing,
// counting the round, and approve of a head GitHub reports clean moves it to accepting. The
// verdict records the commit it names as the commit the reviewer read
// (record.PhaseRow.ReviewedHead), over the one an earlier verdict recorded, and changes tells the
// reviewer's next round that commit. A person's request for changes decides the round too,
// whether it comes before the reviewer completes - whose completion is then recorded on its own
// row with the issue where the request left it, rather than refused as a phase it no longer runs -
// or after a completion that named no verdict; it moves no commit.
func TestTheReviewEventDecidesAndTheCompletionRecordsItsFindings(t *testing.T) {
	person := intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 1, State: "changes_requested", CommitID: headOne,
		Author: "a-writer", AuthorCanWrite: true, Body: "rename the widget", SubmittedAt: reviewTestedAt}
	noVerdict := intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Summary: "reviewed",
		HandoffRecord: "review-1", HandoffVersion: 1}
	for _, tc := range []struct {
		name   string
		facts  []intake.Fact
		want   phase.Phase
		rounds int
		// moved is whether the reviewer's verdict moved the commit it read to headOne.
		moved bool
	}{
		{name: "the reviewer's changes", facts: []intake.Fact{reviewAt(1, "changes_requested", headOne)}, want: phase.Implementing, rounds: 1, moved: true},
		{name: "the reviewer's approval of a clean head", facts: []intake.Fact{reviewAt(1, "approved", headOne)}, want: phase.Accepting, moved: true},
		{name: "a person's request for changes, then the reviewer's verdict", facts: []intake.Fact{person, reviewAt(1, "approved", headOne)}, want: phase.Implementing, rounds: 1},
		{name: "a completion with no verdict, then a person's request for changes", facts: []intake.Fact{noVerdict, person}, want: phase.Implementing, rounds: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			ctx := context.Background()
			seedReview(t, pool, "green")
			anEarlierReviewRead(t, pool)
			engine := testEngine(config.DesignGateRootIssues, nil)
			for i, fact := range tc.facts {
				result, err := intake.ApplyFact(ctx, pool, "test", fmt.Sprintf("fact-%d", i), fact, engine)
				if err != nil || result.Refusal != nil {
					t.Fatalf("fact %d (%T) = %+v, %v; want it recorded", i, fact, result.Refusal, err)
				}
			}
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("the issue is in %s, want %s", got, tc.want)
			}
			var handoff string
			if err := pool.QueryRow(ctx, "select handoff_record from phases where issue = $1 and role = $2",
				"LEGION-208", "reviewer").Scan(&handoff); err != nil {
				t.Fatal(err)
			}
			if handoff != "review-1" {
				t.Fatalf("the reviewer's handoff record = %q, want its completion's review-1", handoff)
			}
			if got := implementerRounds(t, pool); got != tc.rounds {
				t.Fatalf("review rounds = %d, want %d: only a request for changes ends a round", got, tc.rounds)
			}
			if !tc.moved {
				if got := reviewerRow(t, pool).ReviewedHead; got != headEarlier {
					t.Fatalf("the reviewer's row reads the reviewed commit %q, want %s: no verdict of the round that stood moved it", got, headEarlier)
				}
				return
			}
			state := "approved"
			if tc.want == phase.Implementing {
				state = "changes_requested"
			}
			decidedAtHeadOne(t, pool, engine, state)
		})
	}
}

// An approval moves the issue on only once GitHub reports the head one the issue may advance on
// (record.MergeState.Mergeable). While the record holds no merge state for the head
// (record.PullRequest.StateReadAt says what that means), or GitHub answers `unknown` because it is
// still computing the merge, the round waits
// where it stands and nobody is told: the answer is about GitHub, not about the head, and the
// read that finds the state clean is what moves the issue to accepting.
func TestAnApprovalWaitsWhileGitHubHasNotAnsweredForTheHead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state record.MergeState
	}{
		{"no merge state on record", record.MergeStateUnread},
		{"GitHub still computing it", record.MergeStateUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedMergeStateRound(t, pool, tc.state)
			apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

			apply("approve", reviewAt(1, "approved", headOne))
			if got := issuePhase(t, pool); got != phase.Reviewing {
				t.Fatalf("after the reviewer's approval the issue is in %s, want reviewing: GitHub has not answered for the head", got)
			}
			if told := reviewStuckNotices(t, pool); len(told) != 0 {
				t.Fatalf("the architect was told the round was stuck %+v, want nothing: an approval waiting for GitHub's answer is not stuck", told)
			}

			apply("merge-state", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
				Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
			if got := issuePhase(t, pool); got != phase.Accepting {
				t.Fatalf("after GitHub reported the head clean the issue is in %s, want accepting", got)
			}
			if told := reviewStuckNotices(t, pool); len(told) != 0 {
				t.Fatalf("the architect was told the round was stuck %+v, want nothing", told)
			}
		})
	}
}

// GitHub answers `mergeable: true` for a head whose base requires it be up to date and that is
// behind it just as it does for one it would merge now, so the merge state is the only answer that
// tells the two apart. An approval standing on a head GitHub answers for with a state it would not
// merge from and the round does not end on, every check reported on that head having concluded,
// moves nothing. So the architect that owns the issue is told, in the round's review-stuck notice
// naming the pull request, the head and the state GitHub gave - once for that head, however many
// facts ask the round again: the reviewer's completion after it, and every further read of the
// same head that answers the same, tell nothing more. The notice names no role topic: the decision
// is not the reviewer's to give.
func TestAnApprovalOnAHeadGitHubWillNotMergeTellsTheArchitectOnce(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateBehind)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

	apply("approve", reviewAt(1, "approved", headOne))
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the approval of a behind head the issue is in %s, want reviewing: GitHub would not merge the head", got)
	}
	told := reviewStuckNotices(t, pool)
	if len(told) != 1 || !strings.Contains(told[0].Reason, "approved head "+headOne+" on pull request #42") ||
		!strings.Contains(told[0].Reason, "GitHub reports that head behind") || told[0].Topic != "" {
		t.Fatalf("review-stuck notices %+v, want one naming the pull request, the head and behind, with no role topic", told)
	}

	// The reviewer's approval was its completion; every further read of the same head that answers
	// the same tells nothing more.
	apply("second-read", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBehind})
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the second read the issue is in %s, want reviewing: the head is still behind", got)
	}
	if again := reviewStuckNotices(t, pool); len(again) != 1 {
		t.Fatalf("the architect was told %d times (%+v), want once for the head", len(again), again)
	}

	apply("merge-state", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
	if got := issuePhase(t, pool); got != phase.Accepting {
		t.Fatalf("after GitHub reported the head clean the issue is in %s, want accepting", got)
	}
	if again := reviewStuckNotices(t, pool); len(again) != 1 {
		t.Fatalf("the architect was told %d times (%+v), want the one notice of the behind head", len(again), again)
	}
}

// A merge state is GitHub's answer about the head as GitHub holds it now, and it changes under a
// head nobody pushed to: a draft reads `draft`, and the same head reads `clean` once it is marked
// ready for review. So the stuck round an approval on a draft head leaves is there to stop a second
// notice, never to stop the move: the read that answers clean for that very head ends the round and
// moves the issue to accepting, as it would have had GitHub answered clean the first time. The
// architect and the reviewer are told once, when the head was a draft, and the clean read tells
// neither of them again - it is the answer they were waiting for, not news of a round that goes
// nowhere.
func TestAnApprovalOnADraftHeadGitHubLaterReportsCleanMovesOnAndTellsOnce(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateUnread)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

	apply("approve", reviewAt(1, "approved", headOne))
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the approval the issue is in %s, want reviewing: GitHub has not answered for the head", got)
	}

	apply("draft", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateDraft})
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after GitHub reported the head a draft the issue is in %s, want reviewing: GitHub would not merge the head", got)
	}
	told := reviewStuckNotices(t, pool)
	if len(told) != 1 || !strings.Contains(told[0].Reason, "approved head "+headOne+" on pull request #42") ||
		!strings.Contains(told[0].Reason, "GitHub reports that head draft") {
		t.Fatalf("review-stuck notices %+v, want one naming the pull request, the head and draft", told)
	}
	stuck := []record.RoleMessage{{Role: treeRoleToken(t, "LEGION-208", claim.RoleReviewer),
		Body: "The round you approved is stuck: " + told[0].Reason + "."}}
	assertRoleMessages(t, pool, "LEGION-208", stuck)

	apply("clean", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
	if got := issuePhase(t, pool); got != phase.Accepting {
		t.Fatalf("after GitHub reported the same head clean the issue is in %s, want accepting: the round recorded stuck stops a second notice, not the approval", got)
	}
	if again := reviewStuckNotices(t, pool); len(again) != 1 {
		t.Fatalf("the architect was told %d times (%+v), want the one notice of the draft head: the clean read is the answer it waited for", len(again), again)
	}
	assertRoleMessages(t, pool, "LEGION-208", stuck)
}

// The notice of a head GitHub will not merge from is read by a person deciding what to push next,
// so it names GitHub's state and every check GitHub reported on that head with its conclusion, and
// what ends the round at that head: a state GitHub would merge from, or `blocked` with every check
// run of the head concluded. The reviewer is told as well as the architect, since it approved that
// head and is the role that reads the head's checks on GitHub; both are told once for the head and
// the state.
func TestAnApprovalOnABehindHeadNamesTheStateAndEveryCheckGitHubReported(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateBehind)
	// GitHub settled the head reporting the one check run it made there.
	behind := record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne, Failing: []string{}, Cancelled: []string{},
		Mergeability: record.MergeabilityMergeable, MergeState: record.MergeStateBehind,
		CheckedHead: headOne, CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 1, Head: headOne, Conclusion: record.RunSucceeded}}}
	seedPR(t, pool, behind)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

	apply("approve", reviewAt(1, "approved", headOne))

	want := stuckMergeStateReason(behind)
	told := reviewStuckNotices(t, pool)
	if len(told) != 1 || told[0].Reason != want {
		t.Fatalf("review-stuck notices %+v, want one reading %q", told, want)
	}
	for _, phrase := range []string{"GitHub reports that head behind", `"unit tests" succeeded`,
		"until GitHub reports that head clean, unstable or has_hooks, or blocked with every check run it reported there concluded"} {
		if !strings.Contains(told[0].Reason, phrase) {
			t.Fatalf("the notice reads %q, want it to name %q", told[0].Reason, phrase)
		}
	}
	assertRoleMessages(t, pool, "LEGION-208", []record.RoleMessage{
		{Role: treeRoleToken(t, "LEGION-208", claim.RoleReviewer), Body: "The round you approved is stuck: " + want + "."},
	})

	// A second read of the same head answering the same state tells neither of them again.
	apply("second-read", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBehind})
	if again := reviewStuckNotices(t, pool); len(again) != 1 {
		t.Fatalf("the architect was told %d times (%+v), want once for the head and state", len(again), again)
	}
	assertRoleMessages(t, pool, "LEGION-208", []record.RoleMessage{
		{Role: treeRoleToken(t, "LEGION-208", claim.RoleReviewer), Body: "The round you approved is stuck: " + want + "."},
	})

	// GitHub answering the same head `draft` is a different finding, and is told once in its turn.
	apply("draft", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateDraft})
	draft := behind
	draft.MergeState = record.MergeStateDraft
	if again := reviewStuckNotices(t, pool); len(again) != 2 || again[1].Reason != stuckMergeStateReason(draft) {
		t.Fatalf("review-stuck notices %+v, want a second naming draft", again)
	}
}

// The reviewer's own push runs CI, so the head an approval stands on has checks running for as long
// as they take, and whatever GitHub answers for it meanwhile - `blocked` for a required check still
// running, or `behind` - is no finding about the round: nothing is told, and nothing moves. What
// says the head's own checks are over is GitHub having reported a check run on that head itself,
// every one of them concluded (classify.ChecksConcludedOnHead). Once such a run is recorded and the
// read it asks for finds the head still one GitHub would not merge and the round does not end on,
// the architect and the reviewer are told, once. The telling is marked on the round's decision
// rather than recomputed from the record, so GitHub answering `unknown` while it recomputes and
// then `behind` again for the same head is one finding, told once.
func TestAnApprovalWhoseHeadHasACheckRunningTellsNobodyUntilItConcludes(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateBehind)
	// The head the reviewer approved is the one its own push left: GitHub has reported no check
	// run on it, which is what a head run of CI still going leaves behind.
	running := record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne, Failing: []string{}, Cancelled: []string{},
		Mergeability: record.MergeabilityMergeable, MergeState: record.MergeStateBehind}
	seedPR(t, pool, running)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

	apply("approve", reviewAt(1, "approved", headOne))
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the approval the issue is in %s, want reviewing: GitHub would not merge a head it is still checking", got)
	}
	if told := reviewStuckNotices(t, pool); len(told) != 0 {
		t.Fatalf("the architect was told the round was stuck %+v, want nothing: a check is still running on the head", told)
	}
	assertRoleMessages(t, pool, "LEGION-208", nil)

	// Another read while the check runs, `blocked` for it, says the same thing, and tells nobody
	// either.
	apply("blocked-while-running", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked})
	if told := reviewStuckNotices(t, pool); len(told) != 0 || issuePhase(t, pool) != phase.Reviewing {
		t.Fatalf("the architect was told the round was stuck %+v in %s, want nothing in reviewing: the head's checks have not concluded",
			told, issuePhase(t, pool))
	}

	// The last check concludes. The settlement moves nothing and tells nobody of the round; the
	// read it asks for is what finds the head behind.
	apply("checks", intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: headOne, Generation: 1, Snapshot: "settled",
		CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 1}}, Failing: []string{}, Cancelled: []string{}})
	if told := reviewStuckNotices(t, pool); len(told) != 0 {
		t.Fatalf("the settlement told the architect %+v, want nothing: a CI result decides no round", told)
	}
	apply("behind", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBehind})
	told := reviewStuckNotices(t, pool)
	if len(told) != 1 || !strings.Contains(told[0].Reason, "GitHub reports that head behind") ||
		!strings.Contains(told[0].Reason, `"unit tests" succeeded`) {
		t.Fatalf("review-stuck notices %+v, want one naming behind and the check that concluded", told)
	}
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the behind read the issue is in %s, want reviewing", got)
	}
	if stuck := stuckRoleMessages(t, pool); len(stuck) != 1 {
		t.Fatalf("the reviewer was told %d times %+v, want once", len(stuck), stuck)
	}

	// GitHub recomputes the state, answering `unknown` and then `behind` again: the same finding,
	// which the mark on the decision has already told.
	apply("unknown", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateUnknown})
	apply("behind-again", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBehind})
	if again := reviewStuckNotices(t, pool); len(again) != 1 {
		t.Fatalf("the architect was told %d times (%+v), want once for the head and state", len(again), again)
	}
	if stuck := stuckRoleMessages(t, pool); len(stuck) != 1 {
		t.Fatalf("the reviewer was told %d times %+v, want once for the head and state", len(stuck), stuck)
	}
}

// toldStuckMark is the head and GitHub's merge state the reviewer's decision records having been
// reported stuck on (record.ReviewDecision.ToldStuckHead), each "" while no notice has gone out.
func toldStuckMark(t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	var head, state string
	if err := pool.QueryRow(t.Context(), `select coalesce(decision ->> 'toldStuckHead', ''), coalesce(decision ->> 'toldStuckState', '')
		from phases where issue = $1 and role = $2`, "LEGION-208", string(claim.RoleReviewer)).Scan(&head, &state); err != nil {
		t.Fatalf("read the reviewer's told-stuck mark: %v", err)
	}
	return head, state
}

// stuckRoleMessages is every message the outbox holds for a role saying the round it approved is
// stuck, in order: the half of the review-stuck notice the reviewer gets.
func stuckRoleMessages(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select payload->>'body' from outbox where kind = 'role_message'
		and payload->>'body' like 'The round you approved is stuck:%' order by id`)
	if err != nil {
		t.Fatalf("read the reviewer's stuck messages: %v", err)
	}
	defer rows.Close()
	bodies := []string{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatalf("scan a stuck message: %v", err)
		}
		bodies = append(bodies, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the stuck messages: %v", err)
	}
	return bodies
}

// A synchronize can arrive before the push webhook of its own move, and GitHub's verdict for the
// head the branch has left says nothing about the one it arrives at while no recorded push says
// what the move changed. An approval of the new head stands at once - no push can change what an
// approval of the head itself approves - so a verdict kept across that move would move the issue
// to accepting on the old head's `clean` (dispatch://INFRA-714 R2). The verdict goes with the
// move instead: the round waits for GitHub's answer about the head it now stands at, and the read
// that lands is what moves it.
func TestASynchronizeBeforeItsPushLeavesNoVerdictToMoveAnApprovalOn(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateClean)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))

	apply("sync", intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208",
		HeadSHA: headTwo, Before: headOne, Sender: testAppLogin})
	if got := recordedPR(t, pool, "LEGION-208"); got.HeadSHA != headTwo || got.MergeState != record.MergeStateUnread {
		t.Fatalf("after the head arrived before its push the record stands at %s with merge state %q, want head-2 with nothing read",
			got.HeadSHA, got.MergeState)
	}

	apply("approve", reviewAt(1, "approved",
		headTwo))
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the approval of the new head the issue is in %s, want reviewing: GitHub's clean was about the head the branch has left", got)
	}
	if told := reviewStuckNotices(t, pool); len(told) != 0 {
		t.Fatalf("the architect was told the round was stuck %+v, want nothing: GitHub has answered nothing for this head", told)
	}

	apply("read", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headTwo,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
	if got := issuePhase(t, pool); got != phase.Accepting {
		t.Fatalf("after GitHub answered for the head the branch stands at the issue is in %s, want accepting", got)
	}
}

// The read of GitHub's merge state is asked for once, after the fact's handler (Apply), for each
// fact that can change the answer: the pull request's own events, a push to it, a review, a CI
// settlement, and a push to the base it sits on. Keyed on the fact rather than called from the
// handlers, a handler's own early return cannot drop it (dispatch://INFRA-714 R3). GitHub's own
// answer asks for nothing: a read that asked for another would read the same head for as long as
// GitHub kept answering the same way. A push to the branch moves no head of the record - its
// synchronize does - so it asks only for a round left waiting on GitHub.
func TestEveryFactThatCanChangeGitHubsAnswerAsksForAMergeStateRead(t *testing.T) {
	push := intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo}
	for _, tc := range []struct {
		name string
		fact intake.Fact
		// approved is the head the reviewer's standing approval names, headOne for every fact that
		// leaves the record there: an approval of a head the fact moves past stands on nothing,
		// and such a round waits on the push that says what the move changed, not on GitHub.
		approved string
		want     bool
	}{
		// A push whose new head has not arrived leaves the round waiting on that head, not on
		// GitHub: the approval of the head the push replaced stands for nothing until the move is
		// known, so nothing is read. The push moves no head of the record either: the synchronize
		// that carries the head moves it, and that is the fact a head move of an issue in
		// implementing through merging is read for (classify.MergeStateFactHeadMove,
		// dispatch://INFRA-4374 R0) - the head arriving, below.
		{name: "a push to the branch", fact: push, want: false},
		{name: "the head arriving", fact: intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208",
			HeadSHA: headTwo, Before: headOne, Sender: testAppLogin}, approved: headTwo, want: true},
		{name: "a synchronize that moves nothing", fact: intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42,
			Branch: "legion/LEGION-208", HeadSHA: headOne, Before: headEarlier, Sender: testAppLogin}, want: true},
		{name: "a CI settlement", fact: intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: headOne, Generation: 1,
			Snapshot: "settled", CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 1}}, Failing: []string{}, Cancelled: []string{}}, want: true},
		{name: "a review", fact: intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, State: "commented", CommitID: headOne,
			HeadSHA: headOne, Author: testAppLogin, Body: "a note"}, want: true},
		// The reviewer's verdict decides the round with its completion, so the completion is the
		// fact that leaves an approval waiting on GitHub's answer for the head.
		{name: "the reviewer's verdict", fact: intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim,
			Summary: "approved", Verdict: "approve", Commit: headOne, HandoffRecord: "review-9", HandoffVersion: 9}, want: true},
		{name: "a push to the base", fact: intake.BasePush{Repo: "sjawhar/legion", Branch: "main", After: "base-2"}, want: true},
		{name: "a reopen", fact: intake.PullRequestOpened{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", Base: "main",
			HeadSHA: headOne, Reopened: true, UpdatedAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)}, want: true},
		{name: "GitHub's own answer", fact: intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
			Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedMergeStateRound(t, pool, record.MergeStateBlocked)
			// An approval standing on a head GitHub has not answered for since its checks began:
			// the one round that waits on a read (reviewRound).
			waiting := record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
				Number: 42, Branch: "legion/LEGION-208", Base: "main", HeadSHA: headOne, Failing: []string{}, Cancelled: []string{},
				Mergeability: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked}
			seedPR(t, pool, waiting)
			approved := tc.approved
			if approved == "" {
				approved = headOne
			}
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim,
				Decision: &record.ReviewDecision{State: "approved", Head: approved}})
			var asked []string
			engine := New(record.NewStore(), Config{Project: "LEGION", Steps: config.DesignGateRootIssues.Steps(), ReviewRoundCap: 3,
				Linger: time.Hour, AppLogin: testAppLogin, Repositories: testRepositories,
				Clock:          func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) },
				ReadMergeState: func(issue string) { asked = append(asked, issue) }}, nil)

			applyFacts(t, pool, engine)("fact", tc.fact)
			if got := len(asked) > 0; got != tc.want {
				t.Fatalf("%s asked for %d reads %v, want asked=%v", tc.name, len(asked), asked, tc.want)
			}
		})
	}
}

// seedMergeStateRound seeds LEGION-208 in reviewing, its pull request at head with GitHub's
// mergeability read mergeable and its merge state state, and its reviewer's round not yet
// decided. CI settled on the head, reporting the one check run GitHub made there: a round
// reports a head GitHub will not merge from only once GitHub has reported a check run on the
// head itself and every one of them has concluded (reviewRound,
// classify.ChecksConcludedOnHead), which is what that run says.
func seedMergeStateRound(t *testing.T, pool *pgxpool.Pool, state record.MergeState) {
	t.Helper()
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Reviewing, Generation: 1, Status: "needs_review", Rank: "U"})
	pr := record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne, Failing: []string{},
		CheckedHead: headOne, CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 1, Head: headOne, Conclusion: record.RunSucceeded}},
		Mergeability: record.MergeabilityMergeable, MergeState: state}
	if state == record.MergeStateUnread {
		// A head GitHub has never been read for has neither answer recorded, where seedPR's
		// default clean is for the tests that are about something else.
		pr.Mergeability = ""
	}
	seedPR(t, pool, pr)
	if state == record.MergeStateUnread {
		if _, err := pool.Exec(t.Context(), "update pull_requests set mergeability = '', merge_state = '' where issue = $1", pr.Issue); err != nil {
			t.Fatalf("clear GitHub's answer on the seeded pull request: %v", err)
		}
	}
	seedRoundOneRows(t, pool)
}

// A read of GitHub's merge state is one round trip outside the record's transaction, so a push can
// land between GitHub's answer and the apply. A read that answers for a head the record has moved
// past is dropped whole: it records neither verdict, not the time it was read, and it decides
// nothing - not even the first read of a conflict, which would wait for a second read
// (record.PullRequest.ConflictRead), since the head that conflicts is not the head the branch now
// carries. A stamp written there would say the head the record stands at had been read when it
// was the head before it that was.
func TestAMergeStateReadForAHeadTheRecordHasMovedPastIsDropped(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateClean)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("push", intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo})
	apply("sync", intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: headTwo})

	apply("stale-read", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "other", HeadSHA: headOne,
		Mergeable: record.MergeabilityConflicting, MergeState: record.MergeStateDirty,
		ReadAt: time.Date(2026, 11, 12, 7, 5, 3, 0, time.UTC)})
	got := recordedPR(t, pool, "LEGION-208")
	if got.Mergeability != "" || got.MergeState != record.MergeStateUnread || got.Base != "" {
		t.Fatalf("after the stale read the record holds %q/%q on base %q, want nothing read: the move to head-2 took GitHub's answer with it and the read was of the head it left",
			got.Mergeability, got.MergeState, got.Base)
	}
	if got.ConflictRead != nil || got.ConflictingHead != "" {
		t.Fatalf("after the stale read the record holds first read %+v and %q conflicting, want neither: the conflict was of a head the branch has left", got.ConflictRead, got.ConflictingHead)
	}
	if !got.StateReadAt.IsZero() {
		t.Fatalf("after the stale read the record was last read at %v, want no read time at all: the read was of the head head-2 replaced", got.StateReadAt)
	}
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the stale read the issue is in %s, want reviewing: the conflict was of a head the branch has left", got)
	}
}

// The fence keeps the newest run of each check name with the head GitHub ran it on, so after a
// push it still holds the runs of the head the push left until the new head's own CI settles.
// Those runs say nothing about the new head: a round on the new head waits until a run of that
// head itself has been reported and concluded (classify.ChecksConcludedOnHead, over
// classify.HeadRuns), and the architect is told nothing while GitHub holds the new head blocked
// for checks still running there. A head test that counted the left head's runs would tell the
// architect the new head is stuck while its CI runs, naming the old head's checks, and would
// count them into the runs a round is judged by (classify.HeadRuns): this test holds that the
// round on the new head is not reported stuck and that no notice names the left head's runs.
func TestARoundOnANewHeadIsNotJudgedByTheRunsOfTheHeadItLeft(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateBlocked)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("push", intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo})
	apply("sync", intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: headTwo})
	if got := recordedPR(t, pool, "LEGION-208"); len(got.CheckRuns) == 0 || got.CheckRuns[0].Head != headOne {
		t.Fatalf("after the push the fence holds %+v, want the left head's run still there: this test proves nothing without it", got.CheckRuns)
	}
	apply("approve", reviewAt(1, "approved",
		headTwo))
	apply("blocked", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headTwo,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked})
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the approval of %s the issue is in %s, want reviewing: GitHub holds the head blocked", headTwo, got)
	}
	if told := reviewStuckNotices(t, pool); len(told) != 0 {
		t.Fatalf("the architect was told the round was stuck %+v, want nothing: no run of %s itself has been reported, and the only run in the fence is %s's",
			told, headTwo, headOne)
	}
	if got := classify.HeadRuns(recordedPR(t, pool, "LEGION-208")); len(got) != 0 {
		t.Fatalf("classify.HeadRuns = %+v, want none: every run in the fence ran on %s, the head the push left", got, headOne)
	}
}

// What record.PullRequest.StateReadAt records is the read, not the change: a head read a minute
// ago and found blocked must read differently from a head nothing has read for a day.
// So a read that answers exactly what is recorded - the same mergeability, the same merge state,
// the same base - writes the row for the read time alone, and moves nothing else. A fact that
// carries no read time was minted by something that read no GitHub, which is what every test that
// states a verdict rather than reading one does: it writes no stamp and erases none, and the
// recorded one stands as the last real read left it.
func TestAConfirmingReadStampsWhenItWasMadeAndAFactWithNoReadTimeStampsNothing(t *testing.T) {
	recordedAt := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	readAt := time.Date(2026, 11, 12, 7, 5, 3, 0, time.UTC)
	for _, tc := range []struct {
		name string
		read time.Time
		want time.Time
	}{
		{"a read that confirms the recorded verdict, merge state and base", readAt, readAt},
		{"a fact nothing read GitHub for", time.Time{}, recordedAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedMergeStateRound(t, pool, record.MergeStateBlocked)
			seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
				Number: 42, Branch: "legion/LEGION-208", Base: "main", HeadSHA: headOne, Failing: []string{}, Cancelled: []string{},
				Mergeability: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked, StateReadAt: recordedAt,
				CheckedHead: headOne, CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 1, Head: headOne, Conclusion: record.RunSucceeded}}})
			before := recordedPR(t, pool, "LEGION-208")

			applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil),
				intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
					Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked, ReadAt: tc.read})

			got := recordedPR(t, pool, "LEGION-208")
			if !got.StateReadAt.Equal(tc.want) {
				t.Fatalf("after the read the record was last read at %v, want %v", got.StateReadAt, tc.want)
			}
			moved, kept := got, before
			moved.StateReadAt, kept.StateReadAt = time.Time{}, time.Time{}
			if !reflect.DeepEqual(moved, kept) {
				t.Fatalf("the read left the pull request %+v, want %+v: the read time is the only thing a confirming read moves", moved, kept)
			}
		})
	}
}

// The reviewer's approval of the head ends the round whatever CI says at that head: red, green, or
// no verdict yet. The round reads no CI verdict - the reviewer reads the code commit's checks on
// GitHub before it approves, and a red at the head is the daemon's separate rule, acted on by the
// CI fact that makes it stand - so the issue moves to accepting on the approval, with no
// checks-red told and no review-stuck notice; and a completion that named no verdict before it
// leaves the round stuck, told once, until the approval ends it.
func TestAnApprovalEndsTheRoundWhateverCISays(t *testing.T) {
	for _, verdict := range []string{"red", "green", "undecided"} {
		for _, completeFirst := range []bool{false, true} {
			name := verdict + ", the approval alone"
			if completeFirst {
				name = verdict + ", a completion with no verdict, then the approval"
			}
			t.Run(name, func(t *testing.T) {
				pool := migratedPool(t)
				if verdict == "undecided" {
					seedReviewUndecided(t, pool, headOne)
				} else {
					seedReviewOf(t, pool, headOne, verdict)
				}
				apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
				facts := []intake.Fact{reviewAt(2, "approved", headOne)}
				if completeFirst {
					facts = append([]intake.Fact{intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim,
						Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1}}, facts...)
				}
				for i, fact := range facts {
					if result := apply(fmt.Sprintf("fact-%d", i), fact); result.Refusal != nil {
						t.Fatalf("fact %d (%T) was refused: %+v", i, fact, result.Refusal)
					}
				}
				if got := issuePhase(t, pool); got != phase.Accepting {
					t.Fatalf("the issue is in %s, want accepting: the approval ends the round whatever CI says", got)
				}
				kinds := noticeKinds(t, pool, "LEGION-208")
				if !containsNotice(kinds, "phase-finished") || containsNotice(kinds, "checks-red") {
					t.Fatalf("notices %v, want the reviewer's phase-finished and no checks-red", kinds)
				}
				told, want := reviewStuckNotices(t, pool), 0
				if completeFirst {
					// The completion with no verdict leaves the round undecided until the approval
					// lands; the approval tells nothing more.
					want = 1
				}
				if len(told) != want {
					t.Fatalf("the architect was told the round was stuck %d times (%+v), want %d", len(told), told, want)
				}
			})
		}
	}
}

// seedReviewUndecided is seedReviewOf with a settlement in that reports one check, lint, and
// names none failing: the round reads none of it either way (dispatch://INFRA-714/spec R1).
func seedReviewUndecided(t *testing.T, pool *pgxpool.Pool, head string) {
	t.Helper()
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Reviewing, Generation: 1, Status: "needs_review", Rank: "U"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: head, CheckedHead: head, Failing: []string{},
		CheckRuns: []record.AttemptRun{{Name: "lint", ID: 1}}})
	seedRoundOneRows(t, pool)
}

func seedReview(t *testing.T, pool *pgxpool.Pool, verdict string) {
	t.Helper()
	seedReviewOf(t, pool, headOne, verdict)
}

// seedReviewOf seeds LEGION-208 in reviewing, its pull request's head at head with the settlement
// verdict names on it ("green", "red", or "" for none yet), and its reviewer's round not yet
// completed. The settlement reports the one check ci.
func seedReviewOf(t *testing.T, pool *pgxpool.Pool, head, verdict string) {
	t.Helper()
	seedReviewReporting(t, pool, head, verdict, "ci")
}

// seedReviewReporting is seedReviewOf whose settlement reports the checks named: it ran each of
// them, and in a red one each failed.
func seedReviewReporting(t *testing.T, pool *pgxpool.Pool, head, verdict string, checks ...string) {
	t.Helper()
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Reviewing, Generation: 1, Status: "needs_review", Rank: "U"})
	pr := record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: head}
	if verdict != "" {
		pr.CheckedHead = head
		for i, name := range checks {
			pr.CheckRuns = append(pr.CheckRuns, record.AttemptRun{Name: name, ID: int64(i + 1)})
		}
	}
	if verdict == "red" {
		pr.Failing = append([]string{}, checks...)
	}
	seedPR(t, pool, pr)
	seedRoundOneRows(t, pool)
}

func issuePhase(t *testing.T, pool *pgxpool.Pool) phase.Phase {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(), "select phase from issues where key = $1", "LEGION-208").Scan(&got); err != nil {
		t.Fatal(err)
	}
	return phase.Phase(got)
}

// stepHeads is what a step's word for a head means: the 40 hexadecimal characters the fixture
// seeds that head as.
var stepHeads = map[string]string{"head": headOne, "head-2": headTwo, "head-3": headThree}

// An approval is of the code the commit it names carries, and stands only for that head: any push
// may change code, so a push since the approved head leaves the approval standing for nothing,
// whichever of the approval, the new head and its push is processed first, and a push still on its
// way from the approved head keeps the round open until its head arrives. The reviewer's verdict
// moves the issue as it lands, so an approval that stands ends the round there and a push after it
// finds the issue already in accepting. Only the reviewer's verdict approves: a person's approval
// decides nothing, whether or not the person can write, while a person's request for changes
// refuses the head and ends the round however the reviewer's verdict comes after it.
func TestAnApprovalStandsOnlyForTheHeadItNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []string
		want  phase.Phase
		// unsettled seeds the approved head with its checks still running, rather than green.
		unsettled bool
		// quiet is a round the architect is told nothing of: no review-stuck notice.
		quiet bool
		// told is what the round's one review-stuck notice names, when it has one.
		told string
	}{
		{name: "a new head whose push never arrives", steps: []string{"sync", "approve head", "green"}, want: phase.Reviewing},
		{name: "a push, then the approval of the head before it", steps: []string{"sync", "push", "green", "approve head"}, want: phase.Reviewing},
		{name: "the approval of the head, then a push: the round had already ended", steps: []string{"approve head", "sync", "push", "green"}, want: phase.Accepting},
		{name: "a comment between a push and its new head", steps: []string{"push", "approve head", "comment", "sync", "green"}, want: phase.Reviewing},
		{name: "a push whose new head has not arrived", steps: []string{"push", "approve head"}, want: phase.Reviewing},
		{name: "the round ends on its approval of the head before a push arrives", steps: []string{"approve head", "push", "green head"}, want: phase.Accepting, unsettled: true},
		{name: "a push that does not say which head it replaced", steps: []string{"push unplaced", "approve head"}, want: phase.Reviewing},
		{name: "the reviewer's request for changes", steps: []string{"cr head"}, want: phase.Implementing},
		{name: "a comment, then the reviewer's request for changes", steps: []string{"comment id=12", "cr head"}, want: phase.Implementing},
		{name: "a comment, then the reviewer's approval", steps: []string{"comment id=12", "approve head"}, want: phase.Accepting},
		{name: "a person's request for changes, then the reviewer's approval", steps: []string{"cr head id=11 by=writer", "approve head"}, want: phase.Implementing},
		{name: "the reviewer's approval, then a person's request for changes", steps: []string{"approve head", "cr head id=12 by=writer"}, want: phase.Accepting},
		{name: "an outsider's request for changes, then the reviewer's approval", steps: []string{"cr head id=11 by=outsider", "approve head"}, want: phase.Accepting},
		{name: "an outsider's approval the only review of the round", steps: []string{"approve head id=12 by=outsider", "complete"}, want: phase.Reviewing, told: "with no verdict"},
		{name: "a person's approval the only review of the round", steps: []string{"approve head id=12 by=writer", "complete"}, want: phase.Reviewing, told: "with no verdict"},
		{name: "an approval, a push, then the new head's own pending settlement and its green", steps: []string{"sync", "push", "approve head", "cancelled", "rerun green"}, want: phase.Reviewing, unsettled: true},
		{name: "the checks of a head a push replaced", steps: []string{"sync", "push", "approve head", "green head"}, want: phase.Reviewing, unsettled: true},
		{name: "a late settlement of a head a push replaced leaves the head's own", steps: []string{"sync", "push", "green", "red head", "read head-2", "approve"}, want: phase.Accepting, unsettled: true},
		{name: "recorded before the chain: an approval of the current head", steps: []string{"approve head"}, want: phase.Accepting},
		{name: "recorded before the chain: an approval of an earlier head", steps: []string{"approve older"}, want: phase.Reviewing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			ctx := context.Background()
			verdict := "green"
			if tc.unsettled {
				verdict = ""
			}
			seedReview(t, pool, verdict)
			engine := testEngine(config.DesignGateRootIssues, nil)
			// A head's push replaces the head before it, and a step's word for a head is the
			// 40 hexadecimal characters the fixture seeds it as (stepHeads).
			before := map[string]string{headTwo: headOne, headThree: headTwo}
			verdicts := 0
			for i, step := range tc.steps {
				// A step is its name, then optional words: a head it names (head, head-2, ...), a
				// review id (id=N), a review's author when the step is a person's review rather
				// than the reviewer's verdict (by=outsider, an account with no write access to the
				// repository, or by=writer), and the head a push replaced when the map above does
				// not say (from=H).
				head, id, from, by := headTwo, int64(0), "", ""
				var submitted time.Time
				var name []string
				for _, word := range strings.Fields(step) {
					switch {
					case strings.HasPrefix(word, "head"):
						head = stepHeads[word]
					case strings.HasPrefix(word, "id="):
						id, _ = strconv.ParseInt(strings.TrimPrefix(word, "id="), 10, 64)
					case strings.HasPrefix(word, "at="):
						minutes, _ := strconv.Atoi(strings.TrimPrefix(word, "at="))
						submitted = time.Date(2026, 9, 26, 12, minutes, 0, 0, time.UTC)
					case strings.HasPrefix(word, "from="):
						from = strings.TrimPrefix(word, "from=")
					case strings.HasPrefix(word, "by="):
						by = strings.TrimPrefix(word, "by=")
					default:
						name = append(name, word)
					}
				}
				step := strings.Join(name, " ")
				var fact intake.Fact
				switch step {
				case "approve", "approve older", "cr":
					commit, state := head, "approved"
					if step == "approve older" {
						commit = headEarlier
					}
					if step == "cr" {
						state = "changes_requested"
					}
					// With no author named the step is the reviewer's verdict, a completion of
					// its own each time; a person's review decides only by write access, which
					// intake read before the fact arrived.
					if by == "" {
						verdicts++
						fact = reviewAt(int64(verdicts), state, commit)
						break
					}
					author, canWrite := "a-stranger", false
					if by == "writer" {
						author, canWrite = "a-writer", true
					}
					fact = intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: id, SubmittedAt: submitted, State: state,
						CommitID: commit, Author: author, AuthorCanWrite: canWrite, Body: step}
				case "sync":
					fact = intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: head}
				case "comment":
					fact = intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: id, State: "commented", CommitID: headTwo, Body: "a comment"}
				case "push", "push unplaced":
					replaced := before[head]
					if from != "" {
						replaced = from
					}
					if step == "push unplaced" {
						replaced = ""
					}
					fact = intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: replaced, After: head}
				case "green":
					fact = intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: head,
						CheckRuns: []record.AttemptRun{{Name: "ci", ID: 2}}, Generation: 2, Snapshot: "green-" + head, Failing: []string{}}
				case "red":
					fact = intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: head,
						CheckRuns: []record.AttemptRun{{Name: "ci", ID: 3}}, Generation: 2, Snapshot: "red-" + head, Failing: []string{"ci"}}
				case "cancelled":
					// Every check ended cancelled and none failed: the listener's cancelled group.
					fact = intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: head,
						CheckRuns: []record.AttemptRun{{Name: "ci", ID: 4}}, Generation: 3, Snapshot: "cancelled-" + head, Failing: []string{}, Cancelled: []string{"ci"}}
				case "rerun green":
					// The run concurrency cancelled, run again and passed.
					fact = intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: head,
						CheckRuns: []record.AttemptRun{{Name: "ci", ID: 5}}, Generation: 4, Snapshot: "rerun-green-" + head, Failing: []string{}}
				case "read":
					fact = intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: head,
						Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean}
				case "complete":
					fact = intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim,
						Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1}
				}
				result, err := intake.ApplyFact(ctx, pool, "test", fmt.Sprintf("%d-%s", i, step), fact, engine)
				if err != nil || result.Refusal != nil {
					t.Fatalf("%s = %+v, %v", step, result.Refusal, err)
				}
			}
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("the issue is in %s, want %s", got, tc.want)
			}
			got := reviewStuckNotices(t, pool)
			if tc.quiet && len(got) != 0 {
				t.Fatalf("the architect was told the round was stuck: %+v, want nothing", got)
			}
			if tc.told != "" && (len(got) != 1 || !strings.Contains(got[0].Reason, tc.told)) {
				t.Fatalf("review-stuck notices %+v, want one naming %q", got, tc.told)
			}
		})
	}
}

// A review that carries no decision - a comment - leaves the round's decision and its reason as
// they are: the next implementer is told what the reviewer's request for changes said, not the
// comment posted after it.
func TestACommentLeavesTheRoundsRequestForChanges(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	seedReview(t, pool, "")
	engine := testEngine(config.DesignGateRootIssues, nil)
	for i, fact := range []intake.Fact{
		intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, State: "commented", CommitID: headOne, Author: testAppLogin, Body: "the findings, inline"},
		intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Summary: "rename the widget",
			Verdict: "changes", Commit: headOne, HandoffRecord: "review-1", HandoffVersion: 1},
		intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, State: "commented", CommitID: headOne, Body: "one more thought"},
	} {
		if result, err := intake.ApplyFact(ctx, pool, "test", fmt.Sprintf("step-%d", i), fact, engine); err != nil || result.Refusal != nil {
			t.Fatalf("step %d = %+v, %v", i, result.Refusal, err)
		}
	}
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("the issue is in %s, want implementing", got)
	}
	var task string
	if err := pool.QueryRow(ctx, "select payload->>'task' from outbox where kind = 'supervise' and payload->>'op' = 'start' order by id desc limit 1").Scan(&task); err != nil {
		t.Fatalf("read the implementer's start task: %v", err)
	}
	if !strings.Contains(task, "rename the widget") || strings.Contains(task, "one more thought") || strings.Contains(task, "the findings, inline") {
		t.Fatalf("the implementer's task = %q, want the reviewer's verdict's findings and no comment's", task)
	}
}

// Deciding reviews are ordered by when they were submitted, then by GitHub's review id, and the
// order holds across rounds: a person's request for changes from an earlier round, delivered again
// while a later round is open, is no newer than one already processed and records nothing, so the
// round waits for the reviewer's verdict.
func TestAReviewFromAnEarlierRoundDeliveredAgainRecordsNothing(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	seedReview(t, pool, "green")
	engine := testEngine(config.DesignGateRootIssues, nil)
	requestChanges := intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 10, State: "changes_requested", CommitID: headOne,
		Author: "a-writer", AuthorCanWrite: true, Body: "round 1", SubmittedAt: reviewTestedAt}
	passed, _ := testedFact(t, pool, "LEGION-208", headOne, true)
	for i, fact := range []intake.Fact{
		requestChanges,
		intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim", Summary: "fixed", Commit: "round-2", HandoffRecord: "implement-2", HandoffVersion: 2},
		passed,
		intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim", Summary: "tested", Commit: headOne, HandoffRecord: "test-2", HandoffVersion: 2},
		requestChanges,
		reviewAt(2, "approved", headOne),
	} {
		if result, err := intake.ApplyFact(ctx, pool, "test", fmt.Sprintf("step-%d", i), fact, engine); err != nil || result.Refusal != nil {
			t.Fatalf("step %d = %+v, %v", i, result.Refusal, err)
		}
		switch got := issuePhase(t, pool); {
		case i == 0 && got != phase.Implementing:
			t.Fatalf("after round 1 the issue is in %s, want implementing", got)
		case i == 4 && got != phase.Reviewing:
			t.Fatalf("after round 1's request for changes delivered again the issue is in %s, want reviewing", got)
		}
	}
	if got := issuePhase(t, pool); got != phase.Accepting {
		t.Fatalf("the issue is in %s, want accepting: round 2's approval is the reviewer's verdict", got)
	}
	if got := implementerRounds(t, pool); got != 1 {
		t.Fatalf("review rounds = %d, want 1: the request for changes delivered again counted none", got)
	}
}

// A review that arrives once the round has ended - in accepting, after the reviewer's approval
// moved the issue on - decides nothing and counts no round: the rounds and the pull request's
// blocked notice belong to the round that was open.
func TestAReviewOutsideReviewingRecordsNoRound(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	seedReview(t, pool, "green")
	engine := testEngine(config.DesignGateRootIssues, nil)
	if result, err := intake.ApplyFact(ctx, pool, "test", "approve", reviewAt(1, "approved", headOne), engine); err != nil || result.Refusal != nil {
		t.Fatalf("the reviewer's approval = %+v, %v", result.Refusal, err)
	}
	if got := issuePhase(t, pool); got != phase.Accepting {
		t.Fatalf("the issue is in %s, want accepting", got)
	}
	before := implementerRounds(t, pool)
	if _, err := intake.ApplyFact(ctx, pool, "test", "late", intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 12,
		State: "changes_requested", CommitID: headOne, Author: "a-writer", AuthorCanWrite: true, Body: "too late"}, engine); err != nil {
		t.Fatalf("apply the late review: %v", err)
	}
	if got := issuePhase(t, pool); got != phase.Accepting {
		t.Fatalf("after the late review the issue is in %s, want accepting", got)
	}
	if got := implementerRounds(t, pool); got != before {
		t.Fatalf("the implementer's rounds went from %d to %d on a review outside reviewing", before, got)
	}
}

// A person's request for changes can be processed while the issue is held from reviewing: the
// reviewer's launch budget ran out. That request still decides the round, so once the retry puts
// the issue back in reviewing the round ends on it, and the work goes back to implementing. It
// moves no commit the reviewer read: the reviewer did not read the person's.
func TestAReviewWhileHeldFromReviewingDecidesTheRound(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	seedReview(t, pool, "green")
	anEarlierReviewRead(t, pool)
	engine := testEngine(config.DesignGateRootIssues, nil)
	for i, fact := range []intake.Fact{
		intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleReviewer},
		intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 11, State: "changes_requested", CommitID: headOne, Author: "a-writer", AuthorCanWrite: true,
			Body: "the review", SubmittedAt: reviewTestedAt},
		intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision},
	} {
		if i == 1 {
			if got := issuePhase(t, pool); got != phase.Held {
				t.Fatalf("before the review the issue is in %s, want held", got)
			}
		}
		result, err := intake.ApplyFact(ctx, pool, "test", fmt.Sprintf("held-%d", i), fact, engine)
		if err != nil || result.Refusal != nil {
			t.Fatalf("fact %d (%T) = %+v, %v", i, fact, result.Refusal, err)
		}
	}
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("the issue is in %s, want implementing", got)
	}
	if row := reviewerRow(t, pool); row.Decision == nil || row.Decision.State != "changes_requested" || row.Decision.Head != headOne || row.ReviewedHead != headEarlier {
		t.Fatalf("the reviewer's row holds the decision %+v and the reviewed commit %q, want changes_requested at %s and %s kept", row.Decision, row.ReviewedHead, headOne, headEarlier)
	}
}

// Reviews are ordered across the pull request's whole life: reopening it keeps the newest deciding
// review it has had, so a person's request for changes delivered again after the reopen records
// nothing, and the round that stands waits for the reviewer's verdict. The tester's step passes
// on the check state at the head it tested, which the store holds before its completion.
func TestAReopenedPullRequestKeepsItsNewestReview(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	seedReview(t, pool, "green")
	engine := testEngine(config.DesignGateRootIssues, nil)
	requestChanges := intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 11, State: "changes_requested", CommitID: headOne,
		Author: "a-writer", AuthorCanWrite: true, Body: "rename it", SubmittedAt: reviewTestedAt}
	passed, _ := testedFact(t, pool, "LEGION-208", headOne, true)
	for i, fact := range []intake.Fact{
		requestChanges,
		intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim", Summary: "fixed", Commit: "round-2", HandoffRecord: "implement-2", HandoffVersion: 2},
		passed,
		intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim", Summary: "tested", Commit: headOne, HandoffRecord: "test-2", HandoffVersion: 2},
		intake.PullRequestOpened{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne},
		requestChanges,
	} {
		result, err := intake.ApplyFact(ctx, pool, "test", fmt.Sprintf("reopen-%d", i), fact, engine)
		if err != nil || result.Refusal != nil {
			t.Fatalf("fact %d (%T) = %+v, %v", i, fact, result.Refusal, err)
		}
	}
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("the issue is in %s, want reviewing: the request for changes delivered after the reopen is no newer than the one processed", got)
	}
	if got := implementerRounds(t, pool); got != 1 {
		t.Fatalf("review rounds = %d, want 1", got)
	}
}

// The reviewer can complete before its review arrives and before the issue is held from reviewing:
// the review then lands while held, and the retry that restores reviewing finds both halves of the
// round already in. It ends the round there rather than starting the reviewer again, whose second
// completion of the same commit would be refused as not new and leave the issue in reviewing.
func TestARetryEndsARoundWhoseReviewerCompletedBeforeTheHold(t *testing.T) {
	for _, tc := range []struct {
		state  string
		want   phase.Phase
		rounds int
	}{
		{state: "changes_requested", want: phase.Implementing, rounds: 1},
	} {
		t.Run(tc.state, func(t *testing.T) {
			pool := migratedPool(t)
			ctx := context.Background()
			seedReview(t, pool, "green")
			engine := testEngine(config.DesignGateRootIssues, nil)
			for i, fact := range []intake.Fact{
				intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1},
				intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleReviewer},
				intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 11, State: tc.state, CommitID: headOne, Author: "a-writer", AuthorCanWrite: true, Body: "the review"},
				intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision},
			} {
				result, err := intake.ApplyFact(ctx, pool, "test", fmt.Sprintf("early-%d", i), fact, engine)
				if err != nil || result.Refusal != nil {
					t.Fatalf("fact %d (%T) = %+v, %v", i, fact, result.Refusal, err)
				}
			}
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("the issue is in %s, want %s", got, tc.want)
			}
			var starts int
			if err := pool.QueryRow(ctx, `select count(*) from outbox where kind = 'supervise' and payload->>'op' = 'start'
				and payload->>'role' = 'reviewer' and payload->>'phase' = 'reviewing'`).Scan(&starts); err != nil {
				t.Fatal(err)
			}
			if starts != 0 {
				t.Fatalf("the retry started the reviewer %d times, want none: its round had already ended", starts)
			}
			if rounds := implementerRounds(t, pool); rounds != tc.rounds {
				t.Fatalf("the implementer's rounds = %d, want %d: the round ended once", rounds, tc.rounds)
			}
		})
	}
}

// A round the retry restores to reviewing that its reviewer completed before the hold, and that no
// review decides, is stuck as it was: the restarted reviewer's task says why, so it submits the
// decision rather than reviewing afresh. The architect ordered the retry, so it is told nothing new:
// the one review-stuck notice is the completion's, and a second would read as the reviewer's answer.
func TestARetryOfAStuckRoundTellsTheReviewerWhy(t *testing.T) {
	pool := migratedPool(t)
	seedReviewOf(t, pool, headOne, "green")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	for i, fact := range []intake.Fact{
		intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1},
		intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleReviewer},
		intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision},
	} {
		if result := apply(fmt.Sprintf("stuck-%d", i), fact); result.Refusal != nil {
			t.Fatalf("fact %d (%T) was refused: %+v", i, fact, result.Refusal)
		}
	}
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("the issue is in %s, want reviewing", got)
	}
	told := reviewStuckNotices(t, pool)
	if len(told) != 1 {
		t.Fatalf("the architect was told %d times (%+v), want once: at the completion, not at the retry", len(told), told)
	}
	var task string
	if err := pool.QueryRow(t.Context(), `select payload->>'task' from outbox where kind = 'supervise' and payload->>'op' = 'start'
		and payload->>'role' = 'reviewer' order by id desc limit 1`).Scan(&task); err != nil {
		t.Fatalf("read the reviewer's retry task: %v", err)
	}
	if !strings.Contains(task, told[0].Reason) {
		t.Fatalf("the reviewer's retry task = %q, want it to carry the reason the architect was told, %q", task, told[0].Reason)
	}
}

// The retry restores the phase the hold came from and starts its worker. It re-decides no CI
// result: a settlement that reached the issue while it was held moved nothing, as it would have
// moved nothing had it arrived with the issue in its phase (dispatch://INFRA-714/spec R1), so the
// restored worker reads the head's checks itself. Nothing goes back to implementing and the
// architect hears no checks-red, from whatever phase the hold was taken.
func TestARetryOfAHoldWithASettledHeadRestoresThePhase(t *testing.T) {
	for _, tc := range []struct {
		from   phase.Phase
		status string
	}{
		{from: phase.Testing, status: "testing"},
		{from: phase.Retro, status: "retro"},
		{from: phase.Merging, status: "retro"},
	} {
		t.Run(string(tc.from), func(t *testing.T) {
			pool := migratedPool(t)
			role := phase.RoleFor(tc.from)
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
				Phase: tc.from, Generation: 1, Status: tc.status, Rank: "U"})
			seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
				Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne})
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
			if role != claim.RoleImplementer {
				seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: role, Claim: claim.Token(string(role) + "-claim")})
			}
			apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
			for i, fact := range []intake.Fact{
				intake.ClaimFailed{Issue: "LEGION-208", Role: role},
				intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: headOne, CheckRuns: []record.AttemptRun{{Name: "lint", ID: 1}},
					Generation: 1, Snapshot: "red-head", Failing: []string{"lint"}},
				intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision},
			} {
				if result := apply(fmt.Sprintf("held-red-%d", i), fact); result.Refusal != nil {
					t.Fatalf("fact %d (%T) was refused: %+v", i, fact, result.Refusal)
				}
			}
			if got := issuePhase(t, pool); got != tc.from {
				t.Fatalf("the issue is in %s, want %s restored: a settlement moves nothing", got, tc.from)
			}
			if got := noticeKinds(t, pool, "LEGION-208"); containsNotice(got, "checks-red") {
				t.Fatalf("notices %v, want no checks-red", got)
			}
			// A start is two rows (workflow.StartOrOwe) and only the one carrying the task is the
			// start: the other is the relaunch, which brings the role's session back and asks it
			// nothing.
			starts := func(role claim.Role) int {
				var n int
				if err := pool.QueryRow(t.Context(), `select count(*) from outbox where kind = 'supervise' and payload->>'op' = 'start'
					and payload->>'role' = $1 and coalesce(payload->>'task', '') <> ''`, string(role)).Scan(&n); err != nil {
					t.Fatalf("count the %s's starts: %v", role, err)
				}
				return n
			}
			if starts(role) != 1 {
				t.Fatalf("the retry started the %s %d times, want once: the restored phase is its own", role, starts(role))
			}
			if role != claim.RoleImplementer && starts(claim.RoleImplementer) != 0 {
				t.Fatalf("the retry started the implementer %d times, want none: nothing sent the work back", starts(claim.RoleImplementer))
			}
		})
	}
}

// The reviewer's verdict sent after a completion that named none ends the stuck round as any
// verdict does, and tells nothing beyond the first completion's notice.
func TestALateDeliveredApprovalEndsAStuckRound(t *testing.T) {
	pool := migratedPool(t)
	seedReviewOf(t, pool, headOne, "green")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("complete", intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1})
	apply("approve", reviewAt(2, "approved", headOne))
	if got := issuePhase(t, pool); got != phase.Accepting {
		t.Fatalf("the issue is in %s, want accepting", got)
	}
	if got := reviewStuckNotices(t, pool); len(got) != 1 {
		t.Fatalf("the architect was told %d times (%+v), want once: at the completion", len(got), got)
	}
}

// A round ends only on the reviewer's verdict, or a person's request for changes. A reviewer that
// completes its round with no verdict - it submitted a COMMENT, a review in any other state, or no
// review at all - leaves the issue in reviewing with nothing on its way to end it, so its
// architect is told, naming the head. No GitHub review after the completion is the reviewer's
// answer: neither its COMMENT, nor a reply on a review thread, nor anyone else's comment tells the
// architect again, since none changes how the round is stuck. A review that arrives before the
// reviewer completes tells nobody: the completion is still to come. The reviewer's verdict, sent
// with a completion of its own, still ends the round.
func TestARoundNoReviewDecidesTellsTheArchitect(t *testing.T) {
	late := time.Date(2026, 9, 23, 0, 0, 30, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		before   []string
		after    []intake.PullRequestReview
		decision string
		want     phase.Phase
	}{
		{name: "a COMMENT, then the reviewer completes", before: []string{"commented"}, decision: "approved", want: phase.Accepting},
		{name: "no review at all", decision: "approved", want: phase.Accepting},
		{name: "the reviewer completes, then its COMMENT", after: []intake.PullRequestReview{{State: "commented", Author: testAppLogin, Body: "one more thought", SubmittedAt: late}},
			decision: "changes_requested", want: phase.Implementing},
		{name: "the reviewer completes, then its reply on a review thread", after: []intake.PullRequestReview{{State: "commented", Author: testAppLogin, SubmittedAt: late}},
			decision: "approved", want: phase.Accepting},
		{name: "the reviewer completes, then another account's COMMENT", after: []intake.PullRequestReview{{State: "commented", Author: "a-human", Body: "a thought", SubmittedAt: late}},
			decision: "approved", want: phase.Accepting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedReviewOf(t, pool, headOne, "green")
			apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
			review := func(id string, review intake.PullRequestReview) {
				t.Helper()
				review.Repo, review.Number, review.CommitID = "sjawhar/legion", 42, headOne
				apply(id, review)
			}
			for i, state := range tc.before {
				review(fmt.Sprintf("before-%d", i), intake.PullRequestReview{State: state, Author: testAppLogin, Body: "the " + state + " review"})
				if got := reviewStuckNotices(t, pool); len(got) != 0 {
					t.Fatalf("a %s review before the reviewer completed told the architect %+v, want nothing", state, got)
				}
			}
			if result := apply("complete", intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer,
				Claim: reviewClaim, Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1}); result.Refusal != nil {
				t.Fatalf("the reviewer's completion was refused: %+v", result.Refusal)
			}
			for i, after := range tc.after {
				review(fmt.Sprintf("after-%d", i), after)
			}
			if got := issuePhase(t, pool); got != phase.Reviewing {
				t.Fatalf("after the reviews that followed the completion the issue is in %s, want reviewing", got)
			}
			told := reviewStuckNotices(t, pool)
			if len(told) != 1 {
				t.Fatalf("the architect was told %d times (%+v), want once", len(told), told)
			}
			if notice := told[0]; notice.Role != claim.RoleReviewer || notice.Phase != phase.Reviewing || !strings.Contains(notice.Reason, headOne) ||
				notice.Topic != "notifications.role.legion-legion-legion-208-reviewer" || notice.Summary != byCompletion {
				t.Fatalf("notice %+v, want the reviewer's completion's notice of its reviewing round, naming the head, at the reviewer's role topic", notice)
			}
			apply("decides", reviewAt(2, tc.decision, headOne))
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("after the %s verdict the issue is in %s, want %s", tc.decision, got, tc.want)
			}
			if got := reviewStuckNotices(t, pool); len(got) != 1 {
				t.Fatalf("the deciding verdict told the architect again: %+v", got)
			}
		})
	}
}

// On a public repository any GitHub account can review a pull request, and every role acts as the
// one App, so a GitHub review decides a round only when it is a request for changes from an
// account of type User with write access to the repository, which intake reads from GitHub before
// the fact arrives (intake.PullRequestReview.AuthorCanWrite). Every other review decides nothing:
// the App's, whatever its state; a person's approval, with write access or without it; and a
// request for changes from an account without write access, or another App. A round the reviewer
// completed undecided stays stuck, the architect is told nothing more, and the reviewer's verdict
// still ends the round.
func TestOnlyAWritersRequestForChangesDecidesARound(t *testing.T) {
	submitted := time.Date(2026, 9, 23, 0, 1, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, author, state string
		canWrite            bool
		want                phase.Phase
	}{
		{name: "the App's approval", author: testAppLogin, state: "approved", canWrite: true, want: phase.Reviewing},
		{name: "the App's request for changes", author: testAppLogin, state: "changes_requested", canWrite: true, want: phase.Reviewing},
		{name: "an approval by an account with write access", author: "a-writer", canWrite: true, state: "approved", want: phase.Reviewing},
		{name: "a request for changes by an account with write access", author: "a-writer", canWrite: true, state: "changes_requested", want: phase.Implementing},
		{name: "an approval by an account without write access", author: "a-reader", state: "approved", want: phase.Reviewing},
		{name: "a request for changes by an account without write access", author: "a-reader", state: "changes_requested", want: phase.Reviewing},
		{name: "another App's request for changes", author: "another-app[bot]", state: "changes_requested", want: phase.Reviewing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedReviewOf(t, pool, headOne, "green")
			apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
			apply("complete", intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1})
			if got := reviewStuckNotices(t, pool); len(got) != 1 {
				t.Fatalf("the reviewer's completion told the architect %d times (%+v), want once", len(got), got)
			}
			apply("review", intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 8, SubmittedAt: submitted.Add(time.Minute), State: tc.state,
				CommitID: headOne, Author: tc.author, AuthorCanWrite: tc.canWrite, Body: "the review"})
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("after the review the issue is in %s, want %s", got, tc.want)
			}
			if tc.want != phase.Reviewing {
				return
			}
			if got := reviewStuckNotices(t, pool); len(got) != 1 {
				t.Fatalf("a review that decides nothing told the architect again: %+v", got)
			}
			apply("the reviewer's verdict", reviewAt(2, "approved", headOne))
			if got := issuePhase(t, pool); got != phase.Accepting {
				t.Fatalf("after the reviewer's approval the issue is in %s, want accepting", got)
			}
		})
	}
}

// An approval ends the round only while it approves the head's code. One that does not - the pull
// request has moved on since the commit the verdict names, by a push that may have changed code -
// leaves the round stuck, so the architect is told, once, when the round becomes stuck, by
// whichever fact made it so: the verdict's completion, or the arrival of the new head a push was
// still on its way from. A push recorded late that leaves the round stuck the same way tells
// nothing more. The reviewer's verdict of the head, approve or changes, then ends the round and
// tells nothing further. A round whose approved commit a push is still on its way from is open,
// not stuck, and tells nobody: the push's new head decides whether the approval stands. No CI
// verdict is read anywhere here (TestAnApprovalEndsTheRoundWhateverCISays).
func TestARoundItsApprovalCannotEndTellsTheArchitect(t *testing.T) {
	approve := func(id int64, head string) intake.Fact { return reviewAt(id, "approved", head) }
	sync := func(head string) intake.Fact {
		return intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: head}
	}
	// A push leaves GitHub's answer for the head before it saying nothing about the new head
	// (classify's advance), so the round that ends on an approval of that head ends on a read of
	// it: the daemon's own, which every push asks for (workflow's askMergeState).
	read := func(head string) intake.Fact {
		return intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: head,
			Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean}
	}
	push := func(from, to string) intake.Fact {
		return intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: from, After: to}
	}
	for _, tc := range []struct {
		name  string
		steps []intake.Fact
		// told is what the one notice's reason names, and by the fact that wrote it; empty, the
		// architect is told nothing.
		told []string
		by   string
		// then is what ends the round, read before approved where the push dropped GitHub's
		// answer for the head.
		then []intake.Fact
		want phase.Phase
	}{
		{name: "a push since the approved head, then an approval of the head",
			steps: []intake.Fact{sync(headTwo), push(headOne, headTwo), approve(1, headOne)},
			told:  []string{headTwo}, by: byCompletion, then: []intake.Fact{read(headTwo), approve(2, headTwo)}, want: phase.Accepting},
		{name: "a push since the approved head, then a request for changes",
			steps: []intake.Fact{sync(headTwo), push(headOne, headTwo), approve(1, headOne)},
			told:  []string{headTwo}, by: byCompletion, then: []intake.Fact{reviewAt(2, "changes_requested", headTwo)}, want: phase.Implementing},
		{name: "a second push recorded after the completion",
			steps: []intake.Fact{sync(headTwo), push(headOne, headTwo), sync(headThree), approve(1, headOne), push(headTwo, headThree)},
			told:  []string{headThree}, by: byCompletion, then: []intake.Fact{read(headThree), approve(2, headThree)}, want: phase.Accepting},
		{name: "a push whose new head has not arrived",
			steps: []intake.Fact{push(headOne, headTwo), approve(1, headOne)}, want: phase.Reviewing},
		{name: "a push whose new head arrives after the completion",
			steps: []intake.Fact{push(headOne, headTwo), approve(1, headOne), sync(headTwo)},
			told:  []string{headTwo}, by: byPush, then: []intake.Fact{read(headTwo), approve(2, headTwo)}, want: phase.Accepting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedReviewOf(t, pool, headOne, "green")
			apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
			for i, fact := range tc.steps {
				if result := apply(fmt.Sprintf("step-%d", i), fact); result.Refusal != nil {
					t.Fatalf("step %d (%T) was refused: %+v", i, fact, result.Refusal)
				}
			}
			if got := issuePhase(t, pool); got != phase.Reviewing {
				t.Fatalf("the issue is in %s, want reviewing", got)
			}
			told := reviewStuckNotices(t, pool)
			if len(tc.told) == 0 && len(told) != 0 {
				t.Fatalf("the architect was told %+v, want nothing while the push's new head is on its way", told)
			}
			if len(tc.told) != 0 {
				if len(told) != 1 {
					t.Fatalf("the architect was told %d times (%+v), want once", len(told), told)
				}
				for _, want := range tc.told {
					if !strings.Contains(told[0].Reason, want) {
						t.Fatalf("the notice's reason %q does not name %q", told[0].Reason, want)
					}
				}
				if told[0].Summary != tc.by {
					t.Fatalf("the notice was written by %q, want %q", told[0].Summary, tc.by)
				}
			}
			for i, fact := range tc.then {
				if result := apply(fmt.Sprintf("then-%d", i), fact); result.Refusal != nil {
					t.Fatalf("then %d (%T) was refused: %+v", i, fact, result.Refusal)
				}
			}
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("the issue is in %s, want %s", got, tc.want)
			}
			if got := reviewStuckNotices(t, pool); len(got) != len(told) {
				t.Fatalf("the round's end told the architect again: %+v", got)
			}
		})
	}
}

// GitHub delivers a branch's push and the pull request's synchronize in either order. A round
// already stuck - its reviewer completed with no verdict - is re-asked by the head's arrival
// whether or not the head's push is known yet: any push may change code, so the decision must now
// be of the new head, and the architect is told once that the round is stuck there. The push,
// arriving on either side of its head, leaves the round stuck the same way and tells nothing more,
// and so does an approval the App wrote on GitHub, which decides nothing (RefusesRound): the round
// is stuck in the way it was already told. Both orders tell the same notices.
func TestAHeadArrivingReasksAStuckRoundInEitherOrder(t *testing.T) {
	// A step is a fact and how many review-stuck notices the outbox holds once it is applied.
	type step struct {
		fact intake.Fact
		told int
	}
	complete := intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Summary: "reviewed", HandoffRecord: "review-1", HandoffVersion: 1}
	approve := intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, State: "approved", CommitID: headOne, Author: testAppLogin, Body: "approved"}
	sync := intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: headTwo}
	push := intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo}
	// told is each review-stuck notice in the order written: the fact that wrote it, and what its
	// reason names.
	told := []record.Notice{{Summary: byCompletion, Reason: "verdict of head " + headOne + " -"}, {Summary: byPush, Reason: "verdict of head " + headTwo + " -"}}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{name: "the head before its push", steps: []step{{complete, 1}, {sync, 2}, {push, 2}, {approve, 2}}},
		{name: "the push before its head", steps: []step{{complete, 1}, {push, 1}, {sync, 2}, {approve, 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedReviewOf(t, pool, headOne, "green")
			apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
			for i, s := range tc.steps {
				if result := apply(fmt.Sprintf("step-%d", i), s.fact); result.Refusal != nil {
					t.Fatalf("step %d (%T) was refused: %+v", i, s.fact, result.Refusal)
				}
				if got := reviewStuckNotices(t, pool); len(got) != s.told {
					t.Fatalf("after step %d (%T) the architect was told %d times (%+v), want %d", i, s.fact, len(got), got, s.told)
				}
			}
			if got := issuePhase(t, pool); got != phase.Reviewing {
				t.Fatalf("the issue is in %s, want reviewing", got)
			}
			got := reviewStuckNotices(t, pool)
			if len(got) != len(told) {
				t.Fatalf("the architect was told %d times (%+v), want %d", len(got), got, len(told))
			}
			for i, want := range told {
				if got[i].Summary != want.Summary || !strings.Contains(got[i].Reason, want.Reason) {
					t.Fatalf("notice %d was written by %q naming %q, want by %q naming %q", i, got[i].Summary, got[i].Reason, want.Summary, want.Reason)
				}
			}
		})
	}
}

// reviewStuckNotices is every review-stuck notice the outbox holds, in order.
func reviewStuckNotices(t *testing.T, pool *pgxpool.Pool) []record.Notice {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select coalesce(payload->>'role', ''), coalesce(payload->>'phase', ''), coalesce(payload->>'summary', ''),
		coalesce(payload->>'reason', ''), coalesce(payload->>'topic', '')
		from outbox where kind = 'notice' and payload->>'kind' = 'review-stuck' order by id`)
	if err != nil {
		t.Fatalf("read review-stuck notices: %v", err)
	}
	defer rows.Close()
	notices := []record.Notice{}
	for rows.Next() {
		notice := record.Notice{Kind: "review-stuck"}
		if err := rows.Scan(&notice.Role, &notice.Phase, &notice.Summary, &notice.Reason, &notice.Topic); err != nil {
			t.Fatalf("scan review-stuck notice: %v", err)
		}
		notices = append(notices, notice)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate review-stuck notices: %v", err)
	}
	return notices
}

// reviewClaim is LEGION-208's reviewer claim as the daemon names it, the token claim.NewToken
// spells for the issue's project, key and role (the workflow's reviewerToken): the claim the
// fixtures record the reviewer's session on, and the claim each of the reviewer's completions
// carries, as the claim a reviewer's agent completes under does.
const reviewClaim claim.Token = "legion-legion-legion-208-reviewer"

// seedRoundOneRows seeds LEGION-208's implementer and reviewer rows as a first review round holds
// them. The implementer's row names the claim its completion carried. The reviewer's row holds the
// entry the round's start raised, with its task, raised before any review of the round
// (record.PhaseRow.Entry, EntryTask and EntryRaisedAt), as every round's row does: only the
// reviewer's verdict while the row holds that entry's task moves the commit the reviewer read
// (workflow's handoff). It names no claim yet: only a completion writes a row's claim
// (record.PhaseRow.Claim, which the engine's completion handler writes). The reviewer's claim is
// recorded as the daemon records it, under the token the daemon names it by (reviewClaim) and
// with the session its agent registered.
func seedRoundOneRows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleReviewer, Entry: 1,
		EntryTask: "Continue root. Issue: LEGION-208. Phase: reviewing. Pull request: #42.", EntryRaisedAt: seededRaisedAt})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	seedClaimSession(t, pool, reviewClaim, "ses-reviewer")
}

// seedClaimSession records a claims row for token holding session, so the daemon is known to have
// started the reviewer on the issue (engine's leftItsPhase). A row already seeded takes the
// session given, so a fixture that records the claim and a test that then names its session do not
// collide.
func seedClaimSession(t *testing.T, pool *pgxpool.Pool, token claim.Token, session string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `insert into claims (token, project, tree, issue, role, generation, session, session_file,
		state, launch_failures, prompt_failures, prompt_retires, uncertain_streak)
		values ($1, 'LEGION', 'LEGION-208', 'LEGION-208', 'reviewer', 1, $2, '', 'working', 0, 0, 0, 0)
		on conflict (token) do update set session = excluded.session`,
		string(token), session); err != nil {
		t.Fatalf("seed the claim's session: %v", err)
	}
}

// reviewerDecision is LEGION-208's reviewer row's recorded decision, nil until a review decides.
func reviewerDecision(t *testing.T, pool *pgxpool.Pool) *record.ReviewDecision {
	t.Helper()
	return phaseRowOf(t, pool, claim.RoleReviewer).Decision
}

// The head the round records for a verdict is the commit the verdict names, not the head the pull
// request has reached: here the reviewer approves the head the record holds while the
// implementer's push to the next head is on its way. The approval stands for the head it read
// only, so the round stays open while the push's head comes, and the arrival of that head, which
// no verdict read, moves nothing. The commit the reviewer's next round is told
// (record.PhaseRow.ReviewedHead) is the verdict's commit for the same reason.
func TestTheRoundRecordsTheHeadTheReviewsFooterNames(t *testing.T) {
	pool := migratedPool(t)
	seedReviewOf(t, pool, headOne, "green")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("push", intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo})
	apply("approve", reviewAt(1, "approved", headOne))
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the approval the issue is in %s, want reviewing: a push is on its way from the head it read", got)
	}
	if got := reviewerDecision(t, pool); got == nil || got.State != "approved" || got.Head != headOne {
		t.Fatalf("the reviewer's decision = %+v, want the approval recorded at %s, the commit its verdict names", got, headOne)
	}
	if got := reviewerRow(t, pool).ReviewedHead; got != headOne {
		t.Fatalf("the reviewer's row reads the reviewed commit %q, want %s, the commit its verdict names", got, headOne)
	}

	apply("sync", intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: headTwo})
	apply("read", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headTwo,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the push's head arrived the issue is in %s, want reviewing: no review read that head", got)
	}
	if got := reviewerDecision(t, pool); got == nil || got.Head != headOne {
		t.Fatalf("the reviewer's decision = %+v, want it still recorded at %s", got, headOne)
	}
}

// A person's request for changes refuses the head by the commit_id its webhook carried, and the
// round goes back to implementing. The daemon reads no list of a pull request's reviews, whose
// commit_id GitHub moves with the pull request, so the webhook is the one way a person's review
// reaches a round.
func TestAPersonsRequestForChangesByWebhookRefusesTheHead(t *testing.T) {
	pool := migratedPool(t)
	seedReviewOf(t, pool, headOne, "green")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("request-changes", intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 1, State: "changes_requested", CommitID: headOne,
		Author: "a-writer", AuthorCanWrite: true, Body: "rename the widget", SubmittedAt: time.Date(2026, 9, 22, 23, 59, 0, 0, time.UTC)})
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the writer's request for changes the issue is in %s, want implementing", got)
	}
}

// A push the record's bound lets go - the oldest of more pushes waiting for a link than the bound
// keeps, each behind a webhook that never arrived - is logged as a warning that names it, once, by
// the push that let it go: the move it would have linked into is judged without it.
func TestAPushTheBoundLetsGoIsWarnedByName(t *testing.T) {
	const message = "workflow: a pull request holds the most pushes waiting for a link; the oldest is let go"
	pool := migratedPool(t)
	seedReviewOf(t, pool, "head", "green")
	var logged bytes.Buffer
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, slog.New(slog.NewTextHandler(&logged, nil))))
	// Sixteen pushes of a line the branch never takes are the most the record keeps waiting
	// (classify's maxWaitingPushes); the seventeenth lets the first go.
	for i := 1; i <= 17; i++ {
		logged.Reset()
		if result := apply(fmt.Sprintf("push-%d", i), pushedFromTo(fmt.Sprintf("x%d", i-1), fmt.Sprintf("x%d", i))); result.Refusal != nil {
			t.Fatalf("push %d was refused: %+v", i, result.Refusal)
		}
		var warned []string
		for _, line := range strings.Split(logged.String(), "\n") {
			if strings.Contains(line, message) {
				warned = append(warned, line)
			}
		}
		if i < 17 {
			if len(warned) != 0 {
				t.Fatalf("push %d of 16 waiting warned %q, want nothing", i, warned)
			}
			continue
		}
		if len(warned) != 1 {
			t.Fatalf("the push past the bound warned %d times (%q), want once", len(warned), warned)
		}
		for _, want := range []string{"level=WARN", "issue=LEGION-208", "pullRequest=42", "before=x0", "after=x1"} {
			if !strings.Contains(warned[0], want) {
				t.Fatalf("the warning %q does not name %s, want the push let go named", warned[0], want)
			}
		}
	}
}

// A pull request holds at most seventeen pushes of the same two heads on their way, before their
// move is made (classify's maxCopies): the eighteenth lets the oldest after the first go, logged as
// a warning that names it, once, and no push waiting for a link is let go with it.
func TestACopyOfTwoHeadsOnTheirWayLetGoIsWarnedByName(t *testing.T) {
	const message = "workflow: a pull request holds the most pushes of the same two heads on their way; the oldest after the first is let go"
	pool := migratedPool(t)
	seedReviewOf(t, pool, "head", "green")
	var logged bytes.Buffer
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, slog.New(slog.NewTextHandler(&logged, nil))))
	for i := 1; i <= 18; i++ {
		logged.Reset()
		if result := apply(fmt.Sprintf("push-%d", i), pushedFromTo("head", "h1")); result.Refusal != nil {
			t.Fatalf("push %d was refused: %+v", i, result.Refusal)
		}
		var warned []string
		for _, line := range strings.Split(logged.String(), "\n") {
			if strings.Contains(line, "let go") {
				warned = append(warned, line)
			}
		}
		if i < 18 {
			if len(warned) != 0 {
				t.Fatalf("copy %d of 17 warned %q, want nothing", i, warned)
			}
			continue
		}
		if len(warned) != 1 || !strings.Contains(warned[0], message) {
			t.Fatalf("the copy past the bound warned %q, want the one copy let go", warned)
		}
		for _, want := range []string{"level=WARN", "issue=LEGION-208", "pullRequest=42", "before=head", "after=h1"} {
			if !strings.Contains(warned[0], want) {
				t.Fatalf("the warning %q does not name %s, want the copy let go named", warned[0], want)
			}
		}
	}
}

// A move holds at most sixteen later pushes of its two heads (classify's maxRepeats), each the
// move made again or its push delivered again; the seventeenth lets the oldest go, logged as a
// warning that names it, once, and no push waiting for a link is let go with it.
func TestARepeatAMoveLetsGoIsWarnedByName(t *testing.T) {
	const message = "workflow: a move holds the most pushes of its two heads made again; the oldest is let go"
	pool := migratedPool(t)
	seedReviewOf(t, pool, "head", "green")
	var logged bytes.Buffer
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, slog.New(slog.NewTextHandler(&logged, nil))))
	if result := apply("push-0", pushedFromTo("head", "h1")); result.Refusal != nil {
		t.Fatalf("the first push was refused: %+v", result.Refusal)
	}
	if result := apply("sync", intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", Before: "head", HeadSHA: "h1"}); result.Refusal != nil {
		t.Fatalf("h1's synchronize was refused: %+v", result.Refusal)
	}
	for i := 1; i <= 17; i++ {
		logged.Reset()
		if result := apply(fmt.Sprintf("push-%d", i), pushedFromTo("head", "h1")); result.Refusal != nil {
			t.Fatalf("push %d was refused: %+v", i, result.Refusal)
		}
		var warned []string
		for _, line := range strings.Split(logged.String(), "\n") {
			if strings.Contains(line, message) || strings.Contains(line, "waiting for a link") {
				warned = append(warned, line)
			}
		}
		if i < 17 {
			if len(warned) != 0 {
				t.Fatalf("repeat %d of 16 warned %q, want nothing", i, warned)
			}
			continue
		}
		if len(warned) != 1 || !strings.Contains(warned[0], message) {
			t.Fatalf("the repeat past the bound warned %q, want the one repeat let go", warned)
		}
		for _, want := range []string{"level=WARN", "issue=LEGION-208", "pullRequest=42", "before=head", "after=h1"} {
			if !strings.Contains(warned[0], want) {
				t.Fatalf("the warning %q does not name %s, want the repeat let go named", warned[0], want)
			}
		}
	}
}
