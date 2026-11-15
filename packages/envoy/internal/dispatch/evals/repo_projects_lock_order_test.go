package evals

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/delivery/deliverytest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestTheDeliveryFixtureAndAMappingWriteOfOneRepositoryNeverDeadlock: every writer of
// repo_projects takes the change log's lock (6900) in one position against that table's row locks
// (R19). The mapping routes take it first (lockRepoProjectWrite, api/repo_projects.go), and the
// table's trigger takes it after a writer's row lock, so a writer that inserts its mapping row in a
// statement of its own holds the row while it waits for 6900. The delivery fixture a running
// server's database is loaded with (deliverytest.Write, which evalfixture -delivery runs) wrote its
// mappings that way: while a mapping write of the same repository held 6900 and then wrote the
// row, the two waited on each other, and Postgres ended one with 40P01 (a mapping PUT answering
// 500). The fixture is paused at its projects insert, after its delivery settings, while the
// mapping transaction takes 6900 as the route does first; then it is let on, and the mapping
// transaction writes the same repository's row and rolls back, as a refused mapping write does.
func TestTheDeliveryFixtureAndAMappingWriteOfOneRepositoryNeverDeadlock(t *testing.T) {
	r := newRig(t)
	ctx := t.Context()
	pause := pauseInserts(t, r, "projects", "true")
	written := make(chan error, 1)
	go func() {
		_, err := deliverytest.Write(ctx, r.database.Pool, deliverytest.Spec{Project: "M2", Days: 2})
		written <- err
	}()
	if storetest.WaitForLockWaiters(t, pause, 1, lockOrderBound) < 1 {
		t.Fatalf("the fixture never reached its projects insert")
	}
	mapping, err := r.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the mapping write: %v", err)
	}
	t.Cleanup(func() { _ = mapping.Rollback(context.Background()) })
	if err := delivery.LockChanges(ctx, mapping); err != nil {
		t.Fatalf("the mapping write takes the change log's lock: %v", err)
	}
	if err := pause.Commit(ctx); err != nil {
		t.Fatalf("let the fixture on: %v", err)
	}
	if storetest.WaitForLockWaiters(t, mapping, 1, lockOrderBound) < 1 {
		t.Fatalf("the fixture never waited for the change log's lock")
	}
	_, mappingErr := mapping.Exec(ctx, `
		insert into repo_projects (repo, project, created_by, is_default)
		values ('acme/m2-platform', 'M2', '{"kind":"system","id":"mapping"}', false)
		on conflict (repo) do nothing`)
	_ = mapping.Rollback(ctx)
	requireNoDeadlock(t, "the mapping write", mappingErr)
	requireNoDeadlock(t, "the delivery fixture", <-written)
}
