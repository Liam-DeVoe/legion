package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/testnats"
)

// lostReplyPoll is how often the row below reads the hold, the standby's connection and the
// durable: fine enough that what it measures is the take-over and not its own polling.
const lostReplyPoll = 5 * time.Millisecond

// A holder that dies is replaced within the bound held_durable.go states, intakeHoldTTL plus one
// durableBindInterval, when the standby's take-over loses a JetStream reply with its connection.
// The standby's link runs through a relay that drops the take-over's read of the durable under the
// consumer lease, and the row then cuts that link: nats.go fails no request its connection drops,
// so without the connection watch the read waited out the JetStream context's 10 s, the attempt
// ended on it, and the next poll took the durable over 20 s or more after the holder died.
//
// The holder dies halfway through a renewal period, as a kill lands anywhere in one. A death the
// instant after a renewal, with the standby's poll the instant before the lapse, reaches the bound
// with no reply lost at all; halfway through, the bound leaves the take-over the room it leaves an
// ordinary one, and the row's own cut, which keeps the standby off NATS for nats.go's reconnect
// wait (about 1 s), counts against it like every other moment from the death to the durable bound.
func TestATakeOverThatLosesAReplyWithItsConnectionReplacesADeadHolderWithinTheStatedBound(t *testing.T) {
	pool, ctx := deliveryTestPool(t)
	seedDeliverySettings(t, ctx, pool)
	natsURL := testnats.URL(t)
	publisher := intakeTestClient(t)

	holder := startIntakeProcess(t, pool, natsURL, nil)
	awaitBoundDurable(t, publisher)
	held, err := publisher.JS().ConsumerInfo(bus.Stream, bus.DispatchDeliveryDurable)
	if err != nil {
		t.Fatalf("read the durable the holder bound: %v", err)
	}
	relay := testnats.StartUnansweredRelayOn(t, natsURL, "$JS.API.CONSUMER.INFO."+bus.Stream+"."+bus.DispatchDeliveryDurable)
	standby := startIntakeProcess(t, pool, relay.URL(), nil)
	// While the holder lives the standby stands by at the hold and reads nothing of the durable.
	// Once it holds the hold, its first read is the lookup outside the consumer lease, which the
	// relay passes, and its second the take-over's read under the lease, which the relay drops.
	relay.PassThenDrop(1, 1)

	awaitHalfwayThroughARenewal(t, ctx, pool)
	abandoned := time.Now()
	holder.leases.cut.Store(true)
	holder.client.Close()

	waitFor(t, intakeHoldTTL+2*durableBindInterval, func() bool { return relay.Sent() >= 2 }, func() string {
		return "the standby did not send the take-over's read of the durable: " + consumerState(publisher)
	})
	conn := standby.client.Connection()
	before := conn.Stats().Reconnects
	cut := time.Now()
	relay.Drop()
	for conn.Stats().Reconnects == before {
		if time.Since(cut) > 10*time.Second {
			t.Fatal("the standby's connection did not reconnect through the relay within 10s")
		}
		time.Sleep(lostReplyPoll)
	}

	var tookOver time.Time
	for tookOver.IsZero() {
		if time.Since(abandoned) > 40*time.Second {
			t.Fatalf("the standby has not taken the durable over 40s after the holder died: %s", consumerState(publisher))
		}
		info, err := publisher.JS().ConsumerInfo(bus.Stream, bus.DispatchDeliveryDurable)
		if err == nil && info.PushBound && info.Config.DeliverSubject != held.Config.DeliverSubject {
			tookOver = time.Now()
		}
		time.Sleep(lostReplyPoll)
	}
	took := tookOver.Sub(abandoned)
	if bound := intakeHoldTTL + durableBindInterval; took > bound {
		t.Fatalf("the standby took the durable over %s after the holder died, with one reply of its take-over lost with its connection; want within intakeHoldTTL plus one durableBindInterval (%s)",
			took.Round(time.Millisecond), bound)
	}
	t.Logf("the standby took the durable over %s after the holder died", took.Round(time.Millisecond))
}

// awaitHalfwayThroughARenewal waits until the delivery durable's hold was last renewed at least
// half a renewal period ago (intakeHoldRenew) and its next renewal is still half a second off, as
// the hold's row on the database's clock says.
func awaitHalfwayThroughARenewal(t *testing.T, ctx context.Context, pool *store.Pool) {
	t.Helper()
	deadline := time.Now().Add(2 * intakeHoldTTL)
	for {
		var seconds float64
		if err := pool.QueryRow(ctx, `select extract(epoch from expires_at - now())::float8 from duty_holds where name = $1`, intakeHold).Scan(&seconds); err != nil {
			t.Fatalf("read the holder's hold: %v", err)
		}
		left := time.Duration(seconds * float64(time.Second))
		if left <= intakeHoldTTL-intakeHoldRenew/2 && left >= intakeHoldTTL-intakeHoldRenew+500*time.Millisecond {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the holder's hold had %s left at the last read; it never came halfway through a renewal", left)
		}
		time.Sleep(lostReplyPoll)
	}
}
