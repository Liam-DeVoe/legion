package docs

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// rollSession is the agent session whose conversation the stand-in for the release before this
// one uploads a file to.
const rollSession = "ses_rolled_conversation"

// The release before this one writes an issue, a document, an ask and a message with no node, and
// a file it uploads to an agent's conversation appends no event at all. A row it writes while it
// serves beside this build gets its node from this build's next block-index tick, with no second
// backfill, and a tick with this build serving alone does not read those tables whole.
//
// The test drives the tick through the triggers the marking reads the log on
// (markVersionsAnotherBuildWrote). The process's first tick joins its build's cursor, and so writes
// the nodes of the issues the seed left without one. The tick after it finds this build serving
// alone after a tick that did too, so it reads nothing. The stand-in for the release before then
// writes, with no event, a conversation's file, an ask on CORE-1 and a direct message to a
// session. A tick with this build still serving alone leaves them without their nodes, which the
// project census names `missing`. With a process of that release registered, the next tick writes
// all three nodes, each in its project or between its parties, and the project census reads them
// complete.
//
// Its control is the tick's object pass taken out of ReconcileBlockIndex: the three rows stay
// `missing` after the roll's tick, and #3178's TestAVersionThePreviousReleaseWritesAfterTheDrainReachesTheStore
// fails the same way on the issue, the spec and the document the release before creates there.
func TestARowThePreviousReleaseWritesDuringARollGetsItsNodeFromTheTick(t *testing.T) {
	database, service := blockIndexService(t)
	ctx := context.Background()
	appendIssueEvent(t, database)

	joined := awaitedBlockIndexTick(t, service)
	if joined.AnotherBuild || joined.ObjectNodes != 2 {
		t.Fatalf("the process's first tick wrote %d object node(s) and found another build %t, want the nodes of CORE-1 and CORE-2 and no other build",
			joined.ObjectNodes, joined.AnotherBuild)
	}
	requireProjectComplete(t, database, nil, "after the process's first tick")
	if alone := awaitedBlockIndexTick(t, service); alone.ObjectNodes != 0 {
		t.Fatalf("the second tick with this build serving alone wrote %d object node(s), want none", alone.ObjectNodes)
	}

	file, ask, message := writeRowsAsThePreviousRelease(t, database)
	missing := []string{"artifact " + refKeyOf(t, database, file) + ": missing", "ask " + ask + ": missing", "message " + message + ": missing"}
	if alone := awaitedBlockIndexTick(t, service); alone.AnotherBuild || alone.ObjectNodes != 0 {
		t.Fatalf("a tick with this build serving alone wrote %d object node(s) and found another build %t, want none: the pass reads the four tables whole only on a roll's tick",
			alone.ObjectNodes, alone.AnotherBuild)
	}
	requireProjectComplete(t, database, missing, "after a tick with this build serving alone")

	registerProcess(t, database, "")
	registerProcess(t, database, previousReleaseCommit)
	roll := awaitedBlockIndexTick(t, service)
	if !roll.AnotherBuild || roll.ObjectNodes != 3 {
		t.Errorf("the tick with the release before serving wrote %d object node(s) and found another build %t, want the three nodes it left and another build",
			roll.ObjectNodes, roll.AnotherBuild)
	}
	requireProjectComplete(t, database, nil, "after the tick with the release before serving, with no second backfill")
	for _, want := range []struct {
		kind, id, project string
		parties           []string
	}{
		{graphstore.KindArtifact, file, "", []string{"session:" + rollSession}},
		{graphstore.KindAsk, ask, "CORE", nil},
		{graphstore.KindMessage, message, "", []string{"person:alice", "session:" + rollSession}},
	} {
		id := want.id
		if want.kind == graphstore.KindArtifact {
			id = refKeyOf(t, database, file)
		}
		node, held, err := graphstore.ReadNode(ctx, database.Pool, want.kind, id)
		if err != nil || !held || node.State != graphstore.StateLive || node.Project != want.project ||
			!slices.Equal(slices.Sorted(slices.Values(node.Parties)), want.parties) {
			t.Errorf("the node of %s %s is %+v (held %t, %v), want it live in project %q between %v", want.kind, id, node, held, err, want.project, want.parties)
		}
	}
}

// writeRowsAsThePreviousRelease has the stand-in for the release before this one write, with no
// node and no event, a file uploaded to rollSession's conversation, an ask on CORE-1 and a direct
// message from alice to that session, and answers their ids.
func writeRowsAsThePreviousRelease(t *testing.T, database *store.Store) (file, ask, message string) {
	t.Helper()
	ctx := context.Background()
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (session_id, slug, name, kind, created_by)
		values ($1, 'rolled-png', 'rolled.png', 'image', jsonb_build_object('kind', 'session', 'id', $1::text))
		returning id::text
	`, rollSession).Scan(&file); err != nil {
		t.Fatalf("upload the conversation's file as the release before: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		insert into asks (issue_key, author, question) values ('CORE-1', '{"kind":"user","id":"alice"}', 'Does the roll keep it?')
		returning id::text
	`).Scan(&ask); err != nil {
		t.Fatalf("open the ask as the release before: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		insert into messages (issue_key, author, body, target) values (null, '{"kind":"user","id":"alice"}', 'Directly.', 'session:' || $1)
		returning id::text
	`, rollSession).Scan(&message); err != nil {
		t.Fatalf("send the direct message as the release before: %v", err)
	}
	return file, ask, message
}

// refKeyOf is the ref_key of the artifacts row id, the address of its node.
func refKeyOf(t *testing.T, database *store.Store, id string) string {
	t.Helper()
	var ref string
	if err := database.Pool.QueryRow(context.Background(), `select ref_key from artifacts where id = $1::uuid`, id).Scan(&ref); err != nil {
		t.Fatalf("read the address of artifact %s: %v", id, err)
	}
	return ref
}

// requireProjectComplete fails unless the project census names exactly the rows want (none: the
// census reads every row complete), each as `<kind> <id>: <class>`, when names the moment.
func requireProjectComplete(t *testing.T, database *store.Store, want []string, when string) {
	t.Helper()
	incomplete, err := graphcensus.ProjectComplete(context.Background(), database.Pool)
	if err != nil {
		t.Fatalf("take the project census %s: %v", when, err)
	}
	var named []string
	for _, line := range incomplete {
		for _, row := range want {
			if len(line) >= len(row) && line[:len(row)] == row {
				named = append(named, row)
			}
		}
	}
	if len(incomplete) != len(want) || len(named) != len(want) {
		t.Fatalf("the project census names %q %s, want exactly %q", incomplete, when, want)
	}
}

// requireTheRollTicksObjectPassLockOrder holds the block-index tick's object pass to the store's
// lock order at the tick's own call site (writeObjectNodesAnotherBuildWrote), as graphcensus's
// TestEveryBackfillBatchTakesTheStoreLockOrder holds writeObjectBatch under the backfill: the tick
// runs on a pool of one connection, so every statement of it goes through the backend the test
// reads, while a process of the release before serves. The pass takes the owner row of the
// document the release before uploaded, then the document's node row, then appends its tuples
// under the document's own artifact.created event, and never the project's row. While it waits
// behind the owner row it may hold no lock of the marking that runs before it in the same tick:
// the pass runs after the marking's transaction commits, so the cursor row and the
// document_block_versions rows the marking locks are let go of first.
//
// Its controls are the pass run inside the marking's transaction, which holds the cursor row
// while it waits behind the owner row (on one connection the pass then waits for its own
// connection and the run never answers), and writeObjectBatch without its owner rows, which never
// queues behind the owner row.
func requireTheRollTicksObjectPassLockOrder(t *testing.T) {
	database, _ := blockIndexService(t)
	ctx := context.Background()
	alice := model.Actor{Kind: "user", ID: "alice"}
	now := time.Now()
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		return graphstore.Write(ctx, tx, []graphstore.Node{
			graphstore.IssueNode("CORE-1", "CORE", alice, now), graphstore.IssueNode("CORE-2", "CORE", alice, now),
		}, nil)
	}); err != nil {
		t.Fatalf("write the issues' nodes: %v", err)
	}
	var document, ref string
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (issue_key, project_key, slug, name, kind, is_primary, created_by)
		values ('CORE-1', 'CORE', 'rolled', 'rolled.md', 'doc', false, '{"kind":"user","id":"alice"}')
		returning id::text, ref_key
	`).Scan(&document, &ref); err != nil {
		t.Fatalf("upload the document as the release before: %v", err)
	}
	var created int64
	if err := database.Pool.QueryRow(ctx, `
		insert into events (issue_key, seq, type, actor, payload, notify)
		select 'CORE-1', coalesce(max(seq), 0) + 1, 'artifact.created', '{"kind":"user","id":"alice"}',
		       jsonb_build_object('artifact', jsonb_build_object('id', $1::text, 'issue_key', 'CORE-1')), false
		from events where issue_key = 'CORE-1'
		returning id
	`, document).Scan(&created); err != nil {
		t.Fatalf("append the upload's event as the release before: %v", err)
	}
	registerProcess(t, database, previousReleaseCommit)

	ticker := storetest.SingleConnection(t, database)
	service := blockIndexProcess(t, ticker, "")
	var pid uint32
	if err := ticker.Pool.QueryRow(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read the tick's backend: %v", err)
	}
	// The horizon is proven before the tick, so its marking starts this build's cursor and takes
	// its row, and the document_block_versions row the upload's event names, in a transaction of
	// its own: what the pass must let go of before it takes an owner row. The horizon observes
	// through a pool of its own (blockIndexHorizon), so the one connection stays the tick's.
	awaited, cancelAwait := context.WithTimeout(ctx, time.Minute)
	defer cancelAwait()
	if _, err := service.blockIndexHorizon().Await(awaited); err != nil {
		t.Fatalf("await the event log's safe horizon: %v", err)
	}

	owner := lockstep.HoldRow(t, database, &lockstep.Hold{
		Name: "issue CORE-1's row", Step: lockstep.StepOwner, Taken: true, Kind: "issue", ID: "CORE-1",
		Statement: "select closed_at is null from issues where key = $1 for no key update",
		Unlocked:  []string{"graph_nodes", "graph_tuples", "block_index_event_cursors", "document_block_versions"},
	}, `select 1 from issues where key = $1 for update`, "CORE-1")
	node := lockstep.Hold{
		Name: ref + "'s node row", Step: lockstep.StepNode, Taken: true,
		Kind: graphstore.KindArtifact, ID: ref, Statement: "with incoming as (",
	}
	lockstep.Begin(t, database, &node)
	if err := graphstore.Write(ctx, node.Tx, []graphstore.Node{graphstore.ArtifactNode(ref, "CORE", "", alice, now)}, nil); err != nil {
		t.Fatalf("hold %s: %v", node.Name, err)
	}
	state := lockstep.Hold{Name: ref + "'s state tuple", Step: lockstep.StepTuple, Taken: true, Statement: "insert into graph_tuples ("}
	lockstep.Begin(t, database, &state)
	if err := graphstore.Write(ctx, state.Tx, nil, []graphstore.Tuple{{
		FromKind: graphstore.KindArtifact, FromID: ref, Kind: graphstore.AttributeState,
		ToKind: graphstore.KindValue, ToID: graphstore.StateLive,
		At: now, Who: alice, SourceEvent: created, Asserted: true,
	}}); err != nil {
		t.Fatalf("hold %s: %v", state.Name, err)
	}
	project := lockstep.HoldRow(t, database, &lockstep.Hold{Name: "project CORE's row", Step: lockstep.StepEvent},
		`select 1 from projects where key = $1 for update`, "CORE")

	// A tick that waits on its own connection never answers on its own; its context ends it
	// after the holds are let go, so a failed run lets the test's pool close.
	running, stop := context.WithTimeout(ctx, 2*lockstep.Wait)
	defer stop()
	done := make(chan error, 1)
	var tick BlockIndexTick
	go func() {
		var err error
		tick, err = service.ReconcileBlockIndex(running, BlockIndexCursors{}, liveAheadDocuments{})
		done <- err
	}()
	lockstep.Follow(t, database, "the roll tick's object pass", pid, done, []*lockstep.Hold{owner, &node, &state, project})

	if !tick.AnotherBuild || tick.ObjectNodes != 1 {
		t.Errorf("the roll tick wrote %d object node(s) and found another build %t, want the document's node and another build", tick.ObjectNodes, tick.AnotherBuild)
	}
	// The marking's transaction ran and committed in this tick, holding the cursor row while it
	// did: it started the cursor at the horizon and recorded the other build.
	if cursor := blockIndexEventCursor(t, database, ""); !cursor.anotherBuildSeen {
		t.Errorf("the tick's marking left the cursor at %+v, want another build seen: the pass was not followed after the marking's transaction", cursor)
	}
	held, found, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindArtifact, ref)
	if err != nil || !found || held.State != graphstore.StateLive || held.Project != "CORE" || held.SourceEvent != created {
		t.Errorf("the node of %s is %+v (held %t, %v), want it live in CORE under event %d: the run the test followed is not one that committed",
			ref, held, found, err, created)
	}
}
