package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// sharedEnvelopes is the directory of the issue event envelopes the Legion daemon's intake
// decodes, shared with it (its TestTheSharedIssueEnvelopesCarryEveryFieldTheDaemonReads) and with
// packages/contracts, which parses each with the schemas it declares: the envelope this package
// publishes is the one the daemon and the schemas read.
const sharedEnvelopes = "../../../../daemon/internal/intake/testdata/dispatch/"

// sharedIssueEnvelope is one shared example: a write an issue handler makes, as it hands it to the
// event broker - the event, at the id and the time the example fixes, and the logins the event's
// own insert reads (events.Broker.QueueAppendWithLogins; nil for none) - and the row of its issue
// that the insert takes the event's sequence from, at the last_seq it held before the write.
type sharedIssueEnvelope struct {
	file    string
	number  int
	lastSeq int
	logins  *events.Logins
	event   model.Event
}

// sharedIssueEnvelopes is every shared example. Together they hold each field Dispatch writes on
// these events, in each form it writes it in (requireEveryWrittenField):
//
//   - issue-updated-every-field.json: a handed-over child, held by a session, whose patch moves
//     its status, its assignee and a blocker, so it names the blocker and the two assignees.
//   - issue-claimed-every-field.json: a takeover of that child, so it carries the claim it replaced.
//   - issue-updated-no-field-moved.json: a patch of the child's `advances` alone, which is no
//     field `changed` names, so `changed` is [].
//   - issue-updated-null-fields.json: the reopen of a root nobody holds, assigns or routes, with
//     no priority and no repository, so each of those is null; its thread_logins are what the
//     reopen's own insert reads, [] for an issue with no open ask.
//   - issue-released-null-claim.json: its holder's release of that root, so the claim is null.
//
// The actors are the three a broker-issued token records (api.brokerTokenActor): the token names
// a person or a service, never both and never neither, and a project only beside a person
// (brokertoken.sessionCaller). implementer7 is a fleet's session, whose token names the service
// its machine is; implementer8 acts for a person under a token an operator mapped to one project;
// architect3 acts for a person under a token the broker scoped to its person, which names no
// project, and its requests name no place. The issues' creator is a person at a browser.
func sharedIssueEnvelopes(t *testing.T) []sharedIssueEnvelope {
	at := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	origin := func(pane string) *model.ActorOrigin {
		return &model.ActorOrigin{Host: "worker-7", Machine: "worker-7", Cwd: "/work/widgets", Tmux: "legion", Pane: pane, SessionTitle: "CAPTURE-7 implementer"}
	}
	implementer7 := model.Actor{Kind: "session", ID: "ses_implementer7", Origin: origin("%3"), Service: new("legion-fleet"), Proven: true}
	implementer8 := model.Actor{Kind: "session", ID: "ses_implementer8", Origin: origin("%4"), Owner: new("alice@example.com"), Project: new("CAPTURE"), Proven: true}
	architect3 := model.Actor{Kind: "session", ID: "ses_architect3", Owner: new("bob@example.com"), Proven: true}
	creator := &model.Actor{Kind: "user", ID: "alice@example.com", Proven: true}
	held := &model.IssueClaim{Actor: implementer7, At: at("2026-11-19T06:00:00.25Z")}
	taken := &model.IssueClaim{Actor: implementer8, At: at("2026-11-19T06:31:00.654321Z")}

	// CAPTURE-7, a child of CAPTURE-1, whose repository it inherits: the project maps none.
	child := model.Issue{
		Key: "CAPTURE-7", Project: "CAPTURE", Number: 7, Title: "Read every field Dispatch writes",
		Status: "in_progress", Priority: new(1), Rank: "UUUk", Labels: []string{"legion"},
		Parent: new("CAPTURE-1"), BlockedBy: []string{"CAPTURE-2", "CAPTURE-5"},
		Advances:               []model.IssueAdvance{{ID: "contracts-hold-both-sides", Inherited: false}},
		AdvancesRevision:       3,
		Assignee:               new("bob@example.com"),
		Claim:                  held,
		Components:             model.IssueComponents{Mode: "explicit", IDs: []string{"dispatch-outbox"}, Unknown: []string{}},
		ExternalLinks:          []model.ExternalLink{{URL: "https://github.com/acme/widgets/issues/12", Kind: "github"}},
		Route:                  new("role:legion-implementer"),
		ResolvedRepo:           new("acme/widgets"),
		CreatedBy:              creator,
		CreatedAt:              at("2026-11-18T09:15:00.5Z"),
		UpdatedAt:              at("2026-11-19T06:30:00.123456Z"),
		PrimaryArtifactID:      "0d6f4a52-7d1e-4c1b-9a51-3c2b8e6f7a01",
		VerificationArtifactID: "0d6f4a52-7d1e-4c1b-9a51-3c2b8e6f7a02",
		PlanArtifactID:         "0d6f4a52-7d1e-4c1b-9a51-3c2b8e6f7a03",
	}
	moved := model.NewIssueEventPayload(child, model.ReferenceChanges{Targets: []model.ChangedReference{{Kind: "issue", ID: "CAPTURE-5"}}})
	moved.Changed = []string{"status", "assignee", "blocked_by"}
	moved.Ancestors = []string{"CAPTURE-1"}
	// An assignee move names the person the issue's questions reached before it and the one
	// they reach after it, which the handler knows (api.movedLogins).
	moved.ThreadLogins = []string{"alice@example.com", "bob@example.com"}

	advanced := child
	advanced.Claim = taken
	advanced.Advances = []model.IssueAdvance{{ID: "contracts-hold-both-sides", Inherited: false}, {ID: "shared-examples", Inherited: false}}
	advanced.AdvancesRevision = 4
	advanced.UpdatedAt = at("2026-11-19T06:32:00.5Z")
	noneMoved := model.NewIssueEventPayload(advanced, model.ReferenceChanges{})
	// api.issueChangedFields names no `advances`, so an advances-only patch moved nothing it names.
	noneMoved.Changed = []string{}
	noneMoved.Ancestors = []string{"CAPTURE-1"}

	// CAPTURE-9, a root nobody holds, assigns or routes, with no priority, label, blocker,
	// priority it advances, component, link or repository: each as an issue read gives it
	// (api.scanIssueRow), the lists [] and the rest null.
	root := model.Issue{
		Key: "CAPTURE-9", Project: "CAPTURE", Number: 9, Title: "Reopen a root nobody holds", Status: "todo",
		Rank: "UUUm", Labels: []string{}, BlockedBy: []string{}, Advances: []model.IssueAdvance{},
		Components:    model.IssueComponents{Mode: "inherit", IDs: []string{}, Unknown: []string{}},
		ExternalLinks: []model.ExternalLink{},
		CreatedBy:     creator,
		CreatedAt:     at("2026-11-17T14:00:00Z"),
		UpdatedAt:     at("2026-11-19T05:10:00.5Z"),
		// The primary document every create makes.
		PrimaryArtifactID: "0d6f4a52-7d1e-4c1b-9a51-3c2b8e6f7a09",
	}
	reopened := model.NewIssueEventPayload(root, model.ReferenceChanges{})
	reopened.Changed = []string{"status", "closed_at"}
	reopened.Ancestors = []string{}

	return []sharedIssueEnvelope{
		{
			file: "issue-updated-every-field.json", number: 7, lastSeq: 8,
			event: model.Event{ID: 4821, IssueKey: new("CAPTURE-7"), Type: "issue.updated", Actor: implementer7,
				CreatedAt: at("2026-11-19T06:30:00.123456Z"), Payload: moved},
		},
		{
			file: "issue-claimed-every-field.json", number: 7, lastSeq: 9,
			event: model.Event{ID: 4822, IssueKey: new("CAPTURE-7"), Type: "issue.claimed", Actor: implementer8,
				CreatedAt: taken.At, Payload: model.IssueClaimEventPayload{
					Key: "CAPTURE-7", Status: "in_progress", ResolvedRepo: new("acme/widgets"), Claim: taken, Previous: held, Reason: "takeover",
				}},
		},
		{
			file: "issue-updated-no-field-moved.json", number: 7, lastSeq: 10,
			event: model.Event{ID: 4823, IssueKey: new("CAPTURE-7"), Type: "issue.updated", Actor: implementer8,
				CreatedAt: at("2026-11-19T06:32:00.5Z"), Payload: noneMoved},
		},
		{
			// A reopen's thread_logins are read by its own insert, after the asks the reopen
			// restored (api.patchIssue, events.IssueLoginsOf).
			file: "issue-updated-null-fields.json", number: 9, lastSeq: 3, logins: events.IssueLoginsOf("CAPTURE-9"),
			event: model.Event{ID: 4801, IssueKey: new("CAPTURE-9"), Type: "issue.updated", Actor: architect3,
				CreatedAt: at("2026-11-19T05:10:00.5Z"), Payload: reopened},
		},
		{
			file: "issue-released-null-claim.json", number: 9, lastSeq: 5,
			event: model.Event{ID: 4803, IssueKey: new("CAPTURE-9"), Type: "issue.released", Actor: architect3,
				CreatedAt: at("2026-11-19T05:20:00.75Z"), Payload: model.IssueClaimEventPayload{
					Key: "CAPTURE-9", Status: "todo", Previous: &model.IssueClaim{Actor: architect3, At: at("2026-11-19T05:15:00Z")}, Reason: "released",
				}},
		},
	}
}

// The envelope this package publishes for each shared example is, field for field, its file, and
// the examples together hold every field Dispatch writes on these events. Each envelope is built as
// production builds it: the event is appended through the broker's insert, which writes the stored
// payload's last_seq and a reopen's thread_logins in SQL, read back by the outbox's batch read, and
// encoded by envelope. So a field of the payload renamed, dropped or added changes a built envelope
// or the walk of the examples, and this test fails until the files, and so the daemon's and the
// contracts' tests that read them, say the same.
func TestTheBuiltIssueEnvelopesAreTheSharedFixtures(t *testing.T) {
	examples := sharedIssueEnvelopes(t)
	t.Run("the examples hold every field Dispatch writes", func(t *testing.T) {
		requireEveryWrittenField(t, examples)
	})
	for _, example := range examples {
		t.Run(example.file, func(t *testing.T) {
			built := appendAndPublish(t, example)
			want, err := json.MarshalIndent(built, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(sharedEnvelopes + example.file)
			if err != nil {
				t.Fatalf("read the shared fixture: %v\nthe built envelope is:\n%s", err, want)
			}
			var fixture contracts.Envelope
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatalf("decode the shared fixture: %v", err)
			}
			if !reflect.DeepEqual(built, fixture) {
				t.Fatalf("built %s envelope differs from its fixture\nbuilt payload:   %s\nfixture payload: %s\nthe built envelope is:\n%s",
					example.event.Type, built.Payload, fixture.Payload, want)
			}
		})
	}
}

// appendAndPublish appends example's event as its handler does, through the broker's insert, on a
// database holding the event's issue at the last_seq it held before the write, and answers the
// envelope the outbox publishes for it: the event as the outbox's batch reads it back out of the
// events table (readBatch), encoded by envelope. The id and the time are the example's: the id is
// the next value of the events table's sequence, and the time, which the insert takes from the
// database's clock, is set on the row before the batch reads it.
func appendAndPublish(t *testing.T, example sharedIssueEnvelope) contracts.Envelope {
	t.Helper()
	ctx := context.Background()
	database := storetest.Open(t)
	key := *example.event.IssueKey
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`insert into projects (key, name, default_assignee) values ('CAPTURE', 'Capture', 'alice@example.com')`, nil},
		{`insert into issues (key, project_key, number, title, created_by, rank, last_seq)
			values ($1, 'CAPTURE', $2, $1, '{"kind":"user","id":"alice@example.com","proven":true}', 'U', $3)`,
			[]any{key, example.number, example.lastSeq}},
		{`select setval(pg_get_serial_sequence('events', 'id'), $1)`, []any{example.event.ID - 1}},
	} {
		if _, err := database.Pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.sql, err)
		}
	}
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the write: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := store.RunWrite(ctx, tx, func(batch *store.WriteBatch) error {
		return events.NewBroker().QueueAppendWithLogins(batch, example.event, example.logins, func(model.Event) {})
	}); err != nil {
		t.Fatalf("append the event: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the write: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `update events set created_at = $1 where id = $2`, example.event.CreatedAt, example.event.ID); err != nil {
		t.Fatalf("fix the event's time: %v", err)
	}
	pending, stopped, _, err := readBatch(ctx, Deps{Store: database})
	if err != nil || stopped {
		t.Fatalf("read the batch: stopped=%v err=%v", stopped, err)
	}
	if len(pending) != 1 || pending[0].failure != nil || pending[0].event.ID != example.event.ID {
		t.Fatalf("the batch read %d events (first %+v), want the one appended, id %d", len(pending), pending, example.event.ID)
	}
	built, err := envelope(pending[0].event, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := built.Validate(); err != nil {
		t.Fatalf("validate the built envelope: %v", err)
	}
	return built
}

// writtenPayloads is the payload type the issue handlers write for each event type the examples
// hold (api.patchIssue, api.claimEvent).
var writtenPayloads = map[string]reflect.Type{
	"issue.updated":  reflect.TypeFor[model.IssueEventPayload](),
	"issue.claimed":  reflect.TypeFor[model.IssueClaimEventPayload](),
	"issue.released": reflect.TypeFor[model.IssueClaimEventPayload](),
}

// neverWritten is each field of the types these envelopes are built from that Dispatch never
// writes on an issue.updated, issue.claimed or issue.released published on its issue's topic,
// with the reason. Every other field must be in an example.
var neverWritten = map[string]string{
	"model.Event.trees": "the outbox publishes the event log's row, which holds none: trees ride only on an API " +
		"answer to a caller that sent Dispatch-Trees: 1 and on /ws/live frames (model.Event.Trees)",
	"model.IssueEventPayload.forced": "it is on an issue.created alone (model.IssueEventPayload)",
	"model.ForcedCreate.past":        "it is the list model.IssueEventPayload.forced carries",
	"model.ReferenceChangesPayload.references_changed_truncated": "an issue.updated names at most 41 issues: its " +
		"parent and its blockers before and after the patch (api.issueEdgeChanges), each list at most " +
		"contracts.MaxIssueBlockers (20) long, and refs.CountedChanges truncates a kind only past " +
		"refs.CountedChangeLimit (100); a claim or a release names none",
	"model.ReferenceChangesPayload.references_changed_truncated_kinds": "as references_changed_truncated",
	"model.ChangedReference.issue_key": "an issue write names issue targets alone (api.issueEdgeChanges), and the " +
		"address fields are an ask's and a document's (model.ChangedReference)",
	"model.ChangedReference.artifact_id": "as model.ChangedReference.issue_key",
	"model.ChangedReference.ref":         "as model.ChangedReference.issue_key",
	"model.Actor.act": "actingPerson sets it (api/server.go), and no issue handler calls it: a create, a patch and " +
		"a claim record requireActor's actor, a release optionalActor's or bearerSessionActor's",
	"model.ActorAct.session":           "it is the act model.Actor.act carries",
	"model.ActorAct.session_proven":    "it is the act model.Actor.act carries",
	"model.ActorAct.token_id":          "it is the act model.Actor.act carries",
	"model.ActorAct.token_name":        "it is the act model.Actor.act carries",
	"model.ActorAct.scopes":            "it is the act model.Actor.act carries",
	"listenerwire.Envelope.expires_at": "envelope sets none",
	"listenerwire.Envelope.kept_at": "the outbox sets it on the copy of a role's route it kept for a holder (role.go), " +
		"never on the envelope envelope builds, the issue topic's copy these examples are",
	"listenerwire.Envelope.mention_dedupe_keys": "as listenerwire.Envelope.kept_at",
	"listenerwire.Envelope.payload_ref":         "envelope sets none",
	"listenerwire.Envelope.sender":              "envelope sets none",
	"listenerwire.Envelope.in_reply_to":         "envelope sets it on an ask.answered, an ask.resolved, a comment and a message alone",
	"listenerwire.Envelope.supersedes":          "envelope sets none",
	"listenerwire.Envelope.urgency":             "envelope sets it on an ask.* event alone",
	"listenerwire.Envelope.expects_reply":       "envelope leaves it out of every event's envelope, for the reason its comment gives",
	"listenerwire.EnvelopeSender.session_id":    "it is the sender envelope never sets",
	"listenerwire.EnvelopeSender.machine":       "it is the sender envelope never sets",
	"listenerwire.EnvelopeSender.cwd":           "it is the sender envelope never sets",
	"listenerwire.EnvelopeSender.title":         "it is the sender envelope never sets",
	"listenerwire.EnvelopeSender.roles":         "it is the sender envelope never sets",
}

// neverEmpty is each field Go writes as null or [] when it holds nothing that never holds nothing
// on these events, with the reason. Every other such field must be in an example in that form.
var neverEmpty = map[string]string{
	"model.Event.issue_key": "an issue event is its issue's: envelope publishes it on that issue's topic",
	"model.Event.payload":   "every event the issue handlers append carries its payload (writtenPayloads)",
	"model.Issue.created_by": "every issue holds its creator: issues.created_by is jsonb not null from migration 0001, " +
		"and createIssue writes the creating actor",
}

// requireEveryWrittenField fails t for each field of the types these envelopes are built from that
// no example carries, and for each field Go writes as null or [] when it holds nothing that no
// example carries in that form, unless neverWritten or neverEmpty names it with its reason. The
// types are contracts.Envelope, its payload as envelope encodes it (publishedEvent) and that
// event's payload as its handler writes it (writtenPayloads), nested types and embedded ones
// included. So a field Dispatch writes only when it is set - a person's or a service's credential,
// an omitempty field added tomorrow - fails here until an example sets it, and a field written as
// null or [] fails until an example holds nothing there, where an omitempty added to it would drop
// the key. An example that carries a key no type declares, or a value Go leaves out (a zero value
// under omitempty, a nil one under omitzero), fails too: the examples are what Go writes.
func requireEveryWrittenField(t *testing.T, examples []sharedIssueEnvelope) {
	walk := fieldWalk{
		declared: map[string]writtenField{}, visited: map[reflect.Type]bool{},
		carried: map[string]bool{}, carriedEmpty: map[string]bool{},
	}
	walk.declare(walkedEnvelope)
	for _, example := range examples {
		data, err := os.ReadFile(sharedEnvelopes + example.file)
		if err != nil {
			t.Fatalf("read the shared fixture: %v", err)
		}
		var envelope any
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode %s: %v", example.file, err)
		}
		walk.value(t, example.file, walkedEnvelope, envelope)
	}
	for _, list := range []struct {
		name    string
		entries map[string]string
	}{{"neverWritten", neverWritten}, {"neverEmpty", neverEmpty}} {
		for key := range list.entries {
			field, ok := walk.declared[key]
			switch {
			case !ok:
				t.Errorf("%s names %s, which no type these envelopes are built from declares", list.name, key)
			case list.name == "neverEmpty" && field.empty == "":
				t.Errorf("neverEmpty names %s, which Go leaves out when it holds nothing", key)
			}
		}
	}
	keys := make([]string, 0, len(walk.declared))
	for key := range walk.declared {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		field := walk.declared[key]
		if _, never := neverWritten[key]; never {
			if walk.carried[key] {
				t.Errorf("an example carries %s, which neverWritten says these events never carry: drop it from the list", key)
			}
			continue
		}
		if !walk.carried[key] {
			t.Errorf("Dispatch writes %s and no example carries it: add an example that sets it, or name it in neverWritten with the reason these events cannot", key)
		}
		if field.empty == "" {
			continue
		}
		_, full := neverEmpty[key]
		switch {
		case full && walk.carriedEmpty[key]:
			t.Errorf("an example carries %s as %s, which neverEmpty says these events never do: drop it from the list", key, field.empty)
		case !full && !walk.carriedEmpty[key]:
			t.Errorf("Dispatch writes %s as %s when it holds nothing and no example carries it so: add an example that holds nothing there, or name it in neverEmpty with the reason these events always hold one", key, field.empty)
		}
	}
}

// writtenField is one JSON field a type these envelopes are built from declares: its Go type, and
// how Go writes it when it holds nothing and keeps the key - "null" for a pointer, slice, map or
// interface with neither omitempty nor omitzero (a nil slice is null, an empty one []), "[]" for a
// slice under omitzero alone, "" where an empty value drops the key or is a string, number or bool.
// omit is the tag's omitempty or omitzero, "" for neither.
type writtenField struct {
	typ   reflect.Type
	empty string
	omit  string
}

// fieldWalk is requireEveryWrittenField's walk: every field the types declare, by owner and JSON
// name ("model.Actor.owner"), and which of them the examples carry, and carry empty.
type fieldWalk struct {
	declared     map[string]writtenField
	visited      map[reflect.Type]bool
	carried      map[string]bool
	carriedEmpty map[string]bool
}

var (
	walkedEnvelope       = reflect.TypeFor[contracts.Envelope]()
	walkedPublishedEvent = reflect.TypeFor[publishedEvent]()
	walkedEvent          = reflect.TypeFor[model.Event]()
	walkedMarshaler      = reflect.TypeFor[json.Marshaler]()
)

// jsonFields calls visit for each field encoding/json writes of struct type typ: by the struct that
// declares it, so an embedded struct's fields are its own, with the field's JSON name and its
// omitempty or omitzero option.
func jsonFields(typ reflect.Type, visit func(owner reflect.Type, field reflect.StructField, name, omit string)) {
	for field := range typ.Fields() {
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" && options == "" {
			continue
		}
		if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
			jsonFields(field.Type, visit)
			continue
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		omit := ""
		for option := range strings.SplitSeq(options, ",") {
			if option == "omitempty" || option == "omitzero" {
				omit = option
			}
		}
		visit(typ, field, name, omit)
	}
}

// encodedWhole is whether encoding/json writes a value of typ by its own MarshalJSON (time.Time,
// json.RawMessage), which this walk does not look into.
func encodedWhole(typ reflect.Type) bool {
	return typ.Implements(walkedMarshaler) || reflect.PointerTo(typ).Implements(walkedMarshaler)
}

// heldTypes is the types a field's value is written from: its own, except an envelope's payload,
// which is the published event encoded as a string, and an event's payload, which is the
// payload its handler writes for the event's type.
func heldTypes(owner reflect.Type, name string, typ reflect.Type) []reflect.Type {
	switch {
	case owner == walkedEnvelope && name == "payload":
		return []reflect.Type{walkedPublishedEvent}
	case owner == walkedEvent && name == "payload":
		var payloads []reflect.Type
		for _, payload := range writtenPayloads {
			if !slices.Contains(payloads, payload) {
				payloads = append(payloads, payload)
			}
		}
		return payloads
	}
	return []reflect.Type{typ}
}

// declare records every field typ declares, and those of every type a field of it holds.
func (w *fieldWalk) declare(typ reflect.Type) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
		if encodedWhole(typ) {
			return
		}
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || encodedWhole(typ) || w.visited[typ] {
		return
	}
	w.visited[typ] = true
	jsonFields(typ, func(owner reflect.Type, field reflect.StructField, name, omit string) {
		empty := ""
		switch kind := field.Type.Kind(); {
		case omit == "" && (kind == reflect.Pointer || kind == reflect.Slice || kind == reflect.Map || kind == reflect.Interface):
			empty = "null"
		case omit == "omitzero" && kind == reflect.Slice:
			empty = "[]"
		}
		w.declared[owner.String()+"."+name] = writtenField{typ: field.Type, empty: empty, omit: omit}
		for _, held := range heldTypes(owner, name, field.Type) {
			w.declare(held)
		}
	})
}

// value walks one example's value at path, written from typ, recording each field it carries.
func (w *fieldWalk) value(t *testing.T, path string, typ reflect.Type, value any) {
	t.Helper()
	if value == nil || encodedWhole(typ) {
		return
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			t.Errorf("%s is %T, where %s writes a list", path, value, typ)
			return
		}
		for index, item := range items {
			w.value(t, fmt.Sprintf("%s[%d]", path, index), typ.Elem(), item)
		}
		return
	case reflect.Map:
		entries, ok := value.(map[string]any)
		if !ok {
			t.Errorf("%s is %T, where %s writes an object", path, value, typ)
			return
		}
		for name, entry := range entries {
			w.value(t, path+"."+name, typ.Elem(), entry)
		}
		return
	case reflect.Struct:
	default:
		return
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Errorf("%s is %T, where %s writes an object", path, value, typ)
		return
	}
	known := map[string]bool{}
	jsonFields(typ, func(owner reflect.Type, field reflect.StructField, name, omit string) {
		known[name] = true
		held, ok := object[name]
		if !ok {
			return
		}
		key := owner.String() + "." + name
		w.carried[key] = true
		if dropped(held, omit) {
			t.Errorf("%s.%s is %s, which Go leaves out of what it writes (%s %s)", path, name, mustJSON(held), key, omit)
		}
		if held == nil || isEmptyList(held) {
			w.carriedEmpty[key] = true
		}
		switch {
		case owner == walkedEnvelope && name == "payload":
			text, _ := held.(string)
			var event any
			if err := json.Unmarshal([]byte(text), &event); err != nil {
				t.Errorf("%s.payload is not the event's JSON: %v", path, err)
				return
			}
			w.value(t, path+".payload", walkedPublishedEvent, event)
		case owner == walkedEvent && name == "payload":
			typeName, _ := object["type"].(string)
			payload, ok := writtenPayloads[typeName]
			if !ok {
				t.Errorf("%s is a %q, which writtenPayloads names no payload type for", path, typeName)
				return
			}
			w.value(t, path+".payload", payload, held)
		default:
			w.value(t, path+"."+name, field.Type, held)
		}
	})
	for name := range object {
		if !known[name] {
			t.Errorf("%s holds %q, which %s declares no field for", path, name, typ)
		}
	}
}

// dropped is whether encoding/json leaves value out of what it writes under omit: omitempty leaves
// out null, false, 0, "" and an empty list or object; omitzero null, false, 0 and "", keeping an
// empty list, which is not a slice's zero value.
func dropped(value any, omit string) bool {
	switch omit {
	case "omitempty":
		return isZeroScalar(value) || isEmptyList(value) || isEmptyObject(value)
	case "omitzero":
		return isZeroScalar(value)
	}
	return false
}

func isZeroScalar(value any) bool {
	return value == nil || value == false || value == float64(0) || value == ""
}

func isEmptyList(value any) bool {
	items, ok := value.([]any)
	return ok && len(items) == 0
}

func isEmptyObject(value any) bool {
	entries, ok := value.(map[string]any)
	return ok && len(entries) == 0
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}
