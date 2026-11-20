package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The release of a waiting parent's quiet pod arms no backstop for any role of it
// (dispatch://INFRA-3320 R11): the phase no role works has no worker whose wake is due
// (workflow.OpenPhaseRole answers none for awaiting_children), so the release's arming
// (outbox.armBackstops) cancels the backstops an earlier release armed, as every arming does,
// and arms none. The control is the same release of the same issue in implementing, which arms
// the released implementer's.
func TestTheReleaseOfAWaitingParentsPodArmsNoBackstop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		at    phase.Phase
		armed int
	}{
		{name: "in implementing, the control", at: phase.Implementing, armed: 1},
		{name: "in awaiting_children", at: phase.AwaitingChildren},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
				Project: "LEGION", Tree: "LEGION-208", Title: "root", Phase: tc.at, Generation: 1, Status: "in_progress", Rank: "U",
				LastDispatchSeq: 1}
			if tc.at == phase.AwaitingChildren {
				waited := time.Now().UTC().Add(-time.Hour)
				issue.ChildrenWait = &record.ChildrenWait{Start: phase.Implementing, Task: "Continue root. Issue: LEGION-208. Phase: implementing.",
					Reason: "the admission pass stopped the implementer's implementing phase, while these Dispatch children of LEGION-208 were open: LEGION-209 (todo)",
					Since:  waited}
				issue.ForwardMergeOwed, issue.ChildrenWaitedAt = true, &waited
			}
			putOutboxIssue(t, pool, records, issue)
			st := outboxTreeStore(t, pool, issue.Key)
			// The implementer as the idle release leaves it: suspended with the source
			// `released`, serving the issue's generation.
			if err := st.PutClaim(ctx, supervise.Claim{Token: mustClaimToken(t, issue.Key, claim.RoleImplementer), Project: "legion",
				Tree: issue.Tree, Issue: issue.Key, Role: claim.RoleImplementer, TreeEpoch: 1, Generation: 1, ServingGeneration: 1,
				State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByReleased, Session: "ses-implementer",
				SessionFile: "/tmp/implementer.jsonl", PluginContract: api.DaemonAPIVersion}); err != nil {
				t.Fatalf("seed the released implementer: %v", err)
			}
			// The backstop an earlier release of this pod armed, still open.
			if err := st.ArmPodBackstop(ctx, store.PodWakeBackstop{Project: "legion", Issue: issue.Key, Role: claim.RoleImplementer,
				DueAt: time.Now().UTC().Add(30 * time.Minute)}); err != nil {
				t.Fatalf("arm the earlier release's backstop: %v", err)
			}
			runner := &outbox{pool: pool, records: records, project: "legion", dispatchProject: "LEGION", trees: st,
				log: quietLogger(), backstop: time.Hour, now: time.Now}

			if err := runner.armBackstops(ctx, issue); err != nil {
				t.Fatalf("arm the release's backstops: %v", err)
			}

			var open int
			if err := pool.QueryRow(ctx, `select count(*) from pod_wake_backstops where issue = $1 and not fired and role = 'implementer'
				and due_at > now() + interval '45 minutes'`, issue.Key).Scan(&open); err != nil {
				t.Fatalf("count the open backstops: %v", err)
			}
			var earlier int
			if err := pool.QueryRow(ctx, `select count(*) from pod_wake_backstops where issue = $1 and not fired
				and due_at <= now() + interval '45 minutes'`, issue.Key).Scan(&earlier); err != nil {
				t.Fatalf("count the earlier backstops: %v", err)
			}
			if open != tc.armed || earlier != 0 {
				t.Fatalf("after the release in %s: %d backstops of this release open and %d of the earlier one; want %d and none",
					tc.at, open, earlier, tc.armed)
			}
		})
	}
}
