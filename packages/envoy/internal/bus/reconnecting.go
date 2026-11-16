package bus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// ErrReconnecting is wrapped by the error of a send made while nats.go connects or reconnects the
// connection the send would go on. Nothing of such a send was sent, and nothing of it is sent
// later: the client keeps no reconnect buffer (connect.go, options).
//
// Two errors wrap it. A send that waited for the connection (ensureConnWithContext for a publish
// or a request, Bounded for a bucket call a caller bounds by its deadline) and whose ctx ended
// first is `bus: NATS is reconnecting: context deadline exceeded`, which wraps ctx's error too.
// A send that did not wait, or that the connection dropped under after its wait, is
// `bus: NATS is reconnecting` (Reconnecting).
var ErrReconnecting = errors.New("NATS is reconnecting")

// reconnectingSend is a send nats.go refused while it connects or reconnects the connection:
// nats.ErrReconnectBufExceeded, which it answers to every publish then, since the client keeps no
// reconnect buffer, or nats.ErrConnectionReconnecting. Its text is ErrReconnecting's alone. The
// text of nats.ErrReconnectBufExceeded is `nats: outbound buffer limit exceeded`, and a reader
// of an answer or a log line reads that as a client that sent too much, where the bus is only
// reconnecting. It still unwraps to nats.go's error, so a caller that reads that error, such as
// kvwatch's lostConnection or cistore's kvErrorLasts, reads it as before.
type reconnectingSend struct {
	refusal error
}

func (e reconnectingSend) Error() string   { return "bus: " + ErrReconnecting.Error() }
func (e reconnectingSend) Unwrap() []error { return []error{ErrReconnecting, e.refusal} }

// Reconnecting names err as the ErrReconnecting it is when it is nats.go's refusal of a send while
// it connects or reconnects the connection (reconnectingSend), and returns any other err as it is.
// Every send of this package names its error with it. A caller that sends through a nats.go
// handle of its own, such as the listener's reads and writes of its durable consumer, names its
// error with it too.
func Reconnecting(err error) error {
	if err == nil {
		return nil
	}
	var named reconnectingSend
	if errors.As(err, &named) {
		return err
	}
	if errors.Is(err, nats.ErrReconnectBufExceeded) || errors.Is(err, nats.ErrConnectionReconnecting) {
		return reconnectingSend{refusal: err}
	}
	return err
}

// reconnectWaitEnded is the error of a wait for a connection nats.go is reconnecting whose ctx
// ended first. It wraps ErrReconnecting and ctx's error.
func reconnectWaitEnded(ctx context.Context) error {
	return fmt.Errorf("bus: %w: %w", ErrReconnecting, ctx.Err())
}

// awaitConnected returns once conn can take a send. A nil conn, a connected one, a draining one
// and a closed one return at once: a send on a closed connection fails on its own, and nothing
// waits it out. A connection nats.go is connecting or reconnecting is waited for, until it
// connects or closes, or until ctx ends, which returns reconnectWaitEnded. The wait reads the
// connection again at each of its status events (nats.Conn.StatusChanged) and at least every
// reconnectRecheck, as ensureConnWithContext's wait does, rather than polling.
//
// It is ensureConnWithContext's wait for one connection alone. It is for a send on a handle that
// holds only its connection, a KV bucket's (Bounded): such a handle cannot dial a replacement,
// and the client that owns the connection does that.
func awaitConnected(ctx context.Context, conn *nats.Conn) error {
	for conn != nil && connecting(conn) {
		// The listener is registered before the status is read again, so a connect that lands in
		// between is either read here or sent to the listener.
		changed := conn.StatusChanged(nats.CONNECTED, nats.CLOSED)
		if !connecting(conn) {
			conn.RemoveStatusListener(changed)
			return nil
		}
		recheck := time.NewTimer(reconnectRecheck)
		select {
		case <-ctx.Done():
			recheck.Stop()
			conn.RemoveStatusListener(changed)
			return reconnectWaitEnded(ctx)
		case <-changed:
		case <-recheck.C:
		}
		recheck.Stop()
		conn.RemoveStatusListener(changed)
	}
	return nil
}

// connecting reports whether nats.go is connecting conn for the first time or reconnecting it: the
// states in which it refuses every publish (nats.ErrReconnectBufExceeded).
func connecting(conn *nats.Conn) bool {
	switch conn.Status() {
	case nats.CONNECTED, nats.DRAINING_SUBS, nats.DRAINING_PUBS, nats.CLOSED:
		return false
	default:
		return true
	}
}
