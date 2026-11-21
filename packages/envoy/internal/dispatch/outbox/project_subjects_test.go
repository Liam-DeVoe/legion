package outbox

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/roles"
	"github.com/sjawhar/envoy/internal/session"
	envoystore "github.com/sjawhar/envoy/internal/store"
)

// subjectRecorder is a test publisher that records the subject of every send the outbox makes, a
// publish's (its envelope's topic) and a role forward's (the subject the Request goes to) alike,
// with the envelope's own topic beside it.
type subjectRecorder struct {
	publishOnly
	mu    sync.Mutex
	sends []recordedSend
}

// recordedSend is one send: the subject it went out on and the topic its envelope carries.
type recordedSend struct {
	subject, topic, event string
}

func (p *subjectRecorder) WindowUntil(deadline func() time.Time) bus.Sender {
	return deadlineWindow{Sender: p, deadline: deadline}
}

func (p *subjectRecorder) PublishAsync(item contracts.Envelope) <-chan error {
	if !bus.StreamCaptures(item.Topic) {
		return answered(fmt.Errorf("%w: %q", bus.ErrNotCaptured, item.Topic))
	}
	p.record(recordedSend{subject: item.Topic, topic: item.Topic, event: item.SourceEventID})
	return answered(nil)
}

func (p *subjectRecorder) Request(subject string, item contracts.Envelope, _ bus.RequestOptions) <-chan error {
	p.record(recordedSend{subject: subject, topic: item.Topic, event: item.SourceEventID})
	return answered(nil)
}

func (p *subjectRecorder) record(send recordedSend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends = append(p.sends, send)
}

func (p *subjectRecorder) all() []recordedSend {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sends)
}

// missingTheirProject is every subject of names that does not name project as a token of its own
// (contracts.SubjectProject, contracts.CanonicalTopic): an issue event named by its key alone, a
// session's message or a role's lane under no project, or any of them under another project.
func missingTheirProject(names []string, project string) []string {
	var missing []string
	for _, name := range names {
		named, ok := contracts.SubjectProject(name)
		if _, legacy := contracts.CanonicalTopic(name); !ok || legacy || named != project {
			missing = append(missing, name)
		}
	}
	return missing
}

// projectFamilies are the families whose subject the outbox puts the event's project in, each by
// its prefix: an issue's topic, a session's own subject and a role's lane.
var projectFamilies = []string{contracts.DispatchIssueTopicPrefix, contracts.AgentTopicPrefix, contracts.RoleTopicPrefix}

// onBus is the subject the copy a record names went out on for an event of project: the record
// (model.IssueDestinationKey, SessionDestinationKey, RoleDestinationKey) with the project put after
// its family's prefix, as a test writes it out by hand. A Legion role's lane, whose project is its
// own name's (roleProject), is written out by its test instead.
func onBus(project string, records ...string) []string {
	subjects := make([]string, len(records))
	for i, record := range records {
		subjects[i] = record
		for _, prefix := range projectFamilies {
			if rest, ok := strings.CutPrefix(record, prefix); ok {
				subjects[i] = prefix + project + "." + rest
				break
			}
		}
	}
	return subjects
}

// recordOf is the record of the copy each subject names (model.*DestinationKey): the subject with
// its project token taken out, as published_destinations, owed_keys and comment_fallback_copies
// hold it.
func recordOf(subjects ...string) []string {
	records := make([]string, len(subjects))
	for i, subject := range subjects {
		records[i] = subject
		for _, prefix := range projectFamilies {
			if rest, ok := strings.CutPrefix(subject, prefix); ok {
				if _, after, found := strings.Cut(rest, "."); found {
					records[i] = prefix + after
				}
				break
			}
		}
	}
	return records
}

// TestEveryOutboxSubjectNamesItsProject: PUB's issues are routed to a free-form role, to a session
// and to a Legion role of another deployment, and each has a message. Every subject the outbox
// sends on names the project of the event it carries: the issue's topic is
// notifications.dispatch.issue.PUB.<KEY>.message.created, the session's copy goes to
// notifications.agent.PUB.s-route, and each role route's forward goes to
// notifications.agent.PUB.role-holder. The topic a role forward carries names the project the role
// works for: PUB for the free-form role, and OTHER, its own, for the Legion role of OTHER, whose
// claim is only ever made under that project (contracts.ClaimProject).
//
// Control, in the test: the same subjects in the form the record names them, with no project
// (contracts.LegacyTwin), fail the check, each named, so the check can see a subject with no
// project.
func TestEveryOutboxSubjectNamesItsProject(t *testing.T) {
	database := storetest.Open(t)
	broker := events.NewBroker()
	ctx := context.Background()
	const legionRole = "legion-other-pub-3-architect"
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee, owner) values ('PUB', 'PUB', 'alice', 'alice')
	`); err != nil {
		t.Fatalf("create project PUB: %v", err)
	}
	routes := map[string]string{"PUB-1": "role:reviewer", "PUB-2": "session:s-route", "PUB-3": "role:" + legionRole}
	for number, key := range []string{"PUB-1", "PUB-2", "PUB-3"} {
		if _, err := database.Pool.Exec(ctx, `
			insert into issues (key, project_key, number, title, route, created_by, rank)
			values ($1, 'PUB', $2, $1, $3, '{"kind":"user","id":"alice"}', 'U')
		`, key, number+1, routes[key]); err != nil {
			t.Fatalf("create issue %s: %v", key, err)
		}
	}
	byEvent := map[string]string{}
	for _, key := range []string{"PUB-1", "PUB-2", "PUB-3"} {
		byEvent[fmt.Sprint(appendHoldMessage(t, database, broker, key).ID)] = key
	}

	publisher := &subjectRecorder{}
	deps := withHeldRoles(Deps{Store: database, Publisher: publisher, Broker: broker})
	if _, err := scanBatch(ctx, deps, unfenced{}, holding(t, deps).instance); err != nil {
		t.Fatalf("scan the batch: %v", err)
	}

	sent := publisher.all()
	var subjects []string
	got := map[string][]recordedSend{}
	for _, send := range sent {
		subjects = append(subjects, send.subject)
		got[byEvent[send.event]] = append(got[byEvent[send.event]], send)
	}
	if missing := missingTheirProject(subjects, "PUB"); len(missing) != 0 || len(subjects) == 0 {
		t.Fatalf("the outbox sent on %v, of which %v name no project or not PUB, want every one to name PUB", subjects, missing)
	}
	want := map[string][]recordedSend{
		"PUB-1": {
			{subject: contracts.DispatchIssueSubject("PUB", "PUB-1", "message.created"), topic: contracts.DispatchIssueSubject("PUB", "PUB-1", "message.created")},
			{subject: contracts.AgentSubject("PUB", roleHolder), topic: contracts.RoleSubject("PUB", "reviewer")},
		},
		"PUB-2": {
			{subject: contracts.DispatchIssueSubject("PUB", "PUB-2", "message.created"), topic: contracts.DispatchIssueSubject("PUB", "PUB-2", "message.created")},
			{subject: contracts.AgentSubject("PUB", "s-route"), topic: contracts.AgentSubject("PUB", "s-route")},
		},
		"PUB-3": {
			{subject: contracts.DispatchIssueSubject("PUB", "PUB-3", "message.created"), topic: contracts.DispatchIssueSubject("PUB", "PUB-3", "message.created")},
			{subject: contracts.AgentSubject("PUB", roleHolder), topic: contracts.RoleSubject("OTHER", legionRole)},
		},
	}
	for key, sends := range want {
		var have []string
		for _, send := range got[key] {
			have = append(have, send.subject+" (topic "+send.topic+")")
		}
		var wanted []string
		for _, send := range sends {
			wanted = append(wanted, send.subject+" (topic "+send.topic+")")
		}
		slices.Sort(have)
		slices.Sort(wanted)
		if !slices.Equal(have, wanted) {
			t.Errorf("%s's message went out on %v, want %v", key, have, wanted)
		}
	}

	var recordForm []string
	for _, subject := range subjects {
		if twin, ok := contracts.LegacyTwin(subject); ok {
			recordForm = append(recordForm, twin)
		}
	}
	if missing := missingTheirProject(recordForm, "PUB"); len(recordForm) == 0 || len(missing) != len(recordForm) {
		t.Fatalf("control: the check passed %d of the %d subjects in the form with no project (%v), want it to fail every one of them",
			len(recordForm)-len(missing), len(recordForm), strings.Join(recordForm, ", "))
	}
}

// TestADestinationKeyIsTheOneTheRecordAlreadyHolds (model.IssueDestinationKey,
// SessionDestinationKey, RoleDestinationKey): a key this build makes is byte for byte the one a
// release before it stored, and an event whose published_destinations a release before this one
// wrote is read as that release left it. PUB-1 is routed to session s1, and its message's record
// holds the issue's topic family, reached, as that release wrote it, in the form with no project.
// The batch sends the message to s1 alone, on s1's subject under PUB, and records s1's key beside
// the stored one: the topic family is not sent again.
//
// Control, in the test: the bus subject of each key, used as the key, is not what the record
// holds, so a build that recorded and read its bus subjects would send the topic family again.
func TestADestinationKeyIsTheOneTheRecordAlreadyHolds(t *testing.T) {
	stored := map[string]string{
		model.IssueDestinationKey("PUB-1", "message.created"): "notifications.dispatch.issue.PUB-1.message.created",
		model.SessionDestinationKey("s1"):                     "notifications.agent.s1",
		model.RoleDestinationKey("reviewer"):                  "notifications.role.reviewer",
	}
	for built, wrote := range stored {
		if built != wrote {
			t.Errorf("this build keys a destination %q, want %q, the bytes every release before it stored", built, wrote)
		}
		if bus := busSubject(built, "PUB"); bus == wrote {
			t.Errorf("control: the bus subject of %q is %q, the stored key itself, want one that names PUB", built, bus)
		}
	}

	ctx := context.Background()
	database := storetest.Open(t)
	broker := events.NewBroker()
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee, owner) values ('PUB', 'PUB', 'alice', 'alice');
		insert into issues (key, project_key, number, title, route, created_by, rank)
		values ('PUB-1', 'PUB', 1, 'PUB-1', 'session:s1', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("create PUB-1: %v", err)
	}
	event := appendHoldMessage(t, database, broker, "PUB-1")
	owner := "notifications.dispatch.issue.PUB-1.message.created"
	if _, err := database.Pool.Exec(ctx, `
		update events set published_destinations = array[$2], attempt_count = 1 where id = $1
	`, event.ID, owner); err != nil {
		t.Fatalf("record the topic family reached, as the release before this one does: %v", err)
	}
	if slices.Contains(onBus("PUB", owner), owner) {
		t.Fatalf("control: the bus subject of %s is the stored key itself", owner)
	}

	publisher := &subjectRecorder{}
	deps := Deps{Store: database, Publisher: publisher, Broker: broker}
	if _, err := scanBatch(ctx, deps, unfenced{}, holding(t, deps).instance); err != nil {
		t.Fatalf("scan the batch: %v", err)
	}
	var sent []string
	for _, send := range publisher.all() {
		sent = append(sent, send.subject)
	}
	if want := []string{contracts.AgentSubject("PUB", "s1")}; !slices.Equal(sent, want) {
		t.Fatalf("the batch sent %v, want %v alone: the topic family the stored record names reached is reached", sent, want)
	}
	destinations, _ := publishedDestinations(t, database, event.ID)
	if want := []string{owner, model.SessionDestinationKey("s1")}; !slices.Equal(destinations, want) {
		t.Fatalf("published_destinations = %v, want %v: the stored key and s1's beside it, in the form with no project", destinations, want)
	}
	if publishedAt(t, database, event.ID) == nil {
		t.Fatal("the message is unpublished, want it published once s1 is reached")
	}
}

// pubReviewer is a role-claim store and session reader in which reviewer is claimed under PUB
// alone, by pubHolder, a live self-subscribed session: a resolution of reviewer under PUB names
// pubHolder, and one of any other role, or of reviewer under no project, names nobody. PUB is
// public, so a role of it with no claim of its own falls back to its claim under no project,
// which is none here.
type pubReviewer struct{ roles.Windows }

const pubHolder = "pub-holder"

func (pubReviewer) Private(context.Context, string) (bool, error) { return false, nil }

func (pubReviewer) Mode() roles.Mode { return roles.AllPublic }

func (pubReviewer) RoleClaim(_ context.Context, project, role string) (envoystore.RoleClaim, error) {
	if project == "PUB" && role == "reviewer" {
		return envoystore.RoleClaim{HolderSessionID: pubHolder}, nil
	}
	return envoystore.RoleClaim{}, nil
}

func (pubReviewer) Holder(context.Context, string) (session.SessionEntry, error) {
	return session.SessionEntry{SelfSubscribed: true, UpdatedAt: time.Now().UnixMilli()}, nil
}

func (pubReviewer) LastSeen(string) int64 { return 0 }

// plainCutRoleForward is fallbackRoleForward as it read a role before t-c-6: the destination's
// role prefix cut off as plain text, so notifications.role.PUB.reviewer names a role called
// PUB.reviewer, resolved under the event's project as any role of the record is (roleKey).
func plainCutRoleForward(ctx context.Context, deps Deps, item contracts.Envelope, destination, project string) *roleForward {
	role, ok := strings.CutPrefix(destination, contracts.RoleTopicPrefix)
	if !ok || role == "" {
		return nil
	}
	resolution, err := resolveRole(ctx, deps, roleKey(project, role))
	return forwardOf(project, role, item, resolution, err)
}

// TestAFallbackCopyResolvesItsRoleInBothForms (fallbackRoleForward, contracts.CanonicalRole): a
// fallback copy of an event of PUB held for reviewer, recorded as the record names a role's
// topic (notifications.role.reviewer) and in the form with its project
// (notifications.role.PUB.reviewer), resolves one holder, pubHolder, reviewer's holder under
// PUB, and its forward goes to that holder under PUB.
//
// Control, in the test: the same rows through a plain cut of the role prefix (plainCutRoleForward)
// find no holder for the form with the project, which names a role called PUB.reviewer, and the
// check fails naming that destination.
func TestAFallbackCopyResolvesItsRoleInBothForms(t *testing.T) {
	ctx := context.Background()
	deps := Deps{RoleClaims: pubReviewer{}, Sessions: pubReviewer{}}
	item := contracts.Envelope{Topic: contracts.DispatchIssueSubject("PUB", "PUB-1", "comment.created"), DedupeKey: "dispatch-7", EventID: "dispatch-7"}
	destinations := []string{model.RoleDestinationKey("reviewer"), contracts.RoleSubject("PUB", "reviewer")}
	type reader func(context.Context, Deps, contracts.Envelope, string, string) *roleForward
	misread := func(read reader) []string {
		var wrong []string
		for _, destination := range destinations {
			forward := read(ctx, deps, item, destination, "PUB")
			if forward == nil || forward.holder != pubHolder || forward.project != "PUB" {
				wrong = append(wrong, fmt.Sprintf("%s resolved %+v", destination, forward))
			}
		}
		return wrong
	}
	if wrong := misread(fallbackRoleForward); len(wrong) != 0 {
		t.Fatalf("the fallback copy's role read %v, want each form to reach %s under PUB", wrong, pubHolder)
	}
	wrong := misread(plainCutRoleForward)
	if len(wrong) != 1 || !strings.HasPrefix(wrong[0], contracts.RoleSubject("PUB", "reviewer")+" ") {
		t.Fatalf("control: a plain cut of the role prefix misread %v, want it to miss the holder of %s alone",
			wrong, contracts.RoleSubject("PUB", "reviewer"))
	}
}

// TestABatchKeepsEachProjectsRoleCopyOnItsOwnLane (resolveRoleHolders, roleKey, keptOrSkipped):
// PRIV-1 and PUB-1 are each routed to reviewer, which nobody holds, and each has a message. The
// batch keeps PRIV-1's copy on reviewer's lane under PRIV (bus.KeptRoleSubject("PRIV.reviewer"))
// and PUB-1's under PUB, each under the id every writer of that lane keeps it under, and a Legion
// role routed from PUB keeps its own lane: no copy of one project's role waits on another
// project's lane for that project's holder.
//
// Control, in the test: the lanes the role's name alone gives, today's key, are one lane for both
// copies, and the same check fails naming it.
func TestABatchKeepsEachProjectsRoleCopyOnItsOwnLane(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	broker := events.NewBroker()
	const legionRole = "legion-pub-pub-2-architect"
	if _, err := database.Pool.Exec(ctx, `
		insert into projects (key, name, default_assignee, owner, visibility) values
			('PRIV', 'PRIV', 'alice', 'alice', 'private'), ('PUB', 'PUB', 'alice', 'alice', 'public');
		insert into issues (key, project_key, number, title, route, created_by, rank) values
			('PRIV-1', 'PRIV', 1, 'PRIV-1', 'role:reviewer', '{"kind":"user","id":"alice"}', 'U'),
			('PUB-1', 'PUB', 1, 'PUB-1', 'role:reviewer', '{"kind":"user","id":"alice"}', 'U'),
			('PUB-2', 'PUB', 2, 'PUB-2', 'role:`+legionRole+`', '{"kind":"user","id":"alice"}', 'U');
	`); err != nil {
		t.Fatalf("create PRIV and PUB: %v", err)
	}
	byEvent := map[string]string{}
	for _, key := range []string{"PRIV-1", "PUB-1", "PUB-2"} {
		byEvent[fmt.Sprintf("dispatch-%d", appendHoldMessage(t, database, broker, key).ID)] = key
	}
	publisher := &keepRecorder{}
	deps := Deps{Store: database, Publisher: publisher, Broker: broker, RoleClaims: roleClaimStub{}, Sessions: roleClaimStub{}}
	if _, err := scanBatch(ctx, deps, unfenced{}, holding(t, deps).instance); err != nil {
		t.Fatalf("scan the batch: %v", err)
	}
	want := map[string]string{
		"PRIV-1": bus.KeptRoleSubject("PRIV.reviewer"),
		"PUB-1":  bus.KeptRoleSubject("PUB.reviewer"),
		"PUB-2":  bus.KeptRoleSubject(legionRole),
	}
	wrongLanes := func(lanes map[string]string) []string {
		var wrong []string
		for key, lane := range want {
			if lanes[key] != lane {
				wrong = append(wrong, fmt.Sprintf("%s kept on %q, want %q", key, lanes[key], lane))
			}
		}
		slices.Sort(wrong)
		return wrong
	}
	got := map[string]string{}
	for _, kept := range publisher.keeps() {
		key := byEvent[kept.item.DedupeKey]
		if _, twice := got[key]; twice {
			t.Fatalf("%s was kept twice: on %q and %q", key, got[key], kept.subject)
		}
		got[key] = kept.subject
		if kept.msgID != bus.KeptMsgID(kept.item.DedupeKey, strings.TrimPrefix(kept.subject, bus.KeptRoleSubjectPrefix)) {
			t.Errorf("%s was kept under %q, want the id every writer of %s keeps it under", key, kept.msgID, kept.subject)
		}
	}
	if wrong := wrongLanes(got); len(wrong) != 0 {
		t.Fatalf("the batch kept %v, want each project's copy on its own lane: %v", got, wrong)
	}
	bare := map[string]string{"PRIV-1": bus.KeptRoleSubject("reviewer"), "PUB-1": bus.KeptRoleSubject("reviewer"), "PUB-2": bus.KeptRoleSubject(legionRole)}
	if wrong := wrongLanes(bare); len(wrong) != 2 {
		t.Fatalf("control: the role's name alone as the lane misread %v, want PRIV-1 and PUB-1 named", wrong)
	}
}
