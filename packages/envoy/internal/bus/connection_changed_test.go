package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// A publish that waits for NATS to reconnect is woken by the reconnect itself, and its wait loop
// passes a bounded number of times over the outage instead of polling the connection's status
// every few milliseconds. The wait also reads the status again every reconnectRecheck without an
// event, on the client's clock, which stands still here: so the publish returning at all says the
// reconnect's event woke it, and the bound is one pass for the first wait and one for a connection
// event that woke it before the connection was connected, where a 10 ms poll would pass some 100
// times over nats.go's reconnect wait of a second.
func TestAPublishWaitingForAReconnectWakesOnTheReconnect(t *testing.T) {
	server, url := testnats.Start(t)
	still := clocktest.New(time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC))
	client, err := Connect([]string{url}, WithClock(still))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)

	server.Stop(t)
	testwait.Eventually(t, "the client to see its server gone", func() bool { return !client.Connected() })
	waitsBefore := client.reconnectWaits.Load()

	published := make(chan error, 1)
	go func() {
		item := contracts.Envelope{EventID: "evt-reconnect-wake", Topic: "notifications.role.reconnect-wake"}
		published <- client.PublishCoreContext(context.Background(), item.Topic, item)
	}()
	if armed := still.AwaitArmed(t, "the publish's reconnect wait to arm its recheck", 1)[0]; !armed.Equal(still.Now().Add(reconnectRecheck)) {
		t.Fatalf("the reconnect wait rechecks at %s, want %s on", armed, reconnectRecheck)
	}
	server.Start(t)

	if err := testwait.Receive(t, "the publish waiting through the reconnect to return with the clock standing still", published); err != nil {
		t.Fatalf("the publish waiting through the reconnect answered %v, want nil", err)
	}
	if waits := client.reconnectWaits.Load() - waitsBefore; waits > 2 {
		t.Fatalf("the publish's wait loop passed %d times with no recheck due, want at most 2", waits)
	}
}

// nats.go runs a connection's callbacks one after another on one goroutine, so a reconnect's event
// can wait behind a slow callback queued before it, here the disconnect's, held until the test
// ends. A publish waiting for the reconnect still returns once the connection is connected: its
// wait reads the connection's own status again every reconnectRecheck, with the event still queued.
// The recheck is on the client's clock, which the test moves past it once the client has
// reconnected: the publish is still waiting then, and returns once the clock has moved.
func TestAPublishWaitingForAReconnectWhoseEventIsDelayedStillReturns(t *testing.T) {
	server, url := testnats.Start(t)
	clock := clocktest.New(time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC))
	client, err := Connect([]string{url}, WithClock(clock))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	client.mu.Lock()
	conn := client.Conn
	client.mu.Unlock()
	release := make(chan struct{})
	disconnected := conn.Opts.DisconnectedErrCB
	conn.SetDisconnectErrHandler(func(nc *nats.Conn, err error) {
		<-release
		disconnected(nc, err)
	})
	// Runs before client.Close, so the held callback lets the close's own callbacks run.
	t.Cleanup(func() { close(release) })

	server.Stop(t)
	testwait.Eventually(t, "the client to see its server gone", func() bool { return !client.Connected() })
	reconnected := conn.StatusChanged(nats.CONNECTED)
	published := make(chan error, 1)
	go func() {
		item := contracts.Envelope{EventID: "evt-reconnect-delayed", Topic: "notifications.role.reconnect-delayed"}
		published <- client.PublishCoreContext(context.Background(), item.Topic, item)
	}()
	recheck := clock.AwaitArmed(t, "the publish's reconnect wait to arm its recheck", 1)[0]
	if want := clock.Now().Add(reconnectRecheck); !recheck.Equal(want) {
		t.Fatalf("the reconnect wait rechecks at %s, want %s on", recheck, reconnectRecheck)
	}
	server.Start(t)

	testwait.Receive(t, "the client's reconnect", reconnected)
	// The recheck is still armed, so the wait has not ended: it stops its timer whenever it returns.
	if armed := clock.Armed(); len(armed) != 1 || len(published) != 0 {
		t.Fatalf("with the reconnect's event held and no recheck due, the publish's wait has %v armed and answered %d times, want its recheck armed and no answer", armed, len(published))
	}
	clock.Set(recheck)
	if err := testwait.Receive(t, "the publish to return at its recheck with the reconnect's event still held", published); err != nil {
		t.Fatalf("the publish waiting through the reconnect answered %v, want nil", err)
	}
}
