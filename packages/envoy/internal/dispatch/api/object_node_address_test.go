package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// A build before this one writes no node, so a document it removes leaves the node this build
// wrote for it standing behind no row (graphcensus's orphaned class). A document this build then
// makes of the same issue and name takes that address. Its create holds the issue's row and has
// found no row at the address, so the node there is the removed document's: the create states it
// gone and then live, both under its own artifact.created event and dated at the new row's
// created_at, and the new document's node carries no statement of the removed one's as its own.
// The project census reads the new row as complete. The node can be live, or at a state a later
// release marked it with while the document stood (laterNodeState),
// which is no less the removed document's.
//
// Its controls are the create's gone step taken out: the node then states nothing for the new
// document, and keeps the removed one's event and its one live statement; and the gone step taken
// for a live node alone, which moves the marked node live with no gone statement between.
func TestADocumentMadeWhereAnOrphanedNodeStandsStatesItGoneThenLive(t *testing.T) {
	for _, orphan := range []struct{ name, state string }{
		{"a live node", graphstore.StateLive},
		{"a node a later release marked", laterNodeState},
	} {
		t.Run(orphan.name, func(t *testing.T) {
			handler, database := newTestHandlerWithStore(t)
			ctx := context.Background()
			issue := createInteractionIssue(t, handler, "ORP", "A document an earlier build removed", "The spec.\n")
			first := emptyDocument(t, handler, issue.Key, "Scratch.md")
			created := artifactEvent(t, database, "artifact.created", first.ID)
			want := []string{graphstore.StateLive}
			if orphan.state != graphstore.StateLive {
				markNodeState(t, database, graphstore.KindArtifact, first.RefKey, orphan.state)
				want = append(want, orphan.state)
			}
			removeAsAnEarlierBuild(t, database, first.ID)

			second := emptyDocument(t, handler, issue.Key, "Scratch.md")
			if second.RefKey != first.RefKey {
				t.Fatalf("the second document took the address %q, want the removed one's %q, so this test proves nothing", second.RefKey, first.RefKey)
			}
			remade := artifactEvent(t, database, "artifact.created", second.ID)
			requireDocumentNode(t, database, second.RefKey, graphstore.StateLive, remade)
			history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, second.RefKey, graphstore.AttributeState)
			if err != nil {
				t.Fatalf("read the state history of %s: %v", second.RefKey, err)
			}
			var stated []string
			for _, tuple := range history {
				stated = append(stated, tuple.ToID)
			}
			if want = append(want, graphstore.StateGone, graphstore.StateLive); !equalStrings(stated, want) {
				t.Fatalf("the node of %s states %v over its life, want %v", second.RefKey, stated, want)
			}
			gone, live := history[len(history)-2], history[len(history)-1]
			if history[0].SourceEvent != created || gone.SourceEvent != remade || live.SourceEvent != remade {
				t.Errorf("the state statements of %s name events %d first and %d, %d last, want %d, then %d twice", second.RefKey,
					history[0].SourceEvent, gone.SourceEvent, live.SourceEvent, created, remade)
			}
			if !live.At.Equal(second.CreatedAt) {
				t.Errorf("the newest state statement of %s is dated %s, want the new row's created_at %s", second.RefKey, live.At, second.CreatedAt)
			}
			requireProjectsComplete(t, database)
		})
	}
}

// laterNodeState is a state a later release marks a node with, such as a requirement's proposed,
// which this build never writes.
const laterNodeState = "proposed"

// markNodeState writes the node kind/id at state, as a later release's build marks a node: the
// store's own writer moving the state, which states it in one tuple.
func markNodeState(t *testing.T, database *store.Store, kind, id, state string) {
	t.Helper()
	ctx := context.Background()
	node, held, err := graphstore.ReadNode(ctx, database.Pool, kind, id)
	if err != nil || !held {
		t.Fatalf("read the node %s %s to mark it %s: held %t, %v", kind, id, state, held, err)
	}
	node.State, node.MadeAt = state, time.Now().UTC()
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		return graphstore.Write(ctx, tx, []graphstore.Node{node}, nil)
	}); err != nil {
		t.Fatalf("mark the node %s %s %s: %v", kind, id, state, err)
	}
}

// A create's transaction can start while the removal of the document at its address is still
// under way: the removal holds the issue's row, the create queues behind it, and the removal
// states the node gone and deletes the row before it commits. The create dates its row and its
// node's live statement with one time it reads from the database's clock after it holds the
// issue's row (graphstore.StatementTime), so that time is later than the removal's gone
// statement: the node's newest state statement, read by the order the store appends it in and by
// the order of its time alike, is the new document's live one, dated at its row's created_at.
//
// Its control is the create dated at its transaction's start (now()): its live statement is then
// dated before the removal's gone one, and a read of the node's state as of now answers gone.
func TestACreateThatWaitedOnARemovalDatesItsDocumentAfterIt(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	ctx := context.Background()
	issue := createInteractionIssue(t, handler, "CLK", "A document made while its namesake is removed", "The spec.\n")
	first := emptyDocument(t, handler, issue.Key, "Scratch.md")

	removal, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the removal: %v", err)
	}
	defer removal.Rollback(ctx)
	if _, err := removal.Exec(ctx, `select 1 from issues where key = $1 for no key update`, issue.Key); err != nil {
		t.Fatalf("take the issue's row as the removal does: %v", err)
	}
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		answered <- dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/artifacts",
			map[string]any{"name": "Scratch.md"}, "alice")
	}()
	// The create's own statement, the owner check every upload takes (issueStatus with
	// ownerRowLock): a document settlement queues on the issue's row too, with another statement,
	// and is never taken for the create.
	waitForLockWait(t, database, `%select status, closed_at is null from issues where key = $1 for no key update%`)
	// The create's transaction has begun. The removal states the node gone at its own time and
	// deletes the row, as removeArtifact does, then commits.
	goneAt, err := graphstore.StatementTime(ctx, removal, graphstore.KindArtifact, first.RefKey)
	if err != nil {
		t.Fatalf("read the removal's time: %v", err)
	}
	gone := graphstore.ArtifactNode(first.RefKey, "CLK", "", model.Actor{Kind: "user", ID: "alice"}, goneAt)
	gone.State = graphstore.StateGone
	if err := graphstore.Write(ctx, removal, []graphstore.Node{gone}, nil); err != nil {
		t.Fatalf("state the node gone: %v", err)
	}
	clearAndDeleteArtifact(t, ctx, removal, first.ID)
	if err := removal.Commit(ctx); err != nil {
		t.Fatalf("commit the removal: %v", err)
	}

	var response *httptest.ResponseRecorder
	select {
	case response = <-answered:
	case <-time.After(30 * time.Second):
		t.Fatal("the create did not answer within 30s of the removal's commit")
	}
	if response.Code != http.StatusCreated {
		t.Fatalf("the create answered %d: %s", response.Code, response.Body.String())
	}
	second := decodeBody[uploadedArtifact](t, response).Artifact
	if second.RefKey != first.RefKey {
		t.Fatalf("the create took the address %q, want the removed one's %q, so this test proves nothing", second.RefKey, first.RefKey)
	}
	if !second.CreatedAt.After(goneAt) {
		t.Errorf("the new document's created_at %s is not after the removal's gone statement %s", second.CreatedAt, goneAt)
	}
	history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, second.RefKey, graphstore.AttributeState)
	if err != nil || len(history) == 0 {
		t.Fatalf("read the state history of %s: %d tuple(s), %v", second.RefKey, len(history), err)
	}
	if newest := history[len(history)-1]; newest.ToID != graphstore.StateLive || !newest.At.Equal(second.CreatedAt) {
		t.Errorf("the newest state statement of %s, by the order the store appended it in, is %s at %s, want live at the new row's created_at %s",
			second.RefKey, newest.ToID, newest.At, second.CreatedAt)
	}
	var asOfNow string
	if err := database.Pool.QueryRow(ctx, `
		select to_id from graph_tuples
		where from_kind = 'artifact' and from_id = $1 and kind = 'state' and to_kind = 'value' and at <= clock_timestamp()
		order by at desc, seq desc limit 1
	`, second.RefKey).Scan(&asOfNow); err != nil {
		t.Fatalf("read the state of %s as of now: %v", second.RefKey, err)
	}
	if asOfNow != graphstore.StateLive {
		t.Errorf("the state of %s as of now, by the order of its statements' time, is %s, want live", second.RefKey, asOfNow)
	}
	requireProjectsComplete(t, database)
}

// This build removes a document, stating its node gone, and a build before this one makes another
// at its address, which writes no node and dates its row at its transaction's start, here before
// the gone statement. When this build removes that second document, its node is gone already and
// the removal leaves it gone: it states nothing, rather than first moving the node live at the
// second row's created_at, before the gone statement it would then follow. The node's state
// history is the first document's live and gone, in time order, and the census names nothing.
//
// Its control is the removal's first-node write made whatever the store holds: the history then
// gains a live statement dated before the gone one it follows.
func TestARemovalLeavesTheGoneNodeOfAnEarlierBuildsDocumentGone(t *testing.T) {
	handler, database := newTestHandlerWithStore(t)
	ctx := context.Background()
	issue := createInteractionIssue(t, handler, "GNE", "A document an earlier build made where one was removed", "The spec.\n")
	first := emptyDocument(t, handler, issue.Key, "Scratch.md")
	removedDocument(t, handler, first.ID)
	history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, first.RefKey, graphstore.AttributeState)
	if err != nil || len(history) != 2 || history[1].ToID != graphstore.StateGone {
		t.Fatalf("after the first removal the state history of %s is %+v, %v; want live then gone", first.RefKey, history, err)
	}
	goneAt := history[1].At
	var second, ref string
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, created_by, created_at)
		select a.issue_key, i.project_key, 'scratch-md', 'Scratch.md', 'doc', '{"kind":"user","id":"bob"}', $2
		from (select $1::text as issue_key) a join issues i on i.key = a.issue_key
		returning id::text, ref_key
	`, issue.Key, goneAt.Add(-time.Second)).Scan(&second, &ref); err != nil {
		t.Fatalf("make the second document as an earlier build does: %v", err)
	}
	if ref != first.RefKey {
		t.Fatalf("the second document took the address %q, want the removed one's %q, so this test proves nothing", ref, first.RefKey)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into artifact_versions (artifact_id, number, markdown, authors) values ($1, 1, '', '[{"kind":"user","id":"bob"}]')
	`, second); err != nil {
		t.Fatalf("store the second document's empty first version: %v", err)
	}
	removedDocument(t, handler, second)
	after, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, first.RefKey, graphstore.AttributeState)
	if err != nil {
		t.Fatalf("read the state history of %s: %v", first.RefKey, err)
	}
	if len(after) != 2 {
		t.Errorf("after the second removal the state history of %s holds %d statement(s), want the first document's two: %+v", first.RefKey, len(after), after)
	}
	for index := 1; index < len(after); index++ {
		if after[index].At.Before(after[index-1].At) {
			t.Errorf("the state history of %s is out of time order at %d: %s at %s after %s at %s", first.RefKey, index,
				after[index].ToID, after[index].At, after[index-1].ToID, after[index-1].At)
		}
	}
	requireProjectsComplete(t, database)
}

// removeAsAnEarlierBuild removes the untouched document as a build before this one does: the rows
// removeArtifact clears and the artifact row, in one transaction, and no node written.
func removeAsAnEarlierBuild(t *testing.T, database *store.Store, artifactID string) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		clearAndDeleteArtifact(t, ctx, tx, artifactID)
		return nil
	}); err != nil {
		t.Fatalf("remove document %s as an earlier build does: %v", artifactID, err)
	}
}

// clearAndDeleteArtifact runs, in tx, the statements removeArtifact runs to take an untouched
// document's rows away: its clears, its reference rows and the artifact row.
func clearAndDeleteArtifact(t *testing.T, ctx context.Context, tx pgx.Tx, artifactID string) {
	t.Helper()
	artifact, err := loadArtifactRowByID(ctx, tx, artifactID)
	if err != nil {
		t.Fatalf("read document %s: %v", artifactID, err)
	}
	for _, clear := range artifactRemovalClears(artifact) {
		if _, err := tx.Exec(ctx, clear.statement, clear.args...); err != nil {
			t.Fatalf("clear the %s of document %s: %v", clear.what, artifactID, err)
		}
	}
	if err := refs.ForgetDocument(ctx, tx, artifactID); err != nil {
		t.Fatalf("forget the references of document %s: %v", artifactID, err)
	}
	if _, err := tx.Exec(ctx, `delete from artifacts where id = $1`, artifactID); err != nil {
		t.Fatalf("delete document %s: %v", artifactID, err)
	}
}

// requireProjectsComplete fails unless the project census names no row or node incomplete.
func requireProjectsComplete(t *testing.T, database *store.Store) {
	t.Helper()
	incomplete, err := graphcensus.ProjectComplete(context.Background(), database.Pool)
	if err != nil {
		t.Fatalf("read the incomplete rows: %v", err)
	}
	if len(incomplete) != 0 {
		t.Errorf("the project census names %q, want nothing", incomplete)
	}
}

// waitForLockWait waits until a session of database waits on a lock at a statement matching the
// LIKE pattern like, and fails the test after 10 seconds.
func waitForLockWait(t *testing.T, database *store.Store, like string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		waiting, err := storetest.CountLockWaits(context.Background(), database, like)
		if err != nil {
			t.Fatalf("read the lock waits: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no session waited on a lock at %q within 10s, so this test proves nothing", like)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
