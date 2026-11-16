package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// HoldSession records session as token's claim's when no claim has recorded it, and answers the
// claim the table holds it for (supervise.Store.HoldSession): token itself when it recorded the
// session before or no claim had, another claim when that one recorded it first, and the empty
// token for an id two claims recorded before the table existed, which is held for none
// (migration 20261116144907_claim_sessions). A row is never deleted, and nothing but an operator
// settling a held-for-none id moves one, so the answer about a session never changes once given.
//
// The insert and the read are two statements of one transaction, so two claims registering one
// new id at once cannot both take it: the second's insert waits on the first's row, finds the
// conflict once the first commits, and its read, a statement of its own, sees the row the first
// committed.
func (s *Store) HoldSession(ctx context.Context, token claim.Token, session string) (claim.Token, error) {
	var holder string
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into claim_sessions (session, claim) values ($1, $2)
			on conflict (session) do nothing`, session, string(token)); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select claim from claim_sessions where session = $1`, session).Scan(&holder)
	}); err != nil {
		return "", fmt.Errorf("hold session %s for %s: %w", session, token, err)
	}
	return claim.Token(holder), nil
}

// HeldForNone is one session id the claim_sessions migration found recorded by more than one
// claim, and so held for none of them, with every claim whose row records it now: as its session,
// or as the session it last abandoned.
type HeldForNone struct {
	Session string
	Claims  []claim.Token
}

// SessionsHeldForNone is every id held for no claim (HoldSession's empty holder), oldest first,
// each with the claims whose rows record it, in token order. The daemon logs each at boot for an
// operator to settle by hand: `update claim_sessions set claim = '<token>' where session = '<id>'`.
func (s *Store) SessionsHeldForNone(ctx context.Context) ([]HeldForNone, error) {
	rows, err := s.pool.Query(ctx, `select h.session, coalesce(array_agg(c.token order by c.token)
			filter (where c.token is not null), '{}')
		from claim_sessions h
		left join claims c on c.session = h.session or c.abandoned_session ->> 'session' = h.session
		where h.claim = ''
		group by h.session, h.recorded_at
		order by h.recorded_at, h.session`)
	if err != nil {
		return nil, fmt.Errorf("read the sessions held for no claim: %w", err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (HeldForNone, error) {
		var h HeldForNone
		var tokens []string
		if err := row.Scan(&h.Session, &tokens); err != nil {
			return HeldForNone{}, err
		}
		for _, token := range tokens {
			h.Claims = append(h.Claims, claim.Token(token))
		}
		return h, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the sessions held for no claim: %w", err)
	}
	return held, nil
}
