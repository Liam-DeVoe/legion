package api

// The answer of each write to a comment or to an issue message lists the mentions and the
// delivery attempts a read of that post lists right after it. Each write used to answer the post
// as its row alone had it, with no mention and no attempt, where the read listed both.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// writeAnswersFixture is a server whose stand-in Envoy listener knows no holder of
// role:reviewer. A comment that mentions the role, or an issue message sent to it, writes one
// attempt that fails "no holder for role reviewer", as
// TestMentionedCommentDoesNotReresolveMissingRoleHolder and
// TestTargetedMessageResolvesRolesAndEnforcesReplyBoundaries find, so every post below holds a
// mention row and a delivery row, or a delivery row, with no live session to wait on.
type writeAnswersFixture struct {
	handler  http.Handler
	database *store.Store
}

func newWriteAnswersFixture(t *testing.T) writeAnswersFixture {
	t.Helper()
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/roles/reviewer" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no holder for role reviewer"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(listener.Close)
	handler, database := newTargetedMessageHandler(t, listener.URL)
	return writeAnswersFixture{handler: handler, database: database}
}

// mentioningComment posts alice's comment of input on the issue, mentioning role:reviewer, and
// waits until its attempt has settled, so no write below races the mention sender.
func (f writeAnswersFixture) mentioningComment(t *testing.T, issueKey string, input map[string]any) string {
	t.Helper()
	input["mentions"] = []map[string]any{{"target": "role:reviewer"}}
	created := decodeMentionedComment(t, postMentionedComment(t, f.handler, issueKey, input))
	settledCommentDeliveries(t, f.database, created.ID)
	return created.ID
}

// jsonFields is the fields of one JSON object, each as the bytes it was written with.
func jsonFields(t *testing.T, what string, object []byte) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(object, &fields); err != nil {
		t.Fatalf("decode %s: %v (%s)", what, err, object)
	}
	return fields
}

// compactJSON is value with no insignificant space, so two writers of one value compare equal.
func compactJSON(t *testing.T, value json.RawMessage) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		t.Fatalf("compact %s: %v", value, err)
	}
	return compact.String()
}

// requireAnswerListsTheRead fails t unless the answer of write lists, in each of fields, exactly
// what the read lists in it.
func requireAnswerListsTheRead(t *testing.T, write, read string, answer *httptest.ResponseRecorder, stored map[string]json.RawMessage, fields ...string) {
	t.Helper()
	answered := jsonFields(t, "the answer of "+write, answer.Body.Bytes())
	for _, field := range fields {
		if got, want := compactJSON(t, answered[field]), compactJSON(t, stored[field]); got != want {
			t.Fatalf("the answer of %s lists %s %s, want %s, what %s lists", write, field, got, want, read)
		}
	}
}

// requireCommentAnswerListsTheRead fails t unless answer, the 200 of write to comment id, lists
// the mentions and the deliveries GET /api/v1/comments/{id} lists right after it. The read is
// the control: it has to list the mention of role:reviewer and its one attempt, which proves the
// comment holds the rows the answer must list.
func (f writeAnswersFixture) requireCommentAnswerListsTheRead(t *testing.T, write, id string, answer *httptest.ResponseRecorder) {
	t.Helper()
	if answer.Code != http.StatusOK {
		t.Fatalf("%s: status=%d body=%s", write, answer.Code, answer.Body.String())
	}
	response := dispatchRequest(t, f.handler, http.MethodGet, "/api/v1/comments/"+id, nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read comment %s after %s: status=%d body=%s", id, write, response.Code, response.Body.String())
	}
	stored := jsonFields(t, "the comment read", jsonFields(t, "the comment read", response.Body.Bytes())["comment"])
	var mentions []commentMentionRead
	var deliveries []commentDeliveryRead
	if err := json.Unmarshal(stored["mentions"], &mentions); err != nil {
		t.Fatalf("decode the read's mentions %s: %v", stored["mentions"], err)
	}
	if err := json.Unmarshal(stored["deliveries"], &deliveries); err != nil {
		t.Fatalf("decode the read's deliveries %s: %v", stored["deliveries"], err)
	}
	if len(mentions) != 1 || mentions[0].Target != "role:reviewer" ||
		len(deliveries) != 1 || deliveries[0].Target != "role:reviewer" || deliveries[0].Attempt != 1 {
		t.Fatalf("the read after %s lists mentions %s and deliveries %s, want the mention of role:reviewer and its one attempt",
			write, stored["mentions"], stored["deliveries"])
	}
	requireAnswerListsTheRead(t, write, "GET /api/v1/comments/"+id, answer, stored, "mentions", "deliveries")
}

// requireMessageAnswerListsTheRead is requireCommentAnswerListsTheRead for an issue message: the
// answer lists the deliveries GET /api/v1/messages/{id} lists right after it, and that read lists
// the message's one attempt.
func (f writeAnswersFixture) requireMessageAnswerListsTheRead(t *testing.T, write, id string, answer *httptest.ResponseRecorder) {
	t.Helper()
	if answer.Code != http.StatusOK {
		t.Fatalf("%s: status=%d body=%s", write, answer.Code, answer.Body.String())
	}
	response := dispatchRequest(t, f.handler, http.MethodGet, "/api/v1/messages/"+id, nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read message %s after %s: status=%d body=%s", id, write, response.Code, response.Body.String())
	}
	stored := jsonFields(t, "the message read", jsonFields(t, "the message read", response.Body.Bytes())["message"])
	var deliveries []model.MessageDelivery
	if err := json.Unmarshal(stored["deliveries"], &deliveries); err != nil {
		t.Fatalf("decode the read's deliveries %s: %v", stored["deliveries"], err)
	}
	if len(deliveries) != 1 || deliveries[0].MessageID != id || deliveries[0].Attempt != 1 {
		t.Fatalf("the read after %s lists deliveries %s, want the message's one attempt", write, stored["deliveries"])
	}
	requireAnswerListsTheRead(t, write, "GET /api/v1/messages/"+id, answer, stored, "deliveries")
}

// Each write to a comment answers the comment with the mention it carries and the attempt that
// mention wrote, as a read made right after the write lists them: the author's edit and delete,
// a thread's resolve and its reopen, and a suggestion's accept and its reject.
func TestCommentWriteAnswersListTheMentionsAndDeliveriesAReadLists(t *testing.T) {
	f := newWriteAnswersFixture(t)
	issue := createInteractionIssue(t, f.handler, "TEST", "A comment's writes answer", "before")
	type write struct {
		method, route string
		body          any
	}
	for _, test := range []struct {
		name    string
		comment map[string]any
		writes  []write
	}{
		{
			name:    "edit",
			comment: map[string]any{"body": "Reviewers, read the spec."},
			writes:  []write{{method: http.MethodPatch, body: map[string]any{"body": "Reviewers, read the new spec."}}},
		},
		{
			name:    "delete",
			comment: map[string]any{"body": "Reviewers, read the spec."},
			writes:  []write{{method: http.MethodDelete}},
		},
		{
			name:    "resolve and reopen",
			comment: map[string]any{"body": "Reviewers, is this settled?"},
			writes:  []write{{method: http.MethodPost, route: "/resolve"}, {method: http.MethodPost, route: "/reopen"}},
		},
		{
			name: "accept",
			comment: map[string]any{
				"body": "Say after.", "anchor": map[string]any{"artifact": "spec", "quote": "before"},
				"suggestion": map[string]string{"replace_with": "after"},
			},
			writes: []write{{method: http.MethodPost, route: "/accept"}},
		},
		{
			name:    "reject",
			comment: map[string]any{"body": "Say ignored.", "suggestion": map[string]string{"replace_with": "ignored"}},
			writes:  []write{{method: http.MethodPost, route: "/reject"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := f.mentioningComment(t, issue.Key, test.comment)
			for _, write := range test.writes {
				path := "/api/v1/comments/" + id + write.route
				answer := dispatchRequest(t, f.handler, write.method, path, write.body, "alice")
				f.requireCommentAnswerListsTheRead(t, write.method+" "+path, id, answer)
			}
		})
	}
}

// The edit and the delete of an issue message sent to a role answer the message with the attempt
// that send wrote, as a read made right after each lists it.
func TestMessageCorrectionAnswersListTheDeliveriesAReadLists(t *testing.T) {
	f := newWriteAnswersFixture(t)
	issue := createInteractionIssue(t, f.handler, "TEST", "A message's corrections answer", "before")
	message := createIssueMessage(t, f.handler, issue.Key, map[string]any{
		"body": "Who reviews this?", "target": "role:reviewer", "delivery": "btw",
	}, "alice")
	path := "/api/v1/messages/" + message.ID

	edited := dispatchRequest(t, f.handler, http.MethodPatch, path, map[string]any{"body": "Who reviews this now?"}, "alice")
	f.requireMessageAnswerListsTheRead(t, "PATCH "+path, message.ID, edited)

	deleted := dispatchRequest(t, f.handler, http.MethodDelete, path, nil, "alice")
	f.requireMessageAnswerListsTheRead(t, "DELETE "+path, message.ID, deleted)
}
