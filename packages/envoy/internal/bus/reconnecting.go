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
// first is `bus: NATS is reconnecting: context deadline exceeded`, which wraps ctx's error too. So
// is a bucket call whose ctx ended before it started while nats.go reconnects the connection
// (Bounded, endedUnsent). A send that did not wait, or that the connection dropped under after its
// wait, is `bus: NATS is reconnecting` (Reconnecting).
var ErrReconnecting = errors.New("NATS is reconnecting")

// reconnectingSend is a send nats.go refused while it connects or reconnects the connection:
// nats.ErrReconnectBufExceeded, which it answers to every publish then, since the client keeps no
// reconnect buffer, nats.ErrConnectionReconnecting, or any error a JetStream request returns while
// it does (namedOn). Its text is `bus: NATS is reconnecting`,
// ErrReconnecting's under this package's prefix, so a caller that wraps it adds no `bus:` of its
// own. The text of nats.ErrReconnectBufExceeded is `nats: outbound buffer limit exceeded`, and a
// reader of an answer or a log line reads that as a client that sent too much, where the bus is
// only reconnecting. It still unwraps to nats.go's error, so a caller that reads that error, such
// as kvwatch's lostConnection or cistore's kvErrorLasts, reads it as before.
type reconnectingSend struct {
	refusal error
}

func (e reconnectingSend) Error() string   { return "bus: " + ErrReconnecting.Error() }
func (e reconnectingSend) Unwrap() []error { return []error{ErrReconnecting, e.refusal} }

// Reconnecting names err as the ErrReconnecting it is when it is nats.go's refusal of a send while
// it connects or reconnects the connection (reconnectingSend), and returns any other err as it is.
// This package calls it once for each kind of send it makes, at the one place that kind is sent:
//   - a JetStream request through a legacy JetStream context (namedRequests), which also names
//     any error a request returns while the connection reconnects, and which wraps every such
//     context the package builds and every one a caller builds through it (JetStreamOn), and
//     every one a caller hands it (namedJetStream): the client's own, which Client.JS hands its
//     callers, the stream ensure's, the subject read's, the leader read of a key
//     (KeyValue.GetFromLeader), the known-session store's lookup of its bucket
//     (internal/session), and the contexts EnsureKeyValue, OpenKeyValue and SetKeyValueMaxAge are
//     handed;
//   - a JetStream API request this package builds itself (Client.requestConfirmed), which also
//     names any error a request returns while the connection reconnects (namedOn): every request
//     SessionConsumers sends - a consumer's info, create, update and delete, the consumer list
//     and the stream read - and each stored-message read (Client.NextStoredMessage,
//     Client.LastStoredMessage); and a SessionConsumers call that waits for a stream read another
//     call started (streamRead.wait);
//   - a session consumer's pull request (SessionPull.pull) and each answer to a message it pulled
//     (sessionAck);
//   - a publish, JetStream or core, and a core request (Client.refused, window.unsent,
//     SessionConsumers.Publish);
//   - a call through a KV bucket's handles (kvAnswer, KeyValue.Status, KeyValue.ReadStreamState,
//     wrapKeyValue).
//
// A caller that sends through a nats.go handle none of these cover names its error with it too,
// such as kvwatch's delete of a watcher's consumer.
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

// namedRequests is a legacy JetStream context whose requests name their error (named): each method
// below sends one or more JetStream API requests on the context's connection, and answers an
// error it returns while nats.go connects or reconnects that connection as the ErrReconnecting it
// is. It is the one place this package names the error of such a request, so no call through the
// context names it again. Every other method is the context's own: a publish names its own error
// (Client.refused, window.unsent), and ConsumerNames answers no error.
//
// conn is the connection the context sends on, whose state named reads.
type namedRequests struct {
	nats.JetStreamContext
	conn *nats.Conn
}

// JetStreamOn is conn's legacy JetStream context, built with opts, with its requests named
// (namedRequests). Every legacy JetStream context this package builds is built here. A caller
// outside the package that sends a request through a context of its own builds the context here
// too, so the request's error names a reconnect as every other does: the known-session store's
// lookup of its bucket (internal/session openKnownBucket).
func JetStreamOn(conn *nats.Conn, opts ...nats.JSOpt) (nats.JetStreamContext, error) {
	js, err := conn.JetStream(opts...)
	if err != nil {
		return nil, err
	}
	return namedRequests{JetStreamContext: js, conn: conn}, nil
}

// namedJetStream is js, a context a caller handed this package, with its requests named
// (namedRequests); conn is the connection js sends on. A context that names them already is
// returned as it is: a namedRequests, or a context that reads the server's refusals over one
// (refusalReads), which is the shape of Client.JS's (Client.readsOwnRefusals over JetStreamOn's
// context). A refusalReads over a context that names nothing gets the names under its refusal
// reads, in the same order: so a refusal the server reports on the connection is returned as the
// server's answer (ErrPublishDenied, refusedBy) and is never named a reconnect, and a caller that
// then reads the refusals too (readsRefusals) finds them read already and adds nothing.
func namedJetStream(conn *nats.Conn, js nats.JetStreamContext) nats.JetStreamContext {
	switch wrapped := js.(type) {
	case namedRequests:
		return js
	case refusalReads:
		wrapped.JetStreamContext = namedJetStream(conn, wrapped.JetStreamContext)
		return wrapped
	}
	return namedRequests{JetStreamContext: js, conn: conn}
}

// named is err, a request's answer, named by the state of the context's connection (namedOn).
func (j namedRequests) named(err error) error { return namedOn(j.conn, err) }

// namedOn is err, the answer of a request made on conn, as the ErrReconnecting it is when the
// request was made while nats.go connects or reconnects conn: nats.go's refusal of its send
// (Reconnecting), or any error the request returns while conn reports that state. The state is
// read because nats.go answers some requests then with words of its own that say nothing true. It
// refuses a KV bucket's lookup and create before it sends anything: both first check the server's
// version, which a connection that is reconnecting does not report, and answer "nats: key-value
// requires at least server version 2.6.2". And a request sent just before the connection dropped
// waits for a reply that never comes, and answers its caller's deadline. The error nats.go gave
// stays in the chain (reconnectingSend), so a caller that reads it still finds it. An error that
// is ErrReconnecting already is returned as it is, such as a wait for the connection that ended
// first (reconnectWaitEnded).
//
// A nil conn names nats.go's refusal alone and reads no state: connecting would panic on it. The
// one caller that can pass one is streamRead.wait, which passes its client's connection
// (Client.Connection). A client the constructors return holds a connection from the constructor
// on (install), and a replacement of a closed one installs another in its place
// (ensureConnWithContext), so only a Client built as a struct literal, as a test builds one,
// holds none. Every legacy context's conn is the connection it was built on or handed with.
//
// It is the one rule every place that names a request's error follows: a legacy JetStream context
// (namedRequests), a request this package builds itself (Client.requestConfirmed), and a call
// that waits for such a request another call made (streamRead.wait).
func namedOn(conn *nats.Conn, err error) error {
	if err == nil || errors.Is(err, ErrReconnecting) || conn == nil || !connecting(conn) {
		return Reconnecting(err)
	}
	return reconnectingSend{refusal: err}
}

func (j namedRequests) AddStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	info, err := j.JetStreamContext.AddStream(cfg, opts...)
	return info, j.named(err)
}

func (j namedRequests) UpdateStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	info, err := j.JetStreamContext.UpdateStream(cfg, opts...)
	return info, j.named(err)
}

func (j namedRequests) StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	info, err := j.JetStreamContext.StreamInfo(stream, opts...)
	return info, j.named(err)
}

func (j namedRequests) PurgeStream(stream string, opts ...nats.JSOpt) error {
	return j.named(j.JetStreamContext.PurgeStream(stream, opts...))
}

func (j namedRequests) GetLastMsg(stream, subject string, opts ...nats.JSOpt) (*nats.RawStreamMsg, error) {
	message, err := j.JetStreamContext.GetLastMsg(stream, subject, opts...)
	return message, j.named(err)
}

func (j namedRequests) AddConsumer(stream string, cfg *nats.ConsumerConfig, opts ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	info, err := j.JetStreamContext.AddConsumer(stream, cfg, opts...)
	return info, j.named(err)
}

func (j namedRequests) UpdateConsumer(stream string, cfg *nats.ConsumerConfig, opts ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	info, err := j.JetStreamContext.UpdateConsumer(stream, cfg, opts...)
	return info, j.named(err)
}

func (j namedRequests) ConsumerInfo(stream, consumer string, opts ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	info, err := j.JetStreamContext.ConsumerInfo(stream, consumer, opts...)
	return info, j.named(err)
}

func (j namedRequests) DeleteConsumer(stream, consumer string, opts ...nats.JSOpt) error {
	return j.named(j.JetStreamContext.DeleteConsumer(stream, consumer, opts...))
}

// KeyValue looks the bucket up: a STREAM.INFO of its stream, after a check of the server's version.
func (j namedRequests) KeyValue(bucket string) (nats.KeyValue, error) {
	kv, err := j.JetStreamContext.KeyValue(bucket)
	return kv, j.named(err)
}

// CreateKeyValue creates the bucket: after a check of the server's version, the account's
// JetStream info, then its stream's create.
func (j namedRequests) CreateKeyValue(cfg *nats.KeyValueConfig) (nats.KeyValue, error) {
	kv, err := j.JetStreamContext.CreateKeyValue(cfg)
	return kv, j.named(err)
}

// Subscribe binds or creates the subscription's consumer: a CONSUMER.INFO, and a CONSUMER.CREATE
// when it creates one.
func (j namedRequests) Subscribe(subject string, handler nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	sub, err := j.JetStreamContext.Subscribe(subject, handler, opts...)
	return sub, j.named(err)
}

// reconnectWaitEnded is the error of a wait for a connection nats.go is reconnecting whose ctx
// ended first. It wraps ErrReconnecting and ctx's error.
func reconnectWaitEnded(ctx context.Context) error {
	return fmt.Errorf("bus: %w: %w", ErrReconnecting, ctx.Err())
}

// endedUnsent is the error of a send on conn that was not sent because ctx had ended before it
// started: while nats.go connects or reconnects conn, the error of a wait for the connection that
// ctx ended (reconnectWaitEnded), since the send could not have gone then either and its caller
// is told why; and ctx's own error otherwise. A nil conn is no connection to wait for, and gives
// ctx's own error.
func endedUnsent(ctx context.Context, conn *nats.Conn) error {
	if conn != nil && connecting(conn) {
		return reconnectWaitEnded(ctx)
	}
	return ctx.Err()
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
