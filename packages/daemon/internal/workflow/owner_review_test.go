package workflow

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// ownerAtReviewStep is the root LEGION-208 on the plan `implementer reviewer:owner`, with its open
// pull request #42 at head: the implementer's work is done and the task stands at step 2, the
// review step the owner does, which the move into it told the owner.
func ownerAtReviewStep(t *testing.T, pool *pgxpool.Pool, engine *Engine) record.Issue {
	t.Helper()
	root := stepRoot(t, pool, phase.Implementing)
	seedStepsAt(t, pool, root.Key, root.Generation, 1, nil, "implementer", "reviewer:owner")
	move(t, pool, engine, root.Key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
	assertWalksAt(t, pool, engine, root.Key, phase.Reviewing, stepPlace(2))
	if told := ownerSteps(t, pool, root.Key); len(told) != 1 || told[0].Role != claim.RoleReviewer {
		t.Fatalf("the move into the owner's review step told %+v, want one owner-step of the reviewer's step", told)
	}
	return root
}

// ownersReview is the owner's report of its review step 2 of key's task a, as the route
// complete_step applies it: the verdict, the step returnTo names (0 for none), the head the
// review read and the entry the owner's start raised on the reviewer's row.
func ownersReview(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, verdict string, returnTo int) intake.HandoffComplete {
	t.Helper()
	row := roleRowOf(t, pool, engine, key, claim.RoleReviewer)
	return intake.HandoffComplete{Issue: key, Role: claim.RoleReviewer, Claim: claim.Token(treeRoleToken(t, key, claim.RoleArchitect)), Generation: 1,
		Entry: row.Entry, Step: 2, Summary: "The rollback path is unnamed.", Commit: "head", Verdict: verdict, ReturnTo: returnTo,
		HandoffRecord: "review-1", HandoffVersion: 1}
}

// A review step the owner does ends by the owner's own report, with or without a review on
// GitHub: its fail returns the work to the step its returnTo names, whose agent is told the
// owner's report, and its pass starts the retro. No review on GitHub decides it: a request for
// changes from an account with write access, which settles a reviewer's round on its own, moves
// nothing at the owner's step and tells nothing.
//
// The controls: today's reviewing case (every completion to settleRound) leaves the owner's fail
// and pass at reviewing; settleRound without the owner's case takes the person's request for
// changes back to step 1 while the owner's step runs.
func TestAnOwnersReviewStepEndsByItsReport(t *testing.T) {
	t.Run("the owner's fail returns the work to step 1", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := ownerAtReviewStep(t, pool, engine)
		if result := applyFacts(t, pool, engine)("fail", ownersReview(t, pool, engine, root.Key, "fail", 1)); result.Refusal != nil {
			t.Fatalf("the owner's fail was refused: %+v", result.Refusal)
		}
		assertWalksAt(t, pool, engine, root.Key, phase.Implementing, stepPlace(1))
		start := lastStartOf(t, pool, root.Key, claim.RoleImplementer)
		for _, says := range []string{" Step 2 (reviewer) reported: fail. The rollback path is unnamed.", " Handoff: review version 1"} {
			if !strings.Contains(start.Task, says) {
				t.Fatalf("the implementer's task after the owner's fail = %q, want it to say %q", start.Task, says)
			}
		}
		if strings.Contains(start.Task, testerVerdictLabel) {
			t.Fatalf("the implementer's task = %q, want no %s: the verdict is the owner's review's", start.Task, testerVerdictLabel)
		}
	})

	t.Run("the owner's pass starts the retro", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := ownerAtReviewStep(t, pool, engine)
		if result := applyFacts(t, pool, engine)("pass", ownersReview(t, pool, engine, root.Key, "pass", 0)); result.Refusal != nil {
			t.Fatalf("the owner's pass was refused: %+v", result.Refusal)
		}
		assertWalksAt(t, pool, engine, root.Key, phase.Retro, record.TaskPlace{Kind: record.PlaceRetro})
		if start := lastStartOf(t, pool, root.Key, claim.RoleImplementer); start.Phase != phase.Retro {
			t.Fatalf("the implementer's last start = %+v, want the retro's", start)
		}
	})

	t.Run("a review on GitHub decides nothing at the owner's step", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := ownerAtReviewStep(t, pool, engine)
		notices := len(issueOutboxOf(t, pool, root.Key, string(record.OutboxKindNotice)))
		applyFacts(t, pool, engine)("person-requests-changes", intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 3,
			State: "changes_requested", CommitID: "head", HeadSHA: "head", Author: "a-writer", AuthorCanWrite: true, Body: "rename it"})
		assertWalksAt(t, pool, engine, root.Key, phase.Reviewing, stepPlace(2))
		if after := len(issueOutboxOf(t, pool, root.Key, string(record.OutboxKindNotice))); after != notices {
			t.Fatalf("the person's review wrote %d notices, want none", after-notices)
		}
	})
}
