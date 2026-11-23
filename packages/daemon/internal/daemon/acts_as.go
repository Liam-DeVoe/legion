package daemon

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// ownerActsAs is the daemon's one runtime.ActsAs, which both runtimes are given: for the token of
// an architect that is the owner of the issue it names (record.OwnerClaim), the role of the step
// that owner does now, or of the retro it does as the role of its own implementation step
// (record.OwnerActsAs), so its slot holds that role's App while it does the step or the retro and
// its own role's App again after; false for every other token, an issue the daemon does not
// record, and an issue whose owner is another claim. The issue, and at its retro the plan rows of
// its task (taskSteps), are read in a read-only transaction of their own, at each write of a slot's
// gh files that asks (runtime.SlotRole). The key is the token's own (claim.Token.IssueKey), and the
// owner check then holds the token to the issue's own project as well.
func ownerActsAs(pool *store.Pool, records record.Store) runtime.ActsAs {
	return func(ctx context.Context, token claim.Token) (claim.Role, bool, error) {
		_, agent, ok := token.Cut()
		if !ok || agent != claim.Agent(claim.RoleArchitect) {
			return "", false, nil
		}
		key, ok := token.IssueKey()
		if !ok {
			return "", false, nil
		}
		var role claim.Role
		var acting bool
		err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			issue, err := records.Issue(ctx, tx, key)
			if err != nil || issue == nil {
				return err
			}
			owner, err := record.OwnerClaim(*issue)
			if err != nil || owner != token {
				return err
			}
			var steps []record.PlanStep
			if issue.Phase == phase.Retro {
				if steps, err = taskSteps(ctx, tx, records, *issue); err != nil {
					return err
				}
			}
			role, acting = record.OwnerActsAs(*issue, steps)
			return nil
		})
		if err != nil {
			return "", false, err
		}
		return role, acting, nil
	}
}

// taskSteps is the plan rows of the one task of issue's run, which the role its retro is done as is
// read from (record.RetroActsAs), and nil for a run with no plan rows.
func taskSteps(ctx context.Context, tx pgx.Tx, records record.Store, issue record.Issue) ([]record.PlanStep, error) {
	stored, err := records.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil || stored == nil {
		return nil, err
	}
	task, err := stored.Task()
	if err != nil {
		return nil, err
	}
	return stored.Steps[task.Task], nil
}
