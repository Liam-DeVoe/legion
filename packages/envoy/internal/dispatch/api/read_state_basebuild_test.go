//go:build basebuild

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// c-read-state-tuples (dispatch://INFRA-1152 R6) over marks the release before this branch wrote:
// the build of BASELINE_SHA, checked out at $TMPDIR/base-read-state and booted over
// $ISSUE_READ_STATE_DATABASE_URL, which it migrates, as the Verification preamble states (T1's
// harness, one_inbox_basebuild_test.go). It boots a server of an earlier commit because what it
// reads back - marks a release before this one stored, with no tuple beside them - this build
// cannot write. It carries the basebuild tag, as the other base-build checks do.
const (
	readStateDatabaseEnv = "ISSUE_READ_STATE_DATABASE_URL"
	readStateDatabase    = "dispatch_infra_1152_read_state"
	readStateWorktree    = "base-read-state"
)

// readStateTables are the six read-state tables, each with the columns its rows are keyed and
// ordered by, and the marker columns this branch added, which a captured row cannot hold.
var readStateTables = []struct {
	table, order string
	markers      []string
}{
	{"user_threads", "login, thread_id", []string{"marked_version", "marked_seq"}},
	{"user_agent_read", "login, session_id", []string{"tuples_stored_at"}},
	{"user_agent_state", "login, session_id", []string{"tuples_stored_at"}},
	{"user_agent_reply_read", "login, reply_id", []string{"tuples_stored_at"}},
	{"user_agent_thread_dismissed", "login, message_id", []string{"tuples_stored_at"}},
	{"user_ask_snooze", "login, ask_id", []string{"tuples_stored_at"}},
}

// readStateMarks is what the base build wrote that the test reads back: the issue, alice's read
// thread and her dismissed thread, the two direct conversations, the reply she read by its id,
// and the ask she snoozed.
type readStateMarks struct {
	issue, readThread, dismissedThread   string
	conversation, agentThread, readReply string
	snoozed                              string
}

// The test writes the six kinds of mark through the base build's own API: a thread read, a thread
// dismissed, a conversation's read mark and its Clear, a reply read by its id, an Agent thread
// dismissed, and an ask snoozed; the Clear is stored under a second spelling of the person's
// login, with a capital and a trailing space, as a release before the canonical convention kept
// it. It captures every row of the six tables and stops the base build. This branch's build then
// migrates the database, runs the backfill (graphcensus.Backfill) and serves it. For each captured
// row it reads a tuple from the person with the row's far end and the row's own moment, every
// captured column unchanged and the row marked, a user_threads row at marked_version equal to its
// captured version. It reads the read mark and the Clear, stored under two spellings, from one
// person node, and the person's state of that conversation holding the Clear. Then a reply by
// somebody else into the dismissed thread retracts the dismissal rather than overwriting it and
// raises the row's version by one, by the equation of R6; a delivery that is no reply, the person
// made the one an open ask waits on, raises nothing; a dismissal held to the older version is
// refused; and replied_at and last_reply_id are the newest reply-caused delivery's. Last, no node
// of a read-state kind exists, and each of the six tables holds exactly the marks its tuples
// project.
func TestReadStateIsTuplesFromThePersonAndSixProjections(t *testing.T) {
	ctx := context.Background()
	databaseURL := baselineDatabaseURL(t, readStateDatabaseEnv, readStateDatabase)
	binary, commit := buildBaseline(t, filepath.Join(os.TempDir(), readStateWorktree))
	base := bootBaseline(t, binary, databaseURL)
	marks := writeMarksThroughTheBase(t, base, databaseURL)
	captured := captureReadState(t, databaseURL)
	base.process.Terminate(t)
	base.process.WaitExit(t, "the base build was told to stop")
	t.Logf("the base build %s wrote %d user_threads, %d user_agent_read, %d user_agent_state, %d user_agent_reply_read, %d user_agent_thread_dismissed and %d user_ask_snooze rows",
		commit, len(captured["user_threads"]), len(captured["user_agent_read"]), len(captured["user_agent_state"]),
		len(captured["user_agent_reply_read"]), len(captured["user_agent_thread_dismissed"]), len(captured["user_ask_snooze"]))
	for _, table := range readStateTables {
		if len(captured[table.table]) == 0 {
			t.Fatalf("the base build wrote no %s row, so this test would read back nothing of it", table.table)
		}
	}

	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open the database %s names: %v", readStateDatabaseEnv, err)
	}
	t.Cleanup(database.Pool.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate the base build's database forward: %v", err)
	}
	report, err := graphcensus.Backfill(ctx, database, io.Discard)
	if err != nil {
		t.Fatalf("backfill the store: %v", err)
	}
	for _, kind := range report.Kinds {
		if !kind.Complete {
			t.Fatalf("the backfill left %s owing %d rows: %#v", kind.Kind, kind.Remaining, kind)
		}
	}
	handler, _, _ := newTestServer(t, testServerOptions{store: database, envoyURL: standInListener(t)})

	t.Run("each captured row is a tuple from the person at its own moment, unchanged and marked", func(t *testing.T) {
		requireCapturedRowsAreTuples(t, database, captured)
	})
	t.Run("the two spellings of one login meet at one person node", func(t *testing.T) {
		requireOneNodeForTwoSpellings(t, handler, database, captured, marks)
	})
	t.Run("a reply retracts a dismissal and counts once, a move of who an ask waits on counts nothing", func(t *testing.T) {
		requireAReplyRetractsTheDismissal(t, handler, database, marks)
	})
	t.Run("no node is a mark, and each table is what its tuples project", func(t *testing.T) {
		requireNoMarkNode(t, database)
		requireEachTableIsItsTuples(t, database)
	})
}

// basePost sends body to path as login on the base build and answers the id the answer names and
// its created_at, failing unless it answered 201.
func basePost(t *testing.T, base *baselineServer, login, path string, body any) (string, time.Time) {
	t.Helper()
	answer := base.call(t, login, http.MethodPost, path, body)
	if answer.status != http.StatusCreated {
		t.Fatalf("POST %s as %q on the base build: %d %s", path, login, answer.status, answer.body)
	}
	var created struct {
		ID        string    `json:"id"`
		Key       string    `json:"key"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(answer.body, &created); err != nil {
		t.Fatalf("decode the answer to POST %s: %v: %s", path, err, answer.body)
	}
	if created.ID == "" {
		created.ID = created.Key
	}
	return created.ID, created.CreatedAt
}

// baseWrite sends body to path as login on the base build and fails unless it answered one of ok.
func baseWrite(t *testing.T, base *baselineServer, login, method, path string, body any, ok ...int) apiAnswer {
	t.Helper()
	answer := base.call(t, login, method, path, body)
	if !slices.Contains(ok, answer.status) {
		t.Fatalf("%s %s as %q on the base build: %d %s", method, path, login, answer.status, answer.body)
	}
	return answer
}

// writeMarksThroughTheBase writes, through the base build's own API, a thread alice reads, a
// thread she dismisses, a conversation with session s1 whose read mark she sets, whose reply she
// reads by its id and which she Clears - its row then spelled `Alice `, the same person under the
// raw actor id user_agent_state keys a Clear on - an Agent thread she dismisses, and an ask she
// snoozes. bob writes the replies that reach her.
func writeMarksThroughTheBase(t *testing.T, base *baselineServer, databaseURL string) readStateMarks {
	t.Helper()
	const alice, aliceSpelled, bob = "alice", "Alice ", "bob"
	var marks readStateMarks
	baseWrite(t, base, alice, http.MethodPost, "/api/v1/projects", map[string]string{"key": "RSB", "name": "Read state"}, http.StatusCreated)
	marks.issue, _ = basePost(t, base, alice, "/api/v1/issues", map[string]string{
		"project": "RSB", "title": "Marks of the release before", "spec": "A spec.",
	})
	comments := "/api/v1/issues/" + marks.issue + "/comments"

	marks.readThread, _ = basePost(t, base, alice, comments, map[string]any{"body": "Ship on Friday?"})
	_, replied := basePost(t, base, bob, comments, map[string]any{"body": "Friday works.", "reply_to": marks.readThread})
	baseWrite(t, base, alice, http.MethodPut, "/api/v1/me/threads/"+marks.readThread+"/read",
		map[string]string{"read_through": replied.UTC().Format(time.RFC3339Nano)}, http.StatusNoContent)

	marks.dismissedThread, _ = basePost(t, base, alice, comments, map[string]any{"body": "Is the copy final?"})
	basePost(t, base, bob, comments, map[string]any{"body": "Nearly.", "reply_to": marks.dismissedThread})
	baseWrite(t, base, alice, http.MethodPost, "/api/v1/me/inbox/dismiss", map[string]any{
		"threads":      []string{inboxDismissReplyPrefix + marks.dismissedThread},
		"seen_replies": map[string]any{inboxDismissReplyPrefix + marks.dismissedThread: map[string]any{"version": baseVersion(t, databaseURL, alice, marks.dismissedThread)}},
	}, http.StatusOK)

	reply := func(root, body string) (string, time.Time) {
		return basePost(t, base, "", "/api/v1/messages/"+root+"/reply", map[string]any{
			"body": body, "attempt": 1, "actor": map[string]string{"kind": "session", "id": "s1"},
		})
	}
	// A reply is keyed on the message it answers and the attempt it answers (the base build
	// answers a second reply of one attempt as a duplicate of the first), so the second reply
	// answers a second message of alice's in the same conversation.
	marks.conversation, _ = basePost(t, base, alice, "/api/v1/agents/s1/messages", map[string]any{"body": "Can you look?", "delivery": "btw"})
	_, first := reply(marks.conversation, "Looking.")
	again, _ := basePost(t, base, alice, "/api/v1/agents/s1/messages", map[string]any{"body": "And the copy?", "delivery": "btw"})
	marks.readReply, _ = reply(again, "Found it.")
	baseWrite(t, base, alice, http.MethodPut, "/api/v1/me/agents/s1/state", map[string]any{
		"read_through": first.UTC().Format(time.RFC3339Nano), "read_replies": []string{marks.readReply},
	}, http.StatusOK, http.StatusNoContent)
	baseWrite(t, base, alice, http.MethodPut, "/api/v1/me/agents/s1/state", map[string]any{
		"cleared_before": first.UTC().Format(time.RFC3339Nano),
	}, http.StatusOK, http.StatusNoContent)
	// user_agent_state keys a Clear on the raw actor id (api/state.go), and its rows predate the
	// canonical convention migrations 0048 and 0051 follow, so a row of it can hold a spelling of
	// a login no other table holds. Every sign-in of the base build lowercases and trims the login
	// it is given (its routes/devsignin.go and identity.HeaderIdentity), so no route of it stores
	// such a spelling: the row is given the spelling a release before that convention stored,
	// with a capital and a trailing space, before it is captured.
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect to the base build's database: %v", err)
	}
	defer conn.Close(context.Background())
	if tag, err := conn.Exec(context.Background(),
		`update user_agent_state set login = $1 where login = $2 and session_id = 's1'`, aliceSpelled, alice); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("spell the Clear of s1 as %q: %v (%d rows)", aliceSpelled, err, tag.RowsAffected())
	}

	marks.agentThread, _ = basePost(t, base, alice, "/api/v1/agents/s1/messages", map[string]any{"body": "And the build?", "delivery": "btw"})
	answered, _ := reply(marks.agentThread, "Green.")
	baseWrite(t, base, alice, http.MethodPost, "/api/v1/me/inbox/dismiss", map[string]any{
		"threads": []string{inboxDismissAgentPrefix + marks.agentThread},
		"seen_replies": map[string]any{inboxDismissAgentPrefix + marks.agentThread: map[string]any{
			"newest_reply": answered, "unread_replies": []string{answered},
		}},
	}, http.StatusOK)

	marks.snoozed, _ = basePost(t, base, bob, "/api/v1/issues/"+marks.issue+"/asks", map[string]any{
		"question": "Should we ship?", "options": []map[string]string{{"label": "Ship"}, {"label": "Hold"}},
	})
	baseWrite(t, base, alice, http.MethodPut, "/api/v1/me/asks/"+marks.snoozed+"/snooze", map[string]any{
		"snoozed_until": time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second).Format(time.RFC3339),
	}, http.StatusOK)
	return marks
}

// baseVersion is login's user_threads version on thread, as the page the base build serves draws it.
func baseVersion(t *testing.T, databaseURL, login, thread string) int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to the base build's database: %v", err)
	}
	defer conn.Close(ctx)
	var version int64
	if err := conn.QueryRow(ctx, `select version from user_threads where login = $1 and thread_id = $2::uuid`, login, thread).Scan(&version); err != nil {
		t.Fatalf("read %s's version on thread %s: %v", login, thread, err)
	}
	return version
}

// capturedRows is every row of each read-state table as the base build left it, each row its
// columns as JSON, by table.
type capturedRows map[string][]map[string]any

// captureReadState reads every row of the six tables, on a connection of its own, while the base
// build still serves: what the backfill and every later read is held to.
func captureReadState(t *testing.T, databaseURL string) capturedRows {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to the base build's database: %v", err)
	}
	defer conn.Close(ctx)
	captured := capturedRows{}
	for _, table := range readStateTables {
		rows, err := conn.Query(ctx, `select to_jsonb(r) from `+table.table+` r order by `+table.order)
		if err != nil {
			t.Fatalf("capture %s: %v", table.table, err)
		}
		read, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
		if err != nil {
			t.Fatalf("capture %s: %v", table.table, err)
		}
		captured[table.table] = read
	}
	return captured
}

// currentMark is the newest tuple of kind from login's person node to toKind/toID: its moment and
// whether it asserts the mark, and false where the store holds none.
func currentMark(t *testing.T, database *store.Store, login, kind, toKind, toID string) (time.Time, bool, bool) {
	t.Helper()
	var at time.Time
	var asserted bool
	err := database.Pool.QueryRow(context.Background(), `
		select at, asserted from graph_tuples
		where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
		order by seq desc limit 1
	`, graphstore.KindPerson, model.CanonicalLogin(login), kind, toKind, toID).Scan(&at, &asserted)
	if err == pgx.ErrNoRows {
		return time.Time{}, false, false
	}
	if err != nil {
		t.Fatalf("read %s's %s to %s/%s: %v", login, kind, toKind, toID, err)
	}
	return at, asserted, true
}

// capturedTime is the timestamp column of a captured row.
func capturedTime(t *testing.T, row map[string]any, column string) time.Time {
	t.Helper()
	text, ok := row[column].(string)
	if !ok {
		t.Fatalf("the captured row %v holds no %s", row, column)
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatalf("read the captured %s %q: %v", column, text, err)
	}
	return at
}

// requireMarkAt fails unless login's newest tuple of kind to toKind/toID asserts at want.
func requireMarkAt(t *testing.T, database *store.Store, what, login, kind, toKind, toID string, want time.Time) {
	t.Helper()
	at, asserted, found := currentMark(t, database, login, kind, toKind, toID)
	if !found || !asserted || !at.Equal(want) {
		t.Errorf("%s: the newest %s from %q to %s/%s is found %t, asserting %t, at %s; want it asserting at %s",
			what, kind, login, toKind, toID, found, asserted, at, want)
	}
}

// requireCapturedRowsAreTuples holds every captured row to its tuple and to the row as it now
// stands: the same columns, and the marker set.
func requireCapturedRowsAreTuples(t *testing.T, database *store.Store, captured capturedRows) {
	t.Helper()
	ctx := context.Background()
	for _, table := range readStateTables {
		for _, row := range captured[table.table] {
			login, _ := row["login"].(string)
			what := fmt.Sprintf("the captured %s row %v", table.table, row)
			switch table.table {
			case "user_threads":
				thread, kind := row["thread_id"].(string), row["kind"].(string)
				requireMarkAt(t, database, what, login, graphstore.TupleReadThrough, kind, thread, capturedTime(t, row, "read_through"))
				if row["dismissed_at"] != nil {
					requireMarkAt(t, database, what, login, graphstore.TupleDismissed, kind, thread, capturedTime(t, row, "dismissed_at"))
				}
				if _, asserted, found := currentParticipation(t, database, login, kind, thread); !found || !asserted {
					t.Errorf("%s: %s takes no part in %s/%s", what, login, kind, thread)
				}
			case "user_agent_read":
				requireMarkAt(t, database, what, login, graphstore.TupleReadThrough, graphstore.KindSession, row["session_id"].(string), capturedTime(t, row, "read_through"))
			case "user_agent_state":
				requireMarkAt(t, database, what, login, graphstore.TupleCleared, graphstore.KindSession, row["session_id"].(string), capturedTime(t, row, "cleared_before"))
			case "user_agent_reply_read":
				if _, asserted, found := currentMark(t, database, login, graphstore.TupleReplyRead, graphstore.KindMessage, row["reply_id"].(string)); !found || !asserted {
					t.Errorf("%s: no reply_read from %q to message %s stands", what, login, row["reply_id"])
				}
			case "user_agent_thread_dismissed":
				requireMarkAt(t, database, what, login, graphstore.TupleDismissed, graphstore.KindMessage, row["message_id"].(string), capturedTime(t, row, "dismissed_at"))
			case "user_ask_snooze":
				requireMarkAt(t, database, what, login, graphstore.TupleSnoozed, graphstore.KindAsk, row["ask_id"].(string), capturedTime(t, row, "snoozed_until"))
			}
			// Every captured column reads unchanged, and the row carries the marker this branch adds.
			keys := strings.Split(table.order, ", ")
			conditions := make([]string, len(keys))
			args := make([]any, len(keys))
			for index, key := range keys {
				conditions[index] = fmt.Sprintf("r.%s::text = $%d", key, index+1)
				args[index] = fmt.Sprint(row[key])
			}
			var now map[string]any
			if err := database.Pool.QueryRow(ctx, `select to_jsonb(r) from `+table.table+` r where `+strings.Join(conditions, " and "), args...).Scan(&now); err != nil {
				t.Fatalf("%s: read it back: %v", what, err)
			}
			for _, marker := range table.markers {
				if now[marker] == nil {
					t.Errorf("%s: its %s is null after the backfill, want the row marked", what, marker)
				}
				delete(now, marker)
			}
			if !maps.EqualFunc(now, row, func(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) {
				t.Errorf("%s: the backfill changed it to %v; every captured column must read unchanged", what, now)
			}
			if table.table == "user_threads" {
				var markedVersion, version int64
				if err := database.Pool.QueryRow(ctx, `
					select marked_version, version from user_threads where login = $1 and thread_id = $2::uuid
				`, login, row["thread_id"]).Scan(&markedVersion, &version); err != nil {
					t.Fatalf("%s: read its marker: %v", what, err)
				}
				if markedVersion != version || fmt.Sprint(row["version"]) != fmt.Sprint(version) {
					t.Errorf("%s: marked_version %d over version %d, want the captured version %v", what, markedVersion, version, row["version"])
				}
			}
		}
	}
}

// currentParticipation is the newest participates_in from login to kind/id.
func currentParticipation(t *testing.T, database *store.Store, login, kind, id string) (time.Time, bool, bool) {
	t.Helper()
	var at time.Time
	var asserted bool
	err := database.Pool.QueryRow(context.Background(), `
		select at, asserted from graph_tuples
		where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
		order by seq desc limit 1
	`, graphstore.KindPerson, model.CanonicalLogin(login), graphstore.TupleParticipatesIn, kind, id).Scan(&at, &asserted)
	if err == pgx.ErrNoRows {
		return time.Time{}, false, false
	}
	if err != nil {
		t.Fatalf("read %s's participation in %s/%s: %v", login, kind, id, err)
	}
	return at, asserted, true
}

// requireOneNodeForTwoSpellings holds the read mark the base build keyed on the canonical login
// and the Clear stored under the raw spelling to one person node, and the person's state of the
// conversation to that Clear.
func requireOneNodeForTwoSpellings(t *testing.T, handler http.Handler, database *store.Store, captured capturedRows, marks readStateMarks) {
	t.Helper()
	read, cleared := captured["user_agent_read"][0]["login"], captured["user_agent_state"][0]["login"]
	if read == cleared {
		t.Fatalf("the read mark and the Clear are keyed on one spelling, %q, so this case proves nothing", read)
	}
	clearedAt := capturedTime(t, captured["user_agent_state"][0], "cleared_before")
	froms, err := readStrings(context.Background(), database.Pool, `
		select distinct from_id from graph_tuples
		where kind = any($1) and to_kind = $2 and to_id = 's1' order by 1
	`, []string{graphstore.TupleReadThrough, graphstore.TupleCleared}, graphstore.KindSession)
	if err != nil {
		t.Fatalf("read who the conversation's marks are from: %v", err)
	}
	if len(froms) != 1 || froms[0] != "alice" {
		t.Errorf("the read mark the base build keyed on %q and the Clear keyed on %q run from %q, want both from the one person node alice", read, cleared, froms)
	}
	state := decodeBody[userAgentStates](t, dispatchRequest(t, handler, http.MethodGet, "/api/v1/me/agents/state", nil, "alice"))
	conversation, held := state["s1"]
	if !held || conversation.ClearedBefore == nil || !parseTimestamp(t, *conversation.ClearedBefore).Equal(clearedAt) || conversation.ReadThrough == nil {
		t.Errorf("alice's state of s1 is %#v (held %t), want the read mark and the Clear at %s stored under %q", conversation, held, clearedAt, cleared)
	}
}

// requireAReplyRetractsTheDismissal writes, through this build, bob's reply into the thread alice
// dismissed on the base build, and then hands an open ask to alice.
func requireAReplyRetractsTheDismissal(t *testing.T, handler http.Handler, database *store.Store, marks readStateMarks) {
	t.Helper()
	ctx := context.Background()
	thread := marks.dismissedThread
	before := threadRowVersion(t, database, "alice", thread)
	reply := createThreadComment(t, handler, marks.issue, map[string]any{"body": "Final now.", "reply_to": thread}, "bob")

	history, err := graphstore.History(ctx, database.Pool, graphstore.KindPerson, "alice", graphstore.TupleDismissed)
	if err != nil {
		t.Fatalf("read alice's dismissals: %v", err)
	}
	var trail []bool
	for _, tuple := range history {
		if tuple.ToKind == graphstore.KindComment && tuple.ToID == thread {
			trail = append(trail, tuple.Asserted)
		}
	}
	if !slices.Equal(trail, []bool{true, false}) {
		t.Errorf("alice's dismissed tuples of thread %s read %v, want the dismissal and then its retraction", thread, trail)
	}
	after := threadRowVersion(t, database, "alice", thread)
	if after != before+1 {
		t.Errorf("alice's row of thread %s is at version %d after bob's reply, want %d", thread, after, before+1)
	}
	requireVersionCountsReplies(t, database, "alice", graphstore.KindComment, thread)

	// replied_at and last_reply_id are the newest reply-caused delivery into alice from the thread.
	var repliedAt time.Time
	var lastReply string
	if err := database.Pool.QueryRow(ctx, `
		select replied_at, last_reply_id::text from user_threads where login = 'alice' and thread_id = $1::uuid
	`, thread).Scan(&repliedAt, &lastReply); err != nil {
		t.Fatalf("read alice's row of thread %s: %v", thread, err)
	}
	var newestAt time.Time
	var newestFrom string
	if err := database.Pool.QueryRow(ctx, `
		select d.at, d.from_id from graph_tuples d join events e on e.id = d.source_event
		where d.kind = $1 and d.to_kind = $2 and d.to_id = 'alice' and d.asserted
		  and e.type in ('comment.created', 'comment.answered', 'message.created', 'message.answered', 'ask.answered')
		  and (d.from_id = $3 or exists (select 1 from graph_tuples r where r.from_kind = d.from_kind and r.from_id = d.from_id
		                                 and r.kind = $4 and r.to_id = $3 and r.asserted))
		order by d.at desc, d.seq desc limit 1
	`, graphstore.TupleDeliveredTo, graphstore.KindPerson, thread, graphstore.TupleRepliesTo).Scan(&newestAt, &newestFrom); err != nil {
		t.Fatalf("read the newest reply delivered to alice from thread %s: %v", thread, err)
	}
	if !repliedAt.Equal(newestAt) || lastReply != newestFrom || lastReply != reply.ID {
		t.Errorf("alice's row of thread %s replied at %s by %s, want the newest reply-caused delivery's %s by %s (bob's reply %s)",
			thread, repliedAt, lastReply, newestAt, newestFrom, reply.ID)
	}

	// A dismissal held to the version the row stood at before the reply is refused.
	stale := postInboxDismiss(t, handler, "alice", map[string]any{
		"threads":      []string{inboxDismissReplyPrefix + thread},
		"seen_replies": map[string]any{inboxDismissReplyPrefix + thread: map[string]any{"version": before}},
	})
	var dismissedAt *time.Time
	if err := database.Pool.QueryRow(ctx, `select dismissed_at from user_threads where login = 'alice' and thread_id = $1::uuid`, thread).Scan(&dismissedAt); err != nil {
		t.Fatalf("read alice's dismissal of thread %s: %v", thread, err)
	}
	if dismissedAt != nil || (stale.Code == http.StatusOK && dismissedBy(t, stale) != 0) {
		t.Errorf("a dismissal held to version %d answered %d %s and left the row dismissed at %v, want it refused",
			before, stale.Code, stale.Body.String(), dismissedAt)
	}

	// Handing an open ask to alice delivers it to her and raises no version.
	asked := createPersonAsk(t, handler, marks.issue, "bob", "Who signs off?")
	handAskOn(t, handler, asked.ID, "alice", "bob")
	if _, asserted, found := currentDelivery(t, database, asked.ID, "alice"); !found || !asserted {
		t.Errorf("handing ask %s to alice delivered it to her %t (asserting %t), want a delivered_to", asked.ID, found, asserted)
	}
	if moved := threadRowVersion(t, database, "alice", thread); moved != after {
		t.Errorf("alice's row of thread %s moved to version %d when an ask was handed to her, want %d", thread, moved, after)
	}
	requireVersionCountsReplies(t, database, "alice", graphstore.KindComment, thread)
}

func threadRowVersion(t *testing.T, database *store.Store, login, thread string) int64 {
	t.Helper()
	var version int64
	if err := database.Pool.QueryRow(context.Background(), `
		select version from user_threads where login = $1 and thread_id = $2::uuid
	`, login, thread).Scan(&version); err != nil {
		t.Fatalf("read %s's version of thread %s: %v", login, thread, err)
	}
	return version
}

// currentDelivery is the newest delivered_to from ask askID to login.
func currentDelivery(t *testing.T, database *store.Store, askID, login string) (time.Time, bool, bool) {
	t.Helper()
	var at time.Time
	var asserted bool
	err := database.Pool.QueryRow(context.Background(), `
		select at, asserted from graph_tuples
		where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4 and to_id = $5
		order by seq desc limit 1
	`, graphstore.KindAsk, askID, graphstore.TupleDeliveredTo, graphstore.KindPerson, login).Scan(&at, &asserted)
	if err == pgx.ErrNoRows {
		return time.Time{}, false, false
	}
	if err != nil {
		t.Fatalf("read the delivery of ask %s to %s: %v", askID, login, err)
	}
	return at, asserted, true
}

// requireNoMarkNode fails on a node of any read-state kind: a mark is a tuple, never a node.
func requireNoMarkNode(t *testing.T, database *store.Store) {
	t.Helper()
	var nodes int64
	if err := database.Pool.QueryRow(context.Background(), `select count(*) from graph_nodes where kind = any($1)`,
		[]string{graphstore.TupleReadThrough, graphstore.TupleDismissed, graphstore.TupleCleared, graphstore.TupleReplyRead, graphstore.TupleSnoozed}).
		Scan(&nodes); err != nil {
		t.Fatalf("count the nodes of a read-state kind: %v", err)
	}
	if nodes != 0 {
		t.Errorf("the store holds %d nodes of a read-state kind, want none: a mark is a tuple from the person", nodes)
	}
}

// requireEachTableIsItsTuples holds each of the six tables to exactly the marks its tuples
// project: each row's mark, by its person's canonical login, far end and moment, against each
// current asserting tuple of its kind and far end's kind. The corpus holds no issue message, so a
// dismissed tuple to a message is an Agent thread's and one to a comment or an ask a Reply
// thread's.
func requireEachTableIsItsTuples(t *testing.T, database *store.Store) {
	t.Helper()
	for _, projection := range []struct {
		table, rows, kind string
		toKinds           []string
		moment            bool
	}{
		{"user_threads read marks", `select user_threads_login(login), kind, thread_id::text, read_through from user_threads`,
			graphstore.TupleReadThrough, []string{graphstore.KindComment, graphstore.KindAsk, graphstore.KindMessage}, true},
		{"user_threads dismissals", `select user_threads_login(login), kind, thread_id::text, dismissed_at from user_threads where dismissed_at is not null`,
			graphstore.TupleDismissed, []string{graphstore.KindComment, graphstore.KindAsk}, true},
		{"user_agent_read", `select user_threads_login(login), 'session', session_id, read_through from user_agent_read`,
			graphstore.TupleReadThrough, []string{graphstore.KindSession}, true},
		{"user_agent_state", `select user_threads_login(login), 'session', session_id, cleared_before from user_agent_state`,
			graphstore.TupleCleared, []string{graphstore.KindSession}, true},
		{"user_agent_reply_read", `select user_threads_login(login), 'message', reply_id::text, now() from user_agent_reply_read`,
			graphstore.TupleReplyRead, []string{graphstore.KindMessage}, false},
		{"user_agent_thread_dismissed", `select user_threads_login(login), 'message', message_id::text, dismissed_at from user_agent_thread_dismissed`,
			graphstore.TupleDismissed, []string{graphstore.KindMessage}, true},
		{"user_ask_snooze", `select user_threads_login(login), 'ask', ask_id::text, snoozed_until from user_ask_snooze`,
			graphstore.TupleSnoozed, []string{graphstore.KindAsk}, true},
	} {
		mark := `%s > %s/%s`
		if projection.moment {
			mark += ` @ %s`
		}
		read := func(statement string, args ...any) []string {
			rows, err := database.Pool.Query(context.Background(), statement, args...)
			if err != nil {
				t.Fatalf("read %s: %v", projection.table, err)
			}
			marks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
				var login, kind, id string
				var at time.Time
				if err := row.Scan(&login, &kind, &id, &at); err != nil {
					return "", err
				}
				if projection.moment {
					return fmt.Sprintf(mark, login, kind, id, at.UTC().Format(time.RFC3339Nano)), nil
				}
				return fmt.Sprintf(mark, login, kind, id), nil
			})
			if err != nil {
				t.Fatalf("read %s: %v", projection.table, err)
			}
			slices.Sort(marks)
			return marks
		}
		rows := read(projection.rows)
		tuples := read(`
			select current.from_id, current.to_kind, current.to_id, current.at from (
				`+graphstore.CurrentTupleRead(`t.from_id, t.to_kind, t.to_id`, `t.from_id, t.to_kind, t.to_id, t.at, t.asserted`,
			`t.from_kind = $1 and t.kind = $2 and t.to_kind = any($3)`)+`
			) current where current.asserted`, graphstore.KindPerson, projection.kind, projection.toKinds)
		if !slices.Equal(rows, tuples) {
			t.Errorf("%s holds\n  %s\nwhere its tuples project\n  %s", projection.table, strings.Join(rows, "\n  "), strings.Join(tuples, "\n  "))
		}
	}
}
