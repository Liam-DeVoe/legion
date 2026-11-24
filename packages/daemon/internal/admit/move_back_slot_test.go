package admit

import (
	"fmt"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// R3 (f) of dispatch://INFRA-4480: a move back out of a phase that keeps no admission slot
// (record.KeepsSlot) gives the new phase's worker no phase start until the issue takes a slot,
// for the two moves TestAcceptingRetroAndMergingRunWithNoSlot does not show: the architect's move
// back from accepting to testing, and the merger's move back from merging to reviewing on a pull
// request that has not merged. The cap's one row is held by another tree throughout the passes,
// so the moved issue holds no row: its start is marked for the slot (workflow.StartOrOwe reads
// record.PhaseKeepsSlot) and no lane can claim it (record.Store's outboxInOrder) over several
// admission passes. Once the row frees, the line gives it to the issue and the start is claimable.
//
// It guards today's rule, the statement The Spec proposes as
// post-review-phases-release-admission-capacity, and passes at the pull request's base. Its
// negative control is workflow.startOrOwe with its slot mark taken off these starts (NeedsSlot
// false): the start is then claimable while the issue holds no row, and the test fails at its
// first reading of the start.
func TestAMoveBackToTestingOrReviewingWaitsForTheIssuesSlot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stands phase.Phase
		status string
		to     phase.Phase
		worker claim.Role
	}{
		{name: "the architect's move from accepting to testing", stands: phase.Accepting, status: "needs_review", to: phase.Testing,
			worker: claim.RoleTester},
		{name: "the merger's move from merging to reviewing", stands: phase.Merging, status: "in_progress", to: phase.Reviewing,
			worker: claim.RoleReviewer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			admission := quiet(t, 1)
			// The cap's one row, held by a tree already at work.
			seedSlotted(t, pool, "LEGION-100", "A")

			const moved = "LEGION-400"
			slotlessRoot(t, pool, moved, tc.stands, tc.status, "E")
			seedPullRequest(t, pool, moved, 400, record.PullRequestOpen)
			fact := intake.BackwardMove{Issue: moved, Requester: claim.RoleMerger, To: tc.to,
				Reason: "the head GitHub shows is not the head the review read"}
			if tc.stands == phase.Accepting {
				by, generation, entry := rootArchitectsRead(t, pool, moved)
				fact = intake.BackwardMove{Issue: moved, Requester: claim.RoleArchitect, To: tc.to,
					Reason: "the tests do not cover what the spec asks", By: by, Generation: generation, Entry: entry}
			}
			applyFact(t, pool, admission, "moved-back", fact)

			if got := issue(t, pool, moved); got.Phase != tc.to {
				t.Fatalf("the issue moved back = %#v, want it in %s", got, tc.to)
			}
			if holdsSlot(t, pool, moved) {
				t.Fatalf("rows = %#v, want %s holding none: the cap's one row is another tree's", slots(t, pool), moved)
			}
			if start := theOneStart(t, pool, moved); start.role != tc.worker || !start.needsSlot || start.claimable {
				t.Fatalf("the start of %s = %+v, want the %s's, marked for a row and held while the issue holds none: the work goes back to a phase that keeps one",
					moved, start, tc.worker)
			}
			for pass := 1; pass <= 3; pass++ {
				tick(t, pool, admission, fmt.Sprintf("pass-%d", pass))
				if holdsSlot(t, pool, moved) || claimableStart(t, pool, moved) {
					t.Fatalf("admission pass %d: %s holds a row %t and its starts are %+v, want no row and no claimable start while the cap has none to give",
						pass, moved, holdsSlot(t, pool, moved), startsOf(t, pool, moved))
				}
			}

			closeTree(t, pool, admission, "freed", "LEGION-100", "A")

			if !holdsSlot(t, pool, moved) || !claimableStart(t, pool, moved) {
				t.Fatalf("the starts of %s = %+v on rows %#v, want the %s's task claimable on the row the line gave it",
					moved, startsOf(t, pool, moved), slots(t, pool), tc.worker)
			}
		})
	}
}
