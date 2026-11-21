package session

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A RePublish of another stream stores, at a session's key, a body that reads as the session's own
// record - its key, a machine, a time, its projects and the client instance a conditional delete
// names - with that stream's headers, so a direct get reports it at that stream's sequence, the
// control, and a delete fenced there is refused. After that refusal the conditional delete and the
// plain delete each read the key from the stream's leader, find a message the server wrote, and
// delete it at the sequence it is stored at (bus.KeyValue.DeleteServerWritten). The leader's read
// the edge confirms a session by (StoredByWriter) reads the session's own record as a writer's and
// the republished one as none.
func TestADeleteOverARepublishedRecordDeletesItAtItsTrueSequence(t *testing.T) {
	client := setupNATS(t)
	r := openBindingRegistry(t, client)
	ctx := context.Background()
	kv, err := client.JS().KeyValue(bus.SessionBucket)
	if err != nil {
		t.Fatalf("open %s: %v", bus.SessionBucket, err)
	}
	for _, tc := range []struct {
		victim string
		delete func(victim string) error
	}{
		{"ses_republished_deleted_if", func(victim string) error {
			outcome, err := r.DeleteIf(ctx, victim, DeleteConditions{ClientInstanceID: "instance-victim"})
			if err == nil && outcome != EntryDeleted {
				return errors.New(outcome.String())
			}
			return err
		}},
		{"ses_republished_deleted", func(victim string) error { return r.Delete(ctx, victim) }},
	} {
		if err := r.Put(tc.victim, SessionEntry{MachineID: "edge-a", ClientInstanceID: "instance-victim", Projects: []string{"PRIV"}}); err != nil {
			t.Fatalf("register %s: %v", tc.victim, err)
		}
		if own, err := r.StoredByWriter(ctx, tc.victim); err != nil || !own {
			t.Fatalf("StoredByWriter(%s) over its own registration = %t, %v; want true", tc.victim, own, err)
		}
		body, err := json.Marshal(map[string]any{"session_id": tc.victim, "machine_id": "edge-a", "updated_at": time.Now().UnixMilli(),
			"projects": []string{"PRIV"}, "client_instance_id": "instance-victim"})
		if err != nil {
			t.Fatalf("encode the republished record: %v", err)
		}
		stored := testnats.RepublishesKey(t, client.Conn, bus.SessionBucket, tc.victim, body)
		if entry, err := kv.Get(tc.victim); err != nil || entry.Revision() == stored {
			t.Fatalf("a direct get of %s answers revision %v (%v), stored at %d: want another stream's sequence, or the control shows nothing to correct", tc.victim, entry, err, stored)
		}
		if own, err := r.StoredByWriter(ctx, tc.victim); err != nil || own {
			t.Fatalf("StoredByWriter(%s) over the republished record = %t, %v; want false", tc.victim, own, err)
		}
		if err := tc.delete(tc.victim); err != nil {
			t.Fatalf("delete %s over the record republished at %d: %v", tc.victim, stored, err)
		}
		if _, err := kv.Get(tc.victim); !errors.Is(err, nats.ErrKeyNotFound) {
			t.Fatalf("Get(%s) after the delete = %v, want %v", tc.victim, err, nats.ErrKeyNotFound)
		}
	}
}

// A RePublish of another stream stores, at a session's key, a record that names another session,
// with that stream's headers, so a direct get reports it at that stream's sequence (the control)
// and DecodeEntry refuses it, which the registry's copy evicts. PutWithin's compare-and-swap loop
// reads the key back: a direct get fences the write on the copied sequence and the bucket refuses
// it at every attempt, so before the fix every registration of the key times out and answers 503
// for good. After the first refusal the loop reads the key from the stream's leader, finds a
// message the server wrote, and replaces it at the sequence it is stored at, so the registration
// heals.
func TestARegistrationOverARepublishedRecordHealsAtItsTrueSequence(t *testing.T) {
	client := setupNATS(t)
	r := openBindingRegistry(t, client)
	kv, err := client.JS().KeyValue(bus.SessionBucket)
	if err != nil {
		t.Fatalf("open %s: %v", bus.SessionBucket, err)
	}
	const victim = "ses_republished_registration"
	// The victim registers first, so the bucket's stream holds a message of its own before the
	// copy and stores the copy at a sequence past the one the copy's headers name.
	if err := r.Put(victim, SessionEntry{MachineID: "edge-a", Projects: []string{"PRIV"}}); err != nil {
		t.Fatalf("register %s: %v", victim, err)
	}
	body, err := json.Marshal(map[string]any{"session_id": "ses_other", "machine_id": "edge-x",
		"updated_at": time.Now().UnixMilli(), "projects": []string{"PUB"}})
	if err != nil {
		t.Fatalf("encode the republished record: %v", err)
	}
	stored := testnats.RepublishesKey(t, client.Conn, bus.SessionBucket, victim, body)
	awaitApplied(t, r, victim, stored)
	if entry, err := kv.Get(victim); err != nil || entry.Revision() == stored {
		t.Fatalf("a direct get of %s answers revision %v (%v), stored at %d: want another stream's sequence, or the control shows nothing to correct", victim, entry, err, stored)
	}
	if own, err := r.StoredByWriter(context.Background(), victim); err != nil || own {
		t.Fatalf("StoredByWriter(%s) over the republished record = %t, %v; want false", victim, own, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.PutWithin(ctx, victim, SessionEntry{MachineID: "edge-a", Projects: []string{"PRIV"}}); err != nil {
		t.Fatalf("register %s over the record republished at %d: %v", victim, stored, err)
	}
	if own, err := r.StoredByWriter(context.Background(), victim); err != nil || !own {
		t.Fatalf("StoredByWriter(%s) after the registration = %t, %v; want true", victim, own, err)
	}
	entry, err := r.Refresh(context.Background(), victim)
	if err != nil || entry.MachineID != "edge-a" || entry.SessionID != victim || !slices.Equal(entry.Projects, []string{"PRIV"}) {
		t.Fatalf("Refresh(%s) after the registration = %+v, %v; want the victim's own registration", victim, entry, err)
	}
}
