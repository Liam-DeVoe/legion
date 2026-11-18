package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestEveryObjectNodeWriterTakesTheStoreLockOrder holds the create sites that write an issue's, a
// document's, an ask's and a message's node, in its project or between its parties, to the
// store's lock order, as TestEveryApiStoreWriterTakesTheStoreLockOrder holds the comment paths,
// with the same machinery: each path lists its steps in the order it takes them, the list is
// checked against the written order (requireTheWrittenOrderAdmits), and each step is held in turn
// while the path's own backend is read at the statement it waits at and the table locks it holds
// (requireEachStepIsWaitedAtInTurn). The paths are the issue create, a question, a message and a
// document on an issue, an approval request (openApprovalAsk), the ask a settled document's new
// ask block opens (createAskBlock), a broadcast's copy (writeBroadcast) and a session's reply to
// a message (insertSessionReply).
//
// What each path is held to is the order this slice adds: its owner rows first, then the
// object's node, written before the row that holds the object, the event, appended after that
// row, and the facts the node write stated, appended after the event that causes them. The id of
// each object is drawn inside its own transaction, so no other transaction can hold its node or
// its row before they exist: those steps are held as the tables they are written to.
//
// Its controls are the ask create's node write skipped (api/asks.go): held at the ask's node, the
// path then waits at the author follow's node write rather than its own, or at none; and the
// session reply's node write moved after its row (api/message_delivery.go insertSessionReply):
// held at the reply's node, the path then already holds the lock its row takes, and held at its
// row, it holds no lock of its node.
func TestEveryObjectNodeWriterTakesTheStoreLockOrder(t *testing.T) {
	ctx := context.Background()
	rig := newStoreWriterRig(t)
	for _, writer := range []storeWriter{
		rig.objectCreate("createAskFor on an issue", "the ask", "asks", "insert into asks (",
			[]storeStep{rig.issueOwnerStep(issueOwnerStatement)},
			"/api/v1/issues/"+rig.issue+"/asks", map[string]any{"question": "Does the lock order hold?"}),
		rig.objectCreate("createStoredMessage on an issue", "the message", "messages", "insert into messages (",
			nil, "/api/v1/issues/"+rig.issue+"/messages", map[string]any{"body": "A message the lock steps hold."}),
		rig.objectCreate("storeArtifact on an issue", "the document", "artifacts", "insert into artifacts (",
			nil, "/api/v1/issues/"+rig.issue+"/artifacts", nil),
		rig.issueCreate(),
		rig.approvalRequest(),
		rig.askBlockSettle(t),
		rig.broadcastCopy(),
		rig.sessionReply(),
	} {
		t.Run(writer.name, func(t *testing.T) {
			requireTheWrittenOrderAdmits(t, writer)
			requireEachStepIsWaitedAtInTurn(t, ctx, rig, writer)
		})
	}
}

// objectNodeSteps are the steps every create here takes after its owner row: the object's node,
// the row that holds it, the event, and the facts the node write stated, appended under it.
func objectNodeSteps(object, table, insert string) []storeStep {
	node := object + "'s node"
	return []storeStep{
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
		eventAppendStep(),
		tupleAppendStep(),
	}
}

// objectCreate is a create through route, which takes the owner steps it lists and then
// objectNodeSteps. A nil body is a document upload, which each run names afresh, and whose row's
// foreign key into projects takes that project's row in key share mode.
func (r *storeWriterRig) objectCreate(name, object, table, insert string, owner []storeStep, route string, body map[string]any) storeWriter {
	return storeWriter{
		name:            name,
		steps:           append(append([]storeStep(nil), owner...), objectNodeSteps(object, table, insert)...),
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
	steps := objectNodeSteps("the issue", "issues", "insert into issues (")
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

// approvalRequest is a session's request for approval of a document of the issue
// (openApprovalAsk), through its route: it takes the issue's row, as every owner check does, then
// writes the request's node, its row, the ask.opened and the facts the node write stated. Each run
// asks about a document of its own, just uploaded, since a request on a document with one open
// renews that one rather than opening another.
func (r *storeWriterRig) approvalRequest() storeWriter {
	return storeWriter{
		name: "openApprovalAsk",
		steps: append([]storeStep{r.issueOwnerStep(issueOwnerStatement)},
			objectNodeSteps("the approval request", "asks", "insert into asks (")...),
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			document := uploadDocument(t, r.handler, "/api/v1/issues/"+r.issue, r.fresh("approval")+".md",
				"# Approval\n\nWhat the request asks about.\n").Artifact.ID
			return storeWriterRun{write: func() error {
				return createdOrError(bearerRequest(t, r.call, http.MethodPost, "/api/v1/artifacts/"+document+"/approval-requests",
					map[string]any{"actor": map[string]any{"kind": "session", "id": "s1"}}))
			}}
		},
	}
}

// settlementOwnerStatement is lockArtifactOwner's lock of an issue document's owner, the first
// row a settlement locks (docs' settleRoomWithin).
const settlementOwnerStatement = "select closed_at is null from issues where key = $1 for no key update"

// askBlockSettle is the settlement of a document of the issue that holds an ask block no
// settlement has opened: it takes the issue's row (lockArtifactOwner), writes the ask's node and
// then its row (createAskBlock), appends the ask.opened with the settlement's other events, and
// then the facts the node write stated, under that event (causeFollows). Each run uploads a
// document holding a block of its own and settles it in settler, a document service of its own on
// the rig's one connection, so the settlement's transaction runs on the backend the test watches;
// the rig's own service settles an hour on, never inside the test. A settlement runs on the
// service's own goroutine, so a run's write ends once the block's ask is there, which the
// settlement's commit puts there.
func (r *storeWriterRig) askBlockSettle(t *testing.T) storeWriter {
	settler := docs.New(docs.Deps{Store: r.callStore, Events: r.direct.deps.Events, ServerURL: testServerURL, Settle: time.Hour})
	t.Cleanup(func() {
		if err := settler.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down the settling document service: %v", err)
		}
	})
	return storeWriter{
		name: "createAskBlock",
		steps: append([]storeStep{r.issueOwnerStep(settlementOwnerStatement)},
			objectNodeSteps("the ask", "asks", "insert into asks (")...),
		ready: func(t *testing.T, ctx context.Context) storeWriterRun {
			block := r.fresh("lock-block")
			document := uploadDocument(t, r.handler, "/api/v1/issues/"+r.issue, block+".md",
				"# Decision\n\n"+askBlockMarkdown(block, "Does the settled ask take the lock order?")).Artifact.ID
			return storeWriterRun{write: func() error {
				settler.SettleNow(document)
				return r.askBlockOpened(ctx, document, block)
			}}
		},
	}
}

// askBlockOpened waits for the ask of block on document, which the commit of the settlement that
// opens it puts there, and answers an error once the settlement has had longer than any run waits
// for it.
func (r *storeWriterRig) askBlockOpened(ctx context.Context, document, block string) error {
	within := 3 * storeStepWait
	for deadline := time.Now().Add(within); ; time.Sleep(5 * time.Millisecond) {
		var open bool
		if err := r.database.Pool.QueryRow(ctx, `
			select exists (select 1 from asks where block_artifact_id = $1::uuid and block_id = $2)
		`, document, block).Scan(&open); err != nil {
			return fmt.Errorf("read the ask of block %s: %w", block, err)
		}
		if open {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the settlement of %s opened no ask for block %s within %s", document, block, within)
		}
	}
}

// broadcastCopy is a broadcast's copy to one session (writeBroadcast), called as the broadcast
// route calls it once its recipients are resolved: it writes the broadcast's row, which owns its
// copies, then the copy's node, the copy's row, its message.created and the facts the node write
// stated. It is called on the rig's direct server, so no delivery runs on the rig's one connection
// behind it; the attempt it opens stays pending, and nothing here sweeps it.
func (r *storeWriterRig) broadcastCopy() storeWriter {
	return storeWriter{
		name: "writeBroadcast",
		steps: append([]storeStep{{
			name: "the broadcast's row", order: ownerStep,
			hold: holdTableWrites("broadcasts"), at: "insert into broadcasts (",
			takes: []tableLock{{"broadcasts", rowExclusiveMode}},
		}}, objectNodeSteps("the copy", "messages", "insert into messages (")...),
		ready: func(t *testing.T, ctx context.Context) storeWriterRun {
			actor := model.Actor{Kind: "user", ID: "alice"}
			return storeWriterRun{write: func() error {
				_, _, err := r.direct.writeBroadcast(ctx, actor, "A broadcast the lock steps hold.", "btw",
					[]broadcastTarget{{sessionID: "s1", title: "planner"}}, []broadcastExclusion{})
				return err
			}}
		},
	}
}

// attemptRowStatement is the reply route's lock of the delivery attempt a session answers.
const attemptRowStatement = "select " + messageDeliveryColumns + " from message_deliveries where message_id = $1 and attempt = $2 for update"

// sessionReply is a session's reply to a message on the issue (insertSessionReply), through the
// reply route: it takes the issue's row (lockIssue) and then the attempt it answers `for update`,
// then writes the reply's node, the reply's row, the message.answered and the facts the node write
// stated. The attempt's row belongs to a projected table and names no node the reply writes, so
// the written order lets it come before the reply's node, as a mention reply's answered comment
// does (mentionReply). Each run answers a message of its own, sent to s1 and delivered, since an
// attempt already answered takes no second reply.
func (r *storeWriterRig) sessionReply() storeWriter {
	// parent is the message the run under way answers, which its ready sends and its attempt's
	// hold locks.
	var parent string
	return storeWriter{
		name: "insertSessionReply",
		steps: append([]storeStep{
			r.issueOwnerStep(issueOwnerStatement),
			{
				name: "the answered attempt's row", order: projectionStep,
				hold: func(t *testing.T, ctx context.Context, holder pgx.Tx, _ storeWriterRun) {
					holdRow(t, ctx, holder, `select 1 from message_deliveries where message_id = $1::uuid and attempt = 1 for update`, parent)
				},
				at:    attemptRowStatement,
				takes: []tableLock{{"message_deliveries", rowShareMode}},
			},
		}, objectNodeSteps("the reply", "messages", "insert into messages (")...),
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			parent = createIssueMessage(t, r.handler, r.issue, map[string]any{
				"body": "Does the reply take the lock order?", "target": "session:s1", "delivery": "steer",
			}, "alice").ID
			answered := parent
			return storeWriterRun{write: func() error {
				return createdOrError(bearerRequest(t, r.call, http.MethodPost, "/api/v1/messages/"+answered+"/reply", map[string]any{
					"actor": map[string]any{"kind": "session", "id": "s1"}, "attempt": 1, "body": "It does.",
				}))
			}}
		},
	}
}
