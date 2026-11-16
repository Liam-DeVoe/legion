package kvwatch_test

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/kvwatch"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A store captures its handle and epoch before it writes (Watcher.Handle; the session registry's
// PutWithin among them). A write it began before a bucket was deleted and created again can land in
// the new bucket after the new read's consumer was made, and its result reach the copy after the
// install moved the epoch and before the read began: the copy refuses that result for its epoch,
// and no epoch can say which bucket took it. The read still keeps the key, because the bucket
// holds it.
func TestARecreatedReadKeepsAnOldHandlesResultTakenAfterTheEpochMoved(t *testing.T) {
	const key = "written-through-the-old-handle"
	uri := testnats.URL(t)
	conn, kv := bucket(t, uri)
	cache := newWindowCache(2)
	timers := &pending{}
	w := kvwatch.New("test cache", kv, cache, kvwatch.WithAfterFunc(timers.afterFunc()), kvwatch.WithStreamProbeInterval(0))
	t.Cleanup(w.Stop)
	t.Cleanup(cache.letGo)
	w.Start()
	eventually(t, "the first read's completion", func() bool { return len(cache.completions()) == 1 })
	oldKV, oldEpoch := w.Handle()

	js := jetStream(t, conn)
	deleteTestBucket(t, js)
	recreated := testnats.RecreateKeyValue(t, js, &natsgo.KeyValueConfig{Bucket: testBucketName})
	outcomes := make(chan kvwatch.Outcome, 1)
	cache.beforeTarget(func(kvwatch.Read) {
		if epoch := w.Epoch(); epoch == oldEpoch {
			t.Errorf("before the recreated read began the epoch is still %d: the install did not move it", epoch)
		}
		revision, err := oldKV.Put(key, []byte("1"))
		if err != nil {
			t.Errorf("put %s through the old handle: %v", key, err)
			outcomes <- kvwatch.Applied
			return
		}
		outcomes <- cache.putAt(w, oldEpoch, key, revision)
	})
	if err := kvwatch.WatchOn(w, wrap(t, conn, recreated)); err != nil {
		t.Fatalf("the recreated bucket's watch: %v", err)
	}
	if outcome := <-outcomes; outcome != kvwatch.Dropped {
		t.Fatalf("the old handle's result came to outcome %d after the epoch moved, want Dropped (%d)", outcome, kvwatch.Dropped)
	}
	eventually(t, "the recreated bucket's read to complete", cache.targetDone)

	cache.requireNotScanned(t, key)
	if got, want := bucketKeys(t, kv), []string{key}; !slices.Equal(got, want) {
		t.Fatalf("the new bucket holds %v, want %v", got, want)
	}
	if !cache.has(key) {
		t.Fatalf("the recreated bucket's read left %s out of the copy, which the new bucket holds: the copy holds %v", key, cache.keyList())
	}
}

// A key the scan delivered can still be gone from the bucket by the time the read reads it: a write
// updated it in the window, through the copy, and then a delete removed it from the bucket alone,
// while nothing else delivered that delete (the connection the replaced watcher read through is
// gone, as a reconnect's read finds it). The bucket's answer that it holds no record of the key
// removes the scanned row. The same answer leaves a key alone once the copy took a write of it
// after the read of the key began.
func TestAReadRemovesAScannedKeyTheBucketDeletedInItsWindow(t *testing.T) {
	const (
		scannedThenDeleted = "scanned-then-deleted"
		rewrittenMidRead   = "rewritten-while-it-is-read"
	)
	uri := testnats.URL(t)
	lost, kv := bucket(t, uri)
	putKeys(t, kv, scannedThenDeleted, rewrittenMidRead)
	cache := newWindowCache(2)
	timers := &pending{}
	w := kvwatch.New("test cache", kv, cache, kvwatch.WithAfterFunc(timers.afterFunc()), kvwatch.WithStreamProbeInterval(0))
	t.Cleanup(w.Stop)
	t.Cleanup(cache.letGo)
	w.Start()
	eventually(t, "the first read's completion", func() bool { return len(cache.completions()) == 1 })

	// The connection the first watcher reads through is lost; the reconnect's read is installed
	// through a handle on another one, and holds once it made its consumer.
	lost.Close()
	eventually(t, "the first watcher's end", func() bool { return w.Err() != nil })
	_, live := bucket(t, uri)
	held := newHeldCreate(t)
	returned := watchOnInBackground(t, w, scripted(live, held.step()).handle())
	wait(t, held.created, "the reconnect's watch to make its consumer")
	epoch := w.Epoch()
	for _, key := range []string{scannedThenDeleted, rewrittenMidRead} {
		if outcome := cache.putThrough(t, w, live, epoch, key); outcome != kvwatch.Applied {
			t.Fatalf("the window write of %s came to outcome %d, want Applied (%d)", key, outcome, kvwatch.Applied)
		}
		if err := live.Delete(key); err != nil {
			t.Fatalf("delete %s from the bucket: %v", key, err)
		}
	}
	// Between the bucket's not-found for rewrittenMidRead and the copy taking it, a write of that
	// key lands and the copy takes it, as a write-through racing the read does.
	var rewritten atomic.Uint64
	cache.onAbsent(func(key string) {
		if key != rewrittenMidRead {
			return
		}
		revision, err := live.Put(key, []byte("again"))
		if err != nil {
			t.Errorf("put %s again: %v", key, err)
			return
		}
		if outcome := cache.putAt(w, w.Epoch(), key, revision); outcome != kvwatch.Applied {
			t.Errorf("the write of %s after its read began came to outcome %d, want Applied (%d)", key, outcome, kvwatch.Applied)
		}
		rewritten.Store(revision)
	})
	held.let()
	returned()
	eventually(t, "the reconnect's read to complete", cache.targetDone)

	cache.requireScanned(t, scannedThenDeleted, rewrittenMidRead)
	if cache.has(scannedThenDeleted) {
		t.Fatalf("the completed read kept %s, which its scan delivered and the bucket then deleted: the copy holds %v", scannedThenDeleted, cache.keyList())
	}
	if !cache.has(rewrittenMidRead) {
		t.Fatalf("the completed read removed %s, written again after its read began: the copy holds %v", rewrittenMidRead, cache.keyList())
	}
	if revision := rewritten.Load(); revision == 0 || cache.fenceOf(w, rewrittenMidRead) != revision {
		t.Fatalf("the copy's fence for %s is %d, want the revision written after its read began (%d)", rewrittenMidRead, cache.fenceOf(w, rewrittenMidRead), revision)
	}
}

// A run of reads that cannot read their unseen keys from the bucket - its leader does not answer
// them - is read again less and less often: 5, 10, 20 and 40 s apart, then once a minute. Nothing is
// removed and no read completes meanwhile.
func TestReadsWhoseUnseenKeysCannotBeReadAreReadAgainLessOften(t *testing.T) {
	const heldByTheCopy = "held-by-the-copy"
	w, into, timers, _ := failingLeaderWatcher(t, heldByTheCopy, nil)
	w.Start()

	fired := map[*pendingWait]bool{}
	for i, want := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute} {
		var next *pendingWait
		eventually(t, fmt.Sprintf("re-read %d, after %v", i+1, want), func() bool {
			for _, wait := range timers.armed(want) {
				if !wait.stopped.Load() && !fired[wait] {
					next = wait
					return true
				}
			}
			return false
		})
		fired[next] = true
		next.fire()
	}
	if done := len(into.completions()); done != 0 || !into.has(heldByTheCopy) {
		t.Fatalf("after six failed reads: %d completed, the copy holds %v; want none completed and %s kept", done, into.keyList(), heldByTheCopy)
	}
}

// ReadErr says nothing while a run of reads that cannot read their unseen keys is shorter than the
// copy's stale bound, reports the run once it has lasted the bound, naming the reads' own error,
// and says nothing again once a read completes.
func TestReadErrReportsAReadFailureThatOutlastsTheStaleBound(t *testing.T) {
	const heldByTheCopy = "held-by-the-copy"
	clock := newClock()
	w, into, timers, failing := failingLeaderWatcher(t, heldByTheCopy, clock)
	w.Start()
	eventually(t, "the first re-read to be armed", func() bool { return len(timers.armed(5*time.Second)) == 1 })

	if err := w.ReadErr(); err != nil {
		t.Fatalf("ReadErr right after the first failed read = %v, want nil", err)
	}
	clock.advance(2*time.Minute - time.Second)
	if err := w.ReadErr(); err != nil {
		t.Fatalf("ReadErr a second before the 2m stale bound = %v, want nil", err)
	}
	clock.advance(2 * time.Second)
	err := w.ReadErr()
	if !errors.Is(err, kvwatch.ErrUnverified) || !errors.Is(err, natsgo.ErrConnectionClosed) {
		t.Fatalf("ReadErr past the stale bound = %v, want %v wrapping the reads' own %v", err, kvwatch.ErrUnverified, natsgo.ErrConnectionClosed)
	}

	failing.Store(false)
	timers.last(t, 5*time.Second).fire()
	eventually(t, "the read after the leader answers to complete", func() bool { return len(into.completions()) == 1 })
	if err := w.ReadErr(); err != nil || into.has(heldByTheCopy) {
		t.Fatalf("after a read completed: ReadErr %v, the copy holds %v; want nil and %s removed", err, into.keyList(), heldByTheCopy)
	}
}

// failingLeaderWatcher is a watcher, not yet started, on a bucket whose leader-answered key reads
// fail while the returned flag is set (it is at first), over a copy that holds held, a key the
// bucket does not, so every read has a key to read. clock is the watcher's clock when not nil. Its
// waits are the returned pending's, its scans' idle wait a long one no other wait has, and its
// stream probe off.
func failingLeaderWatcher(t *testing.T, held string, clock *clock) (*kvwatch.Watcher, *seen, *pending, *atomic.Bool) {
	t.Helper()
	uri := testnats.URL(t)
	_, kv := bucket(t, uri)
	closed := testnats.Connect(t, uri)
	closed.Close()
	failing := &atomic.Bool{}
	failing.Store(true)
	handle := bus.NewKeyValue(kv.Legacy(), kv.WatchHandle(), failingLeaderReads{JetStream: kv.Streams(), closed: closed, failing: failing})
	into := newSeen()
	into.hold(held)
	timers := &pending{}
	options := []kvwatch.Option{kvwatch.WithAfterFunc(timers.afterFunc()), kvwatch.WithStreamProbeInterval(0), kvwatch.WithScanIdleTimeout(7 * time.Hour)}
	if clock != nil {
		options = append(options, kvwatch.WithNow(clock.Now))
	}
	w := kvwatch.New("test cache", handle, into, options...)
	t.Cleanup(w.Stop)
	return w, into, timers, failing
}
