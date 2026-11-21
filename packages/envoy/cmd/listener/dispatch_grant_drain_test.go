// The hub's auth service backs this row, as it does the Dispatch-grant rows beside it
// (dispatch_grant_fresh_test.go), so the server-change soak leaves it out too.

//go:build !serverchange

package main

import (
	"io"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/logging"
)

// A fresh-mode listener's stop gives a delivery still in its handler the deliveries' whole share of
// the drain, stopDeliveriesDrain (10 s), on its generation's own connection: a handler that is still
// running 8 s into the stop has its message acknowledged before that connection closes. Before the
// fix the generation connection drained within 10 s of which bus.Client.Drain keeps drainHookReserve
// (3 s) from the deliveries, so the handler was cut off at 7 s and its acknowledgement was lost.
func TestAFreshListenersStopLetsAHandlerEightSecondsInAcknowledge(t *testing.T) {
	hub := startDispatchGrantHub(t)
	client, err := bus.ConnectOwningStream([]string{hub.url},
		bus.WithReplicas(1),
		bus.WithName("envoy-listener"),
		bus.WithConsumerNames(consumernames.ModeFresh),
		bus.WithEnvironment(hub.environment))
	if err != nil {
		t.Fatalf("connect the listener as Dispatch: %v", err)
	}
	t.Cleanup(client.Close)
	durable := bus.ListenerDurable(hub.machine)

	started, stopBegan := make(chan struct{}, 1), make(chan time.Time, 1)
	acked := make(chan error, 1)
	sub, err := startListenerSubscription(client, durable, func(msg *natsgo.Msg) {
		started <- struct{}{}
		began := <-stopBegan
		time.Sleep(time.Until(began.Add(8 * time.Second)))
		acked <- msg.AckSync()
	})
	if err != nil {
		t.Fatalf("start the durable: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := awaitDurableBound(t.Context(), client, durable, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatalf("the durable is not bound: %v", err)
	}
	publisher, err := hub.admin.JetStream()
	if err != nil {
		t.Fatalf("the admin's publisher: %v", err)
	}
	ack, err := publisher.Publish(bus.NotificationTopicPrefix+"github.example.issue", []byte(`{}`))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("the handler was never handed the message")
	}

	began := time.Now()
	stopBegan <- began
	drainListenerClients(logging.NewWithWriter("test", io.Discard), client, began)
	select {
	case err := <-acked:
		if err != nil {
			t.Fatalf("the acknowledgement of a handler 8 s into the stop failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler 8 s into the stop never acknowledged")
	}
	record, _, err := hub.records.Read(t.Context(), consumernames.DurableKey(durable))
	if err != nil {
		t.Fatalf("read the durable's record: %v", err)
	}
	info, err := publisher.ConsumerInfo(bus.Stream, record.Current)
	if err != nil {
		t.Fatalf("read the durable: %v", err)
	}
	if info.AckFloor.Stream < ack.Sequence {
		t.Fatalf("the durable's ack floor is %d after the stop, want %d acknowledged", info.AckFloor.Stream, ack.Sequence)
	}
}
