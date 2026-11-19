package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// WriteBatch is a transaction's write phase as its writers queue it: every write statement, in
// the order the transaction made its writes, sent to Postgres as one pipelined batch (Send). The
// server runs the statements back to back and the client reads their results only once all of
// them have run, so no Go code of this process - no scheduling delay of the goroutine that sent
// them - comes between two of them while the transaction holds its transaction id.
//
// A statement's arguments are therefore known before the batch is sent. A value a later statement
// needs from an earlier one - an appended event's sequence or id, a row's timestamp - is derived in
// the later statement's SQL from what the earlier one left (the owner row's last_seq, the events
// id sequence's currval), never read back in Go between them. A value the caller needs once the
// batch has run - an event's id, a returned timestamp - is read by the statement's callback (Exec,
// QueryRow, Query), which runs as Send reads that statement's result, in statement order.
//
// A batch is built for the context its transaction serves (NewWriteBatch) and carries the run of
// the command bar that context names (Run), so a writer that queues on it reads the run off the
// batch rather than off a context of its own: every statement of one write phase is made for one
// run, or for none. The zero WriteBatch is made for no run.
type WriteBatch struct {
	batch pgx.Batch
	run   string
}

// runKey is the context key WithRun carries a run under.
type runKey struct{}

// WithRun is ctx carrying run, the uuid of a run of the command bar - its interaction node's id,
// in the lowercase form Postgres writes a uuid as text - for which every write made on that
// context is made: a batch built for it carries the run (NewWriteBatch), and the graph store's
// writers name each node such a write changes as changed by it. It is the one way a run reaches
// a write. Its caller has proved the run live and the caller's own (api's withinGoingRun); nothing
// here asks either again. The empty run is no run, so WithRun(ctx, "") takes a run off ctx.
func WithRun(ctx context.Context, run string) context.Context {
	return context.WithValue(ctx, runKey{}, run)
}

// RunOf is the run ctx carries (WithRun), and the empty string where it carries none.
func RunOf(ctx context.Context) string {
	run, _ := ctx.Value(runKey{}).(string)
	return run
}

// NewWriteBatch is an empty write phase built for ctx: its writes are made for the run ctx
// carries (RunOf), which the batch keeps for every statement queued on it (Run).
func NewWriteBatch(ctx context.Context) WriteBatch {
	return WriteBatch{run: RunOf(ctx)}
}

// Run is the run of the command bar the batch's writes are made for, the empty string for none.
func (b *WriteBatch) Run() string { return b.run }

// LatestEventID is the SQL expression for the id of the event this session appended last: the
// events id sequence's currval, which the event's insert set (events.Broker.QueueAppend). A
// statement of a write phase that records the id of the event appended just before it - the
// references a write stamps with its event, an ask.opened payload's own id - names it so, rather
// than reading the id back in Go between the two statements. Nothing between the two may append
// another event.
const LatestEventID = `currval(pg_get_serial_sequence('events', 'id'))`

// Queued is one statement of a WriteBatch. Unless one of its methods says otherwise, its result is
// read as an Exec's and dropped.
type Queued struct {
	query *pgx.QueuedQuery
	what  string
}

// Queue adds sql with args after every statement queued so far. A failure of the statement itself
// comes back from Send wrapped as what failed ("what: err"); an empty what returns it unchanged,
// as a statement run on its own would.
func (b *WriteBatch) Queue(what, sql string, args ...any) Queued {
	queued := Queued{query: b.batch.Queue(sql, args...), what: what}
	queued.Exec(nil)
	return queued
}

// failed is err, the statement's own failure, wrapped as what failed.
func (q Queued) failed(err error) error {
	if err == nil || q.what == "" {
		return err
	}
	return fmt.Errorf("%s: %w", q.what, err)
}

// Exec hands the statement's command tag to fn once it has run; nil drops it. fn's error stops the
// batch's reading and is what Send returns, unchanged.
func (q Queued) Exec(fn func(pgconn.CommandTag) error) {
	q.query.Fn = func(results pgx.BatchResults) error {
		tag, err := results.Exec()
		if err != nil {
			return q.failed(err)
		}
		if fn == nil {
			return nil
		}
		return fn(tag)
	}
}

// QueryRow hands the statement's first row to scan once it has run, as tx.QueryRow would: Scan
// answers pgx.ErrNoRows when it returned none, and the statement's own failure wrapped as what
// failed. scan's error stops the batch's reading and is what Send returns, unchanged.
func (q Queued) QueryRow(scan func(pgx.Row) error) {
	q.Query(func(rows pgx.Rows) error {
		return scan(batchRow{rows: rows, queued: q})
	})
}

// Query hands the statement's rows to read once it has run. read's error stops the batch's reading
// and is what Send returns, unchanged; a failure the rows report once read returned is wrapped as
// what failed.
func (q Queued) Query(read func(pgx.Rows) error) {
	q.query.Fn = func(results pgx.BatchResults) error {
		rows, err := results.Query()
		if err != nil {
			return q.failed(err)
		}
		defer rows.Close()
		if err := read(rows); err != nil {
			return err
		}
		rows.Close()
		return q.failed(rows.Err())
	}
}

// batchRow is a queued statement's first row, read as pgx's own row of tx.QueryRow reads it.
type batchRow struct {
	rows   pgx.Rows
	queued Queued
}

func (r batchRow) Scan(dest ...any) error {
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return r.queued.failed(err)
		}
		return pgx.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return r.queued.failed(err)
	}
	r.rows.Close()
	return r.queued.failed(r.rows.Err())
}

// Send sends every queued statement to tx in one batch and reads their results in order, running
// each statement's callback as it reads its result. It returns the first failure: the first
// statement that failed (the server runs none after it) or the first callback that returned an
// error (whose later statements ran, and are rolled back with the transaction the caller now
// abandons), whichever comes first in the batch. The batch is empty again afterwards. An empty
// batch sends nothing.
func (b *WriteBatch) Send(ctx context.Context, tx pgx.Tx) error {
	if b.batch.Len() == 0 {
		return nil
	}
	batch := b.batch
	b.batch = pgx.Batch{}
	return tx.SendBatch(ctx, &batch).Close()
}

// RunWrite runs write's statements in tx at once, as a batch of their own (Send) built for ctx
// (NewWriteBatch): what a write step does in a transaction that queues no write phase.
func RunWrite(ctx context.Context, tx pgx.Tx, write func(*WriteBatch) error) error {
	batch := NewWriteBatch(ctx)
	if err := write(&batch); err != nil {
		return batch.SendFailed(ctx, tx, err)
	}
	return batch.Send(ctx, tx)
}

// SendFailed ends a batch whose building failed with err partway: it sends what was queued before
// the failure and returns the first failure among those statements, else err. Run one at a time,
// those statements would have run before the write that failed was reached, so one of them failing
// is the first failure of the write phase, which the caller answers as it always has.
func (b *WriteBatch) SendFailed(ctx context.Context, tx pgx.Tx, err error) error {
	if sendErr := b.Send(ctx, tx); sendErr != nil {
		return sendErr
	}
	return err
}
