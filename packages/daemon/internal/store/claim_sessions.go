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

// SessionConflict is one name of a session (supervise.Hold) that claims of a project record without
// holding it: a name the table holds for no claim, with every claim whose row records it; or a name
// it holds for another claim, Holder, with every other claim whose row records it.
type SessionConflict struct {
	Hold   supervise.Hold
	Holder claim.Token
	Claims []claim.Token
}

// recordedNames is every name a claim of project records, of its session or of the session it last
// abandoned (supervise.RecordedHolds), each with the claims whose rows record it, in token order.
func recordedNames(ctx context.Context, q pgx.Tx, project string) (map[supervise.Hold][]claim.Token, error) {
	rows, err := q.Query(ctx, `select token, session, session_file,
			coalesce(abandoned_session ->> 'session', ''), coalesce(abandoned_session ->> 'sessionFile', '')
		from claims where project = $1 order by token collate "C"`, project)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return recorders, nil
}

// sortedHolds is the names of recorders in the one order every transaction that holds several
// takes them in (HoldSessions): kind, then name.
func sortedHolds(recorders map[supervise.Hold][]claim.Token) []supervise.Hold {
	holds := make([]supervise.Hold, 0, len(recorders))
	for h := range recorders {
		holds = append(holds, h)
	}
	slices.SortFunc(holds, func(a, b supervise.Hold) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return holds
}

// HoldRecordedSessions holds every name a claim of project records that the table holds for no
// one yet, as the claim_sessions migration's backfill held every name recorded before the table
// existed: for the one claim recording it, or for none where several do. It answers how many names
// it held. The daemon runs it at every boot (internal/daemon's suspendSessionConflicts), because a
// claim records a name without holding it whenever a daemon from before the hold runs on this
// database, as one rolled back to an earlier release does: such a daemon applies only the
// migrations it has and never writes claim_sessions, and the migration's own backfill has run by
// then. On a database only daemons with the hold have run, every recorded name is held already and
// this holds nothing.
func (s *Store) HoldRecordedSessions(ctx context.Context, project string) (int, error) {
	held := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		recorders, err := recordedNames(ctx, tx, project)
		if err != nil {
			return err
		}
		for _, h := range sortedHolds(recorders) {
			holder := ""
			if tokens := recorders[h]; len(tokens) == 1 {
				holder = string(tokens[0])
			}
			tag, err := tx.Exec(ctx, `insert into claim_sessions (project, kind, name, claim) values ($1, $2, $3, $4)
				on conflict (project, kind, name) do nothing`, project, string(h.Kind), h.Name, holder)
			if err != nil {
				return err
			}
			held += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("hold the session names claims of %s record: %w", project, err)
	}
	return held, nil
}

// SessionConflicts is every name a claim of project records without holding it (SessionConflict),
// in kind and then name order: one held for no claim, which the claim_sessions migration or
// HoldRecordedSessions found recorded by several claims, and one held for another claim, which a
// daemon from before the hold let a claim record. A name no row holds is not one, since
// HoldRecordedSessions holds it first. The daemon logs each at boot and suspends its claims, for an
// operator to settle by hand (the claim_sessions migration gives the statement).
func (s *Store) SessionConflicts(ctx context.Context, project string) ([]SessionConflict, error) {
	var conflicts []SessionConflict
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		recorders, err := recordedNames(ctx, tx, project)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select kind, name, claim from claim_sessions where project = $1`, project)
		if err != nil {
			return err
		}
		holders := map[supervise.Hold]claim.Token{}
		var kind, name, holder string
		if _, err := pgx.ForEachRow(rows, []any{&kind, &name, &holder}, func() error {
			holders[supervise.Hold{Kind: supervise.HoldKind(kind), Name: name}] = claim.Token(holder)
			return nil
		}); err != nil {
			return err
		}
		for _, h := range sortedHolds(recorders) {
			holder, held := holders[h]
			if !held {
				continue
			}
			var others []claim.Token
			for _, token := range recorders[h] {
				if token != holder {
					others = append(others, token)
				}
			}
			if len(others) > 0 {
				conflicts = append(conflicts, SessionConflict{Hold: h, Holder: holder, Claims: others})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the session names claims of %s record without holding them: %w", project, err)
	}
	return conflicts, nil
}
