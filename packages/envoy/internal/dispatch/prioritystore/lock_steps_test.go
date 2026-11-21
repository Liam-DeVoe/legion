package prioritystore

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// TestEveryPriorityWriteTakesTheStoreLockOrder holds Write, the one call of the store's writer
// every write of a priority makes, whose call site graphstore's TestEveryStoreWriteCallSiteIsListed
// lists, to the lock order written on api/inbox_read.go's markInboxReadThreads, against a real
// Postgres. A priority belongs to no project, issue or document, so its writes take no owner's
// lock; what a create and a rank move take before any row instead is the order's allocation lock
// (lockRankAllocation), the one advisory lock every such write takes first, which this test holds
// where an owner's lock stands. Then the priority's node row, then its tuples. Every tuple of
// this store is appended at the sentinel, which no repeat key covers, so an append waits on no
// row and no hold stands for it: what a hold can show is that no row is taken after the append.
//
// A create makes the node's row; a rank move and a delete read it `for no key update` before the
// store writes it again (lockedNode); an issue's `advances` takes no row of the priority at all.
func TestEveryPriorityWriteTakesTheStoreLockOrder(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	// The order's allocation lock, held as every write that takes it takes it, before any row.
	holdTheOrder := func(t *testing.T) *lockstep.Hold {
		t.Helper()
		hold := &lockstep.Hold{
			Name: "the order's allocation lock", Step: lockstep.StepOwner, Taken: true,
			Statement: "select pg_advisory_xact_lock(hashtext('priority-rank'))",
			Unlocked:  []string{"graph_nodes", "graph_tuples"},
		}
		lockstep.Begin(t, database, hold)
		if err := lockRankAllocation(ctx, hold.Tx); err != nil {
			t.Fatalf("hold %s: %v", hold.Name, err)
		}
		return hold
	}
	// The node row of a priority the store holds, as a write that reads it for the store's node
	// write takes it.
	holdTheRow := func(t *testing.T, id string, taken bool) *lockstep.Hold {
		t.Helper()
		hold := &lockstep.Hold{
			Name: "priority " + id + "'s node row", Step: lockstep.StepNode, Taken: taken,
			Kind: KindPriority, ID: id, Unlocked: []string{"graph_tuples"},
		}
		if taken {
			hold.Statement = "select kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event from graph_nodes"
		}
		return lockstep.HoldRow(t, database, hold,
			`select 1 from graph_nodes where kind = $1 and id = $2 for update`, KindPriority, id)
	}
	create := func(t *testing.T, id string) {
		t.Helper()
		writeOnce(t, database.Pool, func(tx pgx.Tx) error {
			key, err := RankAt(ctx, tx, id, "", "")
			if err != nil {
				return err
			}
			return Create(ctx, tx, id, map[string]string{AttributeTitle: id, AttributeRank: key}, alice, at)
		})
	}

	t.Run("a create takes the order's lock and then makes the node's row", func(t *testing.T) {
		const id = "lock-step-made"
		order := holdTheOrder(t)
		// The node is new, so it is held as a row another transaction inserted and has not
		// committed: the create's read of whether the id is held does not see it, and the
		// store's insert of that key waits to learn whether it commits. The store sends its node
		// write and its append as one batch, and the create already holds a lock on graph_tuples
		// while that batch's insert waits, so this hold names no table it may not hold; the rank
		// move and the delete below take their row in a statement of their own, before that
		// batch, and their holds show graph_tuples untouched there.
		row := &lockstep.Hold{
			Name: "priority " + id + "'s node row", Step: lockstep.StepNode, Taken: true,
			Kind: KindPriority, ID: id, Statement: "insert into graph_nodes (",
		}
		lockstep.Begin(t, database, row)
		if err := Write(ctx, row.Tx, []Node{{
			Kind: KindPriority, ID: id, Supertag: KindPriority, Visibility: VisibilityProject, State: StateLive,
			MadeAt: at, MadeBy: bob, SourceEvent: NoEvent,
		}}, nil); err != nil {
			t.Fatalf("hold %s: %v", row.Name, err)
		}
		pid, done := startWrite(t, database, func(ctx context.Context, tx pgx.Tx) error {
			return Create(ctx, tx, id, map[string]string{AttributeTitle: "Made under the lock steps"}, alice, at)
		})
		if queued := lockstep.Follow(t, database, "Create", pid, done, []*lockstep.Hold{order, row}); len(queued) != 2 {
			t.Fatalf("the create queued behind %s, want the order's lock and then the node's row", lockstep.HoldNames(queued))
		}
		if node, held, err := Read(ctx, database.Pool, id); err != nil || !held || node.MadeBy != alice {
			t.Fatalf("the create left %+v (held %t, %v), want the node it made", node, held, err)
		}
	})

	t.Run("a rank move takes the order's lock and then the node's row", func(t *testing.T) {
		create(t, "lock-step-first")
		create(t, "lock-step-moved")
		order := holdTheOrder(t)
		row := holdTheRow(t, "lock-step-moved", true)
		pid, done := startWrite(t, database, func(ctx context.Context, tx pgx.Tx) error {
			_, err := Move(ctx, tx, "lock-step-moved", "lock-step-first", "", bob, at.Add(time.Minute))
			return err
		})
		if queued := lockstep.Follow(t, database, "Move", pid, done, []*lockstep.Hold{order, row}); len(queued) != 2 {
			t.Fatalf("the move queued behind %s, want the order's lock and then the node's row", lockstep.HoldNames(queued))
		}
		// The priority the create above made carries no order key, so it lists last.
		assertListed(t, database.Pool, []string{"lock-step-moved", "lock-step-first", "lock-step-made"})
	})

	t.Run("a delete takes the node's row", func(t *testing.T) {
		create(t, "lock-step-gone")
		row := holdTheRow(t, "lock-step-gone", true)
		pid, done := startWrite(t, database, func(ctx context.Context, tx pgx.Tx) error {
			return SetState(ctx, tx, "lock-step-gone", StateGone, bob, at.Add(2*time.Minute))
		})
		if queued := lockstep.Follow(t, database, "SetState", pid, done, []*lockstep.Hold{row}); len(queued) != 1 {
			t.Fatalf("the delete queued behind %s, want the node's row", lockstep.HoldNames(queued))
		}
		if node, _, err := Read(ctx, database.Pool, "lock-step-gone"); err != nil || node.State != StateGone {
			t.Fatalf("the delete left the state %q (%v), want %q", node.State, err, StateGone)
		}
	})

	t.Run("an issue's advances takes no row of the priority", func(t *testing.T) {
		create(t, "lock-step-advanced")
		row := holdTheRow(t, "lock-step-advanced", false)
		pid, done := startWrite(t, database, func(ctx context.Context, tx pgx.Tx) error {
			return Advance(ctx, tx, "LOCK-1", "lock-step-advanced", alice, at.Add(3*time.Minute))
		})
		if queued := lockstep.Follow(t, database, "Advance", pid, done, []*lockstep.Hold{row}); len(queued) != 0 {
			t.Fatalf("the advance queued behind %s, want nothing: it appends a tuple and writes no node", lockstep.HoldNames(queued))
		}
	})
}

// startWrite runs write in a transaction of its own on a connection of its own, so the test knows
// the backend it watches, and answers that backend and the channel the write's result arrives on.
func startWrite(t *testing.T, database *store.Store, write func(context.Context, pgx.Tx) error) (uint32, <-chan error) {
	t.Helper()
	ctx := context.Background()
	single := storetest.SingleConnection(t, database)
	var pid int64
	if err := single.Pool.QueryRow(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read the backend the write runs on: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- pgx.BeginFunc(ctx, single.Pool, func(tx pgx.Tx) error { return write(ctx, tx) })
	}()
	return uint32(pid), done
}
