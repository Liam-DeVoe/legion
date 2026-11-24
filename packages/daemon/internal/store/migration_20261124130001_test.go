package store

import (
	"context"
	"testing"
)

// 20261124130001 records how many review requests GitHub holds open on a pull request, read from
// the same answer as its merge state. A pull request written before it holds no such count, and
// nothing in the row derives one, so the column takes 0 beside the merge_state the row already
// held: the count a read that found no request open writes. The daemon's next read of the pull
// request writes the count that read finds.
func TestAPullRequestRecordedBefore20261124130001HoldsNoReviewRequest(t *testing.T) {
	store := emptyStore(t)
	ctx := context.Background()
	migrateThrough(t, store, 20261123212127)
	if _, err := store.Pool().Exec(ctx, `insert into issues
		(key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
		values ('LEGION-208', 'LEGION-208', 'LEGION', 'root', 'reviewing', 1, 'needs_review', 'A', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `insert into pull_requests (issue, repo, number, branch, head_sha, head_updated_at,
		failing, cancelled, fix_attempts, blocked_attempts, check_runs, generation, snapshot, head_counted, planned_red,
		review_seen, state, checked_head, required_workflows, required_workflows_head, mergeability, merge_state, base, base_moved)
		values ('LEGION-208', 'sjawhar/legion', 1208, 'legion/LEGION-208', 'h4', now(), '[]', '[]', 0, 0, '[]',
		 1, '', '', false, 0, 'open', '', '[]', '', 'MERGEABLE', 'blocked', 'main', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var requested int
	var state string
	if err := store.Pool().QueryRow(ctx,
		`select reviews_requested, merge_state from pull_requests where issue = 'LEGION-208'`).Scan(&requested, &state); err != nil {
		t.Fatalf("read the pull request after 20261124130001: %v", err)
	}
	if requested != 0 || state != "blocked" {
		t.Fatalf("after 20261124130001 the row holds %d review requests and merge state %q, want 0 beside the blocked it held",
			requested, state)
	}
	if _, err := store.Pool().Exec(ctx,
		`update pull_requests set reviews_requested = -1 where issue = 'LEGION-208'`); err == nil {
		t.Fatalf("the column took a negative count of review requests, want the check to refuse it")
	}
}
