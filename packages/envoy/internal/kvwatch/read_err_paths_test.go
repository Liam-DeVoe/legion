package kvwatch_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/kvwatch"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The tests of this file pin each path into ReadErr's count, and each way out of it, that no other
// test pins; Watcher.ReadErr's doc names every path and the test that pins it. The others are a
// barrier that is not written and a run that a completed read ends
// (TestReadErrReportsAReadFailureThatOutlastsTheStaleBound), a wait that times out on a stream
// check that answers and a wait still under way (TestAReadsWaitPastItsBarrierIsBoundedAndReported),
// and a wait its generation ends (TestAWaitItsGenerationEndedIsNoWaitToReadErr).

// pastTheBound is how far a test moves the watcher's clock before it reads ReadErr: past the
// 2-minute stale bound.
const pastTheBound = 2*time.Minute + time.Second

// unreadStreams is a bucket's jetstream context whose reads of a stream (STREAM.INFO, the stream
// check's) fail while unread is set, counting those it failed. With ackAt set, its publishes are
// acknowledged at ackAt without being sent, so a read's consumer never reaches its barrier. With
// unreadOnPublish set, a publish it sent sets unread before it returns, so every stream check
// after a read's barrier fails and every one before it answers.
type unreadStreams struct {
	jetstream.JetStream
	ackAt           uint64
	unreadOnPublish bool
	unread          *atomic.Bool
	failed          *atomic.Int64
}

func (s unreadStreams) PublishMsg(ctx context.Context, msg *natsgo.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	if s.ackAt != 0 {
		return &jetstream.PubAck{Sequence: s.ackAt}, nil
	}
	ack, err := s.JetStream.PublishMsg(ctx, msg, opts...)
	if s.unreadOnPublish {
		s.unread.Store(true)
	}
	return ack, err
}

func (s unreadStreams) Stream(ctx context.Context, name string) (jetstream.Stream, error) {
	if s.unread.Load() {
		s.failed.Add(1)
		return nil, errors.New("test: STREAM.INFO did not answer")
	}
	return s.JetStream.Stream(ctx, name)
}

// closedConn is a bucket's jetstream context whose connection, the one the key list
// (bus.KeyValue.StoredKeys) and the leader's reads (bus.KeyValue.HeldFromLeader) send on, is
// closed, while its barrier writes, stream reads and watches go through the open one it embeds.
type closedConn struct {
	jetstream.JetStream
	closed *natsgo.Conn
}

func (s closedConn) Conn() *natsgo.Conn { return s.closed }

// readErrWatcher is a watcher, not yet started, over handle and a copy that holds each of held,
// on clock, with the package's held waits, a long scan idle wait that no other wait has and no
// stream probe.
func readErrWatcher(t *testing.T, handle bus.KeyValue, clock *clock, held ...string) (*kvwatch.Watcher, *seen, *pending) {
	t.Helper()
	into := newSeen()
	for _, key := range held {
		into.hold(key)
	}
	timers := &pending{}
	w := kvwatch.New("test cache", handle, into, kvwatch.WithNow(clock.Now), kvwatch.WithAfterFunc(timers.afterFunc()),
		kvwatch.WithStreamProbeInterval(0), kvwatch.WithScanIdleTimeout(7*time.Hour))
	t.Cleanup(w.Stop)
	return w, into, timers
}

// countedOnce fails the test unless err is ReadErr's report of a run of one failed read whose
// error wraps want and names what.
func countedOnce(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, kvwatch.ErrUnverified) || !strings.Contains(err.Error(), "over 1 reads") ||
		(want != nil && !errors.Is(err, want)) || !strings.Contains(err.Error(), what) {
		t.Fatalf("ReadErr past the stale bound = %v, want %v over 1 read, wrapping %v and naming %q", err, kvwatch.ErrUnverified, want, what)
	}
}

// A read whose wait past its barrier times out counts when the stream check after the timeout
// fails, as it does when the check answers: only a check that finds the bucket replaced leaves the
// wait uncounted (TestReadErrCountsNoWaitThatFoundItsBucketReplaced).
func TestReadErrCountsATimedOutWaitWhoseStreamCheckFails(t *testing.T) {
	_, kv := bucket(t, testnats.URL(t))
	clock := newClock()
	unread, failed := &atomic.Bool{}, &atomic.Int64{}
	w, into, timers := readErrWatcher(t, bus.NewKeyValue(kv.Legacy(), kv.WatchHandle(),
		unreadStreams{JetStream: kv.Streams(), ackAt: 1 << 40, unread: unread, failed: failed}), clock)
	w.Start()
	eventually(t, "the wait past the barrier to be bounded", func() bool { return len(timers.armed(7*time.Hour)) == 2 })

	unread.Store(true)
	clock.advance(7 * time.Hour)
	timers.last(t, 7*time.Hour).fire()
	eventually(t, "the failed wait's re-read to be armed", func() bool { return len(timers.armed(5*time.Second)) == 1 })
	if failed.Load() == 0 {
		t.Fatal("UNKNOWN: the stream check after the timeout did not fail, so this test says nothing")
	}
	clock.advance(pastTheBound)
	countedOnce(t, w.ReadErr(), nil, "no record at or past the read barrier")
	if done := len(into.completions()); done != 0 {
		t.Fatalf("%d reads completed, want none", done)
	}
}

// A read whose consumer has passed its barrier counts when the list of the bucket's keys it then
// asks for fails (bus.KeyValue.StoredKeys): it removes nothing and is read again.
func TestReadErrCountsAReadWhoseKeyListFailsAfterTheBarrier(t *testing.T) {
	const heldByTheCopy = "held-by-the-copy"
	uri := testnats.URL(t)
	_, kv := bucket(t, uri)
	closed := testnats.Connect(t, uri)
	closed.Close()
	clock := newClock()
	w, into, timers := readErrWatcher(t, bus.NewKeyValue(kv.Legacy(), kv.WatchHandle(), closedConn{JetStream: kv.Streams(), closed: closed}), clock, heldByTheCopy)
	w.Start()
	eventually(t, "the failed read's re-read to be armed", func() bool { return len(timers.armed(5*time.Second)) == 1 })

	clock.advance(pastTheBound)
	countedOnce(t, w.ReadErr(), natsgo.ErrConnectionClosed, "list the keys of KV bucket")
	if done := len(into.completions()); done != 0 || !into.has(heldByTheCopy) {
		t.Fatalf("%d reads completed, the copy holds %v; want none completed and %s kept", done, into.keyList(), heldByTheCopy)
	}
}

// A read whose consumer has passed its barrier counts when a leader read of a key it did not
// deliver fails (bus.KeyValue.HeldFromLeader): it removes nothing and is read again. The row of the
// copy here is no valid subject token, which HeldFromLeader refuses before it sends anything, so
// the failure comes at once and every time.
func TestReadErrCountsAReadWhoseLeaderReadFailsAfterTheBarrier(t *testing.T) {
	const notASubject = "held by the copy"
	_, kv := bucket(t, testnats.URL(t))
	clock := newClock()
	w, into, timers := readErrWatcher(t, kv, clock, notASubject)
	w.Start()
	eventually(t, "the failed read's re-read to be armed", func() bool { return len(timers.armed(5*time.Second)) == 1 })

	clock.advance(pastTheBound)
	countedOnce(t, w.ReadErr(), bus.ErrInvalidSubject, "from the stream's leader")
	if done := len(into.completions()); done != 0 || !into.has(notASubject) {
		t.Fatalf("%d reads completed, the copy holds %v; want none completed and %q kept", done, into.keyList(), notASubject)
	}
}

// A read whose scan times out never reaches its barrier: it is read again and counts for nothing.
func TestReadErrCountsNoReadWhoseScanTimedOut(t *testing.T) {
	_, kv := bucket(t, testnats.URL(t))
	clock := newClock()
	w, into, timers := readErrWatcher(t, scripted(kv, stallsAfter(0)).handle(), clock)
	w.Start()
	eventually(t, "the scan's idle wait to be armed", func() bool { return len(timers.armed(7*time.Hour)) == 1 })

	clock.advance(7 * time.Hour)
	timers.last(t, 7*time.Hour).fire()
	eventually(t, "the timed-out read's re-read to be armed", func() bool { return len(timers.armed(5*time.Second)) == 1 })
	clock.advance(pastTheBound)
	if err := w.ReadErr(); err != nil {
		t.Fatalf("ReadErr past the stale bound after a scan that timed out = %v, want nil", err)
	}
	if done := len(into.completions()); done != 0 {
		t.Fatalf("%d reads completed, want none", done)
	}
}

// A read whose consumer has passed its barrier and whose leader reads succeeded, but whose stream
// check at completion fails, completes nothing and is read again, and counts for nothing: only the
// checks after its barrier fail here.
func TestReadErrCountsNoReadWhoseStreamCheckAtCompletionFails(t *testing.T) {
	_, kv := bucket(t, testnats.URL(t))
	clock := newClock()
	unread, failed := &atomic.Bool{}, &atomic.Int64{}
	w, into, timers := readErrWatcher(t, bus.NewKeyValue(kv.Legacy(), kv.WatchHandle(),
		unreadStreams{JetStream: kv.Streams(), unreadOnPublish: true, unread: unread, failed: failed}), clock)
	w.Start()
	eventually(t, "the unconfirmed read's re-read to be armed", func() bool { return len(timers.armed(5*time.Second)) == 1 })
	if failed.Load() == 0 {
		t.Fatal("UNKNOWN: no stream check failed, so this test says nothing")
	}
	if done := len(into.completions()); done != 0 {
		t.Fatalf("%d reads completed while their stream check failed, want none", done)
	}

	clock.advance(pastTheBound)
	if err := w.ReadErr(); err != nil {
		t.Fatalf("ReadErr past the stale bound after a stream check at completion failed = %v, want nil", err)
	}
}

// A read whose wait past its barrier times out on a stream check that finds the bucket deleted
// and created again waits for nothing more: the new bucket is read, and the wait counts for
// nothing.
func TestReadErrCountsNoWaitThatFoundItsBucketReplaced(t *testing.T) {
	conn, kv := bucket(t, testnats.URL(t))
	clock := newClock()
	w, _, timers := readErrWatcher(t, bus.NewKeyValue(kv.Legacy(), kv.WatchHandle(), ackedAt{JetStream: kv.Streams(), sequence: 1 << 40}), clock)
	w.Start()
	eventually(t, "the wait past the barrier to be bounded", func() bool { return len(timers.armed(7*time.Hour)) == 2 })

	js := jetStream(t, conn)
	deleteTestBucket(t, js)
	testnats.RecreateKeyValue(t, js, &natsgo.KeyValueConfig{Bucket: testBucketName})
	clock.advance(7 * time.Hour)
	if err := w.ReadErr(); !errors.Is(err, kvwatch.ErrUnverified) || !strings.Contains(err.Error(), "still waiting") {
		t.Fatalf("ReadErr while the wait is past the stale bound = %v, want the wait reported", err)
	}
	timers.last(t, 7*time.Hour).fire()
	eventually(t, "the replaced wait to end uncounted", func() bool { return w.ReadErr() == nil })
	if n := len(timers.armed(0)); n != 1 {
		t.Fatalf("%d reads of the new bucket armed, want the one the stream check arms", n)
	}
	clock.advance(pastTheBound)
	if err := w.ReadErr(); err != nil {
		t.Fatalf("ReadErr past the stale bound after the replaced wait ended = %v, want nil", err)
	}
}

// A read the connection dropped under may no longer complete: its wait still counts while it is
// under way, and its failure, once the wait times out, counts for nothing and arms no re-read of a
// failed read; the reconnect's Rewatch reads the bucket again.
func TestReadErrCountsNoFailureOfAReadTheConnectionDroppedUnder(t *testing.T) {
	_, kv := bucket(t, testnats.URL(t))
	clock := newClock()
	w, _, timers := readErrWatcher(t, bus.NewKeyValue(kv.Legacy(), kv.WatchHandle(), ackedAt{JetStream: kv.Streams(), sequence: 1 << 40}), clock)
	w.Start()
	eventually(t, "the wait past the barrier to be bounded", func() bool { return len(timers.armed(7*time.Hour)) == 2 })

	w.Disconnected()
	clock.advance(7 * time.Hour)
	if err := w.ReadErr(); !errors.Is(err, kvwatch.ErrUnverified) || !strings.Contains(err.Error(), "still waiting") {
		t.Fatalf("ReadErr while the dropped read still waits past the stale bound = %v, want the wait reported", err)
	}
	timers.last(t, 7*time.Hour).fire()
	eventually(t, "the dropped read's wait to end uncounted", func() bool { return w.ReadErr() == nil })
	if n := len(timers.armed(5 * time.Second)); n != 0 {
		t.Fatalf("%d re-reads of a failed read armed, want none", n)
	}
}

// A wait that no failed read precedes ends with its read: once the read completes, ReadErr
// measures nothing from it.
func TestReadErrEndsAWaitWhoseReadCompletes(t *testing.T) {
	_, kv := bucket(t, testnats.URL(t))
	clock := newClock()
	w, into, _ := readErrWatcher(t, kv, clock)
	w.Start()
	eventually(t, "the read's completion", func() bool { return len(into.completions()) == 1 })

	clock.advance(pastTheBound)
	if err := w.ReadErr(); err != nil {
		t.Fatalf("ReadErr past the stale bound after the read completed = %v, want nil", err)
	}
}
