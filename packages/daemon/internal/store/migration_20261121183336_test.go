package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// podResumeClaimVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const podResumeClaimVersion = 20261121183336

// 20261121183336 names on each resume the claim its wake brought back (pod_resumes.claim). A
// resume the release before wrote was the wake of its role's own claim, the claim whose token the
// release before builds from the role, so the migration names that token on it: the token
// claim.NewToken gives the resume's project, issue and role, for each project of the database.
// After it the column refuses a resume that names no claim, so no reader has a resume of no claim
// to read.
func TestThePodResumeClaimMigrationNamesTheRolesOwnClaimOnEachResume(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, podResumeClaimVersion-1)
	for _, seed := range []string{
		`insert into pod_resumes (release_id, project, issue, role, kind, at) values (0, 'legion', 'LEGION-208', 'tester', 'delivery', now())`,
		`insert into pod_resumes (release_id, project, issue, role, kind, at, first_turn_at) values (0, 'legion', 'LEGION-208', 'implementer', 'delivery', now(), now())`,
		`insert into pod_resumes (release_id, project, issue, role, kind, at) values (0, 'infra', 'INFRA-5', 'security-reviewer', 'backstop', now())`,
	} {
		if _, err := store.Pool().Exec(ctx, seed); err != nil {
			t.Fatalf("record before the migration: %v", err)
		}
	}
	if _, err := store.Pool().Exec(ctx, `update pod_resumes set claim = 'x'`); err == nil || !strings.Contains(err.Error(), "claim") {
		t.Fatalf("writing claim before the migration = %v, want the column to be unknown: the fixture is not the old shape", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the claim of each resume: %v", err)
	}
	var want []string
	for _, r := range []struct{ project, issue, role string }{
		{"legion", "LEGION-208", "tester"}, {"legion", "LEGION-208", "implementer"}, {"infra", "INFRA-5", "security-reviewer"},
	} {
		token, err := claim.NewToken(r.project, r.issue, claim.Role(r.role))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r.issue+" "+r.role+" "+string(token))
	}
	if got := rowsAsText(t, store, `select issue || ' ' || role || ' ' || claim from pod_resumes order by id`); !slices.Equal(got, want) {
		t.Fatalf("the resumes after the migration = %q, want %q: each naming its role's own claim", got, want)
	}
	for _, unnamed := range []string{
		`insert into pod_resumes (release_id, project, issue, role, kind, at) values (0, 'legion', 'LEGION-208', 'reviewer', 'delivery', now())`,
		`insert into pod_resumes (release_id, project, issue, role, claim, kind, at) values (0, 'legion', 'LEGION-208', 'reviewer', '', 'delivery', now())`,
	} {
		if _, err := store.Pool().Exec(ctx, unnamed); err == nil || !strings.Contains(err.Error(), "claim") {
			t.Fatalf("%s = %v, want the resume that names no claim refused", unnamed, err)
		}
	}
}
