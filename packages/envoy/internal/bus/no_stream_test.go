package bus

// A write no stream takes is sent again while the stream is there and the write's bound lasts
// (sentAgainWhileNoStream): a stream whose leader was just named answers "no response from
// stream" until that leader serves. The first two rows write right after a start on a
// three-server cluster; the issue's verification runs them against a server that holds the
// stream's new leader between its naming and its lead. The loop's own rows script the answers,
// with no server; the rest delete a stream on one server under a handle or a client, which no
// timing decides.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// untilTheRowsEnd is a context that ends 30 s before the test binary's deadline: the bound of a
// row's wait for something that comes.
func untilTheRowsEnd(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, 24*time.Hour, 30*time.Second))
	t.Cleanup(cancel)
	return ctx
}

// A put sent the moment a start that created a three-copy bucket answers is stored. The start
// answers once a read names the bucket's leader, and a member is named leader before its stream
// takes writes, so the put can draw "no responders" first; it is sent again until the stream takes
// it, within the handle's own wait.
func TestAPutRightAfterAStartThatCreatedAThreeCopyBucketIsStored(t *testing.T) {
	cluster := testnats.StartCluster(t, 3)
	conn, js := memberJetStream(t, cluster, 0)
	const bucket = "envoy_first_put_test"
	kv, err := EnsureKeyValue(untilTheRowsEnd(t), conn, js, &nats.KeyValueConfig{Bucket: bucket, Storage: nats.FileStorage, Replicas: 3})
	if err != nil {
		t.Fatalf("ensure a three-copy bucket: %v", err)
	}
	if _, err := kv.Put("key", []byte("value")); err != nil {
		t.Fatalf("the put sent the moment the start answered: %v", err)
	}
}

// A publish sent within its caller's context the moment a start that created the notification
// stream at three copies answers is stored, for the same reason.
func TestAPublishRightAfterAStartThatCreatedTheStreamIsStored(t *testing.T) {
	cluster := testnats.StartCluster(t, 3)
	client := startOnCluster(t, cluster, WithReplicas(3))
	if err := client.PublishWithin(untilTheRowsEnd(t), contracts.Envelope{
		EventID: "event-first-publish", Source: "dispatch",
		Topic: "notifications.agent.ses_first_publish", DedupeKey: "dispatch.event.first_publish",
	}); err != nil {
		t.Fatalf("the publish sent the moment the start answered: %v", err)
	}
}

// scriptedWrite answers each send of a write as script says, in order, and nil once it is spent:
// each step is the error that send answers. gone answers whether the stream was found gone, and
// counts how often it was asked. No server is behind it.
type scriptedWrite struct {
	script []error
	sends  int
	gone   bool
	reads  int
}

func (s *scriptedWrite) send() (int, error) {
	s.sends++
	if s.sends <= len(s.script) {
		return s.sends, s.script[s.sends-1]
	}
	return s.sends, nil
}

func (s *scriptedWrite) streamGone(context.Context) bool {
	s.reads++
	return s.gone
}

// The loop sends a write again after each "no response from stream" while the stream is there,
// and returns the first other answer: here two such answers, then the write is taken.
func TestAWriteNoStreamTookIsSentAgainUntilAStreamTakesIt(t *testing.T) {
	write := &scriptedWrite{script: []error{nats.ErrNoStreamResponse, nats.ErrNoStreamResponse}}
	value, err := sentAgainWhileNoStream(untilTheRowsEnd(t), write.send, write.streamGone)
	if err != nil || value != 3 || write.sends != 3 || write.reads != 2 {
		t.Fatalf("a write answered no stream twice = %d, %v after %d sends and %d reads; want the third send taken, the stream read after each no-stream answer", value, err, write.sends, write.reads)
	}
}

// The loop returns at once, without sending again or reading the stream, an answer that is not
// "no response from stream": here a timeout, which may have reached a stream.
func TestAWriteAnsweredOtherwiseIsSentOnce(t *testing.T) {
	write := &scriptedWrite{script: []error{nats.ErrTimeout}}
	if _, err := sentAgainWhileNoStream(untilTheRowsEnd(t), write.send, write.streamGone); !errors.Is(err, nats.ErrTimeout) || write.sends != 1 || write.reads != 0 {
		t.Fatalf("a write that timed out = %v after %d sends and %d reads, want the timeout after one send and no read", err, write.sends, write.reads)
	}
}

// The loop returns "no response from stream" at once when the stream is gone: sending again could
// not reach it.
func TestAWriteToAStreamFoundGoneIsSentOnce(t *testing.T) {
	write := &scriptedWrite{script: []error{nats.ErrNoStreamResponse, nats.ErrNoStreamResponse}, gone: true}
	if _, err := sentAgainWhileNoStream(untilTheRowsEnd(t), write.send, write.streamGone); !errors.Is(err, nats.ErrNoStreamResponse) || write.sends != 1 {
		t.Fatalf("a write to a stream found gone = %v after %d sends, want nats.ErrNoStreamResponse after one send", err, write.sends)
	}
}

// When its context ends, the loop returns the last send's "no response from stream", not the
// context's end: that answer says nothing was stored.
func TestAWriteNoStreamTakesReturnsThatAnswerWhenItsContextEnds(t *testing.T) {
	write := &scriptedWrite{script: []error{nats.ErrNoStreamResponse}}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sentAgainWhileNoStream(ended, write.send, write.streamGone); !errors.Is(err, nats.ErrNoStreamResponse) || errors.Is(err, context.Canceled) || write.sends != 1 {
		t.Fatalf("a write no stream took under an ended context = %v after %d sends, want nats.ErrNoStreamResponse after one send", err, write.sends)
	}
}

// noStreamLegacy is the legacy handle of a bucket whose stream the row deleted. It counts the
// writes sent through it, and on the first one the server answers "no response from stream" it
// makes the stream again (recreate) before it answers, so the stream is there when it is read.
type noStreamLegacy struct {
	nats.KeyValue
	recreate func()
	once     sync.Once
	sends    atomic.Int32
}

func (l *noStreamLegacy) Put(key string, value []byte) (uint64, error) {
	l.sends.Add(1)
	revision, err := l.KeyValue.Put(key, value)
	if errors.Is(err, nats.ErrNoStreamResponse) && l.recreate != nil {
		l.once.Do(l.recreate)
	}
	return revision, err
}

func (l *noStreamLegacy) Update(key string, value []byte, last uint64) (uint64, error) {
	l.sends.Add(1)
	return l.KeyValue.Update(key, value, last)
}

// noStreamBucket is a bucket on the test's own account, its handle built over a counting legacy
// handle (noStreamLegacy), and the account's JetStream context.
func noStreamBucket(t *testing.T, bucket string) (KeyValue, *noStreamLegacy, nats.JetStreamContext, *nats.KeyValueConfig) {
	t.Helper()
	conn := testnats.Connect(t, testnats.URL(t))
	t.Cleanup(conn.Close)
	js, err := conn.JetStream(nats.MaxWait(RequestTimeout))
	if err != nil {
		t.Fatalf("open JetStream: %v", err)
	}
	config := &nats.KeyValueConfig{Bucket: bucket, Storage: nats.MemoryStorage}
	legacy, err := js.CreateKeyValue(config)
	if err != nil {
		t.Fatalf("create the bucket: %v", err)
	}
	streams, err := jetstream.New(conn)
	if err != nil {
		t.Fatalf("open the jetstream context: %v", err)
	}
	watch, err := streams.KeyValue(untilTheRowsEnd(t), bucket)
	if err != nil {
		t.Fatalf("open the jetstream handle: %v", err)
	}
	counted := &noStreamLegacy{KeyValue: legacy}
	return NewKeyValue(counted, watch, streams), counted, js, config
}

// A put the server answers "no response from stream" while the bucket's stream is there is sent
// again and stored: here the stream is deleted and made again after the first such answer, before
// the handle reads it.
func TestAPutNoStreamTookIsSentAgainOnceTheStreamIsThere(t *testing.T) {
	kv, legacy, js, config := noStreamBucket(t, "envoy_no_stream_put_test")
	if err := js.DeleteStream("KV_" + config.Bucket); err != nil {
		t.Fatalf("delete the bucket's stream: %v", err)
	}
	var recreated error
	legacy.recreate = func() { _, recreated = js.CreateKeyValue(config) }
	deadline, _ := untilTheRowsEnd(t).Deadline()
	kv.noStreamWait = time.Until(deadline)
	if _, err := kv.Put("key", []byte("value")); err != nil {
		t.Fatalf("a put no stream took, the stream then made again: %v (sends %d, recreate %v)", err, legacy.sends.Load(), recreated)
	}
	if recreated != nil || legacy.sends.Load() < 2 {
		t.Fatalf("the stream was made again with %v after %d sends; want it made and the put sent again", recreated, legacy.sends.Load())
	}
	entry, err := kv.Get("key")
	if err != nil || string(entry.Value()) != "value" {
		t.Fatalf("read the key back = %v, %v; want value", entry, err)
	}
}

// A put to a bucket whose stream is gone fails with nats.ErrNoStreamResponse after one send, as it
// did before writes were sent again: the read after the answer finds no stream.
func TestAPutToABucketWhoseStreamIsGoneIsSentOnce(t *testing.T) {
	kv, legacy, js, config := noStreamBucket(t, "envoy_no_stream_gone_test")
	if err := js.DeleteStream("KV_" + config.Bucket); err != nil {
		t.Fatalf("delete the bucket's stream: %v", err)
	}
	deadline, _ := untilTheRowsEnd(t).Deadline()
	kv.noStreamWait = time.Until(deadline)
	if _, err := kv.Put("key", []byte("value")); !errors.Is(err, nats.ErrNoStreamResponse) || legacy.sends.Load() != 1 {
		t.Fatalf("a put to a bucket whose stream is gone = %v after %d sends, want nats.ErrNoStreamResponse after one send", err, legacy.sends.Load())
	}
}

// A write the stream refuses is not sent again: here an update at a revision the key has moved
// past, which the stream answers with its conflict at once.
func TestAConflictingUpdateIsSentOnce(t *testing.T) {
	kv, legacy, _, _ := noStreamBucket(t, "envoy_no_stream_conflict_test")
	first, err := kv.Put("key", []byte("one"))
	if err != nil {
		t.Fatalf("put the key: %v", err)
	}
	if _, err := kv.Put("key", []byte("two")); err != nil {
		t.Fatalf("put the key again: %v", err)
	}
	before := legacy.sends.Load()
	if _, err := kv.Update("key", []byte("three"), first); !IsCASConflict(err) {
		t.Fatalf("an update at a revision the key moved past = %v, want a conflict", err)
	}
	if sent := legacy.sends.Load() - before; sent != 1 {
		t.Fatalf("the conflicting update was sent %d times, want once", sent)
	}
}

// A stored publish to a deleted notification stream fails with nats.ErrNoStreamResponse within
// its context and without ErrUnacknowledged: the read after the answer finds no stream, so it is
// not sent again, and no send of it reached a stream.
func TestAStoredPublishToAStreamThatIsGoneFailsStoringNothing(t *testing.T) {
	client, err := ConnectOwningStream([]string{testnats.URL(t)}, WithKeepInterval(time.Hour))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	if err := client.JS().DeleteStream(Stream); err != nil {
		t.Fatalf("delete the stream: %v", err)
	}
	err = client.PublishWithin(untilTheRowsEnd(t), contracts.Envelope{
		EventID: "event-no-stream-gone", Source: "dispatch",
		Topic: "notifications.agent.ses_no_stream_gone", DedupeKey: "dispatch.event.no_stream_gone",
	})
	if !errors.Is(err, nats.ErrNoStreamResponse) || errors.Is(err, ErrUnacknowledged) {
		t.Fatalf("a publish to a stream that is gone = %v, want nats.ErrNoStreamResponse without ErrUnacknowledged", err)
	}
}
