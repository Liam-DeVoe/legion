// What the send route and the topic publish route answer a caller's repeat under the same
// idempotency_key: the stream keeps the first copy and stores no second one, so the answer is
// the copy the stream holds, with duplicate, and never the new envelope the route built for the
// repeat, whose event_id no stored message carries.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
	"github.com/sjawhar/envoy/internal/testnats"
)

// heldAnswer is a send's or a topic publish's answer as these tests read it: the limitedAnswer
// (status, body, the request's record line), with the envelope the body carries.
type heldAnswer struct {
	limitedAnswer
	envelope contracts.Envelope
}

// held reads the envelope out of answer's body. An answer that carries none, a refusal, reads as
// the zero envelope.
func held(t *testing.T, answer limitedAnswer) heldAnswer {
	t.Helper()
	read := heldAnswer{limitedAnswer: answer}
	if err := json.Unmarshal([]byte(answer.raw), &read.envelope); err != nil {
		t.Fatalf("decode the envelope of the answer %s: %v", answer.raw, err)
	}
	return read
}

// heldCopy is the one copy the stream holds on subject, read with client, and fails the test
// unless the stream holds exactly one.
func heldCopy(t *testing.T, client *bus.Client, subject string) contracts.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stored, err := readStored(ctx, client, subject)
	if err != nil {
		t.Fatalf("read what the stream holds on %s: %v", subject, err)
	}
	if len(stored) != 1 {
		t.Fatalf("the stream holds %d copies on %s, want the one copy the first request stored", len(stored), subject)
	}
	var kept contracts.Envelope
	if err := json.Unmarshal(stored[0].Data, &kept); err != nil {
		t.Fatalf("decode the copy the stream holds on %s: %v", subject, err)
	}
	return kept
}

// requireHeld fails unless the answer is a 200 whose envelope is kept, field for field (its
// event_id, its issued_at, its body and every other field), whose duplicate is duplicate, and
// whose record line names kept's event_id.
func (a heldAnswer) requireHeld(t *testing.T, what string, kept contracts.Envelope, duplicate bool) {
	t.Helper()
	a.requireStatus(t, what, http.StatusOK)
	if !reflect.DeepEqual(a.envelope, kept) {
		t.Fatalf("%s answered event_id %s issued_at %d, want the stored copy, event_id %s issued_at %d:\nanswer %+v\nstored %+v",
			what, a.envelope.EventID, a.envelope.IssuedAt, kept.EventID, kept.IssuedAt, a.envelope, kept)
	}
	if a.Duplicate != duplicate {
		t.Fatalf("%s answered duplicate %t, want %t; body %s", what, a.Duplicate, duplicate, a.raw)
	}
	if got := a.line["event_id"]; got != kept.EventID {
		t.Fatalf("%s: the request's line names event_id %v, want the stored copy's %s; line %v", what, got, kept.EventID, a.line)
	}
}

// TestASendRepeatedUnderOneIdempotencyKeyAnswersTheStoredCopy is check c-send-repeat-stored-copy
// and the send's half of c-first-send-unchanged. A session sends one message to a session under
// one idempotency_key, the transport's UUID, which earns the publish a Nats-Msg-Id, and then sends
// it again under that key, as a sender whose answer was lost does. The stream holds one copy. The
// repeat's answer is that copy, with duplicate; the first send's answer is that copy too, with no
// duplicate, and under the sender limit it costs one.
func TestASendRepeatedUnderOneIdempotencyKeyAnswersTheStoredCopy(t *testing.T) {
	rig := newSenderLimitRig(t)
	rig.limit(4, 2)

	t.Run("control: the first send answers the copy the stream stored, with no duplicate, and costs one", func(t *testing.T) {
		const sender, target = "ses_repeat_first_send", "ses_repeat_first_send_target"
		rig.live(t, target, nil)
		first := held(t, rig.sendFrom(t, sender, target, map[string]any{"idempotency_key": uuid.NewString()}))
		first.requireHeld(t, "the first send", heldCopy(t, rig.harness.client, contracts.LegacyAgentSubject(target)), false)
		first.requireNamed(t, "the first send", sender)
	})

	t.Run("case: a repeat answers 200 with the stored copy and duplicate, and costs nothing", func(t *testing.T) {
		const sender, target = "ses_repeat_send", "ses_repeat_send_target"
		rig.live(t, target, nil)
		key := map[string]any{"idempotency_key": uuid.NewString()}
		rig.sendFrom(t, sender, target, key).requireStatus(t, "the first send", http.StatusOK)
		repeat := held(t, rig.sendFrom(t, sender, target, key))
		repeat.requireHeld(t, "the repeated send", heldCopy(t, rig.harness.client, contracts.LegacyAgentSubject(target)), true)
		repeat.requireUnnamed(t, "the repeated send")
		rig.requireCount(t, sender, true)
	})
}

// TestAPublishRepeatedUnderOneIdempotencyKeyAnswersTheStoredCopy is check
// c-publish-repeat-stored-copy and the publish's half of c-first-send-unchanged. A session
// publishes one message to a team topic under one idempotency_key, which the route makes the
// minted dedupe key publish.<key>, and then publishes it again under that key: once with no sender
// limit, and once under a limit that counts the session. The stream holds one copy each time, and
// the repeat's answer is that copy, with duplicate, and costs nothing. The first publish's answer
// is its stored copy, with no duplicate, and under the limit it costs one.
func TestAPublishRepeatedUnderOneIdempotencyKeyAnswersTheStoredCopy(t *testing.T) {
	rig := newSenderLimitRig(t)
	topic := func() string { return "notifications.team.repeat-answer-" + id.New() }

	t.Run("control: the first publish with no sender limit answers the copy the stream stored, with no duplicate", func(t *testing.T) {
		rig.deps.senderLimit = nil
		const sender = "ses_repeat_first_publish"
		subject := topic()
		first := held(t, rig.publishFrom(t, sender, subject, map[string]any{"idempotency_key": uuid.NewString()}))
		first.requireHeld(t, "the first publish", heldCopy(t, rig.harness.client, subject), false)
	})

	t.Run("control: the first publish under a sender limit answers the copy the stream stored, with no duplicate, and costs one", func(t *testing.T) {
		rig.limit(4, 2)
		const sender = "ses_repeat_first_limited_publish"
		subject := topic()
		first := held(t, rig.publishFrom(t, sender, subject, map[string]any{"idempotency_key": uuid.NewString()}))
		first.requireHeld(t, "the first publish", heldCopy(t, rig.harness.client, subject), false)
		first.requireNamed(t, "the first publish", sender)
	})

	t.Run("case: a repeat with no sender limit answers 200 with the stored copy and duplicate", func(t *testing.T) {
		rig.deps.senderLimit = nil
		const sender = "ses_repeat_publish"
		subject := topic()
		key := map[string]any{"idempotency_key": uuid.NewString()}
		rig.publishFrom(t, sender, subject, key).requireStatus(t, "the first publish", http.StatusOK)
		repeat := held(t, rig.publishFrom(t, sender, subject, key))
		repeat.requireHeld(t, "the repeated publish", heldCopy(t, rig.harness.client, subject), true)
	})

	t.Run("case: a repeat under a sender limit that counts the session answers 200 with the stored copy and duplicate, and costs nothing", func(t *testing.T) {
		rig.limit(4, 2)
		const sender = "ses_repeat_limited_publish"
		subject := topic()
		key := map[string]any{"idempotency_key": uuid.NewString()}
		rig.publishFrom(t, sender, subject, key).requireStatus(t, "the first publish", http.StatusOK)
		repeat := held(t, rig.publishFrom(t, sender, subject, key))
		repeat.requireHeld(t, "the repeated publish", heldCopy(t, rig.harness.client, subject), true)
		repeat.requireUnnamed(t, "the repeated publish")
		rig.requireCount(t, sender, true)
	})
}

// TestARepeatWhoseStoredCopyCannotBeReadAnswers503 is check c-unreadable-copy. The listener's NATS
// user may do everything but read a stored message of the notification stream (the publish of
// $JS.API.STREAM.MSG.GET.<stream> is denied), so the server itself refuses the read of the copy a
// repeat's duplicate verdict names. On the send route and on the topic publish route, the repeat
// is then answered 503 with Retry-After: 1 and no envelope, and the stream still holds the one
// copy. Once the server grants the read, the same repeat is answered 200 with the stored copy and
// duplicate.
//
// The control is the first send and the first publish under the same denial: neither has a copy
// to read back, so each answers 200 with no duplicate, and the denial is shown to touch only the
// answer to a repeat.
func TestARepeatWhoseStoredCopyCannotBeReadAnswers503(t *testing.T) {
	denied := listenerGrant("$JS.API.STREAM.MSG.GET." + bus.Stream)
	server, uri := testnats.StartConfig(t, denied, []string{"-js"}, false)
	uri = asListenerUser(t, uri)
	client, err := bus.ConnectOwningStream([]string{uri}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener's user: %v", err)
	}
	t.Cleanup(client.Close)
	rig := newSenderLimitRigOn(t, client, uri)

	const sender, target = "ses_unread_sender", "ses_unread_target"
	rig.live(t, target, nil)
	team := "notifications.team.unread-repeat-" + id.New()
	routes := []struct {
		name    string
		subject string
		send    func(t *testing.T, key map[string]any) heldAnswer
	}{
		{"send", contracts.LegacyAgentSubject(target), func(t *testing.T, key map[string]any) heldAnswer {
			return held(t, rig.sendFrom(t, sender, target, key))
		}},
		{"topic publish", team, func(t *testing.T, key map[string]any) heldAnswer {
			return held(t, rig.publishFrom(t, sender, team, key))
		}},
	}
	keys := make([]map[string]any, len(routes))
	firsts := make([]heldAnswer, len(routes))

	t.Run("control: the first send and the first publish, which read no copy back, answer 200 with no duplicate", func(t *testing.T) {
		for i, route := range routes {
			keys[i] = map[string]any{"idempotency_key": uuid.NewString()}
			firsts[i] = route.send(t, keys[i])
			firsts[i].requireStatus(t, "the first "+route.name, http.StatusOK)
			if firsts[i].Duplicate || firsts[i].envelope.EventID == "" {
				t.Fatalf("the first %s answered %s, want its envelope with no duplicate", route.name, firsts[i].raw)
			}
			if stored := rig.storedOn(t, route.subject); stored != 1 {
				t.Fatalf("the stream holds %d messages on %s after the first %s, want 1", stored, route.subject, route.name)
			}
		}
	})

	t.Run("case: a repeat answers 503 with Retry-After 1 and no envelope while the read is denied, and the stored copy once it is granted", func(t *testing.T) {
		for i, route := range routes {
			if keys[i] == nil {
				t.Fatalf("the first %s was not sent", route.name)
			}
			repeat := route.send(t, keys[i])
			repeat.requireStatus(t, "the "+route.name+" repeated while the read is denied", http.StatusServiceUnavailable)
			if repeat.retryAfter != "1" {
				t.Fatalf("the %s repeated while the read is denied answered Retry-After %q, want 1", route.name, repeat.retryAfter)
			}
			if repeat.envelope.EventID != "" || repeat.Duplicate {
				t.Fatalf("the %s repeated while the read is denied answered an envelope: %s", route.name, repeat.raw)
			}
			// The body names the stored copy it could not read, not a bus that did not answer or
			// the publish's denial, and the record says the same (storedCopyUnreadablePrefix).
			if !strings.HasPrefix(repeat.Error, "envoy: the stream already holds this message, so the answer is its stored copy: ") {
				t.Fatalf("the %s repeated while the read is denied answered the error %q, want the unread stored copy's", route.name, repeat.Error)
			}
			repeat.requireOutcome(t, "the "+route.name+" repeated while the read is denied", "stored_copy_unreadable")
			if stored := rig.storedOn(t, route.subject); stored != 1 {
				t.Fatalf("the stream holds %d messages on %s after the denied repeat, want the first %s's one", stored, route.subject, route.name)
			}
		}
		if err := server.Reload(t, listenerGrant()); err != nil {
			t.Fatalf("grant the read: %v", err)
		}
		for i, route := range routes {
			kept := heldCopy(t, client, route.subject)
			if kept.EventID != firsts[i].envelope.EventID {
				t.Fatalf("the stream holds event %s on %s, want the first %s's %s", kept.EventID, route.subject, route.name, firsts[i].envelope.EventID)
			}
			route.send(t, keys[i]).requireHeld(t, "the "+route.name+" repeated once the read is granted", kept, true)
		}
	})
}

// storedCopy answers the copy a duplicate verdict named only where the stream holds a copy of
// this message at that very sequence. Two messages are stored on one topic under minted keys, and
// each row asks for the first one's copy: at its own sequence it is answered whole; at sequence 0,
// which names no copy, it is refused before any read (a nil client would panic on one); at the
// second message's sequence, which holds another dedupe key, at a sequence past the topic's last
// message, and at its own sequence once the stream no longer holds it (an operator's delete, where
// the read finds the second message next), it is refused. Every refusal wraps
// errStoredCopyUnread, which the routes answer 503.
func TestTheStoredCopyIsReadOnlyAtTheSequenceTheVerdictNamed(t *testing.T) {
	client := setupPublishTestClient(t)
	topic := "notifications.team.stored-copy-" + id.New()
	message := func(text string) contracts.Envelope {
		return contracts.MessageEnvelope(contracts.Message{SourceSession: "ses_stored_copy", Text: text}, topic, publishDedupePrefix+uuid.NewString())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, other := message("the first message"), message("another message")
	firstAck, err := client.PublishAckWithin(ctx, first)
	if err != nil {
		t.Fatalf("publish the first message: %v", err)
	}
	otherAck, err := client.PublishAckWithin(ctx, other)
	if err != nil {
		t.Fatalf("publish another message: %v", err)
	}

	held, err := storedCopy(ctx, client, first, firstAck.Sequence)
	if err != nil || !reflect.DeepEqual(held, first) {
		t.Fatalf("the copy at the first message's sequence %d = %+v (%v), want the first message %+v", firstAck.Sequence, held, err, first)
	}
	refused := func(what string, from *bus.Client, seq uint64) {
		t.Helper()
		held, err := storedCopy(ctx, from, first, seq)
		if !errors.Is(err, errStoredCopyUnread) || held.EventID != "" {
			t.Fatalf("the copy %s = %+v (%v), want a refusal wrapping errStoredCopyUnread and no envelope", what, held, err)
		}
	}
	refused("at sequence 0", nil, 0)
	refused("at another message's sequence", client, otherAck.Sequence)
	refused("past the topic's last message", client, otherAck.Sequence+1)
	if err := client.JS().DeleteMsg(bus.Stream, firstAck.Sequence); err != nil {
		t.Fatalf("delete the first message: %v", err)
	}
	refused("at the first message's sequence once the stream no longer holds it", client, firstAck.Sequence)
}

// An answer's expects_reply is the answered envelope's, in the request's spelling where the two
// agree: the request's own bytes for the envelope built from them, the old word "none" among
// them, nothing where both say nothing, and the copy's own value in the one new vocabulary where a
// repeat asked for another reply than the stored copy carries.
func TestAnAnswersExpectsReplyIsTheAnsweredEnvelopesInTheRequestsSpelling(t *testing.T) {
	for _, row := range []struct {
		name      string
		requested string
		answered  contracts.ExpectsReply
		want      string
	}{
		{"nothing asked, nothing carried", "", contracts.ExpectsReplyUnset, ""},
		{"null asked, nothing carried", "null", contracts.ExpectsReplyUnset, ""},
		{"false asked and carried", "false", contracts.ExpectsReplyNone, "false"},
		{"the old word asked, no reply carried", `"none"`, contracts.ExpectsReplyNone, `"none"`},
		{"optional asked and carried", `"optional"`, contracts.ExpectsReplyOptional, `"optional"`},
		{"the old word asked, a required reply carried", `"none"`, contracts.ExpectsReplyRequired, `"required"`},
		{"nothing asked, no reply carried", "", contracts.ExpectsReplyNone, "false"},
		{"required asked, nothing carried", `"required"`, contracts.ExpectsReplyUnset, ""},
	} {
		var requested json.RawMessage
		if row.requested != "" {
			requested = json.RawMessage(row.requested)
		}
		if got := expectsReplyAnswer(requested, row.answered); !bytes.Equal(got, []byte(row.want)) {
			t.Errorf("%s: expects_reply %q, want %q", row.name, got, row.want)
		}
	}
}

// A repeat answers the stored copy's expects_reply, as it answers the rest of the copy. The first
// publish asks for no reply in the old word, "none", which the copy carries as no reply. A repeat
// in the same word reads back "none"; a repeat that asks for a required reply, or says nothing,
// reads the copy's false, since the answer names the stored message and not the request.
func TestARepeatAnswersTheStoredCopysExpectsReply(t *testing.T) {
	rig := newSenderLimitRig(t)
	const sender = "ses_repeat_expects_reply"
	topic := "notifications.team.repeat-expects-reply-" + id.New()
	key := uuid.NewString()
	expectsReplyOf := func(t *testing.T, what string, answer limitedAnswer) string {
		t.Helper()
		answer.requireStatus(t, what, http.StatusOK)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(answer.raw), &fields); err != nil {
			t.Fatalf("%s: decode %s: %v", what, answer.raw, err)
		}
		return string(fields["expects_reply"])
	}
	if got := expectsReplyOf(t, "the first publish", rig.publishFrom(t, sender, topic, map[string]any{"idempotency_key": key, "expects_reply": "none"})); got != `"none"` {
		t.Fatalf("the first publish answered expects_reply %s, want the request's own \"none\"", got)
	}
	for _, row := range []struct {
		name      string
		requested any
		want      string
	}{
		{"a repeat in the same old word", "none", `"none"`},
		{"a repeat asking for a required reply", "required", "false"},
		{"a repeat saying nothing", nil, "false"},
	} {
		answer := rig.publishFrom(t, sender, topic, map[string]any{"idempotency_key": key, "expects_reply": row.requested})
		if !answer.Duplicate {
			t.Fatalf("%s answered %s, want duplicate", row.name, answer.raw)
		}
		if got := expectsReplyOf(t, row.name, answer); got != row.want {
			t.Fatalf("%s answered expects_reply %s, want the stored copy's %s", row.name, got, row.want)
		}
	}
}
