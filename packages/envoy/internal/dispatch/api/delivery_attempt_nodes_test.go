package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc/schemanodes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// TestEveryDeliveryAttemptIsANode drives a comment's mention of a session and a message targeted
// to that session through the real handlers, the mention sender and the outbox, has the session
// answer each, and reads each attempt row back as one delivery_attempt node: the row names it,
// its values are its value tuples under the store names its attribute nodes carry, it was owed to
// the session while pending and delivered to it once sent, it is answered by the reply, and the
// write that opened it - the comment's request, the message's - caused its node and first facts.
// A reply that flips a sent attempt appends its answered_by and no second delivered_to.
func TestEveryDeliveryAttemptIsANode(t *testing.T) {
	listener, _ := newMentionListener(t)
	handler, database, deps := newTestServer(t, testServerOptions{envoyURL: listener.URL, envoyTimeout: time.Second})
	issue := createInteractionIssue(t, handler, "TEST", "Attempts are nodes", "before")

	created := decodeMentionedComment(t, postMentionedComment(t, handler, issue.Key, map[string]any{
		"body": "Are you there?", "mentions": []map[string]any{{"target": "session:s1"}},
	}))
	if settled := settledCommentDeliveries(t, database, created.ID); len(settled) != 1 || settled[0].State != "sent" {
		t.Fatalf("the mention's attempts = %#v, want its one attempt sent", settled)
	}
	runOutboxUntilSettled(t, deps, &recordingOutboxPublisher{}, issue.Key, created.ID)
	commentReply := sessionRequest(t, handler, http.MethodPost, "/api/v1/comments/"+created.ID+"/reply", map[string]any{
		"actor": map[string]any{"kind": "session", "id": "s1"}, "attempt": 1, "body": "Here.",
	})
	if commentReply.Code != http.StatusCreated {
		t.Fatalf("the session's reply to the mention: status=%d body=%s", commentReply.Code, commentReply.Body.String())
	}
	requireAttemptIsANode(t, database, attemptExpectation{
		key: graphstore.CommentAttempt(created.ID, "session:s1", 1), table: "comment_deliveries",
		opener: "comment.created", settler: "comment.delivery", answerer: "comment.answered",
		replyKind: graphstore.KindComment, reply: replyID(t, commentReply.Body.Bytes()),
	})

	posted := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues/"+issue.Key+"/messages", map[string]any{
		"body": "Ping.", "target": "session:s1", "delivery": "steer",
	}, "alice")
	if posted.Code != http.StatusCreated {
		t.Fatalf("the targeted message: status=%d body=%s", posted.Code, posted.Body.String())
	}
	messageID := replyID(t, posted.Body.Bytes())
	messageReply := sessionRequest(t, handler, http.MethodPost, "/api/v1/messages/"+messageID+"/reply", map[string]any{
		"actor": map[string]any{"kind": "session", "id": "s1"}, "attempt": 1, "body": "Pong.",
	})
	if messageReply.Code != http.StatusCreated {
		t.Fatalf("the session's reply to the message: status=%d body=%s", messageReply.Code, messageReply.Body.String())
	}
	requireAttemptIsANode(t, database, attemptExpectation{
		key: graphstore.MessageAttempt(messageID, 1), table: "message_deliveries",
		opener: "message.created", settler: "message.delivery", answerer: "message.answered",
		replyKind: graphstore.KindMessage, reply: replyID(t, messageReply.Body.Bytes()),
	})
}

// attemptExpectation is one attempt the test reads back: its key, its table, the event types of
// the write that opened it, of the settle that recorded it sent and of the reply that answered
// it, and the reply node that answered it.
type attemptExpectation struct {
	key                       graphstore.AttemptKey
	table                     string
	opener, settler, answerer string
	replyKind, reply          string
}

// replyID is the id of the record a route answered with.
func replyID(t *testing.T, body []byte) string {
	t.Helper()
	var read struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &read); err != nil || read.ID == "" {
		t.Fatalf("read the id the route answered: %v: %s", err, body)
	}
	return read.ID
}

// requireAttemptIsANode holds one sent and answered attempt to its node and its tuples.
func requireAttemptIsANode(t *testing.T, database *store.Store, want attemptExpectation) {
	t.Helper()
	ctx := context.Background()
	id := want.key.ID()
	where := `message_id = $1::uuid and attempt = $2`
	args := []any{want.key.Message, want.key.Attempt}
	if want.key.Message == "" {
		where = `comment_id = $1::uuid and target = $3 and attempt = $2`
		args = []any{want.key.Comment, want.key.Attempt, want.key.Target}
	}
	var nodeKind, nodeAddress, state string
	if err := database.Pool.QueryRow(ctx, `select node_kind, node_address, state from `+want.table+` where `+where, args...).
		Scan(&nodeKind, &nodeAddress, &state); err != nil {
		t.Fatalf("read the attempt row of %s: %v", id, err)
	}
	if nodeKind != graphstore.KindDeliveryAttempt || nodeAddress != id {
		t.Fatalf("the attempt row names node %s/%s, want %s/%s", nodeKind, nodeAddress, graphstore.KindDeliveryAttempt, id)
	}
	node, held, err := graphstore.ReadNode(ctx, database.Pool, graphstore.KindDeliveryAttempt, id)
	if err != nil || !held {
		t.Fatalf("read the node of %s: held %v, %v", id, held, err)
	}
	if node.Supertag != graphstore.KindDeliveryAttempt || node.Visibility != graphstore.VisibilityProject || node.State != graphstore.StateLive {
		t.Errorf("the node of %s is supertag %s, %s and %s, want delivery_attempt, project and live", id, node.Supertag, node.Visibility, node.State)
	}
	var opener string
	if err := database.Pool.QueryRow(ctx, `select type from events where id = $1`, node.SourceEvent).Scan(&opener); err != nil || opener != want.opener {
		t.Errorf("the node of %s is caused by event %d (%s, %v), want the %s event of the write that opened it", id, node.SourceEvent, opener, err, want.opener)
	}

	// Every value of the attempt is stated under a store name an attribute node of the supertag
	// carries, and the row's own values read back from the tuples.
	storeNames := map[string]bool{}
	attributes, err := graphstore.NodesOfKind(ctx, database.Pool, schemanodes.KindAttribute)
	if err != nil {
		t.Fatalf("read the attribute nodes: %v", err)
	}
	for _, attribute := range attributes {
		if name, _ := attribute.Attributes["store_name"].(string); name != "" && strings.HasPrefix(attribute.ID, graphstore.KindDeliveryAttempt+"/") {
			storeNames[name] = true
		}
	}
	rows, err := database.Pool.Query(ctx, `
		select distinct kind from graph_tuples
		where from_kind = $1 and from_id = $2 and to_kind = $3 and kind <> all($4)
	`, graphstore.KindDeliveryAttempt, id, graphstore.KindValue, graphstore.BaseAttributes())
	if err != nil {
		t.Fatalf("read the value kinds of %s: %v", id, err)
	}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatalf("read a value kind of %s: %v", id, err)
		}
		if !storeNames[kind] {
			t.Errorf("%s states a value of kind %q, which no attribute node of delivery_attempt stores its values under", id, kind)
		}
	}
	rows.Close()
	for kind, value := range map[string]string{
		graphstore.AttemptNumber: "1", graphstore.AttemptState: state, graphstore.AttemptDuplicate: "false",
	} {
		if current, held, err := graphstore.CurrentValue(ctx, database.Pool, graphstore.KindDeliveryAttempt, id, kind); err != nil || !held || current != value {
			t.Errorf("%s carries %s = %q (held %v, %v), want %q, its row's", id, kind, current, held, err, value)
		}
	}
	if state != "sent" {
		t.Errorf("the attempt row of %s is %s, want sent", id, state)
	}

	// Owed to the session while it was pending, and no longer; delivered to it once, though both
	// the settle and the reply recorded it sent; answered by the reply.
	owed, err := graphstore.History(ctx, database.Pool, graphstore.KindDeliveryAttempt, id, graphstore.TupleOwedTo)
	if err != nil {
		t.Fatalf("read the owed_to history of %s: %v", id, err)
	}
	if len(owed) != 2 || !owed[0].Asserted || owed[1].Asserted || owed[0].ToKind != graphstore.KindSession || owed[0].ToID != "s1" {
		t.Errorf("the owed_to history of %s is %+v, want owed to session s1 and then retracted", id, owed)
	}
	if len(owed) > 0 && owed[0].SourceEvent != node.SourceEvent {
		t.Errorf("the first owed_to of %s is caused by event %d, want the opening write's %d", id, owed[0].SourceEvent, node.SourceEvent)
	}
	delivered, err := graphstore.History(ctx, database.Pool, graphstore.KindDeliveryAttempt, id, graphstore.TupleDeliveredTo)
	if err != nil {
		t.Fatalf("read the delivered_to history of %s: %v", id, err)
	}
	if len(delivered) != 1 || !delivered[0].Asserted || delivered[0].ToKind != graphstore.KindSession || delivered[0].ToID != "s1" {
		t.Errorf("the delivered_to history of %s is %+v, want one delivery to session s1", id, delivered)
	}
	answered, err := graphstore.CurrentOut(ctx, database.Pool, graphstore.KindDeliveryAttempt, id, []string{graphstore.TupleAnsweredBy})
	if err != nil {
		t.Fatalf("read what answered %s: %v", id, err)
	}
	if !slices.ContainsFunc(answered, func(tuple graphstore.Tuple) bool {
		return tuple.ToKind == want.replyKind && tuple.ToID == want.reply
	}) || len(answered) != 1 {
		t.Errorf("%s is answered by %+v, want the reply %s/%s alone", id, answered, want.replyKind, want.reply)
	}

	// The delivery is caused by the settle's receipt and the answer by the reply's own event:
	// each transition's facts carry the event its transaction appended, and that event is about
	// this attempt's item.
	item := want.key.Comment
	if want.key.Message != "" {
		item = want.key.Message
	}
	for kind, eventType := range map[string]string{graphstore.TupleDeliveredTo: want.settler, graphstore.TupleAnsweredBy: want.answerer} {
		var source int64
		var got, payload string
		if err := database.Pool.QueryRow(ctx, `
			select t.source_event, coalesce(e.type, ''), coalesce(e.payload::text, '')
			from graph_tuples t left join events e on e.id = t.source_event
			where t.from_kind = $1 and t.from_id = $2 and t.kind = $3 and t.asserted
		`, graphstore.KindDeliveryAttempt, id, kind).Scan(&source, &got, &payload); err != nil {
			t.Fatalf("read the cause of the %s of %s: %v", kind, id, err)
		}
		if got != eventType || !strings.Contains(payload, item) {
			t.Errorf("the %s of %s is caused by event %d (%q, payload %s), want the %s event its transition appended for %s",
				kind, id, source, got, payload, eventType, item)
		}
	}
}
