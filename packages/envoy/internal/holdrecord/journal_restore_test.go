package holdrecord_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// prepareRestoreRows are TestTheHoldJournal's rows of a hold row lost while the hold is prepare,
// each driven through the operator's client alone. The route stores a released restore after a
// prepare entry, since a prepare row refuses no client and released frees nobody, and RestoreRow
// of released reads no journal, so RestoreRow of released with the hold's id, as hold abort from
// prepare leaves it, at the gen Bootstrap's RestoreGenError named brings the row back, before the
// hold's journal starts and after. Each fails while the route stores released only after a
// released entry: it answers 400 bad_transition, and no restore brings the row back. A released
// restore after a held entry stays refused ("RestoreRow of a released row mid-hold is refused" in
// journal_test.go), and so does a held restore of a hold that abort from prepare ended.
func prepareRestoreRows(t *testing.T) {
	ctx := context.Background()
	for _, x := range []struct {
		name    string
		journal bool // the journal started and hold set admitted before the row was lost
	}{
		{"before its journal starts", false},
		{"after its journal starts and hold set is admitted", true},
	} {
		t.Run("a row lost while the hold is prepare, "+x.name+", is restored released with the hold's id", func(t *testing.T) {
			r := newRig(t)
			c := r.client(t)
			const holdID = "h-lost-in-prepare"
			current := row(t, c)
			if _, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, holdID, "prepare"); err != nil {
				t.Fatal(err)
			}
			if x.journal {
				if err := c.StartJournal(ctx, holdID, holdrecord.WorkflowBucketsCreate, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Admit(ctx, holdID, cmd(holdrecord.CommandSet), nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Pool.Exec(ctx, `delete from nats_hold`); err != nil {
				t.Fatal(err)
			}
			var mismatch *holdrecord.RestoreGenError
			if err := c.Bootstrap(ctx); !errors.As(err, &mismatch) {
				t.Fatalf("Bootstrap with the row lost in prepare answered %v, want a RestoreGenError", err)
			}
			restored, err := c.RestoreRow(ctx, mismatch.RestoreGen, natsgrant.OperatorPhaseReleased, holdID, "restored after a delete in prepare")
			if err != nil || restored.Gen != mismatch.RestoreGen || restored.Phase != natsgrant.OperatorPhaseReleased || restored.HoldID != holdID {
				t.Fatalf("RestoreRow of released with %s at restore_gen %d = %+v, %v; want released, that hold id, at that gen", holdID, mismatch.RestoreGen, restored, err)
			}
			hold(t, c, "h-after-prepare", holdrecord.WorkflowBucketsCreate, nil)
		})
	}

	t.Run("a held restore of a hold that abort from prepare ended is refused, since the log shows it ended", func(t *testing.T) {
		// Abort from prepare deletes the cohort and moves the row to released, and writes no journal
		// entry, so RestoreRow's journal read finds the hold not ended. Fails while the route stores
		// held after any log entry: the row lands held for a hold no command will release.
		r := newRig(t)
		c := r.client(t)
		const holdID = "h-aborted-in-prepare"
		current := row(t, c)
		prepared, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, holdID, "prepare")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.StartJournal(ctx, holdID, holdrecord.WorkflowBucketsCreate, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteCohort(ctx, holdID); err != nil {
			t.Fatal(err)
		}
		if _, err := c.MoveRow(ctx, prepared.Gen, natsgrant.OperatorPhaseReleased, holdID, "hold abort from prepare"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold`); err != nil {
			t.Fatal(err)
		}
		got, err := c.RestoreRow(ctx, 1, natsgrant.OperatorPhaseHeld, holdID, "held after the abort")
		var refusal *holdrecord.RouteError
		if !errors.As(err, &refusal) || refusal.Code != "bad_transition" {
			t.Fatalf("RestoreRow of held for the hold abort from prepare ended answered %+v, %v; want 400 bad_transition", got, err)
		}
		if _, err := c.Row(ctx); !errors.As(err, &refusal) || refusal.Code != "no_row" {
			t.Fatalf("after the refused restore Row answered %v, want no_row", err)
		}
	})
}
