package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
)

// errStoredCopyUnread is wrapped by the error of a send, a topic publish or a role forward whose
// message id the stream already knows when the listener read no stored copy to answer: the copy a
// duplicate verdict named could not be read (storedCopy), or a send or a topic publish was refused
// while an earlier publish under the same message id was still in process
// (bus.ErrDuplicateInProcess), so that no copy is stored yet. A role forward refused that way is
// tried again as any forward that failed is, and its next answer names the copy (sendToRole). The
// cause is named in the text and not wrapped, so no comparison of a read's error (a denial, a
// refused subject) can turn the answer into another one: writeBusUnavailable answers it 503 with
// Retry-After: 1 under storedCopyUnreadablePrefix, whatever the read met.
var errStoredCopyUnread = errors.New("no stored copy read")

// storedPublish is the publish of an envelope on its own topic that returns the stream's
// acknowledgement: bus.Client.PublishAckWithin for both routes in the listener, and a function of
// the test's own where a test stands in for an answer of the stream's, as keptRolePublish is for
// a keep.
type storedPublish func(ctx context.Context, item contracts.Envelope) (bus.StoredAck, error)

// publishHeld publishes *item within ctx through publish, marks charge by the stream's answer to
// that publish (senderCharge.published), and leaves in *item the envelope the stream holds for
// it, which it reads through client. It is the one publish of the send route and of the topic
// publish, so neither can answer an envelope the stream does not hold: a route answers *item, and
// after a duplicate verdict *item is the stored copy, not the envelope the route built.
//
// A publish the stream stored as a new copy leaves *item as it was, and duplicate false. A
// duplicate verdict names the copy the stream holds under the same message id, at its sequence
// (bus.StoredAck), and *item becomes that copy as storedCopy reads it. duplicate is whether that
// copy is another request's, which is when its event_id is not *item's: a caller's repeat under
// the same idempotency_key built a new envelope, with a new event_id, that no stored message
// carries. A verdict that answers the bus's own resend after a closed connection
// (bus.StoredAck.Resent) can name the copy this request's first send stored, which carries its own
// event_id: that copy is this request's, answered with no duplicate as any new copy is.
//
// A copy that cannot be read, and a refusal of a publish whose message id is still in process
// under an earlier one (bus.ErrDuplicateInProcess), are an error wrapping errStoredCopyUnread,
// with *item untouched, and the charge stays marked by the publish's own answer.
func publishHeld(ctx context.Context, client *bus.Client, publish storedPublish, charge *senderCharge, item *contracts.Envelope) (duplicate bool, err error) {
	ack, err := publish(ctx, *item)
	charge.published(ack, err)
	if errors.Is(err, bus.ErrDuplicateInProcess) {
		return false, fmt.Errorf("%w: %v", errStoredCopyUnread, err)
	}
	if err != nil || !ack.Duplicate {
		return false, err
	}
	held, err := storedCopy(ctx, client, item.Topic, *item, ack.Sequence)
	if err != nil {
		return false, err
	}
	duplicate = held.EventID != item.EventID
	*item = held
	return duplicate, nil
}

// storedCopy reads the copy a duplicate verdict on item's publish to subject named at seq: the
// message the stream holds at seq on subject, item's own topic for a send or a topic publish and
// the holder's agent subject for a role forward (roleSend.deliveredHeld). Its message id is made
// of its dedupe key and the subject it was stored on (bus.ForwardMsgID), and the read is of that
// subject alone, so the message is that copy only when it is at seq itself and carries item's
// dedupe key. Its envelope's own topic is not compared: a role forward stored on a holder's agent
// subject under that holder's own send key (contracts.RoleForwardDedupeKey) keeps its role topic,
// and is the copy a direct send under the same key repeats. Every other answer is an error
// wrapping errStoredCopyUnread:
//
//   - sequence 0, which names no copy, refused before any read as a defence: the read would hand
//     back the subject's first message, another one (nats-server v2.10.29 answered a duplicate at
//     0, and every server the listener admits answers a stored sequence);
//   - a read that failed or did not answer within ctx;
//   - a stream that holds nothing at seq on subject, as after an operator's delete, or at the
//     edge of the 72-hour age, which equals the duplicate window, where the stream can still
//     remember an id whose copy has aged out: the next message on subject, or none, is no copy
//     of this message, whatever dedupe key it carries;
//   - a message there that does not decode, or carries another dedupe key.
func storedCopy(ctx context.Context, client *bus.Client, subject string, item contracts.Envelope, seq uint64) (contracts.Envelope, error) {
	if seq == 0 {
		return contracts.Envelope{}, fmt.Errorf("%w: the stream's duplicate verdict on %s named no sequence", errStoredCopyUnread, subject)
	}
	stored, err := client.NextStoredMessage(ctx, subject, seq)
	if err != nil {
		return contracts.Envelope{}, fmt.Errorf("%w: read sequence %d of %s: %v", errStoredCopyUnread, seq, subject, err)
	}
	if stored.Sequence != seq {
		return contracts.Envelope{}, fmt.Errorf("%w: the stream holds nothing at sequence %d of %s, and its next message there is at %d",
			errStoredCopyUnread, seq, subject, stored.Sequence)
	}
	var held contracts.Envelope
	if err := json.Unmarshal(stored.Data, &held); err != nil {
		return contracts.Envelope{}, fmt.Errorf("%w: decode sequence %d of %s: %v", errStoredCopyUnread, seq, subject, err)
	}
	if held.DedupeKey != item.DedupeKey {
		return contracts.Envelope{}, fmt.Errorf("%w: sequence %d of %s holds dedupe key %q, not %q",
			errStoredCopyUnread, seq, subject, held.DedupeKey, item.DedupeKey)
	}
	return held, nil
}
