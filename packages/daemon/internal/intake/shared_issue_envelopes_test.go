package intake

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"os"
	"path"
	"reflect"
	"slices"
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
// through its event and the event's payload, nested objects and lists of them included, each name
// encoding/json decodes into them, an embedded struct's among them (requireReadFields), takes none
// as unknown, and decodes the fact the envelope says; LabelSetter, its second reader of an issue
// payload, reads the label's setter from it too. So a field Dispatch dropped or renamed, or one
// the daemon starts reading that Dispatch never wrote, fails here and not in production.
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
			// A forced takeover's holder is the session its body names, proven or not.
			file: "testdata/dispatch/issue-claimed-forced.json", reads: claimed,
			want: DispatchClaim{Key: "CAPTURE-7", Seq: 11, ClaimedBy: "session:ses_reviewer4"},
		},
		{
			// A closed issue's claim is nobody's: the close released it.
			file: "testdata/dispatch/issue-updated-no-field-moved.json", reads: issue, setter: "session",
			want: DispatchIssue{Key: "CAPTURE-3", Seq: 12, Type: "issue.updated", Status: "done", Title: "Name the outbox's components",
				Parent: "CAPTURE-1", Rank: "UUUj", HandedOver: true, ActorSession: "ses_implementer8", ResolvedRepo: "acme/widgets",
				Blockers: []string{}, ClaimKnown: true},
		},
		{
			// A close's release leaves the claim nobody's.
			file: "testdata/dispatch/issue-released-closed.json", reads: claimed,
			want: DispatchClaim{Key: "CAPTURE-3", Seq: 11},
		},
		{
			// A null resolved_repo is a root no repository resolves for, not one an older Dispatch
			// left out, and a null claim is nobody's, not unknown.
			file: "testdata/dispatch/issue-updated-null-fields.json", reads: issue,
			want: DispatchIssue{Key: "CAPTURE-9", Seq: 4, Type: "issue.updated", Status: "todo", Title: "Reopen a root nobody holds",
				Rank: "UUUm", Blockers: []string{}, ClaimKnown: true},
		},
		{
			file: "testdata/dispatch/issue-claimed-first.json", reads: claimed,
			want: DispatchClaim{Key: "CAPTURE-9", Seq: 5, ClaimedBy: "session:ses_architect3"},
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

// The read types TestSharedIssueEnvelopeWalkRejectsUninspectedReadTypes plants, one of each kind
// whose JSON the walk cannot see into: each would read a "lane" key no example carries.
type (
	mapReadForTest       map[string]json.RawMessage
	ownStructReadForTest struct{ lane string }
	ownListReadForTest   []string
	ownTextReadForTest   string
	fromReadForTest      struct{ lane string }
)

func (r *ownStructReadForTest) UnmarshalJSON([]byte) error { r.lane = "read"; return nil }
func (r *ownListReadForTest) UnmarshalJSON([]byte) error   { *r = append(*r, "read"); return nil }
func (r *ownTextReadForTest) UnmarshalJSON([]byte) error   { *r = "read"; return nil }
func (r *fromReadForTest) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	r.lane = "read"
	return decoder.SkipValue()
}

// rawReadHolderForTest keeps a field rawReads names, and reads it with a decoder of its own
// rather than raw; rawKeptHolderForTest keeps it raw, as the daemon's own types do.
type (
	rawReadHolderForTest struct {
		Lanes ownStructReadForTest `json:"lanes"`
	}
	rawKeptHolderForTest struct {
		Lanes json.RawMessage `json:"lanes"`
	}
)

// The holders TestSharedIssueEnvelopeWalkRequiresEachNameEncodingJSONReads embeds, each reading a
// "lane" key no example carries: tagged, unexported and exported, one level deeper, and untagged.
type (
	laneHolderForTest struct {
		Lane string `json:"lane"`
	}
	LaneHolderForTest struct {
		Lane string `json:"lane"`
	}
	laneDeeperForTest   struct{ laneHolderForTest }
	laneUntaggedForTest struct{ Lane string }
	LaneUntaggedForTest struct{ Lane string }
)

// recordedErrors is a testing.TB that keeps each error a walk reports, so a test can require the
// walk's own refusals of it.
type recordedErrors struct {
	testing.TB
	errors []string
}

func (r *recordedErrors) Helper() {}

func (r *recordedErrors) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// The walk refuses, by name, each read type whose JSON it cannot see into, whatever the type's
// kind and whatever rawReads says the field is read into: requireReadFields itself runs over a
// holder of each, as it runs over the daemon's own types, so a walk that stopped asking
// uninspectedReadType, or asked it of the rawReads target in place of the field's own type, fails
// here. A plain struct, time.Time, whose decoder reads a string and no key, and a field kept raw
// are walked as they are.
func TestSharedIssueEnvelopeWalkRejectsUninspectedReadTypes(t *testing.T) {
	object := map[string]json.RawMessage{"lanes": json.RawMessage(`{"lane":"fast"}`)}
	for _, tc := range []struct {
		name   string
		holder reflect.Type
		lanes  string
		want   string
	}{
		{"map", reflect.TypeFor[struct {
			Lanes mapReadForTest `json:"lanes"`
		}](), `{"lane":"fast"}`, "intake.mapReadForTest"},
		{"interface", reflect.TypeFor[struct {
			Lanes any `json:"lanes"`
		}](), `{"lane":"fast"}`, "interface"},
		{"struct with UnmarshalJSON", reflect.TypeFor[struct {
			Lanes ownStructReadForTest `json:"lanes"`
		}](), `{"lane":"fast"}`, "intake.ownStructReadForTest"},
		{"list with UnmarshalJSON", reflect.TypeFor[struct {
			Lanes ownListReadForTest `json:"lanes"`
		}](), `[{"lane":"fast"}]`, "intake.ownListReadForTest"},
		{"string with UnmarshalJSON", reflect.TypeFor[struct {
			Lanes ownTextReadForTest `json:"lanes"`
		}](), `{"lane":"fast"}`, "intake.ownTextReadForTest"},
		{"struct with UnmarshalJSONFrom", reflect.TypeFor[struct {
			Lanes fromReadForTest `json:"lanes"`
		}](), `{"lane":"fast"}`, "intake.fromReadForTest"},
		{"list of maps", reflect.TypeFor[struct {
			Lanes []mapReadForTest `json:"lanes"`
		}](), `[{"lane":"fast"}]`, "intake.mapReadForTest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorded := &recordedErrors{TB: t}
			object["lanes"] = json.RawMessage(tc.lanes)
			requireReadFields(recorded, "holder", object, tc.holder, nil)
			if len(recorded.errors) != 1 || !strings.Contains(recorded.errors[0], "does not inspect") ||
				!strings.Contains(recorded.errors[0], tc.want) {
				t.Fatalf("the walk of a holder of %s reported %q, want one refusal naming %q", tc.name, recorded.errors, tc.want)
			}
		})
	}
	for _, holder := range []reflect.Type{
		reflect.TypeFor[struct {
			Lanes struct {
				Lane string `json:"lane"`
			} `json:"lanes"`
		}](),
		reflect.TypeFor[struct {
			Lanes time.Time `json:"lanes"`
		}](),
	} {
		recorded := &recordedErrors{TB: t}
		object["lanes"] = json.RawMessage(`{"lane":"fast"}`)
		if holder.Field(0).Type == reflect.TypeFor[time.Time]() {
			object["lanes"] = json.RawMessage(`"2026-11-19T05:15:00Z"`)
		}
		requireReadFields(recorded, "holder", object, holder, nil)
		if len(recorded.errors) != 0 {
			t.Fatalf("the walk of %s reported %q, want it walked", holder.Field(0).Type, recorded.errors)
		}
	}
	// A field rawReads names is read by its own type first: a decoder of its own is refused there,
	// whatever rawReads says it is read into, and a field kept raw is read as rawReads says.
	lanes := map[string]json.RawMessage{"lanes": json.RawMessage(`{"lane":"fast"}`)}
	target := reflect.TypeFor[struct {
		Lane string `json:"lane"`
	}]()
	recorded := &recordedErrors{TB: t}
	requireReadFields(recorded, "holder", lanes, reflect.TypeFor[rawReadHolderForTest](),
		map[string]reflect.Type{"rawReadHolderForTest.lanes": target})
	if len(recorded.errors) != 1 || !strings.Contains(recorded.errors[0], "does not inspect") ||
		!strings.Contains(recorded.errors[0], "intake.ownStructReadForTest") {
		t.Fatalf("the walk of a decoder of its own behind rawReads reported %q, want one refusal naming intake.ownStructReadForTest", recorded.errors)
	}
	recorded = &recordedErrors{TB: t}
	requireReadFields(recorded, "holder", lanes, reflect.TypeFor[rawKeptHolderForTest](),
		map[string]reflect.Type{"rawKeptHolderForTest.lanes": target})
	if len(recorded.errors) != 0 {
		t.Fatalf("the walk of a field kept raw reported %q, want it walked as rawReads says", recorded.errors)
	}
}

// The walk requires each name encoding/json decodes into a reading type, by encoding/json's rule
// for the fields a struct's embedded structs promote (readFields). For each holder, encoding/json
// itself decodes {"lane":"fast"} into it, and the walk of an object holding no "lane" must refuse
// it exactly when that decode read it: so a key the daemon reads through an embedded struct,
// exported or not, is required as a field of its own is, and one encoding/json passes over is not.
func TestSharedIssueEnvelopeWalkRequiresEachNameEncodingJSONReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		holder reflect.Type
	}{
		{"a field of its own", reflect.TypeFor[struct {
			Lane string `json:"lane"`
		}]()},
		{"an unexported embedded struct", reflect.TypeFor[struct{ laneHolderForTest }]()},
		{"an exported embedded struct", reflect.TypeFor[struct{ LaneHolderForTest }]()},
		{"an embedded pointer to an exported struct", reflect.TypeFor[struct{ *LaneHolderForTest }]()},
		{"a struct embedded two levels down", reflect.TypeFor[struct{ laneDeeperForTest }]()},
		{"a field of its own over a promoted one", reflect.TypeFor[struct {
			Lane string `json:"lane"`
			laneHolderForTest
		}]()},
		{"an untagged field, read under its Go name", reflect.TypeFor[struct{ Lane string }]()},
		{"an embedded struct tagged -", reflect.TypeFor[struct {
			laneHolderForTest `json:"-"`
		}]()},
		{"an embedded struct under a name of its own", reflect.TypeFor[struct {
			laneHolderForTest `json:"lanes"`
		}]()},
		{"two embedded structs reading it at one depth", reflect.TypeFor[struct {
			laneUntaggedForTest
			LaneUntaggedForTest
		}]()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded := reflect.New(tc.holder)
			if err := json.Unmarshal([]byte(`{"lane":"fast"}`), decoded.Interface()); err != nil {
				t.Fatalf("encoding/json's decode of a holder of %s: %v", tc.name, err)
			}
			read := !decoded.Elem().IsZero()
			recorded := &recordedErrors{TB: t}
			requireReadFields(recorded, "holder", map[string]json.RawMessage{}, tc.holder, nil)
			refused := 0
			for _, problem := range recorded.errors {
				// encoding/json matches a name in any case, so an untagged Lane reads "lane".
				if strings.Contains(strings.ToLower(problem), `holds no "lane"`) {
					refused++
				}
			}
			if want := map[bool]int{true: 1}[read]; refused != want {
				t.Fatalf("encoding/json reads \"lane\" into a holder of %s: %v; the walk reported %q, want %d refusals of it",
					tc.name, read, recorded.errors, want)
			}
		})
	}
}

// uninspectedReadType names a read type whose JSON requireReadFields cannot see into: a map or an
// interface, which encoding/json fills with whatever keys the value holds, and a type of any kind
// with a decoder of its own - UnmarshalJSON (json.Unmarshaler) or UnmarshalJSONFrom
// (encoding/json/v2's UnmarshalerFrom, which encoding/json calls under the JSONv2 go1.27 builds
// with by default) on the type or its pointer - and a list of any of those. time.Time is the one
// such decoder the daemon's reading types use, and it reads a string, no key.
func uninspectedReadType(typ reflect.Type) string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[time.Time]() {
		return ""
	}
	for _, decoder := range []reflect.Type{reflect.TypeFor[json.Unmarshaler](), reflect.TypeFor[jsonv2.UnmarshalerFrom]()} {
		if typ.Implements(decoder) || reflect.PointerTo(typ).Implements(decoder) {
			return fmt.Sprintf("%s, which implements %s", typ, decoder)
		}
	}
	switch typ.Kind() {
	case reflect.Map:
		return fmt.Sprintf("map %s", typ)
	case reflect.Interface:
		return fmt.Sprintf("interface %s", typ)
	case reflect.Slice, reflect.Array:
		return uninspectedReadType(typ.Elem())
	}
	return ""
}

// requireReadFields fails t for each JSON name encoding/json decodes into reads (readFields: an
// embedded struct's fields as the embedding type's own, whatever its export) that object does not
// hold, and so for each field of a nested value the daemon reads that is not null: a struct of
// reads, each object of a list of structs, or what into says a field the daemon keeps raw
// (json.RawMessage), or holding the text of a JSON value (a string), is read into later. The walk
// asks each field's own type first, so a decoder of its own is refused even where into names a
// target. A map, an interface or a custom decoder has no declared field to walk, so it is refused
// by name rather than silently skipped (uninspectedReadType).
func requireReadFields(t testing.TB, what string, object map[string]json.RawMessage, reads reflect.Type, into map[string]reflect.Type) {
	t.Helper()
	for _, read := range readFields(reads) {
		name, own := read.name, read.typ
		raw, ok := object[name]
		if !ok {
			t.Errorf("the %s holds no %q, which the daemon reads into %s", what, name, read.path)
			continue
		}
		keptRaw := own == reflect.TypeFor[json.RawMessage]()
		if !keptRaw {
			if problem := uninspectedReadType(own); problem != "" {
				t.Errorf("the daemon reads %s.%s as %s, which the shared-envelope walk does not inspect", reads.Name(), name, problem)
				continue
			}
		}
		base := own
		for base.Kind() == reflect.Pointer {
			base = base.Elem()
		}
		inner, named := into[reads.Name()+"."+name]
		if named && !keptRaw && base.Kind() != reflect.String {
			t.Errorf("rawReads says what the daemon reads %s.%s into, but the daemon reads it as %s itself, which the walk reads instead", reads.Name(), name, own)
			named = false
		}
		if keptRaw && !named {
			t.Errorf("the daemon keeps %s.%s raw, and rawReads does not say what it reads it into", reads.Name(), name)
			continue
		}
		if string(raw) == "null" {
			continue
		}
		nested := base
		switch {
		case named && inner == nil:
			continue
		case named && base.Kind() == reflect.String:
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

// readField is one JSON name encoding/json decodes into a struct type: the Go names of the
// fields that lead to the field it fills, from that type, and the field's type.
type readField struct {
	name string
	path string
	typ  reflect.Type
}

// readFields lists the JSON names encoding/json decodes into typ, a struct, by encoding/json's
// rule for a struct's fields (its typeFields): an unexported field is passed over unless it embeds
// a struct, and so is a field tagged "-"; a field is read under its tag's name, else under its Go
// name; and a struct embedded with no tag name, exported or not, is read as the embedding type's
// own fields, one level down. Of the fields one name reaches, the one nearest typ is read; of two
// at that level, the one tagged where the other is not, and neither when both or neither are.
// TestSharedIssueEnvelopeWalkRejectsUninspectedReadTypes holds this to encoding/json's own decode.
func readFields(typ reflect.Type) []readField {
	type candidate struct {
		readField
		depth  int
		tagged bool
	}
	var order []string
	reached := map[string][]candidate{}
	var visit func(holder reflect.Type, prefix string, depth int, embedding map[reflect.Type]bool)
	visit = func(holder reflect.Type, prefix string, depth int, embedding map[reflect.Type]bool) {
		for field := range holder.Fields() {
			base := field.Type
			if base.Kind() == reflect.Pointer {
				base = base.Elem()
			}
			if !field.IsExported() && (!field.Anonymous || base.Kind() != reflect.Struct) {
				continue
			}
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if field.Anonymous && name == "" && base.Kind() == reflect.Struct {
				// A type that embeds itself, through a pointer, is walked once on each path.
				if !embedding[base] {
					inner := maps.Clone(embedding)
					inner[base] = true
					visit(base, prefix+field.Name+".", depth+1, inner)
				}
				continue
			}
			tagged := name != ""
			if !tagged {
				name = field.Name
			}
			if reached[name] == nil {
				order = append(order, name)
			}
			reached[name] = append(reached[name], candidate{readField{name, prefix + field.Name, field.Type}, depth, tagged})
		}
	}
	visit(typ, "", 0, map[reflect.Type]bool{typ: true})
	var fields []readField
	for _, name := range order {
		candidates := reached[name]
		slices.SortStableFunc(candidates, func(a, b candidate) int {
			if a.depth != b.depth {
				return a.depth - b.depth
			}
			switch {
			case a.tagged == b.tagged:
				return 0
			case a.tagged:
				return -1
			}
			return 1
		})
		if len(candidates) > 1 && candidates[0].depth == candidates[1].depth && candidates[0].tagged == candidates[1].tagged {
			continue
		}
		fields = append(fields, candidates[0].readField)
	}
	return fields
}
