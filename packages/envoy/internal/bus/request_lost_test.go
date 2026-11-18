package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
)

// lostRequestWait is the deadline the rows below give the request whose connection is cut: the
// listener's sessionConsumerCallTimeout, the wait a relay restart's calls hold, which is what a
// request lost with its connection used to cost in full.
const lostRequestWait = 5 * time.Second

// lostRequestAfterReconnect bounds how long after the reconnect a request lost with its
// connection may take to fail: the client's connection event and one check of the connection,
// with room for a loaded test machine, and far inside lostRequestWait.
const lostRequestAfterReconnect = 500 * time.Millisecond

// awaitRelaySent waits until relay has counted want sends of its subject since it was armed.
func awaitRelaySent(t *testing.T, relay *testnats.UnansweredRelay, want int) {
	t.Helper()
	deadline := time.Now().Add(lostRequestWait)
	for relay.Sent() < want {
		if time.Now().After(deadline) {
			t.Fatalf("the relay counted %d sends of %s, want %d", relay.Sent(), relay.Subject(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitFlushedSinceDrop waits until the server's answer to the flush behind the request relay
// dropped is in the client's socket (testnats.UnansweredRelay.FlushedSinceDrop), so a cut made
// after it finds the publish confirmed and the call waiting on its reply rather than on its flush,
// which nats.go would fail at the cut on its own.
func awaitFlushedSinceDrop(t *testing.T, relay *testnats.UnansweredRelay) {
	t.Helper()
	deadline := time.Now().Add(lostRequestWait)
	for !relay.FlushedSinceDrop() {
		if time.Now().After(deadline) {
			t.Fatalf("the server did not answer the flush behind the dropped %s within %s", relay.Subject(), lostRequestWait)
		}
		time.Sleep(time.Millisecond)
	}
}

// lostAnswer is what a call whose request a row lost answered, and when.
type lostAnswer struct {
	err error
	at  time.Time
}

// cutAndAwaitReconnect cuts every link of relay once its dropped request is out, and returns when
// conn has reconnected through it.
func cutAndAwaitReconnect(t *testing.T, relay *testnats.UnansweredRelay, conn *nats.Conn) time.Time {
	t.Helper()
	before := conn.Stats().Reconnects
	relay.Drop()
	deadline := time.Now().Add(lostRequestWait)
	for conn.Stats().Reconnects == before {
		if time.Now().After(deadline) {
			t.Fatalf("the connection did not reconnect through the relay within %s", lostRequestWait)
		}
		time.Sleep(time.Millisecond)
	}
	return time.Now()
}

// requireLostAtTheReconnect requires the call to have answered ErrRequestLostWithConnection
// within lostRequestAfterReconnect of the reconnect.
func requireLostAtTheReconnect(t *testing.T, what string, answers <-chan lostAnswer, reconnected time.Time) {
	t.Helper()
	var answer lostAnswer
	select {
	case answer = <-answers:
	case <-time.After(lostRequestWait + time.Second):
		t.Fatalf("%s whose connection was cut has not answered %s after the reconnect", what, lostRequestWait+time.Second)
	}
	after := answer.at.Sub(reconnected)
	if !errors.Is(answer.err, ErrRequestLostWithConnection) || after > lostRequestAfterReconnect {
		t.Fatalf("%s whose connection was cut under it answered %v, %s after the reconnect; want %v within %s of it, not the %s deadline",
			what, answer.err, after.Round(time.Millisecond), ErrRequestLostWithConnection, lostRequestAfterReconnect, lostRequestWait)
	}
}

// requireLostWithTheConnection arms relay to drop the next send of its subject, makes call within
// lostRequestWait on conn's client, cuts relay's links once that send is out, and requires call
// to answer ErrRequestLostWithConnection within lostRequestAfterReconnect of the reconnect: the
// order of a request whose bytes reached the socket and then died with it. It suits a call whose
// request waits on nats.go's response subscription with no flush behind it - a KV read or write,
// a JetStream publish - which nats.go fails at no cut, so the one thing that can end it early is
// the connection watch.
func requireLostWithTheConnection(t *testing.T, relay *testnats.UnansweredRelay, conn *nats.Conn, what string, call func(context.Context) error) {
	t.Helper()
	relay.Arm(1)
	ctx, cancel := context.WithTimeout(context.Background(), lostRequestWait)
	defer cancel()
	answers := make(chan lostAnswer, 1)
	go func() {
		err := call(ctx)
		answers <- lostAnswer{err: err, at: time.Now()}
	}()
	awaitRelaySent(t, relay, 1)
	reconnected := cutAndAwaitReconnect(t, relay, conn)
	requireLostAtTheReconnect(t, what, answers, reconnected)
}

// A consumer call whose request went out, and whose connection is then cut before the answer
// comes, fails with ErrRequestLostWithConnection at the reconnect rather than waiting out its
// deadline. The relay drops the call's request but passes the flush behind it, so the publish
// is confirmed and the call is waiting on its reply when the link is cut: the order a relay
// restart met on a pod whose link died, where nats.go wakes no sync subscription and the call
// waited the restart's whole 5 s.
func TestAConsumerCallLostWithItsConnectionFailsAtTheReconnect(t *testing.T) {
	uri := testnats.URL(t)
	owner, err := ConnectOwningStream([]string{uri})
	if err != nil {
		t.Fatalf("connect the stream's owner: %v", err)
	}
	t.Cleanup(owner.Close)
	const session = "ses_call_lost_with_connection"
	name, ok := SessionConsumerName(session)
	if !ok {
		t.Fatalf("no consumer name for %s", session)
	}
	relay := testnats.StartUnansweredRelayOn(t, uri, consumerAPI("INFO", name))
	consumers, err := ConnectSessionConsumers([]string{relay.URL()}, 0, nil)
	if err != nil {
		t.Fatalf("connect session consumers through the relay: %v", err)
	}
	t.Cleanup(consumers.Close)
	conn := consumers.client.Connection()

	relay.Arm(1)
	ctx, cancel := context.WithTimeout(context.Background(), lostRequestWait)
	defer cancel()
	answers := make(chan lostAnswer, 1)
	go func() {
		_, err := consumers.Info(ctx, session)
		answers <- lostAnswer{err: err, at: time.Now()}
	}()
	awaitRelaySent(t, relay, 1)
	awaitFlushedSinceDrop(t, relay)
	reconnected := cutAndAwaitReconnect(t, relay, conn)

	requireLostAtTheReconnect(t, "a consumer info", answers, reconnected)
}

// A KV read waited for through BoundedOn, the read a relay restart makes of the session's interest
// row (store.Registry.RefreshAt), fails with ErrRequestLostWithConnection at the reconnect when
// its connection is cut under it. nats.go sends the read on its shared response subscription and
// fails it only at its own MaxWait (10 s), and only ForceReconnect or a close clears that
// subscription's waiting requests, so a read sent into a socket that then died waited out its
// caller's whole deadline.
func TestAKeyValueReadLostWithItsConnectionFailsAtTheReconnect(t *testing.T) {
	const (
		bucket = "envoy_read_lost_with_connection"
		key    = "ses_read_lost"
	)
	relay := testnats.StartUnansweredRelayOn(t, testnats.URL(t), "$JS.API.DIRECT.GET.KV_"+bucket+".$KV."+bucket+"."+key)
	conn, js := relayedJetStream(t, relay)
	t.Cleanup(func() { _ = js.DeleteKeyValue(bucket) })
	kv, err := EnsureKeyValue(t.Context(), conn, js, &nats.KeyValueConfig{Bucket: bucket, Storage: nats.MemoryStorage})
	if err != nil {
		t.Fatalf("open the bucket: %v", err)
	}
	if _, err := kv.Put(key, []byte("row")); err != nil {
		t.Fatalf("write the key the row reads: %v", err)
	}

	requireLostWithTheConnection(t, relay, conn, "a KV read", func(ctx context.Context) error {
		_, _, err := BoundedOn(ctx, kv, func() (nats.KeyValueEntry, error) { return kv.Get(key) })
		return err
	})

	// The read made again on the connection that came back is answered.
	entry, answered, err := BoundedOn(context.Background(), kv, func() (nats.KeyValueEntry, error) { return kv.Get(key) })
	if err != nil || !answered || string(entry.Value()) != "row" {
		t.Fatalf("the read again after the reconnect = %v, answered %t; want the row", err, answered)
	}
}
