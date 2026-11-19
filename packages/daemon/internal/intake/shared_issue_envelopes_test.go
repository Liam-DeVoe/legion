package intake

import (
	"encoding/json"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
)

// The issue.updated and issue.claimed envelopes Dispatch's outbox builds, field for field
// (packages/envoy/internal/dispatch/outbox's TestTheBuiltIssueEnvelopesAreTheSharedFixtures), and
// packages/contracts parses with the schemas it declares for the two types. Each holds every field
// Dispatch writes on its type, so a field the daemon reads that Dispatch does not write, under that
// name, is missing here.
//
// The daemon reads each one with every field its own types read present (dispatchEvent, then
// dispatchIssuePayload or dispatchClaimPayload), takes none as unknown, and decodes the fact the
// envelope says: so a field Dispatch dropped or renamed, or one the daemon starts reading that
// Dispatch never wrote, fails here and not in production.
func TestTheSharedIssueEnvelopesCarryEveryFieldTheDaemonReads(t *testing.T) {
	for _, tc := range []struct {
		file  string
		reads reflect.Type
		want  Fact
	}{
		{
			file:  "testdata/dispatch/issue-updated-every-field.json",
			reads: reflect.TypeFor[dispatchIssuePayload](),
			want: DispatchIssue{Key: "CAPTURE-7", Seq: 9, Type: "issue.updated", Status: "in_progress", Title: "Read every field Dispatch writes",
				Parent: "CAPTURE-1", Rank: "UUUk", HandedOver: true, ActorSession: "ses_implementer7", ResolvedRepo: "acme/widgets",
				Blockers: []string{"CAPTURE-2", "CAPTURE-5"}, ClaimedBy: "session:ses_implementer7", ClaimKnown: true},
		},
		{
			file:  "testdata/dispatch/issue-claimed-every-field.json",
			reads: reflect.TypeFor[dispatchClaimPayload](),
			want:  DispatchClaim{Key: "CAPTURE-7", Seq: 10, ClaimedBy: "session:ses_implementer8"},
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
		})
	}
}

// requireReadFields fails t for each field of reads, a type the daemon decodes object into, whose
// JSON name object does not hold.
func requireReadFields(t *testing.T, what string, object map[string]json.RawMessage, reads reflect.Type) {
	t.Helper()
	for field := range reads.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if _, ok := object[name]; !ok {
			t.Errorf("the %s holds no %q, which the daemon reads into %s.%s", what, name, reads.Name(), field.Name)
		}
	}
}
