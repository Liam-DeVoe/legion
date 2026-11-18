package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// workBackEngine is the engine the work-back proofs drive: the review round cap and the hooks
// each proof configures, and the review App's login, since a review that requests changes is one
// of the moves back they make.
func workBackEngine(reviewRoundCap int, hooks ...Hook) *Engine {
	return New(record.NewStore(), Config{
		Project: "LEGION", Steps: config.DesignGateRootIssues.Steps(), ReviewRoundCap: reviewRoundCap,
		Linger: time.Hour, ProductionCheckRole: claim.RoleTester, ReviewAppLogin: testReviewApp,
		Repositories: testRepositories, Hooks: hooks,
		Clock: func() time.Time { return time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC) },
	}, nil)
}

// seedWorkBackIssue puts the issue in implementing with an open pull request at head, and the
// implementer's row the counts are kept on, which is what every move back below starts from.
func seedWorkBackIssue(t *testing.T, pool *pgxpool.Pool, head string) {
	t.Helper()
	standsIn(t, pool, phase.Implementing, "in_progress")
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: head})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
}

// backwardMoves is the implementer row's count of the issue's moves back, each under the
// "<from>-<to>" pair it was made by (record.PhaseRow.BackwardMoves).
func backwardMoves(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	var encoded []byte
	if err := pool.QueryRow(context.Background(),
		"select backward_moves from phases where issue = 'LEGION-208' and role = 'implementer'").Scan(&encoded); err != nil {
		t.Fatalf("read the implementer's backward moves: %v", err)
	}
	moves := map[string]int{}
	if err := json.Unmarshal(encoded, &moves); err != nil {
		t.Fatalf("decode the implementer's backward moves %s: %v", encoded, err)
	}
	return moves
}

// testerFails is the tester's fail of the round named, which sends the work back from testing.
// The tester's row is seeded afresh each time because the transition before it cleared the
// handoff the completion writes (clearHandoff).
func testerFails(t *testing.T, pool *pgxpool.Pool, engine *Engine, round string) {
	t.Helper()
	standsIn(t, pool, phase.Testing, "testing")
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim"})
	applyRefusingNothing(t, pool, engine, intake.HandoffComplete{Generation: 1, Issue: "LEGION-208",
		Role: claim.RoleTester, Claim: "test-claim", Summary: "the fix is not in", Verdict: "fail",
		HandoffRecord: "test-" + round, HandoffVersion: 1})
}

// reviewerRequestsChanges is the review App's request for changes at head, the half that ends a
// review round the reviewer has completed, which sends the work back from reviewing. Its body
// carries the Legion footer naming the head it read, which is what lets a review-App review
// decide a round at all (refusesRound).
func reviewerRequestsChanges(t *testing.T, pool *pgxpool.Pool, engine *Engine, round, head string) {
	t.Helper()
	standsIn(t, pool, phase.Reviewing, "needs_review")
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: "review-claim",
		HandoffCommit: "review-" + round})
	seedClaimSession(t, pool, reviewClaim, "ses-reviewer")
	applyRefusingNothing(t, pool, engine, intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42,
		State: "changes_requested", CommitID: head, HeadSHA: head, Author: testReviewApp,
		Body: reviewBody("the round "+round+" findings", head)})
}

// architectMovesBack is the architect's own move of the issue out of accepting, the one phase it
// holds, back to the phase named.
func architectMovesBack(t *testing.T, pool *pgxpool.Pool, engine *Engine, to phase.Phase) {
	t.Helper()
	standsIn(t, pool, phase.Accepting, "needs_review")
	applyRefusingNothing(t, pool, engine, intake.BackwardMove{Issue: "LEGION-208",
		Requester: claim.RoleArchitect, To: to, Reason: "the work does not stand"})
}

// mergerMovesBack is the merger's own move of the issue out of merging, the one phase it holds,
// back to reviewing: a worker's own backward move (request_backward_move) made from the far end
// of the line, for a head it would not merge.
func mergerMovesBack(t *testing.T, pool *pgxpool.Pool, engine *Engine) {
	t.Helper()
	standsIn(t, pool, phase.Merging, "retro")
	applyRefusingNothing(t, pool, engine, intake.BackwardMove{Issue: "LEGION-208",
		Requester: claim.RoleMerger, To: phase.Reviewing,
		Reason: "the head GitHub shows is not the head this merger read"})
}

// marksConflicting is the daemon's read of GitHub that judges head, the pull request's own
// recorded head, conflicting with its base: the mark a conflict round is read against
// (classify.ConflictRound). It is applied while the issue stands in implementing, the one phase
// with no row for the conflict trigger, so it records the judgement and moves nothing
// (workflow's mergeability).
func marksConflicting(t *testing.T, pool *pgxpool.Pool, engine *Engine, head string) {
	t.Helper()
	applyRefusingNothing(t, pool, engine, intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42,
		Base: "main", HeadSHA: head, Mergeable: record.MergeabilityConflicting})
}

// Every move back is counted under its own "<from>-<to>" pair, and the pass the daemon has always
// kept rises with all of them.
//
// Four moves are driven here, each of a different pair: a tester's fail (testing-implementing),
// the review App's request for changes (reviewing-implementing), the architect's own move back to
// the implementer (accepting-implementing) and the architect's move back to the plan
// (accepting-planning). Each pair's count is read on its own, so a build that kept one number for
// every move back reads 4 under the first pair and nothing under the others, and a build that
// counted only the moves that end in implementing reads nothing under accepting-planning. The
// architect's two moves are the same role's, made from the same phase: what tells them apart is
// the phase they send the work to, which is half of what a pair is.
//
// The fifth move is made while the daemon's record holds the head conflicting with its base. It
// raises no pair at all: nobody sent the work back, so the move is of no kind a hook or an
// operator counts (recordPass). The pass rises on all five, because a phase completion's key is
// built from it (record.PhaseRow.Pass, api/handoff.go's handoffPosition) and two passes through
// one phase that carried the same number would collide.
//
// The review rounds review_round_cap bounds are counted apart from the pairs, and only a review
// that requested changes opens one (recordRound): of the five moves here the one review's is the
// only round, and no move at the conflicting head makes a second.
func TestEveryBackwardMoveIsCountedByItsPair(t *testing.T) {
	pool := migratedPool(t)
	// The cap is set above every round this proof opens: what it posts is another test's subject
	// (TestTheArchitectIsToldOncePerCapValue), and a cap reached here would say nothing about the
	// counts.
	engine := workBackEngine(50)
	seedWorkBackIssue(t, pool, headOne)

	testerFails(t, pool, engine, "1")
	reviewerRequestsChanges(t, pool, engine, "1", headOne)
	architectMovesBack(t, pool, engine, phase.Planning)
	architectMovesBack(t, pool, engine, phase.Implementing)

	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the four moves back the issue is in %s, want implementing: each of them hands the work to the implementer or the planner", got)
	}
	moves := backwardMoves(t, pool)
	for _, want := range []struct {
		pair  string
		count int
		by    string
	}{
		{pair: "testing-implementing", count: 1, by: "the tester's fail"},
		{pair: "reviewing-implementing", count: 1, by: "the review App's request for changes"},
		{pair: "accepting-implementing", count: 1, by: "the architect's move back to the implementer"},
		{pair: "accepting-planning", count: 1, by: "the architect's move back to the plan"},
	} {
		if moves[want.pair] != want.count {
			t.Fatalf("the moves back counted under %s = %d, want %d, from %s; the whole count is %v",
				want.pair, moves[want.pair], want.count, want.by, moves)
		}
	}
	if pass := implementerPass(t, pool); pass != 4 {
		t.Fatalf("after the four moves back the implementer's row counts %d passes, want 4: every move back raises the pass, whatever kind it is", pass)
	}
	if rounds, conflicts := implementerRounds(t, pool), implementerConflictRounds(t, pool); rounds != 1 || conflicts != 0 {
		t.Fatalf("after the four moves back the implementer's row counts %d review rounds of which %d conflict rounds, want the one review's round and no conflict round: a round is a review that requested changes alone (recordRound), and none of these was made at a conflicting head", rounds, conflicts)
	}

	// The fifth move is made while the record holds the pull request's own head conflicting with
	// its base, which is what keeps it out of the pairs whatever fact makes the move.
	marksConflicting(t, pool, engine, headOne)
	testerFails(t, pool, engine, "2")

	if pass := implementerPass(t, pool); pass != 5 {
		t.Fatalf("after the move made at the conflicting head the implementer's row counts %d passes, want 5: the pass rises on a move no pair counts, since the completion key is built from it", pass)
	}
	if rounds, conflicts := implementerRounds(t, pool), implementerConflictRounds(t, pool); rounds != 1 || conflicts != 0 {
		t.Fatalf("after the move made at the conflicting head the implementer's row counts %d review rounds of which %d conflict rounds, want the one review's round and no conflict round: a tester's fail opens no review round, at a conflicting head or anywhere else", rounds, conflicts)
	}
	conflicted := backwardMoves(t, pool)
	if conflicted["testing-implementing"] != 1 {
		t.Fatalf("the moves back counted under testing-implementing = %d, want the 1 the tester's own fail left: a move made at a conflicting head raises no pair, since nobody sent the work back; the whole count is %v",
			conflicted["testing-implementing"], conflicted)
	}
	counted := 0
	for _, count := range conflicted {
		counted += count
	}
	if counted != 4 {
		t.Fatalf("the pairs add up to %d moves back, want the 4 moves somebody made, of the 5 passes the implementer's row counts; the whole count is %v", counted, conflicted)
	}
}

// mergerConflictReason is the merger's reason for sending the work back after the merge queue
// took the pull request out for a conflict and GitHub then showed the head dirty.
const mergerConflictReason = "the merge queue removed the pull request for a merge conflict and GitHub shows the head dirty"

// mergerReads is the daemon's read of GitHub made just before the merger's move out of merging
// (intake.BackwardMove.Mergeability): pull request number of sjawhar/legion answering mergeable,
// with state, for head.
func mergerReads(number int, head string, mergeable record.Mergeability, state record.MergeState) *intake.PullRequestMergeability {
	return &intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: number, Base: "main", HeadSHA: head,
		Mergeable: mergeable, MergeState: state, ReadAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)}
}

// A merger that sends the work back because GitHub shows the head conflicting with its base,
// after the merge queue took the pull request out for a conflict, makes a conflict round: it
// raises no pair and fires no hook, as a move made at a head the record holds conflicting never
// has (recordPass). The record held no conflict when the merger moved, since the daemon's own read
// at the removal records none (the daemon's mergeQueueRead), so what makes the move one is the
// read of GitHub the daemon makes for the move itself: the move carries it, and the workflow
// records it before it classifies the move (backward, recordMergeability).
//
// Each case starts from one record: the issue in merging, its pull request open at headOne with
// no conflict recorded, and a hook that fires on every move back. A read that answers the
// recorded head conflicting is the one case excused: no pair, no hook, the pass risen as on every
// move back, the head held conflicting afterwards, and the issue in implementing on the merger's
// own move, with the merger's reason in the implementer's task. Every other case is counted under
// merging-implementing and fires the hook: the move carrying no read, which is the control the
// first case is read against; a read answering the head mergeable; one GitHub has not computed
// yet; and one that answers for a head the record does not stand at, or for another pull request
// than the record holds, which is dropped.
func TestAMergersMoveBackForAConflictWithTheBaseRaisesNoPair(t *testing.T) {
	for _, tc := range []struct {
		name string
		read *intake.PullRequestMergeability
		// excused is whether the move is a conflict round, and conflicting the head the record
		// holds conflicting once it is applied.
		excused     bool
		conflicting string
	}{
		{name: "GitHub answers the recorded head conflicting", read: mergerReads(42, headOne, record.MergeabilityConflicting, record.MergeStateDirty),
			excused: true, conflicting: headOne},
		{name: "the move carries no read", read: nil},
		{name: "GitHub answers the recorded head mergeable", read: mergerReads(42, headOne, record.MergeabilityMergeable, record.MergeStateClean)},
		{name: "GitHub has not computed an answer", read: mergerReads(42, headOne, record.MergeabilityUnknown, record.MergeStateUnknown)},
		{name: "GitHub answers for a head the record does not stand at", read: mergerReads(42, headTwo, record.MergeabilityConflicting, record.MergeStateDirty)},
		{name: "GitHub answers for another pull request", read: mergerReads(43, headOne, record.MergeabilityConflicting, record.MergeStateDirty)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := workBackEngine(50, Hook{Name: "work-back", On: HookEventWorkBack, Every: 1,
				Roles: []claim.Role{claim.RoleImplementer}, Prompt: saySoFar})
			seedWorkBackIssue(t, pool, headOne)
			standsIn(t, pool, phase.Merging, "retro")

			applyRefusingNothing(t, pool, engine, intake.BackwardMove{Issue: "LEGION-208", Requester: claim.RoleMerger,
				To: phase.Implementing, Reason: mergerConflictReason, Mergeability: tc.read})

			moves, prompts, posted := backwardMoves(t, pool), hookPrompts(t, pool), firingMessages(t, pool)
			if tc.excused && (len(moves) != 0 || len(prompts) != 0 || len(posted) != 0) {
				t.Fatalf("the move counted %v and the hook wrote %d role messages and posted %q, want no pair and no firing: GitHub answered the head conflicting with its base, so nobody sent the work back",
					moves, len(prompts), posted)
			}
			if !tc.excused {
				if moves["merging-implementing"] != 1 || len(moves) != 1 {
					t.Fatalf("the move counted %v, want the one move back under merging-implementing", moves)
				}
				want := "Legion hook work-back fired at 1 moves back: merging to implementing. Prompted: implementer."
				if len(prompts) != 1 || !sameStrings(posted, []string{want}) {
					t.Fatalf("the hook wrote %d role messages and posted %q, want one message to the implementer and the one line %q", len(prompts), posted, want)
				}
			}
			if pass := implementerPass(t, pool); pass != 1 {
				t.Fatalf("the implementer's row counts %d passes, want 1: every move back raises the pass, a conflict round too", pass)
			}
			if got := conflictingHead(t, pool); got != tc.conflicting {
				t.Fatalf("the record holds %q conflicting after the move, want %q", got, tc.conflicting)
			}
			if got := issuePhase(t, pool); got != phase.Implementing {
				t.Fatalf("after the merger's move the issue is in %s, want implementing", got)
			}
			if task := startTask(t, pool, claim.RoleImplementer); !strings.Contains(task, "Reason: "+mergerConflictReason+".") {
				t.Fatalf("the implementer was started with %q, want the merger's own reason in it: the move is the merger's, made once", task)
			}
		})
	}
}
