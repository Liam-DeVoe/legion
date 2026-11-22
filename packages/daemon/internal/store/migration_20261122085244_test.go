package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// pendingOpeningVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const pendingOpeningVersion = 20261122085244

// pendingOpeningRelease names the release whose way back the migration's header carries, as a
// failure to read that block names it.
const pendingOpeningRelease = "the release that marks an opening's task on a claim's pending delivery"

// 20261122085244 adds pending_task_deliveries.opening, the work hold's opening sentences a claim's
// pending task begins with (supervise.Delivery.Opening). A delivery the release before wrote takes
// the default, the empty string, and reads back as a task whose work is its whole text. A delivery
// this release writes keeps its sentences, and its work reads back without them. The release
// before's upsert, which names no opening, keeps the sentences of the delivery it replaces, and
// the task it writes reads back as its whole text all the same (supervise.Delivery.Work). The way
// back, the last paragraph of the migration's header (migrations.WayBack), drops the column and
// deletes the migration's own version row, and leaves every column the release before reads and
// writes as it was. The roll-forward runs the migration again, and each delivery takes the empty
// string once more.
func TestThePendingDeliveryOpeningAndItsWayBackLeaveWhatTheReleaseBeforeReads(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, pendingOpeningVersion-1)
	refusedAs := func(err error, code string) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == code
	}
	const opening = `select opening from pending_task_deliveries`
	if _, err := store.Pool().Exec(ctx, opening); !refusedAs(err, "42703") {
		t.Fatalf("the database the release before left read pending_task_deliveries.opening = %v, want no such column (42703): the migration would prove nothing", err)
	}
	c := tmuxClaim("legion-LEGION-209-implementer")
	if err := store.PutClaim(ctx, c); err != nil {
		t.Fatalf("put the claim: %v", err)
	}
	// The release before's own write of a delivery, whose upsert names no opening.
	const releaseBeforesWrite = `insert into pending_task_deliveries (claim_token, delivery_id, task, phase, generation, queued_at)
		values ($1, $2, $3, 'implementing', 4, now())
		on conflict (claim_token) do update set delivery_id = excluded.delivery_id, task = excluded.task,
		phase = excluded.phase, generation = excluded.generation, queued_at = excluded.queued_at`
	const resumeTask = "Continue. Phase: implementing."
	if _, err := store.Pool().Exec(ctx, releaseBeforesWrite, string(c.Token), "outbox:7", resumeTask); err != nil {
		t.Fatalf("write the release before's delivery: %v", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the pending delivery's opening: %v", err)
	}
	readBack := func(when, id, wantOpening, wantWork string) {
		t.Helper()
		got := onlyClaim(t, store).Pending
		if got == nil || got.ID != id || got.Opening != wantOpening || got.Work() != wantWork {
			t.Fatalf("%s the delivery reads back %+v with the work %q, want %s marked %q with the work %q", when, got, workOf(got), id, wantOpening, wantWork)
		}
	}
	readBack("after the migration", "outbox:7", "", resumeTask)

	const holdOpening = "The work hold of LEGION-208's tree has ended. Check what your turn already did: a tool call may not have finished."
	const work = "Implement entry 2 of LEGION-209."
	if err := store.PutDelivery(ctx, c.Token, supervise.Delivery{ID: "outbox:8", Task: supervise.OpeningTask(holdOpening, work),
		Opening: holdOpening, Phase: "implementing", Generation: 4, QueuedAt: at(1)}); err != nil {
		t.Fatalf("put the opening's delivery: %v", err)
	}
	readBack("after this release's write", "outbox:8", holdOpening, work)
	if _, err := store.Pool().Exec(ctx, releaseBeforesWrite, string(c.Token), "outbox:9", resumeTask); err != nil {
		t.Fatalf("write the release before's delivery over the opening's: %v", err)
	}
	readBack("after the release before's write over it", "outbox:9", holdOpening, resumeTask)

	const releaseBeforesColumns = `select row(claim_token, delivery_id, task, phase, generation)::text from pending_task_deliveries order by claim_token`
	kept := rowsAsText(t, store, releaseBeforesColumns)
	if _, err := store.Pool().Exec(ctx, wayBackOf(t, pendingOpeningVersion, pendingOpeningRelease)); err != nil {
		t.Fatalf("run the way back of %d: %v", pendingOpeningVersion, err)
	}
	if _, err := store.Pool().Exec(ctx, opening); !refusedAs(err, "42703") {
		t.Fatalf("after the way back pending_task_deliveries.opening = %v, want no such column (42703)", err)
	}
	if after := rowsAsText(t, store, releaseBeforesColumns); !reflect.DeepEqual(after, kept) {
		t.Fatalf("after the way back the deliveries read %v, want the columns the release before reads as they were, %v", after, kept)
	}
	var recorded bool
	if err := store.Pool().QueryRow(ctx, `select exists (select 1 from schema_version where version = $1)`,
		pendingOpeningVersion).Scan(&recorded); err != nil || recorded {
		t.Fatalf("the way back left the version row recorded = %v (%v), want it deleted", recorded, err)
	}
	if _, err := store.Pool().Exec(ctx, releaseBeforesWrite, string(c.Token), "outbox:10", resumeTask); err != nil {
		t.Fatalf("after the way back the release before's write = %v, want it to write", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate forward again: %v", err)
	}
	readBack("after the roll-forward", "outbox:10", "", resumeTask)
}

// workOf is a delivery's work for a failure to print, "" for no delivery.
func workOf(d *supervise.Delivery) string {
	if d == nil {
		return ""
	}
	return d.Work()
}
