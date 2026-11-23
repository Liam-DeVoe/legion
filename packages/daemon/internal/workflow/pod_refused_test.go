package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// refusedTesterTask is the task the tester's phases row holds for its entry 3 in these tests.
const refusedTesterTask = "Test the change. Issue: LEGION-209. Phase: testing."

// reportRefusal applies one report of a launch of claim, the tester's, that LEGION-209's pod pod-1
// refused, under event id.
func reportRefusal(t *testing.T, pool *pgxpool.Pool, engine *Engine, id string, token claim.Token) {
	t.Helper()
	if _, err := intake.ApplyFact(t.Context(), pool, "supervise", id, intake.PodRefusedLaunch{Issue: "LEGION-209", Role: claim.RoleTester,
		Claim: token, Pod: "pod-1", Detail: "the pod pod-1 of LEGION-209 was built without checker"}, engine, admissionStub{}); err != nil {
		t.Fatalf("apply the refusal's report: %v", err)
	}
}

// queuedStarts is every supervise start the outbox holds, in order.
func queuedStarts(t *testing.T, pool *pgxpool.Pool) []record.SuperviseRequest {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select payload from outbox where kind = 'supervise' and payload->>'op' = 'start' order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []record.SuperviseRequest
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var start record.SuperviseRequest
		if err := json.Unmarshal(payload, &start); err != nil {
			t.Fatal(err)
		}
		got = append(got, start)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// queueStart writes start as a row of LEGION-209's outbox, as the workflow queues one.
func queueStart(t *testing.T, pool *pgxpool.Pool, start record.SuperviseRequest) {
	t.Helper()
	seedRecord(t, pool, func(tx pgx.Tx) error {
		row, err := record.NewOutboxRow("LEGION-209", start, time.Now().UTC())
		if err != nil {
			return err
		}
		return record.NewStore().Enqueue(t.Context(), tx, row)
	})
}

// wantOwedEntryStart fails t unless start is the tester's owed entry 3 of testing: the task its
// phases row stores, marked as a resend of that entry.
func wantOwedEntryStart(t *testing.T, start record.SuperviseRequest) {
	t.Helper()
	if start.Role != claim.RoleTester || start.Phase != phase.Testing || start.Entry != 3 || !strings.HasPrefix(start.Task, refusedTesterTask) || !start.ResendEntry {
		t.Fatalf("the queued start is %+v, want the tester's owed entry 3 of testing, its stored task %q, marked as a resend of that entry", start, refusedTesterTask)
	}
}

// A launch its issue's pod refused is told on the issue and to the controller at each report, and
// the workflow queues a start of the claim wherever no queued start stands that brings the claim
// back once another pod stands, read in the report's own transaction. Which launch the pod
// refused is no part of the rule: an outbox start's own launch, whose row then waits for the pod;
// the turn of a launch the wake pacing held, whose start row had finished when it delivered its
// task; a retry of a launch whose write failed; a crash relaunch; an operator's resume. The
// suspension each ended in retired the task the claim held, so the start queued is the owed
// entry's: the entry and the task text the tester's phases row holds, marked as a resend of that
// entry, so the claim takes that same work again.
//
// A start of the claim's own stands behind it, and so does a start of its role that names no
// claim, which reaches the role's worker; each, run while the pod stands, waits for the pod. A
// wake's start does not: it finishes without acting on a claim the daemon suspended, which every
// claim whose launch its pod refused is. Nor does a start of another claim of the role, or one of
// an earlier run of the issue, which finishes without acting. The report an hour later, while the
// start the first one queued stands, queues none.
func TestALaunchItsPodRefusedQueuesAStartWhereNoQueuedStartBringsTheClaimBack(t *testing.T) {
	token := claim.Token("legion-legion-legion-209-tester")
	for _, tc := range []struct {
		name   string
		queued *record.SuperviseRequest
		queues bool
	}{
		{name: "no start queued", queues: true},
		{name: "the claim's own start", queued: &record.SuperviseRequest{Op: "start", Tree: "LEGION-209", Role: claim.RoleTester, Claim: token,
			Generation: 1, Phase: phase.Testing, Entry: 3, Task: refusedTesterTask}},
		{name: "a start of its role naming no claim", queued: &record.SuperviseRequest{Op: "start", Tree: "LEGION-209", Role: claim.RoleTester,
			Generation: 1, Phase: phase.Testing, Entry: 3, Task: refusedTesterTask}},
		{name: "a wake's start of the claim", queued: &record.SuperviseRequest{Op: "start", Tree: "LEGION-209", Role: claim.RoleTester, Claim: token,
			Generation: 1, WakeKind: pods.WakeChecks}, queues: true},
		{name: "a start of another claim of its role", queued: &record.SuperviseRequest{Op: "start", Tree: "LEGION-209", Role: claim.RoleTester,
			Claim: "legion-legion-legion-209-implementer", Generation: 1}, queues: true},
		{name: "a start of an earlier run", queued: &record.SuperviseRequest{Op: "start", Tree: "LEGION-209", Role: claim.RoleTester, Claim: token}, queues: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedTestingIssue(t, pool, "LEGION-209")
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-209", Role: claim.RoleTester, Claim: token, Entry: 3, EntryTask: refusedTesterTask,
				EntryRaisedAt: time.Now().UTC().Add(-time.Hour)})
			engine := testEngine(config.DesignGateRootIssues, nil)
			seeded := 0
			if tc.queued != nil {
				queueStart(t, pool, *tc.queued)
				seeded = 1
			}

			reportRefusal(t, pool, engine, "pod-refused:legion-legion-legion-209-tester:1", token)
			got := queuedStarts(t, pool)
			if !tc.queues {
				if len(got) != seeded {
					t.Fatalf("the report beside a start that brings the claim back left the starts %+v, want only the one queued before it", got)
				}
			} else {
				if len(got) != seeded+1 {
					t.Fatalf("the report left the starts %+v, want one more than the %d queued before it: nothing queued brings the claim back", got, seeded)
				}
				wantOwedEntryStart(t, got[len(got)-1])
			}
			reportRefusal(t, pool, engine, "pod-refused:legion-legion-legion-209-tester:2", token)
			if again := queuedStarts(t, pool); len(again) != len(got) {
				t.Fatalf("the report an hour later left the starts %+v, want the %d the first report left", again, len(got))
			}
			if posted := messageBodies(t, pool); len(posted) != 2 || !strings.Contains(posted[0], "pod-1") {
				t.Fatalf("the issue was told %v, want one message per report, naming the pod", posted)
			}
			if kinds := controllerNoticeKinds(t, pool); len(kinds) != 2 || kinds[0] != string(record.PodWaitHeldNotice) || kinds[1] != string(record.PodWaitHeldNotice) {
				t.Fatalf("the controller was told %v, want one %s notice per report", kinds, record.PodWaitHeldNotice)
			}
		})
	}
}

// The claim whose start a refused launch queues is the worker of its role's step by the one rule
// every reader of that claim reads (record.WorkerClaim), not the claim the role's phases row
// names: a row whose step a new generation cleared keeps the claim of the agent that last worked
// it, here another key's, and the worker of the role is then the role's own claim, whose launch
// the pod refused. So that claim is sent the owed entry's start, the entry and the task the row
// holds, as a resend.
func TestARefusedLaunchOfTheRolesWorkerIsSentTheOwedEntryWhateverClaimItsRowNames(t *testing.T) {
	pool := migratedPool(t)
	seedTestingIssue(t, pool, "LEGION-209")
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-209", Role: claim.RoleTester, Claim: "legion-legion-legion-209-implementer", Entry: 3,
		EntryTask: refusedTesterTask, EntryRaisedAt: time.Now().UTC().Add(-time.Hour)})
	engine := testEngine(config.DesignGateRootIssues, nil)

	reportRefusal(t, pool, engine, "pod-refused:legion-legion-legion-209-tester:1", "legion-legion-legion-209-tester")
	got := queuedStarts(t, pool)
	if len(got) != 1 {
		t.Fatalf("the report queued %+v, want one start", got)
	}
	wantOwedEntryStart(t, got[0])
}
