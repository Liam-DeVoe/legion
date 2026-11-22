package daemon

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// ownerActsAs is the daemon's one runtime.ActsAs, which both runtimes are given: for the token of
// an architect that is the owner of the issue it names (record.OwnerClaim), the role of the step
// that owner does now (record.OwnerActsAs), so its slot holds that step's App while it does the
// step and its own role's App again after; false for every other token, an issue the daemon does
// not record, and an issue whose owner is another claim. The issue is read in a read-only
// transaction of its own, at each write of a slot's gh files that asks (runtime.SlotRole).
//
// A token spells its issue's key lowercased (claim.AgentToken), and a key's prefix is capitals and
// digits alone (claim.IsIssueKey), so the key is the token's issue part with that prefix in
// capitals; the owner check then holds the token to the issue's own project as well.
func ownerActsAs(pool *store.Pool, records record.Store) runtime.ActsAs {
	return func(ctx context.Context, token claim.Token) (claim.Role, bool, error) {
		part, agent, ok := token.Cut()
		if !ok || agent != claim.Agent(claim.RoleArchitect) {
			return "", false, nil
		}
		// part is `legion-<project>-<prefix>-<number>`: no project token holds a dash.
		names := strings.Split(part, "-")
		if len(names) != 4 {
			return "", false, nil
		}
		key := strings.ToUpper(names[2]) + "-" + names[3]
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
			role, acting = record.OwnerActsAs(*issue)
			return nil
		})
		if err != nil {
			return "", false, err
		}
		return role, acting, nil
	}
}
