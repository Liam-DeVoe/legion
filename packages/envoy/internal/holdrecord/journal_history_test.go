package holdrecord_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/bus/signedrecord"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// historyRows are TestTheHoldJournal's rows of rows a database writer brings from elsewhere: a
// signed row of an earlier history of the tables, a restore at a gen the writer chose, a released
// restore of a hold its journal shows held, and a row the store answers changed.
func historyRows(t *testing.T) {
	ctx := context.Background()
	storedRow := func(t *testing.T, r rig) (int64, string) {
		t.Helper()
		var gen int64
		var phase string
		if err := r.Pool.QueryRow(ctx, `select gen, phase from nats_hold`).Scan(&gen, &phase); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return gen, phase
	}

	t.Run("a signed row of an earlier history, at a higher gen, put back after a reset reads as unknown", func(t *testing.T) {
		// History A reached released at gen 4 an hour ago; the tables were reset, and history B
		// reached held at gen 3. A writer puts A's gen-4 row back: it verifies, and its gen is above
		// the floor, but it was signed an hour before B's gen 3. Fails while the floor reads no
		// signing time: the row and the view read released.
		r := newRig(t)
		c := r.client(t)
		current := row(t, c)
		prepared, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, "h-history-b", "prepare")
		if err != nil {
			t.Fatal(err)
		}
		held, err := c.MoveRow(ctx, prepared.Gen, natsgrant.OperatorPhaseHeld, "h-history-b", "held")
		if err != nil {
			t.Fatal(err)
		}
		view := r.HoldView(false)
		if phase, known := view.State(ctx); !known || phase != natsgrant.OperatorPhaseHeld {
			t.Fatalf("the view reads %q, known %v; want held", phase, known)
		}
		earlier := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
		sig, err := signedrecord.Sign(r.SigningKey, signedrecord.HoldRow(held.Gen+1, string(natsgrant.OperatorPhaseReleased), "h-history-a", earlier))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `update nats_hold set gen = $1, phase = 'released', hold_id = 'h-history-a', signed_at = $2, sig = $3`,
			int64(held.Gen+1), earlier, sig); err != nil {
			t.Fatal(err)
		}
		if got, err := c.Row(ctx); !errors.Is(err, holdrecord.ErrUnknown) {
			t.Errorf("Row with history A's gen-%d row put back answered %+v, %v; want ErrUnknown", held.Gen+1, got, err)
		}
		if phase, known := view.State(ctx); known && phase == natsgrant.OperatorPhaseReleased {
			t.Errorf("the view reads history A's row as released, known: every client is admitted mid-hold")
		}
	})

	t.Run("a client that has seen gen N sends no restore at another gen than N+1, Bootstrap's gen 1 included", func(t *testing.T) {
		// The client read held at gen 3. A writer deletes the row and every log entry, and the
		// broker starts again, so its floor is empty. Fails while only a held restore's followed gen
		// is bounded: Bootstrap stores released at gen 1, mid-hold.
		r := newRig(t)
		c := r.client(t)
		hold(t, c, "h-seen", holdrecord.WorkflowCutover, nil)
		if _, err := c.Admit(ctx, "h-seen", cmd("create-bucket"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold`); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold_log`); err != nil {
			t.Fatal(err)
		}
		r.Restart()
		if err := c.Bootstrap(ctx); !errors.Is(err, holdrecord.ErrUnknown) {
			t.Errorf("Bootstrap by a client that saw gen 3 answered %v, want ErrUnknown", err)
		}
		if gen, phase := storedRow(t, r); gen != 0 {
			t.Errorf("after Bootstrap the table holds %s at gen %d, want no row", phase, gen)
		}
		if got, err := c.RestoreRow(ctx, 1, natsgrant.OperatorPhaseReleased, "", "released at gen 1"); !errors.Is(err, holdrecord.ErrUnknown) {
			t.Errorf("RestoreRow of released at gen 1 by a client that saw gen 3 answered %+v, %v; want ErrUnknown", got, err)
		}
		if gen, phase := storedRow(t, r); gen != 0 {
			t.Errorf("after RestoreRow the table holds %s at gen %d, want no row", phase, gen)
		}
	})

	t.Run("a released restore naming a hold whose journal shows hold set done and no end is refused, whatever the log shows", func(t *testing.T) {
		// The reviewer's case: a writer deletes the row and the log entries above the hold's own
		// prepare entry, and the broker starts again; a client that has seen no row restores
		// released with the hold's id. Fails while the route judges by the log alone: it stores
		// released at gen 3 with a command pending.
		r := newRig(t)
		c := r.client(t)
		held := hold(t, c, "h-cut-log", holdrecord.WorkflowCutover, nil)
		if _, err := c.Admit(ctx, "h-cut-log", cmd("create-bucket"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold`); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold_log where gen >= $1`, int64(held.Gen)); err != nil {
			t.Fatal(err)
		}
		r.Restart()
		fresh := r.client(t)
		got, err := fresh.RestoreRow(ctx, held.Gen, natsgrant.OperatorPhaseReleased, "h-cut-log", "released after a cut log")
		var refusal *holdrecord.RouteError
		if !errors.As(err, &refusal) || refusal.Code != "bad_transition" {
			t.Errorf("RestoreRow of released for h-cut-log answered %+v, %v; want 400 bad_transition", got, err)
		}
		if gen, phase := storedRow(t, r); gen != 0 {
			t.Errorf("after the refused restore the table holds %s at gen %d, want no row", phase, gen)
		}
	})

	t.Run("a row the store answers changed raises no floor, and the genuine row put back still reads", func(t *testing.T) {
		// A database trigger raises the gen of every row written, so the row the move's RETURNING
		// answers does not verify. Fails while the route puts that row through the floor unverified:
		// the floor stands at the raised gen, and the genuine row put back reads as unknown until
		// the process starts again.
		r := newRig(t)
		c := r.client(t)
		current := row(t, c)
		saved := savedHoldRow(t, r)
		if _, err := r.Pool.Exec(ctx, `create function hold_tamper() returns trigger language plpgsql as $$ begin new.gen := new.gen + 1000; return new; end $$;
			create trigger hold_tamper before update on nats_hold for each row execute function hold_tamper()`); err != nil {
			t.Fatal(err)
		}
		if moved, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, "h-tamper", "prepare"); !errors.Is(err, holdrecord.ErrUnknown) {
			t.Errorf("MoveRow under the trigger answered %+v, %v; want ErrUnknown", moved, err)
		}
		if _, err := r.Pool.Exec(ctx, `drop trigger hold_tamper on nats_hold; drop function hold_tamper()`); err != nil {
			t.Fatal(err)
		}
		putHoldRowBack(t, r, saved)
		if got, err := c.Row(ctx); err != nil || got.Gen != current.Gen {
			t.Errorf("Row with the genuine gen-%d row put back answered %+v, %v; want it read", current.Gen, got, err)
		}
	})
}
