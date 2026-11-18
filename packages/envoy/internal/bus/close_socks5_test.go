package bus_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testsocks5"
)

// A call whose wait for a connection dials one through the SOCKS5 proxy to replace the running
// client's closed connection, and finds the proxy refusing every dial, answers ErrConnectionLost,
// as the same dial without a proxy does (TestADialThatFindsNoServerToReplaceAClosedConnectionIsLost):
// the proxy's own dial error names what failed, and nats.go's ErrNoServers stays in its chain
// (connectWithContext), which is what the client reads as its connection's loss (lostIfClosed). Once
// the proxy is closed nothing listens at its address, so every dial is refused, which the bus answers
// only once its ten attempts have run out, about ten seconds. The answer still names the proxy's
// address, and Reconnected, bound by an ended context, matches no more than the loss itself.
//
// The control is this row with connectWithContext putting the proxy's dial error in ErrNoServers's
// place: the subscribe answers that dial error alone, which no caller retries.
func TestADialThroughTheProxyThatFindsNoServerToReplaceAClosedConnectionIsLost(t *testing.T) {
	uri := testnats.URL(t)
	proxy := testsocks5.Start(t, hostOf(t, uri))
	client, err := bus.Connect([]string{withHost(t, uri, unresolvable)}, bus.WithEnvironment(proxied(proxy.Addr)))
	if err != nil {
		t.Fatalf("connect through the proxy: %v", err)
	}
	t.Cleanup(client.Close)
	proxy.Close()
	client.Connection().Close()
	_, err = client.SubscribeCore("envoy.closelost.socks5", func(*nats.Msg) {})
	if !errors.Is(err, bus.ErrConnectionLost) {
		t.Fatalf("a subscribe whose dial through the proxy found no server answered %v, want ErrConnectionLost", err)
	}
	if !strings.Contains(err.Error(), proxy.Addr) {
		t.Fatalf("the lost subscribe's error %q does not name the proxy %s", err, proxy.Addr)
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if bus.Reconnected(ended, err) {
		t.Fatalf("Reconnected under an ended context matched %v", err)
	}
}
