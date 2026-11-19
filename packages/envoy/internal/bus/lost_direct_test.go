package bus

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
)

// A direct call on the client's connection that a forced reconnect answered
// nats.ErrConnectionClosed is lost however late its answer is read. The keeper of a service's token
// reconnects with ForceReconnect to present a new token (natscred), which answers every pending
// flush (clearPendingFlushCalls, nats.go v1.53.1 :2604-2607, :5948-5949) and request that way and
// reconnects in place. The answer is read only once nats.go and the record of the connection's
// status both read the connection connected again, so its state says nothing of the reconnect: a
// close answer for a call made on conn itself is a loss of conn (lostIfClosed).
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
	awaitRecord(t, recordOf(client), "CONNECTED again after the forced reconnect", func(s nats.Status) bool { return s == nats.CONNECTED })
	if got := client.lostIfClosed(nats.ErrConnectionClosed, conn, false); !errors.Is(got, ErrConnectionLost) {
		t.Fatalf("a flush a forced reconnect answered %v, read once the connection was back, answered %v, want ErrConnectionLost", nats.ErrConnectionClosed, got)
	}
	if got := client.lostIfClosed(nats.ErrConnectionClosed, nil, false); !errors.Is(got, nats.ErrConnectionClosed) || errors.Is(got, ErrConnectionLost) {
		t.Fatalf("a call on the handle of a replaced connection (nil) answered %v, want nats.go's %v", got, nats.ErrConnectionClosed)
	}
}

// probeCallers is how many callers the row below sends to the record during one held handshake.
const probeCallers = 1000

// Callers that the record answers up while nats.go holds its lock for a reconnect's handshake share
// one probe of nats.go's state (connStatus.probe): each answers down at heldLockBudget, and the
// handshake, however long, leaves one goroutine waiting on nats.go's lock, not one per caller. The
// row holds the record (holdRecord), so it still reads the connection up through the cut and the
// held handshake, sends probeCallers callers through upNow at once, and counts the probes started.
//
// The control is this row with probe starting a probe for each caller: the count is probeCallers.
func TestCallersDuringAHeldHandshakeShareOneProbe(t *testing.T) {
	client, proxy := proxiedClient(t)
	record := holdRecord(t, client)
	before := record.probes.Load()
	proxy.Cut()
	hold := proxy.HoldConnectAnswers()
	awaitHeld(t, hold)
	var callers sync.WaitGroup
	var up atomic.Int64
	for range probeCallers {
		callers.Go(func() {
			if ok, _ := record.upNow(); ok {
				up.Add(1)
			}
		})
	}
	callers.Wait()
	probes := record.probes.Load() - before
	t.Logf("%d callers during the held handshake started %d probes; %d read the connection up", probeCallers, probes, up.Load())
	if up.Load() != 0 {
		t.Fatalf("%d of %d callers read the connection up during the held handshake, want none", up.Load(), probeCallers)
	}
	if probes != 1 {
		t.Fatalf("%d callers during the held handshake started %d probes, want 1", probeCallers, probes)
	}
}
