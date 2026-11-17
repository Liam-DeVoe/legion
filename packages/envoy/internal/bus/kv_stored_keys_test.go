package bus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/internal/testnats"
)

// StoredKeys lists every key the bucket's stream holds a message on: a key holding a value, one
// past the length Get refuses (testnats.LegacyKeys' unreadable key), and a key whose newest message
// is a delete or a purge marker, which a caller then reads (HeldFromLeader). The read barrier's key
// is left out. A list asked through a sequence the stream has not stored is refused, since the copy
// that answered could lack a key that held a record there.
func TestStoredKeysListsEveryKeyTheStreamHoldsAMessageOn(t *testing.T) {
	const bucket = "kv_stored_keys"
	_, kv := leaderReadBucket(t, bucket)
	_, legacy := testnats.LegacyKeys(bucket, "u")
	if _, err := kv.Legacy().Put(legacy, []byte("1")); err != nil {
		t.Fatalf("put the %d-byte legacy key: %v", len(legacy), err)
	}
	for _, key := range []string{"held", "deleted", "purged"} {
		if _, err := kv.Put(key, []byte("1")); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	if err := kv.Delete("deleted"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := kv.Purge("purged"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	barrier, err := kv.WriteBarrier(context.Background())
	if err != nil {
		t.Fatalf("write the barrier: %v", err)
	}

	keys, err := kv.StoredKeys(context.Background(), barrier)
	if err != nil {
		t.Fatalf("StoredKeys through the barrier at %d: %v", barrier, err)
	}
	slices.Sort(keys)
	want := []string{"deleted", "held", legacy, "purged"}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("StoredKeys = %v, want %v: every key with a message, markers included, the barrier's left out", keys, want)
	}
	if _, err := kv.StoredKeys(context.Background(), barrier+1); !errors.Is(err, ErrKeyListBehind) {
		t.Fatalf("StoredKeys through %d, past the stream's last sequence %d = %v, want %v", barrier+1, barrier, err, ErrKeyListBehind)
	}
}

// A list of more subjects than one STREAM.INFO answer holds comes in pages, each read from the
// stream as it is when asked, so the pages are one list only when the stream's state is the same
// in each and they add up to the first page's total. Each page here is scripted, since a
// single-node server pages only past 100,000 subjects.
func TestStoredKeysTakesPagesOnlyWhenTheyAddUpToOneList(t *testing.T) {
	const prefix = "$KV.b."
	state := func(last uint64, subjects ...string) *jetstream.StreamState {
		s := &jetstream.StreamState{FirstSeq: 1, LastSeq: last, Msgs: 3, Subjects: map[string]uint64{}}
		for _, subject := range subjects {
			s.Subjects[prefix+subject] = 1
		}
		return s
	}
	// pages answers the page scripted at each offset it is asked for.
	pages := func(byOffset map[int]subjectsPage) func(int) (subjectsPage, error) {
		return func(offset int) (subjectsPage, error) {
			page, ok := byOffset[offset]
			if !ok {
				return subjectsPage{}, fmt.Errorf("no page scripted at offset %d", offset)
			}
			return page, nil
		}
	}
	for _, row := range []struct {
		name  string
		pages map[int]subjectsPage
		want  []string
		fails error
	}{
		{
			name:  "two pages of one state",
			pages: map[int]subjectsPage{0: {Total: 3, State: state(3, "a", BarrierKey)}, 2: {Total: 3, State: state(3, "c")}},
			want:  []string{"a", "c"},
		},
		{
			name:  "a later page read the stream after a write",
			pages: map[int]subjectsPage{0: {Total: 3, State: state(3, "a", "b")}, 2: {Total: 3, State: state(4, "c")}},
			fails: ErrIncompleteKeyList,
		},
		{
			name:  "a later page listed nothing of what remains",
			pages: map[int]subjectsPage{0: {Total: 3, State: state(3, "a", "b")}, 2: {Total: 3, State: state(3)}},
			fails: ErrIncompleteKeyList,
		},
		{
			name:  "a later page listed a key again",
			pages: map[int]subjectsPage{0: {Total: 3, State: state(3, "a", "b")}, 2: {Total: 3, State: state(3, "b")}},
			fails: ErrIncompleteKeyList,
		},
		{
			name:  "a page from a copy behind the sequence asked for",
			pages: map[int]subjectsPage{0: {Total: 1, State: state(2, "a")}},
			fails: ErrKeyListBehind,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			keys, err := storedKeys(prefix, 3, pages(row.pages))
			if row.fails != nil {
				if !errors.Is(err, row.fails) {
					t.Fatalf("storedKeys = %v, %v; want %v", keys, err, row.fails)
				}
				return
			}
			slices.Sort(keys)
			if err != nil || !slices.Equal(keys, row.want) {
				t.Fatalf("storedKeys = %v, %v; want %v", keys, err, row.want)
			}
		})
	}
	outside := map[int]subjectsPage{0: {Total: 1, State: &jetstream.StreamState{Subjects: map[string]uint64{"$KV.other.a": 1}}}}
	if _, err := storedKeys(prefix, 0, pages(outside)); err == nil || !strings.Contains(err.Error(), "outside "+prefix) {
		t.Fatalf("storedKeys of a subject outside the filter = %v, want it refused", err)
	}
}
