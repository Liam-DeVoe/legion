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
// step, which an issue with no plan rows keeps on its implementer's phases row
// (record.ImplementerCounts), which tells each implementing, testing, and reviewing pass from the
// last — the place of the completing role's step where the task names the role at two places (0
// otherwise), the handoff record the implementer last named for a file-backed phase
// (record.PhaseRow.LastHandoff), the phase entry the completing role's current phase is on
// (record.PhaseRow.Entry, 0 on a row no start has stamped), whether the start that raised it
// moved the issue back (record.PhaseRow.EntryMovedBack) and the highest entry another step's row of
// that role holds (record.OtherStepsEntry), its pull request record, nil while none is recorded,
// and its production check, nil while none was ever opened. Each role's row is the row of its step
// where the task stands (record.PhaseRowAt), as the workflow reads it, so a role that stands at two
// steps is held to the entry of the step it completes, and a turn of its other step is told apart.
type completionPosition struct {
	issue           *record.Issue
	treeGeneration  uint64
	lingers         bool
	task            string
	pass            int
	place           int
	entry           int64
	entryMovedBack  bool
	otherStepsEntry int64
	lastHandoff     string
	pullRequest     *record.PullRequest
	check           *record.ProductionCheck
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
	stored, err := s.records.Plan(ctx, tx, key, issue.Generation)
	if err != nil {
		return completionPosition{}, err
	}
	var task string
	var at record.TaskPlace
	var steps []record.PlanStep
	counts := record.ImplementerCounts(rows)
	if stored != nil {
		running, err := stored.Task()
		if err != nil {
			return completionPosition{}, err
		}
		task, counts, at, steps = running.Task, running.Counts, running.Place, stored.Steps[running.Task]
	}
	completing := record.PhaseRowAt(rows, role, task, at, steps)
	place := 0
	if record.StandsTwice(role, steps) {
		place = record.RolePlace(role, at, steps)
	}
	pr, err := s.records.PullRequest(ctx, tx, key)
	if err != nil {
		return completionPosition{}, err
	}
	check, err := s.records.ProductionCheck(ctx, tx, key)
	if err != nil {
		return completionPosition{}, err
	}
	return completionPosition{issue: issue, treeGeneration: root.Generation, lingers: root.Lingers(), task: task, pass: counts.Pass, place: place,
		lastHandoff: record.PhaseRowAt(rows, claim.RoleImplementer, task, at, steps).LastHandoff, entry: completing.Entry, entryMovedBack: completing.EntryMovedBack,
		otherStepsEntry: record.OtherStepsEntry(rows, completing), pullRequest: pr, check: check}, tx.Commit(ctx)
}
