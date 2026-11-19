package outbox

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The issue.updated and issue.claimed envelopes the Legion daemon's intake decodes, shared with it
// (its TestTheSharedIssueEnvelopesCarryEveryFieldTheDaemonReads) and with packages/contracts, which
// parses each payload with the schemas it declares for the two types: the payload this package
// publishes is the one the daemon and the schemas read.
const (
	issueUpdatedFixture = "../../../../daemon/internal/intake/testdata/dispatch/issue-updated-every-field.json"
	issueClaimedFixture = "../../../../daemon/internal/intake/testdata/dispatch/issue-claimed-every-field.json"
)

// The envelope this package publishes for an issue.updated and an issue.claimed at a fixed time and
// event id is, field for field, its shared fixture. Each payload holds every field Dispatch writes
// on its type: the update moves the status, the assignee and the blockers of a handed-over child
// issue with a claim, so it carries the changed fields, the ancestors, the thread logins and the
// blocker it named, and the claim is a takeover, so it carries the claim it replaced. A field of
// either payload renamed, dropped or added changes the built envelope, and this test fails until
// the fixture, and so the daemon's and the contracts' tests that read it, say the same.
func TestTheBuiltIssueEnvelopesAreTheSharedFixtures(t *testing.T) {
	at := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	// A session's write under the token the secrets broker issued it for the project: the session
	// the token proved, the project it names and the place the request named, which every agent
	// write records as its actor and a claim stores whole.
	session := func(id, pane string) model.Actor {
		return model.Actor{Kind: "session", ID: id, Project: new("CAPTURE"), Proven: true, Origin: &model.ActorOrigin{
			Host: "worker-7", Machine: "worker-7", Cwd: "/work/widgets", Tmux: "legion", Pane: pane, SessionTitle: "CAPTURE-7 implementer",
		}}
	}
	implementer := session("ses_implementer7", "%3")
	successor := session("ses_implementer8", "%4")
	held := &model.IssueClaim{Actor: implementer, At: at("2026-11-19T06:00:00.25Z")}
	updated := model.NewIssueEventPayload(model.Issue{
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
		CreatedBy:              &model.Actor{Kind: "user", ID: "alice@example.com", Proven: true},
		CreatedAt:              at("2026-11-18T09:15:00.5Z"),
		UpdatedAt:              at("2026-11-19T06:00:00.25Z"),
		PrimaryArtifactID:      "0d6f4a52-7d1e-4c1b-9a51-3c2b8e6f7a01",
		VerificationArtifactID: "0d6f4a52-7d1e-4c1b-9a51-3c2b8e6f7a02",
		PlanArtifactID:         "0d6f4a52-7d1e-4c1b-9a51-3c2b8e6f7a03",
	}, model.ReferenceChanges{Targets: []model.ChangedReference{{Kind: "issue", ID: "CAPTURE-5"}}})
	updated.Changed = []string{"status", "assignee", "blocked_by"}
	updated.Ancestors = []string{"CAPTURE-1"}
	updated.ThreadLogins = []string{"alice@example.com", "bob@example.com"}
	claimedAt := at("2026-11-19T06:31:00.654321Z")
	for _, tc := range []struct {
		name    string
		fixture string
		event   model.Event
	}{
		{
			name:    "issue.updated",
			fixture: issueUpdatedFixture,
			event: model.Event{
				ID: 4821, IssueKey: new("CAPTURE-7"), Project: "CAPTURE", Seq: 9, Type: "issue.updated",
				Actor: implementer, CreatedAt: at("2026-11-19T06:30:00.123456Z"), Payload: updated,
			},
		},
		{
			name:    "issue.claimed",
			fixture: issueClaimedFixture,
			event: model.Event{
				ID: 4822, IssueKey: new("CAPTURE-7"), Project: "CAPTURE", Seq: 10, Type: "issue.claimed",
				Actor: successor, CreatedAt: claimedAt,
				Payload: model.IssueClaimEventPayload{
					Key: "CAPTURE-7", Status: "in_progress", ResolvedRepo: new("acme/widgets"),
					Claim: &model.IssueClaim{Actor: successor, At: claimedAt}, Previous: held, Reason: "takeover",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built := publishedEnvelope(t, tc.event)
			want, err := json.MarshalIndent(built, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("read the shared fixture: %v\nthe built envelope is:\n%s", err, want)
			}
			var fixture contracts.Envelope
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatalf("decode the shared fixture: %v", err)
			}
			if !reflect.DeepEqual(built, fixture) {
				t.Fatalf("built %s envelope differs from %s\nbuilt payload:   %s\nfixture payload: %s\nthe built envelope is:\n%s",
					tc.name, tc.fixture, built.Payload, fixture.Payload, want)
			}
		})
	}
}

// publishedEnvelope is the envelope the outbox publishes for event as an append stores it: the
// broker's Notify and the payload's last_seq (events.Broker.QueueAppend), and the payload read back
// out of the events table as the outbox's batch reads it, into an untyped value, before envelope
// encodes it.
func publishedEnvelope(t *testing.T, event model.Event) contracts.Envelope {
	t.Helper()
	event.Notify = new(events.Broker).Notify(event)
	if carrier, ok := event.Payload.(interface{ WithOwnerLastSeq(int) any }); ok {
		event.Payload = carrier.WithOwnerLastSeq(event.Seq)
	}
	stored, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	event.Payload = nil
	if err := json.Unmarshal(stored, &event.Payload); err != nil {
		t.Fatal(err)
	}
	built, err := envelope(event, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := built.Validate(); err != nil {
		t.Fatalf("validate the built envelope: %v", err)
	}
	return built
}
