package record

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// The one rule of the owner's place: the place where an issue's task stands is its owner's own to
// work where the claim that works it is the architect (PlaceRole), and, with no plan rows, in
// accepting alone. An acceptance a fresh agent of acceptance does is not the owner's, so it is a
// worker's work (AtWork), as every other step a worker does is, and an acceptance the owner does
// is a wait for the architect, as accepting with no plan rows always was. The control is a rule
// that reads the phase alone: the fresh agent's acceptance reads as the owner's and as no work.
func TestThePlaceOfAnIssueIsTheOwnersWhereTheArchitectWorksIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issue  Issue
		owners bool
		atWork bool
	}{
		{"accepting with no plan rows", Issue{Key: "LEGION-1", Tree: "LEGION-1", Phase: phase.Accepting}, true, false},
		{"acceptance:owner", Issue{Key: "LEGION-1", Tree: "LEGION-1", Phase: phase.Accepting, StepRole: "acceptance", PlaceRole: claim.RoleArchitect}, true, false},
		{"acceptance with no key", Issue{Key: "LEGION-1", Tree: "LEGION-1", Phase: phase.Accepting, StepRole: "acceptance", PlaceRole: "acceptance"}, false, true},
		{"implementer:owner", Issue{Key: "LEGION-1", Tree: "LEGION-1", Phase: phase.Implementing, StepRole: claim.RoleImplementer, PlaceRole: claim.RoleArchitect}, true, true},
		{"the owner's retro", Issue{Key: "LEGION-1", Tree: "LEGION-1", Phase: phase.Retro, PlaceRole: claim.RoleArchitect}, true, true},
		{"implementer", Issue{Key: "LEGION-1", Tree: "LEGION-1", Phase: phase.Implementing, StepRole: claim.RoleImplementer, PlaceRole: claim.RoleImplementer}, false, true},
		{"admitted", Issue{Key: "LEGION-2", Tree: "LEGION-1", Phase: phase.Admitted}, false, false},
		{"held from the owner's acceptance", Issue{Key: "LEGION-1", Tree: "LEGION-1", Phase: phase.Held, PlaceRole: claim.RoleArchitect,
			Hold: &Hold{From: phase.Accepting}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.issue.OwnersPlace(); got != tc.owners {
				t.Errorf("OwnersPlace = %t, want %t", got, tc.owners)
			}
			if got := AtWork(tc.issue, TreeGate{}); got != tc.atWork {
				t.Errorf("AtWork = %t, want %t", got, tc.atWork)
			}
		})
	}
}
