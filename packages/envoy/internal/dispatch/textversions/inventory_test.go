package textversions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
)

// TestEveryEventTypeIsSortedForTheKeywordFill holds the rules to every event type there is: each
// member of contracts.DispatchEventTypes, the DispatchEvent union of packages/contracts, either
// can start a version (versionRules) or changes no searchable text (unchangingTypes), and is in
// exactly one of the two; neither list names a type the union lacks; and each type production's
// log holds (testdata/production_event_types.txt, written by `testdata/capture.sh types`) is a
// member of the union, so the fill meets no event of a type the rules were never sorted for.
func TestEveryEventTypeIsSortedForTheKeywordFill(t *testing.T) {
	members := make(map[string]bool, len(contracts.DispatchEventTypes))
	var neither, both []string
	for _, typ := range contracts.DispatchEventTypes {
		members[typ] = true
		_, starts := versionRules[typ]
		_, unchanging := unchangingTypes[typ]
		switch {
		case starts && unchanging:
			both = append(both, typ)
		case !starts && !unchanging:
			neither = append(neither, typ)
		}
	}
	var strays []string
	for typ := range versionRules {
		if !members[typ] {
			strays = append(strays, typ)
		}
	}
	for typ := range unchangingTypes {
		if !members[typ] && !slices.Contains(strays, typ) {
			strays = append(strays, typ)
		}
	}
	slices.Sort(strays)
	var unknown []string
	for _, typ := range productionEventTypes(t) {
		if !members[typ] {
			unknown = append(unknown, typ)
		}
	}

	if len(neither) > 0 {
		t.Errorf("%d of the %d event types are in neither versionRules nor unchangingTypes: %s",
			len(neither), len(contracts.DispatchEventTypes), strings.Join(neither, ", "))
	}
	if len(both) > 0 {
		t.Errorf("%d event types are in both versionRules and unchangingTypes: %s",
			len(both), strings.Join(both, ", "))
	}
	if len(strays) > 0 {
		t.Errorf("the rules name %d types that are no member of contracts.DispatchEventTypes: %s",
			len(strays), strings.Join(strays, ", "))
	}
	if len(unknown) > 0 {
		t.Errorf("production's log holds %d event types that contracts.DispatchEventTypes lacks: %s",
			len(unknown), strings.Join(unknown, ", "))
	}
}

// productionEventTypes reads the types of testdata/production_event_types.txt: one line per type,
// `<type><TAB><count>`, under comment lines that start with #.
func productionEventTypes(t *testing.T) []string {
	t.Helper()
	const path = "testdata/production_event_types.txt"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the types of production's log: %v", err)
	}
	var types []string
	for number, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		typ, count, ok := strings.Cut(line, "\t")
		if _, err := strconv.Atoi(count); !ok || typ == "" || err != nil {
			t.Fatalf("%s:%d is not <type><TAB><count>: %q", path, number+1, line)
		}
		types = append(types, typ)
	}
	if len(types) == 0 {
		t.Fatalf("%s names no event type", path)
	}
	return types
}

// TestEveryProductionPayloadShapeGivesItsVersion decodes the first event of each payload shape
// production's log holds of the types the fill reads (testdata/payloads/<type>-<n>.json, captured
// and redacted by `testdata/capture.sh shapes`) through the rules, as the first event of its item,
// and holds the version it starts to <type>-<n>.expected.json, which capture.sh's jq read from the
// same redacted event and never from this package's code: null there is an event that starts no
// version. It names each shape whose type has no rule.
func TestEveryProductionPayloadShapeGivesItsVersion(t *testing.T) {
	names := payloadFixtures(t)
	var noRule []string
	for _, name := range names {
		fixture := readFixtureEvent(t, name)
		want := readExpectedVersion(t, name)
		got, started, err := versionOf(fixture.event())
		switch {
		case errors.Is(err, errNoRule):
			noRule = append(noRule, name)
		case err != nil:
			t.Errorf("%s: %v", name, err)
		case want == nil && started:
			t.Errorf("%s starts the version %s; its payload starts none", name, expectedOf(got))
		case want == nil:
		case !started:
			t.Errorf("%s starts no version; its payload gives %s", name, *want)
		default:
			if differ := want.differences(expectedOf(got)); len(differ) > 0 {
				t.Errorf("%s starts a version whose %s", name, strings.Join(differ, "; "))
			}
		}
	}
	if len(noRule) > 0 {
		t.Errorf("%d of the %d payload shapes of production's log have no rule:\n%s",
			len(noRule), len(names), strings.Join(noRule, "\n"))
	}
}

// payloadFixtures names each captured event of testdata/payloads, <type>-<n> for
// <type>-<n>.json, by type and then n, and fails for one with no <type>-<n>.expected.json or the
// other way round.
func payloadFixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "payloads", "*.json"))
	if err != nil {
		t.Fatalf("list the captured payloads: %v", err)
	}
	events, expected := map[string]bool{}, map[string]bool{}
	for _, path := range paths {
		base := filepath.Base(path)
		if name, ok := strings.CutSuffix(base, ".expected.json"); ok {
			expected[name] = true
		} else {
			events[strings.TrimSuffix(base, ".json")] = true
		}
	}
	var names []string
	for name := range events {
		if !expected[name] {
			t.Errorf("testdata/payloads/%s.json has no %s.expected.json", name, name)
		}
		names = append(names, name)
	}
	for name := range expected {
		if !events[name] {
			t.Errorf("testdata/payloads/%s.expected.json has no %s.json", name, name)
		}
	}
	if len(names) == 0 {
		t.Fatal("testdata/payloads holds no captured event")
	}
	slices.SortFunc(names, func(a, b string) int {
		aType, aNumber := fixtureOrder(a)
		bType, bNumber := fixtureOrder(b)
		return cmp.Or(strings.Compare(aType, bType), cmp.Compare(aNumber, bNumber))
	})
	return names
}

// fixtureOrder splits <type>-<n> into its type and n.
func fixtureOrder(name string) (string, int) {
	at := strings.LastIndex(name, "-")
	if at < 0 {
		return name, 0
	}
	number, _ := strconv.Atoi(name[at+1:])
	return name[:at], number
}

// fixtureEvent is a captured event as GET /api/v1/events/log answers it: of its envelope, the
// columns of the event's row the rules read, and its payload.
type fixtureEvent struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	IssueKey  *string         `json:"issue_key"`
	Project   string          `json:"project"`
	Seq       int32           `json:"seq"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

func (f fixtureEvent) event() event {
	return event{
		ID:        f.ID,
		Type:      f.Type,
		IssueKey:  f.IssueKey,
		Project:   f.Project,
		Seq:       f.Seq,
		CreatedAt: f.CreatedAt,
		Payload:   f.Payload,
	}
}

func readFixtureEvent(t *testing.T, name string) fixtureEvent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "payloads", name+".json"))
	if err != nil {
		t.Fatalf("read the captured event %s: %v", name, err)
	}
	var fixture fixtureEvent
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode the captured event %s: %v", name, err)
	}
	if fixture.Type == "" || !strings.HasPrefix(name, fixture.Type+"-") {
		t.Fatalf("the captured event %s is of type %q", name, fixture.Type)
	}
	return fixture
}

// expectedVersion is <type>-<n>.expected.json: the version the payload of <type>-<n>.json
// starts, null where the payload gives no value. A body_truncated of null reads as false.
type expectedVersion struct {
	Kind           string          `json:"kind"`
	ID             string          `json:"id"`
	IssueKey       *string         `json:"issue_key"`
	TitleOrBody    *string         `json:"title_or_body"`
	Options        json.RawMessage `json:"options"`
	Answer         json.RawMessage `json:"answer"`
	SpecArtifactID *string         `json:"spec_artifact_id"`
	SpecVersion    *int32          `json:"spec_version"`
	BodyTruncated  bool            `json:"body_truncated"`
}

// readExpectedVersion reads <name>.expected.json: nil where it is null, for an event that starts
// no version.
func readExpectedVersion(t *testing.T, name string) *expectedVersion {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "payloads", name+".expected.json"))
	if err != nil {
		t.Fatalf("read the expected version of %s: %v", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var want *expectedVersion
	if err := decoder.Decode(&want); err != nil {
		t.Fatalf("decode the expected version of %s: %v", name, err)
	}
	return want
}

// expectedOf is the version v in the form of an expected one.
func expectedOf(v version) expectedVersion {
	return expectedVersion{
		Kind:           v.Kind,
		ID:             v.ID,
		IssueKey:       v.IssueKey,
		TitleOrBody:    v.Text,
		Options:        v.Options,
		Answer:         v.Answer,
		SpecArtifactID: v.SpecArtifactID,
		SpecVersion:    v.SpecVersion,
		BodyTruncated:  v.BodyTruncated,
	}
}

// fields is each field of the version with its value as canonical JSON (an absent options or
// answer as null, an object's keys sorted), which is how two versions are compared.
func (e expectedVersion) fields() [][2]string {
	return [][2]string{
		{"kind", canonicalJSON(e.Kind)},
		{"id", canonicalJSON(e.ID)},
		{"issue_key", canonicalJSON(e.IssueKey)},
		{"title_or_body", canonicalJSON(e.TitleOrBody)},
		{"options", canonicalRawJSON(e.Options)},
		{"answer", canonicalRawJSON(e.Answer)},
		{"spec_artifact_id", canonicalJSON(e.SpecArtifactID)},
		{"spec_version", canonicalJSON(e.SpecVersion)},
		{"body_truncated", canonicalJSON(e.BodyTruncated)},
	}
}

// differences names each field in which got differs from e, with both values cut to 200 bytes.
func (e expectedVersion) differences(got expectedVersion) []string {
	var differ []string
	gotFields := got.fields()
	for i, field := range e.fields() {
		if field[1] != gotFields[i][1] {
			differ = append(differ, fmt.Sprintf("%s is %s, and its payload gives %s",
				field[0], cut(gotFields[i][1]), cut(field[1])))
		}
	}
	return differ
}

func (e expectedVersion) String() string {
	parts := make([]string, 0, 9)
	for _, field := range e.fields() {
		parts = append(parts, field[0]+": "+cut(field[1]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func canonicalJSON(value any) string {
	out, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return string(out)
}

func canonicalRawJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Sprintf("<%v: %s>", err, raw)
	}
	return canonicalJSON(value)
}

func cut(text string) string {
	if len(text) <= 200 {
		return text
	}
	return text[:200] + "..."
}
