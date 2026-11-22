package seedcorpus

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// beginner is a pool a claim's write opens its one transaction on.
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// EndClaim ends the claim the store holds on role: for each session its held_by names now, it
// appends that held_by retracted, through the store's writer (graphstore.Write), as a later
// resolution that reads the role unclaimed states it in a deployment (the outbox's record of a
// batch routed to the role) and as the seed's handover retracts its first holder's. It answers the
// sessions whose claim it ended, and refuses a role the store holds no node of or one no session
// holds now. The role's node, its page and every tuple written before stay, so its history reads
// on. It is test support: agent-page.sh ends the seeded role's claim with it, since
// `bun run dispatch:local` runs no bus, and so no outbox that would.
func EndClaim(ctx context.Context, database beginner, role string) ([]string, error) {
	var ended []string
	err := pgx.BeginFunc(ctx, database, func(tx pgx.Tx) error {
		if err := roleHeld(ctx, tx, role); err != nil {
			return err
		}
		holders, err := graphstore.CurrentOut(ctx, tx, graphstore.KindRole, role, []string{graphstore.TupleHeldBy})
		if err != nil {
			return fmt.Errorf("read the holder of role %s: %w", role, err)
		}
		at := time.Now().UTC()
		var tuples []graphstore.Tuple
		for _, holder := range holders {
			tuples = append(tuples, roleTuple(graphstore.KindRole, role, graphstore.TupleHeldBy, holder.ToKind, holder.ToID, false, at))
			ended = append(ended, holder.ToID)
		}
		if len(tuples) == 0 {
			return fmt.Errorf("no session holds role %s now: there is no claim to end", role)
		}
		if err := graphstore.Write(ctx, tx, nil, tuples); err != nil {
			return fmt.Errorf("end the claim of role %s: %w", role, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ended, nil
}

// RestoreClaim puts back the claim of session on role, ended before (EndClaim): it appends role's
// held_by session asserted, through the store's writer, as the seed first stated it. It refuses a
// role the store holds no node of. agent-page.sh puts the seeded role's claim back with it, so the
// corpus reads as the loader wrote it for the next driver.
func RestoreClaim(ctx context.Context, database beginner, role, session string) error {
	return pgx.BeginFunc(ctx, database, func(tx pgx.Tx) error {
		if err := roleHeld(ctx, tx, role); err != nil {
			return err
		}
		tuple := roleTuple(graphstore.KindRole, role, graphstore.TupleHeldBy, graphstore.KindSession, session, true, time.Now().UTC())
		if err := graphstore.Write(ctx, tx, nil, []graphstore.Tuple{tuple}); err != nil {
			return fmt.Errorf("put back the claim of session %s on role %s: %w", session, role, err)
		}
		return nil
	})
}

// roleHeld refuses a role the store holds no node of.
func roleHeld(ctx context.Context, tx pgx.Tx, role string) error {
	var nodes int
	if err := tx.QueryRow(ctx, `select count(*) from graph_nodes where kind = $1 and id = $2`, graphstore.KindRole, role).Scan(&nodes); err != nil {
		return fmt.Errorf("read the node of role %s: %w", role, err)
	}
	if nodes != 1 {
		return fmt.Errorf("the store holds %d nodes of role %s, want one", nodes, role)
	}
	return nil
}
