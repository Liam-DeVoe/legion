package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A person's Retry reads the post before it takes the owner's lock, and an edit of the post takes
// that lock too. This row holds the lock while the Retry waits on it, edits the post as the edit
// route writes its row under the same lock, and lets the Retry through. The Retry must then send
// the text the edit wrote, in the frame and in the body beside it alike: both are built from the
// post as the claim reads it under the lock (mentionFrameEvent, recordPendingMessageDelivery). A
// frame built from what was read before the lock - a message's row, or the comment.created event
// a comment's frame replays - carries the text the edit replaced. holdWriterBehindOwnerLock is
// the race of the route table, as in post_deleted_retry_race_test.go.
func TestARetryQueuedBehindAnEditSendsTheEditedText(t *testing.T) {
	for _, row := range []struct {
		name  string
		ready func(t *testing.T, base routeFixture) lockedItemWrite
	}{
		{name: "the mention retry route", ready: readyRetryOfAnEditedMention},
		{name: "the message retry route", ready: readyRetryOfAnEditedMessage},
	} {
		t.Run(row.name, func(t *testing.T) { holdWriterBehindOwnerLock(t, row.name, row.ready(t, routeFixture{})) })
	}
}

// editedText is the body each row's edit writes while the Retry waits.
const editedText = "The corrected text the Retry must send."

// listenedFrame is one send a recording listener took: the body the send carries beside its
// frame, and the body the frame's event carries.
type listenedFrame struct {
	message, eventBody string
}

// recordingSessionListener is itemRowSessionListener that also keeps every send it takes, in the
// order they arrive; frames answers them.
func recordingSessionListener(t *testing.T, capabilities ...string) (listener *httptest.Server, frames func() []listenedFrame) {
	t.Helper()
	sessions := `[{"session_id":"s1","title":"planner","capabilities":["` +
		strings.Join(capabilities, `","`) + `"]}]`
	var mu sync.Mutex
	var taken []listenedFrame
	listener = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/registry/s1":
			writeSessionRecord(t, w, sessions, "s1")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			_, _ = w.Write([]byte(sessions))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/send":
			var send struct {
				Message string `json:"message"`
				Payload string `json:"payload"`
			}
			var frame struct {
				Event struct {
					Payload struct {
						Body string `json:"body"`
					} `json:"payload"`
				} `json:"event"`
			}
			if err := json.NewDecoder(r.Body).Decode(&send); err != nil {
				t.Errorf("decode a send: %v", err)
			} else if err := json.Unmarshal([]byte(send.Payload), &frame); err != nil {
				t.Errorf("decode a send's frame %q: %v", send.Payload, err)
			}
			mu.Lock()
			taken = append(taken, listenedFrame{message: send.Message, eventBody: frame.Event.Payload.Body})
			mu.Unlock()
			_, _ = w.Write([]byte(`{"event_id":"item-row-envelope","recipient":"s1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(listener.Close)
	return listener, func() []listenedFrame {
		mu.Lock()
		defer mu.Unlock()
		return append([]listenedFrame(nil), taken...)
	}
}

// sentTheEditedText fails t unless the listener took two sends, the post's first and the Retry's,
// and the Retry's carries the edited text in its frame and beside it.
func sentTheEditedText(t *testing.T, frames []listenedFrame) {
	t.Helper()
	if len(frames) != 2 {
		t.Fatalf("the listener took %d sends, want the post's first and the Retry's", len(frames))
	}
	if retried := frames[1]; retried.message != editedText || retried.eventBody != editedText {
		t.Fatalf("the Retry sent %q beside a frame carrying %q, want the edited text %q in both",
			retried.message, retried.eventBody, editedText)
	}
}

// readyRetryOfAnEditedMention is readyMentionRetry with the comment edited while the Retry waits.
func readyRetryOfAnEditedMention(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	listener, frames := recordingSessionListener(t, "steer")
	base.options.envoyURL = listener.URL
	fixture := base.issue(t, "Mention retry behind an edit")
	comment := decodeMentionedComment(t, postMentionedComment(t, fixture.handler, fixture.issueKey, map[string]any{
		"body":     "The mention whose text an edit replaces.",
		"mentions": []map[string]any{{"target": "session:s1"}},
	}))
	settledCommentDeliveries(t, fixture.database, comment.ID)
	write := fixture.routeWrite(t, "the delivery attempt it claims",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return dispatchRequest(t, handler, http.MethodPost, "/api/v1/comments/"+comment.ID+"/deliveries",
				map[string]any{"delivery": "steer"}, "alice")
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated || decodeBody[commentDeliveryRead](t, answer).Attempt != 2 {
				t.Fatalf("retry the edited mention: status=%d body=%s, want 201 and attempt 2", answer.Code, answer.Body.String())
			}
			sentTheEditedText(t, frames())
		})
	write.meanwhile = func(t *testing.T, holder pgx.Tx) {
		t.Helper()
		if _, err := holder.Exec(t.Context(), `update comments set body = $2, edited_at = now() where id = $1`,
			comment.ID, editedText); err != nil {
			t.Fatalf("edit the comment while the Retry waits: %v", err)
		}
	}
	return write
}

// readyRetryOfAnEditedMessage is readyMessageRetry with the message edited while the Retry waits.
func readyRetryOfAnEditedMessage(t *testing.T, base routeFixture) lockedItemWrite {
	t.Helper()
	listener, frames := recordingSessionListener(t, "btw")
	base.options.envoyURL = listener.URL
	fixture := base.issue(t, "Message retry behind an edit")
	message := createIssueMessage(t, fixture.handler, fixture.issueKey, map[string]any{
		"body": "The message whose text an edit replaces.", "target": "session:s1", "delivery": "btw",
	}, "alice")
	if len(message.Deliveries) != 1 || message.Deliveries[0].State != "sent" {
		t.Fatalf("the message this row retries = %#v, want one attempt sent to the listener's session",
			message.Deliveries)
	}
	write := fixture.routeWrite(t, "the delivery attempt it claims",
		func(handler http.Handler) *httptest.ResponseRecorder {
			return dispatchRequest(t, handler, http.MethodPost, "/api/v1/messages/"+message.ID+"/deliveries",
				map[string]any{"delivery": "btw"}, "alice")
		},
		func(t *testing.T, answer *httptest.ResponseRecorder) {
			if answer.Code != http.StatusCreated || decodeBody[model.MessageDelivery](t, answer).Attempt != 2 {
				t.Fatalf("retry the edited message: status=%d body=%s, want 201 and attempt 2", answer.Code, answer.Body.String())
			}
			sentTheEditedText(t, frames())
		})
	write.meanwhile = func(t *testing.T, holder pgx.Tx) {
		t.Helper()
		if _, err := holder.Exec(t.Context(), `update messages set body = $2, edited_at = now() where id = $1`,
			message.ID, editedText); err != nil {
			t.Fatalf("edit the message while the Retry waits: %v", err)
		}
	}
	return write
}
