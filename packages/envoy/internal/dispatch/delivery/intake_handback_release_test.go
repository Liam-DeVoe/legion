package delivery

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// durableEvents records, in the order the server sent them, every answer published to the delivery
// durable ($JS.ACK...) and every creation of that durable ($JS.EVENT.ADVISORY.CONSUMER.CREATED...),
// on a connection of the test's own to uri. Both subscriptions feed one channel, which nats.go fills
// in the order the server's messages arrive, so the record orders an answer against a creation.
// That order cannot show a hand-back that made the recreated consumer deliver a message again: the
// server sends a creation's advisory after the new consumer already takes answers, so the rows also
// read that consumer itself (requireDeliveredOnceAfterTakeOver). For a row's trace (stopTrace), it
// also keeps when it recorded each event and the subject of each: an answer's subject is the reply
// subject of the delivery it answers.
type durableEvents struct {
	mu     sync.Mutex
	events []durableEvent
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
	if err := conn.Flush(); err != nil {
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
			recorded.events = append(recorded.events, durableEvent{at: time.Now(), event: event, subject: msg.Subject})
			recorded.mu.Unlock()
		}
	}()
	return recorded
}

// since is every event recorded from the from'th on.
func (r *durableEvents) since(from int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]string, 0, len(r.events)-from)
	for _, event := range r.events[from:] {
		events = append(events, event.event)
	}
	return events
}

func (r *durableEvents) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// durableEvent is one event durableEvents recorded: when, what, and the subject the server sent it
// on.
type durableEvent struct {
	at      time.Time
	event   string
	subject string
}

// timed is every event recorded so far, with its time and its subject.
func (r *durableEvents) timed() []durableEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// heldPublish names whose hand-back of the row's second message a row holds before its publish
// (stopWithAHeldHandBack).
type heldPublish int

const (
	// stopsHandBack is the stop's own hand-back: the second message waits behind its key's
	// handler, the stop takes it from the keyed runner and hands it back (intakeDelivery.stop).
	stopsHandBack heldPublish = iota
	// callbacksHandBack is the callback's hand-back: the callback of the second message runs once
	// the stop has handed back what it held, as nats.go runs it during the drain, and hands the
	// message back itself (intakeDelivery.receive, giveBack).
	callbacksHandBack
)

// stopWithAHeldHandBack starts a holder and a standby, has the holder's handler hold one pull
// request with a second message of it behind, stops the holder, and holds a write on the holder's
// link (testnats.HandshakeHold.ParkAWrite: a real write parked in the holder's socket, which the
// client's write deadline can end) while a hand-back of the second message waits, after the
// connected read has answered and before the publish (beforeHandBackPublish), so that publish
// waits for nats.go's lock. by names whose hand-back that is:
//   - stopsHandBack: the second message waits behind its key's handler, and the stop hands it
//     back. Its publish goes on once the write is held. The row stops the holder only once the
//     second message waits behind its key in the holder. Two messages outstanding at the server
//     do not show that: the server counts a push message as delivered as soon as it sends it,
//     before the message reaches the holder's client.
//   - callbacksHandBack: the callback of the second message waits before it routes the message
//     (beforeReceive) until the stop has handed back what it held (released), and then hands the
//     message back itself. Its publish goes on only when the row thaws the link, so the stop
//     decides whether to wait for that hand-back while its publish is still held. The stop's drain
//     loop waits in beforeDrainWait until that hand-back has reached its publish hook, where the
//     row puts the production wait back. So the row's own wait for the stop's calls, which runs to
//     the test binary's deadline (callNATSWait), is gone before the loop starts, and each of its
//     reads of whether NATS is connected waits at most bus.ConnLockWait.
//
// It returns once the write is held, with the stop under way. The stop's deadline is
// intakeStopBudget, set to budget. The holder's handler has no deadline of its own short of the
// test binary's (intakeMessageTimeout), so it holds the first message until the row releases it or
// the stop gives it up. The holder's client connects with holderOptions: the frozen-link
// shutdown subtest passes withIntakeWriteDeadline, and every other subtest passes none, so its
// holder keeps nats.go's default write deadline of a minute. Those subtests hold the write on
// purpose past the stop's deadline, or across steps of their own that can take more than 5 s under
// the race detector: they test the stop's logic while a write holds nats.go's lock, which
// production's write deadline (IntakeWriteDeadline) ends after 5 s.
//
// The row records a trace (traceStops), which the test prints only when it fails: the clients are
// named holder, standby and publisher, the holder's link reports each chunk with a -NAK or a +ACK,
// and at the failure the trace reads the holder's and the standby's deliveries and lists the
// durable's events. Each run also logs the reconnect count of the holder's and the standby's
// clients as it ends, passing or failing (logReconnects).
func stopWithAHeldHandBack(t *testing.T, budget time.Duration, by heldPublish, holderOptions ...bus.ConnectOption) heldHandBack {
	t.Helper()
	trace := traceStops(t)
	// The holder's handler holds the first message until the row releases it or the stop gives it
	// up, never until a deadline of its own: the second message has to queue behind it. Set before
	// the intakes start, whose take-overs read it (intakeAckWait).
	messageTimeout := intakeMessageTimeout
	intakeMessageTimeout = untilTheBinaryDeadline(t)
	t.Cleanup(func() { intakeMessageTimeout = messageTimeout })
	before := intakeStopBudget
	intakeStopBudget = budget
	t.Cleanup(func() { intakeStopBudget = before })
	var row heldHandBack
	pool, ctx := deliveryTestPool(t)
	row.pool = pool
	seedDeliverySettings(t, ctx, pool)
	natsURL := testnats.URL(t)
	link := testnats.StartHandshakeHold(t, natsURL)
	row.link = link
	t.Cleanup(link.Restore)
	trace.watchLink(link, "holder")
	publisher := intakeTestClient(t)
	trace.name(publisher, "publisher")
	row.publisher = publisher
	row.recorded = watchDurableEvents(t, natsURL)
	trace.watchDurable(row.recorded)

	const inFlight = 100
	holder := startIntakeProcess(t, pool, link.URL(), func(number int) bool { return number == inFlight }, holderOptions...)
	row.holder = holder
	trace.name(holder.client, "holder")
	awaitBoundDurable(t, publisher)
	standby := startIntakeProcess(t, pool, natsURL, nil)
	logReconnects(t, holder.client, standby.client)
	trace.name(standby.client, "standby")
	trace.name(standby.client.Connection(), "standby")
	holderConn := holder.client.Connection()
	trace.name(holderConn, "holder")
	row.conn = holderConn
	trace.dump("holder delivery", func() string { return namedDeliveryState(trace, subscribedDelivery(holder.intake), "holder") })
	trace.dump("standby delivery", func() string { return namedDeliveryState(trace, subscribedDelivery(standby.intake), "standby") })

	row.sequences = append(row.sequences, publishMergedPullRequest(t, publisher, inFlight))
	waitFor(t, untilTheBinaryDeadline(t), func() bool { return holder.fetches(inFlight) >= 1 }, func() string {
		return "the holder never started on the first message: " + consumerState(publisher)
	})
	row.delivery = subscribedDelivery(holder.intake)
	trace.name(row.delivery, "holder")
	// In a callback row, the callback of the second message waits before it routes the message,
	// so the message never reaches its key and the stop holds nothing of it. The hook is stored
	// once the first message's handler has started, so the first message it sees is the second.
	// It waits until the test's goroutine lets it go on (receive), once the stop has set released;
	// the cleanup, which runs before the holder's own, lets it go on when the test ends early.
	receiving := make(chan struct{})
	receive := func() {}
	if by == callbacksHandBack {
		goOn := make(chan struct{})
		receive = sync.OnceFunc(func() { close(goOn) })
		t.Cleanup(receive)
		var first sync.Once
		hook := func(*natsgo.Msg) {
			first.Do(func() {
				close(receiving)
				<-goOn
			})
		}
		row.delivery.beforeReceive.Store(&hook)
	}
	// The second message has to be in the holder's hands before the stop begins: in a stop row
	// queued behind the first, or the stop has nothing to hand back, and in a callback row held by
	// its callback (receiving). The durable's count of messages outstanding does not say that:
	// nats-server counts a message delivered (consumer.go, deliverMsg's trackPending) before its
	// stream's send loop puts it on the wire (stream.go, internalLoop), and a stop whose drain
	// reaches the server first leaves the message with the durable, never sent to the holder.
	row.sequences = append(row.sequences, publishMergedPullRequest(t, publisher, inFlight))
	if by == stopsHandBack {
		waitFor(t, untilTheBinaryDeadline(t), func() bool { return queuedBehindKeys(row.delivery) == 1 }, func() string {
			return fmt.Sprintf("control: %d message(s) wait behind the holder's handler, want the second message: %s", queuedBehindKeys(row.delivery), consumerState(publisher))
		})
	}
	if by == callbacksHandBack {
		select {
		case <-receiving:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("control: the holder's callback was never handed the second message: " + consumerState(publisher))
		}
	}

	// The stop's hand-backs go out only once its drain has answered (mayHandBack), and each of the
	// stop's calls waits for NATS at most callNATSWait: a drain that answers after it has the stop
	// withhold its hand-backs, and the row has no publish to hold. So the stop's calls wait as long
	// as the test binary's deadline allows, and the wait is put back as the hand-back's publish
	// begins, so the held write still outlives it.
	//
	// The hand-back waits in its hook, before its publish, until the test's goroutine holds the
	// write lock, and in a callback row until the row thaws the link. Every bounded wait and every
	// failure is on the test's goroutine, not the delivery's, so the hand-back reaching its publish
	// and the write taking the lock each have a bound and a message of their own. ended, closed by
	// a cleanup that runs before the holder's own, lets a hand-back still held there go on when the
	// test ends early.
	wait := callNATSWait.Swap(int64(untilTheBinaryDeadline(t)))
	restoreWait := sync.OnceFunc(func() { callNATSWait.Store(wait) })
	t.Cleanup(restoreWait)
	var holding sync.Once
	reached := make(chan struct{})
	held := make(chan struct{})
	ended := make(chan struct{})
	t.Cleanup(func() { close(ended) })
	hook := func() {
		holding.Do(func() {
			restoreWait()
			close(reached)
			select {
			case <-held:
			case <-ended:
			}
		})
	}
	row.delivery.beforeHandBackPublish.Store(&hook)
	// In a callback row, the stop's drain loop waits in its hook (beforeDrainWait) until the test's
	// goroutine has seen the callback's hand-back reach its publish hook, which puts the wait back.
	// So each read of the loop waits at most bus.ConnLockWait. Without this hook, a read that starts
	// under the row's own wait, to the test binary's deadline, and takes nats.go's lock only after
	// the test's goroutine holds the write can wait as long as that, past any wait of the subtest's.
	// A stop row needs no such hook: the stop's own hand-back reaches the publish hook on the stop's
	// goroutine before the loop starts. The test's goroutine closes drainMayBegin in either row, and
	// ended lets the loop begin when the test ends early.
	drainMayBegin := make(chan struct{})
	if by == callbacksHandBack {
		beforeDrain := func() {
			select {
			case <-drainMayBegin:
			case <-ended:
			}
		}
		row.delivery.beforeDrainWait.Store(&beforeDrain)
	}
	row.from = row.recorded.count()
	trace.stopBegins(row.from)
	done := make(chan struct{})
	row.stopped = done
	go func() {
		holder.stop()
		close(done)
	}()
	waitFor(t, untilTheBinaryDeadline(t), holder.keyedRunnerStopped, func() string {
		return "the stopping holder has not stopped its keyed runner: " + consumerState(publisher)
	})
	// The handler finishes as the stop waits for it. In a stop row, the stop then hands back the
	// message that was waiting behind it. In a callback row, the stop then sets released, and the
	// test lets the callback go on, which hands the second message back at once (giveBack). Either
	// hand-back waits in the hook, before its publish. In a stop row the hook runs on a goroutine
	// the holder's Run waits for -- the stop's own, or a handler's the stop gave up, whose deferred
	// answer hands its message back -- so a hand-back reaches the hook before Run returns, and a Run
	// that returned with reached still open made none. In a callback row the stop's drain loop waits
	// for drainMayBegin, which the test's goroutine closes only once reached is, so done does not
	// close before reached unless the test ends. reached is read again once done is, as both can be
	// closed by the time the select runs.
	holder.release()
	withheld := "control: the holder's stop returned with no hand-back having reached its publish: it withheld them: "
	unreached := "the holder's stop neither published a hand-back nor returned: "
	if by == callbacksHandBack {
		waitFor(t, untilTheBinaryDeadline(t), holder.heldListReleased, func() string {
			return "control: the stop never handed back what it held, so the callback would hold its message rather than hand it back: " + consumerState(publisher)
		})
		receive()
		withheld = "control: the holder's stop returned with no hand-back of the callback's having reached its publish: the callback withheld it: "
		unreached = "control: the callback's hand-back never reached its publish: the callback withheld it, as it does when the stop's drain has not answered: "
	}
	select {
	case <-reached:
	case <-done:
		select {
		case <-reached:
		default:
			t.Fatal(withheld + consumerState(publisher))
		}
	case <-time.After(untilTheBinaryDeadline(t)):
		t.Fatal(unreached + consumerState(publisher))
	}
	close(drainMayBegin)
	row.thaw = link.ParkAWrite(t, holderConn)
	// Registered after every cleanup of the row's own, so a failure is read before they move the
	// link, stop the processes or close the clients.
	trace.printAtFailure(t)
	if by == callbacksHandBack {
		thaw := row.thaw
		row.thaw = sync.OnceFunc(func() {
			close(held)
			thaw()
		})
		return row
	}
	close(held)
	return row
}

// heldHandBack is a holder's stop with a hand-back's publish held (stopWithAHeldHandBack): thaw
// lets the holder's link move and ends the held write, and in a callback row first lets the held
// publish go on; stopped closes when the holder's Run has returned; conn is the holder's
// connection and link the relay it goes through, delivery the subscription its stop stopped,
// from the first durable event of the stop, and sequences the stream sequences of the row's two
// messages.
type heldHandBack struct {
	holder    *intakeProcess
	pool      *store.Pool
	thaw      func()
	recorded  *durableEvents
	from      int
	stopped   <-chan struct{}
	publisher *bus.Client
	conn      *natsgo.Conn
	link      *testnats.HandshakeHold
	delivery  *intakeDelivery
	sequences []uint64
}

// handBackPending reports whether a hand-back of the stopped delivery is still taken and not
// settled: in these rows, a publish still waiting for NATS.
func (r heldHandBack) handBackPending() bool {
	select {
	case <-r.delivery.handBacks.handBacksLanded():
		return false
	default:
		return true
	}
}

// stopBudget is the stop's deadline in the rows whose stop must reach its deadline with the
// hand-back still pending (intakeStopBudget): short, since each waits it out.
const stopBudget = 3 * time.Second

// landingBudget is the stop's deadline in the row whose hand-back must land before it. Every step
// of that row between the stop's start and the link moving (the write's hold, the stop's own calls
// under it, the subtest's wait for the stop to start waiting for its hand-back) is spent from it, so
// it is long: with stopBudget, CI job 136549646454 of envoy-go-race saw the stop's deadline pass
// before the hand-back landed, and the subtest then read the hold the finalizer keeps.
const landingBudget = 30 * time.Second

// untilTheBinaryDeadline is how long the rows below wait for an event that ends a wait of their own
// or of the stop's: what the test binary's deadline leaves less rowDeadlineMargin, so that a row
// whose event never comes fails by name before go test's own timeout, and none of these waits ends
// at a span a loaded runner can outlast. A run with no deadline (-timeout 0) waits rowWaitUnbounded.
func untilTheBinaryDeadline(t *testing.T) time.Duration {
	t.Helper()
	deadline, bounded := t.Deadline()
	if !bounded {
		return rowWaitUnbounded
	}
	left := time.Until(deadline)
	if left <= rowDeadlineMargin {
		t.Fatalf("the test binary's deadline leaves %s, not more than rowDeadlineMargin (%s): this looks like a misconfigured run; raise -timeout", left.Round(time.Millisecond), rowDeadlineMargin)
	}
	return left - rowDeadlineMargin
}

const (
	// rowDeadlineMargin is what untilTheBinaryDeadline leaves of the test binary's deadline for a
	// failed row's cleanups.
	rowDeadlineMargin = time.Minute
	// rowWaitUnbounded is the wait of a run that asked for no deadline.
	rowWaitUnbounded = time.Hour
)

// holderHolds reports whether the delivery durable's hold is live and names holder's own process.
// The standby is woken by the release (duty.HoldReleasedChannel) and takes the hold at once, so a
// read of whether any process holds it, made once the stop has released it, can find the
// standby's: under six busy loops 1 of 400 runs of the landing row read it so and failed as a stop
// that kept its hold.
func holderHolds(t *testing.T, pool *store.Pool, holder *intakeProcess) bool {
	t.Helper()
	var named string
	if err := pool.QueryRow(t.Context(),
		"select coalesce((select holder::text from duty_holds where name = $1 and expires_at > now()), '')",
		intakeHoldName(bus.DispatchDeliveryDurable)).Scan(&named); err != nil {
		t.Fatalf("read the delivery durable's hold: %v", err)
	}
	return named == holder.intake.holder.String()
}

// holdRenewedBeforeItsExpiry reads the expiry the delivery durable's hold has now, then waits until
// a renewal moves it later or the database's clock passes it, and reports whether the renewal came
// first. A hold left to lapse is past its expiry within the hold's TTL; the wait past that, to the
// test binary's deadline less its margin (untilTheBinaryDeadline), only guards against a row that
// neither moves nor lapses.
func holdRenewedBeforeItsExpiry(t *testing.T, pool *store.Pool) bool {
	t.Helper()
	read := func() (expires, now time.Time) {
		t.Helper()
		if err := pool.QueryRow(t.Context(), "select expires_at, now() from duty_holds where name = $1",
			intakeHoldName(bus.DispatchDeliveryDurable)).Scan(&expires, &now); err != nil {
			t.Fatalf("read the delivery durable's hold: %v", err)
		}
		return expires, now
	}
	first, _ := read()
	wait := untilTheBinaryDeadline(t)
	guard := time.Now().Add(wait)
	for time.Now().Before(guard) {
		expires, now := read()
		switch {
		case expires.After(first):
			return true
		case !now.Before(first):
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the delivery durable's hold was neither renewed nor past its expiry %s after it was read", wait.Round(time.Second))
	return false
}

// requireHandBackBeforeCreation waits until the server has had both the holder's hand-back and the
// standby's creation of the durable, and fails the test when the hand-back came after, or when the
// standby's consumer delivered one of the row's messages (sequences) more than once.
func requireHandBackBeforeCreation(t *testing.T, recorded *durableEvents, from int, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	waitFor(t, untilTheBinaryDeadline(t), func() bool {
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

// requireDeliveredOnceAfterTakeOver is requireDeliveredOnceAfterTakeOverWithin, waiting to the
// test binary's deadline less its margin (untilTheBinaryDeadline).
func requireDeliveredOnceAfterTakeOver(t *testing.T, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	requireDeliveredOnceAfterTakeOverWithin(t, untilTheBinaryDeadline(t), binaryDeadlineBound, publisher, sequences)
}

// binaryDeadlineBound names the bound of a wait to the test binary's deadline less its margin in a
// failure (requireDeliveredOnceAfterTakeOver, requireNoHandBackAfterCreation).
const binaryDeadlineBound = "the test binary's deadline less rowDeadlineMargin (untilTheBinaryDeadline)"

// requireDeliveredOnceAfterTakeOverWithin waits, for at most within, until the consumer the standby
// created has delivered every message it had to and the standby has acknowledged them, and fails
// the test when it delivered one of them more than once. A wait that passes within fails the test
// and names bound. The consumer had to deliver each of the row's messages (sequences) from its
// start sequence on (OptStartSeq, the durable's ack floor plus one; takeOverConfig): the message
// the holder's stop handed back, and the one before it too when the stop gave up the holder's
// handler at its deadline and the handler's deferred answer handed that one back as well
// (Intake.deliver). Delivered.Consumer counts every delivery, so a hand-back that made the new
// consumer deliver a message again makes it one more than that count. The recorded order cannot
// show that hand-back: nats-server v2.15.1 makes a new consumer's ack subscription live (setLeader,
// consumer.go:1491) before it queues the consumer's create advisory (consumer.go:1504), and sends
// the advisory from the stream's own send queue, so the -NAK can be recorded before "created".
// NumRedelivered does not keep it either: the standby's acknowledgement clears it
// (consumer.go:3802). A -NAK that reaches the new consumer before its first delivery of the
// message, or after the standby acknowledged it, changes nothing, because the server ignores it
// (processNak, consumer.go:3255 and :3259), and the row does not fail for it.
func requireDeliveredOnceAfterTakeOverWithin(t *testing.T, within time.Duration, bound string, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	var info *natsgo.ConsumerInfo
	waitFor(t, within, func() bool {
		read, err := publisher.JS().ConsumerInfo(bus.Stream, bus.DispatchDeliveryDurable)
		if err != nil {
			return false
		}
		info = read
		return info.Delivered.Consumer > 0 && info.NumAckPending == 0 && info.NumPending == 0
	}, func() string {
		return fmt.Sprintf("the standby's consumer did not deliver the messages the holder left and have them acknowledged by its bound, %s: %s", bound, consumerState(publisher))
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

// requireNoHandBackAfterCreation is requireNoHandBackAfterCreationWithin, waiting to the test
// binary's deadline less its margin (untilTheBinaryDeadline).
func requireNoHandBackAfterCreation(t *testing.T, recorded *durableEvents, from int, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	requireNoHandBackAfterCreationWithin(t, untilTheBinaryDeadline(t), binaryDeadlineBound, recorded, from, publisher, sequences)
}

// requireNoHandBackAfterCreationWithin waits for the standby's creation of the durable and fails
// the test when a hand-back reached the server after it, or when the standby's consumer delivered
// one of the row's messages (sequences) more than once (requireDeliveredOnceAfterTakeOverWithin).
// Its waits end within within in all: the read of the consumer has what the wait for the creation
// left of it. A wait that passes within fails the test and names bound. A hand-back the holder's
// client dropped with its connection never reaches the server, which is allowed: the message
// stays with the durable.
func requireNoHandBackAfterCreationWithin(t *testing.T, within time.Duration, bound string, recorded *durableEvents, from int, publisher *bus.Client, sequences []uint64) {
	t.Helper()
	deadline := time.Now().Add(within)
	waitFor(t, within, func() bool { return slices.Contains(recorded.since(from), durableCreated) }, func() string {
		return fmt.Sprintf("the standby did not create the durable by its bound, %s: %q; %s", bound, recorded.since(from), consumerState(publisher))
	})
	if events := recorded.since(from); handBackAfterCreation(events) {
		t.Fatalf("a hand-back landed after the standby created the durable, so it reached the standby's consumer: %q", events)
	}
	requireDeliveredOnceAfterTakeOverWithin(t, time.Until(deadline), bound, publisher, sequences)
}

// finalizerOutcome is the outcome that the trace line of the end of delivery's hand-back finalizer
// gives (traceFinalizerEnds) in records: finalizerReleased, finalizerLapse or finalizerLost, or
// "none" while no finalizer of delivery has ended.
func finalizerOutcome(records []map[string]any, delivery *intakeDelivery) string {
	outcome := "none"
	for _, record := range records {
		if record["msg"] == traceFinalizerEnds && record["delivery"] == tracePointer(delivery) {
			outcome, _ = record["outcome"].(string)
		}
	}
	return outcome
}

// logFinalizerOutcome logs one line as the subtest ends, whether it passed or failed: the outcome
// of the holder's hand-back finalizer, read from the row's trace (finalizerOutcome), as
// "FINALIZER outcome=released", "FINALIZER outcome=lapse", "FINALIZER outcome=lost", or
// "FINALIZER outcome=none" when no finalizer had ended by then. A count of these lines over many
// runs gives how often each end was taken. Its cleanup runs before the row's own cleanups, so the
// stops and closes that they make do not change the outcome that it reads.
func logFinalizerOutcome(t *testing.T, row heldHandBack) {
	t.Helper()
	logs := captureLogs(t)
	t.Cleanup(func() { t.Logf("FINALIZER outcome=%s", finalizerOutcome(logs(), row.delivery)) })
}

// flushConfirmed reports whether records hold a flush that confirmed a hand-back of client: a
// trace line of a settle read (traceHandBacksSettled) that is settled by its flush, or of a flush
// that confirmed hand-backs as they were made (traceHandBacksConfirmed).
func flushConfirmed(records []map[string]any, client *bus.Client) bool {
	_, found := logtest.LineWhere(records, func(record map[string]any) bool {
		if record["client"] != tracePointer(client) {
			return false
		}
		return record["msg"] == traceHandBacksConfirmed ||
			record["msg"] == traceHandBacksSettled && record["settled"] == true && record["reason"] == settledByFlush
	})
	return found
}

// tracedAt is when the first record of message about client was logged, and false when records
// hold none.
func tracedAt(records []map[string]any, message string, client *bus.Client) (time.Time, bool) {
	record, found := logtest.LineWhere(records, func(record map[string]any) bool {
		return record["msg"] == message && record["client"] == tracePointer(client)
	})
	if !found {
		return time.Time{}, false
	}
	stamp, _ := record["time"].(string)
	at, err := time.Parse(time.RFC3339Nano, stamp)
	return at, err == nil
}

// readDeliveryHold reads the delivery durable's hold: the holder its row names and the row's
// expiry, on the database's clock, or "" and the zero time when there is no row.
func readDeliveryHold(ctx context.Context, pool *store.Pool) (named string, expires time.Time, err error) {
	if err := pool.QueryRow(ctx,
		"select coalesce(max(holder::text), ''), coalesce(max(expires_at), 'epoch') from duty_holds where name = $1",
		intakeHoldName(bus.DispatchDeliveryDurable)).Scan(&named, &expires); err != nil {
		return "", time.Time{}, fmt.Errorf("read the delivery durable's hold: %w", err)
	}
	if named == "" {
		return "", time.Time{}, nil
	}
	return named, expires, nil
}

// deliveryHold is readDeliveryHold, failing the test when the read fails.
func deliveryHold(t *testing.T, pool *store.Pool) (named string, expires time.Time) {
	t.Helper()
	named, expires, err := readDeliveryHold(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return named, expires
}

// holdTakeOver is the first take of the delivery durable's hold by a process other than the
// holder's that watchHoldTakeOver saw: takenAt is when that process took the hold, on the
// database's clock, and seenAt when the watch read it, on this process's clock. done closes once
// the watch has seen it, or has ended with err.
type holdTakeOver struct {
	done    chan struct{}
	takenAt time.Time
	seenAt  time.Time
	err     error
}

// watchHoldTakeOver reads the delivery durable's hold every 250 ms from now on, until a read finds
// its row naming a process other than holder's, or the test ends. The take wrote the row's expiry
// as its own now() plus intakeHoldTTL, and the new holder's first renewal moves it only
// intakeHoldRenew later, so the expiry that the first such read finds, less intakeHoldTTL, is when
// the take ran, exactly. seenAt is precise to the 250 ms between reads.
func watchHoldTakeOver(t *testing.T, pool *store.Pool, holder *intakeProcess) *holdTakeOver {
	t.Helper()
	watch := &holdTakeOver{done: make(chan struct{})}
	ctx := t.Context()
	go func() {
		defer close(watch.done)
		for {
			named, expires, err := readDeliveryHold(ctx, pool)
			if err != nil {
				watch.err = err
				return
			}
			if named != "" && named != holder.intake.holder.String() {
				watch.takenAt, watch.seenAt = expires.Add(-intakeHoldTTL), time.Now()
				return
			}
			select {
			case <-ctx.Done():
				watch.err = ctx.Err()
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	return watch
}

// carriesNAK is a match for testnats.HandshakeHold.HoldChunks of each chunk of a client's stream
// that carries a -NAK, or the rest of one whose start ended the chunk before it: the relay reads
// the stream in chunks of its own size, so a -NAK can straddle two. Holding the rest of a -NAK
// holds the -NAK, since the server handles no part of a message before all of it has arrived.
func carriesNAK() func(chunk []byte) bool {
	nak := []byte("-NAK")
	var mu sync.Mutex
	var tail []byte
	return func(chunk []byte) bool {
		mu.Lock()
		defer mu.Unlock()
		stream := append(tail, chunk...)
		tail = slices.Clone(stream[len(stream)-min(len(stream), len(nak)-1):])
		return bytes.Contains(stream, nak)
	}
}

// inFlightHold is how long the in-flight subtest's link keeps the holder's -NAK from the server,
// counted from just before the link's write thaws, unless the standby creates the durable first:
// the -NAK stands for a hand-back still on its way to the server when the holder's client closes.
// The link lets the -NAK go only once StartIntakes' stop has returned, so never before the close.
// The close comes intakeFinalizerConfirmWait (5 s) after the finalizer started, or once the
// hand-back's publish has ended when that is later, and a hold left to lapse then still has at
// least intakeHoldTTL less intakeHoldRenew (10 s) on its row. So the -NAK is let go before any
// standby can take the durable over: about 5 s before, when the publish ends inside the confirm
// window. A hold released at the close lets the standby create the durable at once, and the -NAK
// is let go after that creation.
const inFlightHold = 10 * time.Second

// inFlightSlack is what each bound of the in-flight subtest adds to the product's own bound of the
// event it waits for, for a runner loaded under the race detector. With these bounds, a failed run
// ends in seconds. In CI job 137127087412 of envoy-go-race, the subtest waited 26 minutes, to the
// test binary's deadline, for a -NAK that was never written, and every later test of the package
// then failed at that deadline.
const inFlightSlack = 30 * time.Second

// The bounds of the in-flight subtest's own waits, each counted from the start of its wait, or
// from the moment the link lets the -NAK go where its comment says so. The bound of StartIntakes'
// stop reads intakeFinalizerConfirmWait, a var, so the subtest computes it where it waits.
const (
	// inFlightRunReturnsWithin bounds the holder's Run. It returns at the stop's deadline
	// (stopBudget), which counts from the stop's start, before this wait, or after the stop's last
	// wait for a flush, which ends at most intakeFlushWait after that deadline (handBacksReached).
	inFlightRunReturnsWithin = stopBudget + intakeFlushWait + inFlightSlack
	// inFlightPublishEndsWithin bounds the end of the hand-back's publish once the link's write
	// thaws. The parked write (testnats.HandshakeHold.ParkAWrite), about 1 MiB, drains through
	// small buffers, and the publish then takes nats.go's lock. The product has no bound of its
	// own here. Under the race detector in CI, the drain took about 3 s.
	inFlightPublishEndsWithin = inFlightSlack
	// inFlightNAKLandsWithin bounds the arrival of the -NAK at the server once the link lets it go:
	// the link's relay writes the chunk that it kept.
	inFlightNAKLandsWithin = inFlightSlack
	// inFlightTakeOverWithin bounds the standby's take-over of the hold, from the moment the link
	// lets the -NAK go. That moment comes after StartIntakes' stop has returned, so after the
	// finalizer has left the hold to lapse or released it. A row left to lapse expires at most
	// intakeHoldTTL after its last renewal, which came before that, and the standby tries for the
	// hold every durableBindInterval.
	inFlightTakeOverWithin = intakeHoldTTL + durableBindInterval + inFlightSlack
	// inFlightSettledWithin bounds the reads of the standby's consumer
	// (requireNoHandBackAfterCreationWithin), from the moment the link lets the -NAK go: the
	// standby's take of the hold (inFlightTakeOverWithin), then its take-over of the durable, which
	// deletes and creates it and whose last part takeOverFinishWithin bounds, then the standby's
	// handling of the row's messages. The standby's fake GitHub answers each fetch at once, and the
	// standby acknowledges each message as its handler ends.
	inFlightSettledWithin = inFlightTakeOverWithin + takeOverFinishWithin + inFlightSlack
)

// closeLockWait is the longest the close of an intake's client waits for nats.go's connection
// lock: behind a reconnect attempt, whose dial and handshake nats.go bounds by bus.ConnectTimeout
// each, with the lock held through both (intakeFinalizerConfirmWait).
const closeLockWait = 2 * bus.ConnectTimeout

// frozenLinkSchedulingSlack is the slack the frozen-link shutdown subtest gives its bound on the
// standby's take-over for scheduling: the bound is closeLockWait + intakeHoldTTL +
// durableBindInterval from the close's start (intakeFinalizerConfirmWait), and a loaded runner can
// run each of the trace line, the release or abandon, the standby's poll and the watch's read of
// the take (holdTakeOver.seenAt, at most 250 ms late) a little after its moment.
const frozenLinkSchedulingSlack = time.Second

// withIntakeWriteDeadline is the client option production's intakes connect with
// (bus.WithWriteDeadline at IntakeWriteDeadline), for a row whose holder must write as they do
// (startIntakeProcess).
var withIntakeWriteDeadline = bus.WithWriteDeadline(IntakeWriteDeadline)

// clientReconnects is the reconnect count of client's current connection
// (natsgo.Statistics.Reconnects): its reconnect attempts whose dial connected. nats.go v1.55.0
// counts a reconnect as soon as its dial connects, before the handshake (doReconnect,
// nats.go:3577-3581), and a frozen proxy accepts the dial, so an attempt whose handshake a frozen
// link never answers counts too.
func clientReconnects(client *bus.Client) uint64 {
	if conn := client.Connection(); conn != nil {
		return conn.Stats().Reconnects
	}
	return 0
}

// logReconnects logs one line as the test ends, whether it passed or failed: the reconnect count
// of holder's client and of standby's, as "RECONNECTS holder=<n> standby=<n>" (clientReconnects),
// which counts reconnect attempts whose dial connected. A row with no standby passes nil, and its
// line says standby=0. A count of these lines over many runs gives how often a run's clients tried
// a reconnect whose dial connected. A row calls it before it freezes its link, so it reads the
// counts once the row's later cleanups have restored the link and no frozen write holds nats.go's
// lock, which the read takes. The frozen-link shutdown subtest logs holder=1: its holder's one
// dial through the frozen link before the close, not a reconnect that the restore lets through.
func logReconnects(t *testing.T, holder, standby *bus.Client) {
	t.Helper()
	t.Cleanup(func() {
		var standbyReconnects uint64
		if standby != nil {
			standbyReconnects = clientReconnects(standby)
		}
		t.Logf("RECONNECTS holder=%d standby=%d", clientReconnects(holder), standbyReconnects)
	})
}

// handBackRefused reports whether records hold the end of a hand-back publish of delivery that
// nats.go refused with one of refusals (traceHandBackPublished with that error), as it refuses one
// whose connection had closed (natsgo.ErrConnectionClosed) or one while the connection reconnects
// (natsgo.ErrReconnectBufExceeded), each of publishRefusals: nats.go refuses it before it queues a
// byte, so the publish is not counted (intakeDelivery.endPublish).
func handBackRefused(records []map[string]any, delivery *intakeDelivery, refusals ...error) bool {
	_, found := logtest.LineWhere(records, func(record map[string]any) bool {
		if record["msg"] != traceHandBackPublished || record["delivery"] != tracePointer(delivery) {
			return false
		}
		return slices.ContainsFunc(refusals, func(refusal error) bool { return record["error"] == refusal.Error() })
	})
	return found
}

// handBackPublishEnd reads the first end of a hand-back publish of delivery in records
// (traceHandBackPublished): ended reports whether records hold one, and failure is the error that
// the publish ended with, as the trace line gives it, or nil when it ended with no error.
func handBackPublishEnd(records []map[string]any, delivery *intakeDelivery) (failure any, ended bool) {
	record, ended := logtest.LineWhere(records, func(record map[string]any) bool {
		return record["msg"] == traceHandBackPublished && record["delivery"] == tracePointer(delivery)
	})
	return record["error"], ended
}

// A hand-back the holder made during its stop never reaches the durable a standby recreates.
//
// A hand-back is a publish at the message's reply subject, which names the durable rather than the
// consumer: one that reaches the server after the standby's take-over has deleted the durable and
// created it again under the same name moves that message's pending entry on the standby's consumer
// and has the standby delivered it a second time. The stop waits for each publish at most
// bus.ConnLockWait (intakeDelivery.publishHandBack) and goes on without one NATS has not taken; so
// the stop releases the hold only once every hand-back taken has settled and a flush on the socket
// it went out on, begun after its publish ended, has answered; one still pending at the stop's
// deadline keeps the hold renewing until a flush confirms it (heldDurable.finalizeHandBacks). A
// flush on another socket confirms nothing, so a finalizer whose client closes first leaves the
// hold to lapse at its expiry.
//
// Every subtest holds a write on the holder's link while a hand-back waits before its publish: the
// stop's own in the first four and the sixth, the callback's in the fifth (stopWithAHeldHandBack).
// Each reads, in the order the server sent them, the answers published to the durable and the
// durable's creations, on a connection of its own, and every step waits for an event. Once the
// standby has acknowledged the messages the holder left, each also reads the consumer the standby
// created and requires that it delivered each of them once: a hand-back can make that consumer
// deliver a message again before the server sends its creation's advisory, where the order alone
// passes it (requireDeliveredOnceAfterTakeOver).
//   - a hand-back that lands before the stop's deadline (landingBudget): the stop goes on without
//     the publish (its log line). Then, before the link moves, the stop is seen waiting for its
//     hand-back with its hold live (handBacksWaiting). Once the link moves, the hold is released by
//     the time the stop returns, and the hand-back reached the server before the standby created the
//     durable. The subtest reads its own premise first: a stop whose deadline passed before its
//     hand-back landed logs intakeHoldFinalizing, and the subtest then fails as a premise not met,
//     naming the budget, rather than as a stop that kept its hold;
//   - a hand-back still pending at the stop's deadline: the link stays held, and the stop returns
//     while it is, with the hand-back still pending (handBacksLanded). The hold still names the
//     holder then, and the standby has not created the durable. With the link still held, the hold is
//     renewed before the database's clock passes the expiry it had as the stop returned, so it does
//     not lapse, and the standby still has not created the durable. Once the link moves, the
//     hand-back reaches the server before the standby creates the durable;
//   - a pending hand-back at shutdown whose link thaws before the close: the Run returns at its
//     deadline, then StartIntakes' stop waits for its finalizer with the client open, in a confirm
//     window the subtest widens to 30 s. Once the link moves, the hand-back reaches the server and
//     a flush confirms it, and the finalizer releases the hold before StartIntakes' stop closes the
//     client and returns, so its caller can close the pool without leaving the hold to lapse. The
//     hand-back does not reach the server after the standby creates the durable;
//   - a pending hand-back at shutdown whose link stays frozen past the close: the same stop with
//     the production window and the production write deadline, and the link never moves. The
//     finalizer leaves the hold to lapse, or releases it when nats.go refused the held -NAK before
//     it queued a byte, as closed when the close took nats.go's lock first, or with no reconnect
//     buffer once the write deadline had ended the frozen write, and the standby takes the durable
//     over within closeLockWait + intakeHoldTTL + durableBindInterval of the close's start (its own
//     comment);
//   - a hand-back the callback takes during the drain (landingBudget): the callback of the second
//     message runs once the stop has handed back what it held, and hands the message back itself.
//     Its publish is held until the link moves. The stop's drain loop gives up while the write is
//     held, and the stop is then seen waiting for that hand-back with its hold live. Once the link
//     moves, the subtest reads what the first one reads;
//   - a hand-back still in flight when the client closes: as at shutdown, but the link keeps the
//     holder's -NAK from the server, and the subtest forces the order it tests. It thaws the
//     link's write and waits until the hand-back's publish has ended with no error, and only then
//     begins StartIntakes' stop, so the -NAK is on its way when that stop closes the client. In CI
//     job 137127087412 of envoy-go-race, the subtest began that stop first, and the close took the
//     client before the publish wrote the -NAK. No flush confirms the -NAK before the close. The
//     finalizer leaves the hold to lapse, the standby takes the hold only after its row's expiry,
//     and the -NAK lands before the standby creates the durable. Each wait after
//     stopWithAHeldHandBack has a bound, so a failure ends in seconds (its own comment).
//
// The two shutdown subtests and the in-flight subtest each log the outcome of the holder's
// finalizer, as "FINALIZER outcome=...", and every subtest logs its clients' reconnect counts, as
// "RECONNECTS holder=<n> standby=<n>", whether they pass or fail (logFinalizerOutcome,
// logReconnects). No step waits on a time but the stop's own deadline (intakeStopBudget), the
// finalizer's confirm window, the hold's own expiry, the write deadline in the frozen-link subtest
// and, in the in-flight subtest, the link's hold of the -NAK (inFlightHold). Every other wait runs
// to the test binary's deadline less its margin (untilTheBinaryDeadline) and only guards against an
// event that never comes, but for the in-flight subtest's waits after stopWithAHeldHandBack
// returns: each of them ends at a bound worked out from the product's bounds, with inFlightSlack,
// and its reads of the standby's consumer use the bounded form of the shared helper
// (requireNoHandBackAfterCreationWithin). stopWithAHeldHandBack begins the stop only once the
// second message is in the holder's hands, queued behind the first or held by its callback, and
// decides whether the hand-back reached its publish by which comes first, that publish or the
// holder's Run returning.
// The negative controls, each with the subtests that it fails:
//   - a stop that withholds its hand-backs fails all six in stopWithAHeldHandBack. In the stop's
//     own rows the holder's Run returns with no hand-back having reached its publish. In the fifth
//     the callback withholds its hand-back too, which never reaches its publish;
//   - a release that does not wait for the hand-backs fails the first three and the fifth. The
//     first sees the hold released before the link moves. The second sees the hold released when
//     the stop returns with the hand-back pending. The third sees no finalizer confirm the
//     hand-back once the link moves. The fifth sees the hold released while the callback's publish
//     is held;
//   - a stop that never releases its hold when a publish was left running fails the first: the
//     hold is still live after the stop returns;
//   - a release made at the deadline with a hand-back pending fails the second: the hold is gone
//     when the stop returns;
//   - a stop that leaves its hold to lapse with a hand-back pending fails the second: the hold
//     reaches its expiry unrenewed;
//   - an untracked finalizer fails the third: its shutdown stop returns with the hold live;
//   - the holder's -NAK published again while the standby holds the message, with the server
//     holding the creation's advisory until the new consumer has delivered it a second time, fails
//     the first three by that second delivery. The count does not fail a stop that gives up the
//     holder's handler at its deadline, whose deferred answer hands the first message back too: the
//     new consumer then starts at that message and delivers both once;
//   - a hand-back counted only once its publish begins, in publishHandBack, fails the fifth: the
//     stop finds no hand-back counted when its drain loop gives up, and releases its hold while the
//     callback's publish is held;
//   - a closed connection that settles a hand-back fails the fourth at its outcome check: the
//     finalizer releases the hold at the close. With a close that does not wait for the finalizer
//     and no hold left to lapse, it fails the sixth: the held -NAK lands after the standby creates
//     the durable (its own comment).
func TestAHandBackLeftRunningLandsBeforeTheStandbyTakesOver(t *testing.T) {
	t.Run("a hand-back that lands before the stop's deadline", func(t *testing.T) {
		logs := captureLogs(t)
		// The stop runs the hook on its own goroutine, so the hook only closes waiting. The hook is
		// stored before stopWithAHeldHandBack starts the stop, so the stop finds it in handBacksReached.
		waiting := make(chan struct{})
		var signal sync.Once
		hook := func() { signal.Do(func() { close(waiting) }) }
		handBacksWaiting.Store(&hook)
		t.Cleanup(func() { handBacksWaiting.Store(nil) })
		row := stopWithAHeldHandBack(t, landingBudget, stopsHandBack)
		waitFor(t, untilTheBinaryDeadline(t), func() bool { return loggedMessage(logs(), callLeftRunning) }, func() string {
			return "control: the stop never went on without the held hand-back's publish: " + consumerState(row.publisher)
		})
		waitFor(t, untilTheBinaryDeadline(t), func() bool {
			select {
			case <-waiting:
				return true
			default:
				return !holderHolds(t, row.pool, row.holder)
			}
		}, func() string {
			return "the stop neither started to wait for its hand-back nor released its hold once it went on without the publish: " + consumerState(row.publisher)
		})
		if !holderHolds(t, row.pool, row.holder) {
			t.Fatalf("the stop released its hold before its hand-back reached the server, so a standby could recreate the durable first; the holder logged %q", loggedMessages(logs()))
		}
		row.thaw()
		select {
		case <-row.stopped:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("the holder's stop did not return once the link moved")
		}
		// The premise: the hand-back landed before the stop's deadline. A stop whose deadline passed
		// first has said so (intakeHoldFinalizing) and left its hold to the finalizer, and such a run
		// reads nothing of the stop's release, whatever the hold reads next.
		if loggedMessage(logs(), intakeHoldFinalizing) {
			t.Fatalf("control: the premise did not hold: the stop's deadline (landingBudget, %s) passed before its hand-back landed, so this run reads nothing of a hand-back that lands before it; the holder logged %q", landingBudget, loggedMessages(logs()))
		}
		if holderHolds(t, row.pool, row.holder) {
			t.Fatalf("the stop kept its hold although its hand-back reached the server before its deadline, which costs the standby the lapse; the holder logged %q", loggedMessages(logs()))
		}
		requireHandBackBeforeCreation(t, row.recorded, row.from, row.publisher, row.sequences)
	})

	t.Run("a hand-back still pending at the stop's deadline", func(t *testing.T) {
		row := stopWithAHeldHandBack(t, stopBudget, stopsHandBack)
		select {
		case <-row.stopped:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("the holder's stop did not return while its hand-back was held: it waits for the hand-back")
		}
		testnats.RequireLockHeld(t, row.conn)
		if !row.handBackPending() {
			t.Fatal("control: the stop's hand-back reached NATS while the write was held")
		}
		if !holderHolds(t, row.pool, row.holder) {
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

	// The Run returns at its deadline with the hand-back's publish held, and the subtest then runs
	// the stop of the durables that StartIntakes' stop runs (stopDurables), which waits for the
	// finalizer before it closes the holder's client, and thaws the link's write while the client is
	// still open. The hand-back then lands, a flush of the open connection confirms it, and the
	// finalizer releases the hold. The release waits in finalizerBeforeRelease, so the subtest sees
	// that StartIntakes' stop does not return before it.
	//
	// The finalizer's confirm window (intakeFinalizerConfirmWait) only bounds a stop that never
	// confirms, which this subtest does not test: its own steps before the thaw took more than the
	// production 5 s under the race detector in CI, and the client then closed first. So the
	// subtest widens the window to a 30 s guard before its stop starts, and puts it back in a
	// cleanup registered before the row's, which runs after the row's teardown, so no finalizer
	// reads it afterwards. The thaw comes once StartIntakes' stop has begun, inside that window.
	t.Run("a pending hand-back at shutdown whose link thaws before the close", func(t *testing.T) {
		window := intakeFinalizerConfirmWait.Swap(int64(30 * time.Second))
		t.Cleanup(func() { intakeFinalizerConfirmWait.Store(window) })
		row := stopWithAHeldHandBack(t, stopBudget, stopsHandBack)
		logFinalizerOutcome(t, row)
		select {
		case <-row.stopped:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("the holder's Run did not return while its hand-back was held")
		}
		if !row.handBackPending() {
			t.Fatal("control: the hand-back reached NATS before its client closed")
		}
		finalizing := make(chan struct{})
		releaseFinalizer := make(chan struct{})
		hook := func() {
			close(finalizing)
			<-releaseFinalizer
		}
		finalizerBeforeRelease.Store(&hook)
		t.Cleanup(func() { finalizerBeforeRelease.Store(nil) })
		closed := make(chan struct{})
		go func() {
			stopDurables(&row.holder.intake.heldDurable)
			close(closed)
		}()
		testnats.RequireLockHeld(t, row.conn)
		row.thaw()
		select {
		case <-finalizing:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("no flush confirmed the pending hand-back once the link moved, so its finalizer did not release the hold")
		}
		select {
		case <-closed:
			t.Fatal("StartIntakes' stop returned while its finalizer was held before release")
		default:
		}
		if !holderHolds(t, row.pool, row.holder) {
			t.Fatal("the hold was released before the finalizer's release step")
		}
		close(releaseFinalizer)
		select {
		case <-closed:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("StartIntakes' stop did not return after its finalizer was released")
		}
		if holderHolds(t, row.pool, row.holder) {
			t.Fatal("the pending hand-back's finalizer released but left its hold live: shutdown could close the pool before releasing it")
		}
		requireNoHandBackAfterCreation(t, row.recorded, row.from, row.publisher, row.sequences)
		if outcome := finalizerOutcome(captureLogs(t)(), row.delivery); outcome != finalizerReleased {
			t.Fatalf("the finalizer's outcome is %q, want %q", outcome, finalizerReleased)
		}
	})

	// The same stop with the production confirm window (5 s) and the production write deadline
	// (withIntakeWriteDeadline), with the link frozen to the end. The write deadline ends the frozen
	// write (IntakeWriteDeadline); nats.go closes that socket and tries to reconnect through the
	// frozen link, which never answers. StartIntakes' stop then closes the holder's client, and its
	// close waits for nats.go's lock: up to the write deadline behind the frozen write, or up to
	// closeLockWait behind a reconnect attempt, whose dial and handshake nats.go bounds by
	// bus.ConnectTimeout each.
	//
	// Which end the finalizer takes depends on an order the subtest does not force, so it reads which
	// one happened and requires that case's own outcome:
	//   - nats.go queued the held -NAK before the close, so its publish ended and was counted: no
	//     flush on the open connection can confirm it, and the finalizer leaves the hold to lapse
	//     (finalizerLapse), with the hold's row still naming the holder once StartIntakes' stop has
	//     returned, and the standby's take of the hold after that row's expiry;
	//   - nats.go refused the -NAK before it queued a byte (handBackRefused, publishRefusals): as
	//     closed when the close took the lock first, or with no reconnect buffer once the write
	//     deadline had ended the frozen write and nats.go was reconnecting. Nothing published is
	//     unconfirmed, and the finalizer releases the hold (finalizerReleased). The message stays
	//     with the durable for the standby.
	//
	// Either way it requires the standby's take-over within closeLockWait + intakeHoldTTL +
	// durableBindInterval of the close's start (intakeFinalizerConfirmWait), with
	// frozenLinkSchedulingSlack, measured from the trace line of the close's start to the watch's
	// read of the take (holdTakeOver.seenAt), and names the part that overran: the close's start to
	// the hold's end against closeLockWait, or the hold's end to the take-over against intakeHoldTTL
	// + durableBindInterval. It also requires no hand-back after the standby's creation of the
	// durable, and each of the row's messages delivered once by the standby's consumer
	// (requireNoHandBackAfterCreation).
	//
	// Control: with a closed connection taken to settle the hand-back (the product before
	// 7ce70c15), the finalizer releases the hold at the close in the first case too, and the subtest
	// fails at the outcome check. With nats.go's default write deadline (no withIntakeWriteDeadline),
	// the close waits up to a minute for the frozen write, and the subtest fails at the bound.
	t.Run("a pending hand-back at shutdown whose link stays frozen past the close", func(t *testing.T) {
		row := stopWithAHeldHandBack(t, stopBudget, stopsHandBack, withIntakeWriteDeadline)
		logFinalizerOutcome(t, row)
		logs := captureLogs(t)
		select {
		case <-row.stopped:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("the holder's Run did not return while its hand-back was held")
		}
		if !row.handBackPending() {
			t.Fatal("control: the hand-back reached NATS before its client closed")
		}
		takeOver := watchHoldTakeOver(t, row.pool, row.holder)
		closed := make(chan struct{})
		go func() {
			stopDurables(&row.holder.intake.heldDurable)
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("StartIntakes' stop did not return with the link frozen: " + consumerState(row.publisher))
		}
		records := logs()
		heldBy, expiry := deliveryHold(t, row.pool)
		closeStart, closing := tracedAt(records, traceClientCloses, row.holder.client)
		holdEnd, ended := tracedAt(records, traceFinalizerEnds, row.holder.client)
		if !closing || !ended {
			t.Fatalf("control: the trace holds no start of the holder's close (%t) or no end of its finalizer (%t)", closing, ended)
		}
		refused := handBackRefused(records, row.delivery, publishRefusals...)
		outcome := finalizerOutcome(records, row.delivery)
		switch {
		case refused && outcome != finalizerReleased:
			t.Fatalf("the finalizer's outcome is %q, want %q: nats.go refused the held -NAK before it queued a byte (as closed, or with no reconnect buffer), so nothing published was left unconfirmed", outcome, finalizerReleased)
		case refused && heldBy == row.holder.intake.holder.String():
			t.Fatalf("once StartIntakes' stop had returned, the hold's row still named the holder %s, whose finalizer released it", heldBy)
		case !refused && outcome != finalizerLapse:
			t.Fatalf("the finalizer's outcome is %q, want %q: the held -NAK's publish was counted, and no flush on the open connection confirmed it", outcome, finalizerLapse)
		case !refused && heldBy != row.holder.intake.holder.String():
			t.Fatalf("once StartIntakes' stop had returned, the hold's row named %q, want the holder %s, whose finalizer left it to lapse", heldBy, row.holder.intake.holder)
		}
		select {
		case <-takeOver.done:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("the standby did not take the delivery durable's hold after the close: " + consumerState(row.publisher))
		}
		if takeOver.err != nil {
			t.Fatalf("watch the standby's take-over of the hold: %v", takeOver.err)
		}
		if !refused && takeOver.takenAt.Before(expiry) {
			t.Fatalf("the standby took the hold at %s on the database's clock, %s before the expiry %s of the row the holder left to lapse",
				takeOver.takenAt, expiry.Sub(takeOver.takenAt), expiry)
		}
		closeToEnd, endToTake := holdEnd.Sub(closeStart), takeOver.seenAt.Sub(holdEnd)
		if total := takeOver.seenAt.Sub(closeStart); total > closeLockWait+intakeHoldTTL+durableBindInterval+frozenLinkSchedulingSlack {
			if closeToEnd > closeLockWait {
				t.Fatalf("the standby took the hold %s after the close began: the close's start to the hold's end took %s, past the close's longest wait for nats.go's lock (closeLockWait, %s)",
					total, closeToEnd, closeLockWait)
			}
			t.Fatalf("the standby took the hold %s after the close began: the hold's end to the standby's take-over took %s, past intakeHoldTTL + durableBindInterval (%s)",
				total, endToTake, intakeHoldTTL+durableBindInterval)
		}
		requireNoHandBackAfterCreation(t, row.recorded, row.from, row.publisher, row.sequences)
		t.Logf("TAKEOVER the hold ended (%s) %s after the close began, and the standby took it %s after that, %s in all",
			outcome, closeToEnd.Round(time.Millisecond), endToTake.Round(time.Millisecond), takeOver.seenAt.Sub(closeStart).Round(time.Millisecond))
	})

	// The callback of the second message runs once the stop has handed back what it held, as
	// nats.go runs a message the client had buffered during the drain. It hands the message back
	// itself (receive, giveBack, handBack), and its publish waits in beforeHandBackPublish until the
	// row thaws the link. The stop's drain loop begins only once that hand-back has reached
	// beforeHandBackPublish, where the row puts the production wait back (stopWithAHeldHandBack). So
	// the row's own wait for the stop's calls, to the test binary's deadline, is gone before the loop
	// starts, and each of the loop's connected reads waits at most bus.ConnLockWait. With the write
	// held, the drain loop gives up, since its connected read does not answer, and the stop then
	// decides whether to wait for that hand-back.
	// The subtest requires that the stop waits for it (handBacksWaiting) with its hold live, and then
	// reads what the landing subtest reads.
	//
	// Control: with the hand-back counted only once its publish begins, in publishHandBack (the
	// code before the take), the subtest fails at that check. The stop finds no hand-back counted
	// when its drain loop gives up, and releases its hold while the callback's publish is held.
	t.Run("a hand-back the callback takes during the drain", func(t *testing.T) {
		logs := captureLogs(t)
		// The stop runs the hook on its own goroutine, so the hook only closes waiting. The hook is
		// stored before stopWithAHeldHandBack starts the stop, so the stop finds it in handBacksReached.
		waiting := make(chan struct{})
		var signal sync.Once
		hook := func() { signal.Do(func() { close(waiting) }) }
		handBacksWaiting.Store(&hook)
		t.Cleanup(func() { handBacksWaiting.Store(nil) })
		row := stopWithAHeldHandBack(t, landingBudget, callbacksHandBack)
		waitFor(t, untilTheBinaryDeadline(t), func() bool {
			select {
			case <-waiting:
				return true
			default:
				return !holderHolds(t, row.pool, row.holder)
			}
		}, func() string {
			return "the stop neither started to wait for the callback's hand-back nor released its hold once the write was held: " + consumerState(row.publisher)
		})
		if !holderHolds(t, row.pool, row.holder) {
			t.Fatalf("the stop released its hold while a hand-back the subscription's callback took during the drain had not reached the server; the holder logged %q", loggedMessages(logs()))
		}
		row.thaw()
		select {
		case <-row.stopped:
		case <-time.After(untilTheBinaryDeadline(t)):
			t.Fatal("the holder's stop did not return once the link moved")
		}
		// The premise, as in the landing subtest: the hand-back landed before the stop's deadline.
		if loggedMessage(logs(), intakeHoldFinalizing) {
			t.Fatalf("control: the premise did not hold: the stop's deadline (landingBudget, %s) passed before the callback's hand-back landed, so this run reads nothing of a hand-back that lands before it; the holder logged %q", landingBudget, loggedMessages(logs()))
		}
		if holderHolds(t, row.pool, row.holder) {
			t.Fatalf("the stop kept its hold although the callback's hand-back reached the server before its deadline, which costs the standby the lapse; the holder logged %q", loggedMessages(logs()))
		}
		requireHandBackBeforeCreation(t, row.recorded, row.from, row.publisher, row.sequences)
	})

	// A hand-back still on its way to the server when the holder's client closes. As in the
	// shutdown subtest, the Run returns at its deadline with the hand-back's publish held. Here the
	// link also keeps each chunk of the holder's stream that carries a -NAK from the server
	// (testnats.HandshakeHold.HoldChunks, carriesNAK), on the subtest's own bound: until the standby
	// has created the durable, or for inFlightHold, whichever comes first. The subtest then thaws the
	// link's write, waits until the hand-back's publish has ended (traceHandBackPublished,
	// handBackPublishEnd), and only then runs the durables' stop (stopDurables). So the -NAK is on
	// its way when the client closes, as one in the kernel's send buffer or on the network is. Every
	// chunk after it waits behind it, so no flush of the holder's connection can answer, and
	// StartIntakes' stop closes the client at intakeFinalizerConfirmWait from the finalizer's start,
	// or at once when the publish ended after that.
	//
	// The subtest forces this order, because the order is what it tests. In CI job 137127087412 of
	// envoy-go-race, the subtest began stopDurables first and thawed the write after. Under the race
	// detector, the parked write drained for seconds after the thaw, and the close took nats.go's
	// lock between the two takes of the hand-back's publish: its read of the connection's reconnect
	// count and the publish itself (publishHandBack). nats.go then refused the publish as closed,
	// before it queued a byte, so no -NAK was on its way, and the finalizer released the hold, as it
	// must when nothing published is unconfirmed. The subtest then waited to the test binary's
	// deadline for a -NAK that was never written. A publish that ends with an error fails as a
	// control: no byte of its -NAK is on its way, and the run reads nothing of a hand-back on its way
	// at the close.
	//
	// The subtest requires:
	//   - that the finalizer then leaves the hold to lapse (finalizerLapse), with the hold's row still
	//     naming the holder once StartIntakes' stop has returned;
	//   - that the standby takes the hold only after that row's expiry;
	//   - that the -NAK reaches the server once the link lets it go, and not after the standby
	//     created the durable, and that the standby's consumer delivers each of the row's messages
	//     once, with none lost (requireNoHandBackAfterCreationWithin).
	//
	// A run in which a flush confirmed the hand-back fails as a control: the link did not keep its
	// -NAK, and the run reads nothing of a hand-back still on its way at the close.
	//
	// Each wait after stopWithAHeldHandBack ends at a bound: the product's bound of the event, with
	// inFlightSlack for a loaded runner (inFlightRunReturnsWithin, inFlightPublishEndsWithin, the
	// bound of StartIntakes' stop, inFlightNAKLandsWithin, inFlightTakeOverWithin, and
	// inFlightSettledWithin for the reads of the standby's consumer). A failure names the bound that
	// it passed and ends in seconds, so it does not use up the test binary's deadline and fail every
	// later test of the package.
	//
	// The lapse costs the standby time. The abandoned row expires at most intakeHoldTTL (15 s) after
	// the abandon, since its last renewal came before it, and at least intakeHoldTTL less
	// intakeHoldRenew (10 s) after it. The standby tries for the hold every durableBindInterval
	// (2 s). So the standby takes the durable over at most intakeHoldTTL + durableBindInterval =
	// 17 s after the abandon, which comes at once after the close, or within one flush retry
	// (intakeFlushWait, 1 s) of it, where a release lets it take over at once. The subtest logs the
	// time from the close to the take-over.
	//
	// Control: with the product code before the close waited and the lapse (a closed connection
	// counted as settling the hand-back, the client closed at once, and the hold released), the
	// subtest fails at the hand-back-after-creation check: the finalizer releases the hold once the
	// client has closed, the standby creates the durable, and the held -NAK then lands on the
	// standby's consumer.
	t.Run("a hand-back still in flight when the client closes", func(t *testing.T) {
		row := stopWithAHeldHandBack(t, stopBudget, stopsHandBack)
		logFinalizerOutcome(t, row)
		logs := captureLogs(t)
		select {
		case <-row.stopped:
		case <-time.After(inFlightRunReturnsWithin):
			t.Fatalf("the holder's Run did not return within inFlightRunReturnsWithin (%s) while its hand-back was held", inFlightRunReturnsWithin)
		}
		if !row.handBackPending() {
			t.Fatal("control: the hand-back reached NATS before its client closed")
		}
		letGo := make(chan struct{})
		row.link.HoldChunks(t, carriesNAK(), letGo)
		heldUntil := time.Now().Add(inFlightHold)
		takeOver := watchHoldTakeOver(t, row.pool, row.holder)
		testnats.RequireLockHeld(t, row.conn)
		// The write thaws first. StartIntakes' stop begins only once the hand-back's publish has
		// ended with no error, so the -NAK is written, and kept by the link, before the close begins.
		row.thaw()
		waitFor(t, inFlightPublishEndsWithin, func() bool {
			_, ended := handBackPublishEnd(logs(), row.delivery)
			return ended
		}, func() string {
			return fmt.Sprintf("control: the hand-back's publish did not end within inFlightPublishEndsWithin (%s) of the thaw of the link's write: %s", inFlightPublishEndsWithin, consumerState(row.publisher))
		})
		if failure, _ := handBackPublishEnd(logs(), row.delivery); failure != nil {
			t.Fatalf("control: the hand-back's publish ended with the error %v before StartIntakes' stop began, so no byte of its -NAK is on its way, and this run reads nothing of a hand-back still on its way at the close", failure)
		}
		closed := make(chan struct{})
		go func() {
			stopDurables(&row.holder.intake.heldDurable)
			close(closed)
		}()
		// StartIntakes' stop returns within what is left of the finalizer's confirm window
		// (intakeFinalizerConfirmWait, which began at the Run's deadline), the close's longest wait
		// for nats.go's lock (closeLockWait), the end of the finalizer's flush in flight at the close
		// (intakeFlushWait) and a release, a statement bounded by intakeHoldRenew, with
		// inFlightSlack.
		closeWithin := time.Duration(intakeFinalizerConfirmWait.Load()) + closeLockWait + intakeFlushWait + intakeHoldRenew + inFlightSlack
		select {
		case <-closed:
		case <-time.After(closeWithin):
			t.Fatalf("StartIntakes' stop did not return within %s of its start (intakeFinalizerConfirmWait + closeLockWait + intakeFlushWait + intakeHoldRenew + inFlightSlack): %s", closeWithin, consumerState(row.publisher))
		}
		records := logs()
		abandonedBy, expiry := deliveryHold(t, row.pool)
		if flushConfirmed(records, row.holder.client) {
			t.Fatal("control: a flush confirmed the holder's hand-back with the connection open, so the link did not keep its -NAK from the server, and this run reads nothing of a hand-back still on its way at the close")
		}
		testwait.Holds(time.Until(heldUntil), 20*time.Millisecond, func() bool {
			return slices.Contains(row.recorded.since(row.from), durableCreated)
		})
		close(letGo)
		letGoAt := time.Now()
		waitFor(t, inFlightNAKLandsWithin, func() bool { return slices.Contains(row.recorded.since(row.from), "-NAK") }, func() string {
			return fmt.Sprintf("control: the holder's -NAK did not reach the server within inFlightNAKLandsWithin (%s) of the link letting it go: %q", inFlightNAKLandsWithin, row.recorded.since(row.from))
		})
		select {
		case <-takeOver.done:
		case <-time.After(time.Until(letGoAt.Add(inFlightTakeOverWithin))):
			t.Fatalf("the standby did not take the delivery durable's hold within inFlightTakeOverWithin (%s) of the link letting the -NAK go: %s", inFlightTakeOverWithin, consumerState(row.publisher))
		}
		if takeOver.err != nil {
			t.Fatalf("watch the standby's take-over of the hold: %v", takeOver.err)
		}
		requireNoHandBackAfterCreationWithin(t, time.Until(letGoAt.Add(inFlightSettledWithin)),
			fmt.Sprintf("inFlightSettledWithin (%s) from the link letting the -NAK go", inFlightSettledWithin),
			row.recorded, row.from, row.publisher, row.sequences)
		if outcome := finalizerOutcome(records, row.delivery); outcome != finalizerLapse {
			t.Fatalf("the finalizer's outcome is %q, want %q: the holder's client closed with its -NAK still kept from the server", outcome, finalizerLapse)
		}
		if abandonedBy != row.holder.intake.holder.String() {
			t.Fatalf("once StartIntakes' stop had returned, the hold's row named %q, want the holder %s: the hold was given up before its expiry", abandonedBy, row.holder.intake.holder)
		}
		if takeOver.takenAt.Before(expiry) {
			t.Fatalf("the standby took the hold at %s on the database's clock, %s before the expiry %s of the row the holder left to lapse",
				takeOver.takenAt, expiry.Sub(takeOver.takenAt), expiry)
		}
		closedAt, ok := tracedAt(records, traceClientClosed, row.holder.client)
		if !ok {
			t.Fatal("control: the trace holds no close of the holder's client")
		}
		var createdAt time.Time
		for i, event := range row.recorded.timed() {
			if i >= row.from && event.event == durableCreated {
				createdAt = event.at
				break
			}
		}
		t.Logf("TAKEOVER the standby took the hold %s after the holder's client closed and %s after the expiry of the row the holder left to lapse, and created the durable %s after the close",
			takeOver.seenAt.Sub(closedAt).Round(time.Millisecond), takeOver.takenAt.Sub(expiry).Round(time.Millisecond), createdAt.Sub(closedAt).Round(time.Millisecond))
	})
}

// slowFlushHold is how long the slow-flush row's link keeps the holder's -NAK from the server,
// counted from the end of the hand-back's publish: more than intakeFlushWait, so the stop's first
// flush, which begins as the publish ends, cannot answer, and far less than landingBudget, so a
// later flush can answer long before the stop's deadline.
const slowFlushHold = 2500 * time.Millisecond

// A stop whose first flush does not answer within intakeFlushWait flushes again, and releases its
// hold once a flush answers before its deadline. It does not leave the hold to a finalizer while
// most of its deadline is left.
//
// The row stops a holder whose stop hands a message back, with the hand-back's publish held behind
// a write on the holder's link (stopWithAHeldHandBack, stopsHandBack, landingBudget). The stop goes
// on without the publish (its log line), and the row sees it wait for its hand-back with its hold
// live (handBacksWaiting). The link then keeps each chunk with the holder's -NAK from the server
// (HoldChunks, carriesNAK), and the row thaws the write. So the publish ends, but no flush of the
// holder's connection can answer: its PING waits behind the -NAK. The link lets the -NAK go
// slowFlushHold after the publish ended. The row requires:
//   - as a control, that the stop's first flush did not answer: the stop's first settle read
//     (traceHandBacksSettled) is a flush that failed. A run whose first flush answered fails as a
//     control, since it reads nothing of a slow first flush;
//   - that the stop logs no intakeHoldFinalizing line before its deadline, and starts no finalizer
//     (traceFinalizerStarts). A stop that logs that line only after its deadline fails as a premise
//     not met, naming the budget;
//   - that the hold is released by the time the stop returns;
//   - that the hand-back reached the server before the standby created the durable.
//
// Control: with e10b6866's stop_finalize.go, whose stop makes one flush and answers
// handBacksUnconfirmed when that flush does not answer within intakeFlushWait, the row fails at the
// intakeHoldFinalizing check: the stop logs that line about 1 s after the publish ended, with most
// of its deadline left, and leaves its hold to a finalizer.
func TestAStopWhoseFirstFlushIsSlowReleasesItsHoldBeforeItsDeadline(t *testing.T) {
	logs := captureLogs(t)
	// The stop runs the hook on its own goroutine, so the hook only closes waiting. The hook is
	// stored before stopWithAHeldHandBack starts the stop, so the stop finds it in handBacksReached.
	waiting := make(chan struct{})
	var signal sync.Once
	hook := func() { signal.Do(func() { close(waiting) }) }
	handBacksWaiting.Store(&hook)
	t.Cleanup(func() { handBacksWaiting.Store(nil) })
	// The stop begins inside stopWithAHeldHandBack, so its deadline comes later than landingBudget
	// after this moment.
	began := time.Now()
	row := stopWithAHeldHandBack(t, landingBudget, stopsHandBack)
	waitFor(t, untilTheBinaryDeadline(t), func() bool { return loggedMessage(logs(), callLeftRunning) }, func() string {
		return "control: the stop never went on without the held hand-back's publish: " + consumerState(row.publisher)
	})
	waitFor(t, untilTheBinaryDeadline(t), func() bool {
		select {
		case <-waiting:
			return true
		default:
			return !holderHolds(t, row.pool, row.holder)
		}
	}, func() string {
		return "the stop neither started to wait for its hand-back nor released its hold once it went on without the publish: " + consumerState(row.publisher)
	})
	if !holderHolds(t, row.pool, row.holder) {
		t.Fatalf("the stop released its hold before its hand-back reached the server, so a standby could recreate the durable first; the holder logged %q", loggedMessages(logs()))
	}
	letGo := make(chan struct{})
	row.link.HoldChunks(t, carriesNAK(), letGo)
	row.thaw()
	var published time.Time
	waitFor(t, untilTheBinaryDeadline(t), func() bool {
		var ended bool
		published, ended = tracedAt(logs(), traceHandBackPublished, row.holder.client)
		return ended
	}, func() string {
		return "control: the hand-back's publish did not end once the write thawed: " + consumerState(row.publisher)
	})
	// The row's one wait on a time of its own: the link keeps the -NAK until slowFlushHold after
	// the publish ended.
	time.Sleep(time.Until(published.Add(slowFlushHold)))
	close(letGo)
	select {
	case <-row.stopped:
	case <-time.After(untilTheBinaryDeadline(t)):
		t.Fatal("the holder's stop did not return once the link let the -NAK go")
	}
	records := logs()
	first, read := logtest.LineWhere(records, func(record map[string]any) bool {
		return record["msg"] == traceHandBacksSettled && record["client"] == tracePointer(row.holder.client)
	})
	switch {
	case !read:
		t.Fatalf("control: the stop made no read of whether its hand-backs reached NATS; the holder logged %q", loggedMessages(records))
	case first["settled"] == true:
		t.Fatalf("control: the stop's first flush answered (%v), so this run reads nothing of a first flush that does not answer within intakeFlushWait (%s)", first, intakeFlushWait)
	case first["reason"] != settledByFlush || first["in_doubt"] == true:
		t.Fatalf("control: the stop's first read of its hand-backs is not a flush that did not answer: %v", first)
	}
	if at, logged := loggedAt(records, intakeHoldFinalizing); logged {
		if deadline := began.Add(landingBudget); at.Before(deadline) {
			t.Fatalf("the stop logged %q %s after its hand-back's publish ended, at least %s before its deadline (landingBudget, %s): its first flush did not answer within intakeFlushWait (%s), and the stop left its hold to a finalizer rather than flush again; the holder logged %q",
				intakeHoldFinalizing, at.Sub(published).Round(time.Millisecond), deadline.Sub(at).Round(time.Millisecond), landingBudget, intakeFlushWait, loggedMessages(records))
		}
		t.Fatalf("control: the premise did not hold: the stop's deadline (landingBudget, %s) passed before a flush answered, so this run reads nothing of a flush that answers before it; the holder logged %q", landingBudget, loggedMessages(records))
	}
	if _, started := logtest.LineWhere(records, func(record map[string]any) bool {
		return record["msg"] == traceFinalizerStarts && record["delivery"] == tracePointer(row.delivery)
	}); started {
		t.Fatal("the stop started a hand-back finalizer although a flush answered before its deadline")
	}
	if holderHolds(t, row.pool, row.holder) {
		t.Fatalf("the stop kept its hold although a flush confirmed its hand-back before its deadline, which costs the standby the lapse; the holder logged %q", loggedMessages(records))
	}
	requireHandBackBeforeCreation(t, row.recorded, row.from, row.publisher, row.sequences)
}

// A hand-back published on one connection of the intake's client is not confirmed by a flush of
// the connection that the client put in its place. The client installs a new connection only once
// the one before it has closed, and a flush of the new connection says nothing of what the old one
// wrote to its socket as it closed. The row runs the client through a link
// (testnats.HandshakeHold) and publishes a hand-back on the client's first connection. The link
// keeps each chunk with a -NAK from the server (HoldChunks, carriesNAK), so the flush that confirms
// a hand-back as it is made (confirmPublishes) cannot answer before the connection goes. Then the
// row closes that connection under the client, lets the -NAK go, and reads it at the server on a
// connection of its own, so the publish ran there, and waits until the client's recovery has
// installed a connected replacement. handBacksSettled must then answer handBacksInDoubt, and not
// flush the replacement and answer handBacksConfirmed.
//
// Control: with a flush of the client's current connection taken to confirm every publish that
// had ended, whatever connection it ran on (the code before endPublish recorded the connection),
// the row fails: the flush of the replacement answers, and handBacksSettled answers
// handBacksConfirmed.
func TestAFlushOfTheReplacementConnectionDoesNotConfirmAHandBackOnTheOneBefore(t *testing.T) {
	natsURL := testnats.URL(t)
	link := testnats.StartHandshakeHold(t, natsURL)
	t.Cleanup(link.Restore)
	client, err := bus.Connect([]string{link.URL()}, bus.WithName(intakeConnectionName))
	if err != nil {
		t.Fatalf("connect the intake's client: %v", err)
	}
	t.Cleanup(client.Close)
	first := client.Connection()
	if first == nil || !first.IsConnected() {
		t.Fatal("control: the intake's client has no connected connection")
	}
	watch := testnats.Connect(t, natsURL)
	const subject, reply = "testhandback.message", "testhandback.reply"
	answers, err := watch.SubscribeSync(reply)
	if err != nil {
		t.Fatalf("subscribe to the hand-back's reply subject: %v", err)
	}
	messages, err := first.SubscribeSync(subject)
	if err != nil {
		t.Fatalf("subscribe the first connection to the message: %v", err)
	}
	for _, conn := range []*natsgo.Conn{watch, first} {
		if err := conn.Flush(); err != nil {
			t.Fatalf("flush a subscription: %v", err)
		}
	}
	if err := watch.PublishMsg(&natsgo.Msg{Subject: subject, Reply: reply}); err != nil {
		t.Fatalf("publish the message: %v", err)
	}
	msg, err := messages.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("control: the first connection was not handed the message: %v", err)
	}

	letGo := make(chan struct{})
	link.HoldChunks(t, carriesNAK(), letGo)
	delivery := &intakeDelivery{nats: client}
	delivery.handBack(msg)
	select {
	case <-delivery.handBacks.handBacksLanded():
	case <-time.After(30 * time.Second):
		t.Fatal("control: the hand-back's publish did not end within 30 s")
	}
	if !delivery.handBacks.publishRecord().unconfirmed() {
		t.Fatal("control: the hand-back was withheld rather than published, or a flush confirmed it while the link kept its -NAK")
	}

	first.Close()
	close(letGo)
	if answer, err := answers.NextMsg(10 * time.Second); err != nil || string(answer.Data) != "-NAK" {
		t.Fatalf("control: the server was not sent the hand-back on the first connection: %v", err)
	}
	waitFor(t, 30*time.Second, func() bool {
		replacement := client.Connection()
		return replacement != nil && replacement != first && replacement.IsConnected()
	}, func() string {
		return "control: the intake's client did not install a connected replacement within 30 s of its first connection closing"
	})
	durable := &heldDurable{nats: client, name: bus.DispatchDeliveryDurable}
	if state := durable.handBacksSettled(delivery); state != handBacksInDoubt {
		t.Fatalf("handBacksSettled answered %d, want handBacksInDoubt (%d), where handBacksConfirmed is %d: a flush of the client's replacement connection counted as confirming a hand-back published on the first one",
			state, handBacksInDoubt, handBacksConfirmed)
	}
}

// A hand-back the stop wrote on its connection's socket before that connection reconnected in
// place is never confirmed by a flush on the new socket: the stop does not release its hold, and,
// with the hand-back already in doubt when it reads it, leaves the hold to lapse itself.
//
// nats.go keeps the *natsgo.Conn through a reconnect and dials a new socket, so a -NAK written to
// the old socket can still reach the server after the reconnect while a flush on the new one
// answers. The row runs a holder through a link (testnats.HandshakeHold) against a real
// nats-server and stops it with a second message waiting behind its key, which the stop hands
// back. The link keeps each chunk with a -NAK from the server (HoldChunks, carriesNAK), so neither
// the flush that confirms a hand-back as it is made (confirmPublishes) nor any flush on that socket
// can answer. Once the stop is about to flush (handBacksWaiting) and the -NAK is held at the link,
// the row cuts the link and restores it (Cut, Restore), and waits until the same connection has
// reconnected (its reconnect count has grown and it is connected again). The stop's read then finds
// the hand-back in doubt (handBacksInDoubt), before its deadline. The row requires:
//   - that the stop does not release the hold: its row still names the holder once the stop has
//     returned;
//   - that the stop logs no intakeHoldFinalizing line and starts no finalizer
//     (traceFinalizerStarts): it keeps nothing renewing, since the hand-back's socket has gone
//     already, and it leaves the hold to lapse at once;
//   - that the stop leaves the hold to lapse itself (intakeHoldLeftToLapse), so the hold reaches its
//     expiry unrenewed;
//   - once the row lets the held -NAK go, that the server reads it, after the reconnect.
//
// Control: with 7ce70c15's held_durable.go and intake_delivery.go, which read only the connection
// a hand-back ran on and not its reconnect count, the row fails: the flush on the new socket
// answers, and the stop releases its hold. With d09c4f91's, whose stop read only whether the
// hand-backs were confirmed, the row fails at the intakeHoldFinalizing check: the stop logs that
// line and leaves the hold to a finalizer, which reads the same doubt and then leaves it to lapse.
func TestAHandBackWrittenBeforeAReconnectInPlaceIsNotConfirmedByAFlushOnTheNewSocket(t *testing.T) {
	trace := traceStops(t)
	logs := captureLogs(t)
	pool, ctx := deliveryTestPool(t)
	seedDeliverySettings(t, ctx, pool)
	natsURL := testnats.URL(t)
	link := testnats.StartHandshakeHold(t, natsURL)
	t.Cleanup(link.Restore)
	trace.watchLink(link, "holder")
	publisher := intakeTestClient(t)
	trace.name(publisher, "publisher")
	recorded := watchDurableEvents(t, natsURL)
	trace.watchDurable(recorded)

	const inFlight = 100
	holder := startIntakeProcess(t, pool, link.URL(), func(number int) bool { return number == inFlight })
	trace.name(holder.client, "holder")
	awaitBoundDurable(t, publisher)
	conn := holder.client.Connection()
	trace.name(conn, "holder")
	publishMergedPullRequest(t, publisher, inFlight)
	waitFor(t, 20*time.Second, func() bool { return holder.fetches(inFlight) == 1 }, func() string {
		return "control: the holder never started on the first message: " + consumerState(publisher)
	})
	publishMergedPullRequest(t, publisher, inFlight)
	waitFor(t, 20*time.Second, func() bool { return holder.keyedRunnerQueued() == 1 }, func() string {
		return "control: the second message never waited behind its key in the holder: " + consumerState(publisher)
	})
	delivery := subscribedDelivery(holder.intake)
	trace.name(delivery, "holder")

	nak := carriesNAK()
	nakHeld := make(chan struct{})
	var heldOnce sync.Once
	letGo := make(chan struct{})
	link.HoldChunks(t, func(chunk []byte) bool {
		if !nak(chunk) {
			return false
		}
		heldOnce.Do(func() { close(nakHeld) })
		return true
	}, letGo)
	before := conn.Stats().Reconnects
	reconnected := make(chan string, 1)
	var hooked sync.Once
	hook := func() {
		hooked.Do(func() {
			select {
			case <-nakHeld:
			case <-time.After(30 * time.Second):
				reconnected <- "control: the stop's -NAK did not reach the link within 30 s"
				return
			}
			link.Cut()
			link.Restore()
			if !testwait.Holds(30*time.Second, 20*time.Millisecond, func() bool {
				return conn.Stats().Reconnects > before && conn.IsConnected()
			}) {
				reconnected <- "control: the holder's connection did not reconnect in place within 30 s of the cut"
				return
			}
			reconnected <- ""
		})
	}
	handBacksWaiting.Store(&hook)
	t.Cleanup(func() { handBacksWaiting.Store(nil) })
	from := recorded.count()
	trace.stopBegins(from)
	trace.printAtFailure(t)

	stopped := make(chan struct{})
	go func() {
		holder.stop()
		close(stopped)
	}()
	waitFor(t, 20*time.Second, holder.keyedRunnerStopped, func() string {
		return "control: the stopping holder has not stopped its keyed runner: " + consumerState(publisher)
	})
	holder.release()
	select {
	case failure := <-reconnected:
		if failure != "" {
			t.Fatal(failure)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("control: the stop never came to flush its hand-backs")
	}
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("the holder's stop did not return within 30 s of the reconnect")
	}
	if named, _ := deliveryHold(t, pool); named != holder.intake.holder.String() {
		t.Fatalf("the stop released its hold: a flush on the socket its connection reconnected to confirmed a hand-back written to the socket before; the hold's row names %q, want the holder %s",
			named, holder.intake.holder)
	}
	records := logs()
	if loggedMessage(records, intakeHoldFinalizing) {
		t.Fatalf("the stop logged %q although its hand-back was already in doubt when it read it: the hand-back's socket has gone already, so the stop keeps nothing renewing and leaves the hold to lapse at once; the holder logged %q",
			intakeHoldFinalizing, loggedMessages(records))
	}
	if _, started := logtest.LineWhere(records, func(record map[string]any) bool {
		return record["msg"] == traceFinalizerStarts && record["delivery"] == tracePointer(delivery)
	}); started {
		t.Fatal("the stop started a hand-back finalizer for a hand-back already in doubt, which can only read the same doubt again")
	}
	if !loggedMessage(records, intakeHoldLeftToLapse) {
		t.Fatalf("the stop did not say it left its hold to lapse (%q); the holder logged %q", intakeHoldLeftToLapse, loggedMessages(records))
	}
	if holdRenewedBeforeItsExpiry(t, pool) {
		t.Fatal("the hold was renewed after the stop returned with its hand-back in doubt: the stop did not leave it to lapse")
	}
	close(letGo)
	waitFor(t, 30*time.Second, func() bool { return slices.Contains(recorded.since(from), "-NAK") }, func() string {
		return fmt.Sprintf("control: the -NAK written before the reconnect did not reach the server within 30 s of the link letting it go: %q", recorded.since(from))
	})
}

// A routine hand-back, made outside any stop, is confirmed by a flush as it is made, so a clean
// stop after a reconnect in place releases the hold at once rather than leaving it to lapse.
//
// The Legion state intake hands messages back in its routine work (HeldMessage.HandBackAfter), and
// the same delivery goes on delivering across a reconnect. The row runs a held durable
// (NewHeldDurable) through a link (testnats.HandshakeHold) whose handler hands the first message
// back and acknowledges its redelivery. It waits until the delivery's record shows the hand-back
// confirmed, cuts the link and restores it (Cut, Restore), and waits until the same connection has
// reconnected. Then it stops the durable, with no hand-back pending, and requires that the stop
// released the hold by the time Run returned: the hold's row is gone, no finalizer started, and
// the stop's settle read found no published hand-back unconfirmed.
//
// Control: with Part 1's code and no confirmation as a hand-back is made (the delivery's record
// confirmed only by a stop's flush), the row fails: the routine hand-back stays unconfirmed at the
// connection's old reconnect count, the stop reads it in doubt, and its finalizer leaves the hold
// to lapse.
func TestARoutineHandBackConfirmedAsItIsMadeLetsACleanStopAfterAReconnectReleaseTheHold(t *testing.T) {
	trace := traceStops(t)
	logs := captureLogs(t)
	pool, _ := deliveryTestPool(t)
	natsURL := testnats.URL(t)
	link := testnats.StartHandshakeHold(t, natsURL)
	t.Cleanup(link.Restore)
	trace.watchLink(link, "holder")
	publisher := intakeTestClient(t)
	trace.name(publisher, "publisher")
	second := storetest.SecondProcess(t, &store.Store{Pool: pool})
	client, err := bus.Connect([]string{link.URL()}, bus.WithName(intakeConnectionName))
	if err != nil {
		t.Fatalf("connect the holder's client: %v", err)
	}
	t.Cleanup(client.Close)
	trace.name(client, "holder")
	var handled atomic.Int32
	held := NewHeldDurable(client, storetest.Leases(t, second.Pool), deliveryConsumerConfig, intakeKey, func(message HeldMessage) {
		if handled.Add(1) == 1 {
			message.HandBack()
			return
		}
		message.Ack()
	}, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		held.Run(runCtx)
	}()
	t.Cleanup(sync.OnceFunc(func() {
		cancel()
		<-ran
		held.Stop()
	}))
	trace.printAtFailure(t)
	awaitBoundDurable(t, publisher)
	conn := client.Connection()
	trace.name(conn, "holder")

	publishMergedPullRequest(t, publisher, 1)
	waitFor(t, 30*time.Second, func() bool { return handled.Load() >= 2 }, func() string {
		return fmt.Sprintf("control: the holder handled the message %d time(s), want the hand-back and the redelivery: %s", handled.Load(), consumerState(publisher))
	})
	awaitEverythingAcknowledged(t, publisher)
	held.mu.Lock()
	delivery := held.delivering
	held.mu.Unlock()
	trace.name(delivery, "holder")
	confirmed := testwait.Holds(10*time.Second, 20*time.Millisecond, func() bool {
		record := delivery.handBacks.publishRecord()
		return record.ended > 0 && !record.unconfirmed()
	})

	before := conn.Stats().Reconnects
	link.Cut()
	link.Restore()
	waitFor(t, 30*time.Second, func() bool { return conn.Stats().Reconnects > before && conn.IsConnected() }, func() string {
		return "control: the holder's connection did not reconnect in place within 30 s of the cut"
	})

	cancel()
	select {
	case <-ran:
	case <-time.After(30 * time.Second):
		t.Fatal("the held durable's Run did not return within 30 s of its stop")
	}
	records := logs()
	if named, _ := deliveryHold(t, pool); named != "" {
		t.Fatalf("the clean stop after a reconnect did not release the hold, whose row names %q (the record showed the routine hand-back confirmed before the reconnect: %t); the holder logged %q",
			named, confirmed, loggedMessages(records))
	}
	if loggedMessage(records, intakeHoldFinalizing) {
		t.Fatalf("the clean stop left its hold to a finalizer (the record showed the routine hand-back confirmed before the reconnect: %t)", confirmed)
	}
	if _, found := logtest.LineWhere(records, func(record map[string]any) bool {
		return record["msg"] == traceHandBacksSettled && record["settled"] == true && record["reason"] == "no published hand-back is unconfirmed"
	}); !found {
		t.Fatal("control: the stop's settle read did not find every published hand-back confirmed")
	}
}

// A hand-back that nats.go refused before it queued a byte is not in doubt: a clean stop after the
// reconnect it was refused in releases the hold at once.
//
// The bus keeps no reconnect buffer (internal/bus, ReconnectBufSize -1), so while a connection
// reconnects nats.go refuses every publish with ErrReconnectBufExceeded, under its lock and before
// it queues a byte. An acknowledgement refused that way is handed back (intakeDelivery.acknowledge),
// and that hand-back is refused the same way. The row runs a held durable (NewHeldDurable) through
// a link (testnats.HandshakeHold) whose handler acknowledges the first message only once the row
// has cut the link and the connection is reconnecting (IsReconnecting). Once that hand-back has
// been refused, the row restores the link and waits until the same connection has reconnected.
// Then it stops the durable, with nothing in flight, and requires that the stop released the hold
// by the time Run returned (the hold's row is gone), that it logged neither intakeHoldFinalizing
// nor intakeHoldLeftToLapse, and that its settle read found no published hand-back unconfirmed.
//
// The handler's calls wait for NATS up to the row's own bound rather than callNATSWait, so the
// acknowledgement's answer is read even when a reconnect attempt holds nats.go's lock as it is
// made; the row puts the wait back once the handler has answered.
//
// Control: with d09c4f91's held_durable.go and intake_delivery.go, which counted every publish but
// one refused as closed, the row fails at the hold check: the refused hand-back is counted at the
// reconnect count before the reconnect, the stop reads it in doubt, and the hold is left to lapse.
func TestAHandBackRefusedDuringAReconnectLetsACleanStopReleaseTheHold(t *testing.T) {
	trace := traceStops(t)
	logs := captureLogs(t)
	pool, _ := deliveryTestPool(t)
	natsURL := testnats.URL(t)
	link := testnats.StartHandshakeHold(t, natsURL)
	t.Cleanup(link.Restore)
	trace.watchLink(link, "holder")
	publisher := intakeTestClient(t)
	trace.name(publisher, "publisher")
	second := storetest.SecondProcess(t, &store.Store{Pool: pool})
	client, err := bus.Connect([]string{link.URL()}, bus.WithName(intakeConnectionName))
	if err != nil {
		t.Fatalf("connect the holder's client: %v", err)
	}
	t.Cleanup(client.Close)
	trace.name(client, "holder")
	// The handler holds the first message until the row lets it answer, then acknowledges it.
	handling := make(chan struct{})
	mayAnswer := make(chan struct{})
	answered := make(chan struct{})
	letAnswer := sync.OnceFunc(func() { close(mayAnswer) })
	var handled atomic.Int32
	held := NewHeldDurable(client, storetest.Leases(t, second.Pool), deliveryConsumerConfig, intakeKey, func(message HeldMessage) {
		if handled.Add(1) == 1 {
			close(handling)
			<-mayAnswer
			message.Ack()
			close(answered)
			return
		}
		message.Ack()
	}, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		held.Run(runCtx)
	}()
	t.Cleanup(sync.OnceFunc(func() {
		cancel()
		<-ran
		held.Stop()
	}))
	// Registered after the Run's cleanup, so it runs before it: a row that ends early lets the
	// handler answer, and the stop's wait for it returns.
	t.Cleanup(letAnswer)
	trace.printAtFailure(t)
	awaitBoundDurable(t, publisher)
	conn := client.Connection()
	trace.name(conn, "holder")

	publishMergedPullRequest(t, publisher, 1)
	select {
	case <-handling:
	case <-time.After(30 * time.Second):
		t.Fatal("control: the holder's handler was never handed the message: " + consumerState(publisher))
	}
	held.mu.Lock()
	delivery := held.delivering
	held.mu.Unlock()
	trace.name(delivery, "holder")

	before := conn.Stats().Reconnects
	link.Cut()
	waitFor(t, 30*time.Second, conn.IsReconnecting, func() string {
		return "control: the holder's connection was not reconnecting within 30 s of the cut"
	})
	wait := callNATSWait.Swap(int64(untilTheBinaryDeadline(t)))
	restoreWait := sync.OnceFunc(func() { callNATSWait.Store(wait) })
	t.Cleanup(restoreWait)
	letAnswer()
	select {
	case <-answered:
	case <-time.After(untilTheBinaryDeadline(t)):
		t.Fatal("the handler's acknowledgement did not return while the link was cut")
	}
	restoreWait()
	waitFor(t, 30*time.Second, func() bool { return handBackRefused(logs(), delivery, natsgo.ErrReconnectBufExceeded) }, func() string {
		return fmt.Sprintf("control: nats.go did not refuse a hand-back of the holder with %q while its connection reconnected; the holder logged %q",
			natsgo.ErrReconnectBufExceeded, loggedMessages(logs()))
	})
	link.Restore()
	waitFor(t, 30*time.Second, func() bool { return conn.Stats().Reconnects > before && conn.IsConnected() }, func() string {
		return "control: the holder's connection did not reconnect in place within 30 s of the link's restore"
	})

	cancel()
	select {
	case <-ran:
	case <-time.After(30 * time.Second):
		t.Fatal("the held durable's Run did not return within 30 s of its stop")
	}
	records := logs()
	if named, _ := deliveryHold(t, pool); named != "" {
		t.Fatalf("the clean stop after a hand-back that nats.go refused during a reconnect did not release the hold, whose row names %q: the refused hand-back was counted as one that may still reach the server; the holder logged %q",
			named, loggedMessages(records))
	}
	for _, line := range []string{intakeHoldFinalizing, intakeHoldLeftToLapse} {
		if loggedMessage(records, line) {
			t.Fatalf("the clean stop logged %q, with nothing in flight and its one hand-back refused before nats.go queued a byte; the holder logged %q", line, loggedMessages(records))
		}
	}
	if _, found := logtest.LineWhere(records, func(record map[string]any) bool {
		return record["msg"] == traceHandBacksSettled && record["settled"] == true && record["reason"] == "no published hand-back is unconfirmed"
	}); !found {
		t.Fatal("the stop's settle read did not find every published hand-back confirmed: the hand-back nats.go refused was counted")
	}
}

// A write to a frozen link outside any stop holds the intake's connection lock only until the
// production write deadline (IntakeWriteDeadline), and no message is lost: the deadline ends the
// write, nats.go closes that socket and reconnects the same connection once the link takes it,
// and each message published around the freeze is handled once.
//
// The row runs a holder whose client writes with production's intake write deadline
// (withIntakeWriteDeadline) through a link (testnats.HandshakeHold). Two messages are handled
// before the freeze. The link then freezes with a write parked in the holder's socket
// (ParkAWrite), and a third message is published while it is frozen: the server pushes it to
// the frozen socket, which never hands it to the holder. The row requires that the write ends, so
// the connection is no longer connected, within IntakeWriteDeadline and 5 s of the freeze; then it
// thaws the link, requires the same connection to reconnect (its reconnect count grown by one),
// and publishes a fourth message. It requires that each of the four messages is handled once and
// acknowledged: the third comes back to the reconnected holder after the durable's ack wait
// (intakeAckWait, 25 s here, with shrinkIntakeFlowControl and intakeAckLanding at 5 s), since the
// server counted it delivered to the socket that went. No message is handed back outside a stop
// here: the GitHub intake hands one back only when a head write or an acknowledgement fails.
//
// Control: with nats.go's default write deadline (startIntakeProcess with no option), the write
// holds the lock for a minute, the connection is still connected 10 s after the freeze, and the
// row fails at that check.
func TestAWriteFrozenPastTheIntakesWriteDeadlineReconnectsAndLosesNoMessage(t *testing.T) {
	trace := traceStops(t)
	shrinkIntakeFlowControl(t)
	// Set before the intake starts, whose take-over reads it (intakeAckWait). The ack wait stays
	// above what the in-flight window can take to handle, intakeMaxAckPending times
	// intakeMessageTimeout (20 s).
	ackLanding := intakeAckLanding
	intakeAckLanding = 5 * time.Second
	t.Cleanup(func() { intakeAckLanding = ackLanding })
	pool, ctx := deliveryTestPool(t)
	seedDeliverySettings(t, ctx, pool)
	natsURL := testnats.URL(t)
	link := testnats.StartHandshakeHold(t, natsURL)
	t.Cleanup(link.Restore)
	trace.watchLink(link, "holder")
	publisher := intakeTestClient(t)
	trace.name(publisher, "publisher")
	holder := startIntakeProcess(t, pool, link.URL(), nil, withIntakeWriteDeadline)
	logReconnects(t, holder.client, nil)
	trace.name(holder.client, "holder")
	trace.printAtFailure(t)
	awaitBoundDurable(t, publisher)
	conn := holder.client.Connection()
	trace.name(conn, "holder")
	before := conn.Stats().Reconnects

	numbers := []int{1, 2, 3, 4}
	for _, number := range numbers[:2] {
		publishMergedPullRequest(t, publisher, number)
	}
	awaitEverythingAcknowledged(t, publisher)

	thaw := link.ParkAWrite(t, conn)
	frozen := time.Now()
	writeBound := time.After(IntakeWriteDeadline + 5*time.Second)
	publishMergedPullRequest(t, publisher, numbers[2])
	// The read waits for nats.go's lock, which the frozen write holds, so it runs on a goroutine of
	// its own and the row's wait stays bounded.
	writeEnded := make(chan struct{})
	go func() {
		defer close(writeEnded)
		for conn.IsConnected() && t.Context().Err() == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}()
	select {
	case <-writeEnded:
	case <-writeBound:
		t.Fatalf("the holder's connection was still connected %s after its write froze: the write held nats.go's connection lock past the write deadline (%s)",
			time.Since(frozen).Round(time.Millisecond), IntakeWriteDeadline)
	}
	ended := time.Since(frozen)
	thaw()
	waitFor(t, 30*time.Second, func() bool { return conn.Stats().Reconnects == before+1 && conn.IsConnected() }, func() string {
		return fmt.Sprintf("the holder's connection did not reconnect within 30 s of the link thawing: reconnects %d, from %d before the freeze",
			conn.Stats().Reconnects, before)
	})
	publishMergedPullRequest(t, publisher, numbers[3])

	waitFor(t, intakeAckWait()+30*time.Second, func() bool {
		for _, number := range numbers {
			if holder.fetches(number) == 0 {
				return false
			}
		}
		return true
	}, func() string {
		return fmt.Sprintf("the holder did not handle every message within the ack wait and 30 s: fetches %v; %s", fetchesOf(holder, numbers), consumerState(publisher))
	})
	awaitEverythingAcknowledged(t, publisher)
	for _, number := range numbers {
		if got := holder.fetches(number); got != 1 {
			t.Errorf("the holder handled pull request %d %d times, want once: fetches %v", number, got, fetchesOf(holder, numbers))
		}
	}
	if got := conn.Stats().Reconnects; got != before+1 {
		t.Errorf("the holder's connection reconnected %d times, want once", got-before)
	}
	t.Logf("the write deadline ended the frozen write %s after the freeze", ended.Round(time.Millisecond))
}
