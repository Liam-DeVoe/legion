package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// TestEveryObjectNodeWriterTakesTheStoreLockOrder holds the create sites that write an issue's, a
// document's, an ask's and a message's node (dispatch://INFRA-3862), and a broadcast's copies, to
// the store's lock order, as TestEveryApiStoreWriterTakesTheStoreLockOrder holds the comment paths,
// with the same machinery: each path lists its steps in the order it takes them, the list is
// checked against the written order (requireTheWrittenOrderAdmits), and each step is held in turn
// while the path's own backend is read at the statement it waits at and the table locks it holds
// (requireEachStepIsWaitedAtInTurn).
//
// What each path is held to is the order this slice adds: the object's node is written before the
// row that holds the object, an ask's or a message's body blocks are written as nodes after its
// block state row and before the block rows that name them (bodyBlockSteps), the event is appended
// after those rows, and the facts the node write stated are appended after the event that causes
// them. An ask's create, a message and a broadcast's copy then run the one writer of a thread
// (inboxwrite.Record), whose user_threads steps each lists after its event. The id of each object
// is drawn inside its own transaction, so no other transaction can hold its node or its row before
// they exist: those steps are held as the tables they are written to, and its first block's node by
// the key its row's insert names (holdFirstBlockNode).
//
// Its control is the ask create's node write skipped (api/asks.go): held at the ask's node, the
// path then waits at the author follow's node write rather than its own, or at none.
func TestEveryObjectNodeWriterTakesTheStoreLockOrder(t *testing.T) {
	ctx := context.Background()
	rig := newStoreWriterRig(t)
	for _, writer := range []storeWriter{
		rig.objectCreate("createAskFor on an issue", "the ask", "asks", "insert into asks (",
			[]storeStep{rig.issueOwnerStep(issueOwnerStatement)}, rig.bodyBlockSteps("the ask", refs.ItemAsk, "insert into asks ("),
			[]storeStep{ownRowStep()},
			"/api/v1/issues/"+rig.issue+"/asks", map[string]any{"question": "Does the lock order hold?"}),
		rig.objectCreate("createStoredMessage on an issue", "the message", "messages", "insert into messages (",
			nil, rig.bodyBlockSteps("the message", refs.ItemMessage, "insert into messages ("),
			[]storeStep{threadRowsStep(), ownRowStep()},
			"/api/v1/issues/"+rig.issue+"/messages", map[string]any{"body": "A message the lock steps hold."}),
		rig.objectCreate("storeArtifact on an issue", "the document", "artifacts", "insert into artifacts (",
			nil, nil, nil, "/api/v1/issues/"+rig.issue+"/artifacts", nil),
		rig.issueCreate(),
		rig.broadcastCreate(),
	} {
		t.Run(writer.name, func(t *testing.T) {
			requireTheWrittenOrderAdmits(t, writer)
			requireEachStepIsWaitedAtInTurn(t, ctx, rig, writer)
		})
	}
}

// objectNodeSteps are the steps every create here takes after its owner row: the object's node,
// the row that holds it, the steps of its body's blocks where it has a body (parts), the event,
// and the facts the node write stated, appended under it.
func objectNodeSteps(object, table, insert string, parts []storeStep) []storeStep {
	node := object + "'s node"
	steps := []storeStep{
		{
			name: node, order: nodeStep,
			hold: holdTableWrites("graph_nodes"), at: nodeWriteStatement,
			takes: []tableLock{{"graph_nodes", rowExclusiveMode}},
		},
		{
			name: object + "'s row", order: projectionStep, names: node,
			hold: holdTableWrites(table), at: insert,
			takes: []tableLock{{table, rowExclusiveMode}},
		},
	}
	return append(append(steps, parts...), eventAppendStep(), tupleAppendStep())
}

// objectCreate is a create through route, which takes the owner steps it lists, then
// objectNodeSteps, then the steps of the one writer of a thread it lists (recorded): an ask's
// create keeps its asker's own user_threads row, and a message's write takes every row of its
// thread in one pass and then keeps its writer's (inboxwrite.Record), while a document upload
// records nothing in a thread. A nil body is a document upload, which each run names afresh, and
// whose row's foreign key into projects takes that project's row in key share mode.
func (r *storeWriterRig) objectCreate(name, object, table, insert string, owner, parts, recorded []storeStep, route string, body map[string]any) storeWriter {
	steps := append(append([]storeStep(nil), owner...), objectNodeSteps(object, table, insert, parts)...)
	return storeWriter{
		name:            name,
		steps:           append(steps, recorded...),
		locksOwnProject: body == nil,
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			sent := body
			if sent == nil {
				sent = map[string]any{"name": r.fresh("lock-notes") + ".md", "content": "# Notes\n\nThe lock steps hold.\n"}
			}
			return storeWriterRun{write: func() error {
				return createdOrError(dispatchRequest(t, r.call, http.MethodPost, route, sent, "alice"))
			}}
		},
	}
}

// issueCreate is the issue create: one node write of the issue's node and its spec's, then the
// issue's row and the spec's row, the issue.created event and the facts it caused. It numbers the
// issue under its project's row, which is not the project of its event. Each run's title and spec
// share no word with another's, so the create's duplicate check lets it through.
func (r *storeWriterRig) issueCreate() storeWriter {
	steps := objectNodeSteps("the issue", "issues", "insert into issues (", nil)
	spec := storeStep{
		name: "the spec's row", order: projectionStep, names: "the issue's node",
		hold: holdTableWrites("artifacts"), at: "insert into artifacts (",
		takes: []tableLock{{"artifacts", rowExclusiveMode}},
	}
	steps = append(steps[:2], append([]storeStep{spec}, steps[2:]...)...)
	return storeWriter{
		name: "createIssue", steps: steps, locksOwnProject: true,
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			title, spec := uuid.NewString(), uuid.NewString()+"\n"
			return storeWriterRun{write: func() error {
				return createdOrError(dispatchRequest(t, r.call, http.MethodPost, "/api/v1/issues",
					map[string]any{"project": "LOCK", "title": title, "spec": spec}, "alice"))
			}}
		},
	}
}

// broadcastCreate is a broadcast to s2 and s1, named in that order (writeBroadcast), each of which
// hangs its copy from the page of the session it reaches. It takes every recipient's page first,
// in one node write, in ascending (kind, id), before any copy's node (resolveRecipientPages):
// s1's page sorts first, so a hold of s1's page row stops the write at that node write holding no
// copy's row and no event. A write that took the pages copy by copy, in the order the request
// names its recipients, would have written s2's copy whole - its node and s2's page, its row, its
// event and the tuples it caused - before it waited on s1's page, and two such writes naming the
// same recipients in the other order would each hold one page the other waits for. After the
// pages come the first copy's row, its message.created, the tuples that event caused and the one
// writer of the copy's thread (inboxwrite.Record), whose pass over the thread's user_threads rows
// finds none, since a copy on no issue keeps no row. A broadcast takes no owner row: its copies
// stand on no issue, and their events name no owner (events.Broker's lockEventOwner).
//
// The pages are there before every run (writeRecipientPages), so the hold is a lock of a row the
// store holds, as a node write locks a page another write made. Each run waits out its own copies'
// deliveries, which run behind its answer on the rig's one connection, so none of them stands in
// front of the next run's write.
func (r *storeWriterRig) broadcastCreate() storeWriter {
	first := graphstore.SessionPageID("s1")
	return storeWriter{
		name: "writeBroadcast to s2 and s1",
		steps: []storeStep{
			{
				name: "the recipients' pages", order: nodeStep,
				hold: func(t *testing.T, ctx context.Context, holder pgx.Tx, _ storeWriterRun) {
					holdRow(t, ctx, holder, `select 1 from graph_nodes where kind = '`+graphstore.KindPage+`' and id = $1 for update`, first)
				},
				at:    nodeWriteStatement,
				takes: []tableLock{{"graph_nodes", rowExclusiveMode}},
			},
			{
				name: "the first copy's row", order: projectionStep,
				hold: holdTableWrites("messages"), at: "insert into messages (",
				takes: []tableLock{{"messages", rowExclusiveMode}},
			},
			eventAppendStep(),
			tupleAppendStep(),
			threadRowsStep(),
		},
		ready: func(t *testing.T, ctx context.Context) storeWriterRun {
			r.writeRecipientPages(t)
			return storeWriterRun{write: func() error {
				response := keyedRequest(t, r.call, http.MethodPost, "/api/v1/broadcasts", map[string]any{
					"body": "A broadcast the lock steps hold.", "delivery": "steer", "session_ids": []string{"s2", "s1"},
				}, "alice", uuid.NewString())
				if err := createdOrError(response); err != nil {
					return err
				}
				var sent struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &sent); err != nil {
					return fmt.Errorf("decode the broadcast: %w", err)
				}
				return r.broadcastSettled(ctx, sent.ID)
			}}
		},
	}
}

// writeRecipientPages has the store hold the pages of s1 and s2, as the first message to each
// leaves them: a broadcast to both, through the rig's own server, whose deliveries it waits out.
func (r *storeWriterRig) writeRecipientPages(t *testing.T) {
	t.Helper()
	response := keyedRequest(t, r.handler, http.MethodPost, "/api/v1/broadcasts", map[string]any{
		"body": "Pages for the lock steps.", "delivery": "steer", "session_ids": []string{"s1", "s2"},
	}, "alice", uuid.NewString())
	if response.Code != http.StatusCreated {
		t.Fatalf("broadcast to s1 and s2: status=%d body=%s", response.Code, response.Body.String())
	}
	awaitBroadcastDeliveries(t, r.handler, decodeBody[broadcastResponse](t, response).ID)
}

// broadcastSettled waits until no copy of broadcast id is owed a send, reading through the rig's
// own pool, and answers an error once storeStepWait passes first.
func (r *storeWriterRig) broadcastSettled(ctx context.Context, id string) error {
	deadline := time.Now().Add(storeStepWait)
	for {
		var pending int
		if err := r.database.Pool.QueryRow(ctx, `
			select count(*) from message_deliveries d join messages m on m.id = d.message_id
			where m.broadcast_id = $1::uuid and d.state = 'pending'
		`, id).Scan(&pending); err != nil {
			return fmt.Errorf("read the deliveries of broadcast %s: %w", id, err)
		}
		if pending == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("broadcast %s still owes %d send(s) after %s", id, pending, storeStepWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
