package workflow

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// ownerSteps is every owner-step notice key's outbox holds, oldest first.
func ownerSteps(t *testing.T, pool *pgxpool.Pool, key string) []record.Notice {
	t.Helper()
	return noticesOf(t, pool, key, record.OwnerStepNotice)
}

// assertNoClaimStarts fails when key's outbox holds a start of any claim: an owner's step is told
// to the owner, never started as a worker.
func assertNoClaimStarts(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	if starts := superviseRowsOf(t, pool, key, "start"); len(starts) != 0 {
		t.Fatalf("%s's outbox holds the starts %+v, want none: the owner's step starts no claim", key, starts)
	}
}

// stepStartsAt is every start of the run of key's step of kind at place.
func stepStartsAt(t *testing.T, pool *pgxpool.Pool, key, kind string, place int) []record.StepRecord {
	t.Helper()
	var starts []record.StepRecord
	for _, write := range writesAt(stepWrites(t, pool, key), kind, place) {
		if isStart(write) {
			starts = append(starts, write)
		}
	}
	return starts
}

// A step or a retro whose agent is the owner of its task is told to the owner, the architect that
// owns the issue (here the root's own), as one notice owner-step that carries the step's task, from
// each path that starts a step: a transition (the gate's opening into `implementer:owner`), a task
// started again after its stop (restartTask), the retro of a parent held for its child
// (startHeldRetro) and the move into the owner's acceptance of the default steps. Each writes the
// run's one start record under the owner's claim and the entry on the step's phases row, and
// starts no claim. An issue with no plan rows at the move into accepting writes today's start record
// under the architect alone, raises no entry and tells no owner-step: the reviewer's phase-finished
// tells it, as today.
//
// The controls: start as it was (the caller's role decides; the architect starts nothing) starts the
// implementer and tells no owner-step in the first three paths, and writes no start record at the
// two moves into accepting; the owner's branch taken only where the caller passes the architect
// starts the implementer at the first and a claim of acceptance at the fourth.
func TestAnOwnerStepIsToldToTheOwnerFromEachStartPath(t *testing.T) {
	owner := treeRoleToken(t, "LEGION-208", claim.RoleArchitect)

	t.Run("a transition", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:owner", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})

		notices := ownerSteps(t, pool, root.Key)
		if len(notices) != 1 || notices[0].Role != claim.RoleImplementer || notices[0].Phase != phase.Implementing {
			t.Fatalf("the gate's opening told %+v, want one owner-step of the implementer's step in implementing", notices)
		}
		for _, says := range []string{" Task: a.", " Step 1 of 2: implementer:owner.", " Task text: "} {
			if !strings.Contains(notices[0].Reason, says) {
				t.Fatalf("the owner-step's task = %q, want it to say %q", notices[0].Reason, says)
			}
		}
		assertNoClaimStarts(t, pool, root.Key)
		if starts := stepStartsAt(t, pool, root.Key, "step", 1); len(starts) != 1 || starts[0].Agent != owner {
			t.Fatalf("the start records of step 1 = %+v, want one naming the owner %s", starts, owner)
		}
		if row := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer); row.Claim != claim.Token(owner) || row.Entry != 1 || row.StepPlace != 1 {
			t.Fatalf("the implementer's row = %+v, want the owner's claim at entry 1 of step 1", row)
		}
	})

	t.Run("a task started again after its stop", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:owner", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
		if result := applyFacts(t, pool, engine)("stop", ownersStop(t, root, "a")); result.Refusal != nil {
			t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
		}
		if result := applyFacts(t, pool, engine)("todo", personsTodo(root.Key)); result.Refusal != nil {
			t.Fatalf("the todo was refused: %+v", result.Refusal)
		}

		if notices := ownerSteps(t, pool, root.Key); len(notices) != 2 || notices[1].Role != claim.RoleImplementer {
			t.Fatalf("the restart told %+v, want a second owner-step of step 1", notices)
		}
		if starts := stepStartsAt(t, pool, root.Key, "step", 1); len(starts) != 2 || starts[1].Agent != owner {
			t.Fatalf("the start records of step 1 = %+v, want two, the second naming the owner", starts)
		}
		if starts := roleStartsOf(t, pool, root.Key, claim.RoleImplementer); len(starts) != 0 {
			t.Fatalf("the restart wrote the implementer's starts %+v, want none", starts)
		}
	})

	t.Run("the retro of a parent held for its child", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Testing)
		stepChild(t, pool, heldParentChild, phase.Implementing, nil)
		seedStepsAt(t, pool, root.Key, root.Generation, 1, nil, "tester")
		seedPhase(t, pool, record.PhaseRow{Issue: root.Key, Role: claim.RoleTester, Claim: "tester-claim", HandoffCommit: stepHead, Verdict: "pass",
			StepTask: "a", StepPlace: 1})
		move(t, pool, engine, root.Key, TriggerTesterPassed, record.TaskPlace{}, record.PhaseRow{})
		if held := issueOf(t, pool, engine, root.Key); !record.HeldForChildren(held) {
			t.Fatalf("%s after the tester's pass stands in %s with hold %+v, want held before its retro", root.Key, held.Phase, held.Hold)
		}
		childLeavesDone(t, pool, engine)

		notices := ownerSteps(t, pool, root.Key)
		if len(notices) != 1 || notices[0].Role != "" || notices[0].Phase != phase.Retro {
			t.Fatalf("the child's leave told %+v, want one owner-step of the retro", notices)
		}
		if !strings.Contains(notices[0].Reason, " Task: a.") || strings.Contains(notices[0].Reason, " Step ") {
			t.Fatalf("the retro's owner-step task = %q, want it to name task a and no step", notices[0].Reason)
		}
		if row := roleRowOf(t, pool, engine, root.Key, claim.RoleImplementer); row.Claim != claim.Token(owner) || row.StepPlace != 0 || row.Entry == 0 {
			t.Fatalf("the implementer's row = %+v, want the owner's retro on it at place 0", row)
		}
		if starts := roleStartsOf(t, pool, root.Key, claim.RoleImplementer); len(starts) != 0 {
			t.Fatalf("the retro wrote the implementer's starts %+v, want none: the owner does it", starts)
		}
	})

	t.Run("the owner's acceptance of the default steps", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Reviewing)
		seedStepsAt(t, pool, root.Key, root.Generation, 3, nil, "implementer", "tester", "reviewer", "acceptance:owner")
		move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})

		if notices := ownerSteps(t, pool, root.Key); len(notices) != 1 || notices[0].Role != "acceptance" || notices[0].Phase != phase.Accepting {
			t.Fatalf("the approval told %+v, want one owner-step of the acceptance", notices)
		}
		if starts := stepStartsAt(t, pool, root.Key, "step", 4); len(starts) != 1 || starts[0].Agent != owner {
			t.Fatalf("the start records of step 4 = %+v, want one naming the owner", starts)
		}
		if starts := roleStartsOf(t, pool, root.Key, "acceptance"); len(starts) != 0 {
			t.Fatalf("the approval wrote starts of the role acceptance %+v, want none", starts)
		}
	})

	t.Run("an issue with no plan rows at accepting", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Reviewing)
		move(t, pool, engine, root.Key, TriggerReviewApproved, record.TaskPlace{}, record.PhaseRow{})

		if notices := ownerSteps(t, pool, root.Key); len(notices) != 0 {
			t.Fatalf("the approval told %+v, want no owner-step on an issue with no plan rows", notices)
		}
		if starts := stepStartsAt(t, pool, root.Key, "step", 5); len(starts) != 1 || starts[0].Agent != owner {
			t.Fatalf("the start records of step 5 = %+v, want one naming the architect %s", starts, owner)
		}
		if row := roleRowOf(t, pool, engine, root.Key, claim.RoleArchitect); row.Entry != 0 {
			t.Fatalf("the architect's row = %+v, want no entry raised", row)
		}
		if notices := noticesOf(t, pool, root.Key, "phase-finished"); len(notices) != 1 {
			t.Fatalf("the approval told %d phase-finished notices, want the reviewer's one", len(notices))
		}
	})
}
