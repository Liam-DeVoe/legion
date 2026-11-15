// How a request tells that the connection it was sent on can no longer answer it.

package bus

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go"
)

// ErrRequestLostWithConnection ends a request whose connection was lost before its answer came:
// the connection the request went out on was replaced by another the client dialled, was closed,
// or reconnected in place (connWatch.lostRequest). The server drops a request with the
// connection it came on, and nats.go fails none that is waiting for its answer when its
// connection drops: it fails the pending flushes (processOpErr) but leaves its request map and
// every sync subscription waiting, so such a request would otherwise wait out its whole deadline
// for an answer nothing will send.
//
// It is no answer of the server's. The request may have reached the server and been acted on
// before its answer was lost, so a caller that judges whether a call changed anything counts it
// as unanswered, and a caller that retries sends it again on the connection that came back, at
// once rather than after a backoff: the failure says nothing about the request itself.
// Client.requestConfirmed returns it (every session consumer call, the stream read behind them
// and the stored-message reads), and so do Client.RequestCore (the send route's connection probe),
// a KV call BoundedOn waits for and a JetStream request RequestOnConnection makes (the stream's
// ensure on each connection event, and Dispatch's delivery intake taking its durable over).
var ErrRequestLostWithConnection = errors.New("bus: the request was lost with its NATS connection")

// connWatch follows the connection one request, or one pull's requests (SessionPull), went out on,
// to tell a request the server may still answer from one a connection event dropped.
type connWatch struct {
	client *Client
	// conn is the connection the request goes out on, the client's connection when the watch
	// began (watchConnection).
	conn *nats.Conn
	// reconnects is conn's reconnect count when the watch began (nats.Conn.Stats): the count
	// moves on a reconnect in place, which drops the request while the connection stays the same
	// one.
	reconnects uint64
	// changed is the client's connection-event channel (Client.ConnectionChanged), taken before
	// conn was read, so an event between the two closes it. Only the one goroutine that waits on
	// the watch reads or replaces it (lost, bound).
	changed <-chan struct{}
}

// watchConnection reads the client's connection for a request about to go out on it. The event
// channel is taken before the connection and its reconnect count are read, so an event between
// the two is seen rather than leaving the request waiting on an answer the server dropped. An
// event before the request is sent, which nats.go then sends on the connection that came back,
// ends it too: the cost is one request sent again, where the cost of missing the event is a wait
// to the request's deadline.
func (c *Client) watchConnection() connWatch {
	changed := c.ConnectionChanged()
	conn := c.Connection()
	return connWatch{client: c, conn: conn, reconnects: conn.Stats().Reconnects, changed: changed}
}

// lost reports whether a connection event of the client (ConnectionChanged) has dropped the
// watched request. An event that left the request's connection the client's current one, open,
// and with its reconnect count unmoved is none of the request's (lostRequest): it is a disconnect
// the reconnect has not followed yet, or the moment between a reconnect nats.go has installed,
// which the watch read, and the callback that reports it. The watch goes on past such an event,
// on the next channel.
func (w *connWatch) lost() bool {
	for {
		select {
		case <-w.changed:
		default:
			return false
		}
		if w.lostRequest() {
			return true
		}
		w.changed = w.client.ConnectionChanged()
	}
}

// lostRequest reports whether the watched request is lost: the connection it went out on can no
// longer answer it, because the server drops the request when that connection is replaced by
// another the client dialled, when it is closed, and when it reconnects in place, which leaves
// the connection the same one and moves its reconnect count. A disconnect alone ends no request:
// it ends at the reconnect that follows, which is the first moment the caller can send it again,
// and by then the server has dropped the old connection's request. A pull ended at the disconnect
// could have its caller publish or pull while the server still held that request, and a message
// the server delivered to it would wait out the consumer's ack wait.
func (w *connWatch) lostRequest() bool {
	return w.client.Connection() != w.conn || w.conn.IsClosed() || w.conn.Stats().Reconnects != w.reconnects
}

// bound returns ctx ended, with ErrRequestLostWithConnection as its cause (lostWithConnection),
// as soon as the watched request is lost, and the func that releases the goroutine watching for
// that, which the caller defers. A wait that takes no channel but a context - a sync
// subscription's NextMsgWithContext, a call bounded by Bounded - waits on it to end at the
// connection event rather than at ctx's deadline.
func (w connWatch) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	bounded, cancel := context.WithCancelCause(ctx)
	go func() {
		for {
			select {
			case <-bounded.Done():
				return
			case <-w.changed:
			}
			if w.lostRequest() {
				cancel(ErrRequestLostWithConnection)
				return
			}
			w.changed = w.client.ConnectionChanged()
		}
	}()
	return bounded, func() { cancel(nil) }
}

// lostWithConnection reports whether ctx, a context bound returned, ended because the request it
// watched was lost with its connection.
func lostWithConnection(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrRequestLostWithConnection)
}
