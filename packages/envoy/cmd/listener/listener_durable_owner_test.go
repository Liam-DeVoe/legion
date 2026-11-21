package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// A candidate an open create of the listener's durable names that someone else made, with the
// durable's own metadata and start, is adopted only as the listener creates it. One with a setting
// the listener's create never gives and no correction reaches (headers only, replay at the
// original rate, a deliver subject that is no reply inbox, no deliver subject at all) is the
// listener's refusal of the durable (errListenerDurableRefused), an ErrUnexpectedConsumer naming
// it and that setting, and is neither published nor bound. Today's adoption checked only push, the
// heartbeat and the ack policy, so it bound the first three, and refused the pull one with an
// error the bind retried until its deadline. The control is the candidate as the listener makes
// it, adopted and bound.
func TestAPrecreatedDurableCandidateIsAdoptedOnlyAsTheListenerCreatesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		made    func(*natsgo.ConsumerConfig)
		setting string
	}{
		{name: "headers only", made: func(c *natsgo.ConsumerConfig) { c.HeadersOnly = true }, setting: "headers only"},
		{name: "replay at the original rate", made: func(c *natsgo.ConsumerConfig) { c.ReplayPolicy = natsgo.ReplayOriginalPolicy }, setting: "replay policy original"},
		{name: "a deliver subject that is no reply inbox", made: func(c *natsgo.ConsumerConfig) { c.DeliverSubject = "elsewhere.deliver" }, setting: `deliver subject "elsewhere.deliver"`},
		{name: "no deliver subject", made: func(c *natsgo.ConsumerConfig) { c.DeliverSubject = "" }, setting: `deliver subject ""`},
		{name: "control: as the listener creates it", made: func(*natsgo.ConsumerConfig) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDurableRig(t, consumernames.ModeFresh)
			ctx := context.Background()
			js, err := r.conn.JetStream()
			if err != nil {
				t.Fatal(err)
			}
			name := r.durable + "~PRECREATEDBYAPRINCIPAL"
			made := listenerDurableConfig(name, bus.StreamSubjects())
			made.DeliverPolicy, made.OptStartSeq = natsgo.DeliverByStartSequencePolicy, 1
			made.Metadata = map[string]string{consumernames.LogicalMetadataKey: r.durable}
			tc.made(&made)
			if _, err := js.AddConsumer(bus.Stream, &made); err != nil {
				t.Fatal(err)
			}
			op := &consumernames.Op{Kind: consumernames.OpCreate, Owner: "another process", Candidate: name, ReservedAt: time.Now().Add(-time.Minute)}
			if _, err := r.client.SelectionStore().Write(ctx, consumernames.DurableKey(r.durable), consumernames.Record{Start: 1, Op: op}, 0); err != nil {
				t.Fatal(err)
			}
			sub, err := startListenerSubscription(r.client, r.durable, func(msg *natsgo.Msg) { _ = msg.Ack() })
			if sub != nil {
				t.Cleanup(func() { _ = sub.Unsubscribe() })
			}
			if tc.setting == "" {
				if err != nil || r.current(t) != name {
					t.Fatalf("control: the bind = %v, current %q; want %s adopted and bound", err, r.current(t), name)
				}
				return
			}
			if !errors.Is(err, errListenerDurableRefused) || !errors.Is(err, consumernames.ErrUnexpectedConsumer) ||
				!strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), tc.setting) {
				t.Fatalf("the bind over %s = %v; want the listener's refusal, ErrUnexpectedConsumer naming it and %s", name, err, tc.setting)
			}
			if current := r.current(t); current != "" {
				t.Fatalf("the refused candidate was published: current %q", current)
			}
			if info, err := js.ConsumerInfo(bus.Stream, name); err != nil || info.PushBound {
				t.Fatalf("the refused candidate = %+v, %v; want it held and bound to no one", info, err)
			}
		})
	}
}
