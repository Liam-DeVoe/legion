package store

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// Reviewer repro for cybermonklabs/legion#4199 at 2af78f3e62 (not part of the pull request): an
// account outside the Workspace signs in to an address no people row holds yet, which the address
// rule allows, and a personal token is minted under that address; then the address's Workspace
// member signs in for the first time and reclaims it. The test fails while the outsider's token
// survives the reclaim with its owner's row reading full.
func TestRev4199OutsiderTokenSurvivesTheMembersReclaim(t *testing.T) {
	ctx := context.Background()
	database := migratedEmptyStore(t)
	people := NewPgPeopleStore(database.Pool, testSigningKey, nil)
	const email = "alice@second.example"
	if err := people.SignInSubject(ctx, email, "consumer-sub", "refresh-outsider", model.MemberRestricted); err != nil {
		t.Fatalf("the outsider's sign-in to an address no row holds: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `insert into agent_tokens (owner, name, token_hash, prefix) values ($1, 'outsider', sha256('dsp_repro'::bytea), 'repro')`, email); err != nil {
		t.Fatal(err)
	}
	if err := people.SignInSubject(ctx, email, "workspace-sub", "refresh-member", model.MemberFull); err != nil {
		t.Fatalf("the member's Workspace sign-in: %v", err)
	}
	var kind, sub string
	var revoked bool
	if err := database.Pool.QueryRow(ctx, `select p.member_kind, coalesce(p.sub, ''), t.revoked_at is not null from agent_tokens t join people p on p.email = t.owner where t.owner = $1`, email).Scan(&kind, &sub, &revoked); err != nil {
		t.Fatal(err)
	}
	t.Logf("after the member's reclaim: owner row member_kind=%s sub=%s, the outsider's token revoked=%v", kind, sub, revoked)
	if kind == string(model.MemberFull) && !revoked {
		t.Errorf("REPRO: the token minted while an outsider held the address survives the member's reclaim, and its owner now reads full")
	}
}
