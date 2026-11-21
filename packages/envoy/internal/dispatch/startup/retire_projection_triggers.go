package startup

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphcensus"
	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

var _ = registerRetirement(ProjectionTriggersRetirement)

// projectionTriggersName is ProjectionTriggersRetirement's name, which its backfill's log lines
// carry as the step's own lines do.
const projectionTriggersName = "the inbox's projection triggers and their functions"

// ProjectionTriggersRetirement deletes the two stored functions that the inbox release kept for
// one release to write a projection - user_threads_record (migrations 0095 and 20261105093501),
// which writes user_threads, and user_agent_thread_dismissed_clear (20261105093507), which deletes
// user_agent_thread_dismissed rows - with every trigger that reaches them and the three functions
// those triggers run. The triggers are user_threads_comment on comments (0095), user_threads_ask on
// asks (0096) and user_threads_message on messages (0097), each running the function of its name,
// which calls user_threads_record, and user_agent_thread_dismissed_clear on messages. That release
// made inboxwrite.Record the one writer of a thread's user_threads rows and of its Agent
// dismissals, and kept these for a write that a build of the release before it makes during a roll
// or after a rollback: each returns at once for a row that release marked and projects any other
// (migration 20261116160001). The function user_threads_login stays, because this build's reads
// of a person's replies and answers and the census call it.
//
// It is exported so a test reads its kinds and its name.
//
// Its kinds are the relation kinds that release moved into the store: a comment's or a message's
// replies_to and answers, and part_of. A build that lists them is that release or a later one,
// which marks every row it writes that the store can hold.
//
// It deletes only where no row of comments, messages or asks stands unmarked (unmarkedRows):
// each function still writes for exactly those rows, so each is a holder. Before it reads them it
// brings in the rows a build of an earlier release wrote, through the backfill that release built
// (graphcensus.Backfill, which the command backfill-graph-store runs), with that backfill's own
// batches and commits: that writes each row's facts in the store and marks the row, so the one
// writer reads the row's thread from the store once the triggers are gone. A row the backfill
// cannot take stays unmarked and so stays a holder, which the step logs: a row past the store's
// address bound, which no writer states in the store, and a row whose node the store holds in
// another project. The backfill's own failure is logged and decides nothing: the rows it did not
// mark are the holders. The backfill runs on the shared pool, before the deleting transaction,
// because it commits batch by batch; it runs only where a row stands unmarked, so a boot over
// marked rows reads the three tables and writes nothing. The step reads Holds on the pool before
// it applies its guard, so the backfill also runs at a boot where the guard keeps the objects:
// that backfill is built to run at any time beside a build of an earlier release, and it reads no
// marked row.
//
// The first guard keeps out a build of a release before the inbox release, the one build that
// writes a row with no marker that the store could hold, so no such row comes to stand between
// the read in the deleting transaction and the drops.
//
// It deletes in the order each drop's dependants allow: the four triggers, then the three
// functions they run, then user_threads_record and user_agent_thread_dismissed_clear. Dropping
// user_threads_record while a trigger reaching it stands would fail every insert into comments,
// messages and asks. Dropping a trigger takes its table's ACCESS EXCLUSIVE lock, which waits for
// every transaction that has read or written comments, asks or messages to end, and holds every
// later one off until this transaction commits, bounded by the step's lock timeout. Each drop is
// `if exists`, so a database where some of the objects went by other hands loses the rest at the
// next boot rather than failing at every boot. A trigger of another name that runs one of the
// functions makes its function's drop fail, and the whole transaction with it.
var ProjectionTriggersRetirement = Retirement{
	Name:  projectionTriggersName,
	Kinds: []string{graphstore.TupleRepliesTo, graphstore.TupleAnswers, graphstore.TuplePartOf},
	// A trigger cannot stand without the function it runs, so the five functions answer for the
	// four triggers.
	Stands: func(ctx context.Context, q queryer) (bool, error) {
		var stands bool
		err := q.QueryRow(ctx, `
			select to_regprocedure('user_threads_record(uuid, text, text, text, timestamptz, uuid, boolean, text, uuid)') is not null
			    or to_regprocedure('user_agent_thread_dismissed_clear()') is not null
			    or to_regprocedure('user_threads_comment()') is not null
			    or to_regprocedure('user_threads_ask()') is not null
			    or to_regprocedure('user_threads_message()') is not null`).Scan(&stands)
		return stands, err
	},
	Holds: func(ctx context.Context, q queryer) ([]Holder, error) {
		if pool, onPool := q.(*store.Pool); onPool {
			if err := backfillUnmarkedRows(ctx, pool); err != nil {
				return nil, err
			}
		}
		return unmarkedRows(ctx, q)
	},
	Delete: func(ctx context.Context, tx pgx.Tx) error {
		for _, statement := range []string{
			`drop trigger if exists user_threads_comment on comments`,
			`drop trigger if exists user_threads_ask on asks`,
			`drop trigger if exists user_threads_message on messages`,
			`drop trigger if exists user_agent_thread_dismissed_clear on messages`,
			`drop function if exists user_threads_comment()`,
			`drop function if exists user_threads_ask()`,
			`drop function if exists user_threads_message()`,
			`drop function if exists user_threads_record(uuid, text, text, text, timestamptz, uuid, boolean, text, uuid)`,
			`drop function if exists user_agent_thread_dismissed_clear()`,
		} {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		return nil
	},
}

// The marker tests the triggers read, each the test of a row the inbox release did not mark: a
// comment whose writer stated no pointer in the store (comments.tuples_stored), a message naming no
// node (its node pair), and an ask with no marker, or holding an answer whose moment its marker
// does not hold (asks.tuples_stored_at), which is an answer a build of an earlier release wrote.
const (
	unmarkedComment = `not tuples_stored`
	unmarkedMessage = `node_kind is null or node_address is null`
	unmarkedAsk     = `tuples_stored_at is null
		or (answer->>'user' is not null and tuples_stored_at is distinct from (answer->>'at')::timestamptz)`
)

// unmarkedRowsShown is how many unmarked rows of each table the step names in its log at one boot.
// Every row past the store's address bound stays unmarked for good, so a database can hold many,
// and one line for each would repeat them all at every boot.
const unmarkedRowsShown = 20

// backfillUnmarkedRows runs the backfill over pool where a row of comments, messages or asks stands
// unmarked, and logs what each of its passes wrote, and its failure. It answers an error only
// where it cannot read whether a row stands unmarked.
func backfillUnmarkedRows(ctx context.Context, pool *store.Pool) error {
	var unmarked bool
	if err := pool.QueryRow(ctx, `
		select exists (select 1 from comments where `+unmarkedComment+`)
		    or exists (select 1 from messages where `+unmarkedMessage+`)
		    or exists (select 1 from asks where `+unmarkedAsk+`)`).Scan(&unmarked); err != nil {
		return fmt.Errorf("read whether a row stands unmarked: %w", err)
	}
	if !unmarked {
		return nil
	}
	report, err := graphcensus.Backfill(ctx, &store.Store{Pool: pool}, io.Discard)
	for _, done := range report.Kinds {
		if done.Rows == 0 && done.Nodes == 0 && done.Tuples == 0 && done.Skipped == 0 {
			continue
		}
		slog.InfoContext(ctx, "dispatch: the boot's retirement backfilled the rows an earlier release wrote",
			"retirement", projectionTriggersName, "kind", done.Kind, "rows", done.Rows, "nodes", done.Nodes,
			"tuples", done.Tuples, "skipped", done.Skipped, "remaining", done.Remaining)
	}
	if err != nil {
		slog.WarnContext(ctx, "dispatch: the boot's retirement's backfill of the rows an earlier release wrote failed; each row it left unmarked holds the objects",
			"retirement", projectionTriggersName, "error", err)
	}
	return nil
}

// unmarkedRows is a holder for each row of comments, messages and asks that stands unmarked, at
// most unmarkedRowsShown of each table, by id.
func unmarkedRows(ctx context.Context, q queryer) ([]Holder, error) {
	rows, err := q.Query(ctx, `
		(select 'comments', id::text from comments where `+unmarkedComment+` order by id limit $1)
		union all
		(select 'messages', id::text from messages where `+unmarkedMessage+` order by id limit $1)
		union all
		(select 'asks', id::text from asks where `+unmarkedAsk+` order by id limit $1)`, unmarkedRowsShown)
	if err != nil {
		return nil, fmt.Errorf("read the rows that stand unmarked: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Holder, error) {
		var table, id string
		if err := row.Scan(&table, &id); err != nil {
			return nil, err
		}
		return Holder{slog.String("unmarked_table", table), slog.String("unmarked_row", id)}, nil
	})
}
