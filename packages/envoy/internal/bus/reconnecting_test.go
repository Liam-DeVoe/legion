package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// A JetStream send made while nats.go reconnects the connection fails at once by name, at the one
// place its kind of send names its error: an answer to a pulled message (sessionAck), a session
// consumer's pull request (SessionPull.pull), and a request through the client's JetStream
// context or through a context a caller hands a bucket call (namedRequests). Each error is
// ErrReconnecting, reads `bus: NATS is reconnecting` and nothing more, so nothing named it twice,
// and still unwraps to the error nats.go gave. Most sends nats.go refuses with "nats: outbound
// buffer limit exceeded", since the client keeps no reconnect buffer. A KV bucket's lookup it
// refuses before it sends, with "nats: key-value requires at least server version 2.6.2", since a
// reconnecting connection reports no server version; the context names that from the
// connection's state.
func TestAJetStreamSendWhileNATSReconnectsFailsByName(t *testing.T) {
	const (
		named   = "bus: NATS is reconnecting"
		session = "ses_reconnecting_sends"
		bucket  = "reconnecting-sends"
	)
	srv, uri := testnats.Start(t)
	owner, err := ConnectOwningStream([]string{uri})
	if err != nil {
		t.Fatalf("connect the stream's owner: %v", err)
	}
	t.Cleanup(owner.Close)
	consumers, err := ConnectSessionConsumers([]string{uri}, 0, nil)
	if err != nil {
		t.Fatalf("connect session consumers: %v", err)
	}
	t.Cleanup(consumers.Close)
	ctx := context.Background()
	agent := contracts.LegacyAgentSubject(session)
	if _, err := consumers.Ensure(ctx, session, []string{agent}, 0); err != nil {
		t.Fatalf("ensure the session's consumer: %v", err)
	}
	publishStored(t, owner, agent, "held")
	pull := pullSession(t, consumers, session)
	msg := next(t, pull)
	// A context of the caller's own, as the listener opens its spent-proof bucket through one
	// (cmd/listener/brokerauth.go): nothing names its requests until a bucket call is handed it.
	handed, err := owner.Conn.JetStream()
	if err != nil {
		t.Fatalf("open a JetStream context of the test's own: %v", err)
	}
	if _, err := handed.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket}); err != nil {
		t.Fatalf("create the bucket: %v", err)
	}

	srv.Stop(t)
	testwait.WithinPolling(t, 15*time.Second, 50*time.Millisecond, "both connections to start reconnecting", func() bool {
		return owner.Conn.IsReconnecting() && consumers.client.Connection().IsReconnecting()
	})

	for _, row := range []struct {
		what string
		send func() error
		want string
		// refused is whether nats.go refused the send at its reconnect buffer, rather than
		// answering it with words of its own before it sent anything.
		refused bool
	}{
		{what: "an ack of a pulled message", send: msg.Ack, want: named, refused: true},
		{what: "a progress of a pulled message", send: msg.InProgress, want: named, refused: true},
		{what: "a nak of a pulled message", send: func() error { return msg.NakWithDelay(0) }, want: named, refused: true},
		{what: "a pull request", send: func() error {
			// The pull's request in flight has delivered all it may, so Next sends the next.
			pull.remaining = 0
			_, err := pull.Next(ctx)
			return err
		}, want: named, refused: true},
		{what: "a stream read through the client's context", send: func() error {
			_, err := owner.JS().StreamInfo(Stream)
			return err
		}, want: named, refused: true},
		{what: "a consumer create through the client's context", send: func() error {
			_, err := owner.JS().AddConsumer(Stream, &nats.ConsumerConfig{Durable: "reconnecting-sends", AckPolicy: nats.AckExplicitPolicy})
			return err
		}, want: named, refused: true},
		{what: "a bucket lookup through the client's context", send: func() error {
			_, err := owner.JS().KeyValue(bucket)
			return err
		}, want: named},
		{what: "a bucket open through a context its caller handed it", send: func() error {
			_, err := OpenKeyValue(ctx, owner.Conn, handed, bucket)
			return err
		}, want: named},
		{what: "a bucket's age set through a context its caller handed it", send: func() error {
			_, err := SetKeyValueMaxAge(handed, bucket, time.Hour)
			return err
		}, want: "set KV_" + bucket + " max age: " + named, refused: true},
	} {
		t.Run(row.what, func(t *testing.T) {
			err := row.send()
			if err == nil {
				t.Fatal("the send reported success while NATS reconnects")
			}
			var send reconnectingSend
			if !errors.Is(err, ErrReconnecting) || err.Error() != row.want || !errors.As(err, &send) || send.refusal == nil {
				t.Fatalf("the send while NATS reconnects failed with %q (ErrReconnecting %t), want %q, ErrReconnecting carrying the error nats.go gave",
					err, errors.Is(err, ErrReconnecting), row.want)
			}
			refused := errors.Is(err, nats.ErrReconnectBufExceeded) || errors.Is(err, nats.ErrConnectionReconnecting)
			if row.refused && !refused {
				t.Errorf("the send while NATS reconnects failed with %q, which no longer carries nats.go's refusal: %v", err, send.refusal)
			}
			t.Logf("nats.go's own error: %v", send.refusal)
		})
	}
}
