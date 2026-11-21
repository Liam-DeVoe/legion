package kvwatch_test

import (
	"context"
	"sync"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/sjawhar/envoy/internal/kvwatch"
	"github.com/sjawhar/envoy/internal/testnats"
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

// A key written through to the copy after a read's watcher was created, and before that read
// began, survives the read's completion. The bucket holds it, but the watcher's snapshot does not:
// the scan delivers nothing of it, and the watcher delivers its record only after the read
// completed. Its revision is past the stream's last sequence that the watch read before it created
// the watcher, and that is the read's evidence of it (kvwatch.Copy.BeginRead). A copy that dropped
// it would answer without a key its own write had just put there until that update arrived, and
// for good when the connection closed first. A key the copy held from before, which the bucket
// does not hold, is still swept.
func TestAKeyWrittenThroughBetweenAReadsCreateAndItsBeginSurvivesTheRead(t *testing.T) {
	uri := testnats.URL(t)
	_, kv := bucket(t, uri)
	putKeys(t, kv, "one")
	into := newSeen()
	into.hold("gone-from-the-bucket")
	held := newCreatedThenWritten(t)
	w := kvwatch.New("test cache", scripted(kv, held.step()).handle(), into, kvwatch.WithStreamProbeInterval(0))
	t.Cleanup(w.Stop)
	w.Start()
	wait(t, held.scanned, "the read's watcher to receive its whole scan")

	revision, err := kv.Put("written-through", []byte("1"))
	if err != nil {
		t.Fatalf("put written-through: %v", err)
	}
	if outcome := into.put(w, "written-through", revision); outcome != kvwatch.Applied {
		t.Fatalf("the write into the copy came to outcome %d, want Applied (%d)", outcome, kvwatch.Applied)
	}
	held.let()
	eventually(t, "the read's completion", w.Following)
	if !into.has("written-through") || into.has("gone-from-the-bucket") || !into.has("one") {
		t.Fatalf("after the read: written-through key %v, gone key %v, bucket key %v; want the written key and the bucket's kept, the gone key swept",
			into.has("written-through"), into.has("gone-from-the-bucket"), into.has("one"))
	}
}
