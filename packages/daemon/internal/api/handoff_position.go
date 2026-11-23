package api

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// completionPosition is where issue key stands as the completion route reads it, in one repeatable-read
// snapshot: the issue record, its tree's generation (the root's) and whether the tree lingers, the
// task the issue runs (record.StoredPlan.Task; "" for an issue with no plan rows at its
// generation, which walks today's line), its pass — that task's count of returns to an earlier
// step, which an issue with no plan rows keeps on its implementer's row
// (record.ImplementerCounts), which tells each implementing, testing, and reviewing pass from the
// last — the handoff record the implementer last named for a file-backed phase
// (record.PhaseRow.LastHandoff), the phase entry the completing role's current phase is on
// (record.PhaseRow.Entry, 0 on a row no start has stamped) and whether the start that raised it
// moved the issue back (record.PhaseRow.EntryMovedBack), its pull request record, nil while none
// is recorded, and its production check, nil while none was ever opened.
//
// Of a plan of several tasks the task is the one whose step the completing claim's agent runs
// (record.Issue.StepOfAgent), named in ofTask, and the position is that task's: issue stands in
// that task's own phase and hold, the pass is that task's, the pull request is that task's own
// row (record.Store.PullRequestOfTask), branch its branch (record.PlanTask.BranchOf) and base the
// branch its pull request is against (record.StoredPlan.BaseOf). noStep says the agent runs no
// step of such a plan, whose completion is of no phase of it. Of a plan of one task, or none,
// ofTask is "" and branch the issue's own, as before tasks, and no base is read ("").
type completionPosition struct {
	issue          *record.Issue
	treeGeneration uint64
	lingers        bool
	task           string
	ofTask         string
	noStep         bool
	branch         string
	base           string
	pass           int
	entry          int64
	entryMovedBack bool
	lastHandoff    string
	pullRequest    *record.PullRequest
	check          *record.ProductionCheck
}

// handoffPosition reads key's position (completionPosition) for the completing role and the claim
// that completes, whose phases row names the entry it was last raised to; an issue the workflow
// does not record is a position with no issue and no error.
func (s *server) handoffPosition(ctx context.Context, key string, role claim.Role, completing claim.Token) (completionPosition, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return completionPosition{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	issue, err := s.records.Issue(ctx, tx, key)
	if err != nil || issue == nil {
		return completionPosition{}, err
	}
	root := issue
	if !claim.IsTreeRoot(issue.Key, issue.Tree) {
		if root, err = s.records.Issue(ctx, tx, issue.Tree); err != nil {
			return completionPosition{}, err
		}
		if root == nil {
			return completionPosition{}, fmt.Errorf("the tree root %s of %s is not recorded", issue.Tree, issue.Key)
		}
	}
	rows, err := s.records.Phases(ctx, tx, key)
	if err != nil {
		return completionPosition{}, err
	}
	row := record.PhaseRowOf(rows, role)
	stored, err := s.records.Plan(ctx, tx, key, issue.Generation)
	if err != nil {
		return completionPosition{}, err
	}
	position := completionPosition{issue: issue, treeGeneration: root.Generation, lingers: root.Lingers(), branch: claim.IssueBranch(key),
		lastHandoff: record.PhaseRowOf(rows, claim.RoleImplementer).LastHandoff, entry: row.Entry, entryMovedBack: row.EntryMovedBack}
	counts := record.ImplementerCounts(rows)
	switch {
	case stored != nil && len(stored.Tasks) > 1:
		_, agent, _ := completing.Cut()
		step, ok := issue.StepOfAgent(agent)
		task, onPlan := stored.TaskOf(step.Task)
		if !ok || !onPlan {
			position.noStep = true
			break
		}
		standing := *issue
		standing.Phase, standing.Hold = record.TaskStanding(step.Phase, task.State, task.HoldReason)
		position.issue, position.task, position.ofTask, position.branch = &standing, task.Task, task.Task, task.BranchOf(key)
		if position.base, _, err = stored.BaseOf(*issue, task.Task); err != nil {
			return completionPosition{}, err
		}
		counts = task.Counts
		if position.pullRequest, err = s.records.PullRequestOfTask(ctx, tx, key, task.Task); err != nil {
			return completionPosition{}, err
		}
	case stored != nil:
		running, err := stored.Task()
		if err != nil {
			return completionPosition{}, err
		}
		position.task, counts = running.Task, running.Counts
	}
	position.pass = counts.Pass
	if position.ofTask == "" && !position.noStep {
		if position.pullRequest, err = s.records.MergePullRequest(ctx, tx, key); err != nil {
			return completionPosition{}, err
		}
	}
	if position.check, err = s.records.ProductionCheck(ctx, tx, key); err != nil {
		return completionPosition{}, err
	}
	return position, tx.Commit(ctx)
}
