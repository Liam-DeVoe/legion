package admit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The reach keeper and the lock-out report mark a notice told only once it is in the outbox, so
// they must know whether the wake queued a row: the notice that Envoy lists nobody is one wake per
// project, and the lock-out notice one per claim, as the provider-errors notice is.
func TestTellControllerQueuedSaysWhetherTheNoticeWasQueued(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	tell := func(notice record.ControllerNotice, issue string) bool {
		t.Helper()
		var queued bool
		if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
			var err error
			queued, err = admission.TellControllerQueued(context.Background(), tx, notice, issue)
			return err
		}); err != nil {
			t.Fatalf("tell the controller %s: %v", notice.Kind, err)
		}
		return queued
	}
	noLive := record.ControllerNotice{Kind: record.EnvoyNoLiveSessionNotice, Reason: "Envoy lists no live session"}

	if tell(noLive, testProject) {
		t.Fatal("a notice with no controller registered was queued; nobody would read it")
	}
	registerController(t, pool)
	if !tell(noLive, testProject) {
		t.Fatal("the first no-live-session notice was not queued")
	}
	if tell(noLive, testProject) {
		t.Fatal("a second no-live-session notice was queued while the first waits")
	}
	locked := func(issue string) record.ControllerNotice {
		return record.ControllerNotice{Kind: record.WorkerLockedOutNotice, Role: claim.RoleImplementer,
			Claim: "legion-legion-" + issue + "-implementer", Reason: "an older process will not stop"}
	}
	if !tell(locked("legion-209"), "LEGION-209") || !tell(locked("legion-210"), "LEGION-210") {
		t.Fatal("a per-claim notice of one claim stopped another claim's")
	}
	if tell(locked("legion-209"), "LEGION-209") {
		t.Fatal("a second lock-out notice of one claim was queued while its first waits")
	}
}
