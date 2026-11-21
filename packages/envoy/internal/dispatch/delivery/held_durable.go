package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/duty"
)

// durableBindInterval is how often an intake that does not deliver tries for its durable's hold,
// and for the durable under it. It is cmd/listener/durable.go's interval: a standby polls every two
// seconds, so it takes the durable over within one interval of the holder releasing its hold, and
// within one interval of the hold's lapse when the holder dies (intakeHoldTTL).
//
// Unlike the listener, which gives up at its durableBindDeadline and exits so the runtime starts
// it again, Dispatch's standby polls for the life of the process: there is no deadline and no
// exit. A durable another envoy-dispatch process holds is not a failed start but the ordinary
// state of every process but one, which has every other thing a Dispatch server does to get on
// with meanwhile.
const durableBindInterval = 2 * time.Second

// standbyLogEvery bounds how often one intake says it is standing by for its durable. The first
// refusal is logged as it happens, so an operator sees at once which process stood by and why;
// the hold is then tried every durableBindInterval, and an unthrottled line would be 1,800 an hour
// per durable, each saying exactly what the first said.
const standbyLogEvery = 30 * time.Second

// The hold a process delivers from a durable under (intakeHoldName, duty.Hold): it lasts
// intakeHoldTTL on the database's clock after each renewal, is renewed every intakeHoldRenew, and
// reports itself lost intakeHoldMargin before it lapses, so the holder stops delivering before any
// standby can take it. A holder that dies is replaced within intakeHoldTTL plus one
// durableBindInterval. The holder checks that the durable still delivers to it on the same
// interval it renews (heldDurable.check).
const (
	intakeHoldTTL    = 15 * time.Second
	intakeHoldRenew  = 5 * time.Second
	intakeHoldMargin = 2 * time.Second
)

// intakeHoldName names the hold a process delivers from durable under. Its token records what the
// next holder needs (intakeHoldToken).
func intakeHoldName(durable string) string { return "delivery-intake-" + durable }

// intakeHoldToken is what an intake's hold records as its token:
//
//   - subject, the deliver subject the last holder made the durable deliver to, which tells the
//     next holder whether a binding it finds is a holder's (heldDurable.boundElsewhere);
//   - start, the stream sequence a take-over that deleted the durable creates it again at, or 0
//     for none. A take-over records it before its delete and clears it once its create succeeds.
//     What a take-over that never creates the durable again leaves in the row, and who reads
//     that start next, is on heldDurable.finishTakeOver. A holder that stops with the start set
//     lapses its hold rather than deleting the row (heldDurable.stop), so the next holder still
//     reads it.
//
// The token is the subject alone, or the subject followed by intakeHoldTokenStart and the
// sequence in decimal. A deliver subject holds no space, so the two never mix. A token of any
// other shape reads as a subject alone.
type intakeHoldToken struct {
	subject string
	start   uint64
}

// intakeHoldTokenStart separates an intake hold token's subject from its start.
const intakeHoldTokenStart = " start="

func (t intakeHoldToken) String() string {
	if t.start == 0 {
		return t.subject
	}
	return t.subject + intakeHoldTokenStart + strconv.FormatUint(t.start, 10)
}

// parseIntakeHoldToken reads the token a hold's row records (intakeHoldToken).
func parseIntakeHoldToken(token string) intakeHoldToken {
	subject, start, found := strings.Cut(token, intakeHoldTokenStart)
	if !found {
		return intakeHoldToken{subject: token}
	}
	sequence, err := strconv.ParseUint(start, 10, 64)
	if err != nil || sequence == 0 {
		return intakeHoldToken{subject: token}
	}
	return intakeHoldToken{subject: subject, start: sequence}
}

// DurableLeases is the pool a held durable takes its hold and consumer lease on and renews the hold
// through: the reservation ReserveDurableLeases makes of store.Pool.Leases, which StartIntakes hands
// each intake and a held durable outside this package (NewHeldDurable) takes from its own. A
// *pgxpool.Pool is both.
type DurableLeases interface {
	duty.Leases
	duty.Holds
}

// consumerLease names the lease every change to one durable runs under: this build's take-over
// (heldDurable.takeOver), and the configuration reconciliation of the build before holds, whose
// processes still run during a rolling release and take the same lease. So the two never delete,
// create or update one durable at once.
//
// The name carries the durable because that, and only that, is what the exclusion is about: two
// processes over one durable. One name over both delivery durables coupled two independent
// reconciliations, and INFRA-66 measured what that cost against a real nats-server and the real
// envoy-dispatch binary, with the Dispatch user denied exactly
// $JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.delivery-events.> (envoy-infra's live row "without
// the delivery consumer create Dispatch leaves only that intake unbound"). A denied AddConsumer
// drew no reply at all then, so the create blocked for the client's whole JetStream MaxWait -- 10 s
// (internal/bus/nats.go, nats.MaxWait) -- and the lease was held for every second of it; it now
// ends as the server's refusal arrives (internal/bus/refusal_reads.go), and a take-over step the
// grant refuses is not tried again (retryHeld), so the lease is released within milliseconds,
// while a change that is stuck some other way still holds its lease for as long as it takes. The
// events intake took the lease again on its next durableBindInterval poll and held it for another
// 10 s, round after round:
//
//	17:57:16.232 ERROR dispatch delivery: create NATS consumer name=delivery-events error="context deadline exceeded"
//	17:57:16.232 INFO  dispatch duty: leased pass ran  lease=delivery-consumers
//	17:57:26.235 ERROR dispatch delivery: create NATS consumer name=delivery-events error="context deadline exceeded"
//	17:57:26.236 INFO  dispatch duty: leased pass ran  lease=delivery-consumers
//
// Under that one name the job intake, whose own durable is created by a pass under the same lease,
// could only get in during the milliseconds between those holds: in CI's run of that row it never
// did, and the row failed with "consumer delivery-jobs was not push-bound within 20s". A lease per
// durable keeps a stuck change to the durable it is stuck on.
func consumerLease(durable string) string { return "delivery-consumer-" + durable }

// bindThrottle is one intake's own standby-log state for its own durable: when it last said it
// was standing by for it, and why. Per intake rather than a package-global map keyed by consumer
// name, because an intake is exactly what the window is about -- one process's repeated polls of
// one durable -- and a global outlives the durable it throttles: a second intake of the same name
// would stand by silently for a durable it has said nothing about.
type bindThrottle struct {
	mu sync.Mutex
	// standby is when the last standby line was logged and standbyReason the message it carried,
	// so a change of state -- the hold going from another process's to a binding no hold recorded
	// -- is said at once rather than waiting out the window of the line before it.
	standby       time.Time
	standbyReason string
}

// reportStandby logs message at INFO -- this durable is not this process's to deliver from yet,
// for the reason message names -- unless this intake logged that same message within
// standbyLogEvery.
func (t *bindThrottle) reportStandby(name, message string) {
	t.mu.Lock()
	if t.standbyReason == message && time.Since(t.standby) < standbyLogEvery {
		t.mu.Unlock()
		return
	}
	t.standby, t.standbyReason = time.Now(), message
	t.mu.Unlock()
	slog.Info(message, "name", name)
}

// heldDurable is one intake's delivery from its durable, which it does only while it holds the
// durable's hold (intakeHoldName), so that of every envoy-dispatch process -- of this build and of
// the build before it, during a rolling release -- one delivers from the durable at a time:
//
//   - A standby tries for the hold every durableBindInterval and takes it when no holder holds it
//     live: released, lapsed, or never taken.
//   - Under the hold, and under the durable's consumer lease, it takes the durable over
//     (takeOver): it records the stream sequence after the durable's ack floor in the hold's
//     token, deletes the durable, subscribes to a fresh deliver subject, creates the durable again
//     at that sequence on that subject, and records the subject alone as the hold's token
//     (intakeHoldToken). The delete and create, rather than an update of the deliver subject,
//     redeliver a dead holder's unacked messages at once and fence the holder off: a
//     subscription its client sends again after a reconnect is on a subject the durable no
//     longer delivers to.
//     nats-server refuses that update anyway while the old subject still has interest, which a
//     dead connection keeps for minutes; a delete has no such check. Subscribing before the
//     create leaves the durable push-bound from the moment it exists. A durable bound to a
//     subject no hold recorded is a process of the build before holds, which binds by lookup and
//     holds nothing: it is never taken from it, and the take-over waits until it lets go.
//   - The holder stops delivering at once when its hold is lost, and when its check finds the
//     durable delivering to another subject (check); it does not handle a message it is handed
//     once its hold's loss deadline has passed, and leaves it with the durable rather than
//     answering for it (intakeDelivery.receive, intakeDelivery.handBack).
//   - When its process stops, it stops delivering and hands back every message it was delivered
//     and did not handle. Once a flush of its connection, begun after each hand-back's publish
//     ended, has answered, every hand-back has reached the server, and it releases the hold
//     (stop): a standby takes over at once and is redelivered those messages within seconds rather
//     than after the ack wait. A stop with a hand-back still pending, or whose flush cannot
//     confirm the hand-backs reached the server, keeps the hold renewing until a flush does, and
//     releases it only then (finalizeHandBacks). When the socket a published hand-back went out
//     on goes before a flush on it confirms the hand-back -- the client closes, or its connection
//     reconnects -- that hand-back may still be on its way to the server, so the hold is not
//     released: it stops renewing and lapses at its row's expiry (duty.Hold.Abandon).
//
// The run loop owns hold, recorded and subject; mu guards delivering, which StopDelivering reads
// from any goroutine.
type heldDurable struct {
	nats   *bus.Client
	leases DurableLeases
	// holder names this intake in its hold's row: each intake draws one as it is built.
	holder uuid.UUID
	// throttle is this intake's own standby-log state (bindThrottle).
	throttle *bindThrottle
	// config is the durable's config in this build; each call draws a fresh deliver subject.
	config func() natsgo.ConsumerConfig
	// name is the durable's name: config's Durable, which every call returns alike.
	name string
	// key reads whether this intake wants a message at all and the key whose goroutine handles it
	// (intakeKey for the delivery intake, jobKey for the job intake).
	key func(*natsgo.Msg) (string, *contracts.Envelope, bool)
	// handle handles one message on its key's goroutine and answers it there, under the delivery
	// it arrived on: that is what carries the handler's context and what says whether an answer
	// of the handler's own is still the hold's to make (intakeDelivery.mayAnswer).
	handle func(delivery *intakeDelivery, message intakeMessage)

	// hold is the durable's hold while this intake holds it.
	hold *duty.Hold
	// recorded is the token the hold's row records (intakeHoldToken): the deliver subject the last
	// holder made the durable deliver to, which may be this intake's own, and the start a take-over
	// that deleted the durable and did not create it again recorded.
	recorded intakeHoldToken
	// subject is the deliver subject this intake delivers at, and empty while it delivers nothing.
	subject string
	// bound, when not nil, is the owner's take-over callback: it runs on the run loop once each
	// take-over has bound the durable, with the subject recorded and the subscription delivering,
	// so work it queues on a key (queue) runs on that subscription's keyed runner. The two intakes
	// of this package set none; a held durable outside it names one (NewHeldDurable).
	bound func()
	// boundHook, when not nil, runs once a take-over has bound the durable, before the take-over
	// returns. It is nil outside tests: a test sets it to hold a take-over that has bound the
	// durable until past Run's ctx's end.
	boundHook func()

	mu sync.Mutex
	// delivering is the latest subscription subscribe made, which StopDelivering stops.
	delivering *intakeDelivery
	// finalizers keep a stopped hold renewing while a hand-back still can reach NATS
	// (finalizeHandBacks). confirming counts the finalizers that may still confirm their hand-backs
	// with the connection open: each one leaves it when it ends, or intakeFinalizerConfirmWait after
	// it started if that comes first. The caller that closes d.nats waits for confirming before that
	// close (awaitConfirming) and for finalizers after it, while its database pool is still open
	// (waitFinalizers).
	finalizers sync.WaitGroup
	confirming sync.WaitGroup
}

// newHeldDurable builds the delivery from config's durable on natsClient, taking its hold and
// consumer lease on leases, routing each message by key and handing it to handle.
func newHeldDurable(natsClient *bus.Client, leases DurableLeases, config func() natsgo.ConsumerConfig,
	key func(*natsgo.Msg) (string, *contracts.Envelope, bool), handle func(*intakeDelivery, intakeMessage),
) heldDurable {
	return heldDurable{
		nats: natsClient, leases: leases, holder: uuid.New(), throttle: &bindThrottle{},
		config: config, name: config().Durable, key: key, handle: handle,
	}
}

// queue hands work to key's goroutine of the subscription this intake delivers on, behind the
// messages and the work key already has waiting, so it never runs beside one of key's messages,
// and reports whether it took work. It takes nothing while this intake delivers nothing, once that
// subscription has begun to stop, or once its hold no longer covers it: the work then has no
// runner to wait on, and the next take-over's callback (bound) is where its owner queues it again.
// Work taken runs under the subscription's handling context, which the stop cancels when it gives
// its handlers up; work still waiting behind its key at the stop is dropped, since it holds no
// message to hand back (intakeDelivery.stop).
func (d *heldDurable) queue(key string, work func(context.Context)) bool {
	d.mu.Lock()
	delivery := d.delivering
	d.mu.Unlock()
	if delivery == nil || delivery.stopping.Load() || !delivery.holdCovers() {
		return false
	}
	return delivery.keys.run(key, intakeMessage{work: work})
}

// HeldDurable is a heldDurable for a consumer outside this package: Dispatch's Legion state intake
// (legion.Intake) delivers from its own durable under that durable's own hold and consumer lease,
// exactly as the two intakes here deliver from theirs, so that of every envoy-dispatch process one
// delivers from it at a time. Its owner names the durable's config, how a message is keyed, how
// one is handled, and what runs once a take-over has bound the durable; Run, Queue and Stop are
// what it does with them.
type HeldDurable struct {
	heldDurable
}

// HeldMessage is one message a HeldDurable hands its handler, on the goroutine of the message's
// key: the message, its envelope as the key decoded it (nil where it did not decode), and the
// delivery it arrived on, which answers it.
type HeldMessage struct {
	Msg      *natsgo.Msg
	Envelope *contracts.Envelope
	delivery *intakeDelivery
}

// NewHeldDurable builds the delivery from config's durable on natsClient, a client the owner opens
// for it alone (a bus.Client holds one JetStream subscription), taking the durable's hold and
// consumer lease on leases (ReserveDurableLeases), keying each message by key and handing it to
// handle on that key's goroutine. bound, when not nil, runs once each take-over has bound the
// durable, on the run loop: its first creation, and every take-over after it by this process. It
// should only queue work (Queue), since the run loop waits for it.
func NewHeldDurable(natsClient *bus.Client, leases DurableLeases, config func() natsgo.ConsumerConfig,
	key func(*natsgo.Msg) (string, *contracts.Envelope, bool), handle func(HeldMessage), bound func(),
) *HeldDurable {
	held := &HeldDurable{heldDurable: newHeldDurable(natsClient, leases, config, key, func(delivery *intakeDelivery, message intakeMessage) {
		handle(HeldMessage{Msg: message.msg, Envelope: message.envelope, delivery: delivery})
	})}
	held.bound = bound
	return held
}

// Run delivers from the durable while this process holds its hold, and stands by for the hold
// otherwise, until ctx ends; then it stops delivering and releases the hold under intakeStopBudget
// from ctx's end, as the intakes' Runs do (heldDurable.run), and returns.
func (h *HeldDurable) Run(ctx context.Context) {
	h.run(ctx, nil, func() bool { return true })
}

// Stop, called once Run has returned, closes the durable's NATS client and waits for each hand-back
// finalizer Run left behind (stopDurables). Run can return at its stop deadline with a hand-back
// still waiting for NATS. Its finalizer has until intakeFinalizerConfirmWait after it started to
// confirm that hand-back with a flush, and Stop waits for that (awaitConfirming) before it closes
// the client. A finalizer that confirmed its hand-backs releases the hold. After the close, a
// finalizer with a published hand-back that no flush confirmed leaves the hold to lapse at its
// expiry, since that hand-back may still reach the server, and a finalizer with none releases the
// hold (finalizeHandBacks). Stop returns once each finalizer has. So an owner that closes the pool
// after Stop, as cmd/dispatch's shutdown does, closes it under no finalizer's write.
func (h *HeldDurable) Stop() {
	stopDurables(&h.heldDurable)
}

// stopDurables stops durables whose Runs have returned (HeldDurable.Stop, stopIntakes): it waits
// for each one's hand-back finalizers to confirm their hand-backs with the connection open, for up
// to intakeFinalizerConfirmWait from each one's start (awaitConfirming), then closes each one's
// NATS client (closeNATS), then waits for each one's finalizers to end (waitFinalizers).
func stopDurables(durables ...*heldDurable) {
	for _, durable := range durables {
		durable.awaitConfirming()
	}
	for _, durable := range durables {
		durable.closeNATS()
	}
	for _, durable := range durables {
		durable.waitFinalizers()
	}
}

// closeNATS closes this intake's NATS client (stopDurables). A trace line marks the start of the
// close and its end, with the client.
func (d *heldDurable) closeNATS() {
	if tracing() {
		slog.Debug(traceClientCloses, d.traceAttrs()...)
	}
	d.nats.Close()
	if tracing() {
		slog.Debug(traceClientClosed, d.traceAttrs()...)
	}
}

// Queue runs work on key's goroutine, in its turn among key's messages, and reports whether it
// took it (heldDurable.queue): never while this process does not deliver from the durable.
func (h *HeldDurable) Queue(key string, work func(context.Context)) bool {
	return h.queue(key, work)
}

// Context is the context the message's handling runs under: the stop cancels it when it gives the
// handlers still running up (StopDelivering), and a handler then hands its message back.
func (m HeldMessage) Context() context.Context {
	return m.delivery.handling
}

// Ack acknowledges the message, and hands it back for redelivery when the acknowledgement does not
// reach the server (intakeDelivery.acknowledge).
func (m HeldMessage) Ack() {
	m.delivery.acknowledge(m.Msg)
}

// HandBack hands the message back for redelivery at once, while the hold still covers this
// delivery, and leaves it with the durable for the next holder's take-over once it does not
// (intakeDelivery.handBack).
func (m HeldMessage) HandBack() {
	m.delivery.handBack(m.Msg)
}

// HandBackAfter hands the message back for redelivery in after: the handler's own answer that the
// message is to come round again later, not now (legion.Intake's owed read). It is published only
// while this delivery may still answer the durable, and withheld otherwise, the message left with
// the durable for the next holder's take-over (intakeDelivery.handBackOwn).
func (m HeldMessage) HandBackAfter(after time.Duration) {
	m.delivery.handBackOwn(m.Msg, after)
}

// intakeHoldLost is the line of an intake whose durable's hold is no longer its own, so it stops
// delivering (run).
const intakeHoldLost = "dispatch delivery: the intake's hold is lost; it stops delivering"

// run delivers from the durable while this intake holds its hold, and stands by for it otherwise,
// until ctx ends; then it stops delivering and releases the hold (stop), under the one stop
// deadline armed at ctx's end (stopContext), so a take-over under way at that moment spends that
// budget rather than starting one of its own. Each durableBindInterval while it delivers nothing,
// and ready reports true, it tries for the hold and the durable (attempt); each intakeHoldRenew
// while it delivers, it checks the durable still delivers to it (check); and the moment its hold
// is lost it stops delivering. wake, when not nil, answers a channel the loop waits on beside its
// tickers and takes again each time it fires -- the delivery intake's settings cache storing
// settings, which is what makes its first attempt ready (Intake.Run).
func (d *heldDurable) run(ctx context.Context, wake func() <-chan struct{}, ready func() bool) {
	// Armed before the first attempt, so a take-over under way as ctx ends -- a standby's poll,
	// whose JetStream calls take their own time -- spends the stop's budget rather than starting
	// it late.
	stopping, release := stopContext(ctx)
	defer release()
	defer d.stop(stopping)
	binds := time.NewTicker(durableBindInterval)
	defer binds.Stop()
	checks := time.NewTicker(intakeHoldRenew)
	defer checks.Stop()
	attempt := func() {
		if d.subject == "" && ready() {
			d.attempt(ctx)
		}
	}
	// Taken before each attempt, which reads what the cache holds, so settings stored after that
	// read wake the loop.
	var changed <-chan struct{}
	if wake != nil {
		changed = wake()
	}
	attempt()
	for {
		// lost stays nil, and so never ready, while this intake holds no hold.
		var lost <-chan struct{}
		if d.hold != nil {
			lost = d.hold.Lost()
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
			changed = wake()
			attempt()
		case <-binds.C:
			attempt()
		case <-checks.C:
			d.check(ctx)
		case <-lost:
			slog.Warn(intakeHoldLost, "name", d.name)
			// The hold lasts intakeHoldMargin longer on the database's clock, so a handler that
			// finishes within it finished while the hold still held; the rest are given up,
			// since another process may take the hold from that moment.
			d.stopDeliveringWithin(intakeHoldMargin)
			d.hold, d.subject = nil, ""
		}
	}
}

// stopDeliveringWithin stops this intake's subscription, giving the handlers still running budget
// to finish and be acknowledged before it gives them up (StopDelivering). It runs on a context of
// its own, so a stop of the process does not cut the hand-back short.
func (d *heldDurable) stopDeliveringWithin(budget time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	d.StopDelivering(ctx)
}

// attempt tries to make this intake the one delivering from the durable: it takes the hold when
// no other process holds it live, then takes the durable over under it (takeOver). A durable
// bound to a subject no hold recorded is read first outside the consumer lease, so a holder that
// waits for a process of the build before holds to let go takes no lease on every poll.
func (d *heldDurable) attempt(ctx context.Context) {
	if d.hold == nil && !d.takeHold(ctx) {
		return
	}
	info, err := d.lookupDurable(ctx)
	if err == nil && d.boundElsewhere(info) {
		return
	}
	// The take-over asked for mutual exclusion and nothing else, and no tick is due (the zero time).
	duty.Exclusive(ctx, d.leases, consumerLease(d.name), time.Time{}, 0, d.takeOver)
}

// lookupDurable reads the durable's state from the server, bounded by intakeHoldRenew.
func (d *heldDurable) lookupDurable(ctx context.Context) (*natsgo.ConsumerInfo, error) {
	lookup, cancel := context.WithTimeout(ctx, intakeHoldRenew)
	defer cancel()
	return d.nats.JS().ConsumerInfo(bus.Stream, d.name, natsgo.Context(lookup))
}

// heldElsewhere is the standby line of an intake whose durable's hold another process holds live.
const heldElsewhere = "dispatch delivery: NATS consumer held by another process; standing by"

// takeHold takes the durable's hold when no holder holds it live, keeping the token its row
// records, and reports whether this intake holds it now. duty.TryHold records its own token as it
// takes a lapsed hold, so the lapsed hold's token is read first (duty.HoldToken).
func (d *heldDurable) takeHold(ctx context.Context) bool {
	holdName := intakeHoldName(d.name)
	bounded, cancel := context.WithTimeout(ctx, intakeHoldRenew)
	defer cancel()
	token, live, err := duty.HoldToken(bounded, d.leases, holdName)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			slog.Error("dispatch delivery: read the intake's hold", "name", d.name, "hold", holdName, "error", err)
		}
		return false
	case live:
		d.throttle.reportStandby(d.name, heldElsewhere)
		return false
	}
	hold, err := duty.TryHold(bounded, d.leases, holdName, d.holder, duty.HoldOptions{
		TTL: intakeHoldTTL, Renew: intakeHoldRenew, Margin: intakeHoldMargin, Token: token,
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("dispatch delivery: take the intake's hold", "name", d.name, "hold", holdName, "error", err)
		}
		return false
	}
	if hold == nil {
		d.throttle.reportStandby(d.name, heldElsewhere)
		return false
	}
	d.hold, d.recorded = hold, parseIntakeHoldToken(token)
	slog.Info("dispatch delivery: took the intake's hold", "name", d.name, "hold", holdName)
	return true
}

// boundElsewhere reports whether info is a durable bound to a subject the hold does not record: a
// process of the build before holds, which this intake never takes the durable from. It says so
// at INFO, throttled.
func (d *heldDurable) boundElsewhere(info *natsgo.ConsumerInfo) bool {
	if !info.PushBound || info.Config.DeliverSubject == d.recorded.subject {
		return false
	}
	d.throttle.reportStandby(info.Config.Durable, "dispatch delivery: NATS consumer bound to a process that holds no hold; standing by until it lets go")
	return true
}

// holds reports whether this intake may still act under its hold: the hold's loss deadline has
// not passed (duty.Hold.Deadline).
func (d *heldDurable) holds() bool {
	return d.hold != nil && time.Now().Before(d.hold.Deadline())
}

// takeOver makes the durable deliver to this intake, under the hold and the durable's consumer
// lease. A durable that does not exist is created with this build's config, at the start a
// take-over before this one recorded when it deleted the durable and did not create it again
// (intakeHoldToken). A durable bound to a subject no hold recorded is left as it is
// (boundElsewhere). Any other -- unbound, or bound to the subject the hold records, which is a
// holder that died or was cut off -- is deleted and created again at its ack floor on a fresh
// deliver subject (takeOverConfig), and that start is recorded as the hold's token before the
// delete. The durable's state is read again here, under the lease, since a process of the build
// before holds can bind it between attempt's read and this one.
//
// This intake subscribes to the fresh deliver subject before it creates the durable, so the
// durable is push-bound from the moment it exists. A process of the build before holds binds by
// looking the durable up and binding it when the lookup reports it unbound; a durable created
// before its subscriber would leave a moment in which that lookup finds it unbound, and both
// processes would then be handed every message on one subject. The subscribe sends its SUB on
// the client's connection before the create's request goes out on the same connection, and the
// server handles one connection's protocol in order, so the subject has interest before the
// create is even read (TestATakeOverCreatesTheDurableAlreadyBound). Then this intake records the
// subject alone as the hold's token, still under the lease.
//
// From the delete on -- or from the lookup, when it finds no durable -- the take-over runs to its
// end inside this one lease (finishTakeOver, which says what a lease given up with the durable
// absent leaves behind).
func (d *heldDurable) takeOver(ctx context.Context) {
	js := d.nats.JS()
	wanted := d.config()
	name := wanted.Durable
	create := wanted
	deleteFirst := false
	info, err := js.ConsumerInfo(bus.Stream, name, natsgo.Context(ctx))
	switch {
	case errors.Is(err, natsgo.ErrConsumerNotFound):
		if d.recorded.start != 0 {
			create = startingAt(wanted, d.recorded.start)
		}
	case err != nil:
		slog.Error("dispatch delivery: look up NATS consumer", "name", name, "error", err)
		return
	case d.boundElsewhere(info):
		return
	default:
		create = takeOverConfig(info, wanted)
		if !d.holds() {
			return
		}
		pending := intakeHoldToken{subject: d.recorded.subject, start: create.OptStartSeq}
		if err := d.hold.SetToken(ctx, pending.String()); err != nil {
			slog.Error("dispatch delivery: record the take-over's start before deleting NATS consumer", "name", name, "start_seq", create.OptStartSeq, "error", err)
			return
		}
		d.recorded = pending
		deleteFirst = true
	}
	d.finishTakeOver(create, deleteFirst)
}

// takeOverConfig is the config a take-over recreates the durable deployed describes with: the
// deployed config when a newer build stamped it with a higher generation, this build's (wanted)
// otherwise, on a fresh deliver subject, starting at the stream sequence after the durable's ack
// floor. Starting there, the recreated durable redelivers at once every message the durable had
// not acknowledged -- what a dead holder had been handed, what a stopped one handed back, what a
// handler handed back with a delay (intakeDelivery.handBackOwn) -- and skips nothing
// (TestAnAbandonedHoldersMessagesReachTheStandbyWithinTheHoldsLapse, and
// TestATakeOverWhoseCreateFailsCreatesTheDurableAtItsFloorOnTheNextAttempt for a create retried).
// The ack floor stays below the oldest message not acknowledged, so the recreated durable also
// replays every message after that one that the durable had acknowledged. A message handed back
// with a delay holds the floor there for a bounded time: an envelope whose head row was not stored
// for headWriteMaxDeliveries deliveries, and a Legion state message that holds its project's owed
// read for about 11 minutes (legion's stateMaxHolds).
// The take-over records that start in its hold's token before it deletes the durable, and
// finishTakeOver says what a create that fails after the delete leaves behind.
// The deployed metadata keeps its own keys and drops the ones nats-server sets itself (the
// `_nats.` prefix), which the server sets again on create.
func takeOverConfig(deployed *natsgo.ConsumerInfo, wanted natsgo.ConsumerConfig) natsgo.ConsumerConfig {
	config := wanted
	if bus.MetadataGeneration(deployed.Config.Metadata, consumerGenerationKey) > consumerConfigGeneration {
		config = deployed.Config
		config.DeliverSubject = natsgo.NewInbox()
		config.OptStartTime = nil
		config.Metadata = bus.OwnMetadata(deployed.Config.Metadata, 0)
	}
	return startingAt(config, deployed.AckFloor.Stream+1)
}

// startingAt is config delivering from stream sequence start on.
func startingAt(config natsgo.ConsumerConfig, start uint64) natsgo.ConsumerConfig {
	config.DeliverPolicy = natsgo.DeliverByStartSequencePolicy
	config.OptStartSeq = start
	return config
}

// The bounds on finishTakeOver, the part of a take-over that runs after it has recorded the
// durable's start, and that must not give up the consumer lease with the durable deleted.
//
// takeOverFinishWithin bounds the whole of it, from its first step to its last try. It is under
// intakeHoldTTL - intakeHoldMargin (13 s), how long a hold lasts from its taking or its last
// renewal. A hold takeHold just took has at most 13 s left when attempt reaches these steps, less
// the lookup of the durable (lookupDurable, up to 5 s), the consumer lease (duty.Exclusive) and
// takeOver's read of the durable before them: with every renewal after the taking failing, the
// hold covers most of the 10 s of the steps, not always all of them. A take-over that runs again
// after a check found the durable delivering elsewhere (check) begins at any point in the renewal
// cycle of a hold this intake already held, so up to intakeHoldRenew (5 s) after its last renewal:
// with the renewals from there on failing, about 8 s of the hold are left against the 10 s bound.
// In both cases retryHeld stops at the hold's deadline. That exit is the safe one -- no step acts
// once another process can take the hold, and the next attempt takes the durable over again.
// takeOverFinishWithin is also inside intakeStopBudget, and so inside stopGrace (cmd/dispatch),
// since a stop that comes meanwhile waits for it to end.
//
// takeOverCallTimeout bounds each try of each step, so that one call that draws no answer -- a
// JetStream request during a leader change -- leaves room for the tries after it. A JetStream
// leader election ordinarily ends within it.
//
// A failed try waits takeOverFirstRetry before the next one, doubling after each try up to
// takeOverLastRetry: with calls that fail at once, that is eight tries within takeOverFinishWithin.
// A try the connection's grant refuses (bus.ErrPublishDenied) is not tried again: the server
// refuses it again however often it is sent, so the step gives up at once and the consumer lease
// is released, rather than held for the rest of takeOverFinishWithin.
const (
	takeOverFinishWithin = 10 * time.Second
	takeOverCallTimeout  = 3 * time.Second
	takeOverFirstRetry   = 250 * time.Millisecond
	takeOverLastRetry    = 2 * time.Second
)

// finishTakeOver makes the durable deliver to this intake at create's start, under the consumer
// lease its caller (takeOver) holds: it deletes the durable when deleteFirst is set, subscribes
// to create's deliver subject, creates the durable with create, and records the subject alone as
// the hold's token; then it runs the owner's take-over callback (bound), if it names one. Each
// step that fails is tried again (retryHeld) until it succeeds, this intake no longer holds its
// hold, the grant refuses it, or takeOverFinishWithin has passed. It runs on a context of its own
// rather than the run's, so a stop that comes while the durable is deleted does not leave it so.
// Every step is safe to try again: a delete of a durable already gone succeeds, and a create of
// a durable that a timed-out try did create, with this same config, returns that durable.
//
// A create that fails after the delete is therefore tried again inside the one consumer lease, so
// among builds that take holds such a create loses neither the durable nor its start. The lease is
// given up with the durable absent only when this process is killed between the delete and the
// create, when it loses its hold, when the grant refuses the create, or when every try fails
// within takeOverFinishWithin. The start then stays in the hold's token (intakeHoldToken), and the
// next take-over of this build -- by
// this intake, or by the process that takes the hold next -- creates the durable at that old ack
// floor rather than at the default start (deliveryConsumerConfig names none, so the stream's first
// message). A process of the build before holds reconciles the durable under the same lease and
// never reads the token: finding the durable absent, it creates it at its own default start, the
// stream's first message for the events durable and the next new message for the jobs durable
// (TestATakeOverWhoseCreateFailsCreatesTheDurableBeforeAPreviousBuildReconciles).
func (d *heldDurable) finishTakeOver(create natsgo.ConsumerConfig, deleteFirst bool) {
	js := d.nats.JS()
	finish, cancel := context.WithTimeout(context.Background(), takeOverFinishWithin)
	defer cancel()
	if deleteFirst {
		if d.retryHeld(finish, "delete NATS consumer to take it over", func(call context.Context) error {
			if err := js.DeleteConsumer(bus.Stream, d.name, natsgo.Context(call)); err != nil && !errors.Is(err, natsgo.ErrConsumerNotFound) {
				return err
			}
			return nil
		}) != nil {
			return
		}
	}
	if d.retryHeld(finish, "bind NATS consumer", func(context.Context) error {
		_, err := d.subscribe(create.DeliverSubject)
		return err
	}, "subject", create.FilterSubject) != nil {
		return
	}
	if d.retryHeld(finish, "create NATS consumer", func(call context.Context) error {
		_, err := createDurable(js, &create, natsgo.Context(call))
		return err
	}, "subject", create.FilterSubject, "start_seq", create.OptStartSeq) != nil {
		d.stopDeliveringWithin(intakeHoldRenew)
		return
	}
	bound := intakeHoldToken{subject: create.DeliverSubject}
	if d.retryHeld(finish, "record the intake's deliver subject", func(call context.Context) error {
		return d.hold.SetToken(call, bound.String())
	}) != nil {
		// The durable delivers here, but the hold does not say so: the next holder would take
		// the binding for a process that holds nothing and wait for it. Stop, and take the
		// durable over again on the next attempt.
		d.stopDeliveringWithin(intakeHoldRenew)
		return
	}
	d.recorded, d.subject = bound, create.DeliverSubject
	slog.Info("dispatch delivery: NATS consumer bound", "name", d.name, "subject", create.FilterSubject,
		"start_seq", create.OptStartSeq, "generation", bus.MetadataGeneration(create.Metadata, consumerGenerationKey))
	if tracing() {
		d.mu.Lock()
		delivery := d.delivering
		d.mu.Unlock()
		slog.Debug(traceTakeOverBound, d.traceAttrs("delivery", tracePointer(delivery),
			"deliver_subject", create.DeliverSubject, "start_seq", create.OptStartSeq)...)
	}
	if d.bound != nil {
		d.bound()
	}
	if d.boundHook != nil {
		d.boundHook()
	}
}

// errHoldNotHeld is retryHeld finding that this intake no longer holds its hold.
var errHoldNotHeld = errors.New("the intake no longer holds its hold")

// retryHeld calls step until it succeeds, while this intake holds its hold and finish has not
// ended, waiting takeOverFirstRetry after the first failed try and twice as long after each one
// after it, up to takeOverLastRetry. Each try runs on a context that ends takeOverCallTimeout
// after it starts, or at the hold's loss deadline if that comes first, so no step acts once
// another process can hold the hold. A try the connection's grant refuses (bus.ErrPublishDenied)
// ends the step at once, since no later try gets past it. A failed try is logged at WARN as doing
// what names, and the failure it gives up on at ERROR by failedStep, so a caller writes no step's
// text a second time and keeps only what it does next. It returns nil once step succeeds, and
// otherwise the last try's error, or errHoldNotHeld. attributes are the caller's own on that ERROR
// line.
func (d *heldDurable) retryHeld(finish context.Context, doing string, step func(context.Context) error, attributes ...any) error {
	wait := takeOverFirstRetry
	for {
		if !d.holds() {
			return d.failedStep(doing, errHoldNotHeld, attributes)
		}
		deadline := time.Now().Add(takeOverCallTimeout)
		if lost := d.hold.Deadline(); lost.Before(deadline) {
			deadline = lost
		}
		call, cancel := context.WithDeadline(finish, deadline)
		err := step(call)
		cancel()
		if err == nil {
			return nil
		}
		if errors.Is(err, bus.ErrPublishDenied) {
			return d.failedStep(doing, err, attributes)
		}
		slog.Warn("dispatch delivery: take-over step failed; trying it again", "name", d.name, "step", doing, "retry_in", wait, "error", err)
		timer := time.NewTimer(wait)
		select {
		case <-finish.Done():
			timer.Stop()
			return d.failedStep(doing, err, attributes)
		case <-timer.C:
		}
		wait = min(2*wait, takeOverLastRetry)
	}
}

// failedStep logs the take-over step retryHeld gave up on at ERROR, as "dispatch delivery: " and
// doing, with the durable's name, the caller's own attributes and err, and answers err.
func (d *heldDurable) failedStep(doing string, err error, attributes []any) error {
	slog.Error("dispatch delivery: "+doing, append(append([]any{"name", d.name}, attributes...), "error", err)...)
	return err
}

// createDurable creates the durable config describes on the notification stream. A var so a test
// can make a take-over's create fail after its delete.
var createDurable = func(js natsgo.JetStreamContext, config *natsgo.ConsumerConfig, opts ...natsgo.JSOpt) (*natsgo.ConsumerInfo, error) {
	return js.AddConsumer(bus.Stream, config, opts...)
}

// durableDeliversElsewhere is the line of an intake whose durable no longer delivers to its
// deliver subject, so it stops delivering (check).
const durableDeliversElsewhere = "dispatch delivery: the NATS consumer no longer delivers to this intake; it stops delivering"

// check stops delivering when the durable no longer delivers to this intake's deliver subject:
// something deleted it, or replaced it with one on another subject. The intake keeps its hold, and
// its next attempt takes the durable over again, or waits for a process that binds without a
// hold. A lookup that fails says nothing, and changes nothing.
func (d *heldDurable) check(ctx context.Context) {
	if d.subject == "" {
		return
	}
	info, err := d.lookupDurable(ctx)
	switch {
	case errors.Is(err, natsgo.ErrConsumerNotFound):
	case err != nil:
		return
	case info.Config.DeliverSubject == d.subject:
		return
	}
	slog.Warn(durableDeliversElsewhere, "name", d.name)
	// The durable's messages are the next holder's from here, so a handler still running after
	// the renewal interval is given up rather than left to write what that holder writes too.
	d.stopDeliveringWithin(intakeHoldRenew)
	d.subject = ""
}

// stop is the end of run: it stops delivering under ctx, the run's stop deadline, and releases the
// hold once every hand-back the delivery took has settled and every one it published reached the
// server (handBacksReached), so a standby takes the durable over at once and is redelivered
// whatever this intake handed back. Before that release, it seals the delivery
// (intakeDelivery.seal): a hand-back that begins after the seal is withheld, so none is
// published after the release. A hold whose token still records a take-over's start
// (intakeHoldToken) is lapsed instead, which frees it as Release does and keeps the row, so the
// next holder reads that start. The release runs on a context of its own: it is one statement, and
// it must reach the database even when the stop's budget has run out.
//
// A hand-back still pending when the stop's context ends does not keep Run past that context, and
// does not give the hold up either. A hand-back is a publish at the message's reply subject, which
// names the durable rather than the consumer: one that reached the server after a standby had taken
// the durable over, and recreated it under the same name, would move that message's pending entry on
// the standby's consumer and have it delivered a second time. So the hold keeps renewing on a
// goroutine of its own (finalizeHandBacks) until that hand-back lands and a flush confirms it, and
// is released only then. A flush on another socket confirms nothing: Close writes nats.go's
// outbound buffer to the socket before it closes it, and a reconnect keeps the connection and dials
// a new socket, so a hand-back written to the old socket can still be on its way to the server. So
// when that socket goes first, the finalizer leaves the hold to lapse at its expiry rather than
// releasing it. The finalizer starts here, and the caller that closes the client waits up to
// intakeFinalizerConfirmWait from that start before the close (awaitConfirming).
func (d *heldDurable) stop(ctx context.Context) {
	d.StopDelivering(ctx)
	if d.hold == nil {
		return
	}
	hold, lapse := d.hold, d.recorded.start != 0
	d.hold, d.subject = nil, ""
	d.mu.Lock()
	delivery := d.delivering
	d.mu.Unlock()
	if d.handBacksReached(ctx, delivery, hold) {
		d.releaseHold(hold, lapse)
		return
	}
	select {
	case <-hold.Lost():
		// Another holder may act already, and there is no hold left to keep.
	default:
		slog.Warn(intakeHoldFinalizing, "name", d.name)
		d.finalizers.Add(1)
		d.confirming.Add(1)
		confirming := sync.OnceFunc(d.confirming.Done)
		window := time.AfterFunc(time.Duration(intakeFinalizerConfirmWait.Load()), confirming)
		go func() {
			defer d.finalizers.Done()
			defer confirming()
			defer window.Stop()
			d.finalizeHandBacks(delivery, hold, lapse)
		}()
	}
}

// intakeHoldFinalizing is the line of a stop whose hand-back had not reached NATS by its deadline,
// so its hold stays renewing until it does (stop).
const intakeHoldFinalizing = "dispatch delivery: a hand-back has not reached NATS by the stop's deadline, so the intake's hold stays renewing until it does"

// finalizeHandBacks keeps hold renewing while a hand-back of the stopped delivery is pending, and
// releases it once that hand-back has landed and a flush of the connection, begun after its publish
// ended, has answered (handBacksSettled). It waits until no hand-back of the delivery is taken and
// not settled, and then seals the delivery (intakeDelivery.seal); when a hand-back was taken during
// that wait, the seal fails and it waits again. It flushes again each intakeFlushWait until a flush
// answers. A flush on another socket confirms nothing: once the socket a published hand-back went
// out on has gone before a flush on it confirmed the hand-back -- the client's connection has
// closed, the client has replaced it, or it has reconnected -- that hand-back may still be on its
// way to the server, so the finalizer leaves the hold to lapse at its row's expiry
// (duty.Hold.Abandon), which no standby can take before. With nothing published left unconfirmed,
// nothing more can reach the server, and it releases the hold. It runs on a goroutine of its own
// that nothing in Run waits for, so it waits for nats.go's lock with no bound and makes one flush
// at a time. The caller that closes the client waits for it to confirm with the connection open,
// for up to intakeFinalizerConfirmWait from its start (awaitConfirming), and waits for its end
// after the close, before it returns to a caller that can close the database pool
// (waitFinalizers). A lost hold ends it: another holder may then act, as after any loss of the
// hold. Its trace line gives its outcome.
func (d *heldDurable) finalizeHandBacks(delivery *intakeDelivery, hold *duty.Hold, lapse bool) {
	if tracing() {
		slog.Debug(traceFinalizerStarts, "name", d.name, "delivery", tracePointer(delivery), "client", tracePointer(d.nats), "lapse", lapse)
	}
	for {
		select {
		case <-delivery.handBacksLanded():
		case <-hold.Lost():
			d.traceFinalizerEnd(delivery, finalizerLost, "the hold was lost before the seal")
			return
		}
		if delivery.seal() {
			break
		}
	}
	retry := time.NewTicker(intakeFlushWait)
	defer retry.Stop()
	for {
		switch d.handBacksSettled(delivery) {
		case handBacksConfirmed:
			if before := finalizerBeforeRelease.Load(); before != nil {
				(*before)()
			}
			select {
			case <-hold.Lost():
				d.traceFinalizerEnd(delivery, finalizerLost, "the hold was lost before the release")
			default:
				d.releaseHold(hold, lapse)
				d.traceFinalizerEnd(delivery, finalizerReleased, "no published hand-back is unconfirmed")
			}
			return
		case handBacksInDoubt:
			select {
			case <-hold.Lost():
				d.traceFinalizerEnd(delivery, finalizerLost, "the hold was lost before the finalizer left it to lapse")
			default:
				hold.Abandon()
				slog.Warn(intakeHoldLeftToLapse, "name", d.name)
				d.traceFinalizerEnd(delivery, finalizerLapse, "the connection closed with a published hand-back that no flush confirmed")
			}
			return
		}
		select {
		case <-hold.Lost():
			d.traceFinalizerEnd(delivery, finalizerLost, "the hold was lost before a flush confirmed the hand-backs")
			return
		case <-retry.C:
		}
	}
}

// intakeHoldLeftToLapse is the line of a finalizer that leaves its hold to lapse at its expiry,
// because the socket a hand-back it published went out on has gone before a flush confirmed it
// (finalizeHandBacks). The standby takes the durable over only after that expiry.
const intakeHoldLeftToLapse = "dispatch delivery: the socket a hand-back went out on has gone before a flush confirmed it reached NATS, so the intake's hold is left to lapse at its expiry"

// The outcomes a finalizer's trace line gives (traceFinalizerEnd).
const (
	// finalizerReleased is a finalizer that released its hold (releaseHold): a flush confirmed
	// every hand-back the delivery published, or none was left unconfirmed.
	finalizerReleased = "released"
	// finalizerLapse is a finalizer that left its hold to lapse at its row's expiry
	// (duty.Hold.Abandon): the connection closed with a published hand-back that no flush
	// confirmed.
	finalizerLapse = "lapse"
	// finalizerLost is a finalizer whose hold was lost before it could do either.
	finalizerLost = "lost"
)

// traceFinalizerEnd is the trace line of a finalizer's end (finalizeHandBacks), with its outcome
// and the reason for it.
func (d *heldDurable) traceFinalizerEnd(delivery *intakeDelivery, outcome, reason string) {
	if tracing() {
		slog.Debug(traceFinalizerEnds, "name", d.name, "delivery", tracePointer(delivery), "client", tracePointer(d.nats),
			"outcome", outcome, "reason", reason)
	}
}

// finalizerBeforeRelease is nil outside the shutdown row. It holds a finalizer whose hand-backs a
// flush confirmed before it releases, so the row sees that stopIntakes waits for it.
var finalizerBeforeRelease atomic.Pointer[func()]

// awaitConfirming waits until each hand-back finalizer of this durable has ended, or has had
// intakeFinalizerConfirmWait from its start to confirm its hand-backs with the connection open. Its
// caller then closes the durable's NATS client (stopDurables).
func (d *heldDurable) awaitConfirming() { d.confirming.Wait() }

// waitFinalizers waits for the hand-back finalizers of this durable to end once its NATS client
// has closed. A finalizer then confirms nothing more: it releases its hold through the still-open
// database pool, or leaves it to lapse, before its caller closes that pool (StartIntakes).
func (d *heldDurable) waitFinalizers() { d.finalizers.Wait() }

// releaseHold releases hold, or lapses it when the next holder must read a take-over's token. Its
// trace line gives which of the two it did, and the error.
func (d *heldDurable) releaseHold(hold *duty.Hold, lapse bool) {
	release := hold.Release
	if lapse {
		release = hold.Lapse
	}
	releasing, cancel := context.WithTimeout(context.Background(), intakeHoldRenew)
	defer cancel()
	err := release(releasing)
	if err != nil {
		slog.Error("dispatch delivery: release the intake's hold", "name", d.name, "error", err)
	}
	if tracing() {
		slog.Debug(traceHoldReleased, d.traceAttrs("lapse", lapse, "error", err)...)
	}
}

// handBacksReached seals delivery (intakeDelivery.seal) once no hand-back of it is taken and not
// settled, and reports whether every hand-back it published has then reached the server. While a
// hand-back is taken and not settled, the seal fails, and it waits for that hand-back until ctx ends
// or hold is lost, when it reports false; once the hand-back settles, it tries to seal again, so a
// hand-back taken during the wait is waited for in turn. When the stop returns, a hand-back can
// still be taken and not settled: a publish NATS had not taken when the stop went on without it
// (intakeDelivery.publishHandBack), or the hand-back of the callback that nats.go runs for a
// message the client had buffered, after the drain loop gave up and unsubscribed. Once the delivery
// is sealed with a hand-back published, the connection is flushed, a round trip to the server, so
// the hand-backs nats.go had queued have reached it once the flush answers (flushHandBacks). A
// delivery sealed with no hand-back published reports true at once, as does no delivery.
func (d *heldDurable) handBacksReached(ctx context.Context, delivery *intakeDelivery, hold *duty.Hold) bool {
	if delivery == nil {
		return true
	}
	hooked := false
	waiting := func() {
		if hooked {
			return
		}
		hooked = true
		if hook := handBacksWaiting.Load(); hook != nil {
			(*hook)()
		}
	}
	for !delivery.seal() {
		waiting()
		landed := delivery.handBacksLanded()
		select {
		case <-landed:
		case <-hold.Lost():
		case <-ctx.Done():
		}
		select {
		case <-landed:
		default:
			return false
		}
	}
	if !delivery.handedBack() {
		return true
	}
	waiting()
	return d.flushHandBacks(delivery)
}

// handBacksWaiting is nil outside the release row. It runs once in a stop, before the stop releases
// its hold (handBacksReached): the first time the stop waits for a hand-back its delivery took and
// has not settled, or else before the flush when its delivery published a hand-back. So the row
// sees that the stop waits for its hand-backs with its hold live.
var handBacksWaiting atomic.Pointer[func()]

// flushHandBacks is whether handBacksSettled confirms delivery's hand-backs within intakeFlushWait,
// on a bound of its own as the release that follows it is: both are part of what follows the
// stop's deadline (intakeStopBudget), and a healthy link answers the flush in a round trip. A flush
// that has not answered by then is false.
func (d *heldDurable) flushHandBacks(delivery *intakeDelivery) bool {
	flushing, cancel := context.WithTimeout(context.Background(), intakeFlushWait)
	defer cancel()
	state, _, _ := bus.Bounded(flushing, func() (handBacksState, error) { return d.handBacksSettled(delivery), nil })
	return state == handBacksConfirmed
}

// handBacksState is what handBacksSettled reads of the hand-backs a sealed delivery published.
type handBacksState int

const (
	// handBacksUnconfirmed is a published hand-back that no flush has confirmed yet, on a
	// connection that is still open: a later flush can confirm it.
	handBacksUnconfirmed handBacksState = iota
	// handBacksConfirmed is no hand-back that can reach the server later: the hold may be released.
	handBacksConfirmed
	// handBacksInDoubt is a published hand-back that no flush confirmed, on a socket that has
	// gone: the client's connection has closed, the client has replaced it, or it has reconnected
	// in place. That hand-back may still be on its way to the server, and no flush can tell any
	// more.
	handBacksInDoubt
)

// settledByFlush is the reason handBacksSettled's trace line gives for the answer of its flush.
const settledByFlush = "flush"

// handBacksSettled reads whether a hand-back of delivery that nats.go had queued can reach the
// server later. A published hand-back has reached it once a flush on the socket it ran on, begun
// after the hand-back's publish ended, has answered: the server handles one connection's protocol
// in order, so its answer to the flush comes after the hand-back. So it flushes, within
// intakeFlushWait, while a publish that ended is not confirmed yet (intakeDelivery.publishRecord),
// and confirms every publish that had ended before the flush began once the flush answers
// (handBacksConfirmed). With nothing published left unconfirmed, nothing can reach the server later
// either (handBacksConfirmed).
//
// A flush on another socket confirms nothing (handBacksInDoubt): nats.go's Close writes its
// outbound buffer to the socket before it closes it, and a reconnect in place keeps the
// *natsgo.Conn and dials a new socket, so a hand-back written to the old socket may still be on its
// way to the server, in the kernel's send buffer and on the network. So it answers
// handBacksInDoubt when the unconfirmed publishes ran on more than one socket, on a connection the
// client has replaced, on a connection that has closed, or at another reconnect count than the
// connection's now, read before the flush; and when that count moved while the flush ran, since
// its PING can then have gone out on the new socket. A flush that fails is handBacksUnconfirmed,
// unless the connection closed under it: then it reads the state again at once, which answers
// what the next retry would. Its trace line gives the answer and its reason, with the delivery's
// record.
func (d *heldDurable) handBacksSettled(delivery *intakeDelivery) handBacksState {
	for {
		conn := d.nats.Connection()
		publishes := delivery.publishRecord()
		switch {
		case !publishes.unconfirmed():
			d.traceSettled(conn, handBacksConfirmed, "no published hand-back is unconfirmed", publishes, nil)
			return handBacksConfirmed
		case publishes.onMany:
			d.traceSettled(conn, handBacksInDoubt, "the unconfirmed hand-backs ran on more than one socket", publishes, nil)
			return handBacksInDoubt
		case conn == nil || publishes.on != conn:
			d.traceSettled(conn, handBacksInDoubt, "the unconfirmed hand-backs ran on a connection the client has replaced", publishes, nil)
			return handBacksInDoubt
		case conn.IsClosed():
			d.traceSettled(conn, handBacksInDoubt, "the connection has closed with a published hand-back unconfirmed", publishes, nil)
			return handBacksInDoubt
		case conn.Stats().Reconnects != publishes.reconnects:
			d.traceSettled(conn, handBacksInDoubt, "the connection has reconnected since the unconfirmed hand-backs ran on it", publishes, nil)
			return handBacksInDoubt
		}
		err := conn.FlushTimeout(intakeFlushWait)
		switch {
		case err != nil && conn.IsClosed():
			d.traceSettled(conn, handBacksUnconfirmed, settledByFlush, publishes, err)
			continue
		case err != nil:
			d.traceSettled(conn, handBacksUnconfirmed, settledByFlush, publishes, err)
			return handBacksUnconfirmed
		case conn.Stats().Reconnects != publishes.reconnects:
			d.traceSettled(conn, handBacksInDoubt, "the connection reconnected while the flush ran", publishes, nil)
			return handBacksInDoubt
		}
		delivery.confirmFlushed(publishes.ended)
		d.traceSettled(conn, handBacksConfirmed, settledByFlush, publishes, nil)
		return handBacksConfirmed
	}
}

// traceSettled is the trace line of handBacksSettled's answer, state, for reason, on conn, with the
// delivery's record as it read it before any flush: the publishes that had ended, those a flush
// had confirmed, and the connection and reconnect count the others ran on (published_on,
// published_reconnects), or whether they ran on more than one (published_on_many).
func (d *heldDurable) traceSettled(conn *natsgo.Conn, state handBacksState, reason string, publishes handBackPublishes, err error) {
	if tracing() {
		slog.Debug(traceHandBacksSettled, d.traceAttrs("conn", tracePointer(conn),
			"settled", state == handBacksConfirmed, "in_doubt", state == handBacksInDoubt, "reason", reason,
			"ended", publishes.ended, "confirmed", publishes.confirmed, "published_on", tracePointer(publishes.on),
			"published_reconnects", publishes.reconnects, "published_on_many", publishes.onMany, "error", err)...)
	}
}

// traceAttrs are the attributes of a trace line about this durable that come first: its name and
// its bus client, then attrs.
func (d *heldDurable) traceAttrs(attrs ...any) []any {
	return append([]any{"name", d.name, "client", tracePointer(d.nats)}, attrs...)
}

// intakeFlushWait bounds each flush that confirms a stop's hand-backs reached the server before its
// hold is released (handBacksSettled).
const intakeFlushWait = time.Second

// intakeFinalizerConfirmWait is how long a hand-back finalizer has, from its start, to confirm its
// hand-backs with the intake's connection open, before the stop of the process closes that
// connection (awaitConfirming, stopDurables). It counts from the finalizer's own start, not from the
// call that closes, so a caller that comes late -- the shutdown waits for the document service
// before it stops the intakes, and stops the Legion state intake after the GitHub intakes -- waits
// no longer than each finalizer's own bound. It is 5 s outside tests, read as each finalizer
// starts; a test may widen it, as a row whose own steps can outlast 5 s under the race detector
// does when it does not test a stop that never confirms.
//
// A finalizer starts at the stop's deadline, intakeStopBudget (15 s) after the signal, or up to
// intakeFlushWait (1 s) after it, since the stop's last flush (flushHandBacks) can begin just
// before the deadline. Its hand-back lands once nats.go lets its connection lock go; one flush then
// confirms it in a round trip, at most intakeFlushWait, tried again each intakeFlushWait. So within
// 5 s, a hand-back that lands up to about 4 s after the finalizer's start still has a flush
// answered on the open connection. After the close, the finalizer either leaves its hold to lapse,
// with no statement (duty.Hold.Abandon), at once when the close ends the flush it is in and
// otherwise at its next flush retry, or releases it, a statement bounded by intakeHoldRenew (5 s).
// So a finalizer whose client closes at this bound ends by 26 s after the signal: 15 s + 1 s + 5 s
// + 5 s, 1 s after the latest end of the document service's budget (25 s, stopGrace less its 5 s
// reserve) and 4 s before the kill at stopGrace (30 s, in cmd/dispatch). A longer bound would move
// that end further into the reserve that the shutdown keeps for what follows the intakes: the
// deregistration and the close of the database pool. A shorter one gives a link that heals after
// the deadline less time to confirm, and so lets the hold lapse, which costs the standby up to
// intakeHoldTTL plus durableBindInterval before it takes the durable over, rather than a release's
// moment. The client's close takes nats.go's connection lock at once unless a frozen write holds
// it, up to IntakeWriteDeadline (5 s), or a reconnect attempt through a frozen link does, up to the
// bus's connect timeout (5 s); then the finalizer leaves the hold to lapse at once, or releases it
// when nothing it published is left unconfirmed, and the standby takes the durable over at most
// 22 s after the close began (IntakeWriteDeadline).
var intakeFinalizerConfirmWait atomic.Int64

func init() { intakeFinalizerConfirmWait.Store(int64(5 * time.Second)) }
