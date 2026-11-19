package api

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/testwait"
)

// fingerprint is one read of every table of a server's database (databaseFingerprint): each
// table's row count and the md5 of its rows, by table name; when its reads ended; and the next
// background write the wait before them left armed (quiet), which a fingerprint read after
// quiet.At cannot tell from the write of a request between the two.
type fingerprint struct {
	tables map[string]string
	ended  time.Time
	quiet  docs.BackgroundQuiet
}

// backgroundBound is how long one wait for a server's background writes, with the reads of the
// fingerprint after it, may take before it fails naming what had not ended; less when the test
// binary's deadline, less binaryDeadlineMargin, comes first (backgroundContext).
const backgroundBound = time.Minute

// backgroundSoon is how far ahead the wait looks for a write armed to start later: a document
// settlement armed to run within it is waited for, and one armed for after it is left armed and
// recorded with the moment it may start (docs.BackgroundQuiet), so a test whose document
// settlement is an hour away does not wait the hour.
const backgroundSoon = 10 * time.Second

// databaseFingerprint hashes every row of every table of served's database, so a test can tell a
// request wrote nothing anywhere: no row inserted, updated or deleted. It first waits out every
// write the server's earlier requests left to its background workers (backgroundWrites): one that
// commits between two fingerprints would read as the write of the request between them. It takes
// the server rather than the store so that no fingerprint can be taken without that wait. A write
// the wait leaves armed for later is recorded with the moment it may start (fingerprint.quiet),
// and assertUnchanged fails by name when the second fingerprint read on past that moment. The wait
// and the reads share one bound (backgroundContext), so a writer that never ends, a pool with no
// connection free or a lock that holds a read fails the test by name before the binary's deadline.
func databaseFingerprint(t *testing.T, served *server) fingerprint {
	t.Helper()
	return takeFingerprint(t, served, nil)
}

// takeFingerprint is databaseFingerprint, calling waiting, when it is not nil, as its wait for
// the server's background writes begins: the tests of that wait (database_fingerprint_wait_test.go)
// release there a writer they hold, so a fingerprint that took no wait reads while it is held.
func takeFingerprint(t *testing.T, served *server, waiting func()) fingerprint {
	t.Helper()
	ctx, cancel := backgroundContext(t)
	defer cancel()
	quiet := backgroundWrites(t, ctx, served, waiting)
	database := served.deps.Store
	rows, err := database.Pool.Query(ctx, `select tablename from pg_tables where schemaname = 'public'`)
	if err != nil {
		t.Fatalf("list tables: %v", boundedError(ctx, err))
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read table names: %v", boundedError(ctx, err))
	}
	if len(tables) == 0 {
		t.Fatal("the schema has no tables; a fingerprint of it proves nothing")
	}
	taken := fingerprint{tables: make(map[string]string, len(tables)), quiet: quiet}
	for _, table := range tables {
		var digest string
		if err := database.Pool.QueryRow(ctx, fmt.Sprintf(
			`select count(*) || ':' || coalesce(md5(string_agg(t::text, ',' order by t::text)), '') from %s t`,
			pgx.Identifier{table}.Sanitize(),
		)).Scan(&digest); err != nil {
			t.Fatalf("fingerprint %s: %v", table, boundedError(ctx, err))
		}
		taken.tables[table] = digest
	}
	taken.ended = time.Now()
	if !quiet.At.IsZero() && !taken.ended.Before(quiet.At) {
		t.Fatalf("%s could write from %s, before this fingerprint's reads ended at %s, so the fingerprint is not one state of the database",
			quiet.Writer, quiet.At.Format(time.RFC3339Nano), taken.ended.Format(time.RFC3339Nano))
	}
	return taken
}

// assertUnchanged fails for each table whose rows differ between two fingerprints of one database,
// and fails by name when a background write the first fingerprint's wait left armed could start
// before the second's reads ended: a table it changed would read as the request's.
func assertUnchanged(t *testing.T, request string, before, after fingerprint) {
	t.Helper()
	if !before.quiet.At.IsZero() && !after.ended.Before(before.quiet.At) {
		t.Errorf("%s: %s could write from %s, before the second fingerprint's reads ended at %s, so a table it changed cannot be told from the request's",
			request, before.quiet.Writer, before.quiet.At.Format(time.RFC3339Nano), after.ended.Format(time.RFC3339Nano))
	}
	for _, table := range slices.Sorted(maps.Keys(after.tables)) {
		if before.tables[table] != after.tables[table] {
			t.Errorf("%s changed table %s", request, table)
		}
	}
}

// awaitBackgroundWrites waits for every write srv's earlier requests left to its background
// workers (backgroundWrites), under backgroundContext's bound.
func awaitBackgroundWrites(t *testing.T, srv *server) {
	t.Helper()
	ctx, cancel := backgroundContext(t)
	defer cancel()
	backgroundWrites(t, ctx, srv, nil)
}

// backgroundContext is the bound of one wait for a server's background writes and the reads after
// it: backgroundBound, or what the test binary's deadline leaves less binaryDeadlineMargin.
func backgroundContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	bound := testwait.Bounded(t, backgroundBound, binaryDeadlineMargin)
	if bound <= 0 {
		t.Fatalf("the test binary's deadline leaves no time, past binaryDeadlineMargin (%s), for a wait for the server's background writes: raise -timeout", binaryDeadlineMargin)
	}
	return context.WithTimeoutCause(context.Background(), bound, fmt.Errorf("the wait for the server's background writes and the fingerprint's reads ran past their bound of %s", bound))
}

// boundedError is err, naming the bound that ended it when ctx's bound did.
func boundedError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", context.Cause(ctx), err)
	}
	return err
}

// backgroundWrites waits, within ctx, for every write srv's earlier requests left to the server's
// background workers, which is every writer register starts beside the requests and every one a
// request starts without waiting for it:
//
//   - srv.detached: every keyed handler, every delivery a request did not wait for (a broadcast's
//     recipients, a going run), a related-item check's pick, meaning stage and cross-project leg,
//     the term-statistics refresh a pick starts or joins, a display-tree store, a related record's
//     retry and the route roster fetch;
//   - the mention sender (srv.mentions): every send it holds, queued or in flight, each of which
//     writes its attempt's row and the comment's events when it settles, and the claims it writes
//     again while one waits;
//   - the document service behind srv (docs.Service.AwaitBackgroundWrites): its settlements, running or
//     armed to run before backgroundSoon from now, and the rest of its background writes.
//
// One writer can start another - a detached run that writes a document arms its settlement - so it
// passes over all of them again after any wait, and returns only from a pass that waited for none.
// It answers the next write the document service left armed for later. It refuses, by name, a
// server whose delivery hub runs: a view the hub reads again may record a deploy run's warning
// (firstToWarn, delivery_run_warnings) at any moment, and nothing counts those reads. The other
// goroutines register starts write no row: the event stream relay and the hub's listener read, the
// boot mark consumes a transaction id and stores nothing (events.Horizon.Mark), the test hooks'
// fanout answers each call before its request does, and a socket's heartbeat resolves its caller
// again, which under the test server's header identity is an insert that conflicts and does
// nothing (store.PgPeopleStore.Record).
func backgroundWrites(t *testing.T, ctx context.Context, srv *server, waiting func()) docs.BackgroundQuiet {
	t.Helper()
	if hub := srv.deliveryHub; hub != nil {
		hub.mu.Lock()
		running := hub.generation != nil
		hub.mu.Unlock()
		if running {
			t.Fatal("the server's delivery hub runs (a live socket subscribed to a delivery view): a view it reads again can record a deploy run's warning at any moment, and no wait sees those reads, so no fingerprint of this server can tell its writes from a request's")
		}
	}
	if waiting != nil {
		waiting()
	}
	for {
		waited := false
		if detachedRunning(&srv.detached) {
			waited = true
			awaitWait(t, ctx, "the server's detached work (server.detached)", srv.detached.wait)
		}
		if mentionSendsHeld(srv.mentions) > 0 {
			waited = true
			awaitCondition(t, ctx, "the mention sender's queued and running sends", func() bool { return mentionSendsHeld(srv.mentions) == 0 })
		}
		var quiet docs.BackgroundQuiet
		if srv.deps.Docs != nil {
			var err error
			if quiet, err = srv.deps.Docs.AwaitBackgroundWrites(ctx, time.Now().Add(backgroundSoon)); err != nil {
				t.Fatalf("wait for the document service's background writes: %v", boundedError(ctx, err))
			}
			waited = waited || quiet.Waited
		}
		if !waited {
			return quiet
		}
	}
}

// detachedRunning answers whether any of work runs now.
func detachedRunning(work *backgroundWork) bool {
	work.mu.Lock()
	defer work.mu.Unlock()
	return work.running > 0
}

// mentionSendsHeld is how many sends the mention sender holds, queued or in flight: an attempt
// leaves held only once its send has settled (mentionSender.next). A server a test builds directly
// (directServer) has no sender.
func mentionSendsHeld(sender *mentionSender) int {
	if sender == nil {
		return 0
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return len(sender.held)
}

// awaitWait runs wait, which returns once what it waits for has ended, failing the test naming
// what when ctx ends first.
func awaitWait(t *testing.T, ctx context.Context, what string, wait func()) {
	t.Helper()
	ended := make(chan struct{})
	go func() {
		wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatalf("%s did not end: %v", what, context.Cause(ctx))
	}
}

// awaitCondition asks done every testwait.Poll until it holds, failing the test naming what when
// ctx ends first.
func awaitCondition(t *testing.T, ctx context.Context, what string, done func() bool) {
	t.Helper()
	for !done() {
		select {
		case <-ctx.Done():
			if done() {
				return
			}
			t.Fatalf("%s did not end: %v", what, context.Cause(ctx))
		case <-time.After(testwait.Poll):
		}
	}
}
