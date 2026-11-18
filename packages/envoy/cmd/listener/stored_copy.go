package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
)

// errStoredCopyUnread is wrapped by publishHeld's error when the stream answered the publish as a
// duplicate and the listener could not read the copy it holds. The cause is named in the text and
// not wrapped, so no comparison of a read's error (a denial, a refused subject) can turn the
// answer into another one: writeBusUnavailable answers it 503 with Retry-After: 1 under
// storedCopyUnreadablePrefix, whatever the read met.
var errStoredCopyUnread = errors.New("the copy the stream holds could not be read")

// publishHeld publishes *item within ctx (bus.Client.PublishAckWithin), marks charge by the
// stream's answer to that publish (senderCharge.published), and leaves in *item the envelope the
// stream holds for it. It is the one publish of the send route and of the topic publish, so
// neither can answer an envelope the stream does not hold: a route answers *item, and after a
// duplicate verdict *item is the stored copy, not the envelope the route built.
//
// A publish the stream stored as a new copy leaves *item as it was, and duplicate false. A
// duplicate verdict names the copy an earlier publish under the same message id stored, at its
// sequence (bus.StoredAck), and *item becomes that copy as storedCopy reads it, with duplicate
// true: a caller's repeat under the same idempotency_key built a new envelope, with a new event_id,
// that no stored message carries. A verdict that answers the bus's own resend (bus.StoredAck.Resent)
// may name the copy this publish's first send stored, which is that same envelope, so it is read
// and answered all the same. A copy that cannot be read is an error wrapping errStoredCopyUnread,
// with *item untouched, and the charge stays marked by the publish's own answer.
func publishHeld(ctx context.Context, client *bus.Client, charge *senderCharge, item *contracts.Envelope) (duplicate bool, err error) {
	ack, err := client.PublishAckWithin(ctx, *item)
	charge.published(ack, err)
	if err != nil || !ack.Duplicate {
		return false, err
	}
	held, err := storedCopy(ctx, client, *item, ack.Sequence)
	if err != nil {
		return true, err
	}
	*item = held
	return true, nil
}

// storedCopy reads the copy a duplicate verdict on item's publish named at seq: the message the
// stream holds at seq on item's topic, the subject it was published on. It is that copy only when
// it is at seq itself and carries item's dedupe key and topic, the two its message id is made of
// (bus.ForwardMsgID). Every other answer is an error wrapping errStoredCopyUnread:
//
//   - sequence 0, which names no copy: a clustered nats-server of the 2.11 line, which the
//     listener's start still admits, staged a message id at 0 and could answer a duplicate there,
//     and a read at 0 would hand back the subject's first message, another one;
//   - a read that failed or did not answer within ctx;
//   - a stream that holds nothing at seq on the topic, as after an operator's delete, or at the
//     edge of the 72-hour age, which equals the duplicate window, where the stream can still
//     remember an id whose copy has aged out: the next message on the topic, or none, is no copy
//     of this message;
//   - a message there that does not decode, or carries another dedupe key or topic.
func storedCopy(ctx context.Context, client *bus.Client, item contracts.Envelope, seq uint64) (contracts.Envelope, error) {
	if seq == 0 {
		return contracts.Envelope{}, fmt.Errorf("%w: the stream's duplicate verdict on %s named no sequence", errStoredCopyUnread, item.Topic)
	}
	stored, err := client.NextStoredMessage(ctx, item.Topic, seq)
	if err != nil {
		return contracts.Envelope{}, fmt.Errorf("%w: read sequence %d of %s: %v", errStoredCopyUnread, seq, item.Topic, err)
	}
	if stored.Sequence != seq {
		return contracts.Envelope{}, fmt.Errorf("%w: the stream holds nothing at sequence %d of %s, and its next message there is at %d",
			errStoredCopyUnread, seq, item.Topic, stored.Sequence)
	}
	var held contracts.Envelope
	if err := json.Unmarshal(stored.Data, &held); err != nil {
		return contracts.Envelope{}, fmt.Errorf("%w: decode sequence %d of %s: %v", errStoredCopyUnread, seq, item.Topic, err)
	}
	if held.DedupeKey != item.DedupeKey || held.Topic != item.Topic {
		return contracts.Envelope{}, fmt.Errorf("%w: sequence %d of %s holds dedupe key %q on %s, not %q",
			errStoredCopyUnread, seq, item.Topic, held.DedupeKey, held.Topic, item.DedupeKey)
	}
	return held, nil
}
