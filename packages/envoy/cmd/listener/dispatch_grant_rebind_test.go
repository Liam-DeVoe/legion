// The hub's auth service backs these rows, as it does the Dispatch-grant rows beside them
// (dispatch_grant_fresh_test.go), so the server-change soak leaves them out too.

//go:build !serverchange

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/store"
)

// A fresh-mode listener beside Dispatch, on exactly Dispatch's grant, keeps its durable through a
// drop of the connection its generation is bound on. The drop is nats.go's own in-place reconnect
// of that connection, which a cut of the connection by the hub or the network also brings: the
// connection comes back under the same name, so the auth service grants it the same generation
// again, and the reconnect sends the binding again. Then the listener's reads of its durable follow
// that connection: the subscription it reports live (durableSubOK, the bind loop's and /healthz's
// read) is that connection's, /healthz answers 200 with the durable bound to it, the self-health
// monitor rebuilds nothing and never terminates, and every message published before, during and
// after the drop is delivered and acknowledged, none lost.
//
// Control, an -overlay of durable.go that makes the row fail: durableSubOK reading the listener's
// own client in the fresh mode too (`return client.SubOK()`), which the code did before the fix. The
// listener's own client holds no subscription of the durable there, so /healthz answers 503
// "subscription inactive".
func TestAFreshListenerOnDispatchsGrantFollowsItsDurableThroughADroppedConnection(t *testing.T) {
	hub := startDispatchGrantHub(t)
	ctx := t.Context()
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

	var (
		mu      sync.Mutex
		handled = map[uint64]int{}
		ackErrs []string
	)
	handler := func(msg *natsgo.Msg) {
		if meta, err := msg.Metadata(); err == nil {
			mu.Lock()
			handled[meta.Sequence.Stream]++
			mu.Unlock()
		}
		if err := msg.AckSync(); err != nil {
			mu.Lock()
			ackErrs = append(ackErrs, err.Error())
			mu.Unlock()
		}
	}
	logger := logging.NewWithWriter("test", io.Discard)
	if _, err := bindListenerDurable(ctx, client, durable, handler, logger, 100*time.Millisecond, 30*time.Second); err != nil {
		t.Fatalf("the bind loop did not bind the durable on Dispatch's grant: %v", err)
	}
	terminated, _ := startUnboundDurableMonitor(t, client, durable, logger, handler)

	// /healthz over the listener's deps; the caches are the admin's, which every grant reads.
	registry, err := store.Open(ctx, hub.admin, store.WithReplicas(1))
	if err != nil {
		t.Fatalf("open the interest registry: %v", err)
	}
	t.Cleanup(registry.StopWatch)
	sessions, err := session.OpenSessionRegistry(ctx, hub.admin, session.WithSessionReplicas(1))
	if err != nil {
		t.Fatalf("open the session registry: %v", err)
	}
	t.Cleanup(sessions.StopWatch)
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := registry.WaitForCacheReady(ready); err != nil {
		t.Fatalf("wait for the interest cache: %v", err)
	}
	if err := sessions.WaitForCacheReady(ready); err != nil {
		t.Fatalf("wait for the session cache: %v", err)
	}
	var deps atomic.Pointer[listenerDeps]
	deps.Store(&listenerDeps{client: client, registry: registry, sessions: sessions, caches: listenerCaches(registry, sessions, nil), consumer: durable})
	healthz := func() (int, map[string]any) {
		recorder := httptest.NewRecorder()
		healthzHandler(&deps, nil, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		return recorder.Code, body
	}
	// followsTheBoundConnection reports, as one reading, whether the listener's reads of its durable
	// follow the connection its generation is bound on: that connection's subscription live, the
	// durable bound to its deliver subject, and /healthz answering 200.
	followsTheBoundConnection := func() (bool, string) {
		record, _, err := hub.records.Read(ctx, consumernames.DurableKey(durable))
		if err != nil {
			return false, fmt.Sprintf("read the durable's record: %v", err)
		}
		held := connsOf(client, durable).held(record.Current)
		if held == nil {
			return false, "the listener holds no connection for its current " + record.Current
		}
		if !durableSubOK(client, durable) {
			return false, "the durable's subscription does not read as live"
		}
		info, err := durableInfo(ctx, client, durable)
		if err != nil || !info.PushBound || info.Config.DeliverSubject != held.SubSubject() {
			return false, fmt.Sprintf("the durable is not bound to its connection's subscription (%v)", err)
		}
		if code, body := healthz(); code != http.StatusOK || body["status"] == "unhealthy" {
			return false, fmt.Sprintf("/healthz = %d %v", code, body)
		}
		return true, ""
	}
	awaitFollows := func(when string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			ok, why := followsTheBoundConnection()
			if ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %s", when, why)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	publisher, err := hub.admin.JetStream()
	if err != nil {
		t.Fatalf("the admin's publisher: %v", err)
	}
	var sent []uint64
	publish := func(n int) {
		t.Helper()
		for i := range n {
			subject := fmt.Sprintf("%sgithub.example.issue.%d", bus.NotificationTopicPrefix, len(sent)+i)
			ack, err := publisher.Publish(subject, []byte(`{}`))
			if err != nil {
				t.Fatalf("publish %s: %v", subject, err)
			}
			sent = append(sent, ack.Sequence)
		}
	}
	awaitHandled := func(when string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			mu.Lock()
			var missing []uint64
			for _, seq := range sent {
				if handled[seq] == 0 {
					missing = append(missing, seq)
				}
			}
			mu.Unlock()
			if len(missing) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: messages %v were never delivered", when, missing)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	awaitFollows("before the drop")
	publish(5)
	awaitHandled("before the drop")

	record, _, err := hub.records.Read(ctx, consumernames.DurableKey(durable))
	if err != nil {
		t.Fatalf("read the durable's record: %v", err)
	}
	held := connsOf(client, durable).held(record.Current)
	reconnects := held.Conn.Stats().Reconnects
	if err := held.Conn.ForceReconnect(); err != nil {
		t.Fatalf("drop the durable's connection: %v", err)
	}
	publish(5)
	deadline := time.Now().Add(20 * time.Second)
	for held.Conn.Stats().Reconnects == reconnects {
		if time.Now().After(deadline) {
			t.Fatal("the durable's connection never reconnected after the drop")
		}
		time.Sleep(20 * time.Millisecond)
	}
	awaitFollows("after the drop")
	publish(5)
	awaitHandled("after the drop")

	// None lost: the durable's ack floor reaches the last message, so every one was acknowledged.
	deadline = time.Now().Add(30 * time.Second)
	for {
		info, err := publisher.ConsumerInfo(bus.Stream, record.Current)
		if err == nil && info.AckFloor.Stream >= sent[len(sent)-1] {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("the durable's ack floor never reached %d (%v, %v); acknowledgements that failed: %q", sent[len(sent)-1], info, err, ackErrs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if current := connsOf(client, durable).held(record.Current); current != held {
		t.Fatalf("the durable moved off the connection that reconnected: %p, want %p", current, held)
	}
	if terminated.Load() {
		t.Fatal("the self-health monitor terminated the listener over the drop")
	}
}
