package delivery

import (
	"errors"
	"log/slog"
	"sync"

	natsgo "github.com/nats-io/nats.go"
)

// handBackRecord is a delivery's record of its hand-backs: how many it has taken and not settled,
// whether the stop has sealed it, and the record of its hand-back publishes. mu guards every field
// of it, and nothing else. The delivery's own mu guards held, released and handBackAllowed, and no
// method holds the two locks at once. The delivery changes the record under mu as it takes,
// settles and seals hand-backs (takeHandBack, settleHandBack, seal) and as their publishes end
// (endPublish, confirmPublishes). Those steps are methods of the delivery, since their trace lines
// name the delivery and its bus client.
type handBackRecord struct {
	mu sync.Mutex
	// handingBack counts the hand-backs this delivery has taken and not settled yet
	// (takeHandBack): a hand-back is taken before any read that can wait, and settles when a
	// rule withholds it or when its publish call ends (publishHandBack). landed is closed each
	// time that count falls to zero, and made again each time the count rises from zero. The
	// hold's release waits for landed (heldDurable.handBacksReached).
	handingBack int
	landed      chan struct{}
	// publishes is the record of this delivery's hand-back publishes that have ended, and of those
	// a flush has confirmed (handBackPublishes). confirmRunning is set while a flush that confirms
	// them as they are made runs (confirmPublishes), so at most one runs at a time.
	publishes      handBackPublishes
	confirmRunning bool
	// sealed is set once the stop is about to release the hold, with no hand-back taken and not
	// settled (seal). From then on, no hand-back is taken: each is withheld (takeHandBack).
	sealed bool
}

// takeHandBack is the first step of every hand-back (handBackUnder), before any read that can
// wait. It counts msg's hand-back as taken and not settled (handingBack), and reports true, so
// the hold's release waits for that hand-back from this moment (heldDurable.handBacksReached). The
// caller settles it once (settleHandBack): at once when a rule withholds it, or when its publish
// call ends (publishHandBack). Once the delivery is sealed (seal), it takes nothing: it logs
// handBackSealed at INFO and reports false, and the message stays with the durable. Its trace line
// gives the delivery's counters after the take.
func (d *intakeDelivery) takeHandBack(msg *natsgo.Msg) bool {
	d.handBacks.mu.Lock()
	sealed := d.handBacks.sealed
	if !sealed {
		if d.handBacks.handingBack == 0 {
			d.handBacks.landed = make(chan struct{})
		}
		d.handBacks.handingBack++
	}
	taken, ended := d.handBacks.handingBack, d.handBacks.publishes.ended
	d.handBacks.mu.Unlock()
	if sealed {
		slog.Info(handBackSealed, "subject", msg.Subject)
	}
	if tracing() {
		line := traceHandBackTaken
		if sealed {
			line = traceHandBackRefused
		}
		slog.Debug(line, d.traceHandBack(msg, "taken", taken, "sealed", sealed, "ended", ended)...)
	}
	return !sealed
}

// handBackSealed is the line of a hand-back that is not taken because the delivery is sealed for
// the stop (takeHandBack): the stop is about to give the hold up, or has given it up.
const handBackSealed = "dispatch delivery: the delivery is sealed for the stop, so the message stays with the durable"

// settleHandBack settles msg's hand-back, which takeHandBack took. It closes landed when no other
// hand-back of this delivery is taken and not settled. Its trace line gives the count after it.
func (d *intakeDelivery) settleHandBack(msg *natsgo.Msg) {
	d.handBacks.mu.Lock()
	d.handBacks.handingBack--
	taken := d.handBacks.handingBack
	if taken == 0 {
		close(d.handBacks.landed)
	}
	d.handBacks.mu.Unlock()
	if tracing() {
		slog.Debug(traceHandBackSettled, d.traceHandBack(msg, "taken", taken)...)
	}
}

// handBacksLanded is closed once no hand-back of this delivery is taken and not settled
// (takeHandBack): each one withheld has settled, and the publish call of each one published has
// ended. It is already closed when none is.
func (r *handBackRecord) handBacksLanded() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handingBack == 0 {
		landed := make(chan struct{})
		close(landed)
		return landed
	}
	return r.landed
}

// seal seals this delivery, and reports true, when no hand-back of it is taken and not settled
// (takeHandBack). The stop calls it before it gives the hold up (sealLanded). From then
// on, no hand-back is taken: a hand-back taken after the stop's last read of the count could be
// published after the release, and reach a durable a standby recreated. While a hand-back is taken
// and not settled, it seals nothing and reports false. Its trace line gives the result and the
// delivery's counters.
func (d *intakeDelivery) seal() bool {
	d.handBacks.mu.Lock()
	sealed := d.handBacks.handingBack <= 0
	if sealed {
		d.handBacks.sealed = true
	}
	taken, ended := d.handBacks.handingBack, d.handBacks.publishes.ended
	d.handBacks.mu.Unlock()
	if tracing() {
		slog.Debug(traceSeal, d.traceAttrs("sealed", sealed, "taken", taken, "ended", ended)...)
	}
	return sealed
}

// endPublish counts a hand-back's publish that ended with err in the delivery's record, before
// the hand-back settles, so a stop that seals the delivery finds every publish that ended counted.
// A publish that ended with no error also starts a flush that confirms it (confirmPublishes),
// unless one is running.
//
// A publish that nats.go refused before it queued a byte (publishRefused) is not counted, and starts
// nothing: no part of it can reach the server, so it can never be in doubt, and the message stays
// with the durable for its ack wait. The others are counted, those that returned no error and those
// whose error is none of those refusals. nats.go v1.55.0 returns one error after it has queued the
// message: the error of the socket write that the publish made itself because its buffer was full
// (natsWriter.appendBufs, flush), and that write can have sent the message, or part of it. An
// error this does not name is counted too: a hand-back counted and never confirmed leaves the hold
// to lapse rather than be released, which costs the standby time and duplicates nothing.
//
// on and reconnects are the client's connection as the publish began and that connection's
// reconnect count (natsgo.Statistics.Reconnects) then. The client installs a new connection only
// once the one before it has closed, and a message of a closed connection is refused as closed, so
// a publish that is counted ran on on; a reconnect in place keeps on and dials a new socket, which
// the count tells apart (handBackPublishes.end).
func (d *intakeDelivery) endPublish(err error, on *natsgo.Conn, reconnects uint64) {
	if publishRefused(err) {
		return
	}
	d.handBacks.mu.Lock()
	d.handBacks.publishes.end(on, reconnects)
	confirm := err == nil && !d.handBacks.confirmRunning
	d.handBacks.confirmRunning = d.handBacks.confirmRunning || confirm
	d.handBacks.mu.Unlock()
	if confirm {
		go d.confirmPublishes()
	}
}

// publishRefusals are the errors that nats.go v1.55.0 returns for a hand-back's publish
// (natsgo.Msg.NakWithDelay) before it queues a byte of it. (*Msg).ackReply (js.go) returns
// ErrMsgNotBound, ErrMsgNoReply, ErrMsgAlreadyAckd, ErrCantAckIfConsumerAckNone and
// ErrContextAndTimeout before it publishes. (*Conn).publish (nats.go) returns ErrInvalidConnection,
// ErrBadSubject, ErrHeadersNotSupported, ErrConnectionClosed, ErrConnectionDraining, ErrMaxPayload
// and ErrReconnectBufExceeded before it appends the message to its buffer. It returns
// ErrReconnectBufExceeded for every publish while the connection reconnects, since the bus keeps no
// reconnect buffer (internal/bus, ReconnectBufSize -1).
var publishRefusals = []error{
	natsgo.ErrMsgNotBound, natsgo.ErrMsgNoReply, natsgo.ErrMsgAlreadyAckd, natsgo.ErrCantAckIfConsumerAckNone,
	natsgo.ErrContextAndTimeout, natsgo.ErrInvalidConnection, natsgo.ErrBadSubject, natsgo.ErrHeadersNotSupported,
	natsgo.ErrConnectionClosed, natsgo.ErrConnectionDraining, natsgo.ErrMaxPayload, natsgo.ErrReconnectBufExceeded,
}

// publishRefused reports whether err is one of publishRefusals: nats.go refused the publish
// before it queued a byte of it.
func publishRefused(err error) bool {
	if err == nil {
		return false
	}
	for _, refusal := range publishRefusals {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// confirmPublishes confirms the hand-back publishes of this delivery as they are made, so a
// delivery that hands back in its routine work, as the Legion state intake does, does not carry
// them unconfirmed into a stop that comes after a reconnect. endPublish starts it after a publish
// that ended with no error, unless one is running; it runs on a goroutine of its own that nothing
// waits for. Each round reads the record and settles it on the client's connection
// (handBackPublishes.settle), with d.handBacks.mu not held: the rule the stop reads its hand-backs
// by, which flushes that connection while the unconfirmed publishes can still be confirmed. Once
// settle answers handBacksConfirmed, the round confirms the publishes that had ended when it read
// the record. A publish that ends during a round is covered by the next round. It returns once a
// round finds nothing new that ended since the round before, or once settle answers anything but
// handBacksConfirmed. A publish in doubt stays so, and the stop's own read finds it so
// (heldDurable.handBacksSettled); one whose flush did not answer is confirmed by a round that a
// later publish starts, or by the stop's flush.
func (d *intakeDelivery) confirmPublishes() {
	flushed := 0
	for {
		d.handBacks.mu.Lock()
		publishes := d.handBacks.publishes
		if publishes.ended == flushed {
			d.handBacks.confirmRunning = false
			d.handBacks.mu.Unlock()
			return
		}
		d.handBacks.mu.Unlock()
		flushed = publishes.ended
		conn := d.nats.Connection()
		state, reason, _ := publishes.settle(conn)
		if state != handBacksConfirmed {
			d.handBacks.mu.Lock()
			d.handBacks.confirmRunning = false
			d.handBacks.mu.Unlock()
			return
		}
		d.handBacks.confirmFlushed(publishes.ended)
		if tracing() && reason == settledByFlush {
			slog.Debug(traceHandBacksConfirmed, d.traceAttrs("conn", tracePointer(conn), "ended", publishes.ended)...)
		}
	}
}

// settle is the one rule that says whether the publishes of p that have ended can still reach the
// server later (handBacksState), for the stop (heldDurable.handBacksSettled) and for the
// confirmation as they are made (confirmPublishes). current is the client's connection now. Its
// caller confirms the publishes that had ended (confirm) when it answers handBacksConfirmed.
//
// A published hand-back has reached the server once a flush on the socket it ran on, begun after
// the hand-back's publish ended, has answered: the server handles one connection's protocol in
// order, so its answer to the flush comes after the hand-back. So with nothing unconfirmed it
// answers handBacksConfirmed at once, and otherwise it flushes current, within intakeFlushWait, and
// answers handBacksConfirmed when that flush answers on the socket the publishes ran on.
//
// A flush on another socket confirms nothing (handBacksInDoubt): nats.go's Close writes its
// outbound buffer to the socket before it closes it, and a reconnect in place keeps the
// *natsgo.Conn and dials a new socket, so a hand-back written to the old socket may still be on its
// way to the server, in the kernel's send buffer and on the network. So, before any flush, it
// answers handBacksInDoubt when the unconfirmed publishes ran on more than one socket, on a
// connection the client has replaced (current is another, or none), on a connection that has
// closed, or at another reconnect count than current's now. After the flush, it answers
// handBacksInDoubt when that count moved while the flush ran, since the flush's PING can then have
// gone out on the new socket. A flush that fails is handBacksUnconfirmed. With the state, it
// answers the reason for it, settledByFlush for the flush's own answer, and the error of a flush
// that failed.
func (p handBackPublishes) settle(current *natsgo.Conn) (handBacksState, string, error) {
	switch {
	case !p.unconfirmed():
		return handBacksConfirmed, "no published hand-back is unconfirmed", nil
	case p.onMany:
		return handBacksInDoubt, "the unconfirmed hand-backs ran on more than one socket", nil
	case current == nil || p.on != current:
		return handBacksInDoubt, "the unconfirmed hand-backs ran on a connection the client has replaced", nil
	case current.IsClosed():
		return handBacksInDoubt, "the connection has closed with a published hand-back unconfirmed", nil
	case current.Stats().Reconnects != p.reconnects:
		return handBacksInDoubt, "the connection has reconnected since the unconfirmed hand-backs ran on it", nil
	}
	if err := current.FlushTimeout(intakeFlushWait); err != nil {
		return handBacksUnconfirmed, settledByFlush, err
	}
	if current.Stats().Reconnects != p.reconnects {
		return handBacksInDoubt, "the connection reconnected while the flush ran", nil
	}
	return handBacksConfirmed, settledByFlush, nil
}

// handBackPublishes is the record of a delivery's hand-back publishes: how many have ended
// (endPublish) and how many of those a flush has confirmed (confirm). While ended is above
// confirmed, a published hand-back may still be on its way to the server. on and reconnects are
// the connection, and its reconnect count, that those unconfirmed publishes ran on: only a flush on
// that socket, a flush of on that answers with on's count unchanged, can confirm them. onMany is
// set when they ran on more than one connection or reconnect count, so one of them ran on a socket
// that has gone.
type handBackPublishes struct {
	ended, confirmed int
	on               *natsgo.Conn
	reconnects       uint64
	onMany           bool
}

// end counts one publish that ran on on at reconnect count reconnects: with nothing unconfirmed,
// it records on and reconnects, and otherwise it sets onMany when they are not the ones recorded.
func (p *handBackPublishes) end(on *natsgo.Conn, reconnects uint64) {
	switch {
	case !p.unconfirmed():
		p.on, p.reconnects, p.onMany = on, reconnects, false
	case on != p.on || reconnects != p.reconnects:
		p.onMany = true
	}
	p.ended++
}

// unconfirmed reports whether a publish that has ended is not confirmed yet.
func (p handBackPublishes) unconfirmed() bool { return p.ended > p.confirmed }

// confirm records that a flush on the socket the publishes ran on, begun when ended publishes had
// ended, has answered: each of those publishes has reached the server. Once that is every publish
// that has ended, it clears on, reconnects and onMany.
func (p *handBackPublishes) confirm(ended int) {
	p.confirmed = max(p.confirmed, ended)
	if !p.unconfirmed() {
		p.on, p.reconnects, p.onMany = nil, 0, false
	}
}

// publishRecord reads this delivery's record of its hand-back publishes (handBackPublishes).
func (r *handBackRecord) publishRecord() handBackPublishes {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.publishes
}

// confirmFlushed records that a flush on the socket the publishes ran on, begun when ended
// publishes had ended (publishRecord), has answered (handBackPublishes.confirm).
func (r *handBackRecord) confirmFlushed(ended int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishes.confirm(ended)
}

// handBacksState is what a read of a delivery's record says of the hand-backs it published
// (handBackPublishes.settle), and what a stop does with its hold for it (stop).
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

// settledByFlush is the reason handBackPublishes.settle gives for the answer of its flush.
const settledByFlush = "flush"
