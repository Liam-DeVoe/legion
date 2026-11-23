package main

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
)

// A stored rename pair whose new name GitHub cannot hold is dropped at the start, not applied: one
// ERROR line names the pair and why, the rename pass rewrites no field for it, its old name is
// its own canonical name, and a link of it owes its read under that name, a name a read can reach
// (refs.SplitGitHubRepo). The other pairs apply. No route, event or migration writes a pair, so
// such a pair comes only from SQL by hand (store.RepoRename). Before, the pass wrote `not a
// repository name` into every field of badrename/x, INFRA-2's own repository and the read owed
// for #109 among them, and every reader called badrename/x by that name, which no read of GitHub
// can reach. The control in the same test is the other pair, sjawhar/legion to
// cybermonklabs/legion, which the pass writes and the start reads as before.
func TestTheStartDropsARenamePairWhoseNewNameGitHubCannotHold(t *testing.T) {
	ctx := store.WithTransactionTracking(context.Background())
	database := storetest.Open(t)
	seedRenamedIssue(t, ctx, database)
	if _, err := database.Pool.Exec(ctx, `
		insert into repo_projects (repo, project, created_by, renamed_to)
			values ('badrename/x', 'INFRA', '{"kind":"user","id":"alice"}', 'not a repository name');
		insert into issues (key, project_key, number, title, created_by, rank, repo)
			values ('INFRA-2', 'INFRA', 2, 'Two', '{"kind":"user","id":"alice"}', 'U2', 'badrename/x');
		insert into pull_request_reads_owed (repo, number, reason) values ('badrename/x', 109, 'failure');
	`); err != nil {
		t.Fatalf("seed badrename/x renamed to a name GitHub cannot hold: %v", err)
	}
	var logs bytes.Buffer
	logtest.SetDefault(t, slog.NewTextHandler(&logs, nil))

	renames, pairs, err := bootRenames(ctx, database.Pool)
	renameRepositories(ctx, database.Pool, pairs, err, repoRenamePassTimeout)
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.NewAttributor(renames).OweLinkedPullRequestReads(ctx, tx, []string{"https://github.com/badrename/x/pull/110"}); err != nil {
		t.Fatalf("the link write of badrename/x#110: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	var errors []string
	for line := range strings.Lines(out) {
		if strings.Contains(line, "level=ERROR") {
			errors = append(errors, line)
		}
	}
	if len(errors) != 1 || !strings.Contains(errors[0], "from=badrename/x") || !strings.Contains(errors[0], `"not a repository name"`) {
		t.Errorf("the start logged %d ERROR lines %q, want one that names the pair badrename/x to %q and why:\n%s",
			len(errors), errors, "not a repository name", out)
	}
	if !strings.Contains(out, `level=INFO `+renamePassSummary+` pairs=1 rows=1 deleted=0 failures=0 left=0`) {
		t.Errorf("logged, want the pass's summary at INFO with the one pair GitHub can hold, pairs=1 rows=1 deleted=0 failures=0 left=0:\n%s", out)
	}
	if repo := issueRepo(t, ctx, database); repo != "cybermonklabs/legion" {
		t.Errorf("INFRA-1's repository = %s, want cybermonklabs/legion: the other pair applies", repo)
	}
	var kept string
	if err := database.Pool.QueryRow(ctx, `select repo from issues where key = 'INFRA-2'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != "badrename/x" {
		t.Errorf("INFRA-2's repository = %q, want badrename/x: the pass rewrites no field for the dropped pair", kept)
	}
	if canonical := renames.Canonical("BadRename/X"); canonical != "BadRename/X" {
		t.Errorf("the canonical name of BadRename/X = %q, want the name itself: the start holds no pair it dropped", canonical)
	}
	rows, err := database.Pool.Query(ctx, `select repo || '#' || number from pull_request_reads_owed`)
	if err != nil {
		t.Fatal(err)
	}
	owed, err := pgxStrings(rows)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(owed)
	if want := []string{"badrename/x#109", "badrename/x#110"}; !slices.Equal(owed, want) {
		t.Errorf("owed reads %q, want %q: each under a name a read can reach", owed, want)
	}
	for _, row := range owed {
		repo, _, _ := strings.Cut(row, "#")
		if _, _, err := refs.SplitGitHubRepo(repo); err != nil {
			t.Errorf("the read %s is owed under a name no read can reach: %v", row, err)
		}
	}
}

// pgxStrings is each row's one text column.
func pgxStrings(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]string, error) {
	defer rows.Close()
	var all []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		all = append(all, s)
	}
	return all, rows.Err()
}

// bootRenames is the start's read of the pairs (readRenames) and the pairs it gives the rename
// pass (refs.Renames.Pairs), as main runs them.
func bootRenames(ctx context.Context, pool *store.Pool) (refs.Renames, []store.RepoRename, error) {
	renames, err := readRenames(ctx, pool, repoRenamesReadTimeout)
	return renames, renames.Pairs(), err
}
