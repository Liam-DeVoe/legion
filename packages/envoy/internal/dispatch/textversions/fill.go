package textversions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The fill writes the keyword table from the event log in batches, each one transaction of its
// own that reads the events above its cursor, writes their rows and moves the cursor past them
// (filler.batch), and in passes, each up to one read of the event log's safe horizon
// (filler.pass).
const (
	// batchEvents and batchBytes bound one batch: at most 500 events, and at most 2 MB of the text
	// their vectors are made from, each post's body whole and each primary document version by
	// its version.size; an event larger than that alone is a batch alone. A full batch took 0.6
	// to 1.3 s on the pod the plan measured with.
	batchEvents = 500
	batchBytes  = 2 << 20
	// batchStatementBase and batchStatementPerMB bound each statement of a batch: 10 s, and 1 s
	// more for each MB of text the batch holds.
	batchStatementBase  = 10 * time.Second
	batchStatementPerMB = time.Second
	// rereadFirst and rereadMax pace the reads of the horizon a fill makes while the horizon
	// stands below an id an observation saw: 1 s, doubling to 8 s.
	rereadFirst = time.Second
	rereadMax   = 8 * time.Second
	// progressEvery bounds the fill's progress line while it catches up to one each 30 s.
	progressEvery = 30 * time.Second
)

// fillHoldName is the name of the duty hold the fill runs under in a serving process, one process
// at a time (duty.TryHold): each batch of a holder checks that duty_holds still names it.
const fillHoldName = "search-text-versions-fill"

// FillToHorizon writes the rows of every event up to the event log's safe horizon once, from the
// fill's cursor, in batches with their rest, and returns: with no hold, so two fills that run at
// once take turns on the cursor row, and with no pause. It reads the horizon again while the
// horizon stands below an id an observation of it saw, 1 s and then up to 8 s apart, so every
// event committed before the call has its rows when it returns. A server runs the fill under its
// hold instead; this is for tests and tools.
//
// An event whose payload the rules cannot read stops it, with the rows of the events before that
// one written and an error that names it.
func FillToHorizon(ctx context.Context, db *store.Store, horizon *events.Horizon) error {
	return newFiller(db.Pool, horizon).toHorizon(ctx)
}

// txBeginner is what the fill needs of a pool: transactions. *store.Pool, the shared pool
// FillToHorizon writes through, and *pgxpool.Pool, a lease pool's reservation, both satisfy it.
type txBeginner interface {
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// filler is one fill: the pool its batches run on, the horizon its passes read, and what a
// serving process sets on it. A filler is used by one goroutine at a time.
type filler struct {
	pool    txBeginner
	horizon *events.Horizon
	logger  *slog.Logger
	// holder and holdName are the hold the fill's batches run under: each batch checks that
	// duty_holds still names holder for holdName, and writes holder into the cursor row. uuid.Nil
	// runs with no hold.
	holder   uuid.UUID
	holdName string
	// pause, when set, runs before each batch and holds it back until it returns; an error ends
	// the pass with it.
	pause func(ctx context.Context) error
	// rest waits after each batch that read events for as long as the batch ran, so the fill
	// takes at most half of one backend's time; now is the clock the batch is timed by.
	rest func(ctx context.Context, d time.Duration) error
	now  func() time.Time
	// maxEvents and maxBytes are the bounds of one batch (batchEvents, batchBytes).
	maxEvents int
	maxBytes  int64
	// progress is what the batches since the last progress line wrote, and breaches the events
	// this fill wrote at or before a reach it had stored.
	progress progress
	breaches int
}

// progress counts the batches' work since the last progress line, which since names.
type progress struct {
	since             time.Time
	events, rows, cut int
}

// newFiller is a fill on pool, up to horizon, with no hold, no pause and the production bounds.
func newFiller(pool txBeginner, horizon *events.Horizon) *filler {
	return &filler{
		pool:      pool,
		horizon:   horizon,
		logger:    slog.Default(),
		rest:      sleep,
		now:       time.Now,
		maxEvents: batchEvents,
		maxBytes:  batchBytes,
	}
}

// sleep waits d, or until ctx ends with its error.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// toHorizon runs passes until one leaves no id an observation saw above the horizon it read: a
// pass that ends with the horizon held below such an id (Horizon.Gap), or with the horizon proven
// past the safe it read by another reader of it, is followed by another 1 s later, doubling to
// 8 s. The horizon waits on every transaction id of the instance, and a transaction that writes no
// event ends with no notification, so nothing else says when to read it again.
func (f *filler) toHorizon(ctx context.Context) error {
	wait := rereadFirst
	for {
		safe, err := f.pass(ctx)
		if err != nil {
			return err
		}
		if !f.heldBelow(safe) {
			return nil
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		wait = min(2*wait, rereadMax)
	}
}

// heldBelow is whether the horizon stands below an id an observation of it saw, or another reader
// of it has proven it past safe, the horizon the pass read: either way the log holds events above
// what the pass wrote that a later read of the horizon reaches.
func (f *filler) heldBelow(safe int64) bool {
	return f.horizon.Gap() != events.GapNone || f.horizon.Proven() > safe
}

// pass reads the event log's safe horizon once (Horizon.Safe, or Horizon.Await while Safe answers
// 0, which it does until an observation is proven) and writes batches up to it, resting after
// each as long as it ran, and answers the horizon it wrote up to. A batch that finds the log
// reset under the cursor empties the table (filler.batch), and one that finds the horizon read
// from a log since reset writes nothing: either way the horizon names ids of an old log, and the
// pass forgets it (Horizon.Reset) and reads it again, so no id of the new log still open is
// passed.
func (f *filler) pass(ctx context.Context) (int64, error) {
	for {
		safe, err := f.horizon.Safe(ctx)
		if err != nil {
			return 0, fmt.Errorf("keyword fill: read the event log's safe horizon: %w", err)
		}
		if safe == 0 {
			if safe, err = f.horizon.Await(ctx); err != nil {
				return 0, fmt.Errorf("keyword fill: await the event log's safe horizon: %w", err)
			}
		}
		again, err := f.batchesTo(ctx, safe)
		if err != nil || !again {
			return safe, err
		}
		f.horizon.Reset()
	}
}

// batchesTo writes batches up to safe until one reads no event or reads every event left up to
// safe, and answers whether the horizon must be read again, the log having been reset.
func (f *filler) batchesTo(ctx context.Context, safe int64) (bool, error) {
	for {
		if f.pause != nil {
			if err := f.pause(ctx); err != nil {
				return false, err
			}
		}
		result, err := f.batch(ctx, safe)
		if result.events > 0 {
			f.record(result)
		}
		switch {
		case err != nil:
			return false, err
		case result.reset || result.oldHorizon:
			return true, nil
		case result.events == 0:
			return false, nil
		}
		if err := f.rest(ctx, result.took); err != nil {
			return false, err
		}
		if !result.full {
			return false, nil
		}
	}
}

// batchResult is what one batch did.
type batchResult struct {
	// events is how many events it wrote the rows of, from first to the cursor it moved to;
	// bytes their text, rows the rows they started, cut how many of those hold a body cut short.
	events      int
	first       int64
	bytes       int64
	rows, cut   int
	cursor      int64
	reach       *time.Time // the reach the cursor row holds once the batch committed
	took        time.Duration
	full        bool // events up to the horizon were left for the next batch
	breaches    int  // events written whose created_at is at or before the reach stored before
	storedReach *time.Time
	// reset is a batch that found the log reset under the cursor and emptied the table, and
	// oldHorizon one that found the horizon above the log's newest id, read from a log since
	// reset, and wrote nothing.
	reset, oldHorizon bool
}

// The statements of a batch's transaction (filler.batch) but its read of the events.
const (
	// batchBoundsQuery bounds the batch's transaction: a lock it waits 5 s for at most, a pause
	// between its statements of 10 s at most, which ends the transaction of a holder frozen
	// inside a batch and so frees the cursor row, and each statement's time ($1).
	batchBoundsQuery = `select set_config('lock_timeout', '5s', true),
	       set_config('idle_in_transaction_session_timeout', '10s', true),
	       set_config('statement_timeout', $1, true)`
	// batchStatementQuery sets each statement's time once the batch knows its size ($1).
	batchStatementQuery = `select set_config('statement_timeout', $1, true)`
	// lockCursorQuery locks the fill's cursor row for the batch's transaction, so two fills write
	// one after the other: the second reads the cursor the first moved.
	lockCursorQuery = `select event_id, event_at, reach from public.search_text_versions_fill where id = 1 for update`
	// createCursorQuery creates the cursor row where none is: no event written, no reach, the
	// holder $1 and the rules version $2.
	createCursorQuery = `insert into public.search_text_versions_fill (id, event_id, holder, rules) values (1, 0, $1, $2)
	    on conflict (id) do nothing`
	// holdCheckQuery answers whether holder $2 may write a batch under hold $1: duty_holds names
	// it live, and the cursor row names no holder, this one, or one whose hold is gone.
	holdCheckQuery = `select exists (select from public.duty_holds h
	                    where h.name = $1 and h.holder = $2 and h.expires_at > now())
	       and (f.holder is null or f.holder = $2
	            or not exists (select from public.duty_holds o
	                            where o.name = $1 and o.holder = f.holder and o.expires_at > now()))
	  from public.search_text_versions_fill f
	 where f.id = 1`
	// emptyTableQuery and emptyCursorQuery empty both tables of a log that was reset: delete,
	// not truncate, so no read of them waits on an exclusive lock. The cursor row is written
	// again after them, at no event (createCursorQuery).
	emptyTableQuery  = `delete from public.search_text_versions`
	emptyCursorQuery = `delete from public.search_text_versions_fill`
	// advanceCursorQuery moves the cursor to the last event the batch wrote ($1, its created_at
	// $2), stores the reach $3 where it is later than the one stored (greatest ignores a null, so
	// a reach computed now never lowers the stored one, and none computed leaves it), and records
	// the holder $4 and the rules version $5 that wrote it.
	advanceCursorQuery = `update public.search_text_versions_fill
	   set event_id = $1, event_at = $2, reach = greatest(reach, $3::timestamptz), holder = $4,
	       rules = $5, updated_at = now()
	 where id = 1
	returning reach`
)

// readEventsQuery reads one batch's events: those above the cursor ($1) and at or below the safe
// horizon ($2), in id order, at most $3 of them, and of those the first whose text comes to at
// most $4 bytes, the first one whatever its size. An event's text is what its vector is made
// from, for a type a rule reads ($5): its payload, the whole body of a post whose event cut it
// (its row's, the one source a cut body is most often read from), and for a primary document's
// version its version.size; any other type makes no vector and counts nothing. Each read of a
// payload's value as a uuid or a number is guarded, so a payload no rule can read reaches the
// rules, which name it, rather than failing the statement. Its project is the one the log names
// for it: its own, its issue's or its document's.
const readEventsQuery = `
with candidates as (
       select e.id, e.type, e.issue_key, e.artifact_id, e.project_key, e.seq, e.created_at, e.payload,
              case when e.type = any($5::text[]) then
                     octet_length(e.payload::text)
                     + case when e.type = 'artifact.version'
                                 and jsonb_typeof(e.payload #> '{version,size}') = 'number'
                                 and exists (select from public.artifacts a
                                              where a.is_primary
                                                and a.id = case when pg_input_is_valid(e.payload->>'artifact_id', 'uuid')
                                                                then (e.payload->>'artifact_id')::uuid end)
                            then (e.payload #>> '{version,size}')::numeric::bigint
                            else 0 end
                     + case when e.payload->>'body_truncated' = 'true'
                                 and pg_input_is_valid(e.payload->>'id', 'uuid')
                            then coalesce(case when e.type like 'comment.%'
                                                 then (select octet_length(c.body) from public.comments c
                                                        where c.id = (e.payload->>'id')::uuid)
                                               when e.type like 'message.%'
                                                 then (select octet_length(m.body) from public.messages m
                                                        where m.id = (e.payload->>'id')::uuid)
                                          end, 0)
                            else 0 end
                   else 0 end as size
         from public.events e
        where e.id > $1 and e.id <= $2
        order by e.id
        limit $3),
     sized as (
       select c.*, sum(c.size) over (order by c.id) as running, row_number() over (order by c.id) as n,
              count(*) over () as candidates
         from candidates c)
select s.id, s.type, s.issue_key, coalesce(s.project_key, i.project_key, ar.project_key, ''), s.seq,
       s.created_at, s.payload, s.size, s.candidates
  from sized s
  left join public.issues i on i.key = s.issue_key
  left join public.artifacts ar on ar.id = s.artifact_id
 where s.n = 1 or s.running <= $4
 order by s.id`

// batch writes one batch, up to safe, in one READ COMMITTED transaction: it locks the cursor row
// (creating it at no event where none is); under a hold, checks that the hold is still this
// holder's and writes nothing otherwise (duty.ErrHoldLost); checks the log against the cursor
// (events.EventLogWasReset) and empties both tables of a log that was reset; reads the events
// above the cursor up to safe within the batch's bounds; decides their versions with the rules
// and writes them; moves the cursor to the last of them; computes the reach
// (store.ComputeTextVersionsReach) and stores it where it is later than the one stored; and
// commits. A batch that reads no event still stores the reach, so a fill caught up keeps it
// current. An event whose payload the rules cannot read stops the batch there: the events before
// it are written and answered, with the error that names it.
func (f *filler) batch(ctx context.Context, safe int64) (batchResult, error) {
	started := f.now()
	ctx = store.WithTransactionTracking(ctx)
	tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: begin a batch: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, batchBoundsQuery, statementTimeout(0)); err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: bound the batch: %w", err)
	}
	cursor, cursorAt, stored, err := f.lockCursor(ctx, tx)
	if err != nil {
		return batchResult{}, err
	}
	if f.holder != uuid.Nil {
		var holds bool
		if err := tx.QueryRow(ctx, holdCheckQuery, f.holdName, f.holder).Scan(&holds); err != nil {
			return batchResult{}, fmt.Errorf("keyword fill: check the hold %q: %w", f.holdName, err)
		}
		if !holds {
			return batchResult{}, fmt.Errorf("keyword fill: write a batch under the hold %q: %w", f.holdName, duty.ErrHoldLost)
		}
	}
	newest, atCursor, err := events.EventCursorCheck(ctx, tx, cursor)
	if err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: check the event log against the cursor: %w", err)
	}
	var at time.Time
	if cursorAt != nil {
		at = *cursorAt
	}
	if events.EventLogWasReset(cursor, at, newest, atCursor) {
		if err := f.empty(ctx, tx); err != nil {
			return batchResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return batchResult{}, fmt.Errorf("keyword fill: commit the emptied table: %w", err)
		}
		f.logger.Warn("dispatch: the event log was reset under the keyword fill's cursor; the fill emptied its table and starts again from the first event",
			"cursor", cursor, "newest", newest)
		return batchResult{reset: true}, nil
	}
	if safe > newest {
		return batchResult{oldHorizon: true}, nil
	}
	read, bytes, candidates, err := f.readEvents(ctx, tx, cursor, safe)
	if err != nil {
		return batchResult{}, err
	}
	if _, err := tx.Exec(ctx, batchStatementQuery, statementTimeout(bytes)); err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: bound the batch's statements: %w", err)
	}
	got, stop, err := f.decide(ctx, tx, read)
	if err != nil {
		return batchResult{}, err
	}
	read = read[:len(read)-stop.skipped]
	result := batchResult{
		events:      len(read),
		bytes:       bytes,
		rows:        len(got.Versions),
		cut:         got.Cut,
		cursor:      cursor,
		full:        stop.err == nil && (len(read) < candidates || candidates == f.maxEvents),
		storedReach: stored,
	}
	eventAt := cursorAt
	if len(read) > 0 {
		last := read[len(read)-1]
		result.first, result.cursor, eventAt = read[0].ID, last.ID, &last.CreatedAt
	}
	if err := insertVersions(ctx, tx, got.Versions); err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: %w", err)
	}
	reach, computed, err := store.ComputeTextVersionsReach(ctx, tx, result.cursor)
	if err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: %w", err)
	}
	var reachArg *time.Time
	if computed {
		reachArg = &reach
	}
	if err := tx.QueryRow(ctx, advanceCursorQuery, result.cursor, eventAt, reachArg, f.holderArg(), rulesVersion).
		Scan(&result.reach); err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: move the cursor to event %d: %w", result.cursor, err)
	}
	if stored != nil {
		for _, e := range read {
			if !e.CreatedAt.After(*stored) {
				result.breaches++
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return batchResult{}, fmt.Errorf("keyword fill: commit the batch: %w", err)
	}
	result.took = f.now().Sub(started)
	return result, stop.err
}

// lockCursor locks the cursor row for tx, creating it at no event where none is, and answers the
// cursor, the created_at of the event at it, and the reach stored. A row a reset emptied and wrote
// again while this lock waited is gone from the lock's snapshot, so it is read again.
func (f *filler) lockCursor(ctx context.Context, tx pgx.Tx) (cursor int64, cursorAt, reach *time.Time, err error) {
	for attempt := 0; ; attempt++ {
		err = tx.QueryRow(ctx, lockCursorQuery).Scan(&cursor, &cursorAt, &reach)
		switch {
		case err == nil:
			return cursor, cursorAt, reach, nil
		case !errors.Is(err, pgx.ErrNoRows) || attempt > 0:
			return 0, nil, nil, fmt.Errorf("keyword fill: lock the cursor: %w", err)
		}
		if _, err := tx.Exec(ctx, createCursorQuery, f.holderArg(), rulesVersion); err != nil {
			return 0, nil, nil, fmt.Errorf("keyword fill: create the cursor: %w", err)
		}
	}
}

// empty empties both tables in tx for a log that was reset, and writes the cursor row again at no
// event, with no reach (createCursorQuery).
func (f *filler) empty(ctx context.Context, tx pgx.Tx) error {
	for _, statement := range []string{emptyTableQuery, emptyCursorQuery} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("keyword fill: empty the table of a reset log: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, createCursorQuery, f.holderArg(), rulesVersion); err != nil {
		return fmt.Errorf("keyword fill: start the cursor of a reset log: %w", err)
	}
	return nil
}

// holderArg is the holder the cursor row records: this fill's, or null with no hold.
func (f *filler) holderArg() any {
	if f.holder == uuid.Nil {
		return nil
	}
	return f.holder
}

// statementTimeout is the statement_timeout of a batch that holds bytes of text: 10 s and 1 s
// more for each MB, as set_config takes it.
func statementTimeout(bytes int64) string {
	const mb = 1 << 20
	timeout := batchStatementBase + time.Duration((bytes+mb-1)/mb)*batchStatementPerMB
	return fmt.Sprintf("%dms", timeout.Milliseconds())
}

// readEvents reads the batch's events above cursor up to safe (readEventsQuery), with the bytes
// of text they hold and how many events the read took before its bound on bytes, so a batch cut
// short by its bytes is known to leave events for the next one.
func (f *filler) readEvents(ctx context.Context, tx pgx.Tx, cursor, safe int64) ([]event, int64, int, error) {
	types := slices.Sorted(maps.Keys(versionRules))
	rows, err := tx.Query(ctx, readEventsQuery, cursor, safe, f.maxEvents, f.maxBytes, types)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("keyword fill: read the events after event %d: %w", cursor, err)
	}
	defer rows.Close()
	var read []event
	var bytes int64
	var candidates int
	for rows.Next() {
		var e event
		var payload []byte
		var size, taken int64
		if err := rows.Scan(&e.ID, &e.Type, &e.IssueKey, &e.Project, &e.Seq, &e.CreatedAt, &payload, &size, &taken); err != nil {
			return nil, 0, 0, fmt.Errorf("keyword fill: read the events after event %d: %w", cursor, err)
		}
		e.Payload = payload
		read = append(read, e)
		bytes += size
		candidates = int(taken)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("keyword fill: read the events after event %d: %w", cursor, err)
	}
	return read, bytes, candidates, nil
}

// stopped is an event the rules cannot read: the error naming it, and how many events of the
// batch, it and those after it, are left unwritten.
type stopped struct {
	err     error
	skipped int
}

// decide decides the versions read starts (decideVersions). A payload the rules cannot read is
// met first by the rules' own read of each event in id order, which reads every field any rule
// can find missing, so the event it stops at is the first event versionOf cannot read: the
// versions of the events before it are decided again, with the error kept for the batch to answer
// once they are written.
func (f *filler) decide(ctx context.Context, tx pgx.Tx, read []event) (decided, stopped, error) {
	got, err := decideVersions(ctx, tx, f.logger, read)
	if err == nil || !errors.Is(err, errUnreadablePayload) {
		return got, stopped{}, err
	}
	at := slices.IndexFunc(read, func(e event) bool {
		_, _, readErr := versionOf(e)
		return errors.Is(readErr, errUnreadablePayload)
	})
	if at < 0 {
		return decided{}, stopped{}, err
	}
	stop := stopped{err: fmt.Errorf("keyword fill: the fill stops at event %d: %w", read[at].ID, err), skipped: len(read) - at}
	if at == 0 {
		return decided{}, stop, nil
	}
	got, err = decideVersions(ctx, tx, f.logger, read[:at])
	return got, stop, err
}

// record logs a batch that wrote events: a debug line for each batch, which a measurement reads;
// a progress line at most each progressEvery while the fill catches up, with what the batches
// since the last one wrote; and an error for events written at or before the reach stored before,
// each a breach of what the reach said.
func (f *filler) record(r batchResult) {
	f.logger.Debug("dispatch: the keyword fill wrote a batch",
		"events", r.events, "first_event", r.first, "cursor", r.cursor, "rows", r.rows, "cut_bodies", r.cut,
		"bytes", r.bytes, "reach", r.reach, "batch_ms", r.took.Milliseconds())
	now := f.now()
	if f.progress.since.IsZero() {
		f.progress.since = now
	}
	f.progress.events += r.events
	f.progress.rows += r.rows
	f.progress.cut += r.cut
	if r.full && now.Sub(f.progress.since) >= progressEvery {
		f.logger.Info("dispatch: the keyword fill is catching up",
			"events", f.progress.events, "rows", f.progress.rows, "cut_bodies", f.progress.cut,
			"cursor", r.cursor, "reach", r.reach, "batch_ms", r.took.Milliseconds())
		f.progress = progress{since: now}
	}
	if r.breaches > 0 {
		f.breaches += r.breaches
		f.logger.Error("dispatch: the keyword fill wrote events whose created_at is at or before the reach it had stored; a search at a cut up to that reach missed their versions",
			"breaches", r.breaches, "breaches_total", f.breaches, "stored_reach", r.storedReach,
			"first_event", r.first, "cursor", r.cursor)
	}
}
