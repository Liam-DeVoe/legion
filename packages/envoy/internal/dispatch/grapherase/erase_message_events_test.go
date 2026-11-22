package grapherase

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// writeMessageAsThisBuild writes message id on issue CORE-1, by author, answering inReplyTo where
// it names a message, in a transaction of its own (writeMessage).
func writeMessageAsThisBuild(t *testing.T, database *store.Store, id, inReplyTo, body string, author model.Actor) {
	t.Helper()
	ctx := context.Background()
	writeAsThisBuild(t, database, "the message "+id, func(tx pgx.Tx) ([]graphstore.Uncaused, error) {
		held, err := writeMessage(ctx, tx, id, inReplyTo, "", body, author, time.Now().UTC())
		return []graphstore.Uncaused{held}, err
	})
}

// writeMessage writes message id on issue CORE-1 in tx, by author at at, answering inReplyTo and
// sent to target where each names one, as this release's message write does: its node and the
// facts its row projects (graphstore.MessageFacts), its row naming the node, and its authored_by.
// It answers the node write, held for its cause.
func writeMessage(ctx context.Context, tx pgx.Tx, id, inReplyTo, target, body string, author model.Actor, at time.Time) (graphstore.Uncaused, error) {
	row := graphstore.MessageRow{ID: id, IssueKey: "CORE-1", InReplyTo: inReplyTo, Target: target}
	nodes, tuples, _ := graphstore.MessageFacts([]graphstore.Node{graphstore.MessageNode(id, "CORE", nil, author, at)}, nil, row, author, at, graphstore.NoEvent)
	held := graphstore.PlanUncaused(nodes, tuples)
	if err := store.RunWrite(ctx, tx, func(b *store.WriteBatch) error {
		graphstore.QueueNodes(b, held)
		return nil
	}); err != nil {
		return graphstore.Uncaused{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into messages (id, issue_key, author, body, in_reply_to, target, created_at, node_kind, node_address)
		values ($1::uuid, 'CORE-1', $2, $3, nullif($4, '')::uuid, nullif($5, ''), $6, 'message', $1)
	`, id, `{"kind":"user","id":"`+author.ID+`"}`, body, inReplyTo, target, at); err != nil {
		return graphstore.Uncaused{}, err
	}
	if err := store.RunWrite(ctx, tx, func(b *store.WriteBatch) error {
		return graphstore.QueueAuthored(b, graphstore.KindMessage, id, author, at, graphstore.NoEvent)
	}); err != nil {
		return graphstore.Uncaused{}, err
	}
	return held, nil
}

// appendMessageEvent appends a message.created on issue CORE-1 carrying payload, the shape every
// message write appends (model.MessageEventPayload), and answers its id.
func appendMessageEvent(t *testing.T, database *store.Store, payload model.MessageEventPayload) int64 {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode the event of message %s: %v", payload.ID, err)
	}
	return appendEvent(t, database, "CORE-1", "message.created", string(encoded))
}

// R5 over the events a message's writes append (api's messages.go). A message's own
// message.created carries its body and, for a reply, what it quotes of the message it answers:
// that message's head as reply_body (messageReplyPreview) and the block it answers as
// reply_block. A later reply's carries the same two quotes of the message it answers.
//
// Message B answers message A, whose body holds B's whole text inside a longer line. A later reply
// C quoted B, its head and its block, in its own event; C's row is gone and its body is the marker
// of an erase of its own, which is what keeps B erasable, since a standing row whose in_reply_to
// names B would refuse the run (readBlockers). The erase of B leaves A's own event as it was and
// leaves A's words where B's own event quotes them, since those words are A's and the erased text
// stands there only inside a longer line of them (quotedFields). It replaces B's body in B's own
// event and both of C's quotes of B with the marker, and the erased text then stands in no event
// but those two of A's words.
func TestTheEraseOfAMessageTakesItsWordsFromEveryEventAndLeavesTheWordsItQuotes(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedPreStoreDocument(t, database)
	alice := model.Actor{Kind: "user", ID: "alice"}
	issue := "CORE-1"
	parent, erased, later := "00000000-0000-4000-8000-0000000011a1", "00000000-0000-4000-8000-0000000011a2", "00000000-0000-4000-8000-0000000011a3"
	const parentBody = "Shall we take the slow road through Quillmere?"
	const erasedBody = "the slow road through Quillmere"
	writeMessageAsThisBuild(t, database, parent, "", parentBody, alice)
	writeMessageAsThisBuild(t, database, erased, parent, erasedBody, alice)

	parentEvent := appendMessageEvent(t, database, model.MessageEventPayload{
		Message: model.Message{ID: parent, IssueKey: &issue, Author: alice, Body: parentBody},
	})
	erasedEvent := appendMessageEvent(t, database, model.MessageEventPayload{
		Message:   model.Message{ID: erased, IssueKey: &issue, Author: alice, Body: erasedBody, InReplyTo: &parent},
		ReplyBody: parentBody,
	})
	laterEvent := appendMessageEvent(t, database, model.MessageEventPayload{
		Message: model.Message{ID: later, IssueKey: &issue, Author: alice, InReplyTo: &erased,
			Body:       fmt.Sprintf(eraseMarker, graphstore.KindMessage, later),
			ReplyBlock: &model.ReplyBlock{Node: "message/" + erased + "#b1", BlockID: "b1", Text: erasedBody}},
		ReplyBody: erasedBody,
	})
	_, parentBefore := eventPayload(t, database, parentEvent)

	listing := dryRun(t, database, graphstore.KindMessage, erased)
	erasure, err := Erase(ctx, database.Pool.Config().ConnString(), eraseServerURL, graphstore.KindMessage, erased, listing.Digest, time.Millisecond)
	if err != nil {
		t.Fatalf("erase --confirm %s: %v", listing.Digest, err)
	}
	if !erasure.Applied {
		t.Fatalf("the erase of message %s applied nothing: blockers %q, unredacted %q", erased, erasure.Blockers, erasure.Unredacted())
	}
	marker := fmt.Sprintf(eraseMarker, graphstore.KindMessage, erased)

	if _, raw := eventPayload(t, database, parentEvent); raw != parentBefore {
		t.Errorf("the erase rewrote message %s's own event, another node's words: %s, was %s", parent, raw, parentBefore)
	}
	own, ownRaw := eventPayload(t, database, erasedEvent)
	if own["body"] != marker {
		t.Errorf("message %s's own event reads body %q after its erase, want the marker", erased, own["body"])
	}
	if own["reply_body"] != parentBody {
		t.Errorf("message %s's own event reads reply_body %q after its erase, want message %s's words as written, %q",
			erased, own["reply_body"], parent, parentBody)
	}
	quoting, quotingRaw := eventPayload(t, database, laterEvent)
	block, _ := quoting["reply_block"].(map[string]any)
	if quoting["reply_body"] != marker || block["text"] != marker {
		t.Errorf("the later reply's event quotes message %s as reply_body %q and reply_block text %q after its erase, want the marker in both",
			erased, quoting["reply_body"], block["text"])
	}

	rows, err := database.Pool.Query(ctx, `select id, type, payload::text from events order by id`)
	if err != nil {
		t.Fatalf("read the event log: %v", err)
	}
	type logged struct {
		id            int64
		kind, payload string
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (logged, error) {
		var event logged
		err := row.Scan(&event.id, &event.kind, &event.payload)
		return event, err
	})
	if err != nil {
		t.Fatalf("read the event log: %v", err)
	}
	// The erased text stands inside message A's words once in A's own event and once where B's
	// own event quotes them, and nowhere else.
	allowed := map[int64]int{parentEvent: 1, erasedEvent: 1}
	for _, event := range events {
		if held := strings.Count(event.payload, erasedBody); held != allowed[event.id] {
			t.Errorf("event %d (%s) holds the erased text %d time(s) after the erase, want %d: %s",
				event.id, event.kind, held, allowed[event.id], event.payload)
		}
	}
	t.Logf("after the erase message %s's own event reads %s and the later reply's %s", erased, ownRaw, quotingRaw)
}

// erasedMessageWord is the body of the message the statistics test erases: one word no other item
// of its project holds, which Postgres keeps as its own lexeme, so the search for the rows the run
// leaves finds that lexeme's row of the term statistics by the erased text.
const erasedMessageWord = "zyzzyvak"

// R5 over the term statistics of an issue message's project. An issue message is an item those
// statistics count (api's searchTermItemsSQL), so its row's delete queues what its erase takes out
// (migration 20261101182522's messages_search_term_queue), which the run takes out of its
// project's statistics under its locks and counts again after its commit (api.ForgetErasedTermsTx,
// recountTermStats). The project's statistics are therefore rows that derive from the message
// (derivedTables, through nodeProject): the listing leaves none of them, and once the run is done
// they hold no row of the erased word.
func TestTheEraseOfAnIssueMessageTakesItsTermsOutOfItsProjectsStatistics(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedPreStoreDocument(t, database)
	const project = "CORE"
	message := "00000000-0000-4000-8000-0000000011b1"
	writeMessageAsThisBuild(t, database, message, "", erasedMessageWord, model.Actor{Kind: "user", ID: "alice"})
	var lexeme string
	if err := database.Pool.QueryRow(ctx, `
		select u.lexeme from messages m cross join lateral unnest(m.search) u
		where m.id = $1::uuid order by u.lexeme limit 1`, message).Scan(&lexeme); err != nil {
		t.Fatalf("read the lexeme of message %s: %v", message, err)
	}
	if lexeme != erasedMessageWord {
		t.Fatalf("message %s's vector holds the lexeme %q, not its text %q, so the search for the rows left would find no statistics row by the erased text and this test would prove nothing",
			message, lexeme, erasedMessageWord)
	}
	api.RefreshEverySearchTermStats(ctx, database.Pool)
	if _, held := statsRow(t, database, project, lexeme); !held {
		t.Fatalf("the full build wrote no row of %s for %s, so this test would prove nothing", lexeme, project)
	}

	listing := dryRun(t, database, graphstore.KindMessage, message)
	for _, line := range listing.Listing {
		if strings.HasPrefix(strings.TrimSpace(line), "leave public.search_term_stats ") {
			t.Errorf("the listing of the erase of message %s leaves a row of %s's term statistics, which the run takes the erased word out of: %s",
				message, project, line)
		}
	}
	erasure, err := Erase(ctx, database.Pool.Config().ConnString(), eraseServerURL, graphstore.KindMessage, message, listing.Digest, time.Millisecond)
	if err != nil {
		t.Fatalf("erase --confirm %s: %v", listing.Digest, err)
	}
	if !erasure.Applied {
		t.Fatalf("the erase of message %s applied nothing: blockers %q, unredacted %q", message, erasure.Blockers, erasure.Unredacted())
	}
	if !slices.Equal(erasure.Recounted, []string{project}) || len(erasure.NotRecounted) != 0 {
		t.Errorf("the erase of message %s recounted %v and failed %v, want [%s] and none", message, erasure.Recounted, erasure.NotRecounted, project)
	}
	requireNoErasedLexeme(t, database, graphstore.KindMessage+":"+message, project, lexeme, "after the erase")
}
