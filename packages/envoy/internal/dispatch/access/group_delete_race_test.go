package access

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// listWrite is one write of a project's list as the routes run it: LockForAccessWrite, then the
// write, in the one transaction tx.
type listWrite func(ctx context.Context, tx pgx.Tx, ev *events.Broker, key, kind, principal string, by model.Actor) (model.ProjectAccess, model.Event, error)

// raceAgainstDelete opens a write of project's list naming group crew, takes its locks, starts a
// delete of crew in another transaction, and only then writes and commits, so the delete reaches
// every lock it can before the write does. Both must commit (no deadlock), and no row may name
// crew once they have.
func raceAgainstDelete(t *testing.T, project string, write listWrite) {
	t.Helper()
	ctx := context.Background()
	database := seedAccess(t)
	broker := events.NewBroker()
	by := model.Actor{Kind: "user", ID: olga}

	open, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the list write: %v", err)
	}
	defer open.Rollback(ctx)
	if _, err := LockForAccessWrite(ctx, open, project, "crew"); err != nil {
		t.Fatalf("lock %s for its list write: %v", project, err)
	}

	deleted := make(chan error, 1)
	go func() {
		deleted <- func() error {
			tx, err := database.Pool.Begin(context.Background())
			if err != nil {
				return err
			}
			defer tx.Rollback(context.Background())
			if _, err := DeleteGroup(context.Background(), tx, broker, "crew", by); err != nil {
				return err
			}
			return tx.Commit(context.Background())
		}()
	}()
	select {
	case err := <-deleted:
		t.Fatalf("the delete of crew finished (%v) while a write naming crew was still open; want it to wait", err)
	case <-time.After(500 * time.Millisecond):
	}
	if _, _, err := write(ctx, open, broker, project, PrincipalGroup, "crew", by); err != nil {
		t.Fatalf("write crew on %s's list while its delete waits: %v", project, err)
	}
	if err := open.Commit(ctx); err != nil {
		t.Fatalf("commit the list write: %v", err)
	}
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatalf("delete crew after the list write committed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the delete of crew never finished after the list write committed")
	}

	var rows int
	if err := database.Pool.QueryRow(ctx, `
		select count(*) from project_access where principal_kind = 'group' and principal = 'crew'
	`).Scan(&rows); err != nil {
		t.Fatalf("count crew's list rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("%d access-list rows still name crew after it was deleted, want none", rows)
	}
}

// TestAGroupDeletedWhileAListNamesItLeavesNoRow: a group named on a list by a write still open
// when the group is deleted is off that list once both commit; otherwise the list keeps a row
// naming a group that no longer exists, and a later group of the same name inherits the project.
func TestAGroupDeletedWhileAListNamesItLeavesNoRow(t *testing.T) {
	raceAgainstDelete(t, "PUB", AddPrincipal)
}

// TestAGroupDeletedWhileItIsTakenOffAListDeadlocksNothing: a delete of a group racing a write
// that takes the group off a list already naming it both commit. The write holds the project and
// the list row; the delete, unordered against it, would hold the group and sweep that row, then
// wait for the project the write holds, while the write waits for the row.
func TestAGroupDeletedWhileItIsTakenOffAListDeadlocksNothing(t *testing.T) {
	raceAgainstDelete(t, "OTHER", RemovePrincipal)
}
