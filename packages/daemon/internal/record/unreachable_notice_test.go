package record

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// The tree's architect is told of each unreachable claim of its tree but its own, so that kind is
// an architect's notice and not the controller's, queued under a kind of its own; the controller
// is told of no claim out of reach, and a person reads R6's line instead.
func TestTheArchitectsUnreachableNoticeIsAnArchitectsKind(t *testing.T) {
	if !validNoticeKind(ClaimOutOfReachNotice) {
		t.Errorf("%s is not an architect's notice kind", ClaimOutOfReachNotice)
	}
	if controllerOnlyNoticeKind(ClaimOutOfReachNotice) {
		t.Errorf("%s is the controller's alone; the tree's architect is told of its own tree's claims", ClaimOutOfReachNotice)
	}
	if !reachNoticeKind(ClaimOutOfReachNotice) {
		t.Errorf("%s is not queued under a kind of its own", ClaimOutOfReachNotice)
	}
}

// The claim token rides on the kind that speaks for one claim and on no other, as every other
// field of a notice does: a controller notice that names a claim is refused.
func TestOnlyTheClaimNoticesCarryAClaim(t *testing.T) {
	token := "legion-legion-209-implementer"
	for _, tc := range []struct {
		name    string
		payload OutboxPayload
		refused string
	}{
		{name: "the architect's notice names its claim",
			payload: Notice{Kind: ClaimOutOfReachNotice, Role: claim.RoleImplementer, Claim: token, Reason: "r"}},
		{name: "the architect's notice without its claim",
			payload: Notice{Kind: ClaimOutOfReachNotice, Role: claim.RoleImplementer, Reason: "r"}, refused: "names the claim"},
		{name: "a tick that names a claim",
			payload: ControllerNotice{Kind: TickNotice, Claim: token}, refused: "names a claim"},
	} {
		err := validateOutboxPayload("LEGION-209", tc.payload)
		switch {
		case tc.refused == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)):
			t.Errorf("%s: err = %v, want one that %s", tc.name, err, tc.refused)
		}
	}
}
