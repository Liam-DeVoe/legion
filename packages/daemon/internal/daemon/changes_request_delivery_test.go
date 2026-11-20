package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
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
			var dropped []string
			for line := range strings.SplitSeq(logged.String(), "\n") {
				if strings.Contains(line, `msg="outbox changes request finished without publishing: the person's approval followed it"`) {
					dropped = append(dropped, line)
				}
			}
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
