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

// markedCalls are the calls of the listener's main client that a caller makes while it holds a
// session's lock or a role's keep guard, each through the path it reaches the connection by. Each
// is made under a context marked as made under a lock (WithLockHeld).
var markedCalls = []struct {
	name string
	call func(ctx context.Context, client *Client, kv KeyValue) error
}{
	// A legacy KV request (a session or interest bucket read or write, a role claim read):
	// kvRequest binds it under ctx, and requestRead answers "unknown" as the loss.
	{"kv request", func(ctx context.Context, _ *Client, kv KeyValue) error {
		_, err := kv.Get(ctx, "key")
		return err
	}},
	// A stored publish (the cursor write of a role's handover, PublishCursorAt; the kept copies'
	// writes): publishJetStreamOn and publishStoredRaw wait for the connection in connForCall.
	{"stored publish", func(ctx context.Context, client *Client, _ KeyValue) error {
		subject := contracts.AgentSubject(contracts.NoProject, "ses_marked_publish")
		return client.PublishStoredAs(ctx, subject, contracts.Envelope{Topic: subject, Source: "test", DedupeKey: "marked"}, "marked-publish")
	}},
	// A core publish (PublishCoreContext).
	{"core publish", func(ctx context.Context, client *Client, _ KeyValue) error {
		return client.PublishCoreContext(ctx, "envoy.marked.core", contracts.Envelope{Topic: "envoy.marked.core", Source: "test"})
	}},
	// A forward to a role's holder (StartRequestStoredTo), which builds its own bound.
	{"forward", func(ctx context.Context, client *Client, _ KeyValue) error {
		subject := contracts.AgentSubject(contracts.NoProject, "ses_marked_forward")
		forward, err := client.StartRequestStoredTo(ctx, subject, contracts.Envelope{Topic: subject, Source: "test", DedupeKey: "marked"}, "marked-forward", time.Minute)
		if err != nil {
			return err
		}
		return forward.Wait()
	}},
	// The kept lanes' list (KeptRoleLanes).
	{"kept role lanes", func(ctx context.Context, client *Client, _ KeyValue) error {
		_, err := client.KeptRoleLanes(ctx)
		return err
	}},
	// A core request (RequestCore), the connection probe a send makes.
	{"core request", func(ctx context.Context, client *Client, _ KeyValue) error {
		_, err := client.RequestCore(ctx, "envoy.marked.request", nats.Header{ConnectionProbeHeader: []string{"1"}})
		return err
	}},
}

// A call the listener's main client makes for a caller that holds a lock, made while the hub holds
// the handshake of the client's reconnect, answers ErrConnectionLost while the handshake is still
// held, through each path the call reaches the connection by: the request's own read of the
// connection (requestRead), the wait a publish, a forward or a stream read makes for the connection
// before it sends (connForCall), and the context a forward builds for itself, which keeps the
// caller's mark (StartRequestStoredTo). A caller that held its lock across a wait for the
// reconnect would hold it for the whole handshake. Each case builds its own client through its own
// proxy, so one case's wait cannot end another's hold. It decides by events: the answer must come
// while the handshake is held, and before the call begins any wait - for the reconnect
// (ensureConnWithContext's, WithReconnectWaitTick) or for nats.go's own answer about the connection
// (requestReadUntil's, answerWaitTick), either of which a caller with no lock makes and a caller
// with one never does. A case that fails ends at that wait or at the test's deadline.
//
// The controls are each path as it was: the KV request bound with context.Background() rather than
// its caller's context (kvRequest), which begins the wait for nats.go's answer; the stored
// publish's, the core publish's, the kept lanes' and the core request's wait made in
// ensureConnWithContext rather than connForCall; and the forward's context built from
// context.Background(). Each case begins a wait, and fails.
func TestAMarkedCallDuringAHeldHandshakeAnswersAtOnce(t *testing.T) {
	for _, marked := range markedCalls {
		t.Run(marked.name, func(t *testing.T) {
			waited := make(chan string, 2)
			tick := func(what string) func() {
				return func() {
					select {
					case waited <- what:
					default:
					}
				}
			}
			proxy := testnats.StartProxy(t, testnats.URL(t))
			client, err := ConnectOwningStream([]string{proxy.URL()}, WithReplicas(1), WithConnectTimeout(testnats.Remaining(t)),
				WithReconnectWaitTick(tick("a wait for the reconnect")))
			if err != nil {
				t.Fatalf("connect the main client through the proxy: %v", err)
			}
			t.Cleanup(client.Close)
			t.Cleanup(proxy.Restore)
			kv := lostReplyBucket(t, client)
			proxy.Cut()
			hold := proxy.HoldConnectAnswers()
			awaitHeld(t, hold)
			answerTick := tick("a wait for nats.go's own answer")
			client.answerWaitTick.Store(&answerTick)
			ctx, cancel := context.WithTimeout(context.Background(), testnats.Remaining(t))
			defer cancel()
			answered := make(chan error, 1)
			go func() { answered <- marked.call(WithLockHeld(ctx), client, kv) }()
			select {
			case err := <-answered:
				if !errors.Is(err, ErrConnectionLost) {
					t.Fatalf("the %s made under a lock during the held handshake answered %v, want ErrConnectionLost", marked.name, err)
				}
				select {
				case <-hold.Ended():
					t.Fatalf("the %s answered only once the held handshake had ended", marked.name)
				default:
				}
			case what := <-waited:
				proxy.Restore()
				t.Fatalf("the %s made under a lock began %s, with its caller's lock held", marked.name, what)
			case <-hold.Ended():
				t.Fatalf("the %s gave no answer while the handshake was held", marked.name)
			case <-time.After(testnats.Remaining(t)):
				proxy.Restore()
				t.Fatalf("the %s gave no answer by the test's deadline while the handshake was held", marked.name)
			}
		})
	}
}

// A stored publish its caller holds a lock across, whose answer the connection's loss took,
// answers that loss without waiting for the reconnect: sendStored sends a publish whose answer the
// loss took again once the connection is back (Reconnected), for a caller that holds nothing, and
// not for one that marks its context (WithLockHeld), which lets its lock go and sends it again
// itself. The row holds the stream's acknowledgement of the publish (testnats.ReplyHold), and cuts
// the connection once the acknowledgement is withheld, so the publish's life ends at the drop and
// no reconnect succeeds. It decides by events: the publish's answer must come before any wait for
// the reconnect begins (WithReconnectWaitTick, which ensureConnWithContext's wait runs at each
// pass), and a row that fails ends at that wait or at the test's deadline.
//
// The control is sendStored waiting in Reconnected whoever its caller is: the publish waits for the
// reconnect, the tick runs, and the row fails.
func TestAMarkedStoredPublishWhoseAnswerTheLossTookAnswersAtOnce(t *testing.T) {
	hold := testnats.StartReplyHold(t, testnats.URL(t))
	waited := make(chan struct{}, 1)
	client, err := ConnectOwningStream([]string{hold.URL()}, WithReplicas(1),
		WithReconnectWaitTick(func() {
			select {
			case waited <- struct{}{}:
			default:
			}
		}))
	if err != nil {
		t.Fatalf("connect the main client through the hold: %v", err)
	}
	t.Cleanup(client.Close)
	t.Cleanup(hold.Restore)
	subject := contracts.AgentSubject(contracts.NoProject, "ses_marked_resend")
	held := hold.Hold(subject)
	ctx, cancel := context.WithTimeout(context.Background(), testnats.Remaining(t))
	defer cancel()
	answered := make(chan error, 1)
	go func() {
		answered <- client.PublishStoredAs(WithLockHeld(ctx), subject, contracts.Envelope{Topic: subject, Source: "test", DedupeKey: "marked-resend"}, "marked-resend")
	}()
	select {
	case <-held.Withheld():
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the stream's acknowledgement of the publish was never withheld by the test's deadline")
	}
	hold.Cut()
	select {
	case err := <-answered:
		if !errors.Is(err, ErrConnectionLost) {
			t.Fatalf("the publish made under a lock, whose answer the loss took, answered %v, want ErrConnectionLost", err)
		}
	case <-waited:
		t.Fatal("the publish made under a lock waited for the reconnect, with its caller's lock held")
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the publish made under a lock gave no answer by the test's deadline")
	}
}
