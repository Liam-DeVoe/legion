package intake

import (
	"encoding/json"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
)

// The issue event envelopes Dispatch's outbox builds, field for field
// (packages/envoy/internal/dispatch/outbox's TestTheBuiltIssueEnvelopesAreTheSharedFixtures), and
// packages/contracts parses with the schemas it declares for their types. Together they hold every
// field Dispatch writes on these events, in each form it writes it in, so a field the daemon reads
// that Dispatch does not write, under that name, is missing here.
//
// The daemon reads each one with every field its own types read present, nested ones included
// (requireReadFields), takes none as unknown, and decodes the fact the envelope says; LabelSetter,
// its second reader of an issue payload, reads the label's setter from it too. So a field Dispatch
// dropped or renamed, or one the daemon starts reading that Dispatch never wrote, fails here and not
// in production.
func TestTheSharedIssueEnvelopesCarryEveryFieldTheDaemonReads(t *testing.T) {
	issue := reflect.TypeFor[dispatchIssuePayload]()
	claimed := reflect.TypeFor[dispatchClaimPayload]()
	blockers := []string{"CAPTURE-2", "CAPTURE-5"}
	for _, tc := range []struct {
		file   string
		reads  reflect.Type
		want   Fact
		setter string
	}{
		{
			file: "testdata/dispatch/issue-updated-every-field.json", reads: issue, setter: "session",
			want: DispatchIssue{Key: "CAPTURE-7", Seq: 9, Type: "issue.updated", Status: "in_progress", Title: "Read every field Dispatch writes",
				Parent: "CAPTURE-1", Rank: "UUUk", HandedOver: true, ActorSession: "ses_implementer7", ResolvedRepo: "acme/widgets",
				Blockers: blockers, ClaimedBy: "session:ses_implementer7", ClaimKnown: true},
		},
		{
			file: "testdata/dispatch/issue-claimed-every-field.json", reads: claimed,
			want: DispatchClaim{Key: "CAPTURE-7", Seq: 10, ClaimedBy: "session:ses_implementer8"},
		},
		{
			file: "testdata/dispatch/issue-updated-no-field-moved.json", reads: issue, setter: "session",
			want: DispatchIssue{Key: "CAPTURE-7", Seq: 11, Type: "issue.updated", Status: "in_progress", Title: "Read every field Dispatch writes",
				Parent: "CAPTURE-1", Rank: "UUUk", HandedOver: true, ActorSession: "ses_implementer8", ResolvedRepo: "acme/widgets",
				Blockers: blockers, ClaimedBy: "session:ses_implementer8", ClaimKnown: true},
		},
		{
			// A null resolved_repo is a root no repository resolves for, not one an older Dispatch
			// left out, and a null claim is nobody's, not unknown.
			file: "testdata/dispatch/issue-updated-null-fields.json", reads: issue,
			want: DispatchIssue{Key: "CAPTURE-9", Seq: 4, Type: "issue.updated", Status: "todo", Title: "Reopen a root nobody holds",
				Rank: "UUUm", ActorSession: "ses_architect3", Blockers: []string{}, ClaimKnown: true},
		},
		{
			file: "testdata/dispatch/issue-released-null-claim.json", reads: claimed,
			want: DispatchClaim{Key: "CAPTURE-9", Seq: 6},
		},
	} {
		t.Run(path.Base(tc.file), func(t *testing.T) {
			data, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read the shared envelope: %v", err)
			}
			var envelope envoyEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("decode the shared envelope: %v", err)
			}
			var event map[string]json.RawMessage
			if err := json.Unmarshal([]byte(envelope.Payload), &event); err != nil {
				t.Fatalf("decode the envelope's event: %v", err)
			}
			requireReadFields(t, "event", event, reflect.TypeFor[dispatchEvent]())
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(event["payload"], &payload); err != nil {
				t.Fatalf("decode the event's payload: %v", err)
			}
			requireReadFields(t, "payload", payload, tc.reads)

			decoded, err := decodeMessage(envelope.Topic, "CAPTURE", capturedRepositories, data)
			if err != nil {
				t.Fatalf("decode the shared envelope as the consumer does: %v", err)
			}
			if len(decoded.Unread) != 0 {
				t.Errorf("the decode took fields as unknown: %q", decoded.Unread)
			}
			if !reflect.DeepEqual(decoded.Fact, tc.want) {
				t.Errorf("decoded fact = %#v\nwant %#v", decoded.Fact, tc.want)
			}
			setter, err := LabelSetter([]json.RawMessage{json.RawMessage(envelope.Payload)})
			if err != nil {
				t.Fatalf("read the label's setter as the preparation does: %v", err)
			}
			if setter != tc.setter {
				t.Errorf("LabelSetter = %q, want %q", setter, tc.setter)
			}
		})
	}
}

// rawReads is what the daemon reads each field it keeps raw into: the type it decodes the field
// into later, whose own fields requireReadFields requires too, or nil for a field read whole.
var rawReads = map[string]reflect.Type{
	// The event's payload, which the test reads by the event's own payload type.
	"dispatchEvent.payload": nil,
	// A string or null, which decodeDispatchEvent reads whole.
	"dispatchIssuePayload.resolved_repo": nil,
	"dispatchIssuePayload.claim":         reflect.TypeFor[dispatchClaim](),
	"dispatchClaimPayload.claim":         reflect.TypeFor[dispatchClaim](),
}

// requireReadFields fails t for each field of reads, a type the daemon decodes object into, whose
// JSON name object does not hold, and so for each field of a nested object the daemon reads that is
// not null: a struct of reads, or what rawReads says a raw field of it is read into.
func requireReadFields(t *testing.T, what string, object map[string]json.RawMessage, reads reflect.Type) {
	t.Helper()
	unmarshaler := reflect.TypeFor[json.Unmarshaler]()
	for field := range reads.Fields() {
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		raw, ok := object[name]
		if !ok {
			t.Errorf("the %s holds no %q, which the daemon reads into %s", what, name, field.Name)
			continue
		}
		nested := field.Type
		if nested == reflect.TypeFor[json.RawMessage]() {
			into, known := rawReads[reads.Name()+"."+name]
			if !known {
				t.Errorf("the daemon keeps %s.%s raw, and rawReads does not say what it reads it into", reads.Name(), name)
				continue
			}
			if into == nil {
				continue
			}
			nested = into
		}
		for nested.Kind() == reflect.Pointer {
			nested = nested.Elem()
		}
		if nested.Kind() != reflect.Struct || reflect.PointerTo(nested).Implements(unmarshaler) || string(raw) == "null" {
			continue
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Errorf("the %s's %q is no object: %v", what, name, err)
			continue
		}
		requireReadFields(t, what+"'s "+name, inner, nested)
	}
}
