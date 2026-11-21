package main

import (
	"context"
	"errors"
	"testing"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
)

// The listener's durable record takes a consumer only when that consumer's own metadata names the
// durable: a record naming a <durable>~<nuid> consumer whose envoy.logical is another's (made by a
// principal that can create consumers and write the bucket) is refused as a malformed record by the
// bind and by every reader of the durable, and the consumer is neither corrected to the listener's
// policy nor bound. The control is the same record naming a consumer whose metadata names the
// durable, which the bind corrects and binds.
func TestADurableRecordNamingAnotherOwnersConsumerIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		logical func(r *durableRig) string
		served  bool
	}{
		{name: "a consumer of another logical name", logical: func(*durableRig) string { return "ses_someone_else" }},
		{name: "control: the durable's own consumer", logical: func(r *durableRig) string { return r.durable }, served: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDurableRig(t, consumernames.ModeFresh)
			ctx := context.Background()
			js, err := r.conn.JetStream()
			if err != nil {
				t.Fatal(err)
			}
			name := r.durable + "~FORGEDBYAPRINCIPAL0000"
			if _, err := js.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{
				Durable:        name,
				DeliverSubject: natsgo.NewInbox(),
				AckPolicy:      natsgo.AckExplicitPolicy,
				Metadata:       map[string]string{consumernames.LogicalMetadataKey: tc.logical(r)},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.client.SelectionStore().Write(ctx, consumernames.DurableKey(r.durable), consumernames.Record{Current: name, Start: 1}, 0); err != nil {
				t.Fatal(err)
			}
			sub, err := startListenerSubscription(r.client, r.durable, func(msg *natsgo.Msg) { _ = msg.Ack() })
			if sub != nil {
				t.Cleanup(func() { _ = sub.Unsubscribe() })
			}
			info, infoErr := js.ConsumerInfo(bus.Stream, name)
			if infoErr != nil {
				t.Fatal(infoErr)
			}
			if tc.served {
				if err != nil || info.Config.MaxAckPending != consumerMaxAckPending {
					t.Fatalf("control: the bind = %v, max ack pending %d; want it bound and corrected to %d", err, info.Config.MaxAckPending, consumerMaxAckPending)
				}
				return
			}
			if !errors.Is(err, consumernames.ErrMalformedRecord) {
				t.Fatalf("the bind over another's consumer = %v, want ErrMalformedRecord", err)
			}
			if info.Config.MaxAckPending == consumerMaxAckPending || info.PushBound {
				t.Fatalf("the refused bind touched %s: max ack pending %d, push bound %t", name, info.Config.MaxAckPending, info.PushBound)
			}
			if _, err := durableInfo(ctx, r.client, r.durable); !errors.Is(err, consumernames.ErrMalformedRecord) {
				t.Fatalf("durableInfo over another's consumer = %v, want ErrMalformedRecord", err)
			}
		})
	}
}
