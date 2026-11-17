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

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The registry's writes and the refresher's record their changes in the change log's outbox
// (delivery_changes_pending, migration 20261115155340) and take no change log lock: each write's
// drain moves them into the log in a short transaction of its own after it commits
// (delivery.DrainPending). Before that, each took the change log's lock (6900) in a trigger in the
// middle of its transaction, and then each took it first, before any other lock; in the first
// order two writers waited on each other in a cycle. The tests below run two writers at once in
// the orders a review traced to such a deadlock, each through the call its route or the refresher
// makes, with the first writer paused, by a trigger of the test's own database, at the step after
// which the old order waited on the second.

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
// lockOrderBound, so a wait that never ends answers the deadline.
func race(t *testing.T, holder pgx.Tx, first, second func(ctx context.Context) error) (error, error) {
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
