package workflow

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
)

// A phase reminder is armed in the claim that works the issue's place (pods.IdleView.PlaceRole),
// serving its generation: at a retro the coder does, the coder's claim serving generation 3 and
// not the implementer's, nor the coder's serving another; at a step the owner does, no worker's
// claim, since the architect's own turn is no phase worker's. A view whose place role is unset (an
// issue with no plan rows) keeps the role of its phase. The control is the rule of the step's role
// and the phase alone, which arms the implementer's claim at both places and never the coder's.
func TestAPhaseReminderIsArmedForTheClaimThatWorksThePlace(t *testing.T) {
	retro := pods.IdleView{Phase: phase.Retro, PlaceRole: "coder", Generation: 3}
	owners := pods.IdleView{Phase: phase.Implementing, PlaceRole: claim.RoleArchitect, Generation: 3}
	bare := pods.IdleView{Phase: phase.Implementing, Generation: 3}
	for _, tc := range []struct {
		name  string
		view  pods.IdleView
		claim pods.IdleClaim
		want  bool
	}{
		{"the coder at its retro", retro, pods.IdleClaim{Role: "coder", ServingGeneration: 3}, true},
		{"the coder at its retro, serving another generation", retro, pods.IdleClaim{Role: "coder", ServingGeneration: 2}, false},
		{"the implementer at the coder's retro", retro, pods.IdleClaim{Role: claim.RoleImplementer, ServingGeneration: 3}, false},
		{"the implementer at the owner's step", owners, pods.IdleClaim{Role: claim.RoleImplementer, ServingGeneration: 3}, false},
		{"the implementer of an issue with no plan rows", bare, pods.IdleClaim{Role: claim.RoleImplementer, ServingGeneration: 3}, true},
	} {
		if got := idlePhaseOpen(tc.view, tc.claim); got != tc.want {
			t.Errorf("%s: idlePhaseOpen = %v, want %v", tc.name, got, tc.want)
		}
	}
}
