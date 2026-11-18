package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestEveryAskTransitionAndKeepTakesTheStoreLockOrder holds the routes of this package that write
// an ask's answer state through asks.Settle, and the Keep of an agent's decision, to the store's
// lock order, with the machinery of TestEveryApiStoreWriterTakesTheStoreLockOrder: each path
// lists its steps in the order it takes them, the list is checked against the order written on
// api/inbox_read.go's markInboxReadThreads (requireTheWrittenOrderAdmits), and each step is held
// in turn while the path's own backend is read at the statement it waits at and at the table
// locks it holds (requireEachStepIsWaitedAtInTurn). The paths are:
//
//   - a person's answer of an open ask (closeAskTx, through the answer route): the issue's row,
//     the ask's row, then the answerer's node, which asks.Settle writes before the row it settles,
//     the ask's row written, the ask.answered, and the answer's facts under it;
//   - the close of an issue that holds an open ask (recordWithdrawnAsksTx, through the issue's
//     PATCH): the issue's row, the ask's row, the ask's row written withdrawn, the ask.resolved,
//     and the withdrawal's facts under it, which come before the issue.closed;
//   - the reopen of that issue (restoreIssueAsksTx): the issue's row, the withdrawn ask's row, the
//     ask's row written open, the ask.opened, and the restoration's facts under it;
//   - a person's Keep of an agent's decision (keepDecisionNode, through the Keep route): the
//     issue's row, the person's node, the decision_keeps row that projects the Keep, the
//     decision.kept, and the kept_by and kept_text_sum under it.
//
// The ask's row is the row its node projects (asks.node_kind), and none of these paths writes
// that node, so no node step orders the row; the answerer, whose node an answer writes, is the far
// end of a tuple, which no row names. A Keep's decision_keeps row names its keeper (kept_by), whose
// node the Keep writes before it. Each run answers, closes or keeps something of its own, made
// just before it through the rig's own server; the answer and the Keep are a person's no write
// has recorded, whose node is held as a row another transaction inserted and has not committed.
// Every other write of a step is the path's first of its table, held as that table.
func TestEveryAskTransitionAndKeepTakesTheStoreLockOrder(t *testing.T) {
	ctx := context.Background()
	rig := newStoreWriterRig(t)
	for _, writer := range []storeWriter{
		rig.askAnswer(), rig.issueCloseWithdrawing(), rig.issueReopenRestoring(), rig.decisionKeep(t, ctx),
	} {
		t.Run(writer.name, func(t *testing.T) {
			requireTheWrittenOrderAdmits(t, writer)
			requireEachStepIsWaitedAtInTurn(t, ctx, rig, writer)
		})
	}
}

// askRowStatement is the head of the lock of an ask's row a transition takes before it settles
// the ask (lockAskForTransition), and of the lock of an issue's asks its close or reopen takes
// (readOpenIssueAsks, restoreIssueAsksTx).
var askRowStatement = "select " + askRowColumns

// settledRowStatement is asks.Settle's write of an ask's row.
const settledRowStatement = "update asks set state = $2,"

// patchOwnerStatement is the issue PATCH's lock of the issue's row (lockPatchRowsAndParent), its
// first.
const patchOwnerStatement = "select parent_key from issues where key = $1 for no key update"

// askRowSteps are the two steps every path here takes of the ask it settles: the row locked
// `for no key update`, held as a row another transaction locks `for update`, and the row written
// by asks.Settle after the node it states, held as the table, which no row lock conflicts with.
// ask is the run's ask, which the lock names.
func askRowSteps(ask *string) []storeStep {
	return []storeStep{
		{
			name: "the ask's row", order: projectionStep,
			hold: func(t *testing.T, ctx context.Context, holder pgx.Tx, _ storeWriterRun) {
				holdRow(t, ctx, holder, `select 1 from asks where id = $1::uuid for update`, *ask)
			},
			at:    askRowStatement,
			takes: []tableLock{{"asks", rowShareMode}},
		},
		{
			name: "the ask's row written", order: projectionStep,
			hold: holdTableWrites("asks"), at: settledRowStatement,
			takes: []tableLock{{"asks", rowExclusiveMode}},
		},
	}
}

// askAnswer is a person's answer of an open ask of the issue (closeAskTx), through its route. It
// takes the issue's row (requireOpenOwner), locks the ask's row, and settles it (asks.Settle): the
// answerer's node, held for the event, and then the ask's row. It indexes the answer's part, then
// appends ask.answered and the answer's facts under it (graphstore.Caused). Each run answers an
// ask of its own, opened just before it.
func (r *storeWriterRig) askAnswer() storeWriter {
	var ask string
	rows := askRowSteps(&ask)
	return storeWriter{
		name: "closeAskTx answering",
		steps: []storeStep{
			r.issueOwnerStep(issueOwnerStatement),
			rows[0],
			{
				name: "the answerer's node", order: nodeStep,
				hold: holdNewNode, at: nodeWriteStatement,
				takes: []tableLock{{"graph_nodes", rowExclusiveMode}},
			},
			rows[1],
			eventAppendStep(),
			tupleAppendStep(),
		},
		ready: func(t *testing.T, ctx context.Context) storeWriterRun {
			ask = decodeBody[model.Ask](t, created(t, "open the ask the run answers", dispatchRequest(t, r.handler, http.MethodPost,
				"/api/v1/issues/"+r.issue+"/asks", map[string]any{"question": r.fresh("Which way do the lock steps answer, number") + "?"}, "alice"))).ID
			login := r.fresh("lock-answerer")
			answered := ask
			return storeWriterRun{
				made: graphstore.PersonNode(login, model.Actor{Kind: "user", ID: login}, time.Now().UTC(), graphstore.NoEvent),
				write: func() error {
					return statusOrError(dispatchRequest(t, r.call, http.MethodPost, "/api/v1/asks/"+answered+"/answer",
						map[string]any{"text": "This way."}, login), http.StatusOK)
				},
			}
		},
	}
}

// plainIssueWithAsk creates an issue of the rig's project with no spec, which gives it an empty
// primary document, and an open ask on it, and answers both. It settles that document at once: a
// close settles the documents of its issue before its transaction (settleIssueBeforeClose), and
// a settlement owed then would take the issue's row on a backend of its own, behind the hold of
// the close's owner row, while the close waits for it.
func (r *storeWriterRig) plainIssueWithAsk(t *testing.T) (string, string) {
	t.Helper()
	issue := decodeBody[model.Issue](t, created(t, "create the issue the run closes", dispatchRequest(t, r.handler, http.MethodPost, "/api/v1/issues",
		map[string]any{"project": r.project, "title": r.fresh("The lock steps of a close, number"), "force": true}, "alice"))).Key
	ask := decodeBody[model.Ask](t, created(t, "open the ask the close withdraws", dispatchRequest(t, r.handler, http.MethodPost,
		"/api/v1/issues/"+issue+"/asks", map[string]any{"question": "Does the close withdraw this?"}, "alice"))).ID
	if err := r.direct.deps.Docs.SettleIssueDocuments(context.Background(), issue); err != nil {
		t.Fatalf("settle the documents of %s: %v", issue, err)
	}
	return issue, ask
}

// patchIssueSteps are the steps a close or a reopen takes: the issue's row, its ask's two, the
// ask's event and the facts its settle held for it. issue and ask are the run's.
func patchIssueSteps(issue, ask *string) []storeStep {
	return append(append([]storeStep{{
		name: "the owner row", order: ownerStep,
		hold: func(t *testing.T, ctx context.Context, holder pgx.Tx, _ storeWriterRun) {
			holdRow(t, ctx, holder, `select 1 from issues where key = $1 for no key update`, *issue)
		},
		at:    patchOwnerStatement,
		takes: []tableLock{{"issues", rowShareMode}},
	}}, askRowSteps(ask)...), eventAppendStep(), tupleAppendStep())
}

// issueCloseWithdrawing is the close of an issue that holds an open ask (recordWithdrawnAsksTx),
// through the issue's PATCH. It takes the issue's row (lockPatchRowsAndParent), writes it, locks
// the issue's open asks (readOpenIssueAsks) and withdraws each through asks.Settle, which states
// no node for a resolution and writes the ask's row, then appends ask.resolved and the withdrawal's
// facts under it, before the issue.closed. The ask.resolved's append writes the issue's row a
// second time (projectKeyShare). Each run closes an issue of its own.
func (r *storeWriterRig) issueCloseWithdrawing() storeWriter {
	var issue, ask string
	return storeWriter{
		name: "recordWithdrawnAsksTx closing", projectKeyShare: true,
		steps: patchIssueSteps(&issue, &ask),
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			issue, ask = r.plainIssueWithAsk(t)
			closing := issue
			return storeWriterRun{write: func() error {
				return statusOrError(dispatchRequest(t, r.call, http.MethodPatch, "/api/v1/issues/"+closing,
					map[string]any{"status": "done"}, "alice"), http.StatusOK)
			}}
		},
	}
}

// issueReopenRestoring is the reopen of an issue whose close withdrew its ask (restoreIssueAsksTx),
// through the issue's PATCH. It takes the issue's row, writes it, locks the withdrawn asks and
// opens each again through asks.Settle, which writes the ask's row, then indexes the ask's parts
// and appends ask.opened and the restoration's facts under it, before the issue.updated. The
// ask.opened's append writes the issue's row a second time (projectKeyShare). Each run reopens an
// issue of its own, closed just before it.
func (r *storeWriterRig) issueReopenRestoring() storeWriter {
	var issue, ask string
	return storeWriter{
		name: "restoreIssueAsksTx reopening", projectKeyShare: true,
		steps: patchIssueSteps(&issue, &ask),
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			issue, ask = r.plainIssueWithAsk(t)
			if closed := dispatchRequest(t, r.handler, http.MethodPatch, "/api/v1/issues/"+issue, map[string]any{"status": "done"}, "alice"); closed.Code != http.StatusOK {
				t.Fatalf("close %s before its reopen: status=%d body=%s", issue, closed.Code, closed.Body.String())
			}
			reopening := issue
			return storeWriterRun{write: func() error {
				return statusOrError(dispatchRequest(t, r.call, http.MethodPatch, "/api/v1/issues/"+reopening,
					map[string]any{"status": "todo"}, "alice"), http.StatusOK)
			}}
		},
	}
}

// decisionKeep is a person's Keep of an agent's decision (keepDecisionNode), through the Keep
// route, on a document of the issue the agent uploaded. It takes the issue's row (LockOwners),
// writes the person's node (graphstore.WriteUncaused), then the decision_keeps row that projects
// the Keep, appends decision.kept, and appends the kept_by and the kept_text_sum under it
// (graphstore.Caused). Each run keeps the decision as a person no write has recorded.
func (r *storeWriterRig) decisionKeep(t *testing.T, ctx context.Context) storeWriter {
	const name = "lock-steps-choices.md"
	requireAgentWrite(t, "upload the agent's decision", sessionRequest(t, r.handler, http.MethodPost, "/api/v1/issues/"+r.issue+"/artifacts", map[string]any{
		"name": name, "content": "::::decision{#d1}\nThe lock steps hold REST first.\n::::\n", "actor": sessionActor(),
	}), http.StatusCreated)
	choices := readDecisionDocument(t, r.database, r.issue, artifactSlug(name))
	document, err := r.direct.loadArtifact(ctx, r.database.Pool, choices.id)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	decision, found, err := r.direct.readDecisionNode(ctx, r.database.Pool, model.Actor{Kind: "user", ID: "alice"}, document, "d1")
	if err != nil || !found {
		t.Fatalf("read the decision d1 of %s: found %t, %v", name, found, err)
	}
	return storeWriter{
		name: "keepDecisionNode",
		steps: []storeStep{
			r.issueOwnerStep(eventOwnerStatement),
			{
				name: "the keeper's node", order: nodeStep,
				hold: holdNewNode, at: nodeWriteStatement,
				takes: []tableLock{{"graph_nodes", rowExclusiveMode}},
			},
			{
				name: "the Keep's row", order: projectionStep, names: "the keeper's node",
				hold: holdTableWrites("decision_keeps"), at: "insert into decision_keeps (",
				takes: []tableLock{{"decision_keeps", rowExclusiveMode}},
			},
			eventAppendStep(),
			tupleAppendStep(),
		},
		ready: func(t *testing.T, _ context.Context) storeWriterRun {
			login := r.fresh("lock-keeper")
			return storeWriterRun{
				made: graphstore.PersonNode(login, model.Actor{Kind: "user", ID: login}, time.Now().UTC(), graphstore.NoEvent),
				write: func() error {
					return statusOrError(dispatchRequest(t, r.call, http.MethodPost, "/api/v1/artifacts/"+choices.id+"/blocks/d1/keep",
						map[string]any{"text_sha256": decision.TextSHA256}, login), http.StatusOK)
				},
			}
		},
	}
}
