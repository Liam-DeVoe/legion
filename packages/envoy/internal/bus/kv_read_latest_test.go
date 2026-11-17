package bus

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/internal/testnats"
)

// ReadLatest answers each key whose latest record is a put, with that record, and nothing for a key
// deleted, purged, never written or refused; it writes its barrier as a delete marker, so a read of
// the barrier key finds no value, and a second ReadLatest writes a later one.
func TestReadLatestAnswersEachKeysLatestPut(t *testing.T) {
	_, kv := leaderReadBucket(t, "read-latest-answers")
	put := func(key, value string) uint64 {
		t.Helper()
		revision, err := kv.Put(key, []byte(value))
		if err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
		return revision
	}
	put("live", "1")
	put("rewritten", "1")
	rewritten := put("rewritten", "2")
	put("deleted", "1")
	if err := kv.Delete("deleted"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	put("purged", "1")
	if err := kv.Purge("purged"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	latest, err := kv.ReadLatest(context.Background(), []string{"live", "rewritten", "deleted", "purged", "never-written", "bad key*"})
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if got, want := slices.Sorted(maps.Keys(latest)), []string{"live", "rewritten"}; !slices.Equal(got, want) {
		t.Fatalf("ReadLatest answered %v, want %v", got, want)
	}
	if got := latest["rewritten"]; got.Revision() != rewritten || string(got.Value()) != "2" {
		t.Fatalf("ReadLatest answered rewritten at %d %q, want its latest put at %d \"2\"", got.Revision(), got.Value(), rewritten)
	}
	first := barrierRevision(t, kv)
	if first <= rewritten {
		t.Fatalf("the barrier is at %d, want it past every record written before the read (%d)", first, rewritten)
	}
	if _, err := kv.legacy.Get(BarrierKey); !errors.Is(err, nats.ErrKeyNotFound) {
		t.Fatalf("a get of the barrier key = %v, want %v: the barrier is a delete marker", err, nats.ErrKeyNotFound)
	}
	if _, err := kv.ReadLatest(context.Background(), []string{"live"}); err != nil {
		t.Fatalf("a second ReadLatest: %v", err)
	}
	if second := barrierRevision(t, kv); second <= first {
		t.Fatalf("the second read's barrier is at %d, want it past the first's %d", second, first)
	}
	if latest, err := kv.ReadLatest(context.Background(), nil); err != nil || len(latest) != 0 {
		t.Fatalf("ReadLatest of no keys = %v, %v; want nothing and no error", latest, err)
	}
}

// A store that has not applied the barrier yet - a member behind the others - delivers a key's
// existing record and the end of its existing records before the delete that removed the key and
// the barrier after it. ReadLatest waits for the barrier, so it answers the key absent, as the
// bucket holds it, and a barrier older than its own does not end the wait.
func TestReadLatestWaitsForItsBarrierPastAStoreBehindTheBucket(t *testing.T) {
	_, kv := leaderReadBucket(t, "read-latest-lagging")
	const barrier = 9
	script := []jetstream.KeyValueEntry{
		scriptedEntry{key: BarrierKey, revision: 3, op: jetstream.KeyValueDelete},
		scriptedEntry{key: "removed-after", revision: 5, op: jetstream.KeyValuePut},
		nil,
		scriptedEntry{key: "removed-after", revision: 8, op: jetstream.KeyValueDelete},
		scriptedEntry{key: BarrierKey, revision: barrier, op: jetstream.KeyValueDelete},
	}
	lagging := NewKeyValue(kv.Legacy(), scriptedWatch{KeyValue: kv.WatchHandle(), script: script},
		ackedAt{JetStream: kv.Streams(), sequence: barrier})
	latest, err := lagging.ReadLatest(context.Background(), []string{"removed-after"})
	if err != nil {
		t.Fatalf("ReadLatest: %v", err)
	}
	if _, kept := latest["removed-after"]; kept {
		t.Fatalf("ReadLatest answered removed-after, which the store deleted before it applied the barrier: it did not wait for the barrier")
	}
}

// A barrier that is not committed reads nothing: ReadLatest answers its error, and makes no watch.
func TestReadLatestReadsNothingWithoutItsBarrier(t *testing.T) {
	_, kv := leaderReadBucket(t, "read-latest-uncommitted")
	if _, err := kv.Put("held", []byte("1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	var watched atomic.Int32
	refused := errors.New("the barrier was not committed")
	handle := NewKeyValue(kv.Legacy(), countedWatch{KeyValue: kv.WatchHandle(), watches: &watched},
		refusedPublish{JetStream: kv.Streams(), err: refused})
	latest, err := handle.ReadLatest(context.Background(), []string{"held"})
	if !errors.Is(err, refused) || latest != nil {
		t.Fatalf("ReadLatest without a committed barrier = %v, %v; want nothing and %v", latest, err, refused)
	}
	if n := watched.Load(); n != 0 {
		t.Fatalf("ReadLatest made %d watches without a committed barrier, want none", n)
	}
}

// On a three-copy bucket whose other two members have stopped, the member left cannot commit the
// barrier, so ReadLatest answers that error, not the key the member still holds.
func TestReadLatestAnswersNothingFromAMemberCutOffFromTheOthers(t *testing.T) {
	cluster := testnats.StartCluster(t, 3)
	cluster.AwaitReady(t)
	client, err := Connect(cluster.URLs())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	js, err := client.Conn.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	kv, err := EnsureKeyValue(client.Conn, js, &nats.KeyValueConfig{Bucket: "read-latest-cut-off", Replicas: 3, Storage: nats.MemoryStorage})
	if err != nil {
		t.Fatalf("create the bucket: %v", err)
	}
	if _, err := kv.Put("held", []byte("1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	connected := cluster.Member(client.Conn.ConnectedServerName())
	for i := range cluster.Servers {
		if i != connected {
			cluster.Servers[i].Stop(t)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	latest, err := kv.ReadLatest(ctx, []string{"held"})
	if err == nil || latest != nil || !strings.HasPrefix(err.Error(), "write the read barrier of KV bucket read-latest-cut-off") {
		t.Fatalf("ReadLatest on the member left = %v, %v; want nothing and the barrier's write refused", latest, err)
	}
}

// Every call through a KeyValue refuses BarrierKey before it sends anything, as a refusal
// (ErrReservedKey, an ErrRefused): no store writes, deletes or reads a record under it, so no record
// a store keeps can be the barrier, and none can be removed by it.
func TestEveryKeyValueCallRefusesTheBarrierKey(t *testing.T) {
	_, kv := leaderReadBucket(t, "read-latest-reserved")
	refusals := map[string]error{
		"Get":         second(kv.Get(BarrierKey)),
		"RefusesRead": kv.RefusesRead(BarrierKey),
		"GetRevision": second(kv.GetRevision(BarrierKey, 1)),
		"Put":         second(kv.Put(BarrierKey, []byte("1"))),
		"PutString":   second(kv.PutString(BarrierKey, "1")),
		"Create":      second(kv.Create(BarrierKey, []byte("1"))),
		"Update":      second(kv.Update(BarrierKey, []byte("1"), 1)),
		"Delete":      kv.Delete(BarrierKey),
		"DeleteAtRead": func() error {
			_, err := kv.DeleteAtRead(BarrierKey)
			return err
		}(),
		"Purge":             kv.Purge(BarrierKey),
		"WatchKeys":         second(kv.WatchKeys(context.Background(), BarrierKey)),
		"WatchFilteredKeys": second(kv.WatchFilteredKeys(context.Background(), []string{"other", BarrierKey})),
		"GetFromLeader":     second(kv.GetFromLeader(context.Background(), BarrierKey)),
	}
	for call, err := range refusals {
		if !errors.Is(err, ErrReservedKey) || !errors.Is(err, ErrRefused) {
			t.Errorf("%s of the barrier key = %v, want %v (an ErrRefused)", call, err, ErrReservedKey)
		}
	}
	if revision := barrierRevisionOrZero(t, kv); revision != 0 {
		t.Fatalf("the bucket holds a barrier record at %d after calls that refuse its key, want none", revision)
	}
}

func second[T any](_ T, err error) error { return err }

// barrierRevision is the revision of the bucket's latest barrier record.
func barrierRevision(t *testing.T, kv KeyValue) uint64 {
	t.Helper()
	revision := barrierRevisionOrZero(t, kv)
	if revision == 0 {
		t.Fatal("the bucket holds no barrier record")
	}
	return revision
}

// barrierRevisionOrZero is the revision of the bucket's latest barrier record, 0 when it holds none.
func barrierRevisionOrZero(t *testing.T, kv KeyValue) uint64 {
	t.Helper()
	js, err := kv.streams.Conn().JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	message, err := js.GetLastMsg("KV_"+kv.Bucket(), "$KV."+kv.Bucket()+"."+BarrierKey)
	if errors.Is(err, nats.ErrMsgNotFound) {
		return 0
	}
	if err != nil {
		t.Fatalf("read the barrier record: %v", err)
	}
	return message.Sequence
}

// scriptedEntry is a record a scriptedWatch delivers.
type scriptedEntry struct {
	key      string
	revision uint64
	op       jetstream.KeyValueOp
}

func (e scriptedEntry) Bucket() string                  { return "scripted" }
func (e scriptedEntry) Key() string                     { return e.key }
func (e scriptedEntry) Value() []byte                   { return []byte("1") }
func (e scriptedEntry) Revision() uint64                { return e.revision }
func (e scriptedEntry) Created() time.Time              { return time.Time{} }
func (e scriptedEntry) Delta() uint64                   { return 0 }
func (e scriptedEntry) Operation() jetstream.KeyValueOp { return e.op }

// scriptedWatch is a bucket handle whose filtered watch delivers script, in order, then nothing
// until it is stopped: the watch of a store whose state the script is.
type scriptedWatch struct {
	jetstream.KeyValue
	script []jetstream.KeyValueEntry
}

func (w scriptedWatch) WatchFiltered(context.Context, []string, ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
	updates := make(chan jetstream.KeyValueEntry, len(w.script))
	for _, entry := range w.script {
		updates <- entry
	}
	return &scriptedWatcher{updates: updates}, nil
}

type scriptedWatcher struct {
	updates chan jetstream.KeyValueEntry
	stopped atomic.Bool
}

func (w *scriptedWatcher) Updates() <-chan jetstream.KeyValueEntry { return w.updates }

func (w *scriptedWatcher) Stop() error {
	if w.stopped.CompareAndSwap(false, true) {
		close(w.updates)
	}
	return nil
}

// countedWatch is a bucket handle that counts its filtered watches.
type countedWatch struct {
	jetstream.KeyValue
	watches *atomic.Int32
}

func (w countedWatch) WatchFiltered(ctx context.Context, keys []string, opts ...jetstream.WatchOpt) (jetstream.KeyWatcher, error) {
	w.watches.Add(1)
	return w.KeyValue.WatchFiltered(ctx, keys, opts...)
}

// ackedAt is a jetstream context whose publishes are acknowledged at sequence without being sent.
type ackedAt struct {
	jetstream.JetStream
	sequence uint64
}

func (s ackedAt) PublishMsg(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return &jetstream.PubAck{Sequence: s.sequence}, nil
}

// refusedPublish is a jetstream context whose publishes fail with err.
type refusedPublish struct {
	jetstream.JetStream
	err error
}

func (s refusedPublish) PublishMsg(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return nil, s.err
}
