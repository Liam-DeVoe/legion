package graphcensus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// orphanIssue is the issue seedOrphan writes its document on.
const orphanIssue = "ORPH-1"

// seedOrphan writes project ORPH, issue ORPH-1 with its node, and a document of the issue at
// ORPH-1/<slug> with its node, as this build's create writes them, and then removes the document's
// row as the release before this one does: the row goes, a removal event is appended, and nothing
// states the node gone. It answers the document's address and the removal event.
func seedOrphan(t *testing.T, database *store.Store, slug string) (string, int64) {
	t.Helper()
	ctx := context.Background()
	alice := model.Actor{Kind: "user", ID: "alice"}
	ref := orphanIssue + "/" + slug
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			insert into projects (key, name, default_assignee) values ('ORPH', 'Orphans', 'alice') on conflict do nothing;
			insert into issues (key, project_key, number, title, created_by, rank)
			values ('ORPH-1', 'ORPH', 1, 'One', '{"kind":"user","id":"alice"}', 'U') on conflict do nothing
		`); err != nil {
			return err
		}
		if err := graphstore.Write(ctx, tx, []graphstore.Node{
			graphstore.IssueNode(orphanIssue, "ORPH", alice, time.Now().UTC()),
			graphstore.ArtifactNode(ref, "ORPH", "", alice, time.Now().UTC()),
		}, nil); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			insert into artifacts (issue_key, project_key, slug, name, kind, created_by)
			values ('ORPH-1', 'ORPH', $1, $1, 'doc', '{"kind":"user","id":"alice"}')
		`, slug)
		return err
	}); err != nil {
		t.Fatalf("seed the document %s: %v", ref, err)
	}
	var removal int64
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		var artifact []byte
		if err := tx.QueryRow(ctx, `
			delete from artifacts where ref_key = $1
			returning jsonb_build_object('id', id::text, 'issue_key', issue_key, 'slug', slug, 'ref_key', ref_key)
		`, ref).Scan(&artifact); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			insert into events (issue_key, seq, type, actor, payload, notify)
			select 'ORPH-1', coalesce(max(seq), 0) + 1, 'artifact.removed', '{"kind":"user","id":"alice"}',
			       jsonb_build_object('artifact', $1::jsonb), false
			from events where issue_key = 'ORPH-1'
			returning id
		`, artifact).Scan(&removal)
	}); err != nil {
		t.Fatalf("remove the document %s as the release before does: %v", ref, err)
	}
	return ref, removal
}

// A document the release before this one removed, after this build wrote its node, leaves the node
// standing behind no row, which OrphanedDocumentNodes reads and StateOrphanedGone states gone: its
// state tuple is gone, made by the block-index tick, caused by that release's own artifact.removed
// event, and the project census then reads the store complete. A second call leaves the gone node
// as it is.
//
// Its control is the gone state caused at the sentinel rather than by the removal's event: the
// tuple then names no event.
func TestAnOrphanedDocumentNodeIsStatedGoneUnderItsRemoval(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	ref, removal := seedOrphan(t, database, "notes-md")
	orphaned, err := OrphanedDocumentNodes(ctx, database.Pool, 10)
	if err != nil || len(orphaned) != 1 || orphaned[0] != ref {
		t.Fatalf("the orphaned document nodes are %q (%v), want %s alone", orphaned, err, ref)
	}
	if err := StateOrphanedGone(ctx, database, ref); err != nil {
		t.Fatalf("state the orphaned node of %s gone: %v", ref, err)
	}
	history, err := graphstore.History(ctx, database.Pool, graphstore.KindArtifact, ref, graphstore.AttributeState)
	if err != nil || len(history) != 2 {
		t.Fatalf("the state history of %s is %+v (%v), want live then gone", ref, history, err)
	}
	gone := history[1]
	if gone.ToID != graphstore.StateGone || gone.SourceEvent != removal || gone.Who != sweepActor {
		t.Errorf("the gone statement of %s is %+v, want gone, caused by the removal event %d, stated by %+v", ref, gone, removal, sweepActor)
	}
	requireComplete(t, ctx, database, "after the orphan was stated gone")
	if orphaned, _ := OrphanedDocumentNodes(ctx, database.Pool, 10); len(orphaned) != 0 {
		t.Errorf("after the sweep the orphaned document nodes are %q, want none", orphaned)
	}
	if err := StateOrphanedGone(ctx, database, ref); !errors.Is(err, ErrNotOrphaned) {
		t.Errorf("a second sweep of %s answered %v, want ErrNotOrphaned", ref, err)
	}
}

// A create of this build at the address of an orphaned node writes the node and then the row in
// one transaction, holding the node's row until it commits. The sweep takes the issue's row, then
// queues behind the create on the node's row; once the create commits it reads the row standing
// and leaves the node: the node is live, the created row stands, and the census reads nothing.
//
// Its control is the sweep's read of a standing row moved before its lock of the node: it then
// reads no row, states the created document's node gone, and the census names the row gone.
func TestTheOrphanSweepLosesNothingOfACreateItRacesWith(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	ref, _ := seedOrphan(t, database, "notes-md")
	create := &lockstep.Hold{Name: "a create of " + ref + " that wrote its node and its row"}
	lockstep.Begin(t, database, create)
	if _, err := graphstore.WriteUncaused(ctx, create.Tx, []graphstore.Node{graphstore.ArtifactNode(
		ref, "ORPH", "", model.Actor{Kind: "user", ID: "alice"}, time.Now().UTC())}, nil); err != nil {
		t.Fatalf("write the created document's node: %v", err)
	}
	if _, err := create.Tx.Exec(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, created_by)
		values ('ORPH-1', 'ORPH', 'notes-md', 'Made again', 'doc', '{"kind":"user","id":"alice"}')
	`); err != nil {
		t.Fatalf("write the created document's row: %v", err)
	}
	sweeper := storetest.SingleConnection(t, database)
	var pid uint32
	if err := sweeper.Pool.QueryRow(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read the sweep's backend: %v", err)
	}
	swept := make(chan error, 1)
	go func() { swept <- StateOrphanedGone(ctx, sweeper, ref) }()
	if early, done := storetest.WaitForQueuedBehind(t, create.Tx, pid, swept, lockstep.Wait); done {
		t.Fatalf("the sweep answered %v without waiting on the node the create holds, so this test proves nothing", early)
	}
	t.Logf("the sweep waits behind %s at %q", create.Name, lockstep.WaitingStatement(t, database, pid))
	if err := create.Tx.Commit(ctx); err != nil {
		t.Fatalf("commit the create: %v", err)
	}
	create.Release()
	select {
	case err := <-swept:
		if !errors.Is(err, ErrNotOrphaned) {
			t.Errorf("the sweep queued behind the create answered %v, want ErrNotOrphaned", err)
		}
	case <-time.After(lockstep.Wait):
		t.Fatalf("the sweep did not answer within %s of the create's commit", lockstep.Wait)
	}
	node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, ref)
	if err != nil || !held || node.State != graphstore.StateLive {
		t.Errorf("after the create and the sweep the node of %s is %+v (held %t, %v), want it live", ref, node, held, err)
	}
	requireComplete(t, ctx, database, "after the create and the sweep")
}

// The sweep of one orphaned document (StateOrphanedGone) takes the store's lock order: the issue's
// row first, then the node's row, then the state tuple it appends under the removal's event, and
// never the project's row. Each step is held in turn while the sweep's own backend is read at the
// statement it waits at and the locks it holds.
func TestTheOrphanSweepTakesTheStoreLockOrder(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	ref, removal := seedOrphan(t, database, "notes-md")
	sweeper := storetest.SingleConnection(t, database)
	var pid uint32
	if err := sweeper.Pool.QueryRow(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read the sweep's backend: %v", err)
	}
	owner := lockstep.HoldRow(t, database, &lockstep.Hold{
		Name: "issue ORPH-1's row", Step: lockstep.StepOwner, Taken: true, Kind: "issue", ID: orphanIssue,
		Statement: "select closed_at is null from issues where key = $1 for no key update",
		Unlocked:  []string{"graph_nodes", "graph_tuples"},
	}, `select 1 from issues where key = $1 for update`, orphanIssue)
	node := lockstep.HoldRow(t, database, &lockstep.Hold{
		Name: ref + "'s node row", Step: lockstep.StepNode, Taken: true, Kind: graphstore.KindArtifact, ID: ref,
		Statement: "select state from graph_nodes where kind = $1 and id = $2 for update",
		Unlocked:  []string{"graph_tuples"},
	}, `select 1 from graph_nodes where kind = $1 and id = $2 for update`, graphstore.KindArtifact, ref)
	state := lockstep.Hold{Name: ref + "'s gone tuple", Step: lockstep.StepTuple, Taken: true, Statement: "insert into graph_tuples ("}
	lockstep.Begin(t, database, &state)
	if err := graphstore.Write(ctx, state.Tx, nil, []graphstore.Tuple{{
		FromKind: graphstore.KindArtifact, FromID: ref, Kind: graphstore.AttributeState,
		ToKind: graphstore.KindValue, ToID: graphstore.StateGone,
		At: time.Now().UTC(), Who: sweepActor, SourceEvent: removal, Asserted: true,
	}}); err != nil {
		t.Fatalf("hold %s: %v", state.Name, err)
	}
	project := lockstep.HoldRow(t, database, &lockstep.Hold{Name: "project ORPH's row", Step: lockstep.StepEvent},
		`select 1 from projects where key = $1 for update`, "ORPH")
	swept := make(chan error, 1)
	go func() { swept <- StateOrphanedGone(ctx, sweeper, ref) }()
	lockstep.Follow(t, database, "the orphan sweep", pid, swept, []*lockstep.Hold{owner, node, &state, project})
	held, _, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, ref)
	if err != nil || held.State != graphstore.StateGone {
		t.Errorf("after the sweep the node of %s is %+v (%v), want it gone: the run the test followed is not one that committed", ref, held, err)
	}
}

// The object pass writes at most its budget of batches: over 501 asks owed their node, a pass of
// one batch writes the backfill's batch and answers More, and the next writes the one left and
// answers no More.
//
// Its control is the budget not spent: the first pass then writes all 501 and answers no More.
func TestTheObjectPassStopsAtItsBatchBound(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedOrphanIssueWithAsks(t, database, backfillBatch+1)
	first, err := WriteOwedObjectNodes(ctx, database, 1)
	if err != nil || first.Rows != backfillBatch || !first.More {
		t.Fatalf("a pass of one batch answered %+v (%v), want %d rows and More", first, err, backfillBatch)
	}
	second, err := WriteOwedObjectNodes(ctx, database, 1)
	if err != nil || second.Rows != 1 || second.More {
		t.Fatalf("the next pass answered %+v (%v), want the one row left and no More", second, err)
	}
	requireComplete(t, ctx, database, "after the two passes")
}

// A kind whose batch fails holds back no other kind: with the store refusing every ask's node, the
// pass still writes the message's node, and answers the asks' failure.
//
// Its control is the pass stopping at the first failing kind: the message then keeps no node.
func TestAFailingKindHoldsBackNoOtherKind(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedOrphanIssueWithAsks(t, database, 1)
	const message = "33333333-3333-4333-8333-0000000000aa"
	for _, statement := range []string{
		`insert into messages (id, issue_key, author, body) values ('` + message + `', 'ORPH-1', '{"kind":"user","id":"alice"}', 'A message.')`,
		`create function refuse_ask_nodes() returns trigger language plpgsql as $$
		 begin raise exception 'the test refuses ask nodes'; end $$`,
		`create trigger refuse_ask_nodes before insert on graph_nodes for each row
		 when (new.kind = 'ask') execute function refuse_ask_nodes()`,
	} {
		if _, err := database.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("seed the message and refuse ask nodes: %v\n%s", err, statement)
		}
	}
	pass, err := WriteOwedObjectNodes(ctx, database, 0)
	if err == nil {
		t.Fatalf("the pass with ask nodes refused answered %+v and no error, want the asks' failure", pass)
	}
	if _, held, readErr := graphstore.ReadNode(ctx, database.Pool, graphstore.KindMessage, message); readErr != nil || !held {
		t.Errorf("after the pass the message holds a node %t (%v), want it written beside the failing asks: %v", held, readErr, err)
	}
}

// seedOrphanIssueWithAsks writes project ORPH and issue ORPH-1 with their nodes, and asks rows on
// the issue with no node, as the release before this one writes them.
func seedOrphanIssueWithAsks(t *testing.T, database *store.Store, asks int) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			insert into projects (key, name, default_assignee) values ('ORPH', 'Orphans', 'alice');
			insert into issues (key, project_key, number, title, created_by, rank)
			values ('ORPH-1', 'ORPH', 1, 'One', '{"kind":"user","id":"alice"}', 'U')
		`); err != nil {
			return err
		}
		if err := graphstore.Write(ctx, tx, []graphstore.Node{
			graphstore.IssueNode(orphanIssue, "ORPH", model.Actor{Kind: "user", ID: "alice"}, time.Now().UTC()),
		}, nil); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			insert into asks (issue_key, author, question)
			select 'ORPH-1', '{"kind":"user","id":"alice"}', 'Question ' || n from generate_series(1, $1) n
		`, asks)
		return err
	}); err != nil {
		t.Fatalf("seed the issue and its asks: %v", err)
	}
}
