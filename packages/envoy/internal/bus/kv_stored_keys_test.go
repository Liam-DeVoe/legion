package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

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
// in each, in one more ask after the last page too, and they add up to the first page's total. A
// list of one page is taken without that ask. Each answer here is scripted, as the server's JSON,
// since a single-node server pages only past 100,000 subjects.
func TestStoredKeysTakesPagesOnlyWhenTheyAddUpToOneList(t *testing.T) {
	const prefix = "$KV.b."
	// pages answers the answer scripted at each offset it is asked for.
	pages := func(byOffset map[int]answer) func(int) (subjectsPage, error) {
		return func(offset int) (subjectsPage, error) {
			scripted, ok := byOffset[offset]
			if !ok {
				return subjectsPage{}, fmt.Errorf("no page scripted at offset %d", offset)
			}
			return scripted.page(t, prefix), nil
		}
	}
	for _, row := range []struct {
		name  string
		pages map[int]answer
		want  []string
		fails error
	}{
		{
			name:  "one page",
			pages: map[int]answer{0: {total: 2, first: 1, last: 3, msgs: 3, subjects: []string{"a", BarrierKey}}},
			want:  []string{"a"},
		},
		{
			name: "two pages of one state",
			pages: map[int]answer{
				0: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"a", BarrierKey}},
				2: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"c"}},
				3: {total: 3, first: 1, last: 3, msgs: 3},
			},
			want: []string{"a", "c"},
		},
		{
			// The bucket holds a, b and c. The last page reads the stream's state, then a expires
			// and e is stored before it reads its subjects, which are now b, c and e: the subject at
			// its offset is e, and c has moved into the range the first page read.
			name: "the last page's subjects read after its state",
			pages: map[int]answer{
				0: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"a", "b"}},
				2: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"e"}},
				3: {total: 3, first: 2, last: 4, msgs: 3},
			},
			fails: ErrIncompleteKeyList,
		},
		{
			name: "a later page read the stream after a write",
			pages: map[int]answer{
				0: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"a", "b"}},
				2: {total: 3, first: 1, last: 4, msgs: 3, subjects: []string{"c"}},
			},
			fails: ErrIncompleteKeyList,
		},
		{
			name: "a later page listed nothing of what remains",
			pages: map[int]answer{
				0: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"a", "b"}},
				2: {total: 3, first: 1, last: 3, msgs: 3},
			},
			fails: ErrIncompleteKeyList,
		},
		{
			name: "a later page listed a key again",
			pages: map[int]answer{
				0: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"a", "b"}},
				2: {total: 3, first: 1, last: 3, msgs: 3, subjects: []string{"b"}},
			},
			fails: ErrIncompleteKeyList,
		},
		{
			name:  "a page from a copy behind the sequence asked for",
			pages: map[int]answer{0: {total: 1, first: 1, last: 2, msgs: 2, subjects: []string{"a"}}},
			fails: ErrKeyListBehind,
		},
		{
			// A member answers while the stream has no leader (jetstream_api.go:2100-2104) and
			// names none (jetstream_cluster.go:14075, 14081-14083), with every peer, itself too.
			name:  "a page from a member while the stream has no leader",
			pages: map[int]answer{0: {total: 1, first: 1, last: 3, msgs: 3, subjects: []string{"a"}, leaderless: true, others: []string{"n1", "n2", "n3"}}},
			fails: ErrKeyListBehind,
		},
		{
			// The preferred member of a scale-up answers while n1 leads (jetstream_api.go:2112-2133):
			// it names n1 the leader, among the peers other than itself.
			name:  "a page from a member while another server leads",
			pages: map[int]answer{0: {total: 1, first: 1, last: 3, msgs: 3, subjects: []string{"a"}, leader: "n1", others: []string{"n1", "n3"}}},
			fails: ErrKeyListBehind,
		},
		{
			// The leader of a clustered stream names itself, and every other peer but itself.
			name:  "a page from the leader of a clustered stream",
			pages: map[int]answer{0: {total: 1, first: 1, last: 3, msgs: 3, subjects: []string{"a"}, leader: "n2", others: []string{"n1", "n3"}}},
			want:  []string{"a"},
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
	outside := map[int]answer{0: {total: 1, first: 1, last: 1, msgs: 1}}
	outsidePage := func(offset int) (subjectsPage, error) {
		page, err := pages(outside)(offset)
		page.State.Subjects = map[string]uint64{"$KV.other.a": 1}
		return page, err
	}
	if _, err := storedKeys(prefix, 0, outsidePage); err == nil || !strings.Contains(err.Error(), "outside "+prefix) {
		t.Fatalf("storedKeys of a subject outside the filter = %v, want it refused", err)
	}
}

// answer is one scripted STREAM.INFO answer with its subject list: the total the server counts,
// the stream's first and last sequence and message count, this page's subjects under the prefix it
// is rendered with, and the cluster the answering copy names: the server it names the stream's
// leader (s1, the one server of an unclustered deployment, unless leader is set; none when
// leaderless) and the stream's other peers it lists (others).
type answer struct {
	total             int
	first, last, msgs uint64
	subjects          []string
	leader            string
	leaderless        bool
	others            []string
}

// page is a's JSON as nats-server v2.15.1 sends it (jetstream_api.go:503-507), decoded as
// StoredKeys decodes an answer.
func (a answer) page(t *testing.T, prefix string) subjectsPage {
	t.Helper()
	subjects := map[string]uint64{}
	for _, subject := range a.subjects {
		subjects[prefix+subject] = 1
	}
	cluster := map[string]any{"name": "c"}
	switch {
	case a.leaderless:
	case a.leader != "":
		cluster["leader"] = a.leader
	default:
		cluster["leader"] = "s1"
	}
	var peers []map[string]any
	for _, name := range a.others {
		peers = append(peers, map[string]any{"name": name, "current": true, "active": 1000000})
	}
	if peers != nil {
		cluster["replicas"] = peers
	}
	data, err := json.Marshal(map[string]any{
		"type":    "io.nats.jetstream.api.v1.stream_info_response",
		"total":   a.total,
		"state":   map[string]any{"first_seq": a.first, "last_seq": a.last, "messages": a.msgs, "subjects": subjects},
		"cluster": cluster,
	})
	if err != nil {
		t.Fatalf("render the scripted answer: %v", err)
	}
	var page subjectsPage
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatalf("decode the scripted answer: %v", err)
	}
	return page
}
