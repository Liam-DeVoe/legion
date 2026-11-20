package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// A person's changes request whose notice waits in the outbox is published only while it is
// still the person's word (dispatch://INFRA-3136/spec R2). The workflow asks when it queues the
// notice (workflow's tellChanges), and the delivery asks again of the gates as they stand when it
// runs (readNoticeTree's decisionSuperseded): the row's first attempt finds no session holding the
// architect's role, as while its pod is released or relaunching, and waits; the person's approval
// lands; the next attempt runs. The review and the approval reach the engine as Dispatch's
// artifact events do, so the rows are the ones the workflow writes.
//
// Where the person approved the reviewed version after the request, the request's row finishes
// with nothing published and one line naming the row, the issue and both versions, and the
// approval's own notice behind it still goes. The controls are a request no approval followed,
// and one that came after the person's approval: each is published as it always was.
func TestAQueuedChangesNoticeAnApprovalFollowedIsNotPublished(t *testing.T) {
	reviewed := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	changes := intake.DispatchArtifact{Key: "LEGION-1", ArtifactID: "artifact-1", Kind: intake.DispatchArtifactChangesRequested, Version: 3,
		Reason: "name the rollback path", Actor: alice, Mark: record.EventMark{Seq: 8, At: reviewed}}
	approval := func(seq int64, at time.Time) intake.DispatchArtifact {
		return intake.DispatchArtifact{Key: "LEGION-1", ArtifactID: "artifact-1", Kind: intake.DispatchArtifactApproved, Version: 3, Actor: alice,
			Mark: record.EventMark{Seq: seq, At: at}}
	}
	for _, tc := range []struct {
		name string
		// queued are the events applied before the first attempt, and waited those applied while
		// the row waits for the architect. published is every notice the architect is then sent.
		queued, waited []intake.DispatchArtifact
		published      []record.NoticeKind
		superseded     bool
	}{
		{name: "the person approved the reviewed version after the request", queued: []intake.DispatchArtifact{changes},
			waited: []intake.DispatchArtifact{approval(9, reviewed.Add(5*time.Minute))}, published: []record.NoticeKind{"design-approved"}, superseded: true},
		{name: "no approval followed the request", queued: []intake.DispatchArtifact{changes},
			published: []record.NoticeKind{"design-changes-requested"}},
		{name: "the request came after the person's approval", queued: []intake.DispatchArtifact{approval(7, reviewed.Add(-5*time.Minute)), changes},
			published: []record.NoticeKind{"design-approved", "design-changes-requested"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			noticeTree(t, pool, records, false)
			if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				return records.PutGate(ctx, tx, record.DesignGate{Issue: "LEGION-1", Step: record.DesignStepSpec, ArtifactID: "artifact-1", LatestVersion: 3})
			}); err != nil {
				t.Fatalf("register the spec gate at version 3: %v", err)
			}
			steps := config.DesignGateRootIssues.Steps()
			engine := workflow.New(records, workflow.Config{Project: "LEGION", Repositories: testRepositories, Steps: steps}, quietLogger())
			apply := func(events []intake.DispatchArtifact) {
				t.Helper()
				for _, event := range events {
					id := fmt.Sprintf("artifact-%s-%d", event.Kind, event.Mark.Seq)
					if result, err := intake.ApplyFact(ctx, pool, "dispatch", id, event, engine); err != nil || result.Refusal != nil {
						t.Fatalf("apply %s = %#v, %v", id, result, err)
					}
				}
			}
			apply(tc.queued)
			request := queuedNotice(t, pool, "design-changes-requested")
			if want := (record.Notice{Kind: "design-changes-requested", Version: 3, Reason: changes.Reason, Actor: "user:alice",
				Ref: "dispatch://LEGION-1/spec", Review: changes.Mark}); !reflect.DeepEqual(request.notice, want) {
				t.Fatalf("the workflow queued %#v, want the changes request %#v", request.notice, want)
			}

			sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
			architectClaim(t, sup, "LEGION-1", supervise.StateWorking)
			publisher := &holderPublisher{}
			publisher.setAbsent(architectTopic(t, "LEGION-1"))
			var logged bytes.Buffer
			clock := time.Now()
			runner := &outbox{log: slog.New(slog.NewTextHandler(&logged, nil)), pool: pool, dispatchProject: "LEGION", records: records,
				notices: publisher, supervisor: sup, project: "legion", gatedSteps: steps, now: func() time.Time { return clock }}
			if err := runner.RunOnce(ctx); err != nil {
				t.Fatalf("first attempt: %v", err)
			}
			if published := architectNotices(t, publisher); len(published) != 0 || queuedNotice(t, pool, "design-changes-requested").id != request.id {
				t.Fatalf("published %v to the architect on the attempt no session holds its role; want the request's row %d waiting", published, request.id)
			}

			apply(tc.waited)
			publisher.setAbsent()
			for run := 0; run < 5 && outboxRowsOfKind(t, pool, "LEGION-1", record.OutboxKindNotice) > 0; run++ {
				clock = clock.Add(time.Hour)
				if err := runner.RunOnce(ctx); err != nil {
					t.Fatalf("attempt after the wait %d: %v", run, err)
				}
			}
			if left := outboxRowsOfKind(t, pool, "LEGION-1", record.OutboxKindNotice); left != 0 {
				t.Fatalf("%d notice rows of LEGION-1 left, want every row finished; log %s", left, logged.String())
			}
			if published := architectNotices(t, publisher); !reflect.DeepEqual(published, tc.published) {
				t.Fatalf("published %v to the architect, want %v", published, tc.published)
			}
			dropped := textLines(logged.String(), `msg="outbox changes request finished without publishing: the person's approval followed it"`)
			if !tc.superseded {
				if len(dropped) != 0 {
					t.Fatalf("logged %q, want no changes request finished without publishing", dropped)
				}
				return
			}
			want := fmt.Sprintf("row=%d issue=LEGION-1 version=3 approved=3", request.id)
			if len(dropped) != 1 || !strings.Contains(dropped[0], want) || !strings.Contains(dropped[0], "level=INFO") {
				t.Fatalf("logged %q, want one Info line naming %s", dropped, want)
			}
		})
	}
}

// architectNotices is the kind of every notice published to LEGION-1's architect, in order. The
// gate's opening also sends the tree's roles, the architect among them, a role message of its own
// (whose payload is its text), which is no notice and not this test's.
func architectNotices(t *testing.T, publisher *holderPublisher) []record.NoticeKind {
	t.Helper()
	_, delivered := publisher.snapshot()
	var kinds []record.NoticeKind
	for _, published := range delivered {
		if notice, ok := published.payload.(record.Notice); ok && published.topic == architectTopic(t, "LEGION-1") {
			kinds = append(kinds, notice.Kind)
		}
	}
	return kinds
}

// queuedRow is a notice row the outbox holds: its id and the notice it carries.
type queuedRow struct {
	id     int64
	notice record.Notice
}

// queuedNotice is the one notice row of kind the outbox holds.
func queuedNotice(t *testing.T, pool *store.Pool, kind record.NoticeKind) queuedRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select id, kind, issue, payload from outbox
		where kind = $1 and payload->>'kind' = $2 order by id`, string(record.OutboxKindNotice), string(kind))
	if err != nil {
		t.Fatalf("read the %s notice rows: %v", kind, err)
	}
	queued, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (record.OutboxRow, error) {
		var out record.OutboxRow
		return out, row.Scan(&out.ID, &out.Kind, &out.Issue, &out.Payload)
	})
	if err != nil {
		t.Fatalf("scan the %s notice rows: %v", kind, err)
	}
	if len(queued) != 1 {
		t.Fatalf("%d %s notice rows queued, want one", len(queued), kind)
	}
	payload, err := record.DecodeOutboxPayload(queued[0])
	if err != nil {
		t.Fatalf("decode notice row %d: %v", queued[0].ID, err)
	}
	notice, ok := payload.(record.Notice)
	if !ok {
		t.Fatalf("notice row %d carries %T, want a record.Notice", queued[0].ID, payload)
	}
	return queuedRow{id: queued[0].ID, notice: notice}
}

// A person's changes request relayed to the controller, because nobody in its tree hears it
// (parkDecision), is told only while it is still the person's word (dispatch://INFRA-3136/spec
// R2). The relay asks when it writes the controller's row, and the publish asks again
// (controllerNotice): the row's first attempt finds no session holding the controller's claim,
// and waits; the person's approval of the version reviewed lands; the holder returns and the next
// attempt runs. The review and the approval reach the engine as Dispatch's artifact events do, on
// a running tree whose architect's claim failed, so every row is one the workflow and the relay
// write.
//
// Where the approval followed the request, the request's row finishes with nothing published and
// one line naming the row, the issue, the ref and both versions, and the approval's own relay
// still goes. The controls are a request no approval followed, and one Dispatch recorded after
// the person's approval of the same version: each is told as it always was, since the review's
// mark decides and not the version alone.
func TestAChangesRequestRelayedToTheControllerIsNotToldOnceApproved(t *testing.T) {
	reviewed := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	changes := intake.DispatchArtifact{Key: "LEGION-1", ArtifactID: "artifact-1", Kind: intake.DispatchArtifactChangesRequested, Version: 3,
		Reason: "name the rollback path", Actor: alice, Mark: record.EventMark{Seq: 8, At: reviewed}}
	approval := func(seq int64, at time.Time) intake.DispatchArtifact {
		return intake.DispatchArtifact{Key: "LEGION-1", ArtifactID: "artifact-1", Kind: intake.DispatchArtifactApproved, Version: 3, Actor: alice,
			Mark: record.EventMark{Seq: seq, At: at}}
	}
	for _, tc := range []struct {
		name string
		// queued are the events applied before the relay's first attempt, and waited those
		// applied while its row waits for the controller. told is every decision the controller
		// is then told, in order.
		queued, waited []intake.DispatchArtifact
		told           []record.DecisionKind
		superseded     bool
	}{
		{name: "the person approved the reviewed version after the request", queued: []intake.DispatchArtifact{changes},
			waited: []intake.DispatchArtifact{approval(9, reviewed.Add(5*time.Minute))}, told: []record.DecisionKind{record.DecisionDesignApproved},
			superseded: true},
		{name: "no approval followed the request", queued: []intake.DispatchArtifact{changes},
			told: []record.DecisionKind{record.DecisionDesignChangesRequested}},
		{name: "the request came after the person's approval", queued: []intake.DispatchArtifact{approval(7, reviewed.Add(-5*time.Minute)), changes},
			told: []record.DecisionKind{record.DecisionDesignApproved, record.DecisionDesignChangesRequested}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			noticeTree(t, pool, records, false)
			if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				return records.PutGate(ctx, tx, record.DesignGate{Issue: "LEGION-1", Step: record.DesignStepSpec, ArtifactID: "artifact-1", LatestVersion: 3})
			}); err != nil {
				t.Fatalf("register the spec gate at version 3: %v", err)
			}
			// One clock for the engine and the outbox: the rows the engine queues come due by it,
			// and the waiting row's backoff counts from it. The approval lands a minute after the
			// request's row waited, so that row is due before the approval's own notice, and asks
			// the gate while the decision kept for it is still the request.
			clock := reviewed.Add(time.Hour)
			now := func() time.Time { return clock }
			steps := config.DesignGateRootIssues.Steps()
			engine := workflow.New(records, workflow.Config{Project: "LEGION", Repositories: testRepositories, Steps: steps, Clock: now}, quietLogger())
			apply := func(events []intake.DispatchArtifact) {
				t.Helper()
				for _, event := range events {
					id := fmt.Sprintf("artifact-%s-%d", event.Kind, event.Mark.Seq)
					if result, err := intake.ApplyFact(ctx, pool, "dispatch", id, event, engine); err != nil || result.Refusal != nil {
						t.Fatalf("apply %s = %#v, %v", id, result, err)
					}
				}
			}
			apply(tc.queued)

			sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
			architectClaim(t, sup, "LEGION-1", supervise.StateFailed)
			publisher := &holderPublisher{}
			publisher.setAbsent(architectTopic(t, "LEGION-1"), notify.ControllerTopic("legion"))
			var logged bytes.Buffer
			runner := &outbox{log: slog.New(slog.NewTextHandler(&logged, nil)), pool: pool, dispatchProject: "LEGION", records: records,
				notices: publisher, supervisor: sup, project: "legion", gatedSteps: steps, now: now}
			// The first run relays each decision, whose notice finds its architect's claim failed;
			// the second is each relay's first attempt, which waits for the controller's claim.
			for run := range 2 {
				if err := runner.RunOnce(ctx); err != nil {
					t.Fatalf("run %d before the wait: %v", run, err)
				}
			}
			request, attempts := relayRow(t, pool, record.DecisionDesignChangesRequested)
			if told := controllerDecisions(publisher); len(told) != 0 || attempts == 0 {
				t.Fatalf("told the controller %v, and the request's row %d made %d attempts; want nothing told and the row waiting",
					told, request, attempts)
			}

			clock = clock.Add(time.Minute)
			apply(tc.waited)
			publisher.setAbsent(architectTopic(t, "LEGION-1"))
			left := func() int {
				return outboxRowsOfKind(t, pool, "LEGION-1", record.OutboxKindNotice) + outboxRowsOfKind(t, pool, "LEGION-1", record.OutboxKindControllerNotice)
			}
			for run := 0; run < 5 && left() > 0; run++ {
				clock = clock.Add(time.Hour)
				if err := runner.RunOnce(ctx); err != nil {
					t.Fatalf("run %d after the wait: %v", run, err)
				}
			}
			if rows := left(); rows != 0 {
				t.Fatalf("%d notice and controller rows of LEGION-1 left, want every row finished; log %s", rows, logged.String())
			}
			if told := controllerDecisions(publisher); !slices.Equal(told, tc.told) {
				t.Fatalf("told the controller %v, want %v; log %s", told, tc.told, logged.String())
			}
			dropped := textLines(logged.String(), `msg="outbox parked changes request finished without publishing`)
			if !tc.superseded {
				if len(dropped) != 0 {
					t.Fatalf("logged %q, want no relayed changes request finished without publishing", dropped)
				}
				return
			}
			want := fmt.Sprintf("row=%d issue=LEGION-1 ref=dispatch://LEGION-1/spec version=3 approved=3", request)
			if len(dropped) != 1 || !strings.Contains(dropped[0], `the person's approval followed it"`) || !strings.Contains(dropped[0], want) ||
				!strings.Contains(dropped[0], "level=INFO") {
				t.Fatalf("logged %q, want one Info line that the approval followed the request, naming %s", dropped, want)
			}
		})
	}
}

// A relayed changes request that nothing keeps any more, or that the person's later decision on
// the same document replaced, is not told either: the decision kept beside the row is what says
// the request is still owed (relayedChangesAnswered). Nothing keeps it once a start or a catch-up
// told an architect, the forget of a superseded decision took it, or the clear of its generation
// did, and a later decision is told on its own. The control is a relayed approval nothing keeps,
// which is published as every other controller notice is: only a changes request is asked.
func TestARelayedChangesRequestNothingKeepsIsNotToldToTheController(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	ref := "dispatch://LEGION-1/spec"
	for _, tc := range []struct {
		name     string
		decision record.DecisionKind
		kept     *record.ParkedDecision
		// line is the one line that finishes the row unpublished, "" where the row publishes.
		line string
	}{
		{name: "nothing keeps the request any more", decision: record.DecisionDesignChangesRequested,
			line: `msg="outbox parked changes request finished without publishing: nothing is kept for it any more"`},
		{name: "the person's later decision replaced it", decision: record.DecisionDesignChangesRequested,
			kept: &record.ParkedDecision{Issue: "LEGION-1", Ref: ref, Decision: record.DecisionDesignApproved, Actor: "user:alice", At: at.Add(5 * time.Minute), Seq: 9},
			line: `msg="outbox parked changes request finished without publishing: the person's later decision replaced it"`},
		{name: "an approval nothing keeps", decision: record.DecisionDesignApproved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			noticeTree(t, pool, records, true)
			if tc.kept != nil {
				if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return records.AddParkedDecision(ctx, tx, *tc.kept) }); err != nil {
					t.Fatalf("keep %#v: %v", *tc.kept, err)
				}
			}
			var logged bytes.Buffer
			publisher := &holderPublisher{}
			runner := &outbox{log: slog.New(slog.NewTextHandler(&logged, nil)), pool: pool, dispatchProject: "LEGION", records: records,
				notices: publisher, project: "legion", gatedSteps: config.DesignGateRootIssues.Steps(), now: time.Now}
			notice := record.ControllerNotice{Kind: record.ParkedDecisionNotice, Decision: tc.decision, Root: "LEGION-1", Ref: ref, Actor: "user:alice", Version: 3}
			if tc.decision == record.DecisionDesignChangesRequested {
				notice.Reason = "name the rollback path"
			}
			row := leasedOutboxRow(t, pool, records, mustOutboxRow(t, "LEGION-1", notice, at))
			if err := runner.controllerNotice(ctx, row, notice); err != nil {
				t.Fatalf("controller notice: %v", err)
			}
			_, delivered := publisher.snapshot()
			dropped := textLines(logged.String(), `msg="outbox parked changes request finished without publishing`)
			if tc.line == "" {
				if len(delivered) != 1 || !reflect.DeepEqual(delivered[0].payload, record.Notice(notice)) || len(dropped) != 0 {
					t.Fatalf("delivered %+v and logged %q, want the notice published as it is", delivered, dropped)
				}
				return
			}
			want := fmt.Sprintf("row=%d issue=LEGION-1 ref=%s version=3", row.ID, ref)
			if tc.kept != nil {
				want += " kept=" + string(tc.kept.Decision)
			}
			if len(delivered) != 0 || len(dropped) != 1 || !strings.Contains(dropped[0], tc.line) || !strings.Contains(dropped[0], want) ||
				!strings.Contains(dropped[0], "level=INFO") {
				t.Fatalf("delivered %+v and logged %q, want nothing published and one Info line %s naming %s", delivered, dropped, tc.line, want)
			}
		})
	}
}

// relayRow is the one parked-decision controller row of decision the outbox holds: its id and the
// attempts it has made.
func relayRow(t *testing.T, pool *store.Pool, decision record.DecisionKind) (int64, int) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select id, attempts from outbox
		where kind = $1 and payload->>'kind' = $2 and payload->>'decision' = $3 order by id`,
		string(record.OutboxKindControllerNotice), string(record.ParkedDecisionNotice), string(decision))
	if err != nil {
		t.Fatalf("read the %s relay rows: %v", decision, err)
	}
	type relay struct {
		id       int64
		attempts int
	}
	relays, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (relay, error) {
		var out relay
		return out, row.Scan(&out.id, &out.attempts)
	})
	if err != nil {
		t.Fatalf("scan the %s relay rows: %v", decision, err)
	}
	if len(relays) != 1 {
		t.Fatalf("%d %s relay rows queued, want one", len(relays), decision)
	}
	return relays[0].id, relays[0].attempts
}

// controllerDecisions is the decision of every parked-decision notice published on the project's
// controller topic, in order.
func controllerDecisions(publisher *holderPublisher) []record.DecisionKind {
	_, delivered := publisher.snapshot()
	var told []record.DecisionKind
	for _, published := range delivered {
		if notice, ok := published.payload.(record.Notice); ok && published.topic == notify.ControllerTopic("legion") && notice.Kind == record.ParkedDecisionNotice {
			told = append(told, notice.Decision)
		}
	}
	return told
}

// textLines is every line of logged, a text log (slog.TextHandler), that holds text.
func textLines(logged, text string) []string {
	var lines []string
	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, text) {
			lines = append(lines, line)
		}
	}
	return lines
}
