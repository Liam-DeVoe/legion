package holdrecord_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"

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
