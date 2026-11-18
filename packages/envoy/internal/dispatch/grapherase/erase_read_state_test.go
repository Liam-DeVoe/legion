package grapherase

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/inboxwrite"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// A comment, a message and an ask are each the root of their own thread, and the rows of the six
// read-state tables that name one of them mirror tuples from a person to it (dispatch://INFRA-1152
// R6): the user_threads row of each participant, and for the message an Agent dismissal of it and
// a reply-read row of it, for the ask a snooze. The rows are written as an older build writes
// them, beside another person's rows of another thread, and the backfill marks them and states
// their tuples. The erase of each item lists each of its rows - the snoozes through their required
// reference into asks - deletes them with the item, and leaves the other thread's rows standing;
// the census reads zero after the three erases, the people those rows named held to their nodes
// by the records of the erases (Erasure.Writers, reachedPerson).
func TestTheEraseOfAnItemTakesTheReadStateRowsThatNameIt(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	ask := seedMarkedAsk(t, database)
	var comment, message string
	if err := database.Pool.QueryRow(ctx, `
		insert into comments (issue_key, author, body) values ('CORE-1', '{"kind":"user","id":"carol"}', 'A comment nobody keeps.')
		returning id::text`).Scan(&comment); err != nil {
		t.Fatalf("write the comment: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		insert into messages (issue_key, author, body, target) values ('CORE-1', '{"kind":"user","id":"carol"}', 'A message nobody keeps.', 'session:s1')
		returning id::text`).Scan(&message); err != nil {
		t.Fatalf("write the message: %v", err)
	}
	const other = "00000000-0000-4000-8000-000000004152"
	if _, err := database.Pool.Exec(ctx, `
		with threads as (
			insert into user_threads (login, thread_id, kind, project_key, issue_key, read_through) values
				('bob', $1::uuid, 'comment', 'CORE', 'CORE-1', now()),
				('bob', $2::uuid, 'message', 'CORE', 'CORE-1', now()),
				('bob', $3::uuid, 'ask', 'CORE', 'CORE-1', now()),
				('bob', $4::uuid, 'comment', 'CORE', 'CORE-1', now())
		), dismissed as (
			insert into user_agent_thread_dismissed (login, message_id, dismissed_at) values ('erin', $2::uuid, now()), ('erin', $4::uuid, now())
		), replies as (
			insert into user_agent_reply_read (login, session_id, reply_id) values ('erin', 's1', $2::uuid), ('erin', 's1', $4::uuid)
		)
		insert into user_ask_snooze (login, ask_id, snoozed_until) values ('alice', $3::uuid, now() + interval '1 hour'), ('bob', $3::uuid, now() + interval '1 hour')
	`, comment, message, ask, other); err != nil {
		t.Fatalf("write the read state of the three threads: %v", err)
	}
	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill the store: %v", err)
	}
	requireCensusZero(t, database, "before the erases")

	for _, item := range []struct {
		kind, id string
		listed   [][]string
		naming   string
	}{
		{
			kind: graphstore.KindComment, id: comment,
			listed: [][]string{
				{"delete user_threads (login, thread_id)=(bob," + comment + ")"},
				{"delete user_threads (login, thread_id)=(carol," + comment + ")"},
			},
			naming: `select count(*) from user_threads where thread_id = $1::uuid`,
		},
		{
			kind: graphstore.KindMessage, id: message,
			listed: [][]string{
				{"delete user_threads (login, thread_id)=(bob," + message + ")"},
				{"delete user_threads (login, thread_id)=(carol," + message + ")"},
				{"delete user_agent_thread_dismissed (message_id, login)=(" + message + ",erin)"},
				{"delete user_agent_reply_read (login, reply_id)=(erin," + message + ")"},
			},
			naming: `select (select count(*) from user_threads where thread_id = $1::uuid)
			     + (select count(*) from user_agent_thread_dismissed where message_id = $1::uuid)
			     + (select count(*) from user_agent_reply_read where reply_id = $1::uuid)`,
		},
		{
			kind: graphstore.KindAsk, id: ask,
			listed: [][]string{
				{"delete user_threads (login, thread_id)=(alice," + ask + ")"},
				{"delete user_threads (login, thread_id)=(bob," + ask + ")"},
				{"delete user_ask_snooze: 2 row(s) through user_ask_snooze.ask_id"},
			},
			naming: `select (select count(*) from user_threads where thread_id = $1::uuid)
			     + (select count(*) from user_ask_snooze where ask_id = $1::uuid)`,
		},
	} {
		listing := dryRun(t, database, item.kind, item.id)
		for _, line := range item.listed {
			if !listed(listing, line...) {
				t.Errorf("the erase of %s:%s lists\n\t%s\nwith no line %q", item.kind, item.id, joinedListing(listing), line)
			}
		}
		requireErased(t, database, item.kind, item.id)
		var naming int64
		if err := database.Pool.QueryRow(ctx, item.naming, item.id).Scan(&naming); err != nil {
			t.Fatalf("count the read-state rows of %s:%s: %v", item.kind, item.id, err)
		}
		if naming != 0 {
			t.Errorf("after the erase of %s:%s %d read-state row(s) name it, want none", item.kind, item.id, naming)
		}
	}
	var left int64
	if err := database.Pool.QueryRow(ctx, `
		select (select count(*) from user_threads where login = 'bob' and thread_id = $1::uuid)
		     + (select count(*) from user_agent_thread_dismissed where login = 'erin' and message_id = $1::uuid)
		     + (select count(*) from user_agent_reply_read where login = 'erin' and reply_id = $1::uuid)
	`, other).Scan(&left); err != nil {
		t.Fatalf("count the other thread's rows: %v", err)
	}
	if left != 3 {
		t.Errorf("after the erases %d of the other thread's 3 read-state rows stand, want all of them", left)
	}
	requireCensusZero(t, database, "after the erases")
}

// A person's read state is their rows of the six read-state tables, each keyed on a spelling of
// their login: the canonical login every table but user_agent_state keys on, and the raw actor id
// that table keys a Clear on (api/state.go). The erase of the person lists every one of those rows
// and deletes them in its own transaction, with their node, and leaves another person's rows of
// the same threads, sessions, replies and ask standing; the census reads zero before the erase,
// the marked rows holding the person to her node, and after it, nothing left naming her.
func TestTheEraseOfAPersonTakesTheirReadStateRows(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedPreStoreDocument(t, database)
	const person, spelling = "dana", "Dana"
	thread, root, reply := "00000000-0000-4000-8000-000000005152", "00000000-0000-4000-8000-000000006152", "00000000-0000-4000-8000-000000007152"
	var ask string
	if err := database.Pool.QueryRow(ctx, `
		insert into asks (issue_key, author, question) values ('CORE-1', '{"kind":"user","id":"erin"}', 'Which one?')
		returning id::text`).Scan(&ask); err != nil {
		t.Fatalf("write the ask: %v", err)
	}
	writeAsThisBuild(t, database, "the person's node", func(tx pgx.Tx) (graphstore.Uncaused, error) {
		return graphstore.WriteUncaused(ctx, tx, []graphstore.Node{
			graphstore.PersonNode(person, model.Actor{Kind: "user", ID: person}, time.Now().UTC(), graphstore.NoEvent),
		}, nil)
	})
	if _, err := database.Pool.Exec(ctx, `
		with threads as (
			insert into user_threads (login, thread_id, kind, project_key, issue_key, read_through, marked_version, marked_seq) values
				($1::text, $3::uuid, 'comment', 'CORE', 'CORE-1', now(), 0, 0),
				('erin', $3::uuid, 'comment', 'CORE', 'CORE-1', now(), null, null)
		), dismissed as (
			insert into user_agent_thread_dismissed (login, message_id, dismissed_at, tuples_stored_at) values
				($1::text, $4::uuid, now(), now()), ('erin', $4::uuid, now(), null)
		), cleared as (
			insert into user_agent_state (login, session_id, cleared_before, tuples_stored_at) values
				($2::text, 's1', now(), now()), ('erin', 's1', now(), null)
		), reads as (
			insert into user_agent_read (login, session_id, read_through, tuples_stored_at) values
				($1::text, 's1', now(), now()), ('erin', 's1', now(), null)
		), replies as (
			insert into user_agent_reply_read (login, session_id, reply_id, tuples_stored_at) values
				($1::text, 's1', $5::uuid, now()), ('erin', 's1', $5::uuid, null)
		)
		insert into user_ask_snooze (login, ask_id, snoozed_until, tuples_stored_at) values
			($1::text, $6::uuid, now() + interval '1 hour', now()), ('erin', $6::uuid, now() + interval '1 hour', null)
	`, person, spelling, thread, root, reply, ask); err != nil {
		t.Fatalf("write the read state of %s and erin: %v", person, err)
	}
	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill the store: %v", err)
	}
	requireCensusZero(t, database, "before the erase")

	listing := dryRun(t, database, graphstore.KindPerson, person)
	for _, line := range []string{
		"delete user_threads (login, thread_id)=(" + person + "," + thread + ")",
		"delete user_agent_thread_dismissed (message_id, login)=(" + root + "," + person + ")",
		"delete user_agent_state (login, session_id)=(" + spelling + ",s1)",
		"delete user_agent_read (login, session_id)=(" + person + ",s1)",
		"delete user_agent_reply_read (login, reply_id)=(" + person + "," + reply + ")",
		"delete user_ask_snooze (login, ask_id)=(" + person + "," + ask + ")",
	} {
		if !listed(listing, line) {
			t.Errorf("the erase of person:%s lists\n\t%s\nwith no line %q", person, joinedListing(listing), line)
		}
	}
	// erin's rows are the six written above and the user_threads row her ask's insert gave her
	// (migration 0096's trigger), which the erase of another person leaves as they are.
	count := func() (theirs, erins int64) {
		t.Helper()
		if err := database.Pool.QueryRow(ctx, `
			with spelled (login) as (select unnest($1::text[])),
			rows (login) as (
				select login from user_threads union all select login from user_agent_thread_dismissed
				union all select login from user_agent_state union all select login from user_agent_read
				union all select login from user_agent_reply_read union all select login from user_ask_snooze
			)
			select count(*) filter (where login in (select login from spelled)), count(*) filter (where login = 'erin')
			from rows
		`, []string{person, spelling}).Scan(&theirs, &erins); err != nil {
			t.Fatalf("count the read-state rows: %v", err)
		}
		return theirs, erins
	}
	held, erinsBefore := count()
	if held != 6 || erinsBefore < 6 {
		t.Fatalf("before the erase %s holds %d read-state rows and erin %d, want 6 and at least 6, so this test would prove nothing",
			person, held, erinsBefore)
	}
	requireErased(t, database, graphstore.KindPerson, person)
	if theirs, erins := count(); theirs != 0 || erins != erinsBefore {
		t.Errorf("after the erase of person:%s %d of her read-state rows and %d of erin's %d stand, want none of hers and all of erin's",
			person, theirs, erins, erinsBefore)
	}
	requireCensusZero(t, database, "after the erase")
}

// A reply is no thread of its own, so no read-state row is its to delete, but it is counted and
// named by the user_threads rows of the thread it was written in that it reached (R6). alice's
// comment and bob's reply to it are written as the release before writes them and backfilled, so
// alice's row is marked at version 1 naming bob's reply; then carol replies through this
// release's writer (inboxwrite.Record), which delivers her reply to alice and bob above their
// marks and moves both rows to name it. The erase of carol's reply lists both rows as rewritten,
// and leaves alice's at version 1 naming bob's reply again and bob's at version 0 naming none,
// each still marked, so the census reads zero after it.
func TestTheEraseOfAReplyMendsTheRowsItReached(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seedPreStoreDocument(t, database)
	var root, first string
	var firstAt time.Time
	if err := database.Pool.QueryRow(ctx, `
		insert into comments (issue_key, author, body) values ('CORE-1', '{"kind":"user","id":"alice"}', 'Is the copy final?')
		returning id::text`).Scan(&root); err != nil {
		t.Fatalf("write alice's comment: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		insert into comments (issue_key, author, body, reply_to) values ('CORE-1', '{"kind":"user","id":"bob"}', 'Nearly.', $1::uuid)
		returning id::text, created_at`, root).Scan(&first, &firstAt); err != nil {
		t.Fatalf("write bob's reply: %v", err)
	}
	if _, err := graphcensus.Backfill(ctx, database, io.Discard); err != nil {
		t.Fatalf("backfill the store: %v", err)
	}

	const second = "00000000-0000-4000-8000-000000008152"
	carol := model.Actor{Kind: "user", ID: "carol"}
	writeReplyAsThisBuild(t, database, second, root, carol)
	for login, want := range map[string]int64{"alice": 2, "bob": 1} {
		if row := readRepliedRow(t, database, login, root); row.version != want || !row.marked || row.lastReply == nil || *row.lastReply != second {
			t.Fatalf("before the erase %s's row reads %+v, want version %d, marked, naming carol's reply, so this test would prove nothing", login, row, want)
		}
	}
	requireCensusZero(t, database, "before the erase")

	listing := dryRun(t, database, graphstore.KindComment, second)
	for _, login := range []string{"alice", "bob"} {
		if line := "rewrite user_threads (login, thread_id)=(" + login + "," + root + ")"; !listed(listing, line) {
			t.Errorf("the erase of comment:%s lists\n\t%s\nwith no line %q", second, joinedListing(listing), line)
		}
	}
	requireErased(t, database, graphstore.KindComment, second)
	if row := readRepliedRow(t, database, "alice", root); row.version != 1 || !row.marked || row.lastReply == nil || *row.lastReply != first ||
		row.repliedAt == nil || !row.repliedAt.Equal(firstAt) {
		t.Errorf("after the erase alice's row reads %+v, want version 1, marked, naming bob's reply %s at %s", row, first, firstAt)
	}
	if row := readRepliedRow(t, database, "bob", root); row.version != 0 || !row.marked || row.lastReply != nil || row.repliedAt != nil {
		t.Errorf("after the erase bob's row reads %+v, want version 0, marked, naming no reply", row)
	}
	requireCensusZero(t, database, "after the erase")
}

// writeReplyAsThisBuild writes comment id, by author, replying to root on issue CORE-1, as this
// release's createComment writes one at the sentinel: its node and the facts its row projects
// with its pointers, its row naming the node with tuples_stored, its authored_by, and its thread
// (inboxwrite.Record).
func writeReplyAsThisBuild(t *testing.T, database *store.Store, id, root string, author model.Actor) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the reply: %v", err)
	}
	defer tx.Rollback(ctx)
	var at time.Time
	if err := tx.QueryRow(ctx, `select now()`).Scan(&at); err != nil {
		t.Fatalf("read the reply's moment: %v", err)
	}
	node, tuples := graphstore.CommentFacts(nil, id, author, at, graphstore.NoEvent, "", "", "CORE", nil)
	tuples, _ = graphstore.CommentPointerFacts(tuples, id, graphstore.CommentPointers{ReplyTo: root}, author, at, graphstore.NoEvent)
	written, err := graphstore.WriteUncaused(ctx, tx, []graphstore.Node{node}, tuples)
	if err != nil {
		t.Fatalf("write the reply's node: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into comments (id, issue_key, author, body, reply_to, created_at, node_kind, node_address, tuples_stored)
		values ($1::uuid, 'CORE-1', $2, 'Final.', $3::uuid, $4, 'comment', $1, true)
	`, id, `{"kind":"user","id":"`+author.ID+`"}`, root, at); err != nil {
		t.Fatalf("write the reply's row: %v", err)
	}
	if err := graphstore.Caused(ctx, tx, written, graphstore.NoEvent); err != nil {
		t.Fatalf("state the reply: %v", err)
	}
	if err := graphstore.Authored(ctx, tx, graphstore.KindComment, id, author, at, graphstore.NoEvent); err != nil {
		t.Fatalf("state who wrote the reply: %v", err)
	}
	issue := "CORE-1"
	if err := inboxwrite.Record(ctx, tx, inboxwrite.Comment(id, author, at, &issue, nil, "CORE"), graphstore.NoEvent); err != nil {
		t.Fatalf("record the reply's thread: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the reply: %v", err)
	}
}

// repliedThreadRow is what one user_threads row says of the replies it counted: its version,
// whether it is marked, and the reply it names.
type repliedThreadRow struct {
	version   int64
	marked    bool
	lastReply *string
	repliedAt *time.Time
}

func readRepliedRow(t *testing.T, database *store.Store, login, thread string) repliedThreadRow {
	t.Helper()
	var row repliedThreadRow
	if err := database.Pool.QueryRow(context.Background(), `
		select version, marked_seq is not null, last_reply_id::text, replied_at from user_threads where login = $1 and thread_id = $2::uuid
	`, login, thread).Scan(&row.version, &row.marked, &row.lastReply, &row.repliedAt); err != nil {
		t.Fatalf("read %s's row of thread %s: %v", login, thread, err)
	}
	return row
}

// joinedListing is an erasure's listing as one block of text, for a failure to print.
func joinedListing(erasure *Erasure) string {
	return strings.Join(erasure.Listing, "\n\t")
}
