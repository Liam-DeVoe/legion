package bus

import (
	"errors"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
)

// A direct call on the client's connection that a forced reconnect answered
// nats.ErrConnectionClosed is lost however late its answer is read. The keeper of a service's token
// reconnects with ForceReconnect to present a new token (natscred), which answers every pending
// flush (clearPendingFlushCalls, nats.go v1.53.1 :2604-2607, :5948-5949) and request that way and
// reconnects in place. The answer is read only once nats.go reads the connection connected again,
// so its state says nothing of the reconnect: a close answer for a call made on conn itself is a
// loss of conn (lostIfClosed).
//
// The control is this row with lostIfClosed reading conn's state, as closedUnder does for a
// request without a reconnect count: the answer is nats.go's "connection closed", which a caller
// reads as lasting.
func TestADirectCallAForcedReconnectCutOffIsLostOnceTheConnectionIsBack(t *testing.T) {
	client, err := Connect([]string{testnats.URL(t)})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	conn := client.Connection()
	if err := conn.ForceReconnect(); err != nil {
		t.Fatalf("force a reconnect: %v", err)
	}
	waitForNatsStatus(t, conn, "connected again after the forced reconnect", func(s nats.Status) bool { return s == nats.CONNECTED })
	if got := client.lostIfClosed(nats.ErrConnectionClosed, conn, false); !errors.Is(got, ErrConnectionLost) {
		t.Fatalf("a flush a forced reconnect answered %v, read once the connection was back, answered %v, want ErrConnectionLost", nats.ErrConnectionClosed, got)
	}
	if got := client.lostIfClosed(nats.ErrConnectionClosed, nil, false); !errors.Is(got, nats.ErrConnectionClosed) || errors.Is(got, ErrConnectionLost) {
		t.Fatalf("a call on the handle of a replaced connection (nil) answered %v, want nats.go's %v", got, nats.ErrConnectionClosed)
	}
}
