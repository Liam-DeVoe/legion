package store

import (
	"context"
	"testing"
)

// podResumeRepairVersion is the version of the migration this file tests.
const podResumeRepairVersion = 20261123114449

// A database can have run 20261121183336 in either of the shapes it had before it held one open
// resume per claim: alone, naming each resume's claim and keeping one open resume per role, or
// beside 20261122074350, which then made the open resume one per claim and is no migration of this
// release. 20261123114449 brings either to the shape 20261121183336 now gives: the one open resume
// per role gone, one open resume per claim, and no version row of 20261122074350, whose file no
// release carries. Its way back deletes its own version row alone, and the roll-forward runs it
// again, which changes nothing.
func TestTheOpenResumeRepairGivesADatabaseOfAnEarlierShapeOneOpenResumePerClaim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		earlier []string
	}{
		{name: "20261121183336 naming each resume's claim alone", earlier: []string{
			`alter table pod_resumes add column claim text not null default ''`,
			`update pod_resumes set claim = 'legion-' || project || '-' || lower(issue) || '-' || role`,
			`alter table pod_resumes alter column claim drop default, add constraint pod_resumes_claim_check check (claim <> '')`,
			`insert into schema_version (version) values (20261121183336)`,
		}},
		{name: "20261121183336 and 20261122074350", earlier: []string{
			`alter table pod_resumes add column claim text not null default ''`,
			`update pod_resumes set claim = 'legion-' || project || '-' || lower(issue) || '-' || role`,
			`alter table pod_resumes alter column claim drop default, add constraint pod_resumes_claim_check check (claim <> '')`,
			`insert into schema_version (version) values (20261121183336)`,
			`drop index pod_resumes_one_open_per_role`,
			`create unique index pod_resumes_one_open_per_claim on pod_resumes (project, issue, claim) where first_turn_at is null and closed_at is null`,
			`insert into schema_version (version) values (20261122074350)`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := emptyStore(t)
			migrateThrough(t, store, podResumeClaimVersion-1)
			exec := func(sql string) int64 {
				t.Helper()
				tag, err := store.Pool().Exec(ctx, sql)
				if err != nil {
					t.Fatalf("%s: %v", sql, err)
				}
				return tag.RowsAffected()
			}
			for _, statement := range tc.earlier {
				exec(statement)
			}
			exec(`insert into pod_resumes (release_id, project, issue, role, claim, kind, at)
				values (0, 'legion', 'LEGION-208', 'tester', 'legion-legion-legion-208-tester', 'delivery', now())`)

			if _, err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate the database of the earlier shape: %v", err)
			}
			resume := func(token string) string {
				return `insert into pod_resumes (release_id, project, issue, role, claim, kind, at)
					values (0, 'legion', 'LEGION-208', 'tester', '` + token + `', 'delivery', now()) on conflict do nothing`
			}
			if n := exec(resume("legion-legion-legion-208-implementer")); n != 1 {
				t.Fatalf("an open resume of key a's claim on the tester's role, beside the tester's own = %d rows, want 1: one open resume per claim", n)
			}
			if n := exec(resume("legion-legion-legion-208-tester")); n != 0 {
				t.Fatalf("a second open resume of the tester's claim = %d rows, want none: one open resume per claim", n)
			}
			versions := func() []string {
				return rowsAsText(t, store, `select version::text from schema_version where version in (20261122074350, 20261123114449) order by version`)
			}
			if got := versions(); len(got) != 1 || got[0] != "20261123114449" {
				t.Fatalf("the version rows of 20261122074350 and the repair are %q, want the repair's alone", got)
			}

			exec(wayBackOf(t, podResumeRepairVersion, "the repair of the open resume per claim"))
			if got := versions(); len(got) != 0 {
				t.Fatalf("the version rows after the way back are %q, want none", got)
			}
			if _, err := store.Migrate(ctx); err != nil {
				t.Fatalf("roll forward after the way back: %v", err)
			}
			if got := versions(); len(got) != 1 || got[0] != "20261123114449" {
				t.Fatalf("the version rows after the roll-forward are %q, want the repair's alone", got)
			}
			if n := exec(resume("legion-legion-legion-208-reviewer")); n != 1 {
				t.Fatalf("an open resume of a third claim on the tester's role after the roll-forward = %d rows, want 1", n)
			}
		})
	}
}
