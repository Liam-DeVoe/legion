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
// an architect that is the owner of the issue it names (record.OwnerClaim), the role of the act
// that owner does now (record.OwnerActsAs): the implementer while the record holds a merge forward
// asked of it that has not ended (record.OwnersMergeForward), else the role of the step it does, or
// of the retro it does as the role of its own implementation step. So its slot holds that role's
// App while it does the act, and its own role's App again after. It answers false for every other
// token, an issue the daemon does not record, and an issue whose owner is another claim. The
// record is read in a read-only transaction of its own (readOwnerActing), at each write of a slot's
// gh files that asks (runtime.SlotRole). The key is the token's own (claim.Token.IssueKey), and the
// owner check then holds the token to the issue's own project as well.
func ownerActsAs(pool *store.Pool, records record.Store) runtime.ActsAs {
	return func(ctx context.Context, token claim.Token) (claim.Role, bool, error) {
		acting, err := readOwnerActing(ctx, pool, records, token)
		return acting.slot, acting.slotActs, err
	}
}

// ownerActing is what the record says the owner of an issue does now, read once
// (readOwnerActing): place, the role of the step or the retro it does (record.OwnerActsAs with no
// merge forward), and slot, the role its slot acts as, which a merge forward asked of it makes the
// implementer. Each is "" and false where the owner does no such act.
type ownerActing struct {
	place     claim.Role
	placeActs bool
	slot      claim.Role
	slotActs  bool
}

// readOwnerActing reads, in one read-only transaction, what token's owner does now (ownerActing):
// the issue the token names, at its retro the plan rows of its task (taskSteps), and, for an issue
// held by nothing, what decides whether the record holds a merge forward asked of the owner that
// has not ended: the pull request, whether the issue's tree lingers, and the issue's phases rows,
// the implementer's among them (record.OwnersMergeForward). A token that is no architect's, or no
// issue's, or not the issue's owner, does nothing.
func readOwnerActing(ctx context.Context, pool *store.Pool, records record.Store, token claim.Token) (ownerActing, error) {
	var acting ownerActing
	_, agent, ok := token.Cut()
	if !ok || agent != claim.Agent(claim.RoleArchitect) {
		return acting, nil
	}
	key, ok := token.IssueKey()
	if !ok {
		return acting, nil
	}
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
		asked, err := ownersMergeForward(ctx, tx, records, *issue)
		if err != nil {
			return err
		}
		acting.place, acting.placeActs = record.OwnerActsAs(*issue, steps, false)
		acting.slot, acting.slotActs = record.OwnerActsAs(*issue, steps, asked)
		return nil
	})
	if err != nil {
		return ownerActing{}, err
	}
	return acting, nil
}

// ownersMergeForward is whether the record holds a merge forward of issue's pull request asked of
// issue's owner that has not ended (record.OwnersMergeForward), read in tx. A held issue holds none,
// so nothing more is read for it.
func ownersMergeForward(ctx context.Context, tx pgx.Tx, records record.Store, issue record.Issue) (bool, error) {
	if issue.Hold != nil || !record.InWorkflow(issue) {
		return false, nil
	}
	pr, err := records.PullRequest(ctx, tx, issue.Key)
	if err != nil || pr == nil || pr.MergeForward == nil {
		return false, err
	}
	lingers, err := record.TreeLingers(ctx, records, tx, issue.Tree)
	if err != nil {
		return false, err
	}
	rows, err := records.Phases(ctx, tx, issue.Key)
	if err != nil {
		return false, err
	}
	return record.OwnersMergeForward(issue, lingers, pr, rows), nil
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
