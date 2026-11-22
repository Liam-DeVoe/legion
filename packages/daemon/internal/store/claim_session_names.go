package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// ClaimSessionNamesTx is every name claim_sessions holds for token's claim, in the claim's own
// project (its claims row's), read in tx and in byte order: each session id, session file name and
// id an Oh My Pi file name gives (supervise.HoldKind) the claim has recorded and no operator's
// release has freed, those of a session it abandoned among them. It is what the writer of
// legion-verdict reads a commit's Omp-Session trailer against (the daemon's legionInputs): a
// trailer that names one of them is a commit of that claim. A claim the store does not hold, one
// that holds no name, and an empty token answer none. It only reads.
func ClaimSessionNamesTx(ctx context.Context, tx pgx.Tx, token claim.Token) ([]string, error) {
	if token == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `select h.name from claims c
		join claim_sessions h on h.project = c.project and h.claim = c.token
		where c.token = $1 group by h.name order by h.name collate "C"`, string(token))
	if err != nil {
		return nil, fmt.Errorf("read the session names of %s: %w", token, err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the session names of %s: %w", token, err)
	}
	return names, nil
}
