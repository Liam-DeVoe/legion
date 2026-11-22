package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/asks"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// settleAskAsTheCatchUp writes change on the ask askID as the decisions step's catch-up states a
// write a process of the release before made (docs' applyCaughtUpEvent): through the one writer of
// an ask's answer state (asks.Settle), on the ask's node and its row, in a transaction of its own.
func settleAskAsTheCatchUp(t *testing.T, database *store.Store, askID string, change asks.Change) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the catch-up's write: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := asks.Settle(ctx, tx, askID, change); err != nil {
		t.Fatalf("state the answer state of ask %s: %v", askID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the catch-up's write: %v", err)
	}
}

// describedAnswer is answer as JSON, for a failure to print.
func describedAnswer(answer *model.AskAnswer) string {
	encoded, _ := json.Marshal(answer)
	return string(encoded)
}

// Every read of an answer reads the ask's node (R3), as the text routes' asks do (docs'
// TestTheTextsAsksReadTheAnswerOffTheAsksNodeNotItsRow): the rows the document page draws each
// ask's state and answer from (GET /api/v1/issues/{key}/asks, askReadColumns), one ask (GET
// /api/v1/asks/{id}, the same columns), the spec's open questions on the Decisions read
// (specOpenQuestions) and a search hit's ask (attachAskStates). The node and the row differ only
// while a process of the release before has written the row and the decisions step's catch-up has
// not yet stated that write on the node: here the catch-up stated alice's answer on both (asks.Settle
// with asks.Recorded), and then the release before's reopen wrote the row open again, alone. Each
// reader answers alice's answer as the node holds it, and the open questions leave the ask out.
func TestEveryReadOfAnAnswerReadsTheAsksNodeNotItsRow(t *testing.T) {
	handler, database, _ := blockAskHandlerWithDocuments(t)
	issue, askID := seedBlockAsk(t, handler, "Readers of the ask node", "decision", transportAsk, "Which transport?")
	ctx := context.Background()
	words := "REST, as the platform speaks."
	answer := model.AskAnswer{User: "alice", Selected: []string{"REST"}, Text: &words, At: time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC)}
	change := asks.Recorded(asks.StateAnswered, &answer, nil)
	change.Who, change.At, change.Event = model.Actor{Kind: "user", ID: "alice"}, answer.At, graphstore.NoEvent
	settleAskAsTheCatchUp(t, database, askID, change)
	if tag, err := database.Pool.Exec(ctx, `update asks set state = 'open', answer = null where id = $1`, askID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("reopen the row of ask %s alone, as the release before writes it: %v", askID, err)
	}
	if state, held, err := graphstore.CurrentValue(ctx, database.Pool, graphstore.KindAsk, askID, graphstore.TupleAnswerState); err != nil || !held || state != asks.StateAnswered {
		t.Fatalf("the node of ask %s holds answer_state %q (held %t, %v), want answered: the readers below would prove nothing", askID, state, held, err)
	}

	nodes := func(reader string, state string, read *model.AskAnswer) {
		t.Helper()
		if state != asks.StateAnswered || read == nil || read.User != answer.User || !slices.Equal(read.Selected, answer.Selected) ||
			read.Text == nil || *read.Text != words || !read.At.Equal(answer.At) {
			t.Errorf("%s reads ask %s %s with answer %s, want answered with alice's answer as its node holds it, %s",
				reader, askID, state, describedAnswer(read), describedAnswer(&answer))
		}
	}

	const rows = "the document page's ask rows (GET /api/v1/issues/{key}/asks, askReadColumns)"
	listed := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue.Key+"/asks?state=all", nil, "alice")
	if listed.Code != http.StatusOK {
		t.Fatalf("%s: status=%d body=%s", rows, listed.Code, listed.Body.String())
	}
	found := false
	for _, ask := range decodeBody[[]model.Ask](t, listed) {
		if ask.ID == askID {
			found = true
			nodes(rows, ask.State, ask.Answer)
		}
	}
	if !found {
		t.Errorf("%s list no ask %s", rows, askID)
	}

	one := readBlockAsk(t, handler, askID)
	nodes("one ask (GET /api/v1/asks/{id}, askReadColumns)", one.State, one.Answer)

	const questions = "the spec's open questions (GET /api/v1/issues/{key}/decisions open_questions, specOpenQuestions)"
	decided := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue.Key+"/decisions", nil, "alice")
	if decided.Code != http.StatusOK {
		t.Fatalf("%s: status=%d body=%s", questions, decided.Code, decided.Body.String())
	}
	for _, question := range decodeBody[struct {
		OpenQuestions []model.Ask `json:"open_questions"`
	}](t, decided).OpenQuestions {
		if question.ID == askID {
			t.Errorf("%s hold ask %s, %s, which its node holds answered by alice", questions, askID, question.State)
		}
	}

	const hit = "a search hit's ask (GET /api/v1/search, attachAskStates)"
	searched := rawHitAsk(t, rawSearchHit(t, rawSearchResults(t, searchRequest(t, handler, "q=transport&limit=50")), "ask", askID))
	var state string
	var read *model.AskAnswer
	if err := json.Unmarshal(searched["state"], &state); err != nil {
		t.Fatalf("%s: decode the state %s: %v", hit, searched["state"], err)
	}
	if raw, held := searched["answer"]; held {
		read = new(model.AskAnswer)
		if err := json.Unmarshal(raw, read); err != nil {
			t.Fatalf("%s: decode the answer %s: %v", hit, raw, err)
		}
	}
	nodes(hit, state, read)
}
