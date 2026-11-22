package record

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// The controller's notices R5 and R3 add are its alone, and each is keyed as its rule says: the
// notice that Envoy lists nobody speaks for the project, and the lock-out notice for one claim.
func TestTheReachNoticesAreTheControllersAndKeyedAsTheirRuleSays(t *testing.T) {
	for kind, perClaim := range map[NoticeKind]bool{
		EnvoyNoLiveSessionNotice: false,
		WorkerLockedOutNotice:    true,
	} {
		if !controllerOnlyNoticeKind(kind) {
			t.Errorf("%s is not the controller's alone", kind)
		}
		if validNoticeKind(kind) {
			t.Errorf("%s is an architect's notice kind too", kind)
		}
		if got := NoticePerClaim(kind); got != perClaim {
			t.Errorf("NoticePerClaim(%s) = %t, want %t", kind, got, perClaim)
		}
		if !reachNoticeKind(kind) {
			t.Errorf("%s is not queued under a kind of its own", kind)
		}
	}
}

// The tree's architect is told of each unreachable claim of its tree but its own, so that kind is
// an architect's notice and not the controller's.
func TestTheArchitectsUnreachableNoticeIsAnArchitectsKind(t *testing.T) {
	if !validNoticeKind(ClaimOutOfReachNotice) {
		t.Errorf("%s is not an architect's notice kind", ClaimOutOfReachNotice)
	}
	if controllerOnlyNoticeKind(ClaimOutOfReachNotice) {
		t.Errorf("%s is the controller's alone; the tree's architect is told of its own tree's claims", ClaimOutOfReachNotice)
	}
}

// The claim token rides on the kinds that speak for one claim and on no other, as every other
// field of a notice does.
func TestOnlyTheClaimNoticesCarryAClaim(t *testing.T) {
	token := "legion-legion-209-implementer"
	for _, tc := range []struct {
		name    string
		payload OutboxPayload
		refused string
	}{
		{name: "the architect's notice names its claim",
			payload: Notice{Kind: ClaimOutOfReachNotice, Role: claim.RoleImplementer, Claim: token, Reason: "r"}},
		{name: "the lock-out notice names its claim",
			payload: ControllerNotice{Kind: WorkerLockedOutNotice, Role: claim.RoleImplementer, Claim: token, Reason: "r"}},
		{name: "the no-live notice names none",
			payload: ControllerNotice{Kind: EnvoyNoLiveSessionNotice, Reason: "r"}},
		{name: "the architect's notice without its claim",
			payload: Notice{Kind: ClaimOutOfReachNotice, Role: claim.RoleImplementer, Reason: "r"}, refused: "names the claim"},
		{name: "the lock-out notice without its claim",
			payload: ControllerNotice{Kind: WorkerLockedOutNotice, Role: claim.RoleImplementer, Reason: "r"}, refused: "names the claim"},
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
