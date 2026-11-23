package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// A running row's lease is renewed while the row's own effect is inside a transaction of the
// pool. The renewal's tick runs beside the row, on a timer goroutine of its own (renewLease), so
// it is a second caller of the pool and not a second connection of the row's: it must be served
// its own connection, as it was before the pool refused nested connections (INFRA-3169).
//
// Here the row's read of its hand-over (handOverHolds) waits on a lock the test holds, which is
// the shape INFRA-3169 measured: a transaction that waits while it holds its connection, as a fact
// that queues on the fact serializer does. A tick that comes in that wait has to move the lease a
// whole lease ahead, as it does for a row blocked outside any transaction
// (TestALaneRenewsItsRunningRowsLeaseAndNoSweepClaimsItAgain). A tick refused instead leaves the
// lease where it was, and three refused ticks in a row lapse it while the database answers, so
// the sweep runs the row a second time beside the first.
func TestALaneRenewsItsRowsLeaseWhileTheRowIsInsideATransaction(t *testing.T) {
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	clock := microClock()
	ctx := context.Background()
	const issue = "LEGION-208"
	// The issue the hand-over's read finds first, so the read goes on to the hand-over table the
	// test locks.
	if err := store.Transact(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return records.PutIssue(ctx, tx, runningRoot(issue, phase.Implementing))
	}); err != nil {
		t.Fatalf("record the issue the hand-over's read finds: %v", err)
	}
	client := &blockingDispatch{issue: dispatch.Issue{Key: issue, Status: "todo"}}
	runner := &outbox{pool: pool, dispatchProject: "LEGION", records: records, dispatch: client, log: quietLogger()}
	enqueueOutbox(t, pool, records, mustOutboxRow(t, issue,
		record.HandOverStatusWrite("todo", "todo", "LEGION-1", 1), clock.Now()))

	lock, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin the locking transaction: %v", err)
	}
	var unlocked sync.Once
	unlock := func() { unlocked.Do(func() { _ = lock.Rollback(context.Background()) }) }
	t.Cleanup(unlock)
	if _, err := lock.Exec(ctx, "lock table handovers in access exclusive mode"); err != nil {
		t.Fatalf("lock the hand-over table against the row's read: %v", err)
	}
	runLanes(t, runner, clock, unlock)

	testwait.Eventually(t, "the row's read of its hand-over to wait on the test's lock", func() bool {
		var waiting int
		if err := pool.QueryRow(ctx, "select count(*) from pg_stat_activity where datname = current_database() and wait_event_type = 'Lock'").Scan(&waiting); err != nil {
			t.Fatalf("count the backends waiting on a lock: %v", err)
		}
		return waiting == 1
	})
	id := outboxIDs(t, pool)[0]
	claimed := rowStateOf(t, pool, id)
	if claimed.leaseToken == "" || claimed.leaseUntil == nil {
		t.Fatalf("the running row is leased %q until %v, want it the lane's", claimed.leaseToken, claimed.leaseUntil)
	}

	renewEvery := outboxLease / 3
	clock.advance(renewEvery)
	if fired := clock.fire(renewEvery); fired != 1 {
		t.Fatalf("renewal timers fired = %d, want the running row's", fired)
	}
	renewed := rowStateOf(t, pool, id)
	if renewed.leaseUntil == nil || !renewed.leaseUntil.Equal(clock.Now().Add(outboxLease)) {
		t.Fatalf("lease_until = %v after the tick, want %v, a whole lease ahead of now: the tick was not served while the row held its transaction",
			renewed.leaseUntil, clock.Now().Add(outboxLease))
	}
	if renewed.leaseToken != claimed.leaseToken {
		t.Fatalf("lease token %q after the tick, want the row's own %q", renewed.leaseToken, claimed.leaseToken)
	}

	unlock()
	testwait.Eventually(t, "the row finished once the lock went", func() bool { return outboxRows(t, pool) == 0 })
}

// A renewal tick that finds its row gone logs no lost lease. A row's own effect may finish the row
// inside its run: a left pod's clean close finishes its check row in the close's transaction
// (checkLeftPodWorkspace), and a tick that waited on that transaction's lock finds the row gone
// once it commits, a row no other lane holds. A tick that finds the row under another lane's lease
// logs the loss.
func TestALeaseRenewalThatFindsItsRowGoneLogsNoLostLease(t *testing.T) {
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	ctx := context.Background()
	now := time.Now().UTC()
	row := mustOutboxRow(t, "LEGION-208", record.HandOverStatusWrite("todo", "todo", "LEGION-1", 1), now)
	enqueueOutbox(t, pool, records, row)
	row.ID, row.LeaseToken = outboxIDs(t, pool)[0], "this-run"
	logs, log := watchedLog()
	runner := &outbox{pool: pool, records: records, log: log}
	lease := func(token string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `update outbox set lease_token = $2, lease_until = now() + interval '5 minutes'
			where id = $1`, row.ID, token); err != nil {
			t.Fatalf("lease the row to %s: %v", token, err)
		}
	}
	const lostLine = "outbox row lease lost"

	lease("another-lane")
	runner.renewOnce(ctx, row, &leaseRenewal{warned: map[int64]bool{}}, now)
	if got := count(logs, lostLine); got != 1 {
		t.Fatalf("lost-lease lines after a tick that found another lane's lease = %d, want 1: %s", got, logs.String())
	}

	lease(row.LeaseToken)
	if err := store.Transact(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return records.FinishLeasedOutbox(ctx, tx, row.ID, row.LeaseToken)
	}); err != nil {
		t.Fatalf("finish the row as its own effect does: %v", err)
	}
	runner.renewOnce(ctx, row, &leaseRenewal{warned: map[int64]bool{}}, now)
	if got := count(logs, lostLine); got != 1 {
		t.Errorf("lost-lease lines after a tick that found its row finished = %d, want still 1: %s", got, logs.String())
	}
}
