package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// errHeldElsewhere rolls back a HoldSessions transaction one of whose names another claim holds,
// so that none of the names it recorded stays.
var errHeldElsewhere = errors.New("a name is held for another claim")

// HoldSessions records every one of holds as token's claim's within project when no claim of the
// project has recorded it, and answers, in holds' order, the claim the table holds each for
// (supervise.Store.HoldSessions): token itself when it recorded the name before or no claim had,
// another claim when that one recorded it first, and the empty token for a name two claims recorded
// before the table existed, which is held for none (migration 20261116144907_claim_sessions). It
// records all of holds or none: a name another claim holds rolls back every row this call wrote. A
// row is never deleted, and nothing but an operator settling a held-for-none name moves one, so the
// answer about a name never changes once it names a claim.
//
// Each name is an insert and then a read, two statements of one transaction, so two claims
// registering one new name at once cannot both take it: the second's insert waits on the first's
// row, finds the conflict once the first commits, and its read, a statement of its own, sees the row
// the first committed. The names are taken in one order, kind then name, in every transaction, so
// two registrations sharing several names wait on each other one way and never deadlock.
func (s *Store) HoldSessions(ctx context.Context, project string, token claim.Token, holds []supervise.Hold) ([]claim.Token, error) {
	order := make([]int, len(holds))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		return cmp.Or(cmp.Compare(holds[a].Kind, holds[b].Kind), cmp.Compare(holds[a].Name, holds[b].Name))
	})
	holders := make([]claim.Token, len(holds))
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, i := range order {
			h := holds[i]
			if _, err := tx.Exec(ctx, `insert into claim_sessions (project, kind, name, claim) values ($1, $2, $3, $4)
				on conflict (project, kind, name) do nothing`, project, string(h.Kind), h.Name, string(token)); err != nil {
				return err
			}
			var holder string
			if err := tx.QueryRow(ctx, `select claim from claim_sessions where project = $1 and kind = $2 and name = $3`,
				project, string(h.Kind), h.Name).Scan(&holder); err != nil {
				return err
			}
			holders[i] = claim.Token(holder)
		}
		if slices.ContainsFunc(holders, func(holder claim.Token) bool { return holder != token }) {
			return errHeldElsewhere
		}
		return nil
	})
	if err != nil && !errors.Is(err, errHeldElsewhere) {
		return nil, fmt.Errorf("hold the session of %s: %w", token, err)
	}
	return holders, nil
}

// HeldForNone is one name of a session the claim_sessions migration found recorded by more than one
// claim, and so held for none of them, with every claim whose row records it now: of its session,
// or of the session it last abandoned (supervise.RecordedHolds).
type HeldForNone struct {
	Hold   supervise.Hold
	Claims []claim.Token
}

// SessionsHeldForNone is every name of project held for no claim (HoldSessions' empty holder),
// oldest first, each with the claims of the project whose rows record it, in token order, read by
// the rule that named it (supervise.RecordedHolds). The daemon logs each at boot and suspends its
// claims, for an operator to settle by hand (the claim_sessions migration gives the statement).
func (s *Store) SessionsHeldForNone(ctx context.Context, project string) ([]HeldForNone, error) {
	rows, err := s.pool.Query(ctx, `select kind, name from claim_sessions
		where project = $1 and claim = '' order by recorded_at, kind collate "C", name collate "C"`, project)
	if err != nil {
		return nil, fmt.Errorf("read the sessions held for no claim: %w", err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (HeldForNone, error) {
		var kind, name string
		err := row.Scan(&kind, &name)
		return HeldForNone{Hold: supervise.Hold{Kind: supervise.HoldKind(kind), Name: name}}, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the sessions held for no claim: %w", err)
	}
	if len(held) == 0 {
		return nil, nil
	}
	rows, err = s.pool.Query(ctx, `select token, session, session_file,
			coalesce(abandoned_session ->> 'session', ''), coalesce(abandoned_session ->> 'sessionFile', '')
		from claims where project = $1 order by token`, project)
	if err != nil {
		return nil, fmt.Errorf("read the claims recording a session held for no claim: %w", err)
	}
	recorders := map[supervise.Hold][]claim.Token{}
	var token, session, file, abandoned, abandonedFile string
	if _, err := pgx.ForEachRow(rows, []any{&token, &session, &file, &abandoned, &abandonedFile}, func() error {
		seen := map[supervise.Hold]bool{}
		for _, h := range append(supervise.RecordedHolds(session, file), supervise.RecordedHolds(abandoned, abandonedFile)...) {
			if !seen[h] {
				seen[h] = true
				recorders[h] = append(recorders[h], claim.Token(token))
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read the claims recording a session held for no claim: %w", err)
	}
	for i := range held {
		held[i].Claims = recorders[held[i].Hold]
	}
	return held, nil
}
