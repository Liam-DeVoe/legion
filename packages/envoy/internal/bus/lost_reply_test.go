package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// lostReplyWithin is how soon after its connection drops a request whose reply the drop lost must
// fail (INFRA-4150, r-lost-reply-fails-fast).
const lostReplyWithin = 100 * time.Millisecond

// lostReplyWait is how long a row waits for a request it held to fail at all: far past
// lostReplyWithin, and short of every bound those requests carry of their own (3 s to 10 s), so
// a request that waits for its bound fails the row as one still waiting rather than as one late.
const lostReplyWait = 2 * time.Second

// heldClient is a client of its own on a server of its own, connected through a hold
// (testnats.ReplyHold), owning the stream so the stream's requests have a stream to answer for.
func heldClient(t *testing.T) (*Client, *testnats.ReplyHold) {
	t.Helper()
	hold := testnats.StartReplyHold(t, testnats.URL(t))
	client, err := ConnectOwningStream([]string{hold.URL()}, WithReplicas(1))
	if err != nil {
		t.Fatalf("connect through the hold: %v", err)
	}
	t.Cleanup(client.Close)
	return client, hold
}

// requireLostAtTheDrop holds the replies to the requests on prefix, runs send, waits until the hold
// holds its reply with the request's own flush answered, so the request is past its publish and
// waiting for that reply, drops the connection, and requires send to fail within lostReplyWithin
// of the drop with ErrConnectionLost.
func requireLostAtTheDrop(t *testing.T, hold *testnats.ReplyHold, prefix string, send func() error) {
	t.Helper()
	held := hold.Hold(prefix)
	answered := make(chan error, 1)
	go func() { answered <- send() }()
	select {
	case <-held:
	case err := <-answered:
		t.Fatalf("the request on %s answered %v before the hold held its reply", prefix, err)
	case <-time.After(lostReplyWait):
		t.Fatalf("no reply to a request on %s reached the hold within %s", prefix, lostReplyWait)
	}
	dropped := time.Now()
	hold.Drop()
	select {
	case err := <-answered:
		took := time.Since(dropped)
		if !errors.Is(err, ErrConnectionLost) {
			t.Fatalf("the request on %s whose reply the drop lost answered %v %s after the drop, want ErrConnectionLost", prefix, err, took)
		}
		if took > lostReplyWithin {
			t.Fatalf("the request on %s whose reply the drop lost failed %s after the drop, want within %s", prefix, took, lostReplyWithin)
		}
		t.Logf("the request on %s whose reply the drop lost failed %s after the drop: %v", prefix, took, err)
	case <-time.After(lostReplyWait):
		t.Fatalf("the request on %s whose reply the drop lost was still waiting %s after the drop, want it failed within %s", prefix, lostReplyWait, lostReplyWithin)
	}
}

// A request on a client's connection whose reply the connection's drop lost fails within 100 ms
// of the drop with ErrConnectionLost, whatever shape of request it is: a core request on a reply
// inbox of its own (requestConfirmed, which carries every session consumer call and stream read,
// and RequestCore, the connection probe's), a forward's wait for the stream's acknowledgement, a
// JetStream API call through the client's JetStream context, and a KV read and write through a
// bucket handle opened on the client's connection. The request reaches the server, which answers
// it, and the hold keeps that answer from the client: what the client sees is a request whose
// reply its lost connection took.
//
// The control is the same rows at main a97cd59397 (and 44a1f1fb97): nats.go clears no pending
// request when a dropped socket turns the connection to reconnecting (processOpErr), so each of
// them waits for its own bound, 3 s to 10 s, and every row fails as still waiting.
func TestARequestWhoseReplyTheDropLostFailsAtTheDrop(t *testing.T) {
	for _, row := range []struct {
		name, prefix string
		// prepare makes what the row's request needs before anything is held, and returns the
		// request.
		prepare func(*testing.T, *Client) func() error
	}{
		{"core request", "$JS.API.STREAM.INFO.", func(_ *testing.T, c *Client) func() error {
			return func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, err := c.requestConfirmed(ctx, "$JS.API.STREAM.INFO."+Stream, nil)
				return err
			}
		}},
		{"core request on a channel inbox", "envoy.lostreply.probe", func(_ *testing.T, c *Client) func() error {
			return func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, err := c.RequestCore(ctx, "envoy.lostreply.probe", nats.Header{ConnectionProbeHeader: []string{"1"}})
				return err
			}
		}},
		{"forward", "notifications.lostreply.", func(_ *testing.T, c *Client) func() error {
			item := contracts.Envelope{Topic: "notifications.lostreply.forward", Source: "test", DedupeKey: "lost-reply"}
			return func() error {
				forward, err := c.StartRequestStoredTo(item.Topic, item, "lost-reply", 3*time.Second)
				if err != nil {
					return err
				}
				return forward.Wait()
			}
		}},
		{"JetStream API call", "$JS.API.STREAM.INFO.", func(_ *testing.T, c *Client) func() error {
			return func() error {
				_, err := c.JS().StreamInfo(Stream)
				return err
			}
		}},
		{"KV read", "$JS.API.DIRECT.GET.KV_envoy_lost_reply", func(t *testing.T, c *Client) func() error {
			kv := lostReplyBucket(t, c)
			return func() error {
				_, err := kv.Get("key")
				return err
			}
		}},
		{"KV write", "$KV.envoy_lost_reply.", func(t *testing.T, c *Client) func() error {
			kv := lostReplyBucket(t, c)
			return func() error {
				_, err := kv.Put("key", []byte("value"))
				return err
			}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			client, hold := heldClient(t)
			requireLostAtTheDrop(t, hold, row.prefix, row.prepare(t, client))
		})
	}
}

// lostReplyBucket opens the bucket the KV rows hold their requests on, on c's connection, as the
// listener opens its buckets (EnsureKeyValue through the client's JetStream context).
func lostReplyBucket(t *testing.T, c *Client) KeyValue {
	t.Helper()
	kv, err := EnsureKeyValue(c.Conn, c.JS(), &nats.KeyValueConfig{Bucket: "envoy_lost_reply", Storage: nats.MemoryStorage})
	if err != nil {
		t.Fatalf("open the bucket: %v", err)
	}
	return kv
}

// The shared read of the stream's state (SessionConsumers.streamState), which every session
// consumer call waits on, gives up a read whose reply the connection's drop lost within 100 ms of
// the drop, and the read made once the connection is back is answered: a session registration
// after a reconnect, which waits on that read before its consumer call, no longer waits behind
// the one the drop cut off (INFRA-4150's q1 harness: 7 of 30 registrations answered 503 at main).
//
// The control is the same row at main a97cd59397: the read cut off waits out its own
// sessionStreamReadWait (5 s), so the first call is still waiting at lostReplyWait, and a call
// made after the reconnect waits for the read after it, which starts only once that one ends.
func TestAStreamReadTheDropCutOffReleasesTheCallsWaitingOnIt(t *testing.T) {
	hold := testnats.StartReplyHold(t, testnats.URL(t))
	owner, err := ConnectOwningStream([]string{hold.URL()}, WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the stream's owner: %v", err)
	}
	t.Cleanup(owner.Close)
	consumers, err := ConnectSessionConsumers([]string{hold.URL()}, 0, nil)
	if err != nil {
		t.Fatalf("connect the session consumers: %v", err)
	}
	t.Cleanup(consumers.Close)
	requireLostAtTheDrop(t, hold, "$JS.API.STREAM.INFO."+Stream, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := consumers.StreamID(ctx)
		return err
	})
	reconnected := time.Now().Add(lostReplyWait)
	for !consumers.client.Connected() {
		if time.Now().After(reconnected) {
			t.Fatalf("the session consumers' connection was not back %s after the drop", lostReplyWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if id, err := consumers.StreamID(ctx); err != nil || id == "" {
		t.Fatalf("the stream read made once the connection was back answered %q, %v; want the stream's id within 1 s", id, err)
	}
}

// A session consumer's pull whose connection drops ends within 100 ms of the drop with
// ErrConnectionLost: the pull request standing at the server went with the connection, and the
// relay restarts the consumer on the new connection rather than waiting to hear nothing for twice
// the pull's heartbeat (ErrSessionPullStalled).
//
// The control is the same row at main a97cd59397: Next goes on waiting for the server that will
// never send on the lost pull, until it stalls at twice sessionPullHeartbeat (30 s).
func TestAPullWhoseConnectionDropsEndsAtTheDrop(t *testing.T) {
	hold := testnats.StartReplyHold(t, testnats.URL(t))
	owner, err := ConnectOwningStream([]string{hold.URL()}, WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the stream's owner: %v", err)
	}
	t.Cleanup(owner.Close)
	consumers, err := ConnectSessionConsumers([]string{hold.URL()}, 0, nil)
	if err != nil {
		t.Fatalf("connect the session consumers: %v", err)
	}
	t.Cleanup(consumers.Close)
	const sessionID = "ses_lost_pull"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := consumers.Ensure(ctx, sessionID, []string{contracts.AgentSubject(sessionID)}, 0); err != nil {
		t.Fatalf("create the session's consumer: %v", err)
	}
	pull, err := consumers.Pull(ctx, sessionID)
	if err != nil {
		t.Fatalf("pull the session's consumer: %v", err)
	}
	t.Cleanup(pull.Stop)
	answered := make(chan error, 1)
	go func() {
		_, err := pull.Next(context.Background())
		answered <- err
	}()
	// The pull request is out and the stream holds nothing for it: Next waits.
	select {
	case err := <-answered:
		t.Fatalf("the pull of an empty consumer answered %v before the drop", err)
	case <-time.After(200 * time.Millisecond):
	}
	dropped := time.Now()
	hold.Drop()
	select {
	case err := <-answered:
		took := time.Since(dropped)
		if !errors.Is(err, ErrConnectionLost) || took > lostReplyWithin {
			t.Fatalf("the pull whose connection dropped ended %s after the drop with %v, want ErrConnectionLost within %s", took, err, lostReplyWithin)
		}
		t.Logf("the pull whose connection dropped ended %s after the drop: %v", took, err)
	case <-time.After(lostReplyWait):
		t.Fatalf("the pull whose connection dropped was still waiting %s after the drop, want it ended within %s", lostReplyWait, lostReplyWithin)
	}
}
