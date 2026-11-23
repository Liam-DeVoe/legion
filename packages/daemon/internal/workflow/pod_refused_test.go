package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A launch its issue's pod refused is told on the issue and to the controller at each report. One
// no outbox start stood behind — a crash relaunch of the tester, whose suspension then retired the
// phase task it held — asks for a start of the claim, and the workflow queues the owed entry's
// start of that claim: the entry and the task text the tester's phases row holds, marked as a
// resend of that entry, so the claim takes that same work again once another pod stands. A report
// that asks for no start queues none.
func TestALaunchItsPodRefusedWithNoStartBehindItQueuesTheOwedEntrysStart(t *testing.T) {
	const task = "Test the change. Issue: LEGION-209. Phase: testing."
	pool := migratedPool(t)
	seedTestingIssue(t, pool, "LEGION-209")
	token := claim.Token("legion-legion-legion-209-tester")
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-209", Role: claim.RoleTester, Claim: token, Entry: 3, EntryTask: task,
		EntryRaisedAt: time.Now().UTC().Add(-time.Hour)})
	engine := testEngine(config.DesignGateRootIssues, nil)
	report := func(id string, queue bool) {
		t.Helper()
		if _, err := intake.ApplyFact(t.Context(), pool, "supervise", id, intake.PodRefusedLaunch{Issue: "LEGION-209", Role: claim.RoleTester,
			Claim: token, Pod: "pod-1", Detail: "the pod pod-1 of LEGION-209 was built without checker", Queue: queue}, engine, admissionStub{}); err != nil {
			t.Fatalf("apply the refusal's report: %v", err)
		}
	}
	starts := func() []record.SuperviseRequest {
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

	report("pod-refused:legion-legion-legion-209-tester:1", false)
	if got := starts(); len(got) != 0 {
		t.Fatalf("a report asking for no start queued %+v, want none", got)
	}
	report("pod-refused:legion-legion-legion-209-tester:2", true)
	got := starts()
	if len(got) != 1 {
		t.Fatalf("the report asking for a start queued %+v, want one start", got)
	}
	if s := got[0]; s.Role != claim.RoleTester || s.Phase != phase.Testing || s.Entry != 3 || !strings.HasPrefix(s.Task, task) || !s.ResendEntry {
		t.Fatalf("the queued start is %+v, want the tester's owed entry 3 of testing, its stored task %q, marked as a resend of that entry", s, task)
	}
	if posted := messageBodies(t, pool); len(posted) != 2 || !strings.Contains(posted[0], "pod-1") {
		t.Fatalf("the issue was told %v, want one message per report, naming the pod", posted)
	}
	if kinds := controllerNoticeKinds(t, pool); len(kinds) != 2 || kinds[0] != string(record.PodWaitHeldNotice) || kinds[1] != string(record.PodWaitHeldNotice) {
		t.Fatalf("the controller was told %v, want one %s notice per report", kinds, record.PodWaitHeldNotice)
	}
}
