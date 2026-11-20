package intake

import (
	"encoding/json"
	"reflect"
	"testing"
)

// deadLetterEnvelope is a message on the agent subject of the daemon's provider-errors sender: from
// source, naming copy as its source_event_id, with the dead_letter field reason ("" for none), and
// a text payload, as the listener writes a dead letter's.
func deadLetterEnvelope(t *testing.T, source, copy, reason string) []byte {
	t.Helper()
	envelope := map[string]any{
		"event_id": "letter-of-" + copy, "source": source, "source_event_id": copy,
		"topic": "notifications.agent.legion-daemon-capture", "dedupe_key": "envoy.dead-letter.a-role.7",
		"issued_at": 1, "payload_summary": "Envoy dropped your message to role a-role: its hour ended.",
		"payload": "Envoy dropped a message that you published to role a-role.\nThe hour ended.", "trace_id": "trace",
	}
	if reason != "" {
		envelope["dead_letter"] = reason
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A message on the daemon's own agent subject is the listener's dead letter of a kept copy of its
// provider-errors notice when it is from Envoy and carries the dead_letter field: its fact names
// the copy and the field's reason. Its payload is the dead letter's text, which is not JSON, and
// is never read. A message from a session, and a dead letter of a listener before the field, are
// no fact: the reason is read from the field alone, never from the text.
func TestADeadLetterOnTheDaemonsOwnSubjectIsReadFromItsFieldAlone(t *testing.T) {
	const subject = "notifications.agent.legion-daemon-capture"
	for _, tc := range []struct {
		name           string
		source, reason string
		want           Fact
	}{
		{"an expired dead letter", "envoy", "expired", ProviderErrorsDeadLetter{Copy: "copy-1", Reason: "expired"}},
		{"an unconfirmed dead letter", "envoy", "expired_unconfirmed", ProviderErrorsDeadLetter{Copy: "copy-1", Reason: "expired_unconfirmed"}},
		{"a dead letter of a listener before the field", "envoy", "", nil},
		{"a session's message", "agent", "expired", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := decodeMessage(subject, "CAPTURE", capturedRepositories, deadLetterEnvelope(t, tc.source, "copy-1", tc.reason))
			if err != nil {
				t.Fatalf("decode the message: %v, want it decoded although its payload is not JSON", err)
			}
			if !reflect.DeepEqual(decoded.Fact, tc.want) {
				t.Fatalf("the message decodes to %#v, want %#v", decoded.Fact, tc.want)
			}
			if decoded.EventID != "letter-of-copy-1" {
				t.Fatalf("the message is applied under %q, want its own event id", decoded.EventID)
			}
		})
	}
	// The control: the same dead letter on any other subject of the daemon is not a dead letter,
	// and its text payload is refused as every payload intake decodes there is.
	if _, err := decodeMessage(legionWakeSubject, "CAPTURE", capturedRepositories, deadLetterEnvelope(t, "envoy", "copy-1", "expired")); err == nil {
		t.Fatal("a dead letter on the wake subject decoded, want its text payload refused")
	}
}
