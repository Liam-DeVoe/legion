package graphcensus

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// TestTheIssueLinkBatchTakesTheStoreLockOrder holds writeIssueLinkBatch (dispatch://INFRA-1253) to
// the store's lock order, as TestEveryBackfillBatchTakesTheStoreLockOrder holds the other batches:
// the owner row the link stands under, its issue's row, first, then the needs tuple, then the
// issue_links row that names the issue's node. The tuple is stated at the sentinel, which no repeat
// key holds, so no hold stands on it; the projection row's hold reads that the run took no lock of
// a later step while it waited. The link is one a process of the release before wrote, naming no
// node, between two issues this build wrote, so the object pass reads no row of them. After the
// run the row names its issue's node and the issue states one needs tuple to its target, dated at
// the row's own created_at, and the backfill reports the link done.
func TestTheIssueLinkBatchTakesTheStoreLockOrder(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedLockProject(t, database)
	alice := model.Actor{Kind: "user", ID: "alice"}
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		now := time.Now().UTC()
		if err := graphstore.Write(ctx, tx, []graphstore.Node{
			graphstore.IssueNode("LOCK-1", "LOCK", alice, now), graphstore.IssueNode("LOCK-2", "LOCK", alice, now),
		}, nil); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			insert into issues (key, project_key, number, title, created_by, rank, node_kind, node_address) values
				('LOCK-1', 'LOCK', 1, 'Waits', '{"kind":"user","id":"alice"}', 'U', 'issue', 'LOCK-1'),
				('LOCK-2', 'LOCK', 2, 'Waited on', '{"kind":"user","id":"alice"}', 'V', 'issue', 'LOCK-2')
		`)
		return err
	}); err != nil {
		t.Fatalf("seed the two issues: %v", err)
	}
	var createdAt time.Time
	if err := database.Pool.QueryRow(ctx, `
		insert into issue_links (issue_key, kind, target_key, created_at)
		values ('LOCK-1', 'blocked_by', 'LOCK-2', now() - interval '3 days')
		returning created_at
	`).Scan(&createdAt); err != nil {
		t.Fatalf("seed the link the release before wrote: %v", err)
	}

	owner, project := holdLockOwner(t, database)
	row := lockstep.HoldRow(t, database, &lockstep.Hold{
		Name: "the link's issue_links row", Step: lockstep.StepProjection, Taken: true,
		Statement: "update issue_links l set node_kind",
	}, `select 1 from issue_links where issue_key = $1 and target_key = $2 for no key update`, "LOCK-1", "LOCK-2")

	report := followTheBackfill(t, database, "writeIssueLinkBatch", []*lockstep.Hold{owner, project, row})
	if kind, address := rowMarker(t, database,
		`select node_kind, node_address from issue_links where issue_key = $1 and target_key = 'LOCK-2'`, "LOCK-1"); kind != graphstore.KindIssue || address != "LOCK-1" {
		t.Errorf("the link names node %q/%q, want %s/LOCK-1: the run the test followed is not one that committed", kind, address, graphstore.KindIssue)
	}
	needs, err := graphstore.CurrentOut(ctx, database.Pool, graphstore.KindIssue, "LOCK-1", []string{graphstore.TupleNeeds})
	if err != nil {
		t.Fatalf("read LOCK-1's needs: %v", err)
	}
	if len(needs) != 1 || needs[0].ToKind != graphstore.KindIssue || needs[0].ToID != "LOCK-2" || !needs[0].At.Equal(createdAt) {
		t.Errorf("LOCK-1 states needs %+v, want one tuple to issue:LOCK-2 dated at the row's created_at %s", needs, createdAt)
	}
	if done := reportedKind(t, report, "issue_link"); done.Rows != 1 || done.Tuples != 1 || !done.Complete {
		t.Errorf("the backfill reports %#v for issue links, want 1 row, 1 tuple and every row marked", done)
	}
}
