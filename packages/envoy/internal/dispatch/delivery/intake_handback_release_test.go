package delivery

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// durableEvents records, in the order the server sent them, every answer published to the delivery
// durable ($JS.ACK...) and every creation of that durable ($JS.EVENT.ADVISORY.CONSUMER.CREATED...),
// on a connection of the test's own to uri. Both subscriptions feed one channel, which nats.go fills
// in the order the server's messages arrive, so the record orders an answer against a creation.
// That order cannot show a hand-back that made the recreated consumer deliver a message again: the
// server sends a creation's advisory after the new consumer already takes answers, so the rows also
// read that consumer itself (requireDeliveredOnceAfterTakeOver).
type durableEvents struct {
	mu     sync.Mutex
	events []string
}

const durableCreated = "created"

func watchDurableEvents(t *testing.T, uri string) *durableEvents {
	t.Helper()
	conn := testnats.Connect(t, uri)
	arrived := make(chan *natsgo.Msg, 4096)
	for _, subject := range []string{
		"$JS.ACK." + bus.Stream + "." + bus.DispatchDeliveryDurable + ".>",
		"$JS.EVENT.ADVISORY.CONSUMER.CREATED." + bus.Stream + "." + bus.DispatchDeliveryDurable,
	} {
		if _, err := conn.ChanSubscribe(subject, arrived); err != nil {
			t.Fatalf("subscribe to %s: %v", subject, err)
		}
	}
	if err := conn.FlushWithContext(testnats.Context(t)); err != nil {
		t.Fatalf("flush the durable's watch: %v", err)
	}
	recorded := &durableEvents{}
	go func() {
		for msg := range arrived {
			event := string(msg.Data)
			if strings.HasPrefix(msg.Subject, "$JS.EVENT.") {
				event = durableCreated
			}
			recorded.mu.Lock()
			recorded.events = append(recorded.events, event)
			recorded.mu.Unlock()
		}
	}()
	return recorded
}

// since is every event recorded from the from'th on.
func (r *durableEvents) since(from int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events[from:]...)
}

func (r *durableEvents) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// stopWithAHeldHandBack starts a holder and a standby, has the holder's handler hold one pull
// request with a second message of it waiting behind, stops the holder, and holds a write on the
// holder's link from inside the stop's hand-back of the waiting message, after the connected read
// has answered and before the publish (beforeHandBackPublish), so that publish waits for nats.go's
// lock. It returns once the write is held, with the stop under way. The stop's deadline,
// intakeStopBudget, is on the holder's stop clock (heldDurable.clock), a fake that moves only when a
// row moves it past that deadline (passStopDeadline).
func stopWithAHeldHandBack(t *testing.T) heldHandBack {
	t.Helper()
	// The hand-back is what must bring the message to the standby, so the server's own redelivery
	// at the ack wait never comes inside the run (ackWaitPastTheRun).
	ackWaitPastTheRun(t)
	var row heldHandBack
	pool, ctx := deliveryTestPool(t)
	row.pool = pool
	seedDeliverySettings(t, ctx, pool)
	natsURL := testnats.URL(t)
	link := testnats.StartHandshakeHold(t, natsURL)
	t.Cleanup(link.Restore)
	publisher := intakeTestClient(t)
	row.publisher = publisher
	row.recorded = watchDurableEvents(t, natsURL)

	const inFlight = 100
	row.stopClock = clocktest.New(time.Now())
	holder := startIntakeProcessWith(t, pool, link.URL(), func(number int) bool { return number == inFlight }, func(p *intakeProcess) {
		p.intake.clock = row.stopClock
	})
	row.holder = holder
	awaitBoundDurable(t, publisher)
	startIntakeProcess(t, pool, natsURL, nil)
	holderConn := holder.client.Connection()
	row.conn = holderConn

	row.sequences = append(row.sequences, publishMergedPullRequest(t, publisher, inFlight))
	waitFor(t, func() bool { return holder.fetches(inFlight) == 1 }, func() string {
		return "the holder never started on the first message: " + consumerState(publisher)
	})
	row.sequences = append(row.sequences, publishMergedPullRequest(t, publisher, inFlight))
	awaitAckPending(t, publisher, 2)

	// The stop's hand-backs go out only once its drain has answered (mayHandBack), and on a loaded
	// runner the drain can take longer than callNATS's wait: the stop then withholds its hand-backs,
	// and the row has no publish to hold. So the stop's calls wait for NATS with no bound of the
	// row's, and the row puts the wait back as the hand-back's publish begins, so the held write
	// still outlives it.
	wait := callNATSWait.Swap(math.MaxInt64)
	restoreWait := sync.OnceFunc(func() { callNATSWait.Store(wait) })
	t.Cleanup(restoreWait)
	var holding sync.Once
	held := make(chan struct{})
	// The hook runs on the stop's goroutine, where no method of t that ends the test may run: it
	// takes the write lock with no t (TakeWriteLock) and hands what it took to the test goroutine,
	// which fails the row on a give-up. The release is a cleanup from the hook itself, so a write the
	// hook takes after the row has failed is let go at the test's end too.
	type takenWrite struct {
		release func()
		err     error
	}
	taken := make(chan takenWrite, 1)
	hook := func() {
		holding.Do(func() {
			restoreWait()
			release, err := link.TakeWriteLock(holderConn)
			if err == nil {
				t.Cleanup(release)
			}
			taken <- takenWrite{release: release, err: err}
			close(held)
		})
	}
	row.delivery = subscribedDelivery(holder.intake)
	row.delivery.beforeHandBackPublish.Store(&hook)
	row.from = row.recorded.count()
	done := make(chan struct{})
	row.stopped = done
	go func() {
		defer close(done)
		holder.stop()
	}()
	waitFor(t, holder.keyedRunnerStopped, func() string {
		return "the stopping holder has not stopped its keyed runner: " + consumerState(publisher)
	})
	// The handler finishes as the stop waits for it; the stop then hands back the message that
	// was waiting behind it, and the hook holds the write before that publish.
	holder.release()
	waitFor(t, func() bool {
		select {
		case <-held:
			return true
		default:
			return false
		}
	}, func() string {
		return "control: the stop's hand-back never reached its publish: the stop withheld it, as it does when its drain has not answered: " + consumerState(publisher)
	})
	write := testwait.Receive(t, "the hook's take of the held write, sent before held closed", taken)
	if write.err != nil {
		t.Fatalf("the hook could not hold the write before the hand-back's publish: %v", write.err)
	}
	row.thaw = write.release
	return row
}

// heldHandBack is a holder's stop with a hand-back's publish held (stopWithAHeldHandBack): thaw
// lets the holder's link move and ends the held write; stopped closes when the holder's Run has
// returned; conn is the holder's connection, delivery the subscription its stop stopped, from the
// first durable event of the stop, stopClock the holder's stop clock, and sequences the stream
// sequences of the row's two messages.
type heldHandBack struct {
	holder    *intakeProcess
	pool      *store.Pool
	thaw      func()
	recorded  *durableEvents
	from      int
	stopped   <-chan struct{}
	publisher *bus.Client
	conn      *natsgo.Conn
	delivery  *intakeDelivery
	stopClock *clocktest.Fake
	sequences []uint64
}

// handBackPending reports whether a hand-back publish of the stopped delivery is still waiting for
// NATS.
func (r heldHandBack) handBackPending() bool {
	select {
	case <-r.delivery.handBacksLanded():
		return false
	default:
		return true
	}
}

// passStopDeadline moves the holder's stop clock to the stop's deadline, armed as its Run's context
// ended (stopContext), so the stop's context ends there.
func (r heldHandBack) passStopDeadline(t *testing.T) {
	t.Helper()
	armed := r.stopClock.AwaitArmed(t, "the holder's stop deadline, armed as its Run's context ended", 1)
	r.stopClock.Set(armed[0])
}

// holdLive reports whether the delivery durable's hold still names a holder, live.
func holdLive(t *testing.T, pool *store.Pool) bool {
	t.Helper()
	_, live, err := duty.HoldToken(t.Context(), pool, intakeHoldName(bus.DispatchDeliveryDurable))
	if err != nil {
		t.Fatalf("read the delivery durable's hold: %v", err)
	}
	return live
}

// holdRenewedBeforeItsExpiry reads the expiry the delivery durable's hold has now, then waits until
// a renewal moves it later or the database's clock passes it, and reports whether the renewal came
// first. Both are read off the database, whose clock the hold's expiry is on (duty.Hold). A hold left
// to lapse is past its expiry within the hold's TTL; the wait is testwait's, which only guards
// against a row that neither moves nor lapses.
func holdRenewedBeforeItsExpiry(t *testing.T, pool *store.Pool) bool {
	t.Helper()
	type reading struct{ expires, now time.Time }
	read := func() reading {
		t.Helper()
		var got reading
		if err := pool.QueryRow(t.Context(), "select expires_at, now() from duty_holds where name = $1",
			intakeHoldName(bus.DispatchDeliveryDurable)).Scan(&got.expires, &got.now); err != nil {
			t.Fatalf("read the delivery durable's hold: %v", err)
		}
		return got
	}
	first := read().expires
	last := testwait.Until(t, "the delivery durable's hold to be renewed past the expiry it had, or the database's clock to pass that expiry", func() (reading, bool) {
		got := read()
		return got, got.expires.After(first) || !got.now.Before(first)
	})
	return last.expires.After(first)
}

// requireHandBackBeforeCreation waits until the server has had both the holder's hand-back and the
// standby's creation of the durable, and fails the test when the hand-back came after, or when the
// standby's consumer delivered one of the row's messages (sequences) more than once.
func requireHandBackBeforeCreation(t *testing.T, recorded *durableEvents, from int, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	waitFor(t, func() bool {
		events := recorded.since(from)
		return slices.Contains(events, "-NAK") && slices.Contains(events, durableCreated)
	}, func() string {
		return fmt.Sprintf("the hand-back and the standby's creation of the durable did not both reach the server: %q; %s", recorded.since(from), consumerState(publisher))
	})
	events := recorded.since(from)
	if !slices.Contains(events[:slices.Index(events, durableCreated)], "-NAK") {
		t.Fatalf("the hand-back landed after the standby created the durable, so it reached the standby's consumer: %q", events)
	}
	if handBackAfterCreation(events) {
		t.Fatalf("a hand-back landed after the standby created the durable, so it reached the standby's consumer: %q", events)
	}
	requireDeliveredOnceAfterTakeOver(t, publisher, sequences)
}

// handBackAfterCreation reports whether a -NAK follows the first creation of the durable in events.
// It reads every -NAK, not only the first: a -NAK after the creation behind an earlier one counts.
func handBackAfterCreation(events []string) bool {
	created := slices.Index(events, durableCreated)
	return created >= 0 && slices.Contains(events[created+1:], "-NAK")
}

// requireDeliveredOnceAfterTakeOver waits until the consumer the standby created has delivered
// every message it had to and the standby has acknowledged them, and fails the test when it
// delivered one of them more than once. It had to deliver each of the row's messages (sequences)
// from its start sequence on (OptStartSeq, the durable's ack floor plus one; takeOverConfig): the
// message the holder's stop handed back, and the one before it too when the stop gave up the
// holder's handler at its deadline and the handler's deferred answer handed that one back as well
// (Intake.deliver). Delivered.Consumer counts every delivery, so a hand-back that made the new
// consumer deliver a message again makes it one more than that count. The recorded order cannot
// show that hand-back: nats-server v2.15.1 makes a new consumer's ack subscription live (setLeader,
// consumer.go:1491) before it queues the consumer's create advisory (consumer.go:1504), and sends
// the advisory from the stream's own send queue, so the -NAK can be recorded before "created".
// NumRedelivered does not keep it either: the standby's acknowledgement clears it
// (consumer.go:3802). A -NAK that reaches the new consumer before its first delivery of the
// message, or after the standby acknowledged it, changes nothing, because the server ignores it
// (processNak, consumer.go:3255 and :3259), and the row does not fail for it.
func requireDeliveredOnceAfterTakeOver(t *testing.T, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	var info *natsgo.ConsumerInfo
	waitFor(t, func() bool {
		read, err := publisher.JS().ConsumerInfo(bus.Stream, bus.DispatchDeliveryDurable)
		if err != nil {
			return false
		}
		info = read
		return info.Delivered.Consumer > 0 && info.NumAckPending == 0 && info.NumPending == 0
	}, func() string {
		return "the standby's consumer did not deliver the messages the holder left and have them acknowledged: " + consumerState(publisher)
	})
	start := info.Config.OptStartSeq
	var want uint64
	for _, sequence := range sequences {
		if sequence >= start {
			want++
		}
	}
	if info.Delivered.Consumer != want {
		t.Fatalf("the standby's consumer delivered %d time(s) from stream sequence %d, want %d, once for each of the row's messages from there (stream sequences %v): a hand-back made it deliver one again (last delivered stream sequence %d, redelivered %d)",
			info.Delivered.Consumer, start, want, sequences, info.Delivered.Stream, info.NumRedelivered)
	}
}

// requireNoHandBackAfterCreation waits for the standby's creation of the durable and fails the test
// when a hand-back reached the server after it, or when the standby's consumer delivered one of the
// row's messages (sequences) more than once. A hand-back the holder's client dropped with its
// connection never reaches the server, which is allowed: the message stays with the durable.
func requireNoHandBackAfterCreation(t *testing.T, recorded *durableEvents, from int, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	waitFor(t, func() bool { return slices.Contains(recorded.since(from), durableCreated) }, func() string {
		return fmt.Sprintf("the standby did not create the durable: %q; %s", recorded.since(from), consumerState(publisher))
	})
	if events := recorded.since(from); handBackAfterCreation(events) {
		t.Fatalf("a hand-back landed after the standby created the durable, so it reached the standby's consumer: %q", events)
	}
	requireDeliveredOnceAfterTakeOver(t, publisher, sequences)
}

// A hand-back the holder's stop published never reaches the durable a standby recreates.
//
// A hand-back is a publish at the message's reply subject, which names the durable rather than the
// consumer: one that reaches the server after the standby's take-over has deleted the durable and
// created it again under the same name moves that message's pending entry on the standby's consumer
// and has the standby delivered it a second time. The stop waits for each publish at most
// bus.ConnLockWait (intakeDelivery.publishHandBack) and goes on without one NATS has not taken; so
// the stop releases the hold only once every hand-back has reached the server; one still pending at
// the stop's deadline keeps the hold renewing until it has (heldDurable.finalizeHandBacks).
//
// All three subtests hold a write on the holder's link from inside its stop's hand-back
// (stopWithAHeldHandBack). Each reads, in the order the server sent them, the answers published to
// the durable and the durable's creations, on a connection of its own, and every step waits for an
// event. Once the standby has acknowledged the messages the holder left, each also reads the
// consumer the standby created and requires that it delivered each of them once: a hand-back can
// make that consumer deliver a message again before the server sends its creation's advisory,
// where the order alone passes it (requireDeliveredOnceAfterTakeOver).
//   - a hand-back that lands before the stop's deadline: the link moves once the stop has gone on
//     without the publish (its log line). The hold is released by the time the stop returns, and the
//     hand-back reached the server before the standby created the durable.
//   - a hand-back still pending at the stop's deadline: the link stays held, and the stop returns
//     while it is, with the hand-back still pending (handBacksLanded). The hold still names the
//     holder then, and the standby has not created the durable. With the link still held, the hold is
//     renewed before the database's clock passes the expiry it had as the stop returned, so it does
//     not lapse, and the standby still has not created the durable. Once the link moves, the
//     hand-back reaches the server before the standby creates the durable;
//   - a pending hand-back at shutdown: the Run returns at its deadline, then StartIntakes' stop
//     closes the client that can still send it and waits for its finalizer. The hold is gone before
//     that stop returns, so its caller can close the pool without leaving the hold to lapse. The
//     close sends the hand-back in its flush or drops it with the connection, whichever takes
//     nats.go's lock first; either way it does not reach the server after the standby creates the
//     durable.
//
// No step waits on a time but the hold's own expiry: the stop's own deadline (intakeStopBudget) is
// on the holder's stop clock, which the second and third rows move to it once the write is held and
// the first never moves, and every wait is testwait's, which only guards against a stop that never
// returns. The negative controls: a release that does not wait for the hand-backs fails the first's
// order; a stop that never releases its hold when a publish was left running fails the first's
// release; a release made at the deadline with a hand-back pending fails the second's hold; a stop
// that leaves its hold to lapse with a hand-back pending fails the second's renewal; an untracked
// finalizer fails the third by returning from its shutdown stop with the hold live; and the
// holder's -NAK published again while the standby holds the message, with the server holding the
// creation's advisory until the new consumer has delivered it a second time, fails all three by
// that second delivery. The count does not fail a stop that gives up the holder's handler at its
// deadline, whose deferred answer hands the first message back too: the new consumer then starts at
// that message and delivers both once.
func TestAHandBackLeftRunningLandsBeforeTheStandbyTakesOver(t *testing.T) {
	t.Run("a hand-back that lands before the stop's deadline", func(t *testing.T) {
		logs := captureLogs(t)
		row := stopWithAHeldHandBack(t)
		waitFor(t, func() bool { return loggedMessage(logs(), callLeftRunning) }, func() string {
			return "control: the stop never went on without the held hand-back's publish: " + consumerState(row.publisher)
		})
		row.thaw()
		testwait.Receive(t, "the holder's stop to return once the link moved", row.stopped)
		if holdLive(t, row.pool) {
			t.Fatalf("the stop kept its hold although its hand-back reached the server before its deadline, which costs the standby the lapse; the holder logged %q", loggedMessages(logs()))
		}
		requireHandBackBeforeCreation(t, row.recorded, row.from, row.publisher, row.sequences)
	})

	t.Run("a hand-back still pending at the stop's deadline", func(t *testing.T) {
		row := stopWithAHeldHandBack(t)
		row.passStopDeadline(t)
		testwait.Receive(t, "the holder's stop to return at its deadline while its hand-back is held (a stop that waits for the hand-back never does)", row.stopped)
		testnats.RequireLockHeld(t, row.conn)
		if !row.handBackPending() {
			t.Fatal("control: the stop's hand-back reached NATS while the write was held")
		}
		if !holdLive(t, row.pool) {
			t.Fatal("the stop released its hold with a hand-back still pending, so a standby may recreate the durable before the hand-back reaches the server")
		}
		if slices.Contains(row.recorded.since(row.from), durableCreated) {
			t.Fatalf("the standby created the durable while the holder's hand-back was pending: %q", row.recorded.since(row.from))
		}
		if !holdRenewedBeforeItsExpiry(t, row.pool) {
			t.Fatalf("the hold reached its expiry unrenewed with the holder's hand-back still pending, so the standby can recreate the durable before a link that heals sends the hand-back to its consumer: %q", row.recorded.since(row.from))
		}
		testnats.RequireLockHeld(t, row.conn)
		if !row.handBackPending() {
			t.Fatal("control: the stop's hand-back reached NATS while the write was held")
		}
		if slices.Contains(row.recorded.since(row.from), durableCreated) {
			t.Fatalf("the standby created the durable while the holder's hand-back was pending and its hold renewing: %q", row.recorded.since(row.from))
		}
		row.thaw()
		requireHandBackBeforeCreation(t, row.recorded, row.from, row.publisher, row.sequences)
	})

	t.Run("a pending hand-back at shutdown", func(t *testing.T) {
		row := stopWithAHeldHandBack(t)
		row.passStopDeadline(t)
		testwait.Receive(t, "the holder's Run to return at its deadline while its hand-back is held", row.stopped)
		if !row.handBackPending() {
			t.Fatal("control: the hand-back reached NATS before its client closed")
		}
		finalizing := make(chan struct{})
		releaseFinalizer := make(chan struct{})
		release := sync.OnceFunc(func() { close(releaseFinalizer) })
		hook := func() {
			close(finalizing)
			<-releaseFinalizer
		}
		finalizerBeforeRelease.Store(&hook)
		t.Cleanup(func() { finalizerBeforeRelease.Store(nil) })
		var runs sync.WaitGroup
		closed := make(chan struct{})
		go func() {
			stopIntakes(&runs, &row.holder.intake.heldDurable)
			close(closed)
		}()
		// A failure from here to the release leaves the finalizer held in the hook and the stop
		// waiting for it. These cleanups run first, in the reverse of this order: they release the
		// finalizer, thaw the holder's link should the row not have reached that yet, and wait for
		// the stop to return before the cleanups above close the pool it releases its hold through;
		// a give-up of that wait once the row has failed is logged rather than failed again
		// (failOnce).
		t.Cleanup(func() {
			testwait.Receive(failOnce{t}, "StartIntakes' stop to return once the row's cleanup released its finalizer", closed)
		})
		t.Cleanup(row.thaw)
		t.Cleanup(release)
		testnats.RequireLockHeld(t, row.conn)
		row.thaw()
		testwait.Receive(t, "the client's close to settle the pending hand-back", finalizing)
		select {
		case <-closed:
			t.Fatal("StartIntakes' stop returned while its finalizer was held before release")
		default:
		}
		if !holdLive(t, row.pool) {
			t.Fatal("the hold was released before the finalizer's release step")
		}
		release()
		testwait.Receive(t, "StartIntakes' stop to return once the finalizer is released", closed)
		if holdLive(t, row.pool) {
			t.Fatal("the pending hand-back's finalizer released but left its hold live: shutdown could close the pool before releasing it")
		}
		requireNoHandBackAfterCreation(t, row.recorded, row.from, row.publisher, row.sequences)
	})
}
