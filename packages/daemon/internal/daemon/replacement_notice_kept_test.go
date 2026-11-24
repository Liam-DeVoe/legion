package daemon

// R7 of dispatch://INFRA-3271, GPT-6.1 Sol's point 2 on #5285 at 0259f8af (and INFRA-341's
// reviewer's point 10, its enqueue part), which INFRA-3118's architect ruled a fix inside R7: the
// issue's architect's word of what a replacement took is a durable pending row, keyed by the issue
// and the pod, which only the commit of its outbox row clears. A store that refuses the outbox row
// once, after the replacement took the pod, loses nothing: the round ends, or the launch goes on,
// and once the store takes rows again the notice goes out, once.

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// refuseWorkspaceTaken has the store refuse every outbox row of a workspace-taken notice, as a
// store that fails the write does, until the func it answers lifts the refusal.
func (r *podUpdateRig) refuseWorkspaceTaken() (lift func()) {
	r.t.Helper()
	for _, statement := range []string{
		`create function refuse_workspace_taken() returns trigger language plpgsql as $$
		begin
			if new.kind = '` + string(record.OutboxKindNotice) + `' and (new.payload::jsonb)->>'kind' = '` + string(record.WorkspaceTakenNotice) + `' then
				raise exception 'the store refuses the workspace-taken notice';
			end if;
			return new;
		end $$`,
		`create trigger refuse_workspace_taken before insert on outbox for each row execute function refuse_workspace_taken()`,
	} {
		if _, err := r.pool.Exec(r.ctx, statement); err != nil {
			r.t.Fatalf("make the store refuse the workspace-taken notice: %v", err)
		}
	}
	return func() {
		r.t.Helper()
		if _, err := r.pool.Exec(r.ctx, `drop trigger refuse_workspace_taken on outbox`); err != nil {
			r.t.Fatalf("lift the store's refusal: %v", err)
		}
	}
}

// wantToldOnce sweeps roundSweeps times and holds the outbox to one workspace-taken notice to the
// issue's architect, naming the commit the read before the replacement found.
func (r *podUpdateRig) wantToldOnce(what string) {
	r.t.Helper()
	for range roundSweeps {
		r.tick()
	}
	told := r.architectNotices(record.WorkspaceTakenNotice)
	if len(told) != 1 || !strings.Contains(told[0].Reason, passedOverCommit) {
		r.t.Fatalf("the architect was told %+v %s, want one workspace-taken notice naming the commit %s", told, what, passedOverCommit)
	}
}

func TestAWorkspaceTakenNoticeTheStoreRefusedGoesOutOnceTheStoreTakesIt(t *testing.T) {
	t.Parallel()

	// A round over a drift that breaks the pod replaces it over a workspace that holds an unpushed
	// commit. The store refuses the notice: the round ends all the same.
	t.Run("a_round's_replacement", func(t *testing.T) {
		t.Parallel()
		rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
		rig.rt.ScriptWorkspaceWork(podUpdateIssue, roundUnpushed)
		lift := rig.refuseWorkspaceTaken()
		rig.answerRound(roundBreakingDrift, claim.RoleImplementer, claim.RoleTester)
		rig.awaitReplacement(1, "of a round over a drift that breaks the pod")
		for _, role := range []claim.Role{claim.RoleImplementer, claim.RoleTester} {
			rig.awaitClaim(role, "its round to end with the replacement", func(c supervise.Claim) bool { return c.UpdateRound == "" })
		}
		if told := rig.architectNotices(record.WorkspaceTakenNotice); len(told) != 0 {
			t.Fatalf("the store refused every workspace-taken notice, yet the outbox holds %+v", told)
		}
		lift()
		rig.wantToldOnce("once the store takes the row")
	})

	// A launch's own replacement of the issue's pod hands the daemon what it took once its
	// suspension landed. The store refuses the notice; the launch is done, and the pod is gone.
	t.Run("a_launch's_own_replacement", func(t *testing.T) {
		t.Parallel()
		rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer))
		lift := rig.refuseWorkspaceTaken()
		rig.s.tellLaunchTook(rig.ctx, runtime.TakenPod{Operation: "launch-op-1", Claim: rig.tokens[claim.RoleImplementer], Issue: podUpdateIssue, PodUID: podUpdatePod,
			Why:   "a facet of the pod that is fixed for its life moved, so the launching role cannot start in that pod",
			Takes: runtime.TakesPod, Target: podUpdatePod, Work: roundUnpushed})
		if told := rig.architectNotices(record.WorkspaceTakenNotice); len(told) != 0 {
			t.Fatalf("the store refused every workspace-taken notice, yet the outbox holds %+v", told)
		}
		rig.rt.ScriptSuspendedOver(podUpdatePod)
		lift()
		rig.wantToldOnce("once the store takes the row")
	})
}
