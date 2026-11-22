package delivery

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
)

// stop is the end of run: it stops delivering under ctx, the run's stop deadline, and then reads
// whether every hand-back the delivery took has settled and every one it published has reached the
// server (handBacksReached). The delivery is sealed before the hold is given up
// (intakeDelivery.seal): a hand-back that begins after the seal is withheld, so none is published
// after the hold is released. What the stop does with the hold depends on the answer
// (handBacksState):
//
//   - handBacksConfirmed: it releases the hold (releaseHold), so a standby takes the durable over at
//     once and is redelivered whatever this intake handed back. A hold whose token still records a
//     take-over's start (intakeHoldToken) is lapsed instead, which frees it as Release does and
//     keeps the row, so the next holder reads that start. The release runs on a context of its
//     own: it is one statement, and it must reach the database even when the stop's budget has run
//     out.
//   - handBacksInDoubt: a published hand-back went out on a socket that has gone before a flush on
//     it confirmed the hand-back, so it may still be on its way to the server, and no later flush
//     can tell. It leaves the hold to lapse at its expiry at once (abandonHold), as a finalizer
//     that reads the same does, and starts no finalizer.
//   - handBacksUnconfirmed: a hand-back is still pending when the stop's context ends, or no flush
//     that can still confirm it has answered by then: the stop runs one flush at a time and waits
//     for its answer up to intakeFlushWait at a time (handBacksReached). That does not keep Run
//     past the context, but for that last wait, and does not give the hold up either: the hold
//     keeps renewing on a goroutine of its own (finalizeHandBacks) until that hand-back lands and a
//     flush confirms it, and is released only then. It logs intakeHoldFinalizing. The finalizer
//     starts here, and the caller that closes the client waits up to intakeFinalizerConfirmWait
//     from that start before the close (stopDurables).
//
// The hold is kept while a hand-back may still reach the server because a hand-back is a publish
// at the message's reply subject, which names the durable rather than the consumer: one that
// reached the server after a standby had taken the durable over, and recreated it under the same
// name, would move that message's pending entry on the standby's consumer and have it delivered a
// second time. A flush on another socket confirms nothing: Close writes nats.go's outbound buffer
// to the socket before it closes it, and a reconnect keeps the connection and dials a new socket,
// so a hand-back written to the old socket can still be on its way to the server. A hold already
// lost when the stop would leave it to lapse or keep it renewing is left as it is: another holder
// may act already.
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
	switch d.handBacksReached(ctx, delivery, hold) {
	case handBacksConfirmed:
		d.releaseHold(hold, lapse)
	case handBacksInDoubt:
		d.abandonHold(hold)
	case handBacksUnconfirmed:
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
}

// intakeHoldFinalizing is the line of a stop whose hand-back is unconfirmed at its deadline
// (handBacksUnconfirmed): still pending, or with no answer by then from any flush that can still
// confirm it. A flush that does not answer is followed by another until the deadline, one at a
// time (handBacksReached), so a stop logs this line only once its deadline has passed. Its hold
// stays renewing until a flush confirms the hand-back (stop, finalizeHandBacks). A stop whose
// hand-back is in doubt logs intakeHoldLeftToLapse instead.
const intakeHoldFinalizing = "dispatch delivery: a hand-back has not reached NATS by the stop's deadline, so the intake's hold stays renewing until it does"

// finalizeHandBacks keeps hold renewing while a hand-back of the stopped delivery is pending, and
// releases it once that hand-back has landed and a flush of the connection, begun after its publish
// ended, has answered (handBacksSettled). It seals the delivery once no hand-back of it is taken
// and not settled (sealLanded). It flushes again each intakeFlushWait until a flush answers. A
// flush on another socket confirms nothing: once the socket a published hand-back went out on has
// gone before a flush on it confirmed the hand-back -- the client's connection has closed, the
// client has replaced it, or it has reconnected -- that hand-back may still be on its way to the
// server, so the finalizer leaves the hold to lapse at its row's expiry (abandonHold), which no
// standby can take before. With nothing published left unconfirmed, nothing more can reach the
// server, and it releases the hold. It runs on a goroutine of its own that nothing in Run waits
// for, so it waits for nats.go's lock with no bound and makes one flush at a time. The caller that
// closes the client waits for it to confirm with the connection open, for up to
// intakeFinalizerConfirmWait from its start, and waits for its end after the close, before it
// returns to a caller that can close the database pool (stopDurables). A lost hold ends it: another
// holder may then act, as after any loss of the hold. Its trace line gives its outcome.
func (d *heldDurable) finalizeHandBacks(delivery *intakeDelivery, hold *duty.Hold, lapse bool) {
	if tracing() {
		slog.Debug(traceFinalizerStarts, "name", d.name, "delivery", tracePointer(delivery), "client", tracePointer(d.nats), "lapse", lapse)
	}
	if !sealLanded(delivery, hold, nil, nil) {
		d.traceFinalizerEnd(delivery, finalizerLost, "the hold was lost before the seal")
		return
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
			if d.abandonHold(hold) {
				d.traceFinalizerEnd(delivery, finalizerLapse, "the connection closed with a published hand-back that no flush confirmed")
			} else {
				d.traceFinalizerEnd(delivery, finalizerLost, "the hold was lost before the finalizer left it to lapse")
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

// intakeHoldLeftToLapse is the line of a stop or a finalizer that leaves its hold to lapse at its
// expiry (abandonHold), because the socket a hand-back it published went out on has gone before a
// flush confirmed it (handBacksInDoubt). The standby takes the durable over only after that expiry.
const intakeHoldLeftToLapse = "dispatch delivery: the socket a hand-back went out on has gone before a flush confirmed it reached NATS, so the intake's hold is left to lapse at its expiry"

// abandonHold leaves hold to lapse at its row's expiry (duty.Hold.Abandon), with
// intakeHoldLeftToLapse, and reports true: the hold stops renewing and its row stays, so no standby
// takes the durable over before that expiry. A hold already lost is left as it is, and it reports
// false: another holder may act then.
func (d *heldDurable) abandonHold(hold *duty.Hold) bool {
	select {
	case <-hold.Lost():
		return false
	default:
	}
	hold.Abandon()
	slog.Warn(intakeHoldLeftToLapse, "name", d.name)
	return true
}

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
// flush confirmed before it releases, so the row sees that stopDurables waits for it.
var finalizerBeforeRelease atomic.Pointer[func()]

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

// handBacksReached reads whether every hand-back of delivery has reached the server, for the stop
// (handBacksState). It seals delivery once no hand-back of it is taken and not settled
// (sealLanded), waiting for those hand-backs until ctx ends or hold is lost, and answers
// handBacksUnconfirmed when the seal has not come by then. When the stop returns, a hand-back can
// still be taken and not settled: a publish NATS had not taken when the stop went on without it
// (intakeDelivery.publishHandBack), or the hand-back of the callback that nats.go runs for a
// message the client had buffered, after the drain loop gave up and unsubscribed. Once the delivery
// is sealed, no publish of it ends any more, so its record (handBackRecord.publishRecord) can only
// gain confirmations. With a published hand-back unconfirmed, the connection is flushed, a round
// trip to the server, so the hand-backs nats.go had queued have reached it once a flush answers
// (flushHandBacks). It runs one flush at a time, as the finalizer does, and waits for its answer up
// to intakeFlushWait at a time. A confirmed or in-doubt answer is its answer at once. After an
// unconfirmed answer it starts the next flush at the next tick of intakeFlushWait. A flush that
// has not answered within the wait stays in flight, and no other starts while it does, so a stop
// that waits behind nats.go's connection lock has one flush waiting for it. It makes at least one
// flush once the delivery is sealed, even when the seal comes after ctx has ended, as it does for
// the hand-back of a handler the stop gave up at its deadline, and starts no other flush once ctx
// has ended: it answers handBacksUnconfirmed after the wait in which it finds ctx ended, and leaves
// a flush still in flight to end on its own. So a link that answers later than one flush's bound,
// but before the deadline, still lets the stop answer before its deadline, and the stop's last
// wait for a flush begins before ctx ends, or at the seal when that comes later, and lasts at most
// intakeFlushWait (intakeFinalizerConfirmWait). It answers handBacksUnconfirmed at once when hold
// is lost. With no published hand-back unconfirmed, the record is read at once, with no flush and
// no bound (handBacksSettled). The hook handBacksWaiting, when a test sets one, runs each time the
// stop waits for a hand-back to settle, and before each flush. A stop with no delivery reads
// handBacksConfirmed.
func (d *heldDurable) handBacksReached(ctx context.Context, delivery *intakeDelivery, hold *duty.Hold) handBacksState {
	if delivery == nil {
		return handBacksConfirmed
	}
	var waiting func()
	if hook := handBacksWaiting.Load(); hook != nil {
		waiting = *hook
	}
	if !sealLanded(delivery, hold, ctx.Done(), waiting) {
		return handBacksUnconfirmed
	}
	if !delivery.handBacks.publishRecord().unconfirmed() {
		return d.handBacksSettled(delivery)
	}
	retry := time.NewTicker(intakeFlushWait)
	defer retry.Stop()
	var answer <-chan handBacksState
	for {
		if answer == nil {
			if waiting != nil {
				waiting()
			}
			answer = d.flushHandBacks(delivery)
		}
		select {
		case state := <-answer:
			if state != handBacksUnconfirmed {
				return state
			}
			answer = nil
		case <-time.After(intakeFlushWait):
		case <-hold.Lost():
			return handBacksUnconfirmed
		}
		if ctx.Err() != nil {
			return handBacksUnconfirmed
		}
		select {
		case <-retry.C:
		case <-ctx.Done():
		case <-hold.Lost():
		}
		select {
		case <-ctx.Done():
			return handBacksUnconfirmed
		case <-hold.Lost():
			return handBacksUnconfirmed
		default:
		}
	}
}

// sealLanded seals delivery (intakeDelivery.seal) once no hand-back of it is taken and not
// settled, and reports whether it did: the stop's and the finalizer's one wait before they give
// the hold up (handBacksReached, finalizeHandBacks). While a hand-back is taken and not settled,
// the seal fails, and it waits for that hand-back to settle (handBacksLanded) and then tries to
// seal again, so a hand-back taken during the wait is waited for in turn. It gives up, and reports
// false, once hold is lost or done ends before the hand-backs it waits for have settled; a nil done
// never ends. waiting, when not nil, runs before each wait.
func sealLanded(delivery *intakeDelivery, hold *duty.Hold, done <-chan struct{}, waiting func()) bool {
	for !delivery.seal() {
		if waiting != nil {
			waiting()
		}
		landed := delivery.handBacks.handBacksLanded()
		select {
		case <-landed:
		case <-hold.Lost():
		case <-done:
		}
		select {
		case <-landed:
		default:
			return false
		}
	}
	return true
}

// handBacksWaiting is nil outside the release rows. A stop runs it each time it waits for a
// hand-back its delivery took and has not settled, and before each flush when its delivery has a
// published hand-back unconfirmed (handBacksReached), so a row sees that the stop waits for its
// hand-backs with its hold live, or acts at that moment. A row that sets it runs its own steps
// once (sync.Once), since the stop can run it more than once.
var handBacksWaiting atomic.Pointer[func()]

// flushHandBacks starts one read of handBacksSettled for delivery on a goroutine of its own, and
// returns the channel its answer arrives on, which holds that one answer. The read waits for
// nats.go's connection lock with no bound of its own, and its flush then waits up to
// intakeFlushWait for the server's answer (handBackPublishes.settle). A stop that returns before
// the read answers leaves it to end on its own (handBacksReached): its answer goes to the channel,
// which nothing reads, and the stop's finalizer can still confirm the hand-backs.
func (d *heldDurable) flushHandBacks(delivery *intakeDelivery) <-chan handBacksState {
	answer := make(chan handBacksState, 1)
	go func() { answer <- d.handBacksSettled(delivery) }()
	return answer
}

// handBacksSettled reads whether a hand-back of delivery that nats.go had queued can reach the
// server later: it reads the delivery's record (handBackRecord.publishRecord) and settles it on the
// client's connection now (handBackPublishes.settle), which flushes that connection, within
// intakeFlushWait, while a publish that ended is not confirmed yet. Once settle answers
// handBacksConfirmed, it confirms every publish that had ended when it read the record. A flush
// that fails because the connection closed under it is read again at once, which answers what the
// next retry would. Its trace line gives each answer and its reason, with the delivery's record.
func (d *heldDurable) handBacksSettled(delivery *intakeDelivery) handBacksState {
	for {
		conn := d.nats.Connection()
		publishes := delivery.handBacks.publishRecord()
		state, reason, err := publishes.settle(conn)
		d.traceSettled(conn, state, reason, publishes, err)
		switch {
		case state == handBacksConfirmed:
			delivery.handBacks.confirmFlushed(publishes.ended)
		case err != nil && conn.IsClosed():
			continue
		}
		return state
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

// stopDurables stops durables whose Runs have returned (HeldDurable.Stop, and StartIntakes' stop
// once its Runs have returned). First it waits until each durable's hand-back finalizers have
// ended, or have each had intakeFinalizerConfirmWait from its start to confirm its hand-backs with
// the connection open (confirming). Then it closes each one's NATS client (closeNATS), and then it
// waits for each one's finalizers to end (waitFinalizers). After the close, a finalizer with a
// published hand-back that no flush confirmed leaves its hold to lapse at its expiry, and one with
// none releases its hold through the still-open database pool, before this returns.
func stopDurables(durables ...*heldDurable) {
	for _, durable := range durables {
		durable.confirming.Wait()
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

// intakeFlushWait bounds each flush that confirms a stop's hand-backs reached the server before its
// hold is released (handBacksSettled).
const intakeFlushWait = time.Second

// intakeFinalizerConfirmWait is how long a hand-back finalizer has, from its start, to confirm its
// hand-backs with the intake's connection open, before the stop of the process closes that
// connection (stopDurables). It counts from the finalizer's own start, not from the call that
// closes, so a caller that comes late -- the shutdown waits for the document service before it
// stops the intakes, and stops the Legion state intake after the GitHub intakes -- waits no longer
// than each finalizer's own bound. It is 5 s outside tests, read as each finalizer starts; a test
// may widen it, as a row whose own steps can outlast 5 s under the race detector does when it does
// not test a stop that never confirms.
//
// The bounds of an intake's shutdown are worked out here, and only here; every other statement of
// them names this.
//
// A finalizer starts at the stop's deadline, intakeStopBudget (15 s) after the signal, or up to
// intakeFlushWait (1 s) after it, since the stop's last wait for a flush (handBacksReached) can
// begin just before the deadline. Its hand-back lands once nats.go lets its connection lock go; one
// flush then confirms it in a round trip, at most intakeFlushWait, tried again each
// intakeFlushWait. So within 5 s, a hand-back that lands up to about 4 s after the finalizer's
// start still has a flush answered on the open connection.
//
// Then the client closes (stopDurables). The close waits for nats.go's connection lock, while the
// finalizer keeps the hold renewing. On a link that moves, it takes the lock at once. A write to a
// frozen link holds the lock up to IntakeWriteDeadline (5 s). A reconnect attempt holds it up to
// 2 * bus.ConnectTimeout (10 s): nats.go v1.55.0 takes the lock at the attempt's start and keeps it
// through the dial, which bus.ConnectTimeout bounds, and through the handshake, whose deadline it
// sets to bus.ConnectTimeout again once the dial has connected (doReconnect, nats.go:3554-3581;
// processConnectInit, nats.go:2944). So the close waits at most 2 * bus.ConnectTimeout, 10 s, for
// the lock.
//
// Once the close has the lock, the finalizer leaves the hold to lapse, with no statement
// (duty.Hold.Abandon), at once when the close ends the flush it is in and otherwise at its next
// flush retry. Or, when nothing it published is left unconfirmed, it releases the hold, a statement
// bounded by intakeHoldRenew (5 s). A hold left to lapse expires at most intakeHoldTTL (15 s) after
// its last renewal, and the standby tries for it every durableBindInterval (2 s). So the standby
// takes the durable over at most 10 s + 15 s + 2 s = 27 s after the close began, and at once after
// a release.
//
// Against the kill at stopGrace (30 s after the signal, in cmd/dispatch): when the close takes the
// lock at once, a finalizer that waits out this bound and then releases its hold ends by
// 15 s + 1 s + 5 s + 5 s = 26 s after the signal. That is 1 s after the latest end of the document
// service's budget (25 s, stopGrace less its 5 s reserve) and 4 s before the kill. A close that
// waits its full 10 s for the lock, with a release after it, can pass the kill. The kill then cuts
// the release short: it ends the renewal with the process, and the hold lapses at its expiry, as a
// hold left to lapse does, so the standby still takes the durable over within 27 s of the close's
// start.
//
// A longer bound would move the stop's end further into the reserve that the shutdown keeps for
// what follows the intakes: the deregistration and the close of the database pool. A shorter one
// gives a link that heals after the deadline less time to confirm, and so lets the hold lapse, which
// costs the standby up to intakeHoldTTL plus durableBindInterval before it takes the durable over,
// rather than a release's moment.
var intakeFinalizerConfirmWait atomic.Int64

func init() { intakeFinalizerConfirmWait.Store(int64(5 * time.Second)) }
