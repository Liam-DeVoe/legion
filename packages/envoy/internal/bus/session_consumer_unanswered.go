package bus

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nats-io/nats.go"
)

// A create or an update of a session consumer carries the filter the consumer is to hold, and its
// caller can stop waiting for the answer while the server still holds the request: its context
// ends first, or the flush behind its publish cannot confirm it. The server applies such a write
// when it gets to it, and nats-server v2.15.1 gives no order between it and a later call of the
// same connection: every JetStream API request goes to a pool of worker goroutines, up to 16, fed
// from two queues, with the info requests taken only when the other queue is empty
// (server/jetstream_api.go v2.15.1, apiDispatch and processJSAPIRoutedRequests, lines 1011-1092).
// So a consumer info sent after the write can be answered before the write is applied, and an
// update sent after it can be applied before it. A caller that judged the consumer by such an
// info, or wrote it again, can see its filter right and leave; the write it gave up on then lands,
// and the filter it carries - a plan nothing stored, a row the bucket has replaced - stands with
// nothing to read the consumer again. The server holds no conditional consumer update a caller
// could make that write lose to.
//
// So the write's subscription outlives its caller (UnansweredWrite): the one who stopped waiting
// hands it to whoever judges the consumer afterwards (OnUnansweredWrite), which waits for the
// answer and judges the consumer again once the write may have landed (cmd/listener's
// sessionDelivery.unansweredWrite). The answer comes once the server has applied the write or
// refused it, so a read made after it sees what the write did.

// UnansweredWriteEnd is how the wait for an unanswered write's answer ended (UnansweredWrite.Wait).
type UnansweredWriteEnd int

const (
	// WriteApplied is the server's answer that it applied the write: the consumer carries what
	// the write carried, as of that answer.
	WriteApplied UnansweredWriteEnd = iota
	// WriteRefused is the server's answer that it refused the write, which changed nothing.
	WriteRefused
	// WriteAnswerLost is a connection event that leaves the answer unknown: the connection the
	// write went out on reconnected, closed or was replaced. The server may have applied the
	// write before that event, or may hold it still; either way a read made from then on is the
	// only judge of the consumer left.
	WriteAnswerLost
	// WriteNotAwaited is a wait its caller ended (the stop it was given closed) before any of the
	// three above.
	WriteNotAwaited
)

// UnansweredWrite is a create or an update of SessionID's consumer whose caller stopped waiting
// for its answer after the request was sent (SessionConsumers.writeRequest), with the
// subscription its answer arrives on still open. It is handed to the function OnUnansweredWrite
// set, once, and that function either waits for it (Wait) or drops it (Release).
type UnansweredWrite struct {
	// SessionID is the session whose consumer the write is for.
	SessionID string

	client     *Client
	conn       *nats.Conn
	reconnects uint64
	replies    chan *nats.Msg
	sub        *nats.Subscription
	release    sync.Once
}

// Wait waits for w's answer until stop closes, and reports how it ended (UnansweredWriteEnd). A
// connection event of w's client wakes it, and it ends as WriteAnswerLost once the connection w
// went out on has reconnected (its reconnect count moved), closed or been replaced, since
// nats.go answers nothing for a request whose answer it lost with the socket. It drops the
// subscription before it returns.
//
// One window stays open after a WriteAnswerLost: a write the server still held in its queue when
// the connection came back. A read sent from then on is an info request, which the server's
// workers take only once its other queue is empty (the comment at this file's top names the
// code), so it is answered after that write is taken off the queue, though it can be answered
// while a worker is still applying it.
func (w *UnansweredWrite) Wait(stop <-chan struct{}) UnansweredWriteEnd {
	defer w.Release()
	for {
		changed := w.client.ConnectionChanged()
		if w.lost() {
			return WriteAnswerLost
		}
		select {
		case reply := <-w.replies:
			return writeAnswer(reply)
		case <-changed:
		case <-stop:
			return WriteNotAwaited
		}
	}
}

// Release drops w's subscription; a second call does nothing.
func (w *UnansweredWrite) Release() {
	w.release.Do(func() { _ = w.sub.Unsubscribe() })
}

// lost reports whether the connection w went out on can no longer carry its answer.
func (w *UnansweredWrite) lost() bool {
	return w.client.Connection() != w.conn || w.conn.IsClosed() || w.conn.Stats().Reconnects != w.reconnects
}

// writeAnswer is what the answer to a consumer create or update says the server did with it: a
// no-responders status or an error in the answer is a write the server did not apply.
func writeAnswer(reply *nats.Msg) UnansweredWriteEnd {
	if len(reply.Data) == 0 && reply.Header.Get("Status") == "503" {
		return WriteRefused
	}
	var response consumerResponse
	if err := json.Unmarshal(reply.Data, &response); err != nil || response.Error != nil {
		return WriteRefused
	}
	return WriteApplied
}

// OnUnansweredWrite sets the function each unanswered create or update is handed to
// (UnansweredWrite), on the goroutine of the call that stopped waiting, before that call returns
// its error; nil, the default, drops the write's subscription at once. The listener sets it once,
// when it opens its session-consumer connection.
func (s *SessionConsumers) OnUnansweredWrite(handle func(*UnansweredWrite)) {
	s.unansweredMu.Lock()
	defer s.unansweredMu.Unlock()
	s.onUnanswered = handle
}

// writeRequest sends data to subject - a consumer create or update of sessionID's consumer - and
// returns its one answer, as requestConfirmed does: its publish is confirmed, so a write the
// connection's grant may not make returns ErrPublishDenied at once, and a ctx with no deadline
// waits storedMessageWait. Where requestConfirmed drops its reply subscription however it returns,
// this one keeps it when the request was sent and the caller stops waiting for its answer - ctx
// ended, or the flush behind the publish failed or could not confirm it - and hands the write to
// the function OnUnansweredWrite set (UnansweredWrite), since the server may still apply it.
//
// The answer is taken over a channel subscription of this request's own, as RequestCore takes
// one, so a write handed on holds a channel of nats.RequestChanLen replies and not one of
// nats.Options.SubChanLen. Such a channel is not closed when the connection closes, so the wait
// below also wakes at each connection event and ends with nats.ErrConnectionClosed on a closed
// connection, as requestConfirmed's does.
func (s *SessionConsumers) writeRequest(ctx context.Context, sessionID, subject string, data []byte) (*nats.Msg, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, storedMessageWait)
		defer cancel()
	}
	if err := s.client.ensureConnWithContext(ctx); err != nil {
		return nil, err
	}
	conn := s.client.Connection()
	inbox := conn.NewInbox()
	replies := make(chan *nats.Msg, nats.RequestChanLen)
	sub, err := conn.ChanSubscribe(inbox, replies)
	if err != nil {
		return nil, err
	}
	reconnects := conn.Stats().Reconnects
	sent := false
	err = s.client.publishConfirmed(ctx, conn, &nats.Msg{Subject: subject, Reply: inbox, Data: data}, func(msg *nats.Msg) error {
		err := conn.PublishMsg(msg)
		sent = err == nil
		return err
	})
	unanswered := func() {
		s.unanswered(&UnansweredWrite{SessionID: sessionID, client: s.client, conn: conn, reconnects: reconnects, replies: replies, sub: sub})
	}
	if err != nil && !errors.Is(err, ErrPublishUnconfirmed) {
		// A publish refused before it left, or one the server refused under the grant, changed
		// nothing; a publish made whose flush then failed is on its way all the same.
		if sent && !errors.Is(err, ErrPublishDenied) {
			unanswered()
		} else {
			_ = sub.Unsubscribe()
		}
		return nil, err
	}
	for {
		changed := s.client.ConnectionChanged()
		if conn.IsClosed() {
			_ = sub.Unsubscribe()
			return nil, cmp.Or(err, nats.ErrConnectionClosed)
		}
		select {
		case reply := <-replies:
			_ = sub.Unsubscribe()
			if len(reply.Data) == 0 && reply.Header.Get("Status") == "503" {
				return nil, nats.ErrNoResponders
			}
			return reply, nil
		case <-changed:
		case <-ctx.Done():
			unanswered()
			return nil, cmp.Or(err, ctx.Err())
		}
	}
}

// unanswered hands w to the function OnUnansweredWrite set, or drops it where none is set.
func (s *SessionConsumers) unanswered(w *UnansweredWrite) {
	s.unansweredMu.Lock()
	handle := s.onUnanswered
	s.unansweredMu.Unlock()
	if handle == nil {
		w.Release()
		return
	}
	handle(w)
}
