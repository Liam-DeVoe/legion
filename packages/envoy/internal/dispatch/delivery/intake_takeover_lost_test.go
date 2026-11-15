package delivery

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	natsgo "github.com/nats-io/nats.go"

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
// The holder dies halfway through a renewal period, as a kill lands anywhere in one, so its hold
// lapses 10.5 to 12.5 s after the death and the standby tries again at the lapse
// (heldDurable.run). The row's own cut, which keeps the standby off NATS for nats.go's reconnect
// wait (about 1 s), counts against the bound like every other moment from the death to the
// durable bound. The worst phase, a death the instant after a renewal, whose hold lasts the whole
// intakeHoldTTL from the death, is TestADeadHolderIsReplacedWithinTheStatedBoundAtTheWorstPhase's.
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

	took := awaitTakenOver(t, publisher, held, abandoned).Sub(abandoned)
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

// awaitTakenOver waits for the durable to be push-bound to a deliver subject other than held's,
// reading it every lostReplyPoll, and answers when it first read it so: the take-over's
// completion, which a holder that died at abandoned has not seen within 40 s fails.
func awaitTakenOver(t *testing.T, publisher *bus.Client, held *natsgo.ConsumerInfo, abandoned time.Time) time.Time {
	t.Helper()
	for {
		if time.Since(abandoned) > 40*time.Second {
			t.Fatalf("the standby has not taken the durable over 40s after the holder died: %s", consumerState(publisher))
		}
		info, err := publisher.JS().ConsumerInfo(bus.Stream, bus.DispatchDeliveryDurable)
		if err == nil && info.PushBound && info.Config.DeliverSubject != held.Config.DeliverSubject {
			return time.Now()
		}
		time.Sleep(lostReplyPoll)
	}
}

// worstPhaseLead is how long before the hold's lapse the worst-phase row aims the standby's poll,
// and half the window it holds that poll to. A poll aimed at the lapse itself would go out after
// it -- the run's ticker fires about a millisecond late, and the holder's renewal lands up to a
// few milliseconds either side of its own tick -- and so take the hold at once, which is the best
// phase and not the worst. Aimed this far ahead, it went out 5 to 13 ms before the lapse in the
// runs held_durable.go records, the last poll to find the hold live.
const worstPhaseLead = 10 * time.Millisecond

// A holder that dies is replaced within the bound held_durable.go states at the worst phase there
// is, with no reply lost: the holder dies the instant after a renewal landed, so its hold lasts the
// whole intakeHoldTTL from the death, and the standby's poll goes out just before the lapse, so it
// is the last poll to find the hold live. The standby tries again at the lapse that poll read
// (heldDurable.run). held_durable.go records what twenty runs of this row measured, and what they
// measured before it did, when its next try was its next poll, a whole durableBindInterval after
// this one, and every run passed the bound.
//
// The phase is forced rather than waited for. The holder renews every intakeHoldRenew from when it
// took the hold, and the standby polls every durableBindInterval from when its run starts, so the
// row reads one renewal off the hold's row, starts the standby's run (newTakeOverProcess builds it
// unstarted) a whole number of intervals before the moment worstPhaseLead ahead of the lapse the
// next renewal sets, waits for that renewal, and kills the holder as soon as the row shows it. The
// standby's lease pool notes when each read of the hold went out (holdReads). The row fails rather
// than measure another phase when the holder died more than worstPhaseLead after the renewal, or
// when no read went out within twice worstPhaseLead before the lapse with a later read taking the
// hold: a read that finds the hold free is the standby's last, since it holds the hold from then on.
func TestADeadHolderIsReplacedWithinTheStatedBoundAtTheWorstPhase(t *testing.T) {
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
	standby := newTakeOverProcess(t, pool, natsURL)
	reads := &holdReads{intakeLeases: standby.intake.leases}
	standby.intake.leases = reads

	renewed := awaitRenewal(t, ctx, pool, readHold(t, ctx, pool), lostReplyPoll)
	start := renewed.lapse.Add(intakeHoldRenew - worstPhaseLead)
	for time.Until(start) > durableBindInterval {
		start = start.Add(-durableBindInterval)
	}
	// Spun up to rather than slept to: a sleep wakes up to milliseconds late.
	time.Sleep(time.Until(start) - 10*time.Millisecond)
	for time.Now().Before(start) {
	}
	startIntake(t, standby.intake)

	time.Sleep(time.Until(renewed.landed().Add(intakeHoldRenew - 20*time.Millisecond)))
	last := awaitRenewal(t, ctx, pool, renewed, 0)
	abandoned := time.Now()
	holder.leases.cut.Store(true)
	holder.client.Close()

	took := awaitTakenOver(t, publisher, held, abandoned).Sub(abandoned)
	died := abandoned.Sub(last.landed())
	var fromLapse []time.Duration
	live := -1
	for i, sent := range reads.times() {
		fromLapse = append(fromLapse, sent.Sub(last.lapse))
		if sent.Before(last.lapse) {
			live = i
		}
	}
	if died > worstPhaseLead || live < 0 || live == len(fromLapse)-1 || -fromLapse[live] > 2*worstPhaseLead {
		t.Fatalf("the row did not force the worst phase: the holder died %s after its last renewal landed, want within %s; the standby read the hold at %v from the lapse, want the last read before it within %s and a later one taking the hold",
			died, worstPhaseLead, fromLapse, 2*worstPhaseLead)
	}
	t.Logf("the holder died %s after its last renewal landed; the standby's poll went out %s before the lapse and found the hold live, and its next read, %s after the lapse, took it; the standby took the durable over %s after the holder died",
		died, -fromLapse[live], fromLapse[live+1], took)
	if bound := intakeHoldTTL + durableBindInterval; took > bound {
		t.Fatalf("the standby took the durable over %s after the holder died the instant after a renewal, its poll just before the lapse; want within intakeHoldTTL plus one durableBindInterval (%s)",
			took.Round(time.Millisecond), bound)
	}
}

// holdReads is a standby's lease pool that notes when each of its reads of its durable's hold
// goes out. That read is the one statement it sends through QueryRow (duty.HoldToken).
type holdReads struct {
	intakeLeases

	mu   sync.Mutex
	sent []time.Time
}

func (r *holdReads) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.mu.Lock()
	r.sent = append(r.sent, time.Now())
	r.mu.Unlock()
	return r.intakeLeases.QueryRow(ctx, sql, args...)
}

// times is when each read went out, in order.
func (r *holdReads) times() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent)
}

// holdRow is the delivery durable's hold as one read of its row found it: the expiry the last
// renewal wrote, on the database's clock, and that lapse on this process's clock, read as the
// time the hold had left from halfway through the read.
type holdRow struct {
	expires time.Time
	lapse   time.Time
}

// landed is when the renewal that wrote the row landed, on this process's clock.
func (r holdRow) landed() time.Time { return r.lapse.Add(-intakeHoldTTL) }

// readHold reads the delivery durable's hold's row.
func readHold(t *testing.T, ctx context.Context, pool *store.Pool) holdRow {
	t.Helper()
	var row holdRow
	var seconds float64
	sent := time.Now()
	if err := pool.QueryRow(ctx, `select expires_at, extract(epoch from expires_at - now())::float8 from duty_holds where name = $1`,
		intakeHold).Scan(&row.expires, &seconds); err != nil {
		t.Fatalf("read the holder's hold: %v", err)
	}
	row.lapse = sent.Add(time.Since(sent) / 2).Add(time.Duration(seconds * float64(time.Second)))
	return row
}

// awaitRenewal reads the hold's row every every until a renewal later than previous's has landed,
// and answers the row it wrote.
func awaitRenewal(t *testing.T, ctx context.Context, pool *store.Pool, previous holdRow, every time.Duration) holdRow {
	t.Helper()
	deadline := time.Now().Add(2 * intakeHoldRenew)
	for {
		row := readHold(t, ctx, pool)
		if row.expires.After(previous.expires) {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("the holder's hold was not renewed within %s", 2*intakeHoldRenew)
		}
		time.Sleep(every)
	}
}
