package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/logging"
)

// roleForwardWaitLimit bounds the role messages the role lane holds at once: queued behind their
// holder's earlier forwards, waiting for their outcome, or held for another delivery attempt. When
// it is reached the role lane's handler waits for one to end, which only holds role messages in
// the lane's subscription, whose pending limits are far above any backlog that wait can build
// (bus.ConnectRoleLane).
const roleForwardWaitLimit = 4096

// roleHoldFirst and roleHoldMax bound the wait before a held role message's next delivery attempt
// (roleForwarder.hold): it doubles from the first to the max. The first is short because the most
// common hold is a forward whose window a busy process or a reconnect spent, which the next attempt
// usually finishes; the max keeps a listener whose NATS stays down to one attempt per held message
// every 5 s.
const (
	roleHoldFirst = 100 * time.Millisecond
	roleHoldMax   = 5 * time.Second
)

// roleHeldLine is the WARN line of a role message the lane holds for another delivery attempt,
// and roleHeldDeliveryStatus its delivery_status: a NATS step of its delivery failed and the
// listener does not know that the stream holds a copy (roleForwarder.hold).
const (
	roleHeldLine           = "listener role message held for another delivery attempt"
	roleHeldDeliveryStatus = "held"
)

// roleHoldBound is how long the lane holds one message for further attempts, counted from when the
// lane took it (roleHeld.since) by the listener's clock: the stream's duplicate window
// (contracts.DeliveryDuplicateWindow, 72 h) less the kept lane's hour (roleKeepWindow). Every
// forward of a message to its holder carries one Nats-Msg-Id, which the stream drops as a duplicate
// only inside that window, counted from when it stored the first, which is after the lane took the
// message. So a forward the stream stored while its answer was lost would be stored a second time
// by an attempt after the window. No attempt runs past this bound: the message is kept on its role's
// kept lane instead (keepHeld, under roleKeptPastHoldBound), and the kept copy's hour, and so the
// forward its handover makes, ends inside the window.
const roleHoldBound = contracts.DeliveryDuplicateWindow - roleKeepWindow

// roleHeldUnkeptLine is the ERROR line of a role message the lane stopped holding - the listener
// stopped, or the message reached roleHoldBound - and could not keep on its role's kept lane either:
// the one end of a held message that leaves no copy anywhere (roleForwarder.keepHeld).
const roleHeldUnkeptLine = "listener role message the lane stopped holding was not kept"

// roleLaneSlowConsumerInterval is how often the role lane's slow-consumer count is read for a change
// to log (watchRoleLaneSlowConsumers).
const roleLaneSlowConsumerInterval = 5 * time.Second

// roleForward is one role message the role lane resolved to its holder, every guard passed: item is
// the message as it arrived, sessionID its holder and held the attempts it has been held through.
type roleForward struct {
	item      contracts.Envelope
	sessionID string
	held      roleHeld
}

// roleHeld is one role message's place among the messages the role lane holds. attempts counts the
// delivery attempts that ended on a transport failure, a NATS step after which the listener does
// not know that the stream holds a copy; since is when the lane took the message, by the
// listener's clock (cfg.now), which roleHoldBound counts from; and slot says the message holds one
// of the forwarder's slots, which is what envoy_role_forwards_outstanding counts. The zero value
// is a message the lane just took: it takes a slot when it is queued (roleForwarder.enqueue) or
// when a failed resolution holds it (roleForwarder.hold), and keeps that slot through every
// attempt until its delivery ends, when the slot is freed exactly once (releaseHeld).
type roleHeld struct {
	attempts int
	since    time.Time
	slot     bool
}

// roleForwarder runs the role lane's forwards off its NATS handler, so a forward the stream is slow
// to acknowledge holds up no other role message. Forwards to one holder are published in the order
// the handler handed them over, by a worker per holder with forwards queued; each forward's wait
// for its outcome runs on a goroutine of its own, and the outcome becomes the forward's delivery
// status and exception (finishRoleForward). A forward, a read or a keep that fails on NATS leaves
// the message held for another delivery attempt (hold), so once the lane takes a role message the
// stream ends up holding a copy of it or the forwarder holds the message itself. Messages queued,
// waiting and held together are bounded by the forwarder's slots, roleForwardWaitLimit in the
// listener: at that bound the lane's handler waits for a slot, which leaves the role messages NATS
// delivers after it in the lane's own subscription (bus.ConnectRoleLane) and drops none.
type roleForwarder struct {
	// cfg is the delivery configuration the forwards are published (forwardRole) and reported
	// through, and a held message delivered again (deliverRoleMessage); its roleForwards is this
	// forwarder.
	cfg   listenerDeliveryHandlerConfig
	slots chan struct{}
	// pending counts the messages that hold a slot, for wait.
	pending sync.WaitGroup
	// stop is closed when the listener stops (stopHolding): no further slot is given out, a
	// message the lane takes from then on is kept at once, and a held message waiting for its
	// next attempt parks until keepHeldNow.
	stop     chan struct{}
	stopOnce sync.Once
	// keepNow is closed once the lane's own client has drained (keepHeldNow), and only then does
	// each parked held message make its keep: the keeps of up to roleForwardWaitLimit held
	// messages and the lane handler's own keeps of the messages its subscription still holds share
	// the main connection, and held messages that start first spend the window the handler's need
	// to be kept at all.
	keepNow     chan struct{}
	keepNowOnce sync.Once

	mu sync.Mutex
	// queues holds each holder's forwards not yet published, in arrival order. A holder has a key
	// exactly while its worker runs.
	queues map[string][]roleForward
}

func newRoleForwarder(cfg listenerDeliveryHandlerConfig) *roleForwarder {
	return newRoleForwarderLimited(cfg, roleForwardWaitLimit)
}

// newRoleForwarderLimited is newRoleForwarder with limit slots, the bound on the role messages it
// holds at once; a test that fills the bound passes a small one.
func newRoleForwarderLimited(cfg listenerDeliveryHandlerConfig, limit int) *roleForwarder {
	f := &roleForwarder{slots: make(chan struct{}, limit), stop: make(chan struct{}), keepNow: make(chan struct{}), queues: map[string][]roleForward{}}
	cfg.roleForwards = f
	f.cfg = cfg
	return f
}

// forwarded is the envelope published on the holder's agent subject: the message under the key
// contracts.RoleForwardDedupeKey names for this holder, which the durable path recognises and
// acknowledges. That is the role-forward dedupe key for every key but the holder's own agent
// send key, which the forward carries as it is: Dispatch's outbox keys the copy of a comment by
// the failed mention of the session that mention names, and the host of that session, which
// claimed the key when the mention's send landed, drops the copy rather than showing the comment
// twice. A copy kept for a role nobody held carries each mentioned session's own settled
// mention's key instead (contracts.Envelope.MentionDedupeKeys) - a mention delivered as much as
// one that failed, since a delivered mention's key is claimed at that session's host too - the
// holder it would reach being unknown when it was kept, and the forward to a holder that map
// names goes under that session's key alone, so whichever of the comment's mentioned sessions
// claims the role is the one whose host drops the copy. Dispatch's outbox keys its own forwards
// by the same rule
// (internal/dispatch/outbox/role.go, roleForward.forward), so the two writers of a role's kept
// lane forward one copy under one key, and so under one msgID.
func (f roleForward) forwarded() contracts.Envelope {
	forwarded := f.item
	forwarded.DedupeKey = contracts.RoleForwardDedupeKey(f.item, f.sessionID)
	return forwarded
}

// msgID is the forward's Nats-Msg-Id on subject: the name under which the stream keeps one copy
// of this message for this holder, however often it is forwarded - by a retry, or by each
// listener of the machine groups an older grant leaves the lane in, which all forward the same
// message. A forward whose envelope's own key names its event
// (contracts.DedupeKeyNamesTheUpstreamEvent, asked of the message as it arrived: the forward's
// prefix hides a webhook key's shape) is named by the forward's dedupe key and the subject, as
// the publish route's forwards are (bus.ForwardMsgID). Any other key is its sender's choice and
// two distinct envelopes can share one, so such a forward is named by its event id and the
// subject instead: a repeat of one message is still stored once, and two messages under one key
// are two messages, which the stream stores both of.
func (f roleForward) msgID(subject string) string {
	if contracts.DedupeKeyNamesTheUpstreamEvent(f.item) {
		return bus.ForwardMsgID(f.forwarded().DedupeKey, subject)
	}
	return bus.ForwardMsgID(f.item.EventID, subject)
}

// enqueue queues forward behind its holder's earlier forwards, starting the holder's worker when it
// has none. A message that holds no slot yet waits for one and records it (roleHeld.slot); a held
// one carries its own. A listener that stops while the message waits for a slot keeps it on its
// role's kept lane at once instead (keepHeld), and clears the attempt its delivery recorded, since
// no forward is made.
func (f *roleForwarder) enqueue(forward roleForward) {
	if !forward.held.slot {
		if !f.acquire() {
			f.cfg.attemptCache.Clear(forward.item.DedupeKey, forward.sessionID)
			f.keepHeld(forward.item, forward.held, roleKeptAtShutdown, nil)
			return
		}
		forward.held.slot = true
	}
	f.mu.Lock()
	queue, running := f.queues[forward.sessionID]
	f.queues[forward.sessionID] = append(queue, forward)
	f.mu.Unlock()
	if !running {
		go f.publish(forward.sessionID)
	}
}

// acquire takes a slot for one role message, waiting while every slot is taken, and reports whether
// it took one; its caller records that on the message (roleHeld.slot). It takes none once the
// listener stops (stopHolding): the role lane's handler, blocked here with every slot held, must go
// on taking the messages its subscription holds, or the lane's drain runs out and closes the
// connection under them.
func (f *roleForwarder) acquire() bool {
	select {
	case <-f.stop:
		return false
	default:
	}
	select {
	case f.slots <- struct{}{}:
		f.pending.Add(1)
		return true
	case <-f.stop:
		return false
	}
}

// stopped reports whether the listener has stopped the forwarder's attempts (stopHolding).
func (f *roleForwarder) stopped() bool {
	select {
	case <-f.stop:
		return true
	default:
		return false
	}
}

// release frees the slot of one role message that has ended.
func (f *roleForwarder) release() {
	<-f.slots
	f.pending.Done()
}

// releaseHeld frees the slot a message's delivery holds, when it holds one (roleHeld.slot).
func (f *roleForwarder) releaseHeld(held roleHeld) {
	if held.slot {
		f.release()
	}
}

// publish is sessionID's worker: it publishes the holder's queued forwards in order, hands each
// one's wait to a goroutine of its own, and ends once the queue is empty.
func (f *roleForwarder) publish(sessionID string) {
	subject := contracts.LegacyAgentSubject(sessionID)
	for {
		f.mu.Lock()
		queue := f.queues[sessionID]
		if len(queue) == 0 {
			delete(f.queues, sessionID)
			f.mu.Unlock()
			return
		}
		forward := queue[0]
		queue[0] = roleForward{}
		f.queues[sessionID] = queue[1:]
		f.mu.Unlock()

		wait, err := f.cfg.forwardRole(subject, forward.forwarded(), forward.msgID(subject), roleForwardTimeout)
		if err != nil {
			f.finish(forward, err)
			continue
		}
		go func() { f.finish(forward, wait()) }()
	}
}

// finish ends forward with its outcome: a forward the stream acknowledged, or one an error ends
// for good (roleForwardEnded), is reported and frees its slot (finishRoleForward). Any other error
// leaves the listener not knowing whether the stream holds a copy, so the message is held for
// another delivery attempt with its slot, and its attempt record is cleared so that attempt is not
// taken for a repeat of it.
func (f *roleForwarder) finish(forward roleForward, err error) {
	if err != nil && !roleForwardEnded(f.cfg.client, contracts.LegacyAgentSubject(forward.sessionID), err) {
		f.cfg.attemptCache.Clear(forward.item.DedupeKey, forward.sessionID)
		f.hold(forward.item, forward.sessionID, forward.held, err)
		return
	}
	finishRoleForward(f.cfg, forward, err)
	f.release()
}

// roleForwardEnded reports whether err ends a role forward to subject for good, under a rule no
// other attempt changes, rather than leaving unknown whether the stream will ever hold a copy. Its
// errors are every one bus.Client.StartRequestStoredTo and PendingForward.Wait return:
//   - Ended: NATS refuses the envelope or the subject however often it is sent (bus.ErrRefused: no
//     message id, a subject NATS does not accept, a message past the server's max payload), or the
//     listener's grant denies the subject (bus.ErrPublishDenied).
//   - Ended: the stream answered with a refusal of the message itself, which it gives however often
//     and whenever the message is sent (bus.StreamRefusesMessage: a message or headers past the
//     stream's maxima, a store that refuses it as too large, a sealed stream).
//   - Ended: no acknowledgement came, or the server answered no responders, for a publish the
//     server took, and a read of the deployed stream made now says it does not capture subject
//     (bus.Client.ReadDeployedStreamCaptures), so no acknowledgement will come.
//
// Every other error ends nothing, and the message is held for another attempt:
//   - the window spent before the publish, a connection not back in time, a reply subscription or
//     a flush that failed or ran out, a publish not known to have been accepted
//     (bus.ErrPublishUnconfirmed), a reply read that failed: the forward may never have left;
//   - any other answer of the stream, which can change with the stream's state: a full stream
//     (bus.ErrStreamFull) or any other store failure, an id still in process under an earlier
//     publish (bus.ErrDuplicateInProcess), insufficient resources, a clustered stream's sequence
//     mismatch or offline replica;
//   - no responders, or no acknowledgement, for a subject the stream captures or whose capture no
//     read could settle: a clustered stream changing its leader answers no responders for a subject
//     it captures, and an acknowledgement may have been lost after the stream stored the forward,
//     which the next attempt, under the same Nats-Msg-Id, finds as the duplicate it then is.
func roleForwardEnded(client *bus.Client, subject string, err error) bool {
	var answered *nats.APIError
	switch {
	case errors.Is(err, bus.ErrRefused), errors.Is(err, bus.ErrPublishDenied):
		return true
	case !errors.Is(err, bus.ErrForwardNotStored):
		return false
	case errors.As(err, &answered):
		return bus.StreamRefusesMessage(err)
	case errors.Is(err, bus.ErrPublishUnconfirmed):
		return false
	}
	captures, read := client.ReadDeployedStreamCaptures(subject, roleForwardTimeout)
	return read && !captures
}

// hold holds item for another delivery attempt after cause, a NATS step of its delivery that
// failed without the listener knowing that the stream holds a copy: a forward (finish), a read of
// the role's claim or of its holder's session, or a keep (deliverRoleMessage). The message takes a
// slot if it holds none (roleHeld.slot) and keeps it until its delivery ends, so
// envoy_role_forwards_outstanding counts it while it waits. It publishes no delivery exception,
// since the message is not given up, and logs roleHeldLine at WARN, naming holder when one was
// resolved. After a wait that starts at roleHoldFirst and doubles to roleHoldMax, the whole
// delivery runs again (deliverRoleMessage): the holder is resolved again, so a holder that changed
// gets the message and a role that lost its holder keeps it on its kept lane, and a forward goes
// out under the same Nats-Msg-Id. A message whose next attempt would start past roleHoldBound,
// counted from when the lane took it (roleHeld.since), is kept now instead (keepHeld).
//
// A listener that stops makes no further attempt: the message parks, and keeps once the lane's own
// client has drained (keepHeldNow), so the handler's keeps of the messages the subscription still
// holds go first. The wait for that is unbounded by design, and the shutdown bounds it: the drain
// waits for every message handed over, within the forward window, and then goes on (roleLane.drain).
func (f *roleForwarder) hold(item contracts.Envelope, holder string, held roleHeld, cause error) {
	if !held.slot {
		if !f.acquire() {
			f.keepHeld(item, held, roleKeptAtShutdown, cause)
			return
		}
		held.slot = true
	}
	if held.since.IsZero() {
		held.since = f.cfg.now()
	}
	held.attempts++
	if f.stopped() {
		f.keepWhenTheLaneHasDrained(item, held, cause)
		return
	}
	delay := roleHoldDelay(held.attempts)
	if !f.cfg.now().Add(delay).Before(held.since.Add(roleHoldBound)) {
		f.keepHeld(item, held, roleKeptPastHoldBound, cause)
		return
	}
	f.cfg.logger.DeliveryLog(slog.LevelWarn, roleHeldLine, holder, item.Topic, item.EventID, roleHeldDeliveryStatus,
		slog.Int("attempt", held.attempts),
		slog.Duration("next_attempt_in", delay),
		slog.String("error", cause.Error()))
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-f.stop:
		}
		// A stop that came with the timer still wins: the listener is going, so the message is
		// kept rather than tried once more.
		select {
		case <-f.stop:
			f.keepWhenTheLaneHasDrained(item, held, cause)
		default:
			deliverRoleMessage(f.cfg, item, held)
		}
	}()
}

// keepWhenTheLaneHasDrained parks a held message until the lane's own client has drained
// (keepHeldNow) and then keeps it on its role's kept lane under roleKeptAtShutdown. The handler
// keeps each message the subscription still holds while the drain runs, on the same main
// connection: a held message that kept at the stop instead would spend the window those need, and
// a listener holding its bound would lose them.
func (f *roleForwarder) keepWhenTheLaneHasDrained(item contracts.Envelope, held roleHeld, cause error) {
	go func() {
		<-f.keepNow
		f.keepHeld(item, held, roleKeptAtShutdown, cause)
	}()
}

// roleHoldDelay is the wait before a held message's next delivery attempt, after attempts holds:
// roleHoldFirst, doubled for each hold after the first, at most roleHoldMax.
func roleHoldDelay(attempts int) time.Duration {
	delay := roleHoldFirst
	for range attempts - 1 {
		if delay >= roleHoldMax {
			return roleHoldMax
		}
		delay *= 2
	}
	return min(delay, roleHoldMax)
}

// roleKeepRefusedLine is the WARN line of a role message the lane stopped holding, or took after
// the listener stopped, whose keep a rule refused (keepFailureHolds): it is given up as
// delivery_failed, with the exception that tells its sender.
const roleKeepRefusedLine = "listener role message refused by its keep"

// keepHeld is a held message's end when the lane stops holding it - the listener stops
// (roleKeptAtShutdown), or its next attempt would start past roleHoldBound (roleKeptPastHoldBound,
// with cause, the failure that would have held it once more) - and the end of a message the lane
// takes once the listener stopped: one keep on its role's kept lane (keepRoleLaneMessage), which
// the role's next live holder is handed by a handover of any listener, and the message's slot
// freed when it holds one. A keep a rule refuses (keepFailureHolds: a role token no claim can name,
// a refused envelope, a denied subject, a stream that refuses the message itself) gives the message
// up as delivery_failed, with its exception, as the lane gives up a message a rule refuses
// anywhere. A keep that fails on NATS leaves no copy anywhere and no rule to tell the sender by,
// which roleHeldUnkeptLine reports at ERROR with the event id and the reason.
func (f *roleForwarder) keepHeld(item contracts.Envelope, held roleHeld, reason roleKeepReason, cause error) {
	defer f.releaseHeld(held)
	_, role, _ := contracts.CanonicalRole(strings.TrimPrefix(item.Topic, contracts.RoleTopicPrefix))
	err := keepRoleLaneMessage(f.cfg, role, item, reason, "")
	if err == nil {
		return
	}
	attrs := []slog.Attr{slog.String("reason", string(reason)), slog.Int("attempts", held.attempts), slog.String("error", err.Error())}
	if cause != nil {
		attrs = append(attrs, slog.String("last_attempt_error", cause.Error()))
	}
	if !keepFailureHolds(err) {
		applyDeliveryOutcome(f.cfg, item, deliveryOutcome{
			metricStatus: "failed",
			log: func(logger *logging.Logger) {
				logger.DeliveryLog(slog.LevelWarn, roleKeepRefusedLine, "", item.Topic, item.EventID, "failed", attrs...)
			},
			exceptionReason: "delivery_failed",
		})
		return
	}
	f.cfg.logger.DeliveryLog(slog.LevelError, roleHeldUnkeptLine, "", item.Topic, item.EventID, "failed", attrs...)
}

// stopHolding ends the forwarder's further delivery attempts: no slot is given out from now on, a
// message the lane's handler takes from now on, or is waiting for a slot for, is kept on its
// role's kept lane at once (keepHeld), and a message the lane holds parks until keepHeldNow. It
// runs once however often it is called.
func (f *roleForwarder) stopHolding() {
	f.stopOnce.Do(func() { close(f.stop) })
}

// keepHeldNow keeps every message parked by the stop, and every one parked after it
// (keepWhenTheLaneHasDrained). The shutdown runs it once the lane's own client has drained, so the
// handler's keeps of the messages the subscription still held are already made: both sets of keeps
// go over the main connection, each within roleKeepPublishWindow, and a listener holding its bound
// has up to roleForwardWaitLimit held messages against a handful the handler still holds. It runs
// once however often it is called, and a forwarder no shutdown drains keeps nothing.
func (f *roleForwarder) keepHeldNow() {
	f.keepNowOnce.Do(func() { close(f.keepNow) })
}

// outstanding is how many role messages are queued, waiting for their outcome, or held for another
// delivery attempt.
func (f *roleForwarder) outstanding() int {
	return len(f.slots)
}

// wait waits, within ctx, for every message handed over to end. The shutdown runs it once the
// forwarder stopped holding (stopHolding), the role lane drained, and the held messages were
// released to keep (keepHeldNow), so no message takes a slot meanwhile and every held one is being
// kept, and before the main client drains, since the forwards, the keeps and their exceptions are
// published through it. It reports whether all ended.
func (f *roleForwarder) wait(ctx context.Context) bool {
	return waitWithin(ctx, &f.pending)
}

// startRoleForward is forwardRole over client: every forward is stored-only
// (bus.Client.StartRequestStoredTo), so it counts as delivered once the stream acknowledges
// storing it. No session holds a core subscription any more, so no holder answers a receipt: the
// stream's copy on the holder's agent subject is how the forward reaches it, from its session
// consumer, with acknowledgement and redelivery (roleForwardCopyDelivery). msgID is the forward's
// Nats-Msg-Id when it is not empty, which is what keeps the stream to one copy per message and
// holder however many attempts a send makes (roleForward.msgID).
func startRoleForward(client *bus.Client) func(subject string, item contracts.Envelope, msgID string, timeout time.Duration) (func() error, error) {
	return func(subject string, item contracts.Envelope, msgID string, timeout time.Duration) (func() error, error) {
		forward, err := client.StartRequestStoredTo(subject, item, msgID, timeout)
		if err != nil {
			return nil, err
		}
		return forward.Wait, nil
	}
}

// roleLane is the listener's role lane at shutdown: its own client (bus.ConnectRoleLane), the
// forwards its handler hands over, and stopWatch, which ends watchRoleLaneSlowConsumers.
type roleLane struct {
	client    *bus.Client
	forwards  *roleForwarder
	stopWatch func()
}

// roleLaneUndrainedLine is the ERROR line of role messages the lane's subscription still held when
// its drain ran out (bus.UndrainedError.Pending, the one its handler was on included): the
// connection's close drops every one the handler had not taken, and the lane held the only copy.
const roleLaneUndrainedLine = "role lane drain ended before its handler finished the role messages it holds; those not taken are lost"

// drain ends the role lane at shutdown, in the one order that keeps every message the lane holds.
// It stops the forwarder's further delivery attempts first (stopHolding), so each message the
// handler takes from then on is kept on its role's kept lane at once and with no slot to wait for:
// a handler blocked on a slot while every slot is held would otherwise take nothing more, and the
// drain would run out and close the connection under the messages its subscription holds. Then it
// drains the lane's client within timeout, so the handler takes every role message the server
// already delivered and no more, and logs at ERROR how many the drain left untaken
// (bus.UndrainedError). Only then does it release the messages the lane holds to keep
// (keepHeldNow): up to roleForwardWaitLimit keeps on the main connection, which the handler's own
// keeps would otherwise queue behind. Last it waits for the messages handed over, the forward
// window at most, since a forward published by then has its outcome within it. A message still
// waiting then is logged and left to the main client's drain, which a forward's outcome and a held
// message's keep run through.
func (l roleLane) drain(logger *logging.Logger, timeout time.Duration) {
	l.stopWatch()
	l.forwards.stopHolding()
	if err := l.client.Drain(timeout); err != nil {
		var undrained *bus.UndrainedError
		if errors.As(err, &undrained) && undrained.Pending > 0 {
			logger.Error(roleLaneUndrainedLine, slog.Int("pending", undrained.Pending), slog.String("error", err.Error()))
		} else {
			logger.Warn("role lane drain error", slog.String("error", err.Error()))
		}
	}
	l.forwards.keepHeldNow()
	ctx, cancel := context.WithTimeout(context.Background(), roleForwardTimeout)
	defer cancel()
	if !l.forwards.wait(ctx) {
		logger.Warn("role forwards still waiting at shutdown", slog.Int("outstanding", l.forwards.outstanding()))
	}
}

// watchRoleLaneSlowConsumers logs each change of the role lane's slow-consumer count until ctx ends.
// The lane's pending limits are far above any backlog its handler can build, so the count stays 0
// unless nats.go reports a slow consumer anyway; the count is also the
// envoy_role_lane_slow_consumers metric.
func watchRoleLaneSlowConsumers(ctx context.Context, lane *bus.Client, logger *logging.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var seen uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if count := lane.SlowConsumers(); count != seen {
			logger.Warn("role lane slow consumers", slog.Uint64("total", count), slog.Uint64("new", count-seen))
			seen = count
		}
	}
}
