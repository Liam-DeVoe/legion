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
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		select 'CORE-1', 'CORE', 'verification', 'verification.md', 'doc', false, '{"kind":"user","id":"alice"}'
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
