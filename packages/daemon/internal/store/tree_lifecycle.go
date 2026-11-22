package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/treelifecycle"
)

// OpenTreeLifecycle opens a tree in a transaction of its own and runs nothing beside the open; a
// test and a seed open a tree through it. Workflow admission opens a tree inside its fact
// transaction through record.Store.OpenTreeLifecycle instead, and the operator route through
// OpenOperatorTreeLifecycle, because its open runs the tree's draw.
func (s *Store) OpenTreeLifecycle(ctx context.Context, project, tree string, authority treelifecycle.Authority) (treelifecycle.Lifecycle, error) {
	return s.openLifecycle(ctx, treelifecycle.Trees, project, tree, authority, runNothing)
}

// OpenOperatorTreeLifecycle is the authenticated operator route's open of a root's tree, before
// its claim is persisted (internal/api's spawn). created runs in the same transaction as the open
// and only where this call created the tree's row (treelifecycle.Lifecycle.Created): the tree's
// experiments are drawn there (internal/experiment), so no crash between the two can leave a
// lifecycle whose tree was never drawn for. A failing created fails the open, and the row goes
// with the transaction.
func (s *Store) OpenOperatorTreeLifecycle(ctx context.Context, project, tree string,
	created func(ctx context.Context, tx pgx.Tx) error) (treelifecycle.Lifecycle, error) {
	return s.openLifecycle(ctx, treelifecycle.Trees, project, tree, treelifecycle.AuthorityOperator, created)
}

// openLifecycle is the open every entry point of either unit shares: one transaction holding the
// open of key's lifecycle and whatever created runs where the open created its row.
func (s *Store) openLifecycle(ctx context.Context, unit treelifecycle.Unit, project, key string, authority treelifecycle.Authority,
	created func(ctx context.Context, tx pgx.Tx) error) (treelifecycle.Lifecycle, error) {
	var lifecycle treelifecycle.Lifecycle
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		opened, err := unit.Open(ctx, tx, project, key, authority)
		if err != nil {
			return err
		}
		if opened.Created {
			if err := created(ctx, tx); err != nil {
				return err
			}
		}
		lifecycle = opened
		return nil
	})
	if err != nil {
		return treelifecycle.Lifecycle{}, err
	}
	return lifecycle, nil
}

// runNothing is the created of every open but the operator's: nothing is drawn or written beside
// those opens, so there is nothing for the transaction to hold.
func runNothing(context.Context, pgx.Tx) error { return nil }

// AdmitClaim binds a new or reactivated claim to its issue's open lifecycle and writes both in one
// short globally serialized transaction. The caller receives the epoch it must carry through every
// later launch (supervise.Claim.IssueEpoch); runtime work happens after this transaction commits.
// The project controller's claim (`controller: daemon`) belongs to no issue, so it binds no
// lifecycle: it is written at epoch zero.
//
// It writes the two facts the release before reads of the claim, which no decision of this one
// reads (R22 of dispatch://INFRA-3320): the claim's tree is the tree its issue's record stores
// (issues.tree, written when the daemon first recorded the issue), and an operator's claim on an
// issue this project records nothing of keeps the tree the operator named; and its tree epoch is
// the epoch that tree's lifecycle stands at, 0 where the tree has none, whatever state its row is
// in, since only the issue's lifecycle admits or refuses the claim. The record is the project's
// own (lower(project) = project, the token), as IssueLive reads it: another project's record
// of the same key is no record of this one's.
func (s *Store) AdmitClaim(ctx context.Context, c supervise.Claim) (supervise.Claim, error) {
	var queued bool
	err := s.Tx(ctx, func(tx pgx.Tx) (err error) {
		if claim.IsController(c.Role, c.Issue, c.Tree) {
			c.TreeEpoch, c.IssueEpoch = 0, 0
			queued, err = s.putClaim(ctx, tx, c)
			return err
		}
		epoch, err := treelifecycle.Issues.BindWork(ctx, tx, c.Project, c.Issue)
		if err != nil {
			return err
		}
		c.IssueEpoch = epoch
		var stored string
		switch err := tx.QueryRow(ctx, `select tree from issues where key = $1 and lower(project) = $2`, c.Issue, c.Project).Scan(&stored); {
		case err == nil:
			c.Tree = stored
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read the tree %s's record stores: %w", c.Issue, err)
		}
		tree, found, err := treelifecycle.Trees.Read(ctx, tx, c.Project, c.Tree)
		if err != nil {
			return err
		}
		c.TreeEpoch = 0
		if found {
			c.TreeEpoch = tree.Epoch
		}
		queued, err = s.putClaim(ctx, tx, c)
		return err
	})
	if err != nil {
		return supervise.Claim{}, fmt.Errorf("admit claim %s: %w", c.Token, err)
	}
	s.CommittedClaim(c, queued)
	return c, nil
}

// CheckLaunch rejects a delayed claim before it can persist StateLaunching or call a runtime after
// its issue's lifecycle reserved cleanup, or opened another epoch than the one the claim bound.
// The machine returns this named error directly, so outbox retry preserves its claim and launch
// budget. The project controller's claim, on no issue, has no cleanup to meet.
func (s *Store) CheckLaunch(ctx context.Context, c supervise.Claim) error {
	if claim.IsController(c.Role, c.Issue, c.Tree) {
		return nil
	}
	return s.Tx(ctx, func(tx pgx.Tx) error {
		if err := treelifecycle.Issues.CheckWork(ctx, tx, c.Project, c.Issue, c.IssueEpoch); err != nil {
			return fmt.Errorf("launch claim %s: %w", c.Token, err)
		}
		return nil
	})
}
