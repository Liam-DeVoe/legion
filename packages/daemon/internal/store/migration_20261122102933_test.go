package store

import (
	"context"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// releaseSuspendsClaimsVersion is the version of the migration this file tests, so the test can
// migrate through everything before it whatever lands beside it.
const releaseSuspendsClaimsVersion = 20261122102933

// 20261122102933 makes a release's mark name the claims it suspended by their tokens. A mark in
// flight names roles: it is rewritten to the token of every claim of the issue that holds one of
// them, which is the set the release before matched, so three claims of the implementer's role all
// stand in it and the architect, whose role it does not name, does not. pod_releases gains
// `claims`, at its default on every record it finds. The way back the header gives writes the mark
// back as the role each token names, its last part, each once: two of those claims are on the
// implementer's role only because this release moved them to the step they work
// (supervise.Machine.Rerole writes claims.role), and the release before finds a claim by its
// token's role, so each comes back as that role, the one whose name holds dashes among them. It
// then drops the column and deletes its version row, so the roll-forward runs the migration again.
func TestTheReleaseSuspendsClaimsMigrationNamesAMarksClaimsByTheirTokens(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, releaseSuspendsClaimsVersion-1)
	for _, c := range []struct {
		token claim.Token
		role  claim.Role
	}{
		{"legion-legion-legion-208-implementer", claim.RoleImplementer},
		// The agents of two keys on the implementer's step, beside the role's own claim: one named
		// for a role the deployment defines.
		{"legion-legion-legion-208-security-reviewer", claim.RoleImplementer},
		{"legion-legion-legion-208-tester", claim.RoleImplementer},
		{"legion-legion-legion-208-architect", claim.RoleArchitect},
	} {
		if err := store.PutClaim(ctx, supervise.Claim{Token: c.token, Project: "legion", Tree: "LEGION-208", Issue: "LEGION-208",
			Role: c.role, TreeEpoch: 1, Generation: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByReleased}); err != nil {
			t.Fatalf("seed the claim %s: %v", c.token, err)
		}
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := store.Pool().Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`insert into issue_releases (project, issue, generation, first_row, boundary_row, last_row, started_row, role, suspended, marked_at)
		values ('legion', 'LEGION-208', 1, 18, 20, 22, 0, 'implementer', '{implementer}', now())`)
	exec(`insert into pod_releases (project, issue, generation, roles, at) values ('legion', 'LEGION-208', 1, 'implementer', now())`)

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the release's claims: %v", err)
	}
	mark := func() []string {
		t.Helper()
		var suspended []string
		if err := store.Pool().QueryRow(ctx, `select suspended from issue_releases where issue = 'LEGION-208'`).Scan(&suspended); err != nil {
			t.Fatalf("read the mark: %v", err)
		}
		return suspended
	}
	moved := []string{"legion-legion-legion-208-implementer", "legion-legion-legion-208-security-reviewer", "legion-legion-legion-208-tester"}
	if got, want := mark(), moved; !slices.Equal(got, want) {
		t.Fatalf("the mark in flight after the migration = %v, want %v: each claim of the role it named, by its token", got, want)
	}
	if got := rowsAsText(t, store, `select roles || '|' || claims from pod_releases order by id`); !slices.Equal(got, []string{"implementer|"}) {
		t.Fatalf("the records after the migration = %q, want the one naming its role and no claim", got)
	}
	exec(`insert into pod_releases (project, issue, generation, roles, claims, at)
		values ('legion', 'LEGION-208', 1, 'implementer', 'legion-legion-legion-208-implementer,legion-legion-legion-208-tester', now())`)

	if _, err := store.Pool().Exec(ctx, wayBackOf(t, releaseSuspendsClaimsVersion, "the release's claims")); err != nil {
		t.Fatalf("run the way back: %v", err)
	}
	if got, want := mark(), []string{"implementer", "security-reviewer", "tester"}; !slices.Equal(got, want) {
		t.Fatalf("the mark after the way back = %v, want %v: the role each token names, each once, as the release before reads it", got, want)
	}
	var column int
	if err := store.Pool().QueryRow(ctx, `select count(*) from information_schema.columns
		where table_name = 'pod_releases' and column_name = 'claims'`).Scan(&column); err != nil || column != 0 {
		t.Fatalf("pod_releases.claims after the way back = %d columns, %v; want none", column, err)
	}
	var version int
	if err := store.Pool().QueryRow(ctx, `select count(*) from schema_version where version = $1`, releaseSuspendsClaimsVersion).Scan(&version); err != nil || version != 0 {
		t.Fatalf("schema_version rows of %d after the way back = %d, %v; want none", releaseSuspendsClaimsVersion, version, err)
	}
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("roll forward after the way back: %v", err)
	}
	if got, want := mark(), moved; !slices.Equal(got, want) {
		t.Fatalf("the mark after the roll-forward = %v, want %v", got, want)
	}
}
