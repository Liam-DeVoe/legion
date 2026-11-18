package api

import (
	"context"
	"net/http"
	"testing"
)

// TestEveryObjectNodeWriterTakesTheStoreLockOrder holds the create sites that write a message's
// node, in its project, to the store's lock order, as TestEveryApiStoreWriterTakesTheStoreLockOrder
// holds the comment paths, with the same machinery: each path lists its steps in the order it takes
// them, the list is checked against the written order (requireTheWrittenOrderAdmits), and each step
// is held in turn while the path's own backend is read at the statement it waits at and the table
// locks it holds (requireEachStepIsWaitedAtInTurn). The creates of an issue, a document and an ask
// write their object's node the same way, and TestEveryApiStoreWriterTakesTheStoreLockOrder holds
// each of them to these steps beside its block index and its follow (issueCreate, documentUpload,
// askCreate).
//
// What each path is held to is the order this slice adds: the object's node is written before the
// row that holds the object, the event is appended after that row, and the facts the node write
// stated are appended after the event that causes them. The id of each object is drawn inside its
// own transaction, so no other transaction can hold its node or its row before they exist: those
// steps are held as the tables they are written to.
func TestEveryObjectNodeWriterTakesTheStoreLockOrder(t *testing.T) {
	ctx := context.Background()
	rig := newStoreWriterRig(t)
	for _, writer := range []storeWriter{
		rig.objectCreate("createStoredMessage on an issue", "the message", "messages", "insert into messages (",
			"/api/v1/issues/"+rig.issue+"/messages", map[string]any{"body": "A message the lock steps hold."}),
	} {
		t.Run(writer.name, func(t *testing.T) {
			requireTheWrittenOrderAdmits(t, writer)
			requireEachStepIsWaitedAtInTurn(t, ctx, rig, writer)
		})
	}
}

// objectNodeSteps are the steps every create here takes after its owner row: the object's node and
// the row that holds it (objectRowSteps), the event, and the facts the node write stated, appended
// under it.
func objectNodeSteps(object, table, insert string) []storeStep {
	return append(objectRowSteps(object, table, insert), eventAppendStep(), tupleAppendStep())
}

// objectRowSteps are the object's node, written before the row that holds it, and that row, which
// names the node.
func objectRowSteps(object, table, insert string) []storeStep {
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
	}
}

// objectCreate is a create through route, which takes objectNodeSteps.
func (r *storeWriterRig) objectCreate(name, object, table, insert, route string, body map[string]any) storeWriter {
	return storeWriter{
		name:  name,
		steps: objectNodeSteps(object, table, insert),
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			return storeWriterRun{write: func() error {
				return statusOrError(dispatchRequest(t, r.call, http.MethodPost, route, body, "alice"), http.StatusCreated)
			}}
		},
	}
}
