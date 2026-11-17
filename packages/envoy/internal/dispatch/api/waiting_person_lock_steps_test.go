package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestEveryMoveOfWhoAnAskWaitsOnTakesTheStoreLockOrder holds the writes that change who an ask
// waits on (dispatch://INFRA-1152 R2's move, inboxwrite.MoveWaiting, a site of graphstore.ToPerson)
// to the lock order written on api/inbox_read.go's markInboxReadThreads, with the machinery of
// TestEveryApiStoreWriterTakesTheStoreLockOrder: each lists its steps in the order it takes them,
// the list is checked against the written order (requireTheWrittenOrderAdmits), and each step is
// held in turn while the write's own backend is read at the statement it waits at and the table
// locks it holds (requireEachStepIsWaitedAtInTurn).
//
// Each takes its owner row first: a hand-on and a change of an issue's assignee the issue's row,
// a move of a project's default the project's row. A hand-on then locks the ask's row before it
// reads who the ask waited on; a change of an issue's assignee to nobody takes the project's row
// `for share` before it reads the default (inboxwrite.HoldDefault); and each move locks the asks
// rows it moves after its event and before the person nodes and tuples it appends in one
// statement, which is the step graph_tuples is held at.
func TestEveryMoveOfWhoAnAskWaitsOnTakesTheStoreLockOrder(t *testing.T) {
	ctx := context.Background()
	rig := newStoreWriterRig(t)
	for _, writer := range []storeWriter{
		rig.askHandOn(),
		rig.issueReassignment(false),
		rig.issueReassignment(true),
		rig.projectDefaultMove(),
	} {
		t.Run(writer.name, func(t *testing.T) {
			requireTheWrittenOrderAdmits(t, writer)
			requireEachStepIsWaitedAtInTurn(t, ctx, rig, writer)
		})
	}
}

// Each statement head a step of a move is read at.
const (
	// projectDefaultStatement is inboxwrite.HoldDefault's lock of the project row whose default an
	// ask falls through to.
	projectDefaultStatement = "select 1 from projects where key = $1 for share"
	// moveLockStatement is inboxwrite.MoveWaiting's lock of the asks rows a move may move.
	moveLockStatement = "select a.id::text from asks a where a.id in ("
	// askedOfWriteStatement is putAskAskedOf's write of the ask's asked_of.
	askedOfWriteStatement = "update asks set asked_of = $2"
	// patchOwnerStatement is lockIssueRows' lock of the issue a PATCH names.
	patchOwnerStatement = "select parent_key from issues where key = $1 for no key update"
	// issueWriteStatement is patchIssue's one update of the issue's row.
	issueWriteStatement = "update issues set"
	// defaultOwnerStatement is putProjectDefaultAssignee's read of the default it moves, under the
	// project row's lock.
	defaultOwnerStatement = "select default_assignee from projects where key = $1 for no key update"
	// defaultWriteStatement is putProjectDefaultAssignee's write of the default.
	defaultWriteStatement = "update projects set default_assignee = $2 where key = $1"
)

// projectDefaultStep is the project row an ask that falls through to the rig's project's default
// assignee is delivered by (inboxwrite.HoldDefault), `for share`, held by its row `for no key
// update`, the lock a move of the default takes.
func (r *storeWriterRig) projectDefaultStep() storeStep {
	return storeStep{
		name: "the project's row", order: projectStep,
		hold: func(t *testing.T, ctx context.Context, holder pgx.Tx, _ storeWriterRun) {
			holdRow(t, ctx, holder, `select 1 from projects where key = $1 for no key update`, "LOCK")
		},
		at:    projectDefaultStatement,
		takes: []tableLock{{"projects", rowShareMode}},
	}
}

// projectDefaultReader is writer, a write that takes the row of its project, whose default it
// reads, so a lock on projects is that step's and not the project of its event.
func (r *storeWriterRig) projectDefaultReader(writer storeWriter) storeWriter {
	writer.locksOwnProject = true
	return writer
}

// askRowStep is the row of the ask a write decides who waits on, locked `for no key update`
// (lockAskForTransition), held by that row.
func askRowStep() storeStep {
	return storeStep{
		name: "the ask's row", order: askStep,
		hold: func(t *testing.T, ctx context.Context, holder pgx.Tx, run storeWriterRun) {
			holdRow(t, ctx, holder, `select 1 from asks where id = $1::uuid for no key update`, run.ask)
		},
		at:    "select " + askRowColumns,
		takes: []tableLock{{"asks", rowShareMode}},
	}
}

// movedAsksStep is inboxwrite.MoveWaiting's lock of the asks rows the move may move, after the
// write's event, held by the row of the run's ask, one of them.
func movedAsksStep() storeStep {
	return storeStep{
		name: "the asks rows the move locks", order: askStep,
		hold: func(t *testing.T, ctx context.Context, holder pgx.Tx, run storeWriterRun) {
			holdRow(t, ctx, holder, `select 1 from asks where id = $1::uuid for no key update`, run.ask)
		},
		at:    moveLockStatement,
		takes: []tableLock{{"asks", rowShareMode}},
	}
}

// personTuplesStep is the move's one graphstore.ToPerson statement: the person nodes, `do
// nothing` where the store holds them, then the tuples, under the write's event. A person node may
// be the store's already, so graph_tuples is held: the statement takes graph_nodes first, then
// waits at graph_tuples.
func personTuplesStep() storeStep {
	return storeStep{
		name: "the person tuples", order: tupleStep, caused: true,
		hold: holdTableWrites("graph_tuples"), at: armsStatement,
		takes: []tableLock{{"graph_nodes", rowExclusiveMode}, {"graph_tuples", rowExclusiveMode}},
	}
}

// writeStep is a write of a row the write's owner step holds, held by its table's writes.
func writeStep(name, table, statement string) storeStep {
	return storeStep{
		name: name, order: projectionStep,
		hold: holdTableWrites(table), at: statement,
		takes: []tableLock{{table, rowExclusiveMode}},
	}
}

// signedIn is a person no write has recorded, signed in, so a write may hand them an ask, an issue
// or a project's default.
func (r *storeWriterRig) signedIn(t *testing.T, prefix string) string {
	t.Helper()
	login := r.fresh(prefix)
	seedPeople(t, r.database, login)
	return login
}

// askHandOn is putAskAskedOf: the issue's row, the ask's row, the ask's asked_of, the ask.asked_of
// event, then the move, whose lock of the ask re-takes the row the write holds, and its person
// tuples. Each run hands a fresh ask on the rig's issue, which waits on the issue's assignee, to a
// person no write has recorded.
func (r *storeWriterRig) askHandOn() storeWriter {
	return storeWriter{
		name: "putAskAskedOf",
		steps: []storeStep{
			r.issueOwnerStep(issueOwnerStatement),
			askRowStep(),
			writeStep("the ask's asked_of", "asks", askedOfWriteStatement),
			eventAppendStep(),
			personTuplesStep(),
		},
		ready: func(t *testing.T, ctx context.Context) storeWriterRun {
			ask := createIssueAsk(t, r.handler, r.issue, "Who takes it, "+r.fresh("hand-on")+"?")
			to := r.signedIn(t, "lock-hand-on")
			return storeWriterRun{ask: ask, write: func() error {
				return okOrError(dispatchRequest(t, r.call, http.MethodPut, "/api/v1/asks/"+ask+"/asked-of",
					map[string]any{"asked_of": to}, "alice"))
			}}
		},
	}
}

// issueReassignment is patchIssue's change of an issue's assignee: the issue's row, its write, the
// issue.updated event, then the move of its asks and its person tuples. One left to nobody takes
// its project's row `for share` after the issue's write and before its event, since its asks go
// to the project's default. Each run moves the one open ask of an issue of its own: from a person
// no write has recorded to another (a change), or from such a person to nobody (cleared). Either
// takes its project's row of its own besides: the event's update of the issue's last_seq writes
// the row this transaction already updated, and Postgres then checks the row's foreign key into
// projects again, `for key share`, whatever columns moved.
func (r *storeWriterRig) issueReassignment(cleared bool) storeWriter {
	name := "patchIssue changing the assignee"
	steps := []storeStep{
		ownerRowStep(patchOwnerStatement, "issues", `select 1 from issues where key = $1 for no key update`, ""),
		writeStep("the issue's row", "issues", issueWriteStatement),
	}
	if cleared {
		name = "patchIssue clearing the assignee"
		steps = append(steps, r.projectDefaultStep())
	}
	steps = append(steps, eventAppendStep(), movedAsksStep(), personTuplesStep())
	var issue, ask string
	// The owner step holds the issue this writer made, which exists only once the first run is
	// readied.
	steps[0].hold = func(t *testing.T, ctx context.Context, holder pgx.Tx, _ storeWriterRun) {
		holdRow(t, ctx, holder, `select 1 from issues where key = $1 for no key update`, issue)
	}
	writer := storeWriter{
		name:            name,
		steps:           steps,
		locksOwnProject: true,
		ready: func(t *testing.T, ctx context.Context) storeWriterRun {
			if issue == "" {
				created := dispatchRequest(t, r.handler, http.MethodPost, "/api/v1/issues", map[string]any{
					"project": "LOCK", "title": "Who holds it, " + r.fresh("reassigned") + "?", "spec": r.fresh("spec") + "\n",
				}, "alice")
				if created.Code != http.StatusCreated {
					t.Fatalf("create the reassigned issue: status=%d body=%s", created.Code, created.Body.String())
				}
				issue = decodeBody[model.Issue](t, created).Key
				ask = createIssueAsk(t, r.handler, issue, "Who answers it?")
			}
			assignIssue(t, r.handler, issue, r.signedIn(t, "lock-assignee"))
			var to any
			if !cleared {
				to = r.signedIn(t, "lock-assignee")
			}
			return storeWriterRun{ask: ask, write: func() error {
				return okOrError(dispatchRequest(t, r.call, http.MethodPatch, "/api/v1/issues/"+issue,
					map[string]any{"assignee": to}, "alice"))
			}}
		},
	}
	return writer
}

// projectDefaultMove is putProjectDefaultAssignee: the project's row, its write, the project's
// settings event, then the move of every ask that falls through to the default and its person
// tuples. Each run moves the default to a person no write has recorded, which moves an ask on the
// rig's project document, made by the first run.
func (r *storeWriterRig) projectDefaultMove() storeWriter {
	var ask string
	return storeWriter{
		name: "putProjectDefaultAssignee",
		steps: []storeStep{
			ownerRowStep(defaultOwnerStatement, "projects", `select 1 from projects where key = $1 for no key update`, "LOCK"),
			writeStep("the project's row", "projects", defaultWriteStatement),
			eventAppendStep(),
			movedAsksStep(),
			personTuplesStep(),
		},
		locksOwnProject: true,
		ready: func(t *testing.T, ctx context.Context) storeWriterRun {
			if ask == "" {
				response := sessionRequest(t, r.handler, http.MethodPost, "/api/v1/artifacts/"+r.document+"/asks", map[string]any{
					"question": "Who does the document's question go to?",
					"options":  []map[string]string{{"label": "Yes"}, {"label": "No"}},
					"actor":    sessionActor(),
				})
				if response.Code != http.StatusCreated {
					t.Fatalf("open a question on the rig's document: status=%d body=%s", response.Code, response.Body.String())
				}
				ask = decodeBody[model.Ask](t, response).ID
			}
			to := r.signedIn(t, "lock-default")
			return storeWriterRun{ask: ask, write: func() error {
				return okOrError(dispatchRequest(t, r.call, http.MethodPut, "/api/v1/projects/LOCK/default-assignee",
					map[string]any{"assignee": to}, "alice"))
			}}
		},
	}
}

// okOrError answers nil for a 200 and the response as an error for anything else.
func okOrError(response *httptest.ResponseRecorder) error {
	if response.Code != http.StatusOK {
		return fmt.Errorf("status=%d body=%s", response.Code, response.Body.String())
	}
	return nil
}
