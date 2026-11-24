package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
)

// A stop of a task before an issue's first step settles the plan that step would walk by the step's
// own rule, the pass-over of a plan only earlier owners gave first (passOverEarlierOwners). Here
// LEGION-208's architect gives version 1 for its child LEGION-209 at LEGION-209's generation,
// LEGION-209 then stands in a tree of its own, so its first step would pass that version over and
// run the default steps, and LEGION-209's own architect stops the task of the default steps.
//
// The fault it shows: the stop settles the plan only where the generation holds no plan rows
// (stopTask's `if !w.Stored`, taskStopped's `case !w.Stored && beforeItsWork`), so a generation that
// holds only an earlier owner's version is stopped on that version, and the stop of the default
// steps is refused TASK_NOT_FOUND naming the earlier owner's task.
func TestAStopBeforeTheFirstStepPassesOverAnEarlierOwnersPlan(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	recordedAt(t, pool, "LEGION-209", "LEGION-208")
	mustTake(t, "the earlier owner's version 1", applyFacts(t, pool, engine)(planID("LEGION-209", 1, 1),
		ownersPlan(t, "LEGION-209", 1, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))))
	recordedAt(t, pool, "LEGION-209", "LEGION-209")

	stop := intake.TaskStopped{Issue: "LEGION-209", Task: defaultTask, By: claim.Token(treeRoleToken(t, "LEGION-209", claim.RoleArchitect)), Generation: 1}
	if result := applyFacts(t, pool, engine)("stop", stop); result.Refusal != nil {
		t.Errorf("the owner's stop of the default steps = %+v, want it taken: the first step would pass the earlier owner's version over and run the default steps", result.Refusal)
	}
	if got := planRowsOf(t, pool, "LEGION-209"); got != [3]int{1, 1, 5} {
		t.Errorf("the plan rows of LEGION-209 after the stop = %v, want the default steps alone: one version, one task, five steps", got)
	}
}
