package store

import (
	"context"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// formerHeadsInstall is the version of the migration that makes pull_request_former_heads: the
// install of the record of the heads each open pull request left.
func formerHeadsInstall(t *testing.T) int {
	t.Helper()
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		digits, name, _ := strings.Cut(entry.Name(), "_")
		if name != "pull_request_former_heads.up.sql" {
			continue
		}
		version, err := strconv.Atoi(digits)
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		return version
	}
	t.Fatal("no migration makes pull_request_former_heads")
	return 0
}

// TestTheFormerHeadsInstallSeedsTheHeadsAResultWasRecordedAt: before the install, CORE-1's open
// pull request #70 of acme/widgets left 1111… and 2222… and is at 3333…; a tester recorded a result
// of CORE-1 at 1111… and at 3333…, and a CI job one at 2222…. CORE-2's open #71 is at 5555…, and
// CORE-1's closed #72 at 6666…. No trigger saw #70 leave a head, so the install seeds 1111… and
// 2222… as #70's candidates, unverified, and nothing for #71, #72 or the head 3333…, and marks
// CORE-1 owed, so the settler confirms them against #70's commits and publishes their states.
func TestTheFormerHeadsInstallSeedsTheHeadsAResultWasRecordedAt(t *testing.T) {
	const h1, h2, h3, h5, h6 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333", "5555555555555555555555555555555555555555", "6666666666666666666666666666666666666666"
	ctx := context.Background()
	store := openEmptyTestStore(t)
	install := formerHeadsInstall(t)
	if err := store.migrate(ctx, migrationsThrough(t, install-1)); err != nil {
		t.Fatalf("migrate through %d: %v", install-1, err)
	}
	var document string
	if err := store.Pool.QueryRow(ctx, `
		with project as (
			insert into projects (key, name, default_assignee) values ('CORE', 'Core', 'alice@d.example') returning key
		), issues as (
			insert into issues (key, project_key, number, title, created_by, rank)
			select 'CORE-' || n, key, n, 'Issue ' || n, '{"kind":"user","id":"alice"}', 'U' || n
			from project, generate_series(1, 2) n returning key
		)
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, role, created_by)
		select 'CORE-1', 'CORE', 'verification-md', 'verification.md', 'doc', false, 'verification', '{"kind":"user","id":"alice"}'
		from (select count(*) from issues) made
		returning id::text
	`).Scan(&document); err != nil {
		t.Fatalf("seed CORE-1, CORE-2 and CORE-1's Verification document: %v", err)
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`insert into pull_request_heads (repo, number, head_sha, state, issue_key) values
			('acme/widgets', 70, $1, 'open', 'CORE-1'), ('acme/widgets', 71, $2, 'open', 'CORE-2'),
			('acme/widgets', 72, $3, 'closed', 'CORE-1')`, []any{h3, h5, h6}},
		{`insert into check_results (issue_key, check_issue_key, check_artifact_id, check_block_id, check_version,
			check_sha256, role, commit_sha, verdict, actor)
			select 'CORE-1', 'CORE-1', $1::uuid, 'k1', 1, 'sha', 'tester', commit, 'pass', '{"kind":"user","id":"alice"}'
			from unnest(array[$2, $3]::text[]) commit`, []any{document, h1, h3}},
		{`insert into ci_check_results (issue_key, check_issue_key, check_artifact_id, check_block_id, check_version,
			check_sha256, run, commit_sha, branch, verdict, attestation, session, actor)
			values ('CORE-1', 'CORE-1', $1::uuid, 'k3', 1, 'sha', 'run-1', $2, 'legion/core-1', 'pass', '{}', 'job',
				'{"kind":"system","id":"ci"}')`, []any{document, h2}},
		// The marks the triggers made for these writes; the install's own are what is read below.
		{`delete from check_states_owed`, nil},
	} {
		if _, err := store.Pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed the database before the install: %s: %v", statement.sql, err)
		}
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate over the seeded database: %v", err)
	}
	rows, err := store.Pool.Query(ctx, `select repo || '#' || number || '@' || left(head_sha, 4) from pull_request_former_heads order by 1`)
	if err != nil {
		t.Fatalf("read the former heads: %v", err)
	}
	recorded, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the former heads: %v", err)
	}
	if want := []string{"acme/widgets#70@1111", "acme/widgets#70@2222"}; !slices.Equal(recorded, want) {
		t.Fatalf("the install recorded the former heads %v, want %v: the heads #70 left before it, at which a result of its issue was recorded", recorded, want)
	}
	var unverified int
	if err := store.Pool.QueryRow(ctx, `select count(*) from pull_request_former_heads where not verified`).Scan(&unverified); err != nil {
		t.Fatalf("count the unverified former heads: %v", err)
	}
	if unverified != 2 {
		t.Fatalf("%d of the seeded former heads are unverified, want both: a result on the issue does not show the commit was #70's", unverified)
	}
	rows, err = store.Pool.Query(ctx, `select distinct issue_key from check_states_owed order by 1`)
	if err != nil {
		t.Fatalf("read the owed issues: %v", err)
	}
	owed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the owed issues: %v", err)
	}
	if want := []string{"CORE-1"}; !slices.Equal(owed, want) {
		t.Fatalf("the install marked %v owed, want %v: the issue whose pull request holds a candidate", owed, want)
	}
}

// Commits of the tests below: heads of acme/widgets#70 and #71, and commits recorded at.
const (
	installH1 = "1111111111111111111111111111111111111111"
	installH2 = "2222222222222222222222222222222222222222"
	installH3 = "3333333333333333333333333333333333333333"
	installH5 = "5555555555555555555555555555555555555555"
)

// installFixture migrates store through the version before the install, and writes project CORE,
// its issues CORE-1, CORE-2 and CORE-3, the Verification documents of CORE-1 and CORE-2, and the
// open pull requests of acme/widgets #70 at 3333…, CORE-1's, and #71 at 5555…, CORE-2's. It
// answers each document's id by its issue, and leaves no mark owed.
func installFixture(t *testing.T, store *Store) map[string]string {
	t.Helper()
	ctx := context.Background()
	if err := store.migrate(ctx, migrationsThrough(t, formerHeadsInstall(t)-1)); err != nil {
		t.Fatalf("migrate through the version before the install: %v", err)
	}
	rows, err := store.Pool.Query(ctx, `
		with project as (
			insert into projects (key, name, default_assignee) values ('CORE', 'Core', 'alice@d.example') returning key
		), issues as (
			insert into issues (key, project_key, number, title, created_by, rank)
			select 'CORE-' || n, key, n, 'Issue ' || n, '{"kind":"user","id":"alice"}', 'U' || n
			from project, generate_series(1, 3) n returning key
		)
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, role, created_by)
		select issue, 'CORE', 'verification-md', 'verification.md', 'doc', false, 'verification', '{"kind":"user","id":"alice"}'
		from unnest(array['CORE-1', 'CORE-2']) issue, (select count(*) from issues) made
		returning issue_key, id::text`)
	if err != nil {
		t.Fatalf("seed CORE-1, CORE-2, CORE-3 and the Verification documents of the first two: %v", err)
	}
	documents := map[string]string{}
	for rows.Next() {
		var issue, id string
		if err := rows.Scan(&issue, &id); err != nil {
			t.Fatalf("read the Verification documents: %v", err)
		}
		documents[issue] = id
	}
	if err := rows.Err(); err != nil || len(documents) != 2 {
		t.Fatalf("seed the Verification documents: %v (%d made)", err, len(documents))
	}
	if _, err := store.Pool.Exec(ctx, `
		insert into pull_request_heads (repo, number, head_sha, state, issue_key) values
			('acme/widgets', 70, $1, 'open', 'CORE-1'), ('acme/widgets', 71, $2, 'open', 'CORE-2')`, installH3, installH5); err != nil {
		t.Fatalf("seed the open pull requests: %v", err)
	}
	if _, err := store.Pool.Exec(ctx, `delete from check_states_owed`); err != nil {
		t.Fatalf("clear the marks the seed's writes made: %v", err)
	}
	return documents
}

// installResult is the statement that records one result at $4 on issue $1 against a check of
// the Verification document $3 of issue $2: a tester's, in check_results, or a CI job's, in
// ci_check_results.
func installResult(table string) string {
	if table == "ci_check_results" {
		return `insert into ci_check_results (issue_key, check_issue_key, check_artifact_id, check_block_id, check_version,
			check_sha256, run, commit_sha, branch, verdict, attestation, session, actor)
			values ($1, $2, $3::uuid, 'k3', 1, 'sha', 'run-1', $4, 'legion/core', 'pass', '{}', 'job', '{"kind":"system","id":"ci"}')`
	}
	return `insert into check_results (issue_key, check_issue_key, check_artifact_id, check_block_id, check_version,
		check_sha256, role, commit_sha, verdict, actor)
		values ($1, $2, $3::uuid, 'k1', 1, 'sha', 'tester', $4, 'pass', '{"kind":"user","id":"alice"}')`
}

// formerHeadsAndOwed is each row of pull_request_former_heads as repo#number@first four of its
// commit, with " unverified" where it is, and each issue holding a mark.
func formerHeadsAndOwed(t *testing.T, store *Store) (heads, owed []string) {
	t.Helper()
	ctx := context.Background()
	rows, err := store.Pool.Query(ctx, `
		select repo || '#' || number || '@' || left(head_sha, 4) || case when verified then '' else ' unverified' end
		from pull_request_former_heads order by 1`)
	if err != nil {
		t.Fatalf("read the former heads: %v", err)
	}
	if heads, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatalf("read the former heads: %v", err)
	}
	if rows, err = store.Pool.Query(ctx, `select distinct issue_key from check_states_owed order by 1`); err != nil {
		t.Fatalf("read the owed issues: %v", err)
	}
	if owed, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatalf("read the owed issues: %v", err)
	}
	return heads, owed
}

// TestTheFormerHeadsInstallSeedsTheResultsTheSettleOfTheIssueReads: before the install, CORE-1's
// open #70 left 1111… for 3333…, and one result was recorded at 1111…. The settle of an issue reads
// every result recorded against the checks of its Verification document, whichever issue recorded
// it (loadCheckResults), so the install seeds the commit for the pull request of the issue whose
// check the result names: CORE-1's #70 for a result against CORE-1's check, recorded on CORE-1 or
// on its child CORE-3, by a tester or by a CI job; CORE-2's #71 for a result CORE-1 recorded
// against its parent CORE-2's check, which CORE-1's settle never reads.
func TestTheFormerHeadsInstallSeedsTheResultsTheSettleOfTheIssueReads(t *testing.T) {
	for _, row := range []struct {
		name, table, recordedOn, checkOf string
		heads, owed                      []string
	}{
		{"a tester's result recorded on the issue against its check", "check_results", "CORE-1", "CORE-1",
			[]string{"acme/widgets#70@1111 unverified"}, []string{"CORE-1"}},
		{"a tester's result recorded on a child against the issue's check", "check_results", "CORE-3", "CORE-1",
			[]string{"acme/widgets#70@1111 unverified"}, []string{"CORE-1"}},
		{"a CI job's result recorded on a child against the issue's check", "ci_check_results", "CORE-3", "CORE-1",
			[]string{"acme/widgets#70@1111 unverified"}, []string{"CORE-1"}},
		{"a result the issue recorded against its parent's check", "check_results", "CORE-1", "CORE-2",
			[]string{"acme/widgets#71@1111 unverified"}, []string{"CORE-2"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			store := openEmptyTestStore(t)
			documents := installFixture(t, store)
			if _, err := store.Pool.Exec(ctx, installResult(row.table), row.recordedOn, row.checkOf, documents[row.checkOf], installH1); err != nil {
				t.Fatalf("record the result at 1111…: %v", err)
			}
			if _, err := store.Pool.Exec(ctx, `delete from check_states_owed`); err != nil {
				t.Fatalf("clear the result's mark: %v", err)
			}
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrate over the seeded database: %v", err)
			}
			heads, owed := formerHeadsAndOwed(t, store)
			if !slices.Equal(heads, row.heads) || !slices.Equal(owed, row.owed) {
				t.Fatalf("the install recorded the former heads %v and marked %v owed, want %v and %v: the candidates of the pull request of the issue whose settle reads the result", heads, owed, row.heads, row.owed)
			}
		})
	}
}
