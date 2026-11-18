package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// stateEventsVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const stateEventsVersion = 20261118164019

// A state event is an outbox row of one of three kinds (record.StateChange, StateAdmission,
// StateBoot). The outbox refuses a kind its constraint does not name, so before this migration
// the row could not be queued, and the write that queues it would fail with it: the negative
// control is that refusal, read before the migration, on a database that already carries the
// role-holders kind list (20261116165926), which this version stands above. After it the row is
// queued, and so is every kind the constraint already named, since the migration rewrites the
// list whole and a kind left out would stop its own effect. A kind that is none is still refused.
//
// The rest of the migration is the sequence the rows take at their commit: the three state columns,
// null on a row of any other kind and on every row the migration found, written on a state row
// when its transaction commits from the project's counter row (state_publication), with the
// unique index that holds a project to one row per sequence, and the table of the admission block
// last written. How the sequence follows commit order is record's
// TestStateEventsTakeTheirSequenceInCommitOrder.
func TestTheOutboxTakesTheStateKindsAndTheirSequenceWith20261118164019(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, stateEventsVersion-1)
	queue := func(kind, issue string) error {
		_, err := store.Pool().Exec(ctx, `insert into outbox (kind, issue, payload, attempts, next_at, last_error)
			values ($1, $2, '{}', 0, now(), '')`, kind, issue)
		return err
	}
	if err := queue("notice", "LEGION-208"); err != nil {
		t.Fatalf("queue a notice before the migration: %v", err)
	}
	err := queue("state_change", "LEGION")
	if err == nil || !strings.Contains(err.Error(), "outbox_kind_check") {
		t.Fatalf("queueing a state_change row before the migration = %v, want the kind constraint to refuse it: it is a row the migration exists for", err)
	}
	t.Logf("before the migration the state change is refused: %v", err)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the state events: %v", err)
	}

	kinds := []string{
		"dispatch_status", "dispatch_message", "notice", "controller_notice", "supervise", "gate_seed",
		"linger_close", "workspace_remove", "issue_branch", "issue_suspend", "check_wake",
		"check_look", "check_bound", "role_message", "phase_ended", "issue_bundle", "issue_release",
		"gated_steps_publish", "claim_release", "hook_prompt", "design_gates_publish", "update_ask",
		"merge_queue_read", "merge_queue_removal", "role_holders_publish", "state_change",
		"state_admission", "state_boot",
	}
	if len(kinds) != 28 {
		t.Fatalf("the list names %d kinds, want the 28 the daemon writes", len(kinds))
	}
	// A state row carries the bare project key as its issue; every other kind an issue key.
	for _, kind := range kinds {
		issue := "LEGION-208"
		if strings.HasPrefix(kind, "state_") {
			issue = "LEGION"
		}
		if err := queue(kind, issue); err != nil {
			t.Fatalf("queue a %s row after the migration: %v", kind, err)
		}
	}
	t.Logf("after the migration the outbox takes all 28 kinds, the three state kinds among them")
	if err := queue("quiesce", "LEGION-208"); err == nil || !strings.Contains(err.Error(), "outbox_kind_check") {
		t.Fatalf("queueing a row of a kind that is none = %v, want the constraint to refuse it", err)
	}
	if err := queue("ready_merge", "LEGION-208"); err == nil || !strings.Contains(err.Error(), "outbox_kind_check") {
		t.Fatalf("queueing a ready_merge row = %v, want the constraint to refuse it: the merge lane's kinds went with the merge the daemon no longer submits", err)
	}

	// Each insert above was a transaction of its own, so each state row took the next sequence at
	// its own commit, in the order they were queued, and the counter stands at the last.
	rows, err := store.Pool().Query(ctx, `select kind, state_seq from outbox
		where state_seq is not null and state_at is not null and state_tx is not null order by state_seq`)
	if err != nil {
		t.Fatalf("read the sequenced rows: %v", err)
	}
	var sequenced []string
	for rows.Next() {
		var kind string
		var seq int64
		if err := rows.Scan(&kind, &seq); err != nil {
			t.Fatalf("read a sequenced row: %v", err)
		}
		sequenced = append(sequenced, fmt.Sprintf("%s #%d", kind, seq))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the sequenced rows: %v", err)
	}
	if want := []string{"state_change #1", "state_admission #2", "state_boot #3"}; !slices.Equal(sequenced, want) {
		t.Fatalf("the sequenced rows = %v, want %v: the three state rows, each with its sequence, time and transaction, in commit order", sequenced, want)
	}
	var unsequenced int
	if err := store.Pool().QueryRow(ctx, `select count(*) from outbox
		where kind not like 'state\_%' and (state_seq is not null or state_at is not null or state_tx is not null)`).Scan(&unsequenced); err != nil {
		t.Fatalf("count the rows of other kinds that carry a state column: %v", err)
	}
	if unsequenced != 0 {
		t.Fatalf("%d rows of other kinds carry a state column, want none: the notice queued before the migration and every other kind's row stay null", unsequenced)
	}
	var lastSeq int64
	if err := store.Pool().QueryRow(ctx, `select last_seq from state_publication where project = 'LEGION'`).Scan(&lastSeq); err != nil {
		t.Fatalf("read LEGION's counter: %v", err)
	}
	if lastSeq != 3 {
		t.Fatalf("LEGION's counter stands at %d, want 3, the last state row's sequence", lastSeq)
	}

	if _, err := store.Pool().Exec(ctx, `update outbox set state_seq = 1 where kind = 'state_boot'`); err == nil || !strings.Contains(err.Error(), "outbox_state_seq_unique") {
		t.Fatalf("a second row of LEGION at sequence 1 = %v, want the unique index to refuse it", err)
	}
	if _, err := store.Pool().Exec(ctx, `insert into state_admission_blocks (project, block) values ('LEGION', '{"cap": 4}')`); err != nil {
		t.Fatalf("write LEGION's admission block: %v", err)
	}
}
