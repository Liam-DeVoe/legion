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
// task the issue runs (runningTask; "" for an issue with no plan rows at its generation, which
// walks today's line), its pass — that task's count of returns to an earlier step
// (record.Postgres.ReturnCounts), the implementer's row's for an issue with no plan rows, which
// tells each implementing, testing, and reviewing pass from the last — the handoff record the
// implementer last named for a file-backed phase (record.PhaseRow.LastHandoff), the phase entry
// the completing role's current phase is on (record.PhaseRow.Entry, 0 on a row no start has
// stamped) and whether the start that raised it moved the issue back
// (record.PhaseRow.EntryMovedBack), its pull request record, nil while none is recorded, and its
// production check, nil while none was ever opened.
type completionPosition struct {
	issue          *record.Issue
	treeGeneration uint64
	lingers        bool
	task           string
	pass           int
	entry          int64
	entryMovedBack bool
	lastHandoff    string
	pullRequest    *record.PullRequest
	check          *record.ProductionCheck
}

// handoffPosition reads key's position (completionPosition) for the completing role, whose phases
// row names the entry it was last raised to; an issue the workflow does not record is a position
// with no issue and no error.
func (s *server) handoffPosition(ctx context.Context, key string, role claim.Role) (completionPosition, error) {
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
	implementer := record.PhaseRowOf(rows, claim.RoleImplementer)
	completing := record.PhaseRowOf(rows, role)
	pass, lastHandoff := implementer.Pass, implementer.LastHandoff
	stored, err := s.records.Plan(ctx, tx, key, issue.Generation)
	if err != nil {
		return completionPosition{}, err
	}
	var task string
	if stored != nil {
		if task, err = runningTask(stored); err != nil {
			return completionPosition{}, err
		}
		counts, err := s.records.ReturnCounts(ctx, tx, key, issue.Generation, task)
		if err != nil {
			return completionPosition{}, err
		}
		pass = counts.Pass
	}
	pr, err := s.records.PullRequest(ctx, tx, key)
	if err != nil {
		return completionPosition{}, err
	}
	check, err := s.records.ProductionCheck(ctx, tx, key)
	if err != nil {
		return completionPosition{}, err
	}
	return completionPosition{issue: issue, treeGeneration: root.Generation, lingers: root.Lingers(), task: task, pass: pass,
		lastHandoff: lastHandoff, entry: completing.Entry, entryMovedBack: completing.EntryMovedBack,
		pullRequest: pr, check: check}, tx.Commit(ctx)
}

// runningTask is the task of stored its issue runs: the task whose state is running, and the
// plan's only task while none runs (a task its owner stopped). A plan of several tasks none of
// which runs names no task a completion could be of, which is an error: this release's plans hold
// one task (plan.Supported), and the workflow walks no other (workflow's Engine.walk).
func runningTask(stored *record.StoredPlan) (string, error) {
	for _, task := range stored.Tasks {
		if task.State == record.TaskRunning {
			return task.Task, nil
		}
	}
	if len(stored.Tasks) == 1 {
		return stored.Tasks[0].Task, nil
	}
	v := stored.Version
	return "", fmt.Errorf("the plan of %s at generation %d holds %d tasks and none of them runs, so no task is the one a completion is of", v.Issue, v.Generation, len(stored.Tasks))
}
