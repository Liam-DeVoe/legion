package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

// loaded.jsonl names the records each case was loaded into: the database and the start that
// seeded it. A load continues the cases loaded with no error into the same records, as a
// `harness local --keep` restart keeps them, and refuses the file for a database a start made or
// reset since, which does not hold them.
func TestLoadedLinesHoldTheCasesOfOneSeeding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loaded.jsonl")
	first := LoadedInstance{Database: "dispatch_harness_1", Seeded: "seeded 2026-11-20T10:00:00Z"}
	lines, done, err := openLoadedLog(path, first)
	if err != nil || len(done) != 0 {
		t.Fatalf("a new loaded.jsonl: %v, %v", done, err)
	}
	for _, l := range []Loaded{{CaseID: "plain/a"}, {CaseID: "plain/b", Error: "refused"}} {
		if err := lines.add(l); err != nil {
			t.Fatal(err)
		}
	}
	if err := lines.close(); err != nil {
		t.Fatal(err)
	}
	lines, done, err = openLoadedLog(path, first)
	if err != nil || !done["plain/a"] || len(done) != 1 {
		t.Fatalf("the records of a --keep restart continue %v (%v), want plain/a alone", done, err)
	}
	lines.close()
	for _, other := range []LoadedInstance{
		{Database: "dispatch_harness_2", Seeded: "seeded 2026-11-20T11:00:00Z"},
		{Database: "dispatch_harness_1", Seeded: "seeded 2026-11-20T11:00:00Z"},
	} {
		if _, _, err := openLoadedLog(path, other); err == nil || !strings.Contains(err.Error(), "a load continues only in the records it began in") {
			t.Errorf("a load into %+v with the same --out = %v, want the refusal", other, err)
		}
	}
}

// A line a run does not write is an error of the run, not a result: add answers it, and so do
// every later add and close. `harness load` and `harness phrases-labels` append their lines so.
func TestALineThatIsNotWrittenIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	lines, err := appendLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lines.add(LabelResult{Key: "1/a"}); err != nil {
		t.Fatal(err)
	}
	lines.f.Close()
	if err := lines.add(LabelResult{Key: "2/b"}); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("a line that was not written = %v, want its failure", err)
	}
	if err := lines.add(LabelResult{Key: "3/c"}); err == nil {
		t.Error("a line after a failed one was answered as written")
	}
	if err := lines.close(); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("close after a failed write = %v, want the write's failure", err)
	}
}

// `harness load` with the --out of a load into records a start has reset since refuses before it
// reaches the instance's database.
func TestLoadRefusesTheOutOfOtherRecords(t *testing.T) {
	build, _ := writeFixture(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "local.json")
	writeJSONFile(t, local, LocalInstance{BaseURL: "http://127.0.0.1:4200", Database: "dispatch_harness_1", Seeded: "seeded 2026-11-20T11:00:00Z",
		DatabaseURL: "postgres://127.0.0.1:1/dispatch_harness_1"})
	verdicts := filepath.Join(dir, "verdicts.jsonl")
	if err := os.WriteFile(verdicts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "load")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(out, "loaded.jsonl"), Loaded{CaseID: "question/comment:q1@11", Instance: LoadedInstance{Database: "dispatch_harness_1", Seeded: "seeded 2026-11-20T10:00:00Z"}})
	code, _, stderr := runHarness("load", "--local", local, "--cases", build, "--verdicts", verdicts, "--out", out)
	if code != 1 || !strings.Contains(stderr, "a load continues only in the records it began in") {
		t.Errorf("a load into reset records with the --out of the load before = %d:\n%s", code, stderr)
	}
}

// A start of `harness local` with --keep answers the mark the start that seeded its database
// wrote, so a load continues there; a start without it reset the records and marks them anew.
func TestAKeepRestartKeepsTheDatabasesSeedMark(t *testing.T) {
	dsn := os.Getenv("DISPATCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DISPATCH_TEST_DATABASE_URL names no Postgres server")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := "dispatch_harness_seed_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := admin.Exec(ctx, "create database "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "drop database if exists "+pgx.Identifier{name}.Sanitize()) })
	dbURL, err := databaseURL(dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	if mark, err := databaseSeeded(ctx, dbURL, name, true); err != nil || mark != "" {
		t.Fatalf("a database no start marked: %q, %v", mark, err)
	}
	seeded, err := databaseSeeded(ctx, dbURL, name, false)
	if err != nil || seeded == "" {
		t.Fatalf("the seeding start's mark: %q, %v", seeded, err)
	}
	kept, err := databaseSeeded(ctx, dbURL, name, true)
	if err != nil || kept != seeded {
		t.Errorf("a --keep restart answers %q (%v), want the seeding start's %q", kept, err, seeded)
	}
	reset, err := databaseSeeded(ctx, dbURL, name, false)
	if err != nil || reset == "" || reset == seeded {
		t.Errorf("a start that reset the database answers %q (%v), want a mark other than %q", reset, err, seeded)
	}
}

// A second `harness local` with the --out whose local.json a running instance wrote refuses it
// before it starts anything, and leaves the running instance's local.json in place.
func TestLocalRefusesAnOutWhoseLocalJSONExists(t *testing.T) {
	out := t.TempDir()
	held := filepath.Join(out, "local.json")
	writeJSONFile(t, held, LocalInstance{BaseURL: "http://127.0.0.1:4100", Database: "dispatch_harness_1", DatabaseURL: "postgres://127.0.0.1:1/dispatch_harness_1"})
	before, err := os.ReadFile(held)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runHarness("local", "--out", out, "--pg-dsn", "postgres://harness@127.0.0.1:1/postgres")
	if code != 2 || !strings.Contains(stderr, "another `harness local` holds this --out") {
		t.Errorf("harness local over a held --out = %d:\n%s", code, stderr)
	}
	if after, err := os.ReadFile(held); err != nil || string(after) != string(before) {
		t.Errorf("the refused start changed or removed the running instance's local.json (%v)", err)
	}
}
