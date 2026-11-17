package graphcensus

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// A removal of this build takes the document's issue row, states its node gone and deletes the
// row, and commits. A batch of the backfill that read the row before then takes the same issue row
// before it writes (lockObjectOwners), so it waits for the removal, reads under that row that the
// document is gone, and writes no node for it: the removal's gone statement stands, and the census
// names nothing. Here the removal holds the issue's row, with its node stated gone and its row
// deleted, while the batch is shown queued behind it; then the removal commits.
//
// Its control is the batch's owner lock taken out: the batch then reads the row the removal has not
// committed yet, waits only on the node, and moves it live after the removal commits, which the
// census names orphaned.
func TestTheObjectBatchWaitsForARemovalOnTheIssueRowAndWritesNoNode(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	created := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	alice := model.Actor{Kind: "user", ID: "alice"}
	seedIdentityIssue(t, database, created, alice)
	document := insertIdentityDocument(t, database, "alice", created)
	kind := objectKindOf(t, graphstore.KindArtifact)
	nodes, err := readObjectNodes(ctx, database, kind.kind, kind.read, kind.start, backfillBatch)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("read the document no node marks: %d node(s), %v; want 1", len(nodes), err)
	}

	removal, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the removal: %v", err)
	}
	defer removal.Rollback(ctx)
	if _, err := removal.Exec(ctx, `select 1 from issues where key = 'IDEN-1' for no key update`); err != nil {
		t.Fatalf("take the issue's row as the removal does: %v", err)
	}
	gone := graphstore.ArtifactNode("IDEN-1/notes-md", "IDEN", "", alice, created)
	gone.State = graphstore.StateGone
	if err := graphstore.Write(ctx, removal, []graphstore.Node{gone}, nil); err != nil {
		t.Fatalf("state the node gone: %v", err)
	}
	if _, err := removal.Exec(ctx, `delete from artifacts where id = $1`, document); err != nil {
		t.Fatalf("delete the document's row: %v", err)
	}
	type answer struct {
		written KindBackfill
		err     error
	}
	answered := make(chan answer, 1)
	go func() {
		written, err := writeObjectBatch(ctx, database, kind, nodes)
		answered <- answer{written, err}
	}()
	if queued := storetest.WaitForLockWaiters(t, removal, 1, 10*time.Second); queued < 1 {
		t.Fatal("the batch never queued behind the removal, so this test proves nothing")
	}
	if err := removal.Commit(ctx); err != nil {
		t.Fatalf("commit the removal: %v", err)
	}
	var got answer
	select {
	case got = <-answered:
	case <-time.After(30 * time.Second):
		t.Fatal("the batch did not answer within 30s of the removal's commit")
	}
	if got.err != nil || got.written.Nodes != 0 {
		t.Errorf("the batch answered %+v, %v; want no node written for a row the removal took", got.written, got.err)
	}
	requireComplete(t, ctx, database, "after the removal and the batch")
}

// A conversation's file whose session id holds white space is between a party the parties' join
// refuses (graphstore.PartiesValue), and so is a comment on it. No writer of this build writes such
// a row, but a row of an earlier build may hold one. The backfill skips each such row, counts it in
// the report's Skipped, and writes every other row's node, so one such row stops neither the batch
// it falls in nor any later run; the census names the skipped document missing. A comment on it is
// left to the comment pass, which skips it the same way.
//
// Its control is the skip taken out of the object batch: the backfill then fails on the join's
// refusal and writes nothing more.
func TestTheBackfillSkipsARowBetweenARefusedPartyAndWritesTheRest(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	created := time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)
	alice := model.Actor{Kind: "user", ID: "alice"}
	seedIdentityIssue(t, database, created, alice)
	insertIdentityDocument(t, database, "alice", created)
	var bad, badRef string
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (session_id, slug, name, kind, created_by, created_at)
		values ('ses bad', 'notes-md', 'Notes', 'file', '{"kind":"session","id":"ses bad"}', $1)
		returning id::text, ref_key
	`, created).Scan(&bad, &badRef); err != nil {
		t.Fatalf("write the conversation file whose session holds white space: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into comments (artifact_id, author, body) values ($1::uuid, '{"kind":"session","id":"ses bad"}', 'A remark.')
	`, bad); err != nil {
		t.Fatalf("write the comment on it: %v", err)
	}

	report, err := Backfill(ctx, database, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("the backfill answered %v, want it to skip the row between a refused party", err)
	}
	if done := reportedKind(t, report, graphstore.KindArtifact); done.Skipped != 1 || done.Nodes != 1 {
		t.Errorf("the backfill reports %+v for documents, want one node written and one row skipped", done)
	}
	if node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, "IDEN-1/notes-md"); err != nil || !held || node.State != graphstore.StateLive {
		t.Errorf("the issue's document's node is %+v (held %t, %v), want live: one bad row stops nothing else", node, held, err)
	}
	incomplete, err := ProjectComplete(ctx, database.Pool)
	if err != nil {
		t.Fatalf("read the incomplete rows: %v", err)
	}
	if !slices.Contains(incomplete, "artifact "+badRef+": missing") {
		t.Errorf("ProjectComplete answers %q, want it to name the skipped file missing", incomplete)
	}
}
