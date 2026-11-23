package workflow

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/reviewround"
)

// Two commits a review of the task read: step 2's review, and the review of the reviewer at
// step 4.
const (
	stepTwoRead  = "2222222222222222222222222222222222222222"
	stepFourRead = "4444444444444444444444444444444444444444"
)

// recordReviewRead writes commit as the head the newest deciding review of the reviewer's step
// at place read (record.PhaseRow.ReviewedHead), as the workflow's review writes it for a deciding
// review of the step's own session.
func recordReviewRead(t *testing.T, pool *pgxpool.Pool, key string, place int, commit string) {
	t.Helper()
	execSQL(t, pool, `update phases set reviewed_head = $1 where issue = $2 and role = 'reviewer' and step_place = $3`, commit, key, place)
}

// reviewerStepFour walks LEGION-208 to its reviewer's step 4 for the round-th time, the reviewer
// at step 2 having read stepTwoRead and tester completing step 3, and answers step 4's row.
func reviewerStepFour(t *testing.T, pool *pgxpool.Pool, engine *Engine, tester claim.Token, round int) record.PhaseRow {
	t.Helper()
	key := "LEGION-208"
	if round == 1 {
		move(t, pool, engine, key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	}
	completesAs(t, pool, engine, claim.RoleImplementer, agentToken(t, "implementer"), "implement", round)
	recordReviewRead(t, pool, key, 2, stepTwoRead)
	move(t, pool, engine, key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})
	completesAs(t, pool, engine, claim.RoleTester, tester, "test", round)
	assertWalksAt(t, pool, engine, key, phase.Reviewing, stepPlace(4))
	return rolesRows(t, pool, engine, key, claim.RoleReviewer)[4]
}

// A reviewer step of another agent than the reviewer step before it reads the whole change at its
// first round. In `implementer:a reviewer tester:s reviewer:s`, step 4 is key s's, and step 2's
// review read a commit: step 4's first task names no commit of its own, `Last reviewed commit:
// none.`, and names step 2's commit as another agent's before it. Its row holds no commit. After a
// review of key s's own read a commit, a return to step 1 and the walk back to step 4, its task
// names that commit, its own, and no other agent's. Before, step 4's first round was named step
// 2's commit as its own, so key s read only the change past a commit another agent approved.
func TestAReviewerStepOfAnotherAgentReadsTheWholeChangeAtItsFirstRound(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "reviewer", "tester:s", "reviewer:s")
	keyS := agentToken(t, "tester")

	first := reviewerStepFour(t, pool, engine, keyS, 1)
	other := reviewround.AnotherAgentsSentence(stepTwoRead)
	if first.Claim != keyS || first.ReviewedHead != "" ||
		!strings.HasSuffix(first.EntryTask, other+reviewround.Sentences("", first.Entry)) {
		t.Fatalf("step 4's first round = claim %s, commit %q, task %q; want key s's, no commit of its own, and a task ending %q",
			first.Claim, first.ReviewedHead, first.EntryTask, other+reviewround.Sentences("", first.Entry))
	}

	recordReviewRead(t, pool, root.Key, 4, stepFourRead)
	move(t, pool, engine, root.Key, TriggerReviewRejected, stepPlace(1), record.PhaseRow{})
	second := reviewerStepFour(t, pool, engine, keyS, 2)
	if !strings.HasSuffix(second.EntryTask, reviewround.Sentences(stepFourRead, second.Entry)) ||
		strings.Contains(second.EntryTask, reviewround.AnotherAgentLabel) {
		t.Fatalf("step 4's second round task = %q, want it to end %q and name no other agent's commit",
			second.EntryTask, reviewround.Sentences(stepFourRead, second.Entry))
	}
}

// A reviewer step of the same agent as the reviewer step before it keeps today's rule: in
// `implementer:a reviewer:r tester reviewer:r`, steps 2 and 4 are key r's, the reviewer's claim,
// so step 4's first round is named step 2's commit as its own, and no other agent's. (Two steps of
// one role with no key are two agents, which this release refuses as a plan.)
func TestAReviewerStepOfTheSameAgentIsNamedTheStepBeforesCommit(t *testing.T) {
	pool := migratedPool(t)
	engine := textEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:a", "reviewer:r", "tester", "reviewer:r")

	first := reviewerStepFour(t, pool, engine, agentToken(t, "tester"), 1)
	if first.Claim != agentToken(t, "reviewer") || first.ReviewedHead != stepTwoRead ||
		!strings.HasSuffix(first.EntryTask, reviewround.Sentences(stepTwoRead, first.Entry)) ||
		strings.Contains(first.EntryTask, reviewround.AnotherAgentLabel) {
		t.Fatalf("step 4's first round = claim %s, commit %q, task %q; want the reviewer's, step 2's commit, and a task ending %q",
			first.Claim, first.ReviewedHead, first.EntryTask, reviewround.Sentences(stepTwoRead, first.Entry))
	}
}
