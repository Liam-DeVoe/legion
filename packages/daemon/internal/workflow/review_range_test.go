package workflow

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// reviewerRow is LEGION-208's reviewer row as the record holds it.
func reviewerRow(t *testing.T, pool *pgxpool.Pool) record.PhaseRow {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(context.Background())
	rows, err := record.NewStore().Phases(context.Background(), tx, "LEGION-208")
	if err != nil {
		t.Fatalf("read phases: %v", err)
	}
	row := record.PhaseRowOf(rows, claim.RoleReviewer)
	if row.Role != claim.RoleReviewer {
		t.Fatalf("LEGION-208 holds no reviewer's row")
	}
	return row
}

// reviewAt is the review App's review of state as the reviewer posts it, its Legion footer naming
// head: the shared fixtures record no session on the reviewer's claim, so the login decides
// (byReviewer).
func reviewAt(id int64, state, head string, at time.Time) intake.PullRequestReview {
	return intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: id, State: state, CommitID: head,
		Author: testReviewApp, Body: reviewBody("the review", head), SubmittedAt: at}
}

// backToReviewing takes LEGION-208 from implementing through testing into reviewing again, as the
// implementer's and the tester's completions of round n do.
func backToReviewing(t *testing.T, apply func(string, intake.Fact) intake.Result, round int) {
	t.Helper()
	n := strconv.Itoa(round)
	apply("implementer-"+n, intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim",
		Summary: "fixed", Commit: "round-" + n, HandoffRecord: "implement-" + n, HandoffVersion: round})
	apply("tester-"+n, intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim",
		Summary: "tested", Verdict: "pass", HandoffRecord: "test-" + n, HandoffVersion: round})
}

// The reviewer's own deciding review writes the commit it read on the reviewer's row beside the
// round's decision (record.PhaseRow.ReviewedHead), and the entry into the next round, which
// clears the decision (clearHandoff), leaves that commit where it stands.
func TestADecidingReviewRecordsTheCommitItRead(t *testing.T) {
	pool := migratedPool(t)
	seedReviewOf(t, pool, headOne, "green")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("request-changes", reviewAt(1, "changes_requested", headOne, time.Date(2026, 9, 22, 23, 59, 0, 0, time.UTC)))
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the request for changes the issue is in %s, want implementing", got)
	}
	if got := reviewerRow(t, pool).ReviewedHead; got != headOne {
		t.Fatalf("the reviewer's row reads the reviewed commit %q, want %s, the head its footer names", got, headOne)
	}

	backToReviewing(t, apply, 2)
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the tester passed round 2 the issue is in %s, want reviewing", got)
	}
	row := reviewerRow(t, pool)
	if row.Decision != nil {
		t.Fatalf("round 2's reviewer row holds the decision %+v, want none: the entry into reviewing ends round 1's", row.Decision)
	}
	if row.ReviewedHead != headOne {
		t.Fatalf("round 2's reviewer row reads the reviewed commit %q, want %s: no entry into a phase clears it", row.ReviewedHead, headOne)
	}
}

// Only a review that decides the round in reviewing moves the commit: a comment decides nothing, a
// review refused for the head its footer names decides nothing, and a review of an issue outside
// reviewing decides no round.
func TestAReviewThatDecidesNothingRecordsNoCommit(t *testing.T) {
	at := time.Date(2026, 9, 22, 23, 59, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		review intake.PullRequestReview
		// seed puts the issue where the review finds it, after seedReviewOf.
		seed func(t *testing.T, pool *pgxpool.Pool)
	}{
		{name: "a comment", review: reviewAt(1, "commented", headOne, at)},
		{name: "a review its footer's head refuses", review: func() intake.PullRequestReview {
			r := reviewAt(1, "changes_requested", headOne, at)
			r.Body = reviewBody("the review", headEarlier)
			return r
		}()},
		{name: "a review outside reviewing", review: reviewAt(1, "approved", headOne, at), seed: func(t *testing.T, pool *pgxpool.Pool) {
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
				Phase: phase.Testing, Generation: 1, Status: "testing", Rank: "U"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedReviewOf(t, pool, headOne, "green")
			if tc.seed != nil {
				tc.seed(t, pool)
			}
			apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
			apply("review", tc.review)
			row := reviewerRow(t, pool)
			if row.Decision != nil && row.Decision.State != "" {
				t.Fatalf("the reviewer's row holds the decision %+v, want none: the review decides nothing", row.Decision)
			}
			if row.ReviewedHead != "" {
				t.Fatalf("the reviewer's row reads the reviewed commit %q, want none: the review decided nothing", row.ReviewedHead)
			}
		})
	}
}

// A person's review decides a round as it always has, on the commit_id its webhook carried, and
// the round's decision is the person's. The commit the reviewer's next round is told is still the
// one the reviewer's own last deciding review read: the reviewer never read the person's.
func TestAPersonsReviewDoesNotMoveTheReviewersCommit(t *testing.T) {
	pool := migratedPool(t)
	seedReviewOf(t, pool, headOne, "green")
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("reviewer-requests-changes", reviewAt(1, "changes_requested", headOne, time.Date(2026, 9, 22, 23, 59, 0, 0, time.UTC)))
	backToReviewing(t, apply, 2)
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the tester passed round 2 the issue is in %s, want reviewing", got)
	}

	apply("person-requests-changes", intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 2, State: "changes_requested", CommitID: headTwo,
		Author: "a-writer", AuthorCanWrite: true, Body: "rename it", SubmittedAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)})
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the person's request for changes the issue is in %s, want implementing", got)
	}
	row := reviewerRow(t, pool)
	if row.Decision == nil || row.Decision.State != "changes_requested" || row.Decision.Head != headTwo || row.Decision.Body != "rename it" {
		t.Fatalf("round 2's decision = %+v, want the person's request for changes at %s", row.Decision, headTwo)
	}
	if row.ReviewedHead != headOne {
		t.Fatalf("the reviewer's row reads the reviewed commit %q, want %s: the person's review is no review the reviewer read", row.ReviewedHead, headOne)
	}
}
