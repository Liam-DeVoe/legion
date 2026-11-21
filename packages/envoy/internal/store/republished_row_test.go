package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// republishRow stores item at the interest bucket's key victim as the census row M1-R does: a
// stream of the test's own RePublishes the message it stores on a subject of its own to the key's
// subject, so the server stores the body at the key with that stream's Nats-Stream, Nats-Subject
// and Nats-Sequence headers. It answers the sequence the interest bucket's stream stored it at.
func republishRow(t *testing.T, conn *natsgo.Conn, js natsgo.JetStreamContext, victim string, item Interest) uint64 {
	t.Helper()
	stream, subject := "FORGE_"+victim, "forge."+victim
	if _, err := js.AddStream(&natsgo.StreamConfig{Name: stream, Subjects: []string{subject}, Storage: natsgo.MemoryStorage,
		RePublish: &natsgo.RePublish{Source: subject, Destination: "$KV." + bus.InterestBucket + "." + victim}}); err != nil {
		t.Fatalf("create the republishing stream %s: %v", stream, err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(stream) })
	value, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("encode the republished row: %v", err)
	}
	before := testnats.LastSequence(t, conn, bus.InterestBucket, victim)
	if _, err := js.Publish(subject, value); err != nil {
		t.Fatalf("publish the row to %s: %v", subject, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if stored := testnats.LastSequence(t, conn, bus.InterestBucket, victim); stored > before {
			return stored
		}
		if time.Now().After(deadline) {
			t.Fatalf("the interest bucket stored no republished message at %s within 10 s", victim)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The census row M1-R at the store. A RePublish stores at a session's key a body that reads as the
// session's own row, with a PUB topic beside its own, and with the republishing stream's headers;
// a direct get reports it at that stream's sequence (1), the control, so a write fenced there is
// refused at every attempt, and every registration of the session answered 503 and its
// deregistration failed for as long as the record stood. The write made after the refusal reads
// the key from the stream's leader, reads a record the server wrote as no row of the session's
// (bus.WrittenByServer), and replaces it at the sequence it is stored at with the session's own
// row, the PUB topic not merged in; the deregistration deletes it at that sequence.
func TestAWriteOverARepublishedRecordReplacesItAtItsTrueSequence(t *testing.T) {
	conn, cleanup := connectNATS(t)
	t.Cleanup(cleanup)
	reg := openRegistry(t, conn)
	ctx := context.Background()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	kv, err := js.KeyValue(bus.InterestBucket)
	if err != nil {
		t.Fatalf("open %s: %v", bus.InterestBucket, err)
	}
	forge := func(t *testing.T, victim string) uint64 {
		t.Helper()
		stored := republishRow(t, conn, js, victim, Interest{SessionID: victim, MachineID: "edge-a", UpdatedAt: time.Now().UnixMilli(),
			Topics: []string{bindingPrivTopic, bindingPubTopic}})
		awaitInterestApplied(t, reg, victim, stored)
		if entry, err := kv.Get(victim); err != nil || entry.Revision() == stored {
			t.Fatalf("a direct get of %s answers revision %v (%v), stored at %d: want another stream's sequence, or the control shows nothing to correct", victim, entry, err, stored)
		}
		return stored
	}
	own := Interest{MachineID: "edge-a", Dir: "/priv"}
	for _, write := range rowWrites {
		t.Run(write.name, func(t *testing.T) {
			victim := "ses_republished_" + write.suffix
			own.SessionID = victim
			if _, err := write.write(ctx, reg, own, []string{bindingPrivTopic}); err != nil {
				t.Fatalf("register %s: %v", victim, err)
			}
			stored := forge(t, victim)
			row, err := write.write(ctx, reg, own, []string{bindingPrivTopic})
			if err != nil {
				t.Fatalf("%s for %s over the record republished at %d: %v", write.name, victim, stored, err)
			}
			requireOwnRow(t, row, victim, write.name)
			bucket, revision, err := reg.RefreshAt(ctx, victim)
			if err != nil || revision <= stored {
				t.Fatalf("the bucket's row of %s is %+v at revision %d (%v), want one written after the record republished at %d", victim, bucket, revision, err, stored)
			}
			requireOwnRow(t, bucket, victim, "the bucket's row of "+victim)
		})
	}
	t.Run("the deregistration", func(t *testing.T) {
		victim := "ses_republished_deregistered"
		own.SessionID = victim
		if _, err := reg.Upsert(own, []string{bindingPrivTopic}); err != nil {
			t.Fatalf("register %s: %v", victim, err)
		}
		stored := forge(t, victim)
		if _, err := reg.Remove(ctx, victim, nil, releasedByItsProcess); err != nil {
			t.Fatalf("deregister %s over the record republished at %d: %v", victim, stored, err)
		}
		if _, err := kv.Get(victim); !errors.Is(err, natsgo.ErrKeyNotFound) {
			t.Fatalf("Get(%s) after the deregistration = %v, want %v", victim, err, natsgo.ErrKeyNotFound)
		}
	})
}
