package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/startup"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// projectionTriggersBuild is the commit the boot's retirements run for in these tests, so an
// announcement a test writes under another commit names the build a rollback reaches.
const projectionTriggersBuild = "c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7"

// projectionTriggerFunctions are the five stored functions the inbox release kept for one release:
// the two that write a thread's projection, user_threads_record and
// user_agent_thread_dismissed_clear, and the three trigger functions that call user_threads_record.
var projectionTriggerFunctions = []string{
	"user_agent_thread_dismissed_clear", "user_threads_ask", "user_threads_comment", "user_threads_message", "user_threads_record",
}

// projectionTriggerObjectsSQL is each of the five functions pg_proc holds, as "pg_proc <name>", and
// each trigger pg_trigger holds that calls one - whose function is one of the five, or whose
// function's body calls one - as "pg_trigger <table>.<trigger> -> <function>".
const projectionTriggerObjectsSQL = `
select 'pg_proc ' || p.proname
  from pg_proc p join pg_namespace n on n.oid = p.pronamespace
 where n.nspname = current_schema() and p.proname = any($1)
union all
select 'pg_trigger ' || c.relname || '.' || t.tgname || ' -> ' || p.proname
  from pg_trigger t
  join pg_class c on c.oid = t.tgrelid
  join pg_namespace n on n.oid = c.relnamespace
  join pg_proc p on p.oid = t.tgfoid
 where not t.tgisinternal and n.nspname = current_schema()
   and (p.proname = any($1) or p.prosrc ~* ('\m(' || array_to_string($1, '|') || ')\s*\('))`

// projectionTriggerObjects is what projectionTriggerObjectsSQL answers over a database holding the
// five functions and the four triggers that run them, sorted.
var projectionTriggerObjects = []string{
	"pg_proc user_agent_thread_dismissed_clear", "pg_proc user_threads_ask", "pg_proc user_threads_comment",
	"pg_proc user_threads_message", "pg_proc user_threads_record",
	"pg_trigger asks.user_threads_ask -> user_threads_ask",
	"pg_trigger comments.user_threads_comment -> user_threads_comment",
	"pg_trigger messages.user_agent_thread_dismissed_clear -> user_agent_thread_dismissed_clear",
	"pg_trigger messages.user_threads_message -> user_threads_message",
}

// The inbox release keeps, for one release, two stored functions that write a thread's projection
// for a row an older build wrote - user_threads_record and user_agent_thread_dismissed_clear - with
// the triggers that reach them and the three functions those triggers run. They go in a step of
// every boot (startup.ProjectionTriggersRetirement, one of startup.Retirements), never in a
// migration: no migration file drops one, read after the same scan finds a planted drop in a
// scratch copy. The exemption list of TestNoProjectionRowIsWrittenWithoutTheStore is empty.
//
// The step runs as the server runs it after its migrations, through startup.Retire with every
// registered entry, over four databases:
//   - one where every row of comments, messages and asks carries its marker and the newest
//     announcement of another build lists the inbox release's kinds: the five functions and every
//     trigger that calls one go, a second boot changes nothing, and a comment written afterwards
//     reads its user_threads rows and its tuples from the one Go writer;
//   - one where a build of the release before wrote a comment with no marker during a roll: the
//     step brings that row in through the backfill first - it is marked, its writer takes part in
//     its thread in the store and their row is marked - then deletes, and the next write into the
//     thread moves that writer's row;
//   - one whose newest such announcement is of the release before the inbox release: every object
//     stays through a first boot, a second process and a restart, each logging the announcement it
//     read, the server serves, and an older build's comment is still projected;
//   - one this boot created from empty: the objects go at once, and no stored function writes a
//     projected table or a thread's projection.
//
// user_threads_login stays wherever the others go.
func TestTheProjectionTriggersGoWithTheirArms(t *testing.T) {
	t.Run("a step of every boot", requireTheTriggersGoInABootStep)
	t.Run("the exemption list", func(t *testing.T) {
		if len(markerTestedProjectionWriters) != 0 {
			t.Errorf("TestNoProjectionRowIsWrittenWithoutTheStore exempts %v, want an empty list", markerTestedProjectionWriters)
		}
	})
	t.Run("released", requireTheReleasedDatabaseLosesTheTriggers)
	t.Run("a roll", requireARollsUnmarkedCommentComesInFirst)
	t.Run("release before", requireTheReleaseBeforeKeepsTheTriggers)
	t.Run("from empty", requireAnEmptyDatabaseLosesTheTriggersAtOnce)
}

// projectionTriggerDrop matches a statement that drops one of the five functions or one of the
// four triggers, each of which bears its function's name.
var projectionTriggerDrop = regexp.MustCompile(`(?is)\bdrop\s+(?:function|trigger)\s+(?:if\s+exists\s+)?(?:` +
	strings.Join(projectionTriggerFunctions, "|") + `)\b`)

// migrationDropsOfTheTriggers is each statement of a SQL file of dir that drops one of the five
// functions or the four triggers, as "<file>: <statement>", read with its line comments dropped.
func migrationDropsOfTheTriggers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var drops []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, drop := range projectionTriggerDrop.FindAllString(withoutSQLComments(string(body)), -1) {
			drops = append(drops, entry.Name()+": "+drop)
		}
	}
	return drops
}

func requireTheTriggersGoInABootStep(t *testing.T) {
	name := startup.ProjectionTriggersRetirement.Name
	if !slices.ContainsFunc(startup.Retirements, func(retirement startup.Retirement) bool { return retirement.Name == name }) {
		t.Errorf("startup.Retirements holds no entry %q, so no boot deletes the triggers", name)
	}

	planted := t.TempDir()
	if err := os.WriteFile(filepath.Join(planted, "29990101000000_planted.up.sql"),
		[]byte("-- planted\ndrop trigger if exists user_threads_comment on comments;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if drops := migrationDropsOfTheTriggers(t, planted); len(drops) != 1 {
		t.Fatalf("the scan finds %v in a scratch copy that drops a trigger: it cannot find what it looks for", drops)
	}
	if drops := migrationDropsOfTheTriggers(t, filepath.Join("..", "store", "migrations")); len(drops) != 0 {
		t.Errorf("migrations drop the inbox release's projection objects, which only the boot's step deletes:\n  %s",
			strings.Join(drops, "\n  "))
	}
}

// retireAsTheBootDoes runs the boot's retirements over pool as a process of projectionTriggersBuild
// runs them after its migrations (cmd/dispatch's main): every registered entry, for a boot that
// created the database from empty where fromEmpty. It answers what they logged.
func retireAsTheBootDoes(t *testing.T, pool *store.Pool, fromEmpty bool) string {
	t.Helper()
	var logged bytes.Buffer
	startup.Retire(context.Background(), pool, projectionTriggersBuild, fromEmpty,
		slog.New(slog.NewTextHandler(&logged, nil)), startup.Retirements)
	t.Logf("the boot's retirements logged:\n%s", logged.String())
	return logged.String()
}

// projectionTriggerLines is each line of logged that the triggers' retirement wrote.
func projectionTriggerLines(logged string) []string {
	named := fmt.Sprintf("retirement=%q", startup.ProjectionTriggersRetirement.Name)
	var lines []string
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, named) {
			lines = append(lines, line)
		}
	}
	return lines
}

// requireLogged fails unless one of lines holds every one of words.
func requireLogged(t *testing.T, lines []string, when string, words ...string) {
	t.Helper()
	if !slices.ContainsFunc(lines, func(line string) bool {
		for _, word := range words {
			if !strings.Contains(line, word) {
				return false
			}
		}
		return true
	}) {
		t.Errorf("%s the triggers' retirement logged no line holding %q; it logged:\n  %s", when, words, strings.Join(lines, "\n  "))
	}
}

// standingProjectionTriggerObjects is what projectionTriggerObjectsSQL answers over pool, sorted,
// which it prints with the query.
func standingProjectionTriggerObjects(t *testing.T, pool *store.Pool) []string {
	t.Helper()
	standing, err := readStrings(context.Background(), pool, projectionTriggerObjectsSQL, projectionTriggerFunctions)
	if err != nil {
		t.Fatalf("read the catalogue: %v", err)
	}
	slices.Sort(standing)
	t.Logf("%s\n-- $1 = %v\n=> %d rows: %v", strings.TrimSpace(projectionTriggerObjectsSQL), projectionTriggerFunctions, len(standing), standing)
	return standing
}

// requireTheObjectsGone fails unless the catalogue holds none of the five functions and no trigger
// that calls one, and still holds user_threads_login.
func requireTheObjectsGone(t *testing.T, pool *store.Pool, when string) {
	t.Helper()
	if standing := standingProjectionTriggerObjects(t, pool); len(standing) != 0 {
		t.Errorf("%s the catalogue holds %v, want none of the five functions and no trigger that calls one", when, standing)
	}
	var login bool
	if err := pool.QueryRow(context.Background(), `select to_regprocedure('user_threads_login(text)') is not null`).Scan(&login); err != nil {
		t.Fatalf("read whether user_threads_login stands: %v", err)
	}
	if !login {
		t.Errorf("%s user_threads_login is gone, want it kept: this build's reads call it", when)
	}
}

// requireTheObjectsStanding fails unless the catalogue holds the five functions and the four
// triggers.
func requireTheObjectsStanding(t *testing.T, pool *store.Pool, when string) {
	t.Helper()
	if standing := standingProjectionTriggerObjects(t, pool); !slices.Equal(standing, projectionTriggerObjects) {
		t.Errorf("%s the catalogue holds %v, want the five functions and the four triggers %v", when, standing, projectionTriggerObjects)
	}
}

// unmarkedRowsSQL is each row of comments, messages and asks that carries no marker of the inbox
// release, as "<table> <id>": the test each of the triggers reads.
const unmarkedRowsSQL = `
select 'comments ' || id::text from comments where not tuples_stored
union all
select 'messages ' || id::text from messages where node_kind is null or node_address is null
union all
select 'asks ' || id::text from asks
 where tuples_stored_at is null
    or (answer->>'user' is not null and tuples_stored_at is distinct from (answer->>'at')::timestamptz)`

func unmarkedRowsOf(t *testing.T, pool *store.Pool) []string {
	t.Helper()
	unmarked, err := readStrings(context.Background(), pool, unmarkedRowsSQL)
	if err != nil {
		t.Fatalf("read the unmarked rows: %v", err)
	}
	slices.Sort(unmarked)
	return unmarked
}

// projectionTriggerFixture is one database with the server over it, holding an issue and, written
// through the server, a comment, an issue message and an ask on it, each of which this build marks.
type projectionTriggerFixture struct {
	handler  http.Handler
	database *store.Store
	issue    string
}

func seedProjectionTriggers(t *testing.T, project string) projectionTriggerFixture {
	t.Helper()
	handler, database, _ := newTestServer(t, testServerOptions{})
	issue := createInteractionIssue(t, handler, project, "The projection triggers", "# Spec\n\nA spec.\n")
	createThreadComment(t, handler, issue.Key, map[string]any{"body": "A remark this build writes."}, "alice")
	createIssueMessage(t, handler, issue.Key, map[string]any{"body": "A message this build writes."}, "alice")
	createPersonAsk(t, handler, issue.Key, "alice", "Does this build mark its ask?")
	fixture := projectionTriggerFixture{handler: handler, database: database, issue: issue.Key}
	if unmarked := unmarkedRowsOf(t, database.Pool); len(unmarked) != 0 {
		t.Fatalf("the rows this build wrote stand unmarked: %v", unmarked)
	}
	return fixture
}

// announce writes an announcement of another build, under commit, listing kinds.
func (f projectionTriggerFixture) announce(t *testing.T, commit string, kinds []string) {
	t.Helper()
	if _, err := f.database.Pool.Exec(context.Background(),
		`insert into server_build_announcements (commit, tuple_kinds) values ($1, $2)`, commit, kinds); err != nil {
		t.Fatalf("announce %s: %v", commit, err)
	}
}

// writeOlderComment writes a comment as a build of a release before the inbox release writes one,
// on a connection that build opens (olderBuild): its row, in reply to replyTo on issue by the
// person login, naming no node and holding no tuples_stored, and its comment.created event,
// numbered under its issue as that build's broker numbers it, in one transaction. The trigger
// user_threads_comment projects it while that trigger stands. It answers the comment's id and the
// event's id.
func writeOlderComment(t *testing.T, database *store.Store, issue, replyTo, login string) (string, int64) {
	t.Helper()
	ctx := context.Background()
	conn := olderBuild(t, database)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the older build's comment: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	actor := fmt.Sprintf(`{"kind":"user","id":%q}`, login)
	var id string
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		insert into comments (issue_key, author, body, reply_to)
		values ($1, $2::jsonb, 'Written by the release before.', $3::uuid)
		returning id::text, created_at
	`, issue, actor, replyTo).Scan(&id, &createdAt); err != nil {
		t.Fatalf("write a comment as the release before writes one: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"id": id, "issue_key": issue, "reply_to": replyTo, "body": "Written by the release before.",
		"author": map[string]string{"kind": "user", "id": login}, "created_at": createdAt,
	})
	if err != nil {
		t.Fatalf("encode the older comment's comment.created: %v", err)
	}
	var event int64
	if err := tx.QueryRow(ctx, `
		with numbered as (update issues set last_seq = last_seq + 1 where key = $1 returning last_seq)
		insert into events (issue_key, seq, type, actor, payload, notify)
		select $1, numbered.last_seq, 'comment.created', $2::jsonb, $3::jsonb, false from numbered
		returning id
	`, issue, actor, payload).Scan(&event); err != nil {
		t.Fatalf("append the older comment's comment.created: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the older build's comment: %v", err)
	}
	return id, event
}

func requireTheReleasedDatabaseLosesTheTriggers(t *testing.T) {
	fixture := seedProjectionTriggers(t, "TRGR")
	database, pool := fixture.database, fixture.database.Pool
	fixture.announce(t, "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", refs.TupleKinds())
	requireTheObjectsStanding(t, pool, "before the step")

	lines := projectionTriggerLines(retireAsTheBootDoes(t, pool, false))
	requireLogged(t, lines, "at the first boot", "the boot's retirement deleted its objects")
	requireTheObjectsGone(t, pool, "after the step")
	if again := projectionTriggerLines(retireAsTheBootDoes(t, pool, false)); len(again) != 0 {
		t.Errorf("a second boot over the deleted objects logged %v, want nothing", again)
	}

	// A comment thread written afterwards: with no trigger left, its rows and its tuples are the
	// one Go writer's (inboxwrite.Record).
	root := createThreadComment(t, fixture.handler, fixture.issue, map[string]any{"body": "Ship it after the step?"}, "alice")
	reply := createThreadComment(t, fixture.handler, fixture.issue, map[string]any{"body": "After it.", "reply_to": root.ID}, "bob")
	rootEvent := writeEventOf(t, database, "comment.created", root.ID)
	replyEvent := writeEventOf(t, database, "comment.created", reply.ID)
	thread := graphstore.KindComment + ":" + root.ID
	requireRelationTuples(t, database, graphstore.TupleParticipatesIn, thread,
		fmt.Sprintf("person:alice > %s @%d", thread, rootEvent),
		fmt.Sprintf("person:bob > %s @%d", thread, replyEvent))
	requireRelationTuples(t, database, graphstore.TupleDeliveredTo, graphstore.KindComment+":"+reply.ID,
		fmt.Sprintf("comment:%s > person:alice @%d", reply.ID, replyEvent))
	for login, version := range map[string]int64{"alice": 1, "bob": 0} {
		if row := readThreadRow(t, database, login, root.ID); !row.marked || row.markedVersion != 0 || row.version != version {
			t.Errorf("%s's row of the thread written after the step = %+v, want one the Go writer began, marked at version 0, at version %d",
				login, row, version)
		}
		requireVersionCountsReplies(t, database, login, graphstore.KindComment, root.ID)
	}
	if row := threadRowWrites(t, database, "alice", root.ID); row.lastReply != reply.ID || !row.repliedAt.Equal(reply.CreatedAt) {
		t.Errorf("alice's row names %+v, want bob's reply %s at %s", row, reply.ID, reply.CreatedAt)
	}
}

func requireARollsUnmarkedCommentComesInFirst(t *testing.T) {
	fixture := seedProjectionTriggers(t, "TRGL")
	database, pool := fixture.database, fixture.database.Pool
	fixture.announce(t, "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2", refs.TupleKinds())
	root := createThreadComment(t, fixture.handler, fixture.issue, map[string]any{"body": "Ship it during the roll?"}, "alice")
	rootEvent := writeEventOf(t, database, "comment.created", root.ID)
	older, olderEvent := writeOlderComment(t, database, fixture.issue, root.ID, "bob")
	if unmarked := unmarkedRowsOf(t, pool); !slices.Equal(unmarked, []string{"comments " + older}) {
		t.Fatalf("the rows standing unmarked are %v, want the older build's comment %s alone", unmarked, older)
	}
	if row := readThreadRow(t, database, "bob", root.ID); row.marked {
		t.Fatalf("the older build's comment left bob a marked row %+v, want the unmarked row its trigger writes", row)
	}
	requireTheObjectsStanding(t, pool, "before the step")

	lines := projectionTriggerLines(retireAsTheBootDoes(t, pool, false))
	requireLogged(t, lines, "at the first boot", "the boot's retirement deleted its objects")
	for _, line := range lines {
		if strings.Contains(line, "something still holds them") {
			t.Errorf("the step logged a holder after its backfill: %s", line)
		}
	}
	requireTheObjectsGone(t, pool, "after the step")

	// The backfill brought the older comment in before the drops: the row is marked, its writer
	// takes part in the thread in the store, and their row of it is marked.
	if unmarked := unmarkedRowsOf(t, pool); len(unmarked) != 0 {
		t.Errorf("after the step the rows %v stand unmarked, want the backfill to have marked the older build's comment %s", unmarked, older)
	}
	thread := graphstore.KindComment + ":" + root.ID
	requireRelationTuples(t, database, graphstore.TupleParticipatesIn, thread,
		fmt.Sprintf("person:alice > %s @%d", thread, rootEvent),
		fmt.Sprintf("person:bob > %s @%d", thread, olderEvent))
	// bob's own write moved no replied_at of his, so his row is read by its version alone here.
	before := readThreadRow(t, database, "bob", root.ID)
	if !before.marked {
		t.Errorf("bob's row of thread %s = %+v after the step, want the backfill to have marked it", root.ID, before)
	}

	// The next write into the thread, by this build once the triggers are gone, reaches the older
	// comment's writer and moves their row.
	next := createThreadComment(t, fixture.handler, fixture.issue, map[string]any{"body": "After the roll.", "reply_to": root.ID}, "carol")
	nextEvent := writeEventOf(t, database, "comment.created", next.ID)
	requireRelationTuples(t, database, graphstore.TupleDeliveredTo, graphstore.KindComment+":"+next.ID,
		fmt.Sprintf("comment:%s > person:alice @%d", next.ID, nextEvent),
		fmt.Sprintf("comment:%s > person:bob @%d", next.ID, nextEvent))
	after := threadRowWrites(t, database, "bob", root.ID)
	if after.version != before.version+1 || after.lastReply != next.ID || !after.repliedAt.Equal(next.CreatedAt) {
		t.Errorf("thread %s: bob's row after carol's reply %s = %+v, at version %d before it; want it moved to that reply, one version on",
			root.ID, next.ID, after, before.version)
	}
	for _, login := range []string{"alice", "bob"} {
		requireVersionCountsReplies(t, database, login, graphstore.KindComment, root.ID)
	}
}

func requireTheReleaseBeforeKeepsTheTriggers(t *testing.T) {
	fixture := seedProjectionTriggers(t, "TRGB")
	database, pool := fixture.database, fixture.database.Pool
	const beforeCommit = "b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7"
	before := slices.DeleteFunc(refs.TupleKinds(), func(kind string) bool {
		return slices.Contains(startup.ProjectionTriggersRetirement.Kinds, kind)
	})
	fixture.announce(t, beforeCommit, before)

	for _, boot := range []string{"the first boot", "a second process", "a restart"} {
		lines := projectionTriggerLines(retireAsTheBootDoes(t, pool, false))
		requireLogged(t, lines, "at "+boot, "kept its objects", "announcement_commit="+beforeCommit)
		requireTheObjectsStanding(t, pool, "after "+boot)
	}

	if response := dispatchRequest(t, fixture.handler, http.MethodGet, "/api/v1/issues/"+fixture.issue, nil, "alice"); response.Code != http.StatusOK {
		t.Errorf("GET /api/v1/issues/%s after the kept step = %d %s, want 200", fixture.issue, response.Code, response.Body.String())
	}
	// A build of the release before may still serve, and its write is still projected.
	root := createThreadComment(t, fixture.handler, fixture.issue, map[string]any{"body": "Still served?"}, "alice")
	older, _ := writeOlderComment(t, database, fixture.issue, root.ID, "bob")
	if row := threadRowWrites(t, database, "alice", root.ID); row.lastReply != older {
		t.Errorf("alice's row names %+v after the older build's comment %s, want the trigger to have moved it", row, older)
	}
}

func requireAnEmptyDatabaseLosesTheTriggersAtOnce(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, storetest.Empty(t))
	if err != nil {
		t.Fatalf("open the empty database: %v", err)
	}
	t.Cleanup(database.Pool.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate the empty database: %v", err)
	}
	if !database.CreatedFromEmpty() {
		t.Fatal("the migration run over an empty database read it as one an earlier release served")
	}
	requireTheObjectsStanding(t, database.Pool, "after the migrations")

	lines := projectionTriggerLines(retireAsTheBootDoes(t, database.Pool, database.CreatedFromEmpty()))
	requireLogged(t, lines, "at the first boot", "the boot's retirement deleted its objects", "created_from_empty=true")
	requireTheObjectsGone(t, database.Pool, "after the first boot over an empty database")

	writers, read := storedFunctionsWritingAThread(t, ctx, database.Pool)
	if read == 0 {
		t.Fatal("no stored function was read, so nothing was checked")
	}
	if len(writers) != 0 {
		t.Errorf("after the step the stored functions %v write user_threads or user_agent_thread_dismissed, want none", writers)
	}
	if writes, _ := catalogueProjectionWrites(t, ctx, &projectionRun{database: database}, storedFunctionBodiesSQL); len(writes) != 0 {
		t.Errorf("after the step stored functions write a projected table:\n  %s", strings.Join(writes, "\n  "))
	}
}
