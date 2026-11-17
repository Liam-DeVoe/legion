package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// unsubscribeBody is the body of an unsubscribe of topics from sessionID, as the plugin sends it.
func unsubscribeBody(t *testing.T, sessionID string, topics ...string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"session_id": sessionID, "topics": topics})
	if err != nil {
		t.Fatalf("marshal the unsubscribe: %v", err)
	}
	return string(body)
}

// An unsubscribe the listener answers while its NATS connection is reconnecting fails by name, and
// inside the client's own deadline (R14 of dispatch://INFRA-3589), as a registration does
// (TestARegistrationThroughANATSReconnectFailsByName). An unsubscribe of a session with a consumer
// removes topics from the session's interest row and then sets the consumer's filter to what is
// left (sessionDelivery.refollow), and the client waits for both. Its client is the plugin, whose
// envoy-client gives one attempt contracts.ListenerCallTimeout: an answer that comes later
// reaches nobody. So the answer is a 503 whose reason names the reconnect, inside that attempt,
// and no listener line names nats.go's buffer refusal.
func TestAnUnsubscribeThroughANATSReconnectFailsByName(t *testing.T) {
	const (
		machineID = "reconnect-unsubscribe"
		sessionID = "ses_reconnect_unsubscribe"
		topic     = "notifications.team.reconnect-unsubscribe"
	)
	server, uri := testnats.Start(t)
	listener := startListenerProcess(t, cmdtest.Build(t, "envoy-listener"), uri, machineID)
	listener.waitHealthy(t)
	postListener(t, listener.port, "/v1/interests/subscribe", consumerRegistration(sessionID, "epoch-1", topic))

	disconnected := func() int {
		return strings.Count(listener.Output.String(), "envoy nats disconnected name=envoy ")
	}
	before := disconnected()
	server.Stop(t)
	waitFor(t, 15*time.Second, "the listener to see the NATS server go", func() bool { return disconnected() > before })

	started := time.Now()
	answer := reconnectWritesCall(listener.port, "/v1/interests/unsubscribe", unsubscribeBody(t, sessionID, topic), 15*time.Second)
	took := time.Since(started)
	t.Logf("the unsubscribe during the outage answered %s after %s", answer, took.Round(time.Millisecond))
	if answer.err != nil || answer.status != http.StatusServiceUnavailable || !strings.Contains(answer.body, reconnectingReason) {
		t.Errorf("the unsubscribe during the outage answered %s, want 503 naming %q", answer, reconnectingReason)
	}
	if took >= contracts.ListenerCallTimeout {
		t.Errorf("the unsubscribe during the outage was answered after %s, want inside the client's %s attempt (contracts.ListenerCallTimeout), or the plugin never reads the answer",
			took.Round(time.Millisecond), contracts.ListenerCallTimeout)
	}
	if strings.Contains(listener.Output.String(), reconnectBufferRefusal) {
		t.Errorf("the listener logged %q during the outage, want the reconnect named instead", reconnectBufferRefusal)
	}
}

// An unsubscribe whose store work answers and whose consumer call meets a NATS reconnect fails by
// name, and inside the client's own deadline, as a registration's consumer call does. The filter
// update after the row is written (sessionDelivery.refollow) is the route's one consumer call, and
// its client waits for it: it runs within routeConsumerCallTimeout of the request's arrival, and a
// call that failed because NATS is reconnecting is the bus-unavailable 503 that names the
// reconnect. The session-consumer connection alone goes through a proxy, which the test cuts
// after the update read the row and before it sends the call, so the bucket keeps answering.
//
// Before routeConsumerCallTimeout bounded it, the update waited out sessionConsumerCallTimeout
// (5 s) from its own start and was only logged, and the route answered 200 after the client's
// attempt.
func TestAnUnsubscribeWhoseConsumerCallMeetsANATSReconnectFailsByName(t *testing.T) {
	const (
		sessionID = "ses_unsubscribe_consumer_call"
		topic     = "notifications.team.unsubscribe-consumer-call"
	)
	_, uri := testnats.Start(t)
	proxy := testnats.StartProxy(t, uri)
	client, err := bus.ConnectOwningStream([]string{uri}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener: %v", err)
	}
	t.Cleanup(client.Close)
	rig := newSessionConsumerRigOn(t, client, proxy.URL())
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1", topic))

	consumerCall := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, err := rig.deps.consumers.consumers.Info(ctx, sessionID)
		return err
	}
	var cut sync.Once
	rig.deps.consumers.afterStoredRowRead = func() {
		cut.Do(func() {
			proxy.Cut()
			waitFor(t, 3*time.Second, "the listener's session-consumer connection to start reconnecting", func() bool {
				return errors.Is(consumerCall(), bus.ErrReconnecting)
			})
		})
	}
	t.Cleanup(proxy.Restore)

	recorder := httptest.NewRecorder()
	started := time.Now()
	rig.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/interests/unsubscribe", strings.NewReader(unsubscribeBody(t, sessionID, topic))))
	took := time.Since(started)
	t.Logf("the unsubscribe whose consumer call met the reconnect answered %d %s after %s",
		recorder.Code, strings.TrimSpace(recorder.Body.String()), took.Round(time.Millisecond))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), reconnectingReason) {
		t.Errorf("the unsubscribe answered %d %s, want 503 naming %q", recorder.Code, recorder.Body.String(), reconnectingReason)
	}
	if took >= contracts.ListenerCallTimeout {
		t.Errorf("the unsubscribe was answered after %s, want inside the client's %s attempt (contracts.ListenerCallTimeout)",
			took.Round(time.Millisecond), contracts.ListenerCallTimeout)
	}
	if strings.Contains(rig.harness.logs.String(), reconnectBufferRefusal) {
		t.Errorf("the listener logged %q, want the reconnect named instead", reconnectBufferRefusal)
	}
}
