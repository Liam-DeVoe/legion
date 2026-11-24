package embedqueue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The coverage counts are kept by triggers as delta rows (migration
// 20261124011834_search_coverage_counts): each write statement on a counted table inserts one delta
// row for each class it moved, in its own transaction, and the report sums the base rows and the
// delta rows not yet folded (coverageRead). This file is what keeps the base: the fold, which moves
// the delta rows into it every 10 s; the recount, which counts every class from its rows again, so
// a write that fired no trigger is followed; the fill, which is the recount over counts that hold
// nothing yet and records that it ended; and the loop that runs them (RunCounts).
//
// The fold, the recount and the fill each take one advisory lock inside their own transaction
// (countsLock), as api/issue_rank.go's lockProjectRankAllocation takes its lock, never a session
// lock on a pooled connection, so the lock ends with the transaction. So no fold and no recount
// run at once, in this process or in another one during a roll, and a fold never adds to a base
// row that a recount is replacing.

// The counts loop's periods and bounds (RunCounts). coverageFoldInterval and coverageFoldJitter
// are spec version 27's: every 10 s, plus a jitter of up to 1 s. The backoff of a failed fill, and
// of a failed recount, is its too: the first wait's bound is 10 s, each later bound twice the one
// before, and no bound passes 10 minutes. coverageFoldTimeout bounds one fold: a fold still
// running when the next would start has failed, and the next period runs it again.
const (
	coverageFoldInterval   = 10 * time.Second
	coverageFoldJitter     = time.Second
	coverageFoldTimeout    = coverageFoldInterval
	coverageFirstBackoff   = 10 * time.Second
	coverageBackoffCeiling = 10 * time.Minute
)

// The counts loop's settings a test sets, as identityRecheckInterval is a var for a test to shrink.
var (
	// coverageFillTimeout bounds the whole fill (FillCoverage): a fill that reaches it is a failed
	// fill. T16 sets it to four times the fill's longest time on scripts/coverage-scale.sh's corpus
	// at scale 3, from the builds Job; until then it holds 30 minutes, and the orchestrator replaces
	// it and this comment before the pull request leaves draft.
	coverageFillTimeout = 30 * time.Minute
	// coverageRecountTimeout is T, the one deadline of a whole recount (RecountCoverage): every
	// refresh batch and every group statement runs under the time left to it. T16 sets it to four
	// times the recount's longest whole time on scripts/coverage-scale.sh's corpus at scale 3, from
	// the builds Job; until then it holds 2 minutes, and the orchestrator replaces it and this
	// comment before the pull request leaves draft.
	coverageRecountTimeout = 2 * time.Minute
	// coverageRecountInterval and coverageRecountJitter space the recounts: each starts the interval
	// plus up to the jitter after the one before started (recountGap). Both follow from T
	// (recountSchedule).
	coverageRecountInterval, coverageRecountJitter = recountSchedule(coverageRecountTimeout)
	// coverageFoldRandom, coverageRecountRandom and coverageBackoffRandom are the random sources of
	// the fold's jitter, of the recount's jitter and of the backoff after a failed fill or recount,
	// each a fraction in [0, 1).
	coverageFoldRandom    = rand.Float64
	coverageRecountRandom = rand.Float64
	coverageBackoffRandom = rand.Float64
	// coverageFillFailures is how many fills from now on fail before their first statement
	// (errFillMadeToFail): 0 but under cmd/dispatch's DISPATCH_TEST_COVERAGE_FILL_FAILURES
	// (FailCoverageFillsForTest), which a test of a failed fill sets.
	coverageFillFailures atomic.Int64
)

// recountSchedule is the recount's interval and jitter for T, the recount's deadline: the
// interval is 10 minutes and the jitter one minute less T while T is under one minute, and
// otherwise the interval is 11 minutes less T and the jitter 0. A recount that ends took at most T,
// so each recount starts at most 11 minutes less one recount after the one before, whatever a
// recount takes, and every 10 to 11 minutes while T is under one minute.
func recountSchedule(timeout time.Duration) (interval, jitter time.Duration) {
	if timeout < time.Minute {
		return 10 * time.Minute, time.Minute - timeout
	}
	return 11*time.Minute - timeout, 0
}

// countsClock is the time the counts loop reads and waits on (RunCounts): the wall clock in a
// serving process (wallClock). A test of this package sets countsTime to a clock it moves itself,
// so a loop that waits ten seconds between folds runs its periods at once and the test reads every
// wait it asked for.
type countsClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

var countsTime countsClock = wallClock{}

// countsLock takes the one transaction-scoped advisory lock the fold, the recount and the fill
// share (pg_advisory_xact_lock), which ends with the transaction that took it.
const countsLock = `select pg_advisory_xact_lock(hashtext('search-coverage-counts'))`

// foldDeltas moves every delta row its snapshot sees into the base: it deletes them and adds their
// sums to the base rows of their classes, in one statement, so a reader sees the delta rows or the
// base they were folded into, never both and never neither. A delta of a transaction that has not
// committed at its snapshot is not visible to the delete and stays for the next fold.
const foldDeltas = `
	with moved as (delete from search_coverage_deltas returning kind, model, matched, dead, items)
	insert into search_coverage_counts (kind, model, matched, dead, items)
	select kind, model, matched, dead, sum(items)::bigint from moved group by kind, model, matched, dead
	on conflict (kind, model, matched, dead) do update set items = search_coverage_counts.items + excluded.items`

// recountStatement is one group's recount over counted, a select of that group's classes from its
// rows as (kind, model, matched, dead, items): at the statement's one snapshot it counts them,
// replaces the base rows of the group's kinds ($1) with them, a class the rows no longer hold at 0,
// and deletes the group's delta rows. A delta of a transaction that committed before that snapshot
// is in the rows counted and goes; a delta of any other transaction, one still running or one that
// commits after the snapshot, is not visible to the delete and stays for the next fold. A base row
// whose count stands is not written.
func recountStatement(counted string) string {
	return `
	with counted (kind, model, matched, dead, items) as (` + counted + `),
	     moved as (delete from search_coverage_deltas where kind = any($1::text[])),
	     kept as (select kind, model, matched, dead from search_coverage_counts where kind = any($1::text[]))
	insert into search_coverage_counts (kind, model, matched, dead, items)
	select kind, model, matched, dead, coalesce(counted.items, 0)
	from counted full join kept using (kind, model, matched, dead)
	on conflict (kind, model, matched, dead) do update set items = excluded.items
	where search_coverage_counts.items is distinct from excluded.items`
}

// windowClasses is the window kinds' classes, read from their rows as the window arm of main's
// coverage statement read them, through a BitmapOr of the two window indexes under planCoverage's
// settings (windowItems, planCoverage), and grouped by the kind's position for the reason that arm
// grouped by it: no index's order is of use to it. A class is matched where the row holds a vector
// of its current text, and then carries the row's model (” for none); otherwise its model is ”.
var windowClasses = `
	select (` + windowKindArray + `)[windows.position], windows.model, windows.matched, windows.dead, count(*)
	from (select array_position(` + windowKindArray + `, kind) as position,
	             coalesce(embedding is not null and embedded_hash = content_hash, false) as matched,
	             case when embedding is not null and embedded_hash = content_hash then coalesce(model, '') else '' end as model,
	             dead
	      from embeddings
	      where (` + windowEmbedded + `) or (` + windowUnembedded + `)) windows
	group by windows.position, windows.model, windows.matched, windows.dead`

// issueClasses is the issue's classes, read from the issues' state rows, which the triggers and
// the recount's refresh keep (refreshIssueStates); an issue with nothing to embed holds no class.
const issueClasses = `select '` + issueKind + `'::text, model, matched, dead, count(*)
	from search_coverage_issues where has_text group by model, matched, dead`

// countsGroup is one group the recount counts in a transaction and a statement of its own
// (recountStatement): its kinds, and the select of its classes. planned is the window group's,
// which runs under planCoverage's settings.
type countsGroup struct {
	kinds   []string
	counted string
	planned bool
}

// countsGroups is every group the recount counts, in the order it counts them: the issue, whose
// state rows the recount refreshes first (refreshIssueStates), the window kinds, and each
// not-indexed kind a count keeps (notIndexedKinds but the session), one class of every row of its
// table.
var countsGroups = func() []countsGroup {
	groups := []countsGroup{
		{kinds: []string{issueKind}, counted: issueClasses},
		{kinds: windowCoverageKinds, counted: windowClasses, planned: true},
	}
	for _, kind := range notIndexedKinds {
		if kind.kind == sessionKind {
			continue
		}
		groups = append(groups, countsGroup{
			kinds:   []string{kind.kind},
			counted: "select '" + kind.kind + "'::text, ''::text, false, false, " + kind.items,
		})
	}
	return groups
}()

// staleIssueStates refreshes (search_coverage_issue_refresh), in key order, up to $2 issues whose
// key sorts after $1 and whose state row is missing or records other inputs than the issue's own
// now: search_coverage_issue_inputs over issueSource, the issue's created_at and its embeddings
// row's id and xmin, the one rendering the refresh records, so a write to any input with its
// triggers off changes a value or a row's xmin and the comparison finds it (issueSource's comment
// names its one theoretical miss). It answers the last key it refreshed (” when none) and how many.
var staleIssueStates = `
	select coalesce(max(refreshed.key), ''), count(*)
	from (select picked.key, search_coverage_issue_refresh(picked.key)
	      from (select i.key
	            from ` + issueSourceFrom + `
	            left join search_coverage_issues s on s.key = i.key
	            where i.key > $1
	              and s.inputs is distinct from search_coverage_issue_inputs(` + issueSource + `, i.created_at, e.id, e.xmin)
	            order by i.key
	            limit $2) picked
	      order by picked.key) refreshed`

// goneIssueStates refreshes, in key order, up to $2 state rows whose key sorts after $1 and whose
// issue is gone, which the refresh deletes, and answers as staleIssueStates does.
const goneIssueStates = `
	select coalesce(max(refreshed.key), ''), count(*)
	from (select gone.key, search_coverage_issue_refresh(gone.key)
	      from (select s.key from search_coverage_issues s
	            where s.key > $1 and not exists (select 1 from issues i where i.key = s.key)
	            order by s.key
	            limit $2) gone
	      order by gone.key) refreshed`

// markFilled records that a fill has ended, in the transaction of the fill's last group.
const markFilled = `insert into search_coverage_filled (done_at) values (now()) on conflict (id) do nothing`

// countsFilled reads whether a fill has ended since the migration that made the counts.
const countsFilled = `select exists (select 1 from search_coverage_filled where id)`

// errFillTimedOut is what a fill that reached coverageFillTimeout answers, wrapped around the error
// of the statement the timeout ended.
var errFillTimedOut = errors.New("the search coverage counts fill ran past its timeout")

// errFillMadeToFail is what a fill answers while coverageFillFailures is above 0.
var errFillMadeToFail = errors.New("the search coverage counts fill was made to fail (DISPATCH_TEST_COVERAGE_FILL_FAILURES)")

// countsTransaction runs fn in a transaction of its own whose statements each run under the time
// left to ctx's deadline, which every caller sets (the recount's, the fill's, the fold's), after
// taking countsLock when locked. It commits when fn answers nil. The rollback runs on a context
// ctx's end does not end, so a loop stopped mid-statement still hands its connection back to the
// pool.
func countsTransaction(ctx context.Context, database *store.Store, locked bool, fn func(pgx.Tx) error) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("a coverage counts transaction needs a deadline")
	}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), coverageRollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rollback)
	}()
	if _, err := tx.Exec(ctx, fmt.Sprintf("set local statement_timeout = %d", max(time.Until(deadline).Milliseconds(), 1))); err != nil {
		return fmt.Errorf("bound the statements: %w", err)
	}
	if locked {
		if _, err := tx.Exec(ctx, countsLock); err != nil {
			return fmt.Errorf("take the counts lock: %w", err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// foldCoverage runs one fold (foldDeltas) under the counts lock, within coverageFoldTimeout.
func foldCoverage(ctx context.Context, database *store.Store) error {
	ctx, cancel := context.WithTimeout(ctx, coverageFoldTimeout)
	defer cancel()
	return countsTransaction(ctx, database, true, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, foldDeltas); err != nil {
			return fmt.Errorf("fold the coverage counts: %w", err)
		}
		return nil
	})
}

// refreshIssueStates brings every issue's state row up to its inputs before the issue group is
// counted: staleIssueStates, then goneIssueStates, each in batches of coverageStatementRows, each
// batch a transaction of its own that takes no counts lock (a refresh writes state rows, and
// through their trigger delta rows, as any write does, and no base row).
func refreshIssueStates(ctx context.Context, database *store.Store) error {
	for _, statement := range []string{staleIssueStates, goneIssueStates} {
		after := ""
		for {
			var last string
			var refreshed int
			if err := countsTransaction(ctx, database, false, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, statement, after, coverageStatementRows).Scan(&last, &refreshed)
			}); err != nil {
				return fmt.Errorf("refresh the issues' coverage states: %w", err)
			}
			if refreshed < coverageStatementRows {
				break
			}
			after = last
		}
	}
	return nil
}

// recountGroups counts every group (countsGroups), each in a transaction of its own under the
// counts lock, after refreshing the issues' state rows; with fill, the last group's transaction
// records that the fill ended (markFilled).
func recountGroups(ctx context.Context, database *store.Store, fill bool) error {
	if err := refreshIssueStates(ctx, database); err != nil {
		return err
	}
	for position, group := range countsGroups {
		last := fill && position == len(countsGroups)-1
		if err := countsTransaction(ctx, database, true, func(tx pgx.Tx) error {
			if group.planned {
				if _, err := tx.Exec(ctx, coverageSettings); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, recountStatement(group.counted), group.kinds); err != nil {
				return err
			}
			if last {
				if _, err := tx.Exec(ctx, markFilled); err != nil {
					return fmt.Errorf("record the fill: %w", err)
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("recount %s: %w", strings.Join(group.kinds, ", "), err)
		}
	}
	return nil
}

// RecountCoverage counts every class of the coverage counts from its rows again, group by group
// (recountGroups), beside live writes: a write that commits while it runs keeps its delta row for
// the next fold, and one that fired no trigger is counted. The whole recount runs under one
// deadline, coverageRecountTimeout. It logs when it starts and, with its length, when it ends.
func RecountCoverage(ctx context.Context, database *store.Store) error {
	ctx, cancel := context.WithTimeout(ctx, coverageRecountTimeout)
	defer cancel()
	started := time.Now()
	slog.Info("dispatch embedqueue: coverage counts recount started", "timeout", coverageRecountTimeout)
	if err := recountGroups(ctx, database, false); err != nil {
		return err
	}
	slog.Info("dispatch embedqueue: coverage counts recounted", "elapsed_ms", time.Since(started).Milliseconds())
	return nil
}

// FillCoverage fills the coverage counts: the recount over counts that hold nothing yet, so it
// stays exact beside live writes as the recount does, which records in its last group's
// transaction that the fill ended, so a fill cut off before then leaves the counts not filled
// (ErrCoverageNotFilled) and the next fill runs whole. The whole fill runs under one deadline,
// coverageFillTimeout, and a fill that reaches it fails with errFillTimedOut. It logs when it
// starts and, with its length, when it ends.
func FillCoverage(ctx context.Context, database *store.Store) error {
	fillCtx, cancel := context.WithTimeout(ctx, coverageFillTimeout)
	defer cancel()
	started := time.Now()
	slog.Info("dispatch embedqueue: coverage counts fill started", "timeout", coverageFillTimeout)
	if coverageFillFailures.Load() > 0 && coverageFillFailures.Add(-1) >= 0 {
		return errFillMadeToFail
	}
	if err := recountGroups(fillCtx, database, true); err != nil {
		var pgErr *pgconn.PgError
		if ctx.Err() == nil && (errors.Is(fillCtx.Err(), context.DeadlineExceeded) || errors.As(err, &pgErr) && pgErr.Code == "57014") {
			return fmt.Errorf("%w (%s): %w", errFillTimedOut, coverageFillTimeout, err)
		}
		return err
	}
	slog.Info("dispatch embedqueue: coverage counts filled", "elapsed_ms", time.Since(started).Milliseconds())
	return nil
}

// CountsDeps are what the counts loop runs with.
type CountsDeps struct {
	Store *store.Store
	// Serving is closed once this process serves: it listens and the registry of live server
	// processes admitted it (cmd/dispatch). RunCounts takes nothing and writes nothing before.
	Serving <-chan struct{}
}

// RunCounts keeps the coverage counts until ctx ends. It waits for deps.Serving, so nothing of it
// runs before the server listens and no start waits for a count. Its first run is the fill when no
// fill has ended (fillUnlessFilled), and a recount otherwise; then it folds every
// coverageFoldInterval plus up to coverageFoldJitter (foldCoverage), and starts each recount
// coverageRecountInterval plus up to coverageRecountJitter after the one before started
// (RecountCoverage), one fold or one recount at a time.
//
// A fill that fails, or that reaches its timeout, is logged at ERROR and run again after a capped
// exponential backoff with full jitter (backoffWait), until one ends; a stop that cuts a fill off
// leaves the counts not filled, and the next start fills again. A failed recount is logged at ERROR
// and run again after the same backoff, which a recount that ends resets, so a recount its bound
// outgrows runs at most every 10 minutes; the folds go on meanwhile. A failed fold is logged at
// ERROR and runs again at the next period. Cmd/dispatch starts it at every start, with an embedder
// or with none, since the counts follow every write whatever embeds.
func RunCounts(ctx context.Context, deps CountsDeps) {
	select {
	case <-ctx.Done():
		return
	case <-deps.Serving:
	}
	ctx = store.ForConcurrentWorker(ctx)
	filledAt, ok := fillUnlessFilled(ctx, deps.Store)
	if !ok {
		return
	}
	nextFold := countsTime.Now().Add(foldGap())
	// A start whose counts were filled already recounts at once; a fill was a recount.
	nextRecount := countsTime.Now()
	if !filledAt.IsZero() {
		nextRecount = filledAt.Add(recountGap())
	}
	recountFailures := 0
	for {
		recount := !nextRecount.After(nextFold)
		due := nextFold
		if recount {
			due = nextRecount
		}
		if !countsWait(ctx, due) {
			return
		}
		started := countsTime.Now()
		if !recount {
			if err := foldCoverage(ctx, deps.Store); err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("dispatch embedqueue: coverage counts fold failed; it runs again at the next period", "error", err)
			}
			nextFold = started.Add(foldGap())
			continue
		}
		begun := time.Now()
		if err := RecountCoverage(ctx, deps.Store); err != nil {
			if ctx.Err() != nil {
				return
			}
			recountFailures++
			wait := backoffWait(recountFailures)
			slog.Error("dispatch embedqueue: coverage counts recount failed; it runs again later",
				"error", err, "elapsed_ms", time.Since(begun).Milliseconds(), "retryIn", wait)
			nextRecount = countsTime.Now().Add(wait)
			continue
		}
		recountFailures = 0
		nextRecount = started.Add(recountGap())
	}
}

// fillUnlessFilled is the counts loop's first run where no fill has ended: the fill, run again
// after its backoff until one ends. It answers when the fill that ended started, the zero time
// when the counts were filled already, and false once ctx has ended.
func fillUnlessFilled(ctx context.Context, database *store.Store) (filledAt time.Time, ok bool) {
	for failures := 0; ; {
		started := countsTime.Now()
		begun := time.Now()
		var filled bool
		err := database.Pool.QueryRow(ctx, countsFilled).Scan(&filled)
		switch {
		case err == nil && filled:
			return time.Time{}, true
		case err == nil:
			err = FillCoverage(ctx, database)
		default:
			err = fmt.Errorf("read whether the coverage counts are filled: %w", err)
		}
		if ctx.Err() != nil {
			return time.Time{}, false
		}
		if err == nil {
			return started, true
		}
		failures++
		wait := backoffWait(failures)
		slog.Error("dispatch embedqueue: coverage counts fill failed; it runs again later",
			"error", err, "elapsed_ms", time.Since(begun).Milliseconds(), "timed_out", errors.Is(err, errFillTimedOut), "retryIn", wait)
		if !countsWait(ctx, countsTime.Now().Add(wait)) {
			return time.Time{}, false
		}
	}
}

// backoffWait is the wait after the failures'th failed fill, or failed recount, in a row: a random
// time from 0 to its bound (coverageBackoffRandom), capped exponential backoff with full jitter
// (https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/).
func backoffWait(failures int) time.Duration {
	return time.Duration(coverageBackoffRandom() * float64(backoffBound(failures)))
}

// backoffBound is the bound of the wait after the failures'th failure in a row: the first is
// coverageFirstBackoff, each later one twice the one before, and none passes
// coverageBackoffCeiling.
func backoffBound(failures int) time.Duration {
	bound := coverageFirstBackoff
	for range failures - 1 {
		if bound >= coverageBackoffCeiling {
			break
		}
		bound *= 2
	}
	return min(bound, coverageBackoffCeiling)
}

// foldGap is how long after a fold started the next one starts: coverageFoldInterval plus the
// fold's jitter (coverageFoldRandom).
func foldGap() time.Duration {
	return coverageFoldInterval + time.Duration(coverageFoldRandom()*float64(coverageFoldJitter))
}

// recountGap is how long after a recount started the next one starts: coverageRecountInterval plus
// the recount's jitter (coverageRecountRandom).
func recountGap() time.Duration {
	return coverageRecountInterval + time.Duration(coverageRecountRandom()*float64(coverageRecountJitter))
}

// countsWait waits until until on countsTime, and answers false when ctx ends first.
func countsWait(ctx context.Context, until time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-countsTime.After(max(until.Sub(countsTime.Now()), 0)):
		return ctx.Err() == nil
	}
}
