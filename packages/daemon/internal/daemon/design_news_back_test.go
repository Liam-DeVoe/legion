package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	planwords "github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// planSteps writes the plan the owner gave key for generation 1: one task, a, whose steps are
// words, each `role` or `role:key`, standing at its step position.
func (r *newsReconcileRig) planSteps(key string, position int, words ...string) {
	r.t.Helper()
	version := record.PlanVersion{Issue: key, Generation: 1, Version: 2, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner,
		GivenBy: mustClaimToken(r.t, reconcileRoot, claim.RoleArchitect), GivenAt: time.Now().UTC().Add(-time.Hour)}
	task := record.PlanTask{Issue: key, Generation: 1, Task: "a", PlanVersion: 2, BlockText: "Build the widget.",
		State: record.TaskRunning, Place: record.TaskPlace{Kind: record.PlaceStep, Position: position}}
	roles := planwords.BuiltinRoles()
	steps := make([]record.PlanStep, 0, len(words))
	for i, word := range words {
		role, agent, _ := strings.Cut(word, ":")
		steps = append(steps, record.PlanStep{Issue: key, Generation: 1, Task: "a", Position: i + 1, Role: role, AgentKey: agent,
			Type: roles[role].Type})
	}
	r.inTx(func(tx pgx.Tx) error { return r.records.PutPlan(r.ctx, tx, version, []record.PlanTask{task}, steps) })
}

// The architect that holds an issue in accepting answers its news by its sign-off or by a move
// back, and the move back reaches the phase of each step of the issue's task before its acceptance
// and no other. On the plan `implementer reviewer acceptance:owner` that is implementing and
// reviewing: the news names those two, and neither planning nor testing, which the task has no
// step for, so the move would be refused. Both forms that name the answer say so: the holder form
// of the member's news, and the line of the root architect's summary for a member it answers
// itself.
func TestTheAcceptingAnswerNamesTheEarlierStepsOfTheTask(t *testing.T) {
	t.Parallel()
	r := newNewsReconcileRig(t)
	r.member(reconcileRoot, "", phase.Implementing)
	r.member(reconcileChild, reconcileRoot, phase.Accepting)
	r.planSteps(reconcileChild, 3, "implementer", "reviewer", "acceptance:owner")
	r.told(reconcileRoot, record.AudienceHolder, 5)
	r.told(reconcileRoot, record.AudienceArchitect, 4)
	r.told(reconcileChild, record.AudienceHolder, 4)
	r.holder(reconcileRoot, claim.RoleArchitect, supervise.StateWorking, "", 0)

	if err := r.reconcile(0); err != nil {
		t.Fatalf("reconcile the tree's news: %v", err)
	}
	starts := r.newsStarts()
	if got := startsTo(starts); len(got) != 1 || got[0] != reconcileRoot+"/architect@" {
		t.Fatalf("the news-only starts = %v, want the owning root architect's alone", got)
	}
	machine := r.machine(reconcileRoot, claim.RoleArchitect, supervise.StateIdle, "", 0)
	if err := r.runner.execute(r.ctx, starts[0].row); err != nil {
		t.Fatalf("run the architect's news-only start: %v", err)
	}
	task, _ := pendingNews(t, machine)
	t.Logf("the architect's task:\n%s", task)
	for _, want := range []string{
		"sign " + reconcileChild + " off with `sign_off`, or call `request_backward_move` to move it back to implementing or reviewing, with a reason",
		"you answer it yourself: read what changed, then sign it off with `sign_off`, or call `request_backward_move` to move it back to implementing or reviewing.",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("the architect's task does not say %q", want)
		}
	}
	if all, named := strings.Count(task, "move it back to"), strings.Count(task, "move it back to implementing or reviewing"); all != named {
		t.Errorf("the architect's task names a move back %d times and %d of them as implementing or reviewing, want every one: planning and testing are no step of the task of %s",
			all, named, reconcileChild)
	}
}
