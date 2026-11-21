package kvwatch_test

import (
	"context"
	"sync"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/sjawhar/envoy/internal/kvwatch"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// createdThenWritten is a watch that creates its watcher on the bucket and holds it until the test
// lets it go, as heldCreate does, once the watcher has received its whole scan, its nil included:
// a record the test writes while it holds is one the watcher's snapshot does not hold, which the
// watcher delivers only after its nil. It holds those later records back until the test delivers
// them, so the read completes with no delivery of them, as it does when such an update has not
// arrived by then. scanned closes once the scan's nil has arrived.
type createdThenWritten struct {
	scanned, release, deliver chan struct{}
	letOnce, deliverOnce      sync.Once
}

func newCreatedThenWritten(t *testing.T) *createdThenWritten {
	h := &createdThenWritten{scanned: make(chan struct{}), release: make(chan struct{}), deliver: make(chan struct{})}
	t.Cleanup(h.let)
	t.Cleanup(h.deliverLater)
	return h
}

// let hands the created watcher to the watch.
func (h *createdThenWritten) let() { h.letOnce.Do(func() { close(h.release) }) }

// deliverLater forwards the records the watcher delivered after its scan.
func (h *createdThenWritten) deliverLater() { h.deliverOnce.Do(func() { close(h.deliver) }) }

// step is the watch that holds its create on h.
func (h *createdThenWritten) step() watchStep {
	return func(ctx context.Context, kv jetstream.KeyValue) (jetstream.KeyWatcher, error) {
		watcher, err := kv.WatchAll(ctx)
		if err != nil {
			return nil, err
		}
		// The scan is forwarded while the watch is held, so its buffer holds the bucket's few
		// records and the nil.
		updates := make(chan jetstream.KeyValueEntry, 16)
		go func() {
			defer close(updates)
			scanning := true
			for entry := range watcher.Updates() {
				if !scanning {
					<-h.deliver
				}
				updates <- entry
				if scanning && entry == nil {
					scanning = false
					close(h.scanned)
				}
			}
		}()
		<-h.release
		return &forwardedWatcher{KeyWatcher: watcher, updates: updates}, nil
	}
}

// holdAt puts key in the copy at revision as if an earlier read had left it there, before any read
// begins.
func (s *seen) holdAt(key string, revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.keys.Delivered(kvwatch.Entry{Key: key, Op: kvwatch.OpPut, Revision: revision})
}

// A key written through to the copy after a read's watcher was created, and before that read
// began, survives the read's completion. The bucket holds it, but the watcher's snapshot does not:
// the scan delivers nothing of it, and the watcher delivers its record only after the scan's end.
// Its revision is past the stream's last sequence that the watch read before it created the
// watcher, and that is the read's evidence of it (kvwatch.Copy.BeginRead). A copy that dropped it
// would answer without a key its own write had just put there until that update arrived, and until
// the next read when the connection closed first. A key the copy held from before, at a revision
// the stream had reached by then and which the bucket no longer holds, is still swept.
func TestAKeyWrittenThroughBetweenAReadsCreateAndItsBeginSurvivesTheRead(t *testing.T) {
	uri := testnats.URL(t)
	_, kv := bucket(t, uri)
	putKeys(t, kv, "one")
	into := newSeen()
	into.holdAt("gone-from-the-bucket", 1)
	held := newCreatedThenWritten(t)
	w := kvwatch.New("test cache", scripted(kv, held.step()).handle(), into, kvwatch.WithStreamProbeInterval(0))
	t.Cleanup(w.Stop)
	w.Start()
	testwait.Receive(t, "the read's watcher to receive its whole scan", held.scanned)

	revision, err := kv.Put("written-through", []byte("1"))
	if err != nil {
		t.Fatalf("put written-through: %v", err)
	}
	if outcome := into.put(w, "written-through", revision); outcome != kvwatch.Applied {
		t.Fatalf("the write into the copy came to outcome %d, want Applied (%d)", outcome, kvwatch.Applied)
	}
	held.let()
	testwait.Until(t, "the read's completion", func() (bool, bool) {
		following := w.Following()
		return following, following
	})
	if !into.has("written-through") || into.has("gone-from-the-bucket") || !into.has("one") {
		t.Fatalf("after the read: written-through key %v, gone key %v, bucket key %v; want the written key and the bucket's kept, the gone key swept",
			into.has("written-through"), into.has("gone-from-the-bucket"), into.has("one"))
	}
}

// A read that begins at a stream's last sequence below the one the read before it began at marks
// none of the keys the copy took past it. The bucket's sequence space moved back under the same
// stream: a JetStream restore keeps the stream's creation time, so the watch reads no recreated
// bucket. A revision the copy took before the restore says nothing about the restored bucket, and
// marking it would keep a key the restored bucket does not hold through every read until the
// stream's sequence passed it. A read that begins further on keeps such a key.
func TestAReadWhoseStreamMovedBackMarksNothingPastIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lastSeq uint64
		kept    bool
	}{
		{name: "the stream moved on to below the key's revision", lastSeq: 35, kept: true},
		{name: "the stream moved back below the read before", lastSeq: 25, kept: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copied := kvwatch.NewCopy(kvwatch.Rows[bool]{Decode: func(kvwatch.Entry) (bool, bool, error) { return true, true, nil }})
			copied.BeginRead(kvwatch.Read{LastSeq: 30})
			copied.CompleteRead()
			// Taken while the copy follows its bucket, at a revision past that read's start.
			if err := copied.Delivered(kvwatch.Entry{Key: "taken-after", Op: kvwatch.OpPut, Revision: 40}); err != nil {
				t.Fatalf("deliver taken-after: %v", err)
			}
			copied.BeginRead(kvwatch.Read{LastSeq: tc.lastSeq})
			copied.CompleteRead()
			if _, kept := copied.Get("taken-after"); kept != tc.kept {
				t.Fatalf("a read beginning at %d after one at 30: the key at revision 40 kept %v, want %v", tc.lastSeq, kept, tc.kept)
			}
		})
	}
}
