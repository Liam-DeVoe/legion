package record

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// A task_branch row names the generation and the task whose branch it makes on GitHub, runs on the
// lifecycle lane beside the issue's own branch row, and reads back as it was queued. A row that
// names no task makes no branch, so it is refused where it is queued.
func TestATaskBranchRowNamesItsTask(t *testing.T) {
	now := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	row, err := NewOutboxRow("LEGION-208", TaskBranch{Generation: 2, Task: "b"}, now)
	must(t, err)
	if row.Kind != OutboxKindTaskBranch || string(row.Kind) != "task_branch" {
		t.Fatalf("row kind = %q, want task_branch", row.Kind)
	}
	payload, err := DecodeOutboxPayload(row)
	must(t, err)
	if want := (TaskBranch{Generation: 2, Task: "b"}); !reflect.DeepEqual(payload, want) {
		t.Fatalf("decoded %#v, want %#v", payload, want)
	}
	if lane := LaneOf(OutboxKindTaskBranch); lane != LaneLifecycle {
		t.Fatalf("LaneOf(task_branch) = %q, want the lifecycle lane, beside issue_branch", lane)
	}
	if _, err := NewOutboxRow("LEGION-208", TaskBranch{Generation: 2}, now); err == nil {
		t.Fatal("a task_branch row that names no task was queued, want it refused")
	}
}

// A start of an issue's role waits while an older task_branch row of the issue is unfinished, as it
// waits for the issue's own branch row: no step's agent starts before its task's branch is on
// GitHub, where its first push would otherwise create the branch, which GitHub can refuse on a large
// repository. While the branch row runs the start is held behind it, and once the row finishes the
// start is due. A start of another issue waits for nothing of this one.
func TestAStartWaitsForItsTasksBranch(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	records := NewStore()
	now := time.Now().UTC().Truncate(time.Microsecond)
	enqueue := func(tx pgx.Tx, issue string, payload OutboxPayload) {
		t.Helper()
		row, err := NewOutboxRow(issue, payload, now)
		must(t, err)
		must(t, records.Enqueue(ctx, tx, row))
	}
	start := func(issue string) SuperviseRequest {
		return SuperviseRequest{Op: "start", Tree: issue, Role: claim.RoleImplementer, Generation: 1}
	}
	inTx(t, st, func(tx pgx.Tx) {
		enqueue(tx, "LEGION-208", TaskBranch{Generation: 1, Task: "b"})
		enqueue(tx, "LEGION-208", start("LEGION-208"))
		enqueue(tx, "LEGION-209", start("LEGION-209"))
	})
	var branch OutboxRow
	inTx(t, st, func(tx pgx.Tx) {
		due, err := records.DueRows(ctx, tx, "LEGION", "", nil, now.Add(time.Hour))
		must(t, err)
		if keys, want := outboxKeys(due), []string{"task_branch:LEGION-208", "supervise:LEGION-209:start"}; !reflect.DeepEqual(keys, want) {
			t.Fatalf("due rows = %v, want %v: LEGION-208's start waits for its task's branch", keys, want)
		}
		leased, ok, err := records.ClaimRow(ctx, tx, due[0].ID, now, time.Minute)
		must(t, err)
		if !ok {
			t.Fatal("the task_branch row was not claimed")
		}
		branch = leased
	})
	inTx(t, st, func(tx pgx.Tx) {
		held, err := records.HeldRows(ctx, tx, "LEGION", "LEGION-208", now)
		must(t, err)
		if len(held) != 1 || held[0].Kind != OutboxKindSupervise || held[0].WaitsOn != branch.ID {
			t.Fatalf("held rows of LEGION-208 = %#v, want its start, waiting on the running task_branch row %d", held, branch.ID)
		}
		must(t, records.FinishOutbox(ctx, tx, branch.ID, branch.LeaseToken))
	})
	inTx(t, st, func(tx pgx.Tx) {
		due, err := records.DueRows(ctx, tx, "LEGION", "LEGION-208", nil, now.Add(time.Hour))
		must(t, err)
		if keys, want := outboxKeys(due), []string{"supervise:LEGION-208:start"}; !reflect.DeepEqual(keys, want) {
			t.Fatalf("due rows of LEGION-208 once its task's branch row finished = %v, want %v", keys, want)
		}
	})
}
