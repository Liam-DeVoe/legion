package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Every text below is written for these tests; none comes from Dispatch.
const (
	loadToken        = "agent-token"
	loadOtherSession = "01a10000-0000-7000-8000-00000000000c"
)

// heldMessage is one message the conversation stand-in holds: its author (a person's login, or
// session:<id>), the session its conversation is with, the message it replies to, and its body.
type heldMessage struct {
	ID, Author, Session, InReplyTo, Body string
}

// conversationDispatch is the local Dispatch's conversation routes, holding each to the author
// Dispatch lets use it. A send to a session needs a person's sign-in and refuses the agent bearer
// (OAUTH_SCOPE_REQUIRED). A reply needs the agent bearer and names its session in actor; it
// answers a person's message to that session once, then only as a follow-up under that first
// reply.
type conversationDispatch struct {
	server   *httptest.Server
	mu       sync.Mutex
	messages []heldMessage
}

func newConversationDispatch(t *testing.T) *conversationDispatch {
	f := &conversationDispatch{}
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	find := func(id string) *heldMessage {
		for i := range f.messages {
			if f.messages[i].ID == id {
				return &f.messages[i]
			}
		}
		return nil
	}
	hold := func(m heldMessage) heldMessage {
		m.ID = "m" + strconv.Itoa(len(f.messages)+1)
		f.messages = append(f.messages, m)
		return m
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/_dev/signin", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "dsession", Value: r.URL.Query().Get("login")})
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("POST /api/v1/agents/{session_id}/messages", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("dsession")
		if err != nil || r.Header.Get("Authorization") != "" {
			write(w, http.StatusForbidden, map[string]string{"code": "OAUTH_SCOPE_REQUIRED"})
			return
		}
		var body struct {
			Body      string `json:"body"`
			InReplyTo string `json:"in_reply_to"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		session := r.PathValue("session_id")
		f.mu.Lock()
		defer f.mu.Unlock()
		if parent := find(body.InReplyTo); body.InReplyTo != "" && (parent == nil || parent.Session != session) {
			write(w, http.StatusBadRequest, map[string]string{"code": "MESSAGE_INPUT"})
			return
		}
		m := hold(heldMessage{Author: cookie.Value, Session: session, InReplyTo: body.InReplyTo, Body: body.Body})
		write(w, http.StatusCreated, map[string]any{"id": m.ID, "deliveries": []map[string]any{{"attempt": 1, "session_id": session}}})
	})
	mux.HandleFunc("POST /api/v1/messages/{id}/reply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Actor struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"actor"`
			Attempt int    `json:"attempt"`
			Body    string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Authorization") != "Bearer "+loadToken || body.Actor.Kind != "session" {
			write(w, http.StatusForbidden, map[string]string{"code": "REPLY_FORBIDDEN"})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		asked := find(r.PathValue("id"))
		if asked == nil || strings.HasPrefix(asked.Author, "session:") || body.Attempt != 1 {
			write(w, http.StatusNotFound, map[string]string{"code": "MESSAGE_NOT_FOUND"})
			return
		}
		if asked.Session != body.Actor.ID {
			write(w, http.StatusForbidden, map[string]string{"code": "REPLY_FORBIDDEN"})
			return
		}
		author, parent := "session:"+body.Actor.ID, asked.ID
		for _, m := range f.messages {
			if m.InReplyTo == asked.ID && m.Author == author {
				parent = m.ID
				break
			}
		}
		if parent != asked.ID && r.URL.Query().Get("follow_up") != "true" {
			write(w, http.StatusOK, map[string]any{"id": parent, "duplicate": true})
			return
		}
		m := hold(heldMessage{Author: author, Session: asked.Session, InReplyTo: parent, Body: body.Body})
		write(w, http.StatusCreated, map[string]any{"id": m.ID})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// held is the messages the stand-in holds, oldest first.
func (f *conversationDispatch) held() []heldMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.messages)
}

// conversationCase is a case on no issue whose writer is fixtureWriter, with thread.
func conversationCase(thread ...CaseText) *Case {
	return &Case{ID: "answer/message:a9@90", Group: GroupAnswer, Kind: "message", Conversation: true, WriterSession: fixtureWriter,
		Agent:  CaseText{Key: "message:a9", EventID: 90, Kind: "message", ActorKind: "session", Actor: fixtureWriter, Session: fixtureWriter, Text: "The zork train merged."},
		Record: CaseRecord{Project: "TEST", Thread: thread}}
}

func personText(key, text string) CaseText {
	return CaseText{Key: key, Kind: "message", ActorKind: "user", Actor: fixtureOwner, Text: text}
}

func sessionText(key, session, text string) CaseText {
	return CaseText{Key: key, Kind: "message", ActorKind: "session", Actor: session, Session: session, Text: text}
}

// A case on no issue whose thread holds a person's text and then the writer's loads each text by
// the route its author may use: the person's as a send to the writer session with the reader's
// sign-in, and the writer's as its reply to that message with the agent bearer.
func TestLoadWritesAConversationByEachAuthorsRoute(t *testing.T) {
	f := newConversationDispatch(t)
	ld := &loader{client: newLocalClient(LocalInstance{BaseURL: f.server.URL, AgentToken: loadToken}), owner: fixtureOwner}
	c := conversationCase(personText("message:p1", "Is the zork train ready?"), sessionText("message:s2", fixtureWriter, "It merges at noon."))
	got := ld.load(context.Background(), c)
	if got.Error != "" {
		t.Fatalf("load: %s", got.Error)
	}
	want := []heldMessage{
		{ID: "m1", Author: readerOf(c), Session: fixtureWriter, Body: "Is the zork train ready?"},
		{ID: "m2", Author: "session:" + fixtureWriter, Session: fixtureWriter, InReplyTo: "m1", Body: "It merges at noon."},
	}
	if held := f.held(); !slices.Equal(held, want) {
		t.Errorf("the conversation = %+v, want %+v", held, want)
	}
	if !slices.Equal(got.Thread, []string{"m1", "m2"}) || got.ReplyTo != "m1" || got.Opened != "" || len(got.LeftOut) != 0 {
		t.Errorf("loaded = %+v, want the thread m1, m2 replying to m1", got)
	}
}

// A conversation whose thread starts with the writer's text opens with a message by the reader,
// since a session writes there only in reply to a person. The writer's second text in a row is a
// follow-up under its first reply. Another session's text has no message to answer in the writer's
// conversation, so it is left out and named.
func TestLoadOpensAConversationThatStartsWithTheWritersText(t *testing.T) {
	f := newConversationDispatch(t)
	ld := &loader{client: newLocalClient(LocalInstance{BaseURL: f.server.URL, AgentToken: loadToken}), owner: fixtureOwner}
	c := conversationCase(
		sessionText("message:s1", fixtureWriter, "The zork train is ready."),
		sessionText("message:s2", fixtureWriter, "It merges at noon."),
		sessionText("message:x3", loadOtherSession, "The flim gate is shut."),
		personText("message:p4", "What merges at noon?"),
		sessionText("message:s5", fixtureWriter, "The queue of pull requests."),
	)
	got := ld.load(context.Background(), c)
	if got.Error != "" {
		t.Fatalf("load: %s", got.Error)
	}
	reader, writer := readerOf(c), "session:"+fixtureWriter
	want := []heldMessage{
		{ID: "m1", Author: reader, Session: fixtureWriter, Body: openingText},
		{ID: "m2", Author: writer, Session: fixtureWriter, InReplyTo: "m1", Body: "The zork train is ready."},
		{ID: "m3", Author: writer, Session: fixtureWriter, InReplyTo: "m2", Body: "It merges at noon."},
		{ID: "m4", Author: reader, Session: fixtureWriter, InReplyTo: "m3", Body: "What merges at noon?"},
		{ID: "m5", Author: writer, Session: fixtureWriter, InReplyTo: "m4", Body: "The queue of pull requests."},
	}
	if held := f.held(); !slices.Equal(held, want) {
		t.Errorf("the conversation = %+v, want %+v", held, want)
	}
	if !slices.Equal(got.Thread, []string{"m2", "m3", "m4", "m5"}) || got.Opened != "m1" || got.ReplyTo != "m4" || !slices.Equal(got.LeftOut, []string{"message:x3"}) {
		t.Errorf("loaded = %+v, want the thread m2 to m5 opened by m1, replying to m4, with message:x3 left out", got)
	}
}
