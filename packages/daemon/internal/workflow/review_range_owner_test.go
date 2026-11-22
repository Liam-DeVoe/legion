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

// ownerStepAgain moves key's work, back at step 1 after the owner's step returned it, on to the
// owner's step 2 again at newHead: the implementer pushes newHead and completes, as the review
// range's later round does (v-review-range).
func ownerStepAgain(t *testing.T, pool *pgxpool.Pool, engine *Engine, key, newHead string) {
	t.Helper()
	assertWalksAt(t, pool, engine, key, phase.Implementing, stepPlace(1))
	applyFacts(t, pool, engine)("push-"+newHead, intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/" + key, HeadSHA: newHead})
	move(t, pool, engine, key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
}

// reviewedLine is the sentence a later run of a review step is told the commit its run before
// read in (reviewround.CommitLabel).
func reviewedLine(sha string) string { return " Last reviewed commit: " + sha + "." }

// A review step the owner does is told, at its next run, the commit its run before read, as a
// reviewer's later round is (v-review-range for the owner): for `reviewer:owner`, the commit its
// complete_step fail named, and for `acceptance:owner`, the head of the pull request its
// request_backward_move acted on, since that move names no commit of its own. Its first run names
// no commit read. A step of another type the owner does names none at any run.
//
// The controls: the reviewed commit cleared between the two runs (the rule that reads the
// reviewer's row alone writes none for acceptance, and the move back writes none): the second
// owner-step of acceptance names no commit; the rule of the reviewer's and the deployment's step
// alone (a commit named only at step) names none at accepting either.
func TestAReviewStepOfTheOwnerGetsTheCommitItsRunBeforeRead(t *testing.T) {
	t.Run("reviewer:owner", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := ownerAtReviewStep(t, pool, engine)
		if result := applyFacts(t, pool, engine)("fail", ownersReview(t, pool, engine, root.Key, "fail", 1)); result.Refusal != nil {
			t.Fatalf("the owner's fail was refused: %+v", result.Refusal)
		}
		ownerStepAgain(t, pool, engine, root.Key, "head-2")
		told := ownerSteps(t, pool, root.Key)
		if len(told) != 2 {
			t.Fatalf("the owner was told %+v, want two owner-steps of step 2", told)
		}
		if !strings.Contains(told[0].Reason, reviewedLine("none")) {
			t.Fatalf("the first owner-step = %q, want it to name no commit read (%q)", told[0].Reason, reviewedLine("none"))
		}
		if !strings.Contains(told[1].Reason, reviewedLine("head")) {
			t.Fatalf("the second owner-step = %q, want it to say %q", told[1].Reason, reviewedLine("head"))
		}
	})

	t.Run("acceptance:owner", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Implementing)
		seedStepsAt(t, pool, root.Key, root.Generation, 1, nil, "implementer", "acceptance:owner")
		move(t, pool, engine, root.Key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
		assertWalksAt(t, pool, engine, root.Key, phase.Accepting, stepPlace(2))
		if result := applyFacts(t, pool, engine)("move-back", rootsMoveBack(t, pool, engine, root.Key, phase.Implementing, "the migration is not tested")); result.Refusal != nil {
			t.Fatalf("the owner's move back was refused: %+v", result.Refusal)
		}
		ownerStepAgain(t, pool, engine, root.Key, "head-2")
		told := ownerSteps(t, pool, root.Key)
		if len(told) != 2 || told[1].Role != acceptanceRole {
			t.Fatalf("the owner was told %+v, want two owner-steps of the acceptance", told)
		}
		if strings.Contains(told[0].Reason, "Last reviewed commit") {
			t.Fatalf("the first owner-step = %q, want no commit read named", told[0].Reason)
		}
		if !strings.Contains(told[1].Reason, reviewedLine("head")) {
			t.Fatalf("the second owner-step = %q, want it to say %q", told[1].Reason, reviewedLine("head"))
		}
	})

	t.Run("tester:owner", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Implementing)
		seedStepsAt(t, pool, root.Key, root.Generation, 1, nil, "implementer", "tester:owner")
		move(t, pool, engine, root.Key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
		assertWalksAt(t, pool, engine, root.Key, phase.Testing, stepPlace(2))
		fail := ownersReport(root.Key, claim.Token(treeRoleToken(t, root.Key, claim.RoleArchitect)), claim.RoleTester, 2, "test-1")
		fail.Verdict, fail.ReturnTo, fail.Commit = "fail", 1, "head"
		fail.Entry = roleRowOf(t, pool, engine, root.Key, claim.RoleTester).Entry
		if result := applyFacts(t, pool, engine)("fail", fail); result.Refusal != nil {
			t.Fatalf("the owner's test fail was refused: %+v", result.Refusal)
		}
		ownerStepAgain(t, pool, engine, root.Key, "head-2")
		told := ownerSteps(t, pool, root.Key)
		if len(told) != 2 {
			t.Fatalf("the owner was told %+v, want two owner-steps of the tester's step", told)
		}
		for _, notice := range told {
			if strings.Contains(notice.Reason, "Last reviewed commit") {
				t.Fatalf("an owner-step of the tester's step = %q, want no commit read named: the step reviews nothing", notice.Reason)
			}
		}
	})
}
