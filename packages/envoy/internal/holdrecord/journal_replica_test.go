package holdrecord_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus/signedrecord"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// replicaName names one of the rig's two replicas in a failure.
func replicaName(second bool) string {
	if second {
		return "the second replica"
	}
	return "the first replica"
}

// replicaRows are TestTheHoldJournal's rows of two broker replicas on one database
// (holdrecordtest's UseReplica), each with a records floor of its own. A cohort the routes delete
// reads as deleted on both, since the delete the store keeps is the operator's signed one, and a
// cohort gone with no delete that verifies reads as unknown on each replica that saw it.
func replicaRows(t *testing.T) {
	ctx := context.Background()

	// prepared is a rig whose row is prepare under holdID, with its cohort written on the first
	// replica, writes times, and then read on both, so each has a floor for it at its last gen.
	prepared := func(t *testing.T, holdID string, writes int) rig {
		t.Helper()
		r := newRig(t)
		c := r.client(t)
		current := row(t, c)
		if _, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, holdID, "prepare"); err != nil {
			t.Fatal(err)
		}
		for gen := range writes {
			if _, err := c.PutRecord(ctx, holdID, "cohort", "cohort", uint64(gen), json.RawMessage(`{"raised":["a"]}`)); err != nil {
				t.Fatal(err)
			}
		}
		for _, second := range []bool{false, true} {
			r.UseReplica(second)
			if records, err := c.Records(ctx, holdID, "cohort"); err != nil || len(records) != 1 {
				t.Fatalf("%s reads the cohort: %d records, %v", replicaName(second), len(records), err)
			}
		}
		r.UseReplica(false)
		return r
	}

	t.Run("a cohort the routes delete on one replica reads as deleted on the other, and the abort's rerun there answers nil", func(t *testing.T) {
		// Fails while the delete is kept only in the floor of the replica that served it: the
		// second replica, which saw the cohort, reads its absence as a database writer's delete
		// (ErrUnknown, below_floor), and so fails the abort's rerun.
		r := prepared(t, "h-replicas", 1)
		c := r.client(t)
		var value, sig string
		var gen int64
		if err := r.Pool.QueryRow(ctx, `select value::text, gen, sig from nats_hold_records where hold_id = 'h-replicas' and kind = 'cohort'`).Scan(&value, &gen, &sig); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteCohort(ctx, "h-replicas"); err != nil {
			t.Fatalf("DeleteCohort on the first replica: %v", err)
		}
		for _, second := range []bool{false, true} {
			r.UseReplica(second)
			if records, err := c.Records(ctx, "h-replicas", "cohort"); err != nil || len(records) != 0 {
				t.Fatalf("%s reads the deleted cohort: %d records, %v; want none and no error", replicaName(second), len(records), err)
			}
			if err := c.DeleteCohort(ctx, "h-replicas"); err != nil {
				t.Fatalf("the abort's rerun of DeleteCohort on %s: %v", replicaName(second), err)
			}
		}
		// A database writer puts the deleted cohort back: on each replica it is a deleted record
		// put back, which reads as unknown.
		if _, err := r.Pool.Exec(ctx, `insert into nats_hold_records (hold_id, kind, key, value, gen, written_at, sig)
			values ('h-replicas', 'cohort', 'cohort', $1::jsonb, $2, now(), $3)`, value, gen, sig); err != nil {
			t.Fatal(err)
		}
		for _, second := range []bool{false, true} {
			r.UseReplica(second)
			if _, err := c.Records(ctx, "h-replicas", "cohort"); !errors.Is(err, holdrecord.ErrUnknown) {
				t.Fatalf("%s reads the deleted cohort put back as %v, want ErrUnknown", replicaName(second), err)
			}
		}
	})

	t.Run("a cohort a database writer deletes, with no signed delete, reads as unknown on each replica that saw it", func(t *testing.T) {
		r := prepared(t, "h-sql", 1)
		c := r.client(t)
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold_records where hold_id = 'h-sql' and kind = 'cohort'`); err != nil {
			t.Fatal(err)
		}
		for _, second := range []bool{false, true} {
			r.UseReplica(second)
			if _, err := c.Records(ctx, "h-sql", "cohort"); !errors.Is(err, holdrecord.ErrUnknown) {
				t.Fatalf("%s reads the cohort deleted outside the routes as %v, want ErrUnknown", replicaName(second), err)
			}
			if err := c.DeleteCohort(ctx, "h-sql"); !errors.Is(err, holdrecord.ErrUnknown) {
				t.Fatalf("DeleteCohort on %s, with the cohort deleted outside the routes, answered %v, want ErrUnknown", replicaName(second), err)
			}
		}
	})

	t.Run("a delete the store holds that does not verify, or that names an earlier gen, deletes nothing a replica saw", func(t *testing.T) {
		// A database writer deletes the cohort, read at gen 2 on both replicas, and writes a delete
		// row beside it. Each row here must leave the absence a database writer's: one that
		// counted a delete without verifying it, or one signed over gen 1, fails.
		_, foreign, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range []struct {
			name     string
			gen      uint64             // the delete row's gen
			foreign  ed25519.PrivateKey // its signer when not the operator
			operator bool               // signed by the operator's key; with no foreign key either, no signature
			hold     string             // the hold its signature names; "" for the cohort's own
		}{
			{name: "signed by a key the broker does not hold", gen: 2, foreign: foreign},
			{name: "with no signature", gen: 2},
			{name: "signed by the operator over another hold's cohort", gen: 2, operator: true, hold: "h-other"},
			{name: "signed by the operator over gen 1, below the gen both replicas read", gen: 1, operator: true},
		} {
			r := prepared(t, "h-forged-delete", 2)
			c := r.client(t)
			signer, hold := x.foreign, x.hold
			if x.operator {
				signer = r.SigningKey
			}
			if hold == "" {
				hold = "h-forged-delete"
			}
			sig := ""
			if signer != nil {
				if sig, err = signedrecord.Sign(signer, signedrecord.RecordDelete(hold, "cohort", "cohort", x.gen)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Pool.Exec(ctx, `with gone as (delete from nats_hold_records where hold_id = 'h-forged-delete' and kind = 'cohort')
				insert into nats_hold_record_deletes (hold_id, kind, key, gen, deleted_at, sig) values ('h-forged-delete', 'cohort', 'cohort', $1, now(), $2)`,
				int64(x.gen), sig); err != nil {
				t.Fatal(err)
			}
			for _, second := range []bool{false, true} {
				r.UseReplica(second)
				if _, err := c.Records(ctx, "h-forged-delete", "cohort"); !errors.Is(err, holdrecord.ErrUnknown) {
					t.Errorf("a delete row %s: %s reads the cohort as %v, want ErrUnknown", x.name, replicaName(second), err)
				}
			}
		}
	})
}

// rowFloorRows are TestTheHoldJournal's rows of the routes' floor of the hold row: a row a
// database writer puts back below the gen the broker served or wrote, or another verified row at
// that gen, reads as unknown, and no move is made from it.
func rowFloorRows(t *testing.T) {
	ctx := context.Background()
	const rowColumns = `gen, phase, hold_id, reason, updated_at, updated_by, signed_at, sig`

	t.Run("a prepare row put back mid-hold reads as unknown, and no release is moved from it", func(t *testing.T) {
		// The reviewer's replay: prepare at gen 2, held at gen 3 with hold set done and a command's
		// intent pending; a database writer puts the signed prepare row back and deletes log row 3.
		// Fails while the routes keep no floor for the row: Row reads prepare at gen 2, and
		// MoveRow(2, released) stores released at gen 3, freeing every client mid-hold.
		r := newRig(t)
		c := r.client(t)
		current := row(t, c)
		prepared, err := c.MoveRow(ctx, current.Gen, natsgrant.OperatorPhasePrepare, "h-replayed", "prepare")
		if err != nil {
			t.Fatal(err)
		}
		var saved [8]any
		var gen int64
		var phase, holdID, reason, updatedBy, sig string
		var updatedAt, signedAt time.Time
		if err := r.Pool.QueryRow(ctx, `select `+rowColumns+` from nats_hold`).Scan(&gen, &phase, &holdID, &reason, &updatedAt, &updatedBy, &signedAt, &sig); err != nil {
			t.Fatal(err)
		}
		saved = [8]any{gen, phase, holdID, reason, updatedAt, updatedBy, signedAt, sig}
		if err := c.StartJournal(ctx, "h-replayed", holdrecord.WorkflowCutover, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Admit(ctx, "h-replayed", cmd(holdrecord.CommandSet), nil); err != nil {
			t.Fatal(err)
		}
		held, err := c.MoveRow(ctx, prepared.Gen, natsgrant.OperatorPhaseHeld, "h-replayed", "held")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AppendDone(ctx, "h-replayed", holdrecord.CommandSet, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Admit(ctx, "h-replayed", cmd("create-bucket"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `update nats_hold set gen = $1, phase = $2, hold_id = $3, reason = $4, updated_at = $5, updated_by = $6, signed_at = $7, sig = $8`, saved[:]...); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `delete from nats_hold_log where gen = $1`, int64(held.Gen)); err != nil {
			t.Fatal(err)
		}
		// Each step reports and the next still runs, so the control shows where the move lands.
		if got, err := c.Row(ctx); !errors.Is(err, holdrecord.ErrUnknown) {
			t.Errorf("Row with the prepare row put back answered %+v, %v; want ErrUnknown", got, err)
		}
		var refusal *holdrecord.RouteError
		if moved, err := c.MoveRow(ctx, prepared.Gen, natsgrant.OperatorPhaseReleased, "h-replayed", "abort"); !errors.As(err, &refusal) || refusal.Code != "gen_mismatch" {
			t.Errorf("MoveRow(%d, released) from the put-back prepare row answered %+v, %v; want 409 gen_mismatch", prepared.Gen, moved, err)
		}
		var storedGen int64
		var storedPhase string
		if err := r.Pool.QueryRow(ctx, `select gen, phase from nats_hold`).Scan(&storedGen, &storedPhase); err != nil || storedPhase != "prepare" || storedGen != int64(prepared.Gen) {
			t.Errorf("after the refused move the row is %s at gen %d (%v), want the put-back prepare at gen %d", storedPhase, storedGen, err, prepared.Gen)
		}
	})

	t.Run("another verified row at the gen the broker served reads as unknown", func(t *testing.T) {
		// A database writer holding a released row signed at the held row's gen (the second effect
		// of the replay above) swaps it in. Fails while the floor keeps no digest of the row.
		r := newRig(t)
		c := r.client(t)
		held := hold(t, c, "h-swapped", holdrecord.WorkflowCutover, nil)
		if _, err := c.Row(ctx); err != nil {
			t.Fatal(err)
		}
		signedAt := time.Now().UTC().Truncate(time.Microsecond)
		sig, err := signedrecord.Sign(r.SigningKey, signedrecord.HoldRow(held.Gen, string(natsgrant.OperatorPhaseReleased), "h-swapped", signedAt))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Pool.Exec(ctx, `update nats_hold set phase = 'released', signed_at = $1, sig = $2`, signedAt, sig); err != nil {
			t.Fatal(err)
		}
		if got, err := c.Row(ctx); !errors.Is(err, holdrecord.ErrUnknown) {
			t.Fatalf("Row with a released row swapped in at gen %d answered %+v, %v; want ErrUnknown", held.Gen, got, err)
		}
	})
}
