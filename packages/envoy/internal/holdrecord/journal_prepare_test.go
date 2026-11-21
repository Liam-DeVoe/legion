package holdrecord_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// prepareAbortRows are TestTheHoldJournal's rows of a hold whose row was lost in prepare and
// restored released (ReleasedPrepareAbort): hold abort is admitted on that row while the journal
// shows prepare, its cohort delete is admitted there, and its done entry ends the journal. A
// released row whose journal shows anything else, or no journal, deletes nothing.
func prepareAbortRows(t *testing.T) {
	ctx := context.Background()
	cohort := json.RawMessage(`{"raised":["a"]}`)

	// restoredReleased deletes the row and restores it released with holdID, at the gen the
	// route names, as the operator restores a row lost in prepare.
	restoredReleased := func(t *testing.T, r rig, c *holdrecord.Client, holdID string) {
		t.Helper()
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold`); err != nil {
			t.Fatal(err)
		}
		var mismatch *holdrecord.RestoreGenError
		if err := c.Bootstrap(ctx); !errors.As(err, &mismatch) {
			t.Fatalf("Bootstrap with the row lost answered %v, want a RestoreGenError", err)
		}
		if _, err := c.RestoreRow(ctx, mismatch.RestoreGen, natsgrant.OperatorPhaseReleased, holdID, "restored after a delete in prepare"); err != nil {
			t.Fatal(err)
		}
	}
	cohorts := func(t *testing.T, r rig, holdID string) int {
		t.Helper()
		var n int
		if err := r.Pool.QueryRow(ctx, `select count(*) from nats_hold_records where hold_id = $1 and kind = 'cohort'`, holdID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("after a row lost in prepare is restored released, hold abort deletes the cohort and ends the journal", func(t *testing.T) {
		// Fails while the released row admits nothing: Admit refuses ErrNotHeld and the cohort
		// delete 409 hold_mismatch, so the cohort is orphaned and the journal never ends.
		r := newRig(t)
		c := r.client(t)
		const holdID = "h-prepare-abort"
		current := row(t, c)
		if _, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, holdID, "prepare"); err != nil {
			t.Fatal(err)
		}
		if err := c.StartJournal(ctx, holdID, holdrecord.WorkflowCutover, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Admit(ctx, holdID, cmd(holdrecord.CommandSet), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.PutRecord(ctx, holdID, "cohort", "cohort", 0, cohort); err != nil {
			t.Fatal(err)
		}
		restoredReleased(t, r, c, holdID)
		if _, err := c.Admit(ctx, holdID, cmd(holdrecord.CommandAbort), nil); err != nil {
			t.Errorf("Admit(hold abort) on the released row answered %v, want admitted", err)
		}
		if err := c.DeleteCohort(ctx, holdID); err != nil {
			t.Errorf("DeleteCohort on the released row answered %v, want nil", err)
		}
		if err := c.AppendDone(ctx, holdID, holdrecord.CommandAbort, nil); err != nil {
			t.Fatalf("AppendDone(hold abort) answered %v", err)
		}
		if n := cohorts(t, r, holdID); n != 0 {
			t.Errorf("after the abort the table holds %d cohort records, want 0", n)
		}
		d, _ := journal(t, c, holdID)
		if done, ok := d.Latest(holdrecord.CommandAbort, holdrecord.StateDone); !ok || !d.Complete {
			t.Errorf("the journal's abort done entry is %+v, %v, and complete %v; want a done abort that ends it", done, ok, d.Complete)
		}
		if _, err := c.Admit(ctx, holdID, cmd(holdrecord.CommandAbort), nil); !errors.Is(err, holdrecord.ErrNotHeld) {
			t.Errorf("a second abort after the journal ended answered %v, want ErrNotHeld", err)
		}
	})

	t.Run("a released row whose journal shows a done hold set deletes no cohort", func(t *testing.T) {
		// A hold that ran and was released keeps its cohort record; its row is released with its
		// hold id. Fails while the route deletes on any released row of the hold.
		r := newRig(t)
		c := r.client(t)
		const holdID = "h-released-done"
		hold(t, c, holdID, holdrecord.WorkflowBucketsCreate, nil)
		if _, err := c.PutRecord(ctx, holdID, "cohort", "cohort", 0, cohort); err != nil {
			t.Fatal(err)
		}
		run(t, c, holdID, cmd("buckets create"), nil, nil)
		release(t, c, holdID)
		var refusal *holdrecord.RouteError
		if err := c.DeleteCohort(ctx, holdID); !errors.As(err, &refusal) || refusal.Code != "hold_mismatch" {
			t.Errorf("DeleteCohort on the released row of a hold with a done hold set answered %v, want 409 hold_mismatch", err)
		}
		if n := cohorts(t, r, holdID); n != 1 {
			t.Errorf("after the refused delete the table holds %d cohort records, want 1", n)
		}
	})

	t.Run("a released row with no journal for its hold deletes no cohort", func(t *testing.T) {
		// The row was lost in prepare before the journal was written. Fails while the route reads
		// a missing journal as one that shows prepare.
		r := newRig(t)
		c := r.client(t)
		const holdID = "h-released-nojournal"
		current := row(t, c)
		if _, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, holdID, "prepare"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.PutRecord(ctx, holdID, "cohort", "cohort", 0, cohort); err != nil {
			t.Fatal(err)
		}
		restoredReleased(t, r, c, holdID)
		var refusal *holdrecord.RouteError
		if err := c.DeleteCohort(ctx, holdID); !errors.As(err, &refusal) || refusal.Code != "hold_mismatch" {
			t.Errorf("DeleteCohort on the released row of a hold with no journal answered %v, want 409 hold_mismatch", err)
		}
		if n := cohorts(t, r, holdID); n != 1 {
			t.Errorf("after the refused delete the table holds %d cohort records, want 1", n)
		}
	})
}
