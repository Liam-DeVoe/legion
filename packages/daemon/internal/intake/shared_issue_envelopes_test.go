package intake

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The issue event envelopes Dispatch's outbox builds, field for field
// (packages/envoy/internal/dispatch/outbox's TestTheBuiltIssueEnvelopesAreTheSharedFixtures), and
// packages/contracts parses with the schemas it declares for their types. Together they hold every
// field Dispatch writes on these events, each set and each in the empty form Go writes it in when
// it holds nothing, so a field the daemon reads that Dispatch does not write, under that name, is
// missing here.
//
// The daemon reads each one with every field its own types read present, from the envelope down
// through its event and the event's payload, nested objects and lists of them included
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
			// A closed issue's claim is nobody's: the close released it.
			file: "testdata/dispatch/issue-updated-no-field-moved.json", reads: issue, setter: "session",
			want: DispatchIssue{Key: "CAPTURE-3", Seq: 12, Type: "issue.updated", Status: "done", Title: "Name the outbox's components",
				Parent: "CAPTURE-1", Rank: "UUUj", HandedOver: true, ActorSession: "ses_implementer8", ResolvedRepo: "acme/widgets",
				Blockers: []string{}, ClaimKnown: true},
		},
		{
			// A null resolved_repo is a root no repository resolves for, not one an older Dispatch
			// left out, and a null claim is nobody's, not unknown.
			file: "testdata/dispatch/issue-updated-null-fields.json", reads: issue,
			want: DispatchIssue{Key: "CAPTURE-9", Seq: 4, Type: "issue.updated", Status: "todo", Title: "Reopen a root nobody holds",
				Rank: "UUUm", Blockers: []string{}, ClaimKnown: true},
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
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("decode the shared envelope: %v", err)
			}
			// The walk starts at the envelope the daemon decodes (decodeMessage), and goes down
			// through the event its payload holds to that event's payload, read by its type.
			into := maps.Clone(rawReads)
			into["dispatchEvent.payload"] = tc.reads
			requireReadFields(t, "envelope", envelope, reflect.TypeFor[envoyEnvelope](), into)

			decoded, err := decodeMessage(topicOf(t, envelope), "CAPTURE", capturedRepositories, data)
			if err != nil {
				t.Fatalf("decode the shared envelope as the consumer does: %v", err)
			}
			if len(decoded.Unread) != 0 {
				t.Errorf("the decode took fields as unknown: %q", decoded.Unread)
			}
			if !reflect.DeepEqual(decoded.Fact, tc.want) {
				t.Errorf("decoded fact = %#v\nwant %#v", decoded.Fact, tc.want)
			}
			var event string
			if err := json.Unmarshal(envelope["payload"], &event); err != nil {
				t.Fatalf("read the envelope's event: %v", err)
			}
			setter, err := LabelSetter([]json.RawMessage{json.RawMessage(event)})
			if err != nil {
				t.Fatalf("read the label's setter as the preparation does: %v", err)
			}
			if setter != tc.setter {
				t.Errorf("LabelSetter = %q, want %q", setter, tc.setter)
			}
		})
	}
}

// topicOf is the envelope's topic, the subject the consumer reads it from.
func topicOf(t *testing.T, envelope map[string]json.RawMessage) string {
	t.Helper()
	var topic string
	if err := json.Unmarshal(envelope["topic"], &topic); err != nil {
		t.Fatalf("read the envelope's topic: %v", err)
	}
	return topic
}

// rawReads is what the daemon reads each field it keeps raw, or as the text of a JSON value, into:
// the type it decodes the field into later, whose own fields requireReadFields requires too, or nil
// for a field read whole. The event's payload is read by the event's own payload type, which each
// case of the test adds.
var rawReads = map[string]reflect.Type{
	// The envelope's payload is the text of the Dispatch event, which decodeDispatchEvent reads.
	"envoyEnvelope.payload": reflect.TypeFor[dispatchEvent](),
	// A string or null, which decodeDispatchEvent reads whole.
	"dispatchIssuePayload.resolved_repo": nil,
	"dispatchIssuePayload.claim":         reflect.TypeFor[dispatchClaim](),
	"dispatchClaimPayload.claim":         reflect.TypeFor[dispatchClaim](),
}

type mapReadForTest struct {
	Components map[string]json.RawMessage `json:"components"`
}

type ownJSONReadForTest struct{}

func (ownJSONReadForTest) UnmarshalJSON([]byte) error { return nil }

type ownJSONReadHolderForTest struct {
	Lane ownJSONReadForTest `json:"lane"`
}

func TestSharedIssueEnvelopeWalkRejectsUninspectedReadTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"map", reflect.TypeFor[mapReadForTest]().Field(0).Type, "map"},
		{"own unmarshal", reflect.TypeFor[ownJSONReadHolderForTest]().Field(0).Type, "ownJSONReadForTest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := uninspectedReadType(tc.typ); !strings.Contains(got, tc.want) {
				t.Fatalf("uninspectedReadType(%s) = %q, want it to name %q", tc.typ, got, tc.want)
			}
		})
	}
	if got := uninspectedReadType(reflect.TypeFor[time.Time]()); got != "" {
		t.Fatalf("uninspectedReadType(time.Time) = %q, want time's own decoder allowed", got)
	}
}

// uninspectedReadType names a map or a custom JSON decoder that requireReadFields cannot walk.
// time.Time is the one decoder every current daemon reading type uses and has no JSON fields this
// test needs to name.
func uninspectedReadType(typ reflect.Type) string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Map:
		return fmt.Sprintf("map %s", typ)
	case reflect.Slice, reflect.Array:
		return uninspectedReadType(typ.Elem())
	case reflect.Struct:
		if typ == reflect.TypeFor[time.Time]() {
			return ""
		}
		unmarshaler := reflect.TypeFor[json.Unmarshaler]()
		if typ.Implements(unmarshaler) || reflect.PointerTo(typ).Implements(unmarshaler) {
			return fmt.Sprintf("%s implements json.Unmarshaler", typ)
		}
	}
	return ""
}

// requireReadFields fails t for each field of reads, a type the daemon decodes object into, whose
// JSON name object does not hold, and so for each field of a nested value the daemon reads that is
// not null: a struct of reads, each object of a list of structs, or what into says a raw field or a
// field holding the text of a JSON value is read into. A map or custom decoder has no declared
// field walk, so it is refused by name rather than silently skipped.
func requireReadFields(t *testing.T, what string, object map[string]json.RawMessage, reads reflect.Type, into map[string]reflect.Type) {
	t.Helper()
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
		inner, named := into[reads.Name()+"."+name]
		if nested == reflect.TypeFor[json.RawMessage]() && !named {
			t.Errorf("the daemon keeps %s.%s raw, and rawReads does not say what it reads it into", reads.Name(), name)
			continue
		}
		if string(raw) == "null" {
			continue
		}
		switch {
		case named && inner == nil:
			continue
		case named && nested.Kind() == reflect.String:
			// The field holds the text of a JSON value, which the daemon decodes into inner.
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				t.Errorf("the %s's %q is no string: %v", what, name, err)
				continue
			}
			raw, nested = json.RawMessage(text), inner
		case named:
			nested = inner
		}
		for nested.Kind() == reflect.Pointer {
			nested = nested.Elem()
		}
		if problem := uninspectedReadType(nested); problem != "" {
			t.Errorf("the daemon reads %s.%s as %s, which the shared-envelope walk does not inspect", reads.Name(), name, problem)
			continue
		}
		if nested.Kind() == reflect.Slice || nested.Kind() == reflect.Array {
			element := nested.Elem()
			for element.Kind() == reflect.Pointer {
				element = element.Elem()
			}
			if element.Kind() != reflect.Struct || element == reflect.TypeFor[time.Time]() {
				continue
			}
			var items []json.RawMessage
			if err := json.Unmarshal(raw, &items); err != nil {
				t.Errorf("the %s's %q is no list: %v", what, name, err)
				continue
			}
			for index, item := range items {
				if string(item) == "null" {
					continue
				}
				var entry map[string]json.RawMessage
				if err := json.Unmarshal(item, &entry); err != nil {
					t.Errorf("the %s's %q[%d] is no object: %v", what, name, index, err)
					continue
				}
				requireReadFields(t, fmt.Sprintf("%s's %s[%d]", what, name, index), entry, element, into)
			}
			continue
		}
		if nested.Kind() != reflect.Struct || nested == reflect.TypeFor[time.Time]() {
			continue
		}
		var inside map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inside); err != nil {
			t.Errorf("the %s's %q is no object: %v", what, name, err)
			continue
		}
		requireReadFields(t, what+"'s "+name, inside, nested, into)
	}
}
