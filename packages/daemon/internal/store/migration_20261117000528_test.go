package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// stepRecordKindVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const stepRecordKindVersion = 20261117000528

// A step record is the row that writes one step of an issue onto Dispatch (record.StepRecord). The
// outbox refuses a kind its constraint does not name, so the row could not be queued before this
// migration, and the transaction that started or ended the step would fail with it: the negative
// control is that refusal, read before the migration, on a database that already carries the
// merge-queue kinds' list (20261114031356), which this version stands above. After it the row is
// queued, and so is every kind the constraint already named, since the migration rewrites the list
// whole and a kind left out would stop its own effect. A kind that is none is still refused, with
// the check's own SQLSTATE. The rows the release before it left pending — the merge-queue kinds
// its list added among them, each on an issue and with a payload of its own — come through the
// migration whole: the same id, kind, issue, payload, attempts, due time and error.
func TestTheOutboxCheckTakesTheStepRecord(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, stepRecordKindVersion-1)
	queue := func(kind string) error {
		_, err := store.Pool().Exec(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
			values ($1, 'LEGION-208', '{}', 0, now(), '')`, kind)
		return err
	}
	refusedByTheCheck := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "outbox_kind_check"
	}
	if err := queue("step_record"); !refusedByTheCheck(err) {
		t.Fatalf("queueing a step_record row before the migration = %v, want the kind constraint to refuse it (23514): it is the row the migration exists for", err)
	}
	type pending struct {
		id          int64
		kind, issue string
		payload     string
		attempts    int
		nextAt      time.Time
		lastError   string
	}
	readPending := func() []pending {
		t.Helper()
		rows, err := store.Pool().Query(ctx, `select id, kind, issue, payload::text, attempts, next_at, last_error from outbox order by id`)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		defer rows.Close()
		var all []pending
		for rows.Next() {
			var row pending
			if err := rows.Scan(&row.id, &row.kind, &row.issue, &row.payload, &row.attempts, &row.nextAt, &row.lastError); err != nil {
				t.Fatalf("scan the outbox: %v", err)
			}
			all = append(all, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		return all
	}
	for i, kind := range []string{"merge_queue_removal", "merge_queue_read", "update_ask", "supervise", "dispatch_status"} {
		if _, err := store.Pool().Exec(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
			values ($1, $2, jsonb_build_object('pending', $1::text), $3, now() + make_interval(mins => $3), $4)`,
			kind, fmt.Sprintf("LEGION-%d", 300+i), i, fmt.Sprintf("attempt %d failed", i)); err != nil {
			t.Fatalf("queue a pending %s row under the release before: %v", kind, err)
		}
	}
	before := readPending()
	if len(before) != 5 {
		t.Fatalf("the outbox holds %d rows before the migration, want the five pending rows of the release before", len(before))
	}
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the step record's kind: %v", err)
	}
	if after := readPending(); !reflect.DeepEqual(after, before) {
		t.Fatalf("the pending rows after the migration = %+v, want the release before's rows whole: %+v", after, before)
	}
	for _, kind := range []string{
		"dispatch_status", "dispatch_message", "notice", "controller_notice", "supervise", "gate_seed",
		"linger_close", "workspace_remove", "issue_branch", "issue_suspend", "check_wake",
		"check_look", "check_bound", "role_message", "phase_ended", "issue_bundle", "issue_release",
		"gated_steps_publish", "claim_release", "hook_prompt", "design_gates_publish", "update_ask",
		"merge_queue_read", "merge_queue_removal", "step_record",
	} {
		if err := queue(kind); err != nil {
			t.Fatalf("queue a %s row after the migration: %v", kind, err)
		}
	}
	if err := queue("nope"); !refusedByTheCheck(err) {
		t.Fatalf("queueing a row of a kind that is none = %v, want the constraint to refuse it (23514)", err)
	}
	if err := queue("ready_merge"); !refusedByTheCheck(err) {
		t.Fatalf("queueing a ready_merge row = %v, want the constraint to refuse it: the merge lane's kinds went with the merge the daemon no longer submits", err)
	}
}
