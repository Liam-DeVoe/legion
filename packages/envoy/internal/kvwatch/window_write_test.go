package kvwatch_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/kvwatch"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The keys of TestAReadKeepsAWriteThatLandedAfterItsConsumerWasMade.
const (
	// writtenInTheWindow is written into the bucket, and into the copy as a write-through, after the
	// read's watch made its consumer and before the read began: the read's scan does not deliver it,
	// and the bucket holds it.
	writtenInTheWindow = "written-in-the-window"
	// deletedInTheWindow is written and then deleted in the same window, bucket and copy both.
	deletedInTheWindow = "deleted-in-the-window"
	// goneFromTheBucket is a row the copy holds whose every record was purged from the bucket
	// before the read's watch read the stream, leaving no delete marker: no read delivers it again.
	goneFromTheBucket = "gone-from-the-bucket"
)

// A write that lands after a read's watch made its consumer and before the read began
// (Cache.BeginRead) is not in that read's scan: the consumer delivers it only after the scan's end.
// The read's completion must keep the row such a write left in the copy, which the bucket holds,
// rather than sweep it as a row the read found no evidence of: a swept row is missing from a copy
// that serves as complete until that later delivery, and TestSessionsHandler_IncludesTitle
// (cmd/listener) saw a session it had just written go missing so (INFRA-4048). Every read path
// begins its read this way: the first read (Start), a reconnect's read (Rewatch, here through
// WatchOn on the handle a Rewatch would open), a resync (a scheduled read, re-read through the
// current handle) and the read of a bucket deleted and created again, whose window write is the
// new bucket's while the copy's fence still holds the old bucket's revisions. Each write goes as a
// store's does: through the epoch the watcher's Handle names before the write is made.
//
// The other two keys are the control that the read still removes what it should: a key deleted in
// the same window is not in the copy once the read completes, and a row whose records the bucket
// no longer holds at all is swept. A read that kept every row would fail the second.
func TestAReadKeepsAWriteThatLandedAfterItsConsumerWasMade(t *testing.T) {
	for _, path := range []struct {
		name string
		// read is which read the watcher begins is the one under test: 1 for its first.
		read int
		// recreated is whether the read under test reads a bucket deleted and created again.
		recreated bool
		// handle is the handle the watcher starts on, whose watch held, when it holds one, is the
		// read under test's.
		handle func(kv bus.KeyValue, held *heldCreate) bus.KeyValue
		// options are the watcher's options beyond the ones every path sets.
		options []kvwatch.Option
		// begin begins the read under test, whose watch holds in held once it made its consumer,
		// and returns what waits for begin's own call to return once held lets the watch go.
		begin func(t *testing.T, w *kvwatch.Watcher, conn *natsgo.Conn, kv bus.KeyValue, held *heldCreate, timers *pending) func()
	}{
		{
			name:   "the first read",
			read:   1,
			handle: func(kv bus.KeyValue, held *heldCreate) bus.KeyValue { return scripted(kv, held.step()).handle() },
			begin: func(_ *testing.T, w *kvwatch.Watcher, _ *natsgo.Conn, _ bus.KeyValue, _ *heldCreate, _ *pending) func() {
				w.Start()
				return func() {}
			},
		},
		{
			name:   "a reconnect's read",
			read:   2,
			handle: func(kv bus.KeyValue, _ *heldCreate) bus.KeyValue { return kv },
			begin: func(t *testing.T, w *kvwatch.Watcher, _ *natsgo.Conn, kv bus.KeyValue, held *heldCreate, _ *pending) func() {
				w.Disconnected()
				return watchOnInBackground(t, w, scripted(kv, held.step()).handle())
			},
		},
		{
			name: "a resync",
			read: 2,
			handle: func(kv bus.KeyValue, held *heldCreate) bus.KeyValue {
				return scripted(kv, nil, held.step()).handle()
			},
			options: []kvwatch.Option{kvwatch.WithScheduledRead(scheduledEvery)},
			begin: func(t *testing.T, _ *kvwatch.Watcher, _ *natsgo.Conn, _ bus.KeyValue, _ *heldCreate, timers *pending) func() {
				scheduled := timers.last(t, scheduledEvery)
				done := make(chan struct{})
				go func() {
					defer close(done)
					scheduled.fire()
				}()
				return func() {
					t.Helper()
					wait(t, done, "the scheduled read's watch to return")
				}
			},
		},
		{
			name:      "a recreated bucket's read",
			read:      2,
			recreated: true,
			handle:    func(kv bus.KeyValue, _ *heldCreate) bus.KeyValue { return kv },
			begin: func(t *testing.T, w *kvwatch.Watcher, conn *natsgo.Conn, _ bus.KeyValue, held *heldCreate, _ *pending) func() {
				js := jetStream(t, conn)
				deleteTestBucket(t, js)
				recreated := testnats.RecreateKeyValue(t, js, &natsgo.KeyValueConfig{Bucket: testBucketName})
				return watchOnInBackground(t, w, scripted(wrap(t, conn, recreated), held.step()).handle())
			},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			uri := testnats.URL(t)
			conn, kv := bucket(t, uri)
			goneRevision, err := kv.Put(goneFromTheBucket, []byte("1"))
			if err != nil {
				t.Fatalf("put %s: %v", goneFromTheBucket, err)
			}
			held := newHeldCreate(t)
			window := &windowDeliveries{into: newSeen(), read: path.read, let: make(chan struct{})}
			timers := &pending{}
			options := append([]kvwatch.Option{kvwatch.WithAfterFunc(timers.afterFunc()), kvwatch.WithStreamProbeInterval(0)}, path.options...)
			w := kvwatch.New("test cache", path.handle(kv, held), withApply{seen: window.into, before: window.before}, options...)
			t.Cleanup(w.Stop)
			// Runs before Stop: a delivery held after the read under test completed holds the
			// watcher's apply lock.
			t.Cleanup(window.letGo)

			// The copy holds the gone key before the read under test: the first read's copy as a
			// write-through left it, a later read's as the first read delivered it.
			if path.read == 1 {
				if outcome := window.into.put(w, goneFromTheBucket, goneRevision); outcome != kvwatch.Applied {
					t.Fatalf("the write of %s into the copy came to outcome %d, want Applied (%d)", goneFromTheBucket, outcome, kvwatch.Applied)
				}
			} else {
				w.Start()
				eventually(t, "the first read's completion", func() bool { return len(window.into.completions()) == 1 })
				if !window.into.has(goneFromTheBucket) {
					t.Fatalf("the first read did not deliver %s", goneFromTheBucket)
				}
			}
			purgeKey(t, conn, goneFromTheBucket)
			if _, err := kv.Get(goneFromTheBucket); !errors.Is(err, natsgo.ErrKeyNotFound) {
				t.Fatalf("read %s after the purge: %v, want %v", goneFromTheBucket, err, natsgo.ErrKeyNotFound)
			}

			returned := path.begin(t, w, conn, kv, held, timers)
			wait(t, held.created, "the read's watch to make its consumer")
			// The window: the watch has made its consumer and holds before it takes the watcher's
			// locks, so the read has not begun. Each write reaches the copy as a store's
			// write-through does once its write answered.
			if outcome := window.into.writeThrough(t, w, writtenInTheWindow); outcome != kvwatch.Applied {
				t.Fatalf("the write of %s into the copy came to outcome %d, want Applied (%d)", writtenInTheWindow, outcome, kvwatch.Applied)
			}
			if outcome := window.into.writeThrough(t, w, deletedInTheWindow); outcome != kvwatch.Applied {
				t.Fatalf("the write of %s into the copy came to outcome %d, want Applied (%d)", deletedInTheWindow, outcome, kvwatch.Applied)
			}
			if outcome := window.into.deleteThrough(t, w, deletedInTheWindow); outcome != kvwatch.Applied {
				t.Fatalf("the delete of %s from the copy came to outcome %d, want Applied (%d)", deletedInTheWindow, outcome, kvwatch.Applied)
			}

			held.let()
			returned()
			eventually(t, "the read's completion", func() bool { return len(window.into.completions()) == path.read })
			if reads := window.into.reads(); len(reads) != path.read || reads[path.read-1].Recreated != path.recreated {
				t.Fatalf("reads begun %+v, want %d, the last one Recreated %v", reads, path.read, path.recreated)
			}
			if scanned := window.scannedKeys(); slices.Contains(scanned, writtenInTheWindow) || slices.Contains(scanned, deletedInTheWindow) {
				t.Fatalf("the read's scan delivered %v: the writes did not land after its consumer was made", scanned)
			}
			if got, want := bucketKeys(t, kv), []string{writtenInTheWindow}; !slices.Equal(got, want) {
				t.Fatalf("the bucket holds %v, want %v", got, want)
			}
			// The control first: the read still removes what it should.
			if window.into.has(deletedInTheWindow) || window.into.has(goneFromTheBucket) {
				t.Fatalf("the completed read left the copy holding %v, want %s and %s out of it", window.into.keyList(), deletedInTheWindow, goneFromTheBucket)
			}
			if !window.into.has(writtenInTheWindow) {
				t.Fatalf("the completed read swept %s, which the bucket holds: the copy holds %v", writtenInTheWindow, window.into.keyList())
			}

			// Once the deliveries after the scan go on, the copy holds what the bucket holds.
			window.letGo()
			eventually(t, "the copy to hold what the bucket holds", func() bool {
				return slices.Equal(window.into.keyList(), bucketKeys(t, kv))
			})
		})
	}
}

// windowDeliveries is the apply hook of TestAReadKeepsAWriteThatLandedAfterItsConsumerWasMade
// (withApply): it records the keys the scan of the read under test delivers, and holds every
// record delivered once that read has completed until let closes, so the copy that read's
// completion left can be read before any later delivery changes it.
type windowDeliveries struct {
	into *seen
	// read is which read the watcher begins is the one under test: 1 for its first.
	read    int
	let     chan struct{}
	letOnce sync.Once
	mu      sync.Mutex
	scanned []string
}

func (d *windowDeliveries) before(entry natsgo.KeyValueEntry) {
	switch done := len(d.into.completions()); {
	case done >= d.read:
		<-d.let
	case len(d.into.reads()) == d.read:
		d.mu.Lock()
		defer d.mu.Unlock()
		d.scanned = append(d.scanned, entry.Key())
	}
}

func (d *windowDeliveries) letGo() { d.letOnce.Do(func() { close(d.let) }) }

func (d *windowDeliveries) scannedKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.scanned)
}

// writeThrough writes key into the bucket and hands the copy that write as a store's write-through
// does: through the handle and epoch the watcher's Handle names before the write is made
// (kvwatch.Copy.Put). It returns what became of the write in the copy.
func (s *seen) writeThrough(t *testing.T, w *kvwatch.Watcher, key string) kvwatch.Outcome {
	t.Helper()
	kv, epoch := w.Handle()
	revision, err := kv.Put(key, []byte("1"))
	if err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys.Put(w, epoch, key, true, revision)
}

// deleteThrough deletes key from the bucket and hands the copy that delete at its marker's
// revision, as a store's delete does when it cannot read its own record back (kvwatch.Copy.Evict),
// through the handle and epoch the watcher's Handle names before the delete is made. It returns
// what became of the delete in the copy.
func (s *seen) deleteThrough(t *testing.T, w *kvwatch.Watcher, key string) kvwatch.Outcome {
	t.Helper()
	kv, epoch := w.Handle()
	if err := kv.Delete(key); err != nil {
		t.Fatalf("delete %s: %v", key, err)
	}
	revision := lastRevision(t, kv, key)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys.Evict(w, epoch, key, revision)
}

// watchOnInBackground installs a read on kv through WatchOn, as a Rewatch does once it has opened
// its handle, from a goroutine of its own, since kv's watch holds; it returns what waits for that
// call to return once the watch is let go.
func watchOnInBackground(t *testing.T, w *kvwatch.Watcher, kv bus.KeyValue) func() {
	done := make(chan error, 1)
	go func() { done <- kvwatch.WatchOn(w, kv) }()
	return func() {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the watch: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for the watch to return")
		}
	}
}

// purgeKey removes every record of key from the test bucket's stream and leaves no marker in
// their place, so no read of the bucket delivers key again.
func purgeKey(t *testing.T, conn *natsgo.Conn, key string) {
	t.Helper()
	subject := "$KV." + testBucketName + "." + key
	if err := jetStream(t, conn).PurgeStream("KV_"+testBucketName, &natsgo.StreamPurgeRequest{Subject: subject}); err != nil {
		t.Fatalf("purge %s: %v", subject, err)
	}
}

// lastRevision is the revision of key's latest record in the bucket kv is a handle on: a delete's
// own marker once key was deleted.
func lastRevision(t *testing.T, kv bus.KeyValue, key string) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	history, err := kv.ReadHistory(ctx, key)
	if err != nil || len(history) == 0 {
		t.Fatalf("read %s's history: %d records, %v", key, len(history), err)
	}
	return history[len(history)-1].Revision()
}
