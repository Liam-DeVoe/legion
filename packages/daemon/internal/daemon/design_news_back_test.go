package daemon

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/phase"
	planwords "github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
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

// The answer to the news of an issue in accepting names the phases a move back of it reaches,
// which the send reads from the issue's task (moveBackOf): on the plan `implementer reviewer
// acceptance:owner`, implementing and reviewing. The root architect's summary reads them for a
// member it answers itself (workflow.NewsYouAnswer) and for no other member. A member in accepting
// whose task holds no step before an acceptance is an error, so no news goes out whose answer
// names no move back.
func TestTheAcceptingAnswerReadsTheTasksStepsBeforeTheAcceptance(t *testing.T) {
	t.Parallel()
	r := newNewsReconcileRig(t)
	r.member(reconcileRoot, "", phase.Implementing)
	child := r.member(reconcileChild, reconcileRoot, phase.Accepting)
	r.planSteps(reconcileChild, 3, "implementer", "reviewer", "acceptance:owner")
	r.told(reconcileRoot, record.AudienceHolder, 5)
	r.told(reconcileChild, record.AudienceHolder, 4)
	r.holder(reconcileRoot, claim.RoleArchitect, supervise.StateWorking, "", 0)

	want := []phase.Phase{phase.Implementing, phase.Reviewing}
	var drafts []memberDraft
	r.inTx(func(tx pgx.Tx) error {
		back, err := moveBackOf(r.ctx, tx, r.records, child)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(back, want) {
			t.Errorf("the move back of %s reaches %v, want %v", reconcileChild, back, want)
		}
		root, err := r.records.Issue(r.ctx, tx, reconcileRoot)
		if err != nil {
			return err
		}
		drafts, err = readTreeMembers(r.ctx, tx, r.records, config.DesignGateRootIssues.Steps(), newNewsClaims(*root, nil))
		return err
	})
	for _, draft := range drafts {
		switch {
		case draft.issue.Key == reconcileChild && (draft.state != workflow.NewsYouAnswer || !reflect.DeepEqual(draft.back, want)):
			t.Errorf("the summary's draft of %s is %q with move back %v, want %q with %v", reconcileChild, draft.state, draft.back,
				workflow.NewsYouAnswer, want)
		case draft.issue.Key != reconcileChild && draft.back != nil:
			t.Errorf("the summary's draft of %s carries move back %v, want none: the root architect answers it no move back", draft.issue.Key, draft.back)
		}
	}

	bad := newNewsReconcileRig(t)
	bad.member(reconcileRoot, "", phase.Implementing)
	held := bad.member(reconcileChild, reconcileRoot, phase.Accepting)
	bad.planSteps(reconcileChild, 2, "implementer", "reviewer")
	bad.inTx(func(tx pgx.Tx) error {
		if back, err := moveBackOf(bad.ctx, tx, bad.records, held); err == nil {
			t.Errorf("the move back of %s in accepting on a task with no acceptance = %v, want an error", reconcileChild, back)
		}
		return nil
	})
}
