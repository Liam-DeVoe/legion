package outbox

import (
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/roles"
)

// TestAPRIVAndAPUBCopyOfOneRoleKeepSeparateLanes: a role route no holder takes keeps its copy on
// the lane of its claim key, (project, role): a PRIV copy and a PUB copy of one free-form role keep
// separate lanes, PRIV.reviewer and PUB.reviewer, as the listener keys its own keep by the role
// topic; a bare role keeps its bare key, and a Legion role its own. A role no claim can name is
// skipped. Control: today's key, the role's name alone, merges the two copies on one lane, so the
// row fails for the key it names.
func TestAPRIVAndAPUBCopyOfOneRoleKeepSeparateLanes(t *testing.T) {
	unclaimed := roles.Resolution{State: roles.Unclaimed}
	keptFor := func(role string) string {
		return forwardOf("TT", role, contracts.Envelope{}, unclaimed, nil).keptFor
	}
	priv := keptFor(contracts.RoleClaimKey("PRIV", "reviewer"))
	pub := keptFor(contracts.RoleClaimKey("PUB", "reviewer"))
	if priv != "PRIV.reviewer" || pub != "PUB.reviewer" {
		t.Fatalf("a PRIV and a PUB copy of reviewer keep lanes %q and %q, want PRIV.reviewer and PUB.reviewer", priv, pub)
	}
	if bare := keptFor("reviewer"); bare != "reviewer" {
		t.Errorf("a bare role's copy keeps lane %q, want reviewer, as today", bare)
	}
	if legion := keptFor("legion-infra-infra-12-architect"); legion != "legion-infra-infra-12-architect" {
		t.Errorf("a Legion role's copy keeps lane %q, want its own key", legion)
	}
	if skipped := forwardOf("TT", "Not A Role", contracts.Envelope{}, unclaimed, nil); skipped.keptFor != "" || skipped.skip == "" {
		t.Errorf("a role no claim can name was kept for %q, want it skipped", skipped.keptFor)
	}
	// Control: the role's name alone is one lane for both copies.
	_, privName := contracts.ParseRoleClaimKey(contracts.RoleClaimKey("PRIV", "reviewer"))
	_, pubName := contracts.ParseRoleClaimKey(contracts.RoleClaimKey("PUB", "reviewer"))
	if privName != pubName {
		t.Fatalf("control: the two copies' names are %q and %q, want one name, the merge the key above prevents", privName, pubName)
	}
}
