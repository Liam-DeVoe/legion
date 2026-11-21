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

// restoreLogRows are TestTheHoldJournal's rows of a restore judged by the log: the route judges
// only by a latest log entry that verifies, each entry carrying the row the operator signed, and a
// released restore after a held entry only once that hold's journal shows it ended.
func restoreLogRows(t *testing.T) {
	ctx := context.Background()

	t.Run("a forged released log entry mid-hold restores nothing, and the view does not read released", func(t *testing.T) {
		// The reviewer's case: the hold is held at gen 3 with hold set done and a command's intent
		// pending; a database writer deletes the row and adds a log entry, released at gen 10, which
		// it cannot sign. Fails while the route judges by an entry it does not verify: Bootstrap
		// names restore_gen 11, RestoreRow(released) stores released there, and the view reads it.
		r := newRig(t)
		c := r.client(t)
		const holdID = "h-forged-log"
		hold(t, c, holdID, holdrecord.WorkflowCutover, nil)
		if _, err := c.Admit(ctx, holdID, cmd("create-bucket"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold`); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `insert into nats_hold_log (gen, phase, hold_id, reason, written_at, written_by, signed_at, sig)
			values (10, 'released', $1, 'forged', now(), 'writer', now(), 'forged')`, holdID); err != nil {
			t.Fatal(err)
		}
		gen := uint64(11)
		var mismatch *holdrecord.RestoreGenError
		if err := c.Bootstrap(ctx); errors.As(err, &mismatch) {
			gen = mismatch.RestoreGen
		}
		got, err := c.RestoreRow(ctx, gen, natsgrant.OperatorPhaseReleased, holdID, "restored after a delete")
		var refusal *holdrecord.RouteError
		if !errors.Is(err, holdrecord.ErrUnknown) || !errors.As(err, &refusal) || refusal.Code != "below_floor" {
			t.Errorf("RestoreRow of released at gen %d after the forged entry answered %+v, %v; want 409 below_floor, read as ErrUnknown", gen, got, err)
		}
		if phase, known := r.HoldView(false).State(ctx); known && phase == natsgrant.OperatorPhaseReleased {
			t.Errorf("the hold view reads released, known: every client is admitted mid-hold")
		}
	})

	t.Run("a row lost between hold release's done entry and the row's move restores released, by the journal", func(t *testing.T) {
		// The log's latest entry is held, but the hold's journal, signed, shows hold release done:
		// the hold has ended, and released frees no command. Fails without the journal's word: the
		// restore is refused 400 bad_transition, as after any held entry.
		r := newRig(t)
		c := r.client(t)
		const holdID = "h-released-unmoved"
		hold(t, c, holdID, holdrecord.WorkflowBucketsCreate, nil)
		run(t, c, holdID, cmd("buckets create"), nil, nil)
		if _, err := c.Admit(ctx, holdID, cmd(holdrecord.CommandRelease), nil); err != nil {
			t.Fatal(err)
		}
		if err := c.AppendDone(ctx, holdID, holdrecord.CommandRelease, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold`); err != nil {
			t.Fatal(err)
		}
		var mismatch *holdrecord.RestoreGenError
		if err := c.Bootstrap(ctx); !errors.As(err, &mismatch) {
			t.Fatalf("Bootstrap answered %v, want a RestoreGenError", err)
		}
		restored, err := c.RestoreRow(ctx, mismatch.RestoreGen, natsgrant.OperatorPhaseReleased, holdID, "restored after the release's done entry")
		if err != nil || restored.Phase != natsgrant.OperatorPhaseReleased || restored.Gen != mismatch.RestoreGen {
			t.Fatalf("RestoreRow of released at restore_gen %d = %+v, %v; want released at that gen", mismatch.RestoreGen, restored, err)
		}
	})
}
