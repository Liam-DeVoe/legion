package session

import (
	"errors"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// lateSessionWrite runs late, once, before the first write of key through it (Put or Update)
// reaches the bucket - what another writer does between a registration's read and its
// write - and records what the bucket answered that first write. late's own writes go through the
// raw handle and are not hooked.
type lateSessionWrite struct {
	natsgo.KeyValue

	key      string
	late     func()
	writes   int
	firstErr error
}

func (kv *lateSessionWrite) Put(key string, value []byte) (uint64, error) {
	return kv.wrote(key, func() (uint64, error) { return kv.KeyValue.Put(key, value) })
}

func (kv *lateSessionWrite) Update(key string, value []byte, revision uint64) (uint64, error) {
	return kv.wrote(key, func() (uint64, error) { return kv.KeyValue.Update(key, value, revision) })
}

func (kv *lateSessionWrite) wrote(key string, write func() (uint64, error)) (uint64, error) {
	if key != kv.key {
		return write()
	}
	kv.writes++
	if kv.writes != 1 {
		return write()
	}
	kv.late()
	revision, err := write()
	kv.firstErr = err
	return revision, err
}

// A registration's write of a session the bucket held no entry for, which another writer's entry
// and its delete (a deregistration, a reap) overtake before the write lands, is refused over that
// delete, and the registry reads the bucket again and writes its entry over the delete it then
// reads (write, readStored): each write lands only over the very entry its own read found.
//
// Before the write went out as an Update at the revision its read answered, a read that found no
// entry wrote through Create, and nats.go's Create, refused over the delete marker, updates at
// that marker's revision (nats.go@v1.53.1 kv.go:729-739): the write planned before the delete
// landed over it, an entry no read had seen gone.
func TestALateSessionWriteIsRefusedOverADeleteMadeAfterItsRead(t *testing.T) {
	client := setupNATS(t)
	reg, err := OpenSessionRegistry(client.Conn, WithSessionReplicas(1), WithSessionTTL(10*time.Second))
	if err != nil {
		t.Fatalf("open the session registry: %v", err)
	}
	const sessionID = "ses_late_entry"
	raw := reg.watcher.KV().Legacy()
	late := &lateSessionWrite{KeyValue: raw, key: sessionID, late: func() {
		if _, err := raw.Put(sessionID, []byte(`{"machine_id":"another-host"}`)); err != nil {
			t.Fatalf("another writer stores %s: %v", sessionID, err)
		}
		if err := raw.Delete(sessionID); err != nil {
			t.Fatalf("another writer deletes %s: %v", sessionID, err)
		}
	}}
	useKV(t, reg, late)

	if err := reg.Put(sessionID, SessionEntry{MachineID: "example-host", Dir: "/late"}); err != nil {
		t.Fatalf("register %s: %v", sessionID, err)
	}
	if late.writes == 0 {
		t.Fatal("the registration never wrote, so nothing overtook it")
	}
	if !errors.Is(late.firstErr, natsgo.ErrKeyExists) {
		t.Fatalf("the registration's write planned on no entry answered %v after another writer's entry and delete, want the bucket's refusal (nats.ErrKeyExists): it landed over the delete made after its read", late.firstErr)
	}
	if late.writes != 2 {
		t.Fatalf("the registration wrote %d times, want 2: the refused write and the one planned again over the delete", late.writes)
	}
	stored, err := raw.Get(sessionID)
	if err != nil {
		t.Fatalf("the bucket holds no entry of %s after the write planned again over the delete: %v", sessionID, err)
	}
	entry, err := decodeSessionEntry(stored.Value())
	if err != nil || entry.MachineID != "example-host" {
		t.Fatalf("the bucket holds %s for %s (%v), want the registration's own entry", stored.Value(), sessionID, err)
	}
}
