package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The registry's writes and the refresher's record their changes in the change log's outbox
// (delivery_changes_pending, migration 20261117100001) and take no change log lock: each write's
// drain moves a batch of them into the log in a short transaction of its own after it commits
// (delivery.DrainPendingBatch). Before that, each took the change log's lock (6900) in a trigger
// in the middle of its transaction, and then each took it first, before any other lock; in the
// first order two writers waited on each other in a cycle. The tests below run two writers at once
// in the orders a review traced to such a deadlock, each through the call its route or the
// refresher makes, with the first writer paused, by a trigger of the test's own database, at the
// step after which the old order waited on the second.

// pauseKey is the advisory lock a paused insert waits for (pauseInserts).
const pauseKey = int64(6900_4815)

// lockOrderBound is how long a test waits for a writer before it counts the wait as stuck.
const lockOrderBound = time.Minute

// pauseInserts makes every insert into table of a row when holds wait, before the row is written,
// for a lock the answered transaction holds: the test commits it to let the insert on. The
// trigger is the test database's alone.
func pauseInserts(t *testing.T, r rig, table, when string) pgx.Tx {
	t.Helper()
	ctx := t.Context()
	for _, statement := range []string{
		`create or replace function evals_test_pause() returns trigger language plpgsql as $$
		begin
			perform pg_advisory_xact_lock(tg_argv[0]::bigint);
			return new;
		end $$`,
		fmt.Sprintf(`create trigger evals_test_pause_%s before insert on %s for each row when (%s)
			execute function evals_test_pause('%d')`, table, table, when, pauseKey),
	} {
		if _, err := r.database.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("pause the inserts into %s: %v", table, err)
		}
	}
	holder, err := r.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the pause: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if _, err := holder.Exec(ctx, `select pg_advisory_xact_lock($1)`, pauseKey); err != nil {
		t.Fatalf("take the pause's lock: %v", err)
	}
	return holder
}

// race runs first until it waits on the pause holder holds, then second until it waits too,
// behind the pause or behind first, or answers, then lets first on and answers both writers'
// errors. A second writer that needs no lock first holds answers at once: in the old orders it
// waited on the change log's lock, which no eval writer takes any more. Each writer runs under
// lockOrderBound, so a wait that never ends answers the deadline. Each of whileWaiting runs once
// the second writer waits or answered, before first is let on: what a test observes of the two
// writers' waits.
func race(t *testing.T, holder pgx.Tx, first, second func(ctx context.Context) error, whileWaiting ...func()) (error, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), lockOrderBound)
	defer cancel()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { firstDone <- first(ctx) }()
	if waiting := storetest.WaitForLockWaiters(t, holder, 1, lockOrderBound); waiting < 1 {
		t.Fatalf("the first writer never reached the pause")
	}
	go func() { secondDone <- second(ctx) }()
	var secondErr error
	secondAnswered := false
	for deadline := time.Now().Add(lockOrderBound); ; {
		select {
		case secondErr = <-secondDone:
			secondAnswered = true
		default:
		}
		if secondAnswered || storetest.WaitForLockWaiters(t, holder, 2, 10*time.Millisecond) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second writer neither waited, behind the pause or the first writer, nor answered")
		}
	}
	for _, observe := range whileWaiting {
		observe()
	}
	if err := holder.Commit(t.Context()); err != nil {
		t.Fatalf("let the first writer on: %v", err)
	}
	if !secondAnswered {
		secondErr = <-secondDone
	}
	return <-firstDone, secondErr
}

// requireNoDeadlock fails the test where err is not nil, saying where it is Postgres's deadlock.
func requireNoDeadlock(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "40P01":
		t.Errorf("%s deadlocked (SQLSTATE 40P01): %v", what, err)
	case err != nil:
		t.Errorf("%s: %v", what, err)
	}
}

// TestAReadingsWriteAndTheZeroFillOfItsDayNeverDeadlock: a reading of a logged eval on a day that
// holds no row of its series yet, and the refresher's fill of that eval's zero rows through the
// same day, at once. The readings write is paused at its first day row's insert, which in the old
// order came after its readings' insert had taken the change log's lock; the fill then inserted
// the day's zero row and waited for that lock, and the readings write, let on, waited for the
// fill's uncommitted row: a deadlock. Both now land: the day holds the reading, and the days around
// it zero rows.
func TestAReadingsWriteAndTheZeroFillOfItsDayNeverDeadlock(t *testing.T) {
	r := newRig(t)
	slug := "fill-race"
	r.write(t, slug, alice, row(t, map[string]any{"series": "minutes", "row_kind": "run", "row_key": "a", "value": 2, "measured_at": at(4, 9, 0)}))
	holder := pauseInserts(t, r, "eval_days", "new.built_by = 'readings-race'")
	readingsErr, fillErr := race(t, holder,
		func(ctx context.Context) error {
			_, err := r.registry.WriteReadings(ctx, Batch{Project: r.project, Slug: slug, Caller: alice, BuiltBy: "readings-race",
				Rows: []json.RawMessage{row(t, map[string]any{"series": "minutes", "row_kind": "run", "row_key": "b", "value": 5, "measured_at": at(6, 9, 0)})}})
			return err
		},
		func(ctx context.Context) error {
			return r.registry.newRefresh("fill-race", at(7, 12, 0), walkBatch).fillLogged(ctx, r.project, nil)
		})
	requireNoDeadlock(t, "the readings write", readingsErr)
	requireNoDeadlock(t, "the zero fill", fillErr)
	if t.Failed() {
		return
	}
	evalID := EvalID(r.project, slug)
	for _, want := range []struct {
		day time.Time
		n   int
	}{{at(4, 0, 0), 1}, {at(5, 0, 0), 0}, {at(6, 0, 0), 1}, {at(7, 0, 0), 0}} {
		if got := r.day(t, evalID, "minutes", "", want.day); got.N != want.n {
			t.Errorf("the row of %s holds n %d, want %d", want.day.Format(time.DateOnly), got.N, want.n)
		}
	}
}

// collateSeriesAsADeploymentDoes holds eval_days' series under ICU's en-US collation, which is
// what this deployment's server collates text by (prioritystore's rankKey), so the test sees two
// orders apart whatever collation the server it runs against was created with: under it `latency`
// sorts before `Queue`, and their bytes put `Queue` first.
func collateSeriesAsADeploymentDoes(t *testing.T, r rig) {
	t.Helper()
	ctx := t.Context()
	// A PostgreSQL built with ICU carries ICU collations of its own and one built without ICU
	// carries none, so this count is the server's own answer to whether the collation below can
	// be made. An error from the read itself ends the test.
	var icu bool
	if err := r.database.Pool.QueryRow(ctx, `select count(*) > 0 from pg_collation where collprovider = 'i'`).Scan(&icu); err != nil {
		t.Fatalf("ask whether this Postgres was built with ICU: %v", err)
	}
	if !icu {
		t.Skip("a PostgreSQL built with ICU must serve this test: ICU is what holds two series names in an order their bytes do not follow")
	}
	for _, statement := range []string{
		`create collation deployment_text (provider = icu, locale = 'en-US')`,
		`alter table eval_days alter column series type text collate deployment_text`,
	} {
		// The server has ICU, so a statement it refuses here is a permission or a schema problem
		// rather than an absence to pass over, and the test says so rather than skips.
		if _, err := r.database.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("hold the series under a deployment's own collation (%s): %v", statement, err)
		}
	}
	var apart bool
	if err := r.database.Pool.QueryRow(ctx, `
		select 'latency' collate deployment_text < 'Queue' collate deployment_text and 'Queue' collate "C" < 'latency' collate "C"
	`).Scan(&apart); err != nil {
		t.Fatalf("compare the two series under the collation and by bytes: %v", err)
	}
	if !apart {
		t.Fatal("this server's en-US collation does not sort `latency` before `Queue`, so the orders the test races would be one order")
	}
}

// TestAReadingsWriteAndTheZeroFillOfTwoSeriesNeverDeadlock: the race above over two series of one
// logged eval whose byte order and collation order differ, `Queue` and `latency`, held under the
// deployment's collation. Each has a row on the 4th and none after it; the readings write carries
// a reading of each on the 6th, and the fill writes each series' zero rows through the 7th. The
// readings write is paused at its insert of latency's row of the 6th, having inserted Queue's,
// since it takes the rows by their bytes.
//
// It pins that the two never deadlock, by either of two guards. Every writer of an eval's day rows
// takes the eval's lock first (lockEval, lockEvals), so the fill waits for the readings write to
// commit before it reads or inserts a row; and every statement that inserts or locks those rows
// takes them in the byte order of their keys (writeDays), so two writers that did run side by side
// would take them in one order. With the lock taken the second guard is never reached, so the
// deadlock this test looks for comes back only once both are gone: the fill, taking its rows in
// the collation's order, inserted latency's rows of the 5th to the 7th, then Queue's of the 5th,
// and waited for the readings write's uncommitted Queue row of the 6th, and the readings write,
// let on, waited for the fill's latency row of the 6th. What it observes of the first guard alone
// is that the fill waited on the eval's advisory lock while the paused readings write held it
// (requireQueuedOnTheEvalLock), which fails once the fill stops taking the lock. Both land: each
// series holds its readings on the 4th and the 6th and zero rows on the 5th and the 7th.
func TestAReadingsWriteAndTheZeroFillOfTwoSeriesNeverDeadlock(t *testing.T) {
	r := newRig(t)
	collateSeriesAsADeploymentDoes(t, r)
	slug := "fill-order"
	evalID := EvalID(r.project, slug)
	reading := func(series, key string, value float64, day int) json.RawMessage {
		return row(t, map[string]any{"series": series, "row_kind": "run", "row_key": key, "value": value, "measured_at": at(day, 9, 0)})
	}
	r.write(t, slug, alice, reading("Queue", "a", 1, 4), reading("latency", "a", 2, 4))
	holder := pauseInserts(t, r, "eval_days", "new.built_by = 'readings-race' and new.series = 'latency'")
	readingsErr, fillErr := race(t, holder,
		func(ctx context.Context) error {
			_, err := r.registry.WriteReadings(ctx, Batch{Project: r.project, Slug: slug, Caller: alice, BuiltBy: "readings-race",
				Rows: []json.RawMessage{reading("Queue", "b", 3, 6), reading("latency", "b", 4, 6)}})
			return err
		},
		func(ctx context.Context) error {
			return r.registry.newRefresh("fill-race", at(7, 12, 0), walkBatch).fillLogged(ctx, r.project, nil)
		},
		func() { requireQueuedOnTheEvalLock(t, holder, evalID, "the zero fill") })
	requireNoDeadlock(t, "the readings write", readingsErr)
	requireNoDeadlock(t, "the zero fill", fillErr)
	if t.Failed() {
		return
	}
	for _, series := range []string{"Queue", "latency"} {
		for _, want := range []struct {
			day time.Time
			n   int
		}{{at(4, 0, 0), 1}, {at(5, 0, 0), 0}, {at(6, 0, 0), 1}, {at(7, 0, 0), 0}} {
			if got := r.day(t, evalID, series, "", want.day); got.N != want.n {
				t.Errorf("the row of %s on %s holds n %d, want %d", series, want.day.Format(time.DateOnly), got.N, want.n)
			}
		}
	}
}

// requireQueuedOnTheEvalLock fails the test unless a backend waits, not granted, on the advisory
// lock of evalID (the key lockEvals takes, hashtext('eval:' || id) as a bigint, which pg_locks
// shows as its high half in classid, its low half in objid and objsubid 1) behind a backend that
// holds it and itself waits behind holder: a race's second writer, what names, queued on the eval's
// lock behind the paused first. It reads pg_locks through holder's own connection, as
// storetest.WaitForLockWaiters does.
func requireQueuedOnTheEvalLock(t *testing.T, holder pgx.Tx, evalID, what string) {
	t.Helper()
	var queued bool
	if err := holder.QueryRow(context.Background(), `
		select exists (
			select 1
			from pg_locks waiter
			join pg_locks held on held.locktype = waiter.locktype and held.database = waiter.database
			  and held.classid = waiter.classid and held.objid = waiter.objid and held.objsubid = waiter.objsubid
			where waiter.locktype = 'advisory' and not waiter.granted and held.granted
			  and waiter.classid = ((hashtext('eval:' || $1)::bigint >> 32) & 4294967295)::oid
			  and waiter.objid = (hashtext('eval:' || $1)::bigint & 4294967295)::oid
			  and waiter.objsubid = 1
			  and held.pid = any(pg_blocking_pids(waiter.pid))
			  and pg_backend_pid() = any(pg_blocking_pids(held.pid)))
	`, evalID).Scan(&queued); err != nil {
		t.Fatalf("read the waits on the lock of %s: %v", evalID, err)
	}
	if !queued {
		t.Errorf("%s did not wait on the advisory lock of %s while the paused writer held it", what, evalID)
	}
}

// A register write that lists or unlists a logged eval's series writes the new listing into every
// day row the series holds (relistDays), and every other writer of those rows takes the eval's
// lock before it reads the listing it writes into them, so none writes the listing the register
// write replaced once that write has committed. The two tests below pause such a writer after its
// read of the listing while a register write unlists the series. Before, the paused writer held no
// lock of the eval: the register write unlisted the stored rows and committed beside it, and the
// writer, let on, wrote the old listing into the rows it wrote, which a window then folded.

// listDepth is a register write that lists or unlists the depth series of slug.
func listDepth(r rig, slug string, listed bool) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := r.registry.Register(ctx, Registration{Project: r.project, Slug: slug, Caller: alice,
			Series: []SeriesChange{{Series: "depth", Listed: Set(listed)}}})
		return err
	}
}

// requireDepthRows fails the test unless the depth series of slug holds want day rows, every one
// of them unlisted.
func requireDepthRows(t *testing.T, r rig, slug string, want int) {
	t.Helper()
	evalID := EvalID(r.project, slug)
	rows := r.count(t, `select count(*) from eval_days where eval_id = $1 and series = 'depth'`, evalID)
	listed := r.count(t, `select count(*) from eval_days where eval_id = $1 and series = 'depth' and listed`, evalID)
	if rows != want || listed != 0 {
		t.Errorf("depth holds %d day rows, %d of them listed, after the register write unlisted it; want %d, none listed", rows, listed, want)
	}
}

// TestARegisterWriteThatUnlistsASeriesBesideAReadingsWriteUnlistsEveryRow: a readings write of a
// listed series, one reading on a day that holds rows of it and one on a day that holds none, each
// counted in the project's row and its repository's, is paused at its readings' insert, after it
// read the series, while a register write unlists the series. The register write now waits for
// the readings write and then unlists every row, the ones the readings write made among them.
func TestARegisterWriteThatUnlistsASeriesBesideAReadingsWriteUnlistsEveryRow(t *testing.T) {
	r := newRig(t)
	slug := "queue-depth"
	depth := func(key string, day int) json.RawMessage {
		return row(t, map[string]any{"series": "depth", "row_kind": "queue", "row_key": key, "value": float64(day), "measured_at": at(day, 9, 0),
			"attributes": map[string]any{"repo": "acme/web"}})
	}
	r.write(t, slug, alice, depth("a", 4), depth("b", 5))
	holder := pauseInserts(t, r, "eval_readings", "new.row_key = 'paused'")
	readingsErr, registerErr := race(t, holder,
		func(ctx context.Context) error {
			_, err := r.registry.WriteReadings(ctx, Batch{Project: r.project, Slug: slug, Caller: alice, BuiltBy: testBuild,
				Rows: []json.RawMessage{depth("paused", 4), depth("c", 6)}})
			return err
		},
		listDepth(r, slug, false))
	requireNoDeadlock(t, "the readings write", readingsErr)
	requireNoDeadlock(t, "the register write", registerErr)
	if t.Failed() {
		return
	}
	requireDepthRows(t, r, slug, 6)
	if day := r.day(t, EvalID(r.project, slug), "depth", "", at(4, 0, 0)); day.N != 2 {
		t.Errorf("the 4th holds n %d, want both its readings", day.N)
	}
}

// TestARegisterWriteThatUnlistsASeriesBesideTheZeroFillUnlistsEveryRow: the refresher's fill of a
// listed series' zero rows through the 7th is paused at its first zero row's insert, after it read
// the listing of the series' newest row, while a register write unlists the series. The fill now
// holds the eval's lock from before that read, so the register write waits for it and then
// unlists the zero rows with the rest.
func TestARegisterWriteThatUnlistsASeriesBesideTheZeroFillUnlistsEveryRow(t *testing.T) {
	r := newRig(t)
	slug := "queue-depth"
	r.write(t, slug, alice, row(t, map[string]any{"series": "depth", "row_kind": "queue", "row_key": "a", "value": 4, "measured_at": at(4, 9, 0)}))
	holder := pauseInserts(t, r, "eval_days", "new.built_by = 'fill-race'")
	fillErr, registerErr := race(t, holder,
		func(ctx context.Context) error {
			return r.registry.newRefresh("fill-race", at(7, 12, 0), walkBatch).fillLogged(ctx, r.project, nil)
		},
		listDepth(r, slug, false))
	requireNoDeadlock(t, "the zero fill", fillErr)
	requireNoDeadlock(t, "the register write", registerErr)
	if t.Failed() {
		return
	}
	requireDepthRows(t, r, slug, 4)
}

// TestAFirstReadingAndARegisterWriteWithDocTextNeverDeadlock: a register write that replaces an
// eval's method and a first reading under another slug of the same project, at once. The register
// write is paused at its version's insert, after it locked the project's evals-md to write the
// method; in the old order the first reading had then taken the change log's lock with its own
// version 1 and waited for evals-md to write its section, and the register write, let on, waited
// for the change log's lock: a deadlock. Both now land, each section in evals-md.
func TestAFirstReadingAndARegisterWriteWithDocTextNeverDeadlock(t *testing.T) {
	r := newRig(t)
	if _, err := r.registry.Register(t.Context(), Registration{Project: r.project, Slug: "review-time", Caller: alice, Name: Set("Review time")}); err != nil {
		t.Fatalf("register review-time: %v", err)
	}
	holder := pauseInserts(t, r, "eval_versions", "new.written_via = 'register'")
	registerErr, readingErr := race(t, holder,
		func(ctx context.Context) error {
			_, err := r.registry.Register(ctx, Registration{Project: r.project, Slug: "review-time", Caller: alice, DocText: Set("Alice's method: time each review.")})
			return err
		},
		func(ctx context.Context) error {
			_, err := r.registry.WriteReadings(ctx, Batch{Project: r.project, Slug: "deploy-wait", Caller: alice, BuiltBy: testBuild,
				Rows: []json.RawMessage{row(t, map[string]any{"series": "minutes", "row_kind": "run", "row_key": "a", "value": 3, "measured_at": at(5, 9, 0)})}})
			return err
		})
	requireNoDeadlock(t, "the register write", registerErr)
	requireNoDeadlock(t, "the first reading", readingErr)
	if t.Failed() {
		return
	}
	text, _ := r.document(t)
	if headings(text, "review-time") != 1 || headings(text, "deploy-wait") != 1 || !strings.Contains(text, "Alice's method: time each review.") {
		t.Errorf("evals-md does not hold one section of each eval with alice's method:\n%s", text)
	}
}

// TestAFirstReadingAndTheSeedNeverDeadlock: the seed of a project whose evals-md lost one
// catalogue method block, and a first reading under a slug of the same project, at once. The seed
// is paused at its versions' insert, after it locked evals-md to add the missing section; in the
// old order the first reading had then taken the change log's lock with its version 1 and waited
// for evals-md, and the seed, let on, waited for the change log's lock: a deadlock. Both now land:
// deploy-frequency's newest version names the block the seed added, and deploy-wait has its
// section.
func TestAFirstReadingAndTheSeedNeverDeadlock(t *testing.T) {
	r := newRig(t)
	if _, err := r.registry.Ensure(t.Context(), r.project); err != nil {
		t.Fatalf("seed %s: %v", r.project, err)
	}
	definition, _ := r.currentOf(t, SlugDeployFrequency)
	r.deleteBlock(t, r.documentID(t, r.project), definition.DocBlock)
	holder := pauseInserts(t, r, "eval_versions", "new.written_via = 'seed'")
	seedErr, readingErr := race(t, holder,
		func(ctx context.Context) error {
			_, err := r.registry.Ensure(ctx, r.project)
			return err
		},
		func(ctx context.Context) error {
			_, err := r.registry.WriteReadings(ctx, Batch{Project: r.project, Slug: "deploy-wait", Caller: alice, BuiltBy: testBuild,
				Rows: []json.RawMessage{row(t, map[string]any{"series": "minutes", "row_kind": "run", "row_key": "a", "value": 3, "measured_at": at(5, 9, 0)})}})
			return err
		})
	requireNoDeadlock(t, "the seed", seedErr)
	requireNoDeadlock(t, "the first reading", readingErr)
	if t.Failed() {
		return
	}
	text, blocks := r.document(t)
	restored, _ := r.currentOf(t, SlugDeployFrequency)
	if !hasBlock(blocks, restored.DocBlock) || restored.DocBlock == definition.DocBlock || headings(text, "deploy-wait") != 1 {
		t.Errorf("deploy-frequency's doc_block %s is no block of evals-md, or is the deleted one, or deploy-wait has no one section:\n%s", restored.DocBlock, text)
	}
}

// TestFirstRegistrationsBesideAnEditOfEvalsMdNeitherDeadlockNorLoseASection: five first
// registrations of one project, three by a reading and two by a register write, and a person's
// edit of the project's evals-md, at once. A registration takes its eval's lock, queues behind the
// project's other registry writes of the document (lockDocumentWrites), stores its rows and then
// takes the document's locks in the docs package's order (the owner row, the writer slot, the
// advisory lock), and the edit takes the owner row first, as the edit route does
// (requireOpenOwnerStatus), then the rest in that same order. The first registration is paused
// inside its write of the document, at the insert of the document's version, holding all of them;
// the edit then waits on the owner row; and the four other registrations each wait on the
// registry's queue, holding their eval's lock and no transaction id, so none of them holds back
// the event horizon while it waits (requireQueuedWithNoTransactionID). Each writer runs on a
// context marked as a route marks its request (store.WithTransactionTracking), so a second
// connection of the pool asked for inside its transaction is refused, as the server refuses it.
// Let on, every write lands: evals-md holds the edit's paragraph and one section of each eval,
// which its doc_block names.
func TestFirstRegistrationsBesideAnEditOfEvalsMdNeitherDeadlockNorLoseASection(t *testing.T) {
	r := newRig(t)
	reading := func(slug string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			written, err := r.registry.WriteReadings(store.WithTransactionTracking(ctx), Batch{Project: r.project, Slug: slug, Caller: alice, BuiltBy: testBuild,
				Rows: []json.RawMessage{row(t, map[string]any{"series": "minutes", "row_kind": "run", "row_key": "a", "value": 3, "measured_at": at(5, 9, 0)})}})
			if err == nil && !written.Registered {
				err = fmt.Errorf("the first reading under %s registered no eval", slug)
			}
			return err
		}
	}
	register := func(slug string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := r.registry.Register(store.WithTransactionTracking(ctx), Registration{Project: r.project, Slug: slug, Caller: alice, Name: Set("Registered " + slug)})
			return err
		}
	}
	r.write(t, "deploy-wait", alice, row(t, map[string]any{"series": "minutes", "row_kind": "run", "row_key": "a", "value": 1, "measured_at": at(4, 9, 0)}))
	documentID := r.documentID(t, r.project)
	const note = "Alice's note: every eval of this project is measured on the main branch."
	edit := func(ctx context.Context) error {
		ctx = store.WithTransactionTracking(ctx)
		tx, err := r.database.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, `select true from artifacts where id = $1 and issue_key is null for no key update`, documentID); err != nil {
			return fmt.Errorf("lock the document's owner row: %w", err)
		}
		joined, ledger := r.documents.Join(ctx, tx)
		defer ledger.Discard()
		applied, err := r.documents.ApplyOps(joined, documentID, []model.EditOp{{Op: "insert", After: "end", Markdown: note}}, alice, nil)
		if err != nil {
			return err
		}
		if !applied.Changed {
			return errors.New("the edit changed nothing")
		}
		if _, err := r.documents.SnapshotVersion(joined, documentID, alice); err != nil {
			return err
		}
		return ledger.Commit(ctx)
	}
	queued := []struct {
		slug  string
		write func(ctx context.Context) error
	}{
		{"review-time", register("review-time")},
		{"build-minutes", reading("build-minutes")},
		{"merge-wait", register("merge-wait")},
		{"flaky-reruns", reading("flaky-reruns")},
	}
	answers := make(chan error, len(queued))
	holder := pauseInserts(t, r, "artifact_versions", "true")
	firstErr, editErr := race(t, holder, reading("cache-hits"), edit, func() {
		ctx, cancel := context.WithTimeout(t.Context(), lockOrderBound)
		t.Cleanup(cancel)
		for _, registration := range queued {
			go func() { answers <- registration.write(ctx) }()
		}
		if waiting := storetest.WaitForLockWaiters(t, holder, 2+len(queued), lockOrderBound); waiting < 2+len(queued) {
			t.Errorf("%d writers wait behind the paused registration, want the edit and the %d registrations", waiting, len(queued))
		}
		requireQueuedWithNoTransactionID(t, holder, r.project, len(queued))
	})
	requireNoDeadlock(t, "the paused registration", firstErr)
	requireNoDeadlock(t, "the edit", editErr)
	for range queued {
		requireNoDeadlock(t, "a queued registration", <-answers)
	}
	if t.Failed() {
		return
	}
	text, blocks := r.document(t)
	if !strings.Contains(text, note) {
		t.Errorf("evals-md lost the edit's paragraph:\n%s", text)
	}
	for _, slug := range []string{"deploy-wait", "cache-hits", "review-time", "build-minutes", "merge-wait", "flaky-reruns"} {
		definition, _ := r.currentOf(t, slug)
		if headings(text, slug) != 1 || !hasBlock(blocks, definition.DocBlock) {
			t.Errorf("evals-md holds %d sections of %s, and its doc_block %q names a block of it: %t; want one, naming one",
				headings(text, slug), slug, definition.DocBlock, hasBlock(blocks, definition.DocBlock))
		}
	}
}

// requireQueuedWithNoTransactionID fails the test unless want backends wait, not granted, on the
// registry's lock of project's evals-md (lockDocumentWrites: hashtext('evals-md:' || project) as a
// bigint, which pg_locks shows as requireQueuedOnTheEvalLock reads the eval's), each holding an
// advisory lock, its eval's (lockEval), and no transaction id: a backend that has one holds the
// lock on it, which pg_locks shows as a granted transactionid lock. It reads pg_locks through
// holder's own connection, as storetest.WaitForLockWaiters does, and only the test database's.
func requireQueuedWithNoTransactionID(t *testing.T, holder pgx.Tx, project string, want int) {
	t.Helper()
	var queued, withID, withNoEvalLock int
	if err := holder.QueryRow(context.Background(), `
		select count(*),
			count(*) filter (where exists (
				select 1 from pg_locks own where own.pid = waiter.pid and own.locktype = 'transactionid' and own.granted)),
			count(*) filter (where not exists (
				select 1 from pg_locks own where own.pid = waiter.pid and own.locktype = 'advisory' and own.granted))
		from pg_locks waiter
		where waiter.locktype = 'advisory' and not waiter.granted
		  and waiter.database = (select oid from pg_database where datname = current_database())
		  and waiter.classid = ((hashtext('evals-md:' || $1)::bigint >> 32) & 4294967295)::oid
		  and waiter.objid = (hashtext('evals-md:' || $1)::bigint & 4294967295)::oid
		  and waiter.objsubid = 1
	`, project).Scan(&queued, &withID, &withNoEvalLock); err != nil {
		t.Fatalf("read the waits on the registry's lock of %s's evals-md: %v", project, err)
	}
	if queued != want || withID != 0 || withNoEvalLock != 0 {
		t.Errorf("%d writes wait on the registry's lock of %s's evals-md, %d of them holding a transaction id and %d no eval lock; want %d, none holding an id, each its eval's lock",
			queued, project, withID, withNoEvalLock, want)
	}
}
