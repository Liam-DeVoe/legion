package inboxwrite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// thread is the root of the thread a write is in, as the store addresses its node: an ask, a
// comment or a message. Its kind is the kind a user_threads row of the thread names.
type thread struct{ kind, id string }

// maxThreadDepth bounds the walk up a message thread, as the trigger user_threads_message bounds
// its walk up in_reply_to (0095): sixty-four steps, where production threads are seven deep at
// most.
const maxThreadDepth = 64

// threadOf is the root of the thread w is written in (decision 12), read from the write's head
// pointer tuples rather than from a projection's columns, as the triggers of 0095-0097 read it from
// the row. An ask is its own thread. A comment's thread is one step away, as user_threads_comment
// takes it (coalesce(ask_id, reply_to, id)): the ask its replies_to names, else the comment its
// replies_to names, which is the root of its thread (api's threadHeadOf stores every reply against
// its head), else the comment itself. comments.reply_to carries no acyclicity constraint, so the
// step is never repeated. A message's replies_to names the message it answers, and the walk climbs
// those to the root its in_reply_to chain reaches within maxThreadDepth steps, as
// user_threads_message walks it. A message on the way whose row names no node - an older build's,
// which states no tuple - is climbed by its in_reply_to, which is the old arm answering a row the
// marker test passes until the backfill gives it its tuples.
func threadOf(ctx context.Context, tx pgx.Tx, w Write) (thread, error) {
	root := thread{kind: w.NodeKind, id: w.NodeID}
	switch w.NodeKind {
	case graphstore.KindAsk:
		return root, nil
	case graphstore.KindComment:
		err := tx.QueryRow(ctx, `
			select t.to_kind, t.to_id
			from graph_tuples t
			where t.from_kind = $1 and t.from_id = $2 and t.kind = $3 and t.to_kind = any($4::text[]) and t.asserted
			order by t.to_kind = $5 desc, t.seq desc
			limit 1
		`, w.NodeKind, w.NodeID, graphstore.TupleRepliesTo, []string{graphstore.KindAsk, graphstore.KindComment},
			graphstore.KindAsk).Scan(&root.kind, &root.id)
		if errors.Is(err, pgx.ErrNoRows) {
			return root, nil
		}
		if err != nil {
			return thread{}, fmt.Errorf("read the thread comment %s is written in: %w", w.NodeID, err)
		}
		return root, nil
	}
	if err := tx.QueryRow(ctx, `
		with recursive walk(id, depth) as (
			select $1::text, 0
			union all
			select up.id, walk.depth + 1
			from walk
			cross join lateral (
				select coalesce(stored.id, older.id) as id
				from (values (1)) as one (row)
				left join lateral (
					select t.to_id as id
					from graph_tuples t
					where t.from_kind = $2 and t.from_id = walk.id and t.kind = $3 and t.to_kind = $2 and t.asserted
					order by t.seq desc
					limit 1
				) stored on true
				left join lateral (
					select m.in_reply_to::text as id
					from messages m
					where m.id = walk.id::uuid and m.node_kind is null
				) older on true
			) up
			where walk.depth < $4 and up.id is not null
		)
		select id from walk order by depth desc limit 1
	`, w.NodeID, graphstore.KindMessage, graphstore.TupleRepliesTo, maxThreadDepth).Scan(&root.id); err != nil {
		return thread{}, fmt.Errorf("read the thread message %s is written in: %w", w.NodeID, err)
	}
	return root, nil
}

// member is one person a thread holds: a row of user_threads it locked, a participant, or a
// person whose dismissal of it stands, or several of those.
type member struct {
	login string
	// row is whether the person holds a user_threads row of the thread, and marked whether that
	// row is marked (marked_seq, migration 20261116160000).
	row, marked bool
	// takesPart is whether a participates_in from the person into the thread stands.
	takesPart bool
	// dismissed is whether a dismissed tuple from the person into the thread stands.
	dismissed bool
}

// lockThread is step 2 of Record, in one statement: every user_threads row of the thread, locked
// `for no key update` in ascending login order in one pass, the one order every writer of a
// thread's rows and the backfill take them in, and, beside them, the people whose participates_in
// and whose dismissed into the thread's root stand now, each read the way CurrentOut reads a
// relation (CurrentTupleRead), so a retraction takes a person out. A write into the thread by
// anyone else waits at the owner row its writer took first, so nothing moves the thread between
// the statement's snapshot and its locks. The pass takes every row of the thread, which holds the
// row of each person the write's catch-up delivers to (owedBy) as well as every participant's,
// since each of those is a row of the thread. A session or nobody keys no row, and neither does a
// thread whose owner resolves to no project, so such a thread locks nothing and still answers its
// participants. A row's marker is read to tell a marked row from one the marker test passes, and
// no other column of a marked row is read.
func lockThread(ctx context.Context, tx pgx.Tx, thread thread) ([]member, error) {
	current := func(kind string) string {
		return graphstore.CurrentTupleRead(`t.from_id`, `t.from_id, t.asserted`,
			`t.to_kind = $2 and t.to_id = $3 and t.kind = `+kind+` and t.from_kind = $6`)
	}
	rows, err := tx.Query(ctx, `
		with held as materialized (
			select u.login, u.marked_seq is not null as marked
			from user_threads u
			where u.thread_id = $1::uuid
			order by u.login
			for no key update
		), taking_part as (
			select current.from_id as login from (`+current("$4")+`) current where current.asserted
		), standing_dismissals as (
			select current.from_id as login from (`+current("$5")+`) current where current.asserted
		)
		select login, bool_or(row_held), bool_or(marked), bool_or(takes_part), bool_or(was_dismissed)
		from (
			select login, true, marked, false, false from held
			union all
			select login, false, false, true, false from taking_part
			union all
			select login, false, false, false, true from standing_dismissals
		) as fact (login, row_held, marked, takes_part, was_dismissed)
		group by login
		order by login
	`, thread.id, thread.kind, thread.id, graphstore.TupleParticipatesIn, graphstore.TupleDismissed, graphstore.KindPerson)
	if err != nil {
		return nil, fmt.Errorf("lock the rows of thread %s/%s: %w", thread.kind, thread.id, err)
	}
	defer rows.Close()
	var members []member
	for rows.Next() {
		var held member
		if err := rows.Scan(&held.login, &held.row, &held.marked, &held.takesPart, &held.dismissed); err != nil {
			return nil, fmt.Errorf("read a member of thread %s/%s: %w", thread.kind, thread.id, err)
		}
		members = append(members, held)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lock the rows of thread %s/%s: %w", thread.kind, thread.id, err)
	}
	return members, nil
}

// moveRows is step 4 of Record over the rows logins name, every one of which lockThread took: a
// write by somebody else, so each row is replied to - replied_at and last_reply_id name the newer
// of the write and what the row named - its dismissal ends, and its version counts the write. The
// statement is user_threads_record's own (20261105093501), so a row moves alike whichever of the
// two writers moved it.
func moveRows(ctx context.Context, tx pgx.Tx, thread thread, w Write, logins []string) error {
	if len(logins) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		update user_threads
		set replied_at = greatest(replied_at, $3),
		    last_reply_id = case when replied_at is null or $3 >= replied_at then $4::uuid else last_reply_id end,
		    dismissed_at = null,
		    version = version + 1
		where thread_id = $1::uuid and login = any($2::text[])
	`, thread.id, logins, w.At, w.NodeID); err != nil {
		return fmt.Errorf("move the rows of thread %s/%s: %w", thread.kind, thread.id, err)
	}
	return nil
}

// keepRow is step 5 of Record: the row of login, w's writer, on the thread, its read mark moved to
// the write's moment where it stood earlier, and it answers the read mark the row stands at once
// the upsert ran, which is the moment Record's read_through tuple states. A row this write begins
// is marked from its insert: marked_version 0, since no write by somebody else has reached it, and
// marked_seq the newest position of the log this transaction sees, which is above every tuple an
// earlier write into the thread appended - each of those committed before this write took the
// owner row - and below every one a later write appends. A row that was there keeps the marker it
// holds, or holds none, which is a row the backfill takes; as the trigger's own insert, it touches
// neither version nor the dismissal.
func keepRow(ctx context.Context, tx pgx.Tx, thread thread, w Write, login string) (time.Time, error) {
	var readThrough time.Time
	if err := tx.QueryRow(ctx, `
		insert into user_threads (login, thread_id, kind, project_key, issue_key, artifact_id, read_through, marked_version, marked_seq)
		values ($1, $2::uuid, $3, $4, $5, case when $5::text is null then $6::uuid end, $7, 0,
		        (select coalesce(max(seq), 0) from graph_tuples))
		on conflict (login, thread_id) do update
			set read_through = greatest(user_threads.read_through, excluded.read_through)
		returning read_through
	`, login, thread.id, thread.kind, w.Project, w.IssueKey, w.ArtifactID, w.At).Scan(&readThrough); err != nil {
		return time.Time{}, fmt.Errorf("keep the row of %s on thread %s/%s: %w", login, thread.kind, thread.id, err)
	}
	return readThrough, nil
}

// clearAgentDismissals takes the Agent dismissals of root, the message a session's reply climbed
// to, and answers the login of each person whose dismissal it ended. It is the clear of migration
// 20261105093507 in two statements, as that trigger takes it: the root `for key share`, which
// orders the clear against the dismiss route's `for update` of the same root
// (inboxDismissLockAgentRoots, api/inbox_dismiss.go) whichever takes it first, then the delete,
// whose statement starts once the lock is held and so sees a mark that route committed.
func clearAgentDismissals(ctx context.Context, tx pgx.Tx, root string) ([]string, error) {
	if _, err := tx.Exec(ctx, `select 1 from messages where id = $1::uuid for key share`, root); err != nil {
		return nil, fmt.Errorf("take the root message %s: %w", root, err)
	}
	rows, err := tx.Query(ctx, `delete from user_agent_thread_dismissed where message_id = $1::uuid returning login`, root)
	if err != nil {
		return nil, fmt.Errorf("clear the dismissals of message %s: %w", root, err)
	}
	defer rows.Close()
	var cleared []string
	for rows.Next() {
		var login string
		if err := rows.Scan(&login); err != nil {
			return nil, fmt.Errorf("read a dismissal of message %s: %w", root, err)
		}
		cleared = append(cleared, login)
	}
	return cleared, rows.Err()
}
