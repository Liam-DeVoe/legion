package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// heldRowRequest is one row of TestARequestBehindARowAnotherTransactionHoldsIsRefusedBusyAtItsBound:
// the server, the statement that holds the row the request waits on, and the request.
type heldRowRequest struct {
	handler  http.Handler
	database *store.Store
	// hold, with holdArgs, takes the row in the test's own transaction, as another writer would.
	hold     string
	holdArgs []any
	// method, path and body are the request, sent with the session bearer when session is set and
	// as alice otherwise; written is its answer once the row is free.
	method, path string
	body         any
	session      bool
	written      int
}

// A request whose transaction waits on a row lock another transaction holds waits no longer than a
// write waits for any lock another writer holds (the store's lockWait), and is refused 503
// DATABASE_BUSY with Retry-After at that bound, so its client sends it again; once the row is free
// the same request goes through. An admitted write has no deadline, and none of these rows is
// ordered by an owner lock the request holds against every other writer of it, so without the
// bound the request held its pool connection and its admission unit for as long as the holder held
// the row. One row per route family whose lock takes the bound: a direct message's row (the
// accept; the claim takes the same row first), a delivery attempt's row (a session's reply; the
// claim takes the same rows), a write phase's lock of the message a reply names a block of
// (store.WriteBatch.QueueWithinBound, which an item's block state takes too), a comment delivery's
// attempt (a session's reply; the claim takes the same rows), the Inbox's Mark all read and its
// dismiss (each with the row its own lock order reaches first), and an agent's read mark's prune.
// The bound is the test's (store.WithLockWaitForTest) rather than production's ten seconds, and the
// holder is a transaction of the test's own that holds the row until the test lets it go.
func TestARequestBehindARowAnotherTransactionHoldsIsRefusedBusyAtItsBound(t *testing.T) {
	const bound = time.Second
	for _, row := range []struct {
		name    string
		arrange func(t *testing.T) heldRowRequest
	}{
		{
			name: "a session's accept behind its direct message's row",
			arrange: func(t *testing.T) heldRowRequest {
				handler, database, root, _, _ := directConversationFrom(t, "alice")
				return heldRowRequest{
					handler: handler, database: database,
					hold: `select 1 from messages where id = $1 for update`, holdArgs: []any{root.ID},
					method: http.MethodPost, path: "/api/v1/messages/" + root.ID + "/deliveries/1/accept",
					body:    map[string]any{"actor": map[string]any{"kind": "session", "id": "s1"}},
					session: true, written: http.StatusOK,
				}
			},
		},
		{
			name: "a session's reply behind its delivery attempt's row",
			arrange: func(t *testing.T) heldRowRequest {
				handler, database, root, _, _ := directConversationFrom(t, "alice")
				return heldRowRequest{
					handler: handler, database: database,
					hold:     `select 1 from message_deliveries where message_id = $1 and attempt = 1 for update`,
					holdArgs: []any{root.ID},
					method:   http.MethodPost, path: "/api/v1/messages/" + root.ID + "/reply?follow_up=true",
					body: map[string]any{
						"actor": map[string]any{"kind": "session", "id": "s1"}, "attempt": 1, "body": "On it.",
					},
					session: true, written: http.StatusCreated,
				}
			},
		},
		{
			name: "a reply naming a block of a direct message behind that message's row",
			arrange: func(t *testing.T) heldRowRequest {
				live := true
				sent := []map[string]any{}
				listener := sessionListener(t, &live, &sent)
				t.Cleanup(listener.Close)
				handler, database := newTargetedMessageHandler(t, listener.URL)
				direct := decodeBody[model.Message](t, postAgentMessage(t, handler, "s1", map[string]any{
					"body": "Direct one.\n\nDirect two.", "delivery": "btw",
				}, "alice"))
				return heldRowRequest{
					handler: handler, database: database,
					hold: `select 1 from messages where id = $1 for update`, holdArgs: []any{direct.ID},
					method: http.MethodPost, path: "/api/v1/agents/s1/messages",
					body: map[string]any{
						"body": "On direct two.", "delivery": "btw", "in_reply_to": direct.ID, "in_reply_to_block": "b2",
					},
					written: http.StatusCreated,
				}
			},
		},
		{
			name: "a session's reply to a comment mention behind its delivery attempt's row",
			arrange: func(t *testing.T) heldRowRequest {
				live := true
				sent := []map[string]any{}
				listener := sessionListener(t, &live, &sent)
				t.Cleanup(listener.Close)
				handler, database := newTargetedMessageHandler(t, listener.URL)
				issue := createInteractionIssue(t, handler, "TEST", "A reply behind a held attempt", "before")
				mentioned := decodeMentionedComment(t, postMentionedComment(t, handler, issue.Key, map[string]any{
					"body": "Can this ship?", "mentions": []map[string]any{{"target": "session:s1"}},
				}))
				// The mention sender settles the attempt after the create answers; the row is held
				// once it has, so the sender is not what waits on it.
				settledCommentDeliveries(t, database, mentioned.ID)
				return heldRowRequest{
					handler: handler, database: database,
					hold: `
						select 1 from comment_deliveries
						where comment_id = $1 and target = 'session:s1' and attempt = 1 for update`,
					holdArgs: []any{mentioned.ID},
					method:   http.MethodPost, path: "/api/v1/comments/" + mentioned.ID + "/reply",
					body: map[string]any{
						"actor": map[string]any{"kind": "session", "id": "s1"}, "attempt": 1, "body": "It ships.",
					},
					session: true, written: http.StatusCreated,
				}
			},
		},
		{
			name: "a Mark all read behind the caller's own thread row",
			arrange: func(t *testing.T) heldRowRequest {
				handler, database, _ := newTestServer(t, testServerOptions{settle: time.Hour})
				issue := createInteractionIssue(t, handler, "TEST", "Marked behind a held row", "A spec")
				thread := seedUnreadThread(t, handler, "/api/v1/issues/"+issue.Key+"/comments", "Ready to ship?")
				return heldRowRequest{
					handler: handler, database: database,
					hold:     `select 1 from user_threads where login = 'alice' and thread_id = $1 for no key update`,
					holdArgs: []any{thread},
					method:   http.MethodPost, path: "/api/v1/me/inbox/read",
					body:    map[string]any{"read_through": time.Now().UTC().Format(time.RFC3339Nano)},
					written: http.StatusOK,
				}
			},
		},
		{
			name: "a dismiss of an Agent thread behind its root's row",
			arrange: func(t *testing.T) heldRowRequest {
				live := true
				sent := []map[string]any{}
				listener := sessionListener(t, &live, &sent)
				t.Cleanup(listener.Close)
				handler, database := newTargetedMessageHandler(t, listener.URL)
				root, _ := agentThread(t, handler, "btw", "Deploy is tonight.", "Noted.")
				return heldRowRequest{
					handler: handler, database: database,
					hold: `select 1 from messages where id = $1 for update`, holdArgs: []any{root.ID},
					method: http.MethodPost, path: "/api/v1/me/inbox/dismiss",
					body:    dismissBody(t, database, "alice", []string{inboxDismissAgentPrefix + root.ID}),
					written: http.StatusOK,
				}
			},
		},
		{
			name: "an agent's read mark behind a reply row its prune deletes",
			arrange: func(t *testing.T) heldRowRequest {
				live := true
				sent := []map[string]any{}
				listener := sessionListener(t, &live, &sent)
				t.Cleanup(listener.Close)
				handler, database := newTargetedMessageHandler(t, listener.URL)
				_, reply := agentThread(t, handler, "btw", "Deploy is tonight.", "Noted.")
				// The reply read by id, as a view that shows it marks it: the row the read mark that
				// passes it then prunes.
				if marked := dispatchRequest(t, handler, http.MethodPut, "/api/v1/me/agents/s1/state", map[string]any{
					"read_replies": []string{reply.ID},
				}, "alice"); marked.Code != http.StatusOK {
					t.Fatalf("mark the reply read by id: status=%d body=%s", marked.Code, marked.Body.String())
				}
				return heldRowRequest{
					handler: handler, database: database,
					hold:     `select 1 from user_agent_reply_read where login = 'alice' and reply_id = $1 for update`,
					holdArgs: []any{reply.ID},
					method:   http.MethodPut, path: "/api/v1/me/agents/s1/state",
					body:    map[string]any{"read_through": time.Now().UTC().Format(time.RFC3339Nano)},
					written: http.StatusOK,
				}
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			arranged := row.arrange(t)
			send := func() *httptest.ResponseRecorder {
				data, err := json.Marshal(arranged.body)
				if err != nil {
					t.Errorf("encode the request: %v", err)
				}
				request := httptest.NewRequest(arranged.method, arranged.path, bytes.NewReader(data)).
					WithContext(store.WithLockWaitForTest(context.Background(), bound))
				request.Header.Set("Content-Type", "application/json")
				if arranged.session {
					request.Header.Set("Authorization", "Bearer agent-token")
				} else {
					request.Header.Set("X-Dispatch-User", "alice")
				}
				response := httptest.NewRecorder()
				arranged.handler.ServeHTTP(response, request)
				return response
			}

			ctx := context.Background()
			holder, err := arranged.database.Pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin the holder: %v", err)
			}
			defer func() { _ = holder.Rollback(ctx) }()
			held, err := holder.Exec(ctx, arranged.hold, arranged.holdArgs...)
			if err != nil {
				t.Fatalf("hold the row: %v", err)
			}
			if held.RowsAffected() != 1 {
				t.Fatalf("the holder took %d rows, want the one row the request waits on", held.RowsAffected())
			}

			answered := make(chan *httptest.ResponseRecorder, 1)
			started := time.Now()
			go func() { answered <- send() }()
			if waiting := storetest.WaitForLockWaiters(t, holder, 1, 10*time.Second); waiting < 1 {
				t.Fatalf("the request never queued behind the row the holder holds")
			}
			var response *httptest.ResponseRecorder
			select {
			case response = <-answered:
			case <-time.After(10 * time.Second):
				waited := time.Since(started)
				_ = holder.Rollback(ctx)
				<-answered
				t.Fatalf("the request was still waiting on the held row %s after it was sent, past a write's lock bound of %s: "+
					"it waits on a held row until its holder lets go", waited.Round(time.Millisecond), bound)
			}
			waited := time.Since(started)
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"`+admission.CodeDatabaseBusy+`"`) ||
				response.Header().Get("Retry-After") == "" {
				t.Fatalf("the request behind the held row: status=%d Retry-After=%q body=%s, want 503 %s with Retry-After",
					response.Code, response.Header().Get("Retry-After"), response.Body.String(), admission.CodeDatabaseBusy)
			}
			if waited < bound {
				t.Fatalf("the request was refused %s after it was sent, inside its bound of %s", waited.Round(time.Millisecond), bound)
			}
			if waiting := storetest.WaitForLockWaiters(t, holder, 0, time.Second); waiting != 0 {
				t.Fatalf("%d sessions still wait behind the held row after the refusal, want none", waiting)
			}

			if err := holder.Rollback(ctx); err != nil {
				t.Fatalf("let the row go: %v", err)
			}
			if again := send(); again.Code != arranged.written {
				t.Fatalf("the request sent again once the row was free: status=%d body=%s, want %d",
					again.Code, again.Body.String(), arranged.written)
			}
		})
	}
}
