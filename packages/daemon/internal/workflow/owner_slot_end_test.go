package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The end of a step its owner ran gives the owner's slot its own App back at once: on
// `implementer:owner tester:owner`, the gate's opening, which ends no step, queues no supervise
// slot_credential, and the owner's report of step 1 ends the owner's implementer step and queues
// one, of the owner's claim, the architect's role, in the issue's tree, though the owner's next
// step starts in the same move: the row writes the slot for the App the record answers when it
// runs.
func TestTheEndOfAnOwnersStepGivesItsSlotItsOwnApp(t *testing.T) {
	owner := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleArchitect))
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	seedStepsPlan(t, pool, root.Key, root.Generation, "implementer:owner", "tester:owner")
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	written := func(t *testing.T, want int) {
		t.Helper()
		rows := superviseRowsOf(t, pool, root.Key, "slot_credential")
		if len(rows) != want {
			t.Fatalf("%s's outbox holds the slot_credential rows %+v, want %d", root.Key, rows, want)
		}
		for _, row := range rows {
			if row.Claim != owner || row.Role != claim.RoleArchitect || row.Tree != root.Key || row.Generation != root.Generation {
				t.Fatalf("the slot_credential row %+v, want one of the owner %s, the architect, in the tree %s at generation %d", row, owner, root.Key, root.Generation)
			}
		}
	}
	written(t, 0)

	apply := applyFacts(t, pool, engine)
	if result := apply("step-1", ownersReport(root.Key, owner, claim.RoleImplementer, 1, "implement-LEGION-208")); result.Refusal != nil {
		t.Fatalf("the owner's report of step 1 was refused: %+v", result.Refusal)
	}
	assertWalksAt(t, pool, engine, root.Key, phase.Testing, stepPlace(2))
	written(t, 1)
}
