package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Change is one delivery_changes row (migration 0100): what changed, its run's kind when it is a
// run or a job, and the time that decides which windows it can touch, before and after (a pull
// request's merged_at; a run's later head commit or start). The row triggers write one only when a
// row's content changed, so a reconcile pass that finds nothing new logs none.
//
// An issue_facts row holds no repository: Repo carries the project key and Ref the issue key,
// because a stored Legion fact belongs to a project and an issue (migration 20261111093100). Its
// TOld and TNew are the two ends of the span of days that issue's work falls in, never the row's
// time before and after.
//
// The three eval kinds hold none either: Repo carries the project key and Ref the eval id
// (migrations 20261113082200 to 20261113082202). An eval_readings row is one statement's readings
// of one eval, spanning their measured_at; an eval_versions row is one new version of a
// definition or of one of its series and carries no span, since it changes how every window of
// the eval reads; an eval_days row is one transaction's writes of one eval's day rows, spanning
// midnight UTC of the first and the last day written.
//
// An issue_filings row holds none either: Repo carries the project key and Ref the filed issue's
// key (migration 20261113170751). TOld and TNew are the filing's filed_at before and after: an
// insert carries TNew alone, a delete TOld alone.
//
// A settings row is a write of the delivery settings, whose Repo is empty, or of a repository's
// mapping in repo_projects, whose Repo is that repository (migration 20261113170752); Ref, TOld
// and TNew are empty for both.
type Change struct {
	Seq     int64
	At      time.Time
	Kind    string // one of the ChangeKind constants
	RunKind string // "deploy" | "pr_checks" | "" for the rest
	Repo    string
	Ref     string
	TOld    *time.Time
	TNew    *time.Time
}

// The kinds of delivery change (Change.Kind), each the kind a table's trigger logs, so a reader of
// the log compares with these rather than spell one again. The delivery intake's five
// (pull_request, run, run_job, session_group, settings) are written into delivery_changes by their
// triggers (delivery_change_log, migration 0100); the eval and Legion kinds (issue_facts and the
// rest) are recorded in the outbox and moved into the log by DrainPending.
const (
	ChangeKindPullRequest  = "pull_request"
	ChangeKindRun          = "run"
	ChangeKindRunJob       = "run_job"
	ChangeKindSessionGroup = "session_group"
	ChangeKindSettings     = "settings"
	ChangeKindIssueFacts   = "issue_facts"
	ChangeKindEvalReadings = "eval_readings"
	ChangeKindEvalVersions = "eval_versions"
	ChangeKindEvalDays     = "eval_days"
	ChangeKindIssueFilings = "issue_filings"
)

// ChangeRetention is how long delivery_changes keeps a row: a page away longer reads its window once.
const ChangeRetention = 24 * time.Hour

// ChangesChannel is the Postgres notification channel a delivery change announces itself on: the
// trigger that writes each delivery_changes row (migration 0100, delivery_change_log), the trigger
// that records each pending row (delivery_change_log_pending, 20261115155339) and the drain that
// moves pending rows into the log (DrainPending) each send it, and Postgres delivers it at their
// commit. The Delivery page's live hub and the Legion facts consumer both listen on it, and this
// package owns the row, so the name is declared here alone, as SettingsChannel is beside the
// settings row.
const ChangesChannel = "delivery_changes"

// LockChanges takes, for the rest of tx, the change log's lock: the transaction advisory lock
// (6900, 0) every insert into delivery_changes takes before it draws a seq, and holds to its
// commit (migration 0100). A seq is therefore drawn only once every row below it has committed or
// rolled back, so a reader's cursor never skips a change row: a reader that has read through seq
// never meets a row below it later.
//
// A path takes the lock either first, before any row lock, or last, in the statement that ends its
// transaction, after every row lock it takes; a one-statement write is the last-statement case. A
// path that takes it last holds every row lock it needs when it waits, and a path that takes it
// first locks no row a path taking it last ever holds, so no path holds the lock while it waits
// for a lock a waiter for it holds, and the lock order has no cycle. A path that takes it last
// holds it for its last statement and its commit; one that takes it first, for its whole
// transaction, so those transactions are short.
//
//   - Last, in one statement: every one-statement write of a delivery row (a pull request, a run,
//     the settings), and a run's jobs write (UpsertRunJobs), which writes the run row and every job
//     in one statement.
//   - Last, in the statement that ends its transaction: the issue.deployed write
//     (recordDeployedEvent), which locks its pull request's row first, appends the event, and
//     marks the row last.
//   - First, with LockChanges: every write of the session groups (lockSessionGroups) and of a
//     repository's project mapping (api's lockRepoProjectWrite, and cmd/dispatch-seed's write of
//     the projects it seeds), each a few statements; and the drain (DrainPending), which takes it
//     before it locks the pending rows it moves.
//
// No eval, refresher or Legion facts write takes it: their changes wait in the outbox
// (delivery_changes_pending, migration 20261115155339), and the drain moves them into the log in a
// short transaction of its own after they commit.
func LockChanges(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(6900, 0)`); err != nil {
		return fmt.Errorf("take the delivery change log's lock: %w", err)
	}
	return nil
}

// drainBatch is the most pending rows one transaction of the drain moves. On the pod it was
// measured on, a batch of 1,000 answered in 13 to 30 ms while the pod was quiet and in up to 94 ms,
// a sampled hold up to 126 ms, while the api package's tests ran beside it, and 10,000 rows in one
// statement held the change log's lock 118 to 328 ms; a quarter of that batch stays well under
// R19's 100 ms under load too, and no burst, however large, holds the lock longer than one batch.
const drainBatch = 250

// DrainPending moves every committed pending change row (delivery_changes_pending, the outbox the
// eval and Legion tables' triggers record their changes in) into delivery_changes, and answers how
// many it moved. It moves them a batch at a time, each batch one statement of its own,
// delivery_changes_drain (migration 20261115155339), never a statement of a caller's transaction,
// and moves another batch while a whole one moved. So it holds the change log's lock for one
// batch's statement and its commit at a time, whatever the burst: each batch takes the lock first,
// before the pending rows it deletes, and moves the lowest pending ids in id order, keeping each
// row's at. Where nothing is pending it answers 0 and takes no lock. Two drains take turns on the
// lock, and each pending row is moved exactly once.
//
// A pending row's id is drawn at its insert and not at its commit, so seq follows the order the
// drains move rows in, not the order their writers committed in; every seq is still drawn under
// the lock (LockChanges), so a reader's cursor never skips a moved row. Its callers: each eval,
// refresher and Legion facts write, right after its commit and before its route answers; the live
// hub, before each read of the log; the refresher, at the start of each pass; and the server's
// boot, before it serves. A writer killed between its commit and its drain loses nothing: its
// pending row's notification reached every live process's hub at that commit, and each drains on
// that wake, or else the next process's boot does.
func DrainPending(ctx context.Context, pool *store.Pool) (int64, error) {
	var total int64
	for {
		moved, err := drainOneBatch(ctx, pool)
		total += moved
		if err != nil || moved < drainBatch {
			return total, err
		}
	}
}

// drainOneBatch moves up to drainBatch pending rows into delivery_changes in one statement of its
// own, and answers how many it moved (DrainPending).
func drainOneBatch(ctx context.Context, pool *store.Pool) (int64, error) {
	var moved int64
	if err := pool.QueryRow(ctx, `select delivery_changes_drain($1)`, drainBatch).Scan(&moved); err != nil {
		return 0, fmt.Errorf("move the pending delivery changes into the change log: %w", err)
	}
	return moved, nil
}

// ChangesSince reads up to limit changes after seq, oldest first. Every seq is drawn under the
// change log's lock (LockChanges), so a reader that has read through seq never meets a row below it
// later: its cursor never skips a change row.
func ChangesSince(ctx context.Context, q Querier, seq int64, limit int) ([]Change, error) {
	rows, err := q.Query(ctx, `
		select seq, at, kind, coalesce(run_kind, ''), coalesce(repo, ''), coalesce(ref, ''), t_old, t_new
		from delivery_changes
		where seq > $1
		order by seq
		limit $2
	`, seq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []Change{}
	for rows.Next() {
		var change Change
		if err := rows.Scan(&change.Seq, &change.At, &change.Kind, &change.RunKind, &change.Repo, &change.Ref, &change.TOld, &change.TNew); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

// LatestChangeSeq is the newest change's seq, or 0 when the log is empty.
func LatestChangeSeq(ctx context.Context, q Querier) (int64, error) {
	var seq int64
	err := q.QueryRow(ctx, `select coalesce(max(seq), 0) from delivery_changes`).Scan(&seq)
	return seq, err
}

// OldestChangeSeq is the oldest kept change's seq, or 0 when the log is empty: a reader whose
// cursor is below it has missed changes PruneChanges removed.
func OldestChangeSeq(ctx context.Context, q Querier) (int64, error) {
	var seq int64
	err := q.QueryRow(ctx, `select coalesce(min(seq), 0) from delivery_changes`).Scan(&seq)
	return seq, err
}

// ChangeAt is when the change at seq was recorded, or nil when no row holds seq (pruned, or the log
// was reset): a reader compares it with the time it read for its cursor to tell a reset log.
func ChangeAt(ctx context.Context, q Querier, seq int64) (*time.Time, error) {
	var at time.Time
	err := q.QueryRow(ctx, `select at from delivery_changes where seq = $1`, seq).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// PruneChanges deletes every change recorded more than ChangeRetention before the database's now(),
// and answers how many it deleted. Each row's at is the database's clock too, so the cutoff and the
// rows it compares share one clock whichever process prunes.
func PruneChanges(ctx context.Context, pool *store.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `delete from delivery_changes where at < now() - $1::interval`, ChangeRetention)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
