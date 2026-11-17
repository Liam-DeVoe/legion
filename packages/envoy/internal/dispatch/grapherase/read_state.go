package grapherase

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// readStateTable is one of the six tables a person's read state is projected onto
// (dispatch://INFRA-1152 R6): each row mirrors tuples from the person's node to what the mark is
// about, and so derives from both ends. Key is the table's primary key, the order the erase takes
// a node's rows of it in (lockReadStateRows) and how the listing names each. Items names, by the
// kind of an erased item, the predicate over $1, the item's id, that picks the rows naming it; a
// kind it does not name has no row of this table, and an erased person's rows are picked by login
// in every table (readStateScope).
type readStateTable struct {
	Table, Key string
	Items      map[string]string
}

// readStateTables are the six, in the order their writers take them (api/inbox_read.go's
// markInboxReadThreads writes it down): user_threads rows (step 3), then the Agent dismissals
// (step 4), then user_agent_state, user_agent_read and the reply-read rows in putUserAgentState's
// order, the reply-read rows being step 5, and the snoozes last, whose writer takes nothing else.
// Within a table the rows go in key order: for one person that is ascending thread_id, message_id
// or reply_id, the order the mark routes take one login's rows in, and for one item ascending
// login, the order inboxwrite.Record takes a thread's rows in.
//
// An erased item's thread is its own root, so the user_threads rows whose thread is the item are
// its, and so are the message's Agent dismissals, which name it as the root of an Agent thread, and
// the reply-read rows that name it as the reply read. A snooze of an erased ask is no row of this
// list: user_ask_snooze's ask_id is a required reference into asks, so the erase deletes it with
// the asks row (apply's walk of every required reference into a projected table) and lists it
// there.
var readStateTables = []readStateTable{
	{Table: "user_threads", Key: "login, thread_id", Items: map[string]string{
		graphstore.KindComment: `thread_id = $1::uuid and kind = 'comment'`,
		graphstore.KindAsk:     `thread_id = $1::uuid and kind = 'ask'`,
		graphstore.KindMessage: `thread_id = $1::uuid and kind = 'message'`,
	}},
	{Table: "user_agent_thread_dismissed", Key: "message_id, login", Items: map[string]string{
		graphstore.KindMessage: `message_id = $1::uuid`,
	}},
	{Table: "user_agent_state", Key: "login, session_id"},
	{Table: "user_agent_read", Key: "login, session_id"},
	{Table: "user_agent_reply_read", Key: "login, reply_id", Items: map[string]string{
		graphstore.KindMessage: `reply_id = $1::uuid`,
	}},
	{Table: "user_ask_snooze", Key: "login, ask_id"},
}

// readStateScope is which rows of the six tables derive from the erased node. A person's are every
// row keyed on one of their spellings: the canonical login their node is named by, each raw
// actor id a record stored for them (the recorded_as values of their node, graphstore's aliases),
// and any other spelling that canonicalises to their login (user_threads_login, migration 0095),
// since user_agent_state keys its Clear on the raw actor id the route was given (api/state.go)
// and the census counts every row by its canonical login. An item's are the rows that name it
// (readStateTables' Items). No other node has any.
type readStateScope struct {
	kind, id  string
	spellings []string
}

// readStateScope reads the erased node's scope over the read-state tables. A person's spellings
// are read off the store's tuples, so a confirmed run reads them once it holds the node's row.
func (e *Erasure) readStateScope(ctx context.Context, tx pgx.Tx) (readStateScope, error) {
	scope := readStateScope{kind: e.Kind, id: e.ID}
	if e.Kind != graphstore.KindPerson {
		return scope, nil
	}
	rows, err := tx.Query(ctx, `
		select $2::text
		union
		select to_id from graph_tuples
		where from_kind = $1 and from_id = $2 and kind = $3 and to_kind = $4
		order by 1`, graphstore.KindPerson, e.ID, graphstore.TupleRecordedAs, graphstore.KindValue)
	if err != nil {
		return readStateScope{}, fmt.Errorf("read the spellings of %s:%s: %w", e.Kind, e.ID, err)
	}
	if scope.spellings, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return readStateScope{}, fmt.Errorf("read the spellings of %s:%s: %w", e.Kind, e.ID, err)
	}
	return scope, nil
}

// where is the predicate that picks the scope's rows of table, with its arguments, and false where
// the node has no row of that table.
func (s readStateScope) where(table readStateTable) (string, []any, bool) {
	if s.kind == graphstore.KindPerson {
		return `(login = any($1::text[]) or user_threads_login(login) = $2)`, []any{s.spellings, s.id}, true
	}
	predicate, ok := table.Items[s.kind]
	return predicate, []any{s.id}, ok
}

// readStateRows are the rows of one read-state table the erase lists and deletes, each by its key
// as row(Key)::text spells it.
type readStateRows struct {
	table readStateTable
	keys  []string
}

// lockReadStateRows takes every read-state row of the node `for update`, the level the delete in
// apply takes, table by table in readStateTables' order and by key within each. A confirmed run
// takes them right after the node's projection rows: an item's row (its comments, asks or messages
// row, which for an Agent thread's root message is step 2 of markInboxReadThreads' order) comes
// before the rows of its thread, as every writer of those rows takes the thread's owner or its root
// first, and an erased person, whom no owner row stands over, takes their node's row and then their
// rows. A writer that takes a person's rows and then writes that person's node (inboxwrite.Record
// through graphstore.ToPerson, a mark route through graphstore.FromPerson) writes it `do nothing`,
// which waits on no lock of a row already there, and the erase deletes the node only after it holds
// every one of these rows.
func lockReadStateRows(ctx context.Context, tx pgx.Tx, scope readStateScope) error {
	for _, table := range readStateTables {
		where, args, ok := scope.where(table)
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `select 1 from `+table.Table+` where `+where+` order by `+table.Key+` for update`,
			args...); err != nil {
			return fmt.Errorf("lock the %s rows of %s:%s: %w", table.Table, scope.kind, scope.id, err)
		}
	}
	return nil
}

// readReadState lists every read-state row of the node by its key, so the run without --confirm
// says what it deletes, and holds the keys for apply, which deletes those rows and no others.
func (e *Erasure) readReadState(ctx context.Context, tx pgx.Tx, read *erasedRead) error {
	var err error
	if read.readStateScope, err = e.readStateScope(ctx, tx); err != nil {
		return err
	}
	for _, table := range readStateTables {
		where, args, ok := read.readStateScope.where(table)
		if !ok {
			continue
		}
		rows, err := tx.Query(ctx, `select row(`+table.Key+`)::text from `+table.Table+` where `+where+
			` order by `+table.Key, args...)
		if err != nil {
			return fmt.Errorf("read the %s rows of %s:%s: %w", table.Table, e.Kind, e.ID, err)
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read the %s rows of %s:%s: %w", table.Table, e.Kind, e.ID, err)
		}
		if len(keys) == 0 {
			continue
		}
		for _, key := range keys {
			read.listing.rows = append(read.listing.rows, fmt.Sprintf("delete %s (%s)=%s", table.Table, table.Key, key))
		}
		read.readState = append(read.readState, readStateRows{table: table, keys: keys})
	}
	return nil
}

// deleteReadState deletes the read-state rows the listing named, by their keys, under the
// predicate that found them, so a row written since the listing was read is no row it deletes.
func (e *Erasure) deleteReadState(ctx context.Context, tx pgx.Tx, read erasedRead) error {
	for _, held := range read.readState {
		where, args, ok := read.readStateScope.where(held.table)
		if !ok {
			continue
		}
		tag, err := tx.Exec(ctx, `delete from `+held.table.Table+` where `+where+
			fmt.Sprintf(` and row(%s)::text = any($%d::text[])`, held.table.Key, len(args)+1), append(args, held.keys)...)
		if err != nil {
			return fmt.Errorf("delete the %s rows of %s:%s: %w", held.table.Table, e.Kind, e.ID, err)
		}
		e.removed(held.table.Table, tag.RowsAffected())
	}
	return nil
}
