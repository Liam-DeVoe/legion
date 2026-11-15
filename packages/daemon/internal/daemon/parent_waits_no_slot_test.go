package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The daemon's side of a parent waiting on its open Dispatch children (dispatch://INFRA-3320 R11,
// R12, R13, and its check dispatch://INFRA-3320/artifact/verification-md#b-k13): the controller
// tick's re-read, which is the only read that reaches a child filed and not released, the restart
// between the move and the return, and the release of the waiting parent's pod.
func TestParentWaitsHoldsNoSlotAndComesBack(t *testing.T) {
	parent := startedParent
	filed := dispatch.IssueSummary{Key: filedChild, Status: "triage", Parent: &parent, LastSeq: 1}

	// The project waits on 25 keys or fewer: one root fewer than the single-read bound each waits
	// on a blocker of its own, and the waiting parent's open child, once the mirror holds it, is
	// the bound's last key. Each turn reads them one at a time and lists the project once for
	// the children of the issues whose phases started. The first turn takes the filed child from
	// that listing, though it sends the daemon no event, and its own admission pass moves the
	// parent. A daemon restart before the next turn, on a new store with a new engine and a new
	// admission, finds the parent waiting with the start it held; the child then moves to icebox,
	// with no event either, and one more turn takes the parent back to that start.
	t.Run("the tick's re-read moves a parent over a child filed in triage, a restart keeps the start it holds, and the child's move to icebox brings it back", func(t *testing.T) {
		ctx := context.Background()
		pool := isolatedOutboxPool(t)
		client := &tickDispatch{summaries: []dispatch.IssueSummary{filed}}
		w := tickRuntime(t, pool, client)
		seedStartedParent(t, pool, startedParent)
		seedBlockedRoots(t, pool, blockerReadFanout-1)
		blockers := make([]string, 0, blockerReadFanout)
		for i := 1; i < blockerReadFanout; i++ {
			blockers = append(blockers, ownBlocker(i))
		}
		slices.Sort(blockers)

		w.applyControllerTick(ctx, time.Now())

		if asked, listings := client.asked(), client.listings(); !slices.Equal(sortedKeys(asked), blockers) || listings != 1 {
			t.Fatalf("the first turn read %v singly and listed the project %d times, want the %d blockers singly and one listing for the children",
				asked, listings, len(blockers))
		}
		if row := tickMirrored(t, pool)[filedChild]; row.Status != "triage" || row.Parent != startedParent {
			t.Fatalf("the mirror holds %s as %+v after one turn, want the listing's reading: triage under %s", filedChild, row, startedParent)
		}
		moved := tickIssue(t, pool, startedParent)
		if moved.Phase != phase.AwaitingChildren || moved.ChildrenWait == nil || moved.ChildrenWait.Start != phase.Implementing {
			t.Fatalf("%s after the turn = phase %s, held start %+v; want awaiting_children holding implementing", startedParent, moved.Phase, moved.ChildrenWait)
		}
		if slotted := tickSlotted(t, pool); slices.Contains(slotted, startedParent) {
			t.Fatalf("slots = %v, want none for the waiting parent", slotted)
		}

		// The restart.
		restartedStore, err := store.Open(ctx, pool.Config().ConnString())
		if err != nil {
			t.Fatalf("open a new store on the database: %v", err)
		}
		t.Cleanup(restartedStore.Close)
		restarted := tickRuntime(t, restartedStore.Pool(), client)
		client.serve(dispatch.Issue{Key: filedChild, Project: "CAPTURE", Status: "triage", Parent: &parent, LastSeq: 1})

		restarted.applyControllerTick(ctx, time.Now().Add(time.Second))

		if asked, listings := client.asked(), client.listings(); !slices.Equal(sortedKeys(asked[len(blockers):]), sortedKeys(append(slices.Clone(blockers), filedChild))) ||
			listings != 2 {
			t.Fatalf("the turns read %v singly and listed the project %d times, want the second turn to read the %d keys of the bound singly and list once",
				asked, listings, blockerReadFanout)
		}
		again := tickIssue(t, pool, startedParent)
		if again.Phase != phase.AwaitingChildren || !sameChildrenWait(again.ChildrenWait, moved.ChildrenWait) || !again.ForwardMergeOwed {
			t.Fatalf("%s after the restarted daemon's turn = phase %s, held start %+v; want it waiting still with %+v",
				startedParent, again.Phase, again.ChildrenWait, moved.ChildrenWait)
		}

		// The child moves to icebox, with no event.
		iceboxed := filed
		iceboxed.Status, iceboxed.LastSeq = "icebox", 2
		client.summaries = []dispatch.IssueSummary{iceboxed}
		client.serve(dispatch.Issue{Key: filedChild, Project: "CAPTURE", Status: "icebox", Parent: &parent, LastSeq: 2})

		restarted.applyControllerTick(ctx, time.Now().Add(2*time.Second))

		if row := tickMirrored(t, pool)[filedChild]; row.Status != "icebox" || row.Seq != 2 {
			t.Fatalf("the mirror holds %s as %+v, want icebox at 2", filedChild, row)
		}
		back := tickIssue(t, pool, startedParent)
		if back.Phase != phase.Implementing || back.ChildrenWait != nil {
			t.Fatalf("%s after its only child moved to icebox and one more turn = phase %s, held start %+v; want implementing, the start it held",
				startedParent, back.Phase, back.ChildrenWait)
		}
		var task string
		if err := pool.QueryRow(ctx, `select coalesce(payload->>'task', '') from outbox where issue = $1 and kind = 'supervise'
			and payload->>'op' = 'start' and payload->>'role' = $2 order by id desc limit 1`, startedParent, string(claim.RoleImplementer)).Scan(&task); err != nil {
			t.Fatalf("read the implementer's start of %s: %v", startedParent, err)
		}
		if !strings.HasPrefix(task, moved.ChildrenWait.Task) {
			t.Fatalf("the implementer's start carries %q, want the task it held, %q", task, moved.ChildrenWait.Task)
		}
	})

	// The release of a waiting parent's quiet pod arms no backstop for any role of it: the release
	// in implementing arms the released implementer's, and the same release once the tick has
	// moved the parent into awaiting_children cancels that earlier backstop and arms none, since
	// no role works the phase (outbox.armBackstops, workflow.OpenPhaseRole).
	t.Run("a release of the waiting parent's pod arms no backstop", func(t *testing.T) {
		ctx := context.Background()
		pool := isolatedOutboxPool(t)
		client := &tickDispatch{summaries: []dispatch.IssueSummary{filed}}
		w := tickRuntime(t, pool, client)
		seedStartedParent(t, pool, startedParent)
		project, err := claim.ProjectToken("CAPTURE")
		if err != nil {
			t.Fatalf("project token: %v", err)
		}
		token, err := claim.NewToken(project, startedParent, claim.RoleImplementer)
		if err != nil {
			t.Fatalf("claim token: %v", err)
		}
		st := outboxTreeStore(t, pool)
		if err := st.PutClaim(ctx, supervise.Claim{Token: token, Project: project, Tree: startedParent, Issue: startedParent,
			Role: claim.RoleImplementer, TreeEpoch: 1, Generation: 1, ServingGeneration: 1, State: supervise.StateSuspended,
			SuspendedBy: supervise.SuspendedByReleased, Session: "ses-implementer", SessionFile: "/tmp/implementer.jsonl",
			PluginContract: api.DaemonAPIVersion}); err != nil {
			t.Fatalf("seed the released implementer: %v", err)
		}
		runner := &outbox{pool: pool, records: record.NewStore(), project: project, dispatchProject: "CAPTURE", trees: st,
			log: quietLogger(), backstop: time.Hour, now: time.Now}
		armed := func() int {
			t.Helper()
			var open int
			if err := pool.QueryRow(ctx, `select count(*) from pod_wake_backstops where issue = $1 and not fired`, startedParent).Scan(&open); err != nil {
				t.Fatalf("count the open backstops: %v", err)
			}
			return open
		}

		if err := runner.armBackstops(ctx, tickIssue(t, pool, startedParent)); err != nil {
			t.Fatalf("arm the earlier release's backstops: %v", err)
		}
		if open := armed(); open != 1 {
			t.Fatalf("open backstops after the release in implementing = %d, want the implementer's one", open)
		}

		w.applyControllerTick(ctx, time.Now())
		waiting := tickIssue(t, pool, startedParent)
		if waiting.Phase != phase.AwaitingChildren {
			t.Fatalf("%s after the turn = %s, want awaiting_children", startedParent, waiting.Phase)
		}

		if err := runner.armBackstops(ctx, waiting); err != nil {
			t.Fatalf("arm the release's backstops: %v", err)
		}
		if open := armed(); open != 0 {
			t.Fatalf("open backstops after the release in awaiting_children = %d, want none", open)
		}
	})
}

// sortedKeys is keys sorted, in a slice of its own.
func sortedKeys(keys []string) []string {
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	return sorted
}

// sameChildrenWait says whether a and b are one held start, its moment compared as an instant.
func sameChildrenWait(a, b *record.ChildrenWait) bool {
	return a != nil && b != nil && a.Start == b.Start && a.Task == b.Task && a.Reason == b.Reason && a.Since.Equal(b.Since)
}
