package storesmove

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// lockNotAvailable is the SQLSTATE Postgres cancels a statement with once its lock_timeout runs
// out.
const lockNotAvailable = "55P03"

// swapLocks are the locks the swap takes before it reads any row, in this order. graph_nodes in
// share row exclusive mode stops each direct write of a node row, the release before's included,
// and lets each read and each foreign-key check through. The four old tables in access exclusive
// mode wait for each open transaction on them, so no write of the release before lands between the
// copy and the drop. These locks, their order and the half second each wait is bounded by
// (swapLockTimeout) are those of the second migration of spec version 113
// (dispatch://INFRA-1098/spec#b-r-stores-moved@v113), whose text states, with a trial on
// PostgreSQL 18.6, why they keep the release before's create, patch and read shapes of an issue
// out of a deadlock with the swap.
var swapLocks = []struct{ table, mode string }{
	{"graph_nodes", "share row exclusive"},
	{"priority_nodes", "access exclusive"},
	{"priority_tuples", "access exclusive"},
	{"workflow_nodes", "access exclusive"},
	{"workflow_tuples", "access exclusive"},
}

// lockWait is an attempt of the swap that gave up waiting for a lock (SQLSTATE 55P03): lock names
// what it waited for.
type lockWait struct {
	lock string
	err  error
}

func (w *lockWait) Error() string {
	return "the swap gave up waiting for " + w.lock + ": " + w.err.Error()
}
func (w *lockWait) Unwrap() error { return w.err }

// waited answers err as a *lockWait for what, where err is Postgres giving up a lock wait, and as
// it is otherwise.
func waited(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return &lockWait{lock: what, err: err}
	}
	return err
}

// swapWithRetries runs the swap (swap) until it swaps, finds the stores moved, or fails with
// anything but a lock wait it gave up, for up to m.swapAttempts attempts, pausing SwapPause,
// holding nothing, between two. It logs one line for each attempt that gave up, naming the lock
// it waited for. An attempt that gives up has rolled back, so it changed nothing, and the reads and
// writes queued behind its locks run during the pause.
func (m mover) swapWithRetries(ctx context.Context, conn *pgx.Conn, result *Result) error {
	for attempt := 1; ; attempt++ {
		result.SwapAttempts = attempt
		err := m.swap(ctx, conn, result)
		var gaveUp *lockWait
		if !errors.As(err, &gaveUp) {
			return err
		}
		if attempt >= m.swapAttempts {
			slog.Warn("dispatch: an attempt of the stores' swap gave up waiting for a lock, its last; the boot fails",
				"lock", gaveUp.lock, "attempt", attempt, "attempts", m.swapAttempts)
			return fmt.Errorf("the stores' swap: after %d attempts: %w", attempt, err)
		}
		slog.Warn("dispatch: an attempt of the stores' swap gave up waiting for a lock; the boot tries it again",
			"lock", gaveUp.lock, "attempt", attempt, "attempts", m.swapAttempts, "pause", SwapPause.String())
		select {
		case <-ctx.Done():
			return fmt.Errorf("the stores' swap: after %d attempts: %w", attempt, ctx.Err())
		case <-time.After(SwapPause):
		}
	}
}

// swap is one attempt of the swap, one transaction under `set local lock_timeout = '500ms'`. It
// takes swapLocks before it reads any row, then reads pg_class again and, where priority_nodes is no
// table any more, changes nothing (result.Census stays nil). Otherwise it draws the graph's
// sequence past the greatest seq of priority_tuples, copies every tuple of the old tables that the
// record does not hold, in the order of its seq, with the node rows they name, as the copy does
// (copyAll), and takes the census before the drop (takeCensus). Where the census reads a
// difference it rolls back and answers a *DifferenceError. Where it reads none it drops the record
// and the four tables, creates the four views of the same names and columns over graph_nodes and
// graph_tuples, then the two triggers, and commits. A lock wait that gives up answers a *lockWait.
func (m mover) swap(ctx context.Context, conn *pgx.Conn, result *Result) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("the stores' swap: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `set local lock_timeout = '`+strconv.FormatInt(swapLockTimeout.Milliseconds(), 10)+`ms'`); err != nil {
		return fmt.Errorf("the stores' swap: bound its lock waits: %w", err)
	}
	for _, lock := range swapLocks {
		if _, err := tx.Exec(ctx, `lock table `+lock.table+` in `+lock.mode+` mode`); err != nil {
			return waited(err, lock.table+" in "+lock.mode+" mode")
		}
	}
	relkind, err := priorityNodesKind(ctx, tx)
	if err != nil {
		return err
	}
	if relkind != tableKind {
		return nil
	}
	copied, err := m.copyAll(ctx, tx)
	if err != nil {
		return waited(err, "a lock its copy of what the record lacks met")
	}
	census, err := takeCensus(ctx, tx, nil)
	if err != nil {
		return waited(err, "a lock its census met")
	}
	if census.Differs() {
		return &DifferenceError{Census: census}
	}
	if _, err := tx.Exec(ctx, dropOldTables+storesViews+graphstore.StoresMovedTriggers); err != nil {
		return waited(err, "a lock its drop of the old tables and its views and triggers met")
	}
	if err := tx.Commit(ctx); err != nil {
		return waited(err, "a lock its commit met")
	}
	result.SwapCopied, result.Census = copied, census
	return nil
}

// dropOldTables drops the move's record and the four tables, in the order the swap locked them.
const dropOldTables = `
drop table stores_moved_seqs;
drop table priority_nodes, priority_tuples, workflow_nodes, workflow_tuples;
`

// storesViews are the four names of the release before as views over the graph store, with the
// columns and the column order of the tables they replace (20261112024320_priority_nodes.up.sql,
// 20261116235202_workflow_nodes.up.sql), so the release before still reads and writes them during a
// roll and after a rollback. Each selects from one table and holds no with, distinct, group by,
// having, limit, offset, set operation, aggregate or set-returning function, so PostgreSQL updates
// each itself (CREATE VIEW, "Updatable Views",
// https://www.postgresql.org/docs/18/sql-createview.html), an insert's `on conflict (kind, id)` of
// a node view among them, whose key graph_nodes_pkey holds and whose kind the view selects. A
// column a write through a view leaves out takes the default of graph_nodes' or graph_tuples' own
// column, a tuple's seq from the graph's sequence among them. priority_tuples holds the tuples out
// of a priority and the advances tuples, which the release before stated on the issue.
const storesViews = `
-- kept for one release: priority-nodes-view
create view priority_nodes as
  select kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event
  from graph_nodes
  where kind = 'priority';
-- kept for one release: priority-tuples-view
create view priority_tuples as
  select seq, from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted
  from graph_tuples
  where from_kind = 'priority' or kind = 'advances';
-- kept for one release: workflow-nodes-view
create view workflow_nodes as
  select kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event
  from graph_nodes
  where kind in ('step', 'plan');
-- kept for one release: workflow-tuples-view
create view workflow_tuples as
  select seq, from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted
  from graph_tuples
  where from_kind in ('step', 'plan');
`
