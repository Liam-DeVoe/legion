package bus

import (
	"errors"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
)

// writingOnMissingGet stores value under the key whose read answered that the bucket holds none,
// which is what a registration landing between DeleteAtRead's read and its delete does. It writes
// once, so the delete's own read of the bucket, if one is made, answers what it stored.
type writingOnMissingGet struct {
	nats.KeyValue
	value []byte
	wrote bool
	err   error
}

func (kv *writingOnMissingGet) Get(key string) (nats.KeyValueEntry, error) {
	entry, err := kv.KeyValue.Get(key)
	if errors.Is(err, nats.ErrKeyNotFound) && !kv.wrote {
		kv.wrote = true
		_, kv.err = kv.KeyValue.Put(key, kv.value)
	}
	return entry, err
}

// A delete whose read found the key absent deletes nothing, so the row a write stored between
// that read and the delete is kept. A delete at no revision would take it: nats.go sends no
// expected-last-subject-sequence header for revision 0, so that delete carries no fence at all
// and the bucket applies it over whatever it holds - a session's live registration, which the
// delete read none of and judged nothing about.
func TestADeleteAtReadKeepsTheRowWrittenAfterItsReadFoundNone(t *testing.T) {
	client, err := Connect([]string{testnats.URL(t)})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	js, err := client.Conn.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	const bucket = "kv_delete_at_read"
	kv, err := EnsureKeyValue(t.Context(), client.Conn, js, &nats.KeyValueConfig{Bucket: bucket, Storage: nats.MemoryStorage})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	t.Cleanup(func() { _ = js.DeleteKeyValue(bucket) })

	const key = "ses_raced"
	racing := &writingOnMissingGet{KeyValue: kv.Legacy(), value: []byte("registered")}
	revision, err := NewKeyValue(racing, kv.WatchHandle(), kv.Streams()).DeleteAtRead(t.Context(), key, nil)
	if err != nil {
		t.Fatalf("delete %s at the revision its read answered: %v", key, err)
	}
	if racing.err != nil {
		t.Fatalf("the write that landed under the delete's read: %v", racing.err)
	}
	if revision != 0 {
		t.Fatalf("the delete read revision %d, want 0: its read found the key absent", revision)
	}

	entry, err := kv.Get(key)
	if err != nil {
		t.Fatalf("%s after the delete: %v, want the row written under its read kept", key, err)
	}
	if got := string(entry.Value()); got != "registered" {
		t.Fatalf("%s holds %q, want %q", key, got, "registered")
	}
}

// A value the caller's readable refuses can be a copy the server stored from another key's
// record, which carries that record's Nats-Sequence header ahead of its own, so the revision a
// direct get reports for it is the source's. The delete reads such a value again from the
// stream's leader and deletes it at the sequence it is stored at, and the source keeps its record.
// The control is the same delete with no readable, which reads no leader and is refused at the
// source's revision, leaving the copy stored.
func TestADeleteAtReadDeletesACopyItsReadableRefusesAtItsTrueSequence(t *testing.T) {
	const bucket = "kv_delete_at_read_copy"
	_, kv := leaderReadBucket(t, bucket)
	conn := kv.Streams().Conn()
	source, err := kv.Put("ses_source", []byte("the source's record"))
	if err != nil {
		t.Fatalf("put ses_source: %v", err)
	}
	copied := testnats.ServerWritesKey(t, conn, bucket, "ses_copy", testnats.DirectGetLast(bucket, "ses_source"), nil)
	own := func(value []byte) bool { return string(value) == "ses_copy's own record" }

	revision, err := kv.DeleteAtRead(t.Context(), "ses_copy", nil)
	if !errors.Is(err, nats.ErrKeyExists) || revision != source {
		t.Fatalf("DeleteAtRead(ses_copy) with no readable = revision %d, %v; want the source's revision %d refused with %v: the control shows nothing to correct", revision, err, source, nats.ErrKeyExists)
	}
	if stored := testnats.LastSequence(t, conn, bucket, "ses_copy"); stored != copied {
		t.Fatalf("ses_copy's newest message is at %d after the refused delete, want the copy at %d", stored, copied)
	}

	revision, err = kv.DeleteAtRead(t.Context(), "ses_copy", own)
	if err != nil || revision != copied {
		t.Fatalf("DeleteAtRead(ses_copy) = revision %d, %v; want the copy deleted at %d, the sequence it is stored at", revision, err, copied)
	}
	if _, err := kv.Get("ses_copy"); !errors.Is(err, nats.ErrKeyNotFound) {
		t.Fatalf("Get(ses_copy) after the delete = %v, want %v", err, nats.ErrKeyNotFound)
	}
	if entry, err := kv.Get("ses_source"); err != nil || entry.Revision() != source {
		t.Fatalf("ses_source after the delete is %v (%v), want its record kept at %d", entry, err, source)
	}
}
