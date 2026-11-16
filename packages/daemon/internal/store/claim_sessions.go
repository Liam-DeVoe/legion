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
// records all of holds or none: a name another claim holds rolls back every row this call wrote,
// and so does a call whose new rows take token's claim past supervise.MaxHeldNamesPerClaim, which
// answers an error wrapping supervise.ErrHeldNamesCap. A call of names the claim holds already adds
// no row, so it is never refused for the bound. A row is never deleted, and nothing but an operator
// settling a held-for-none name moves one, so the answer about a name never changes once it names a
// claim.
//
// Each name is an insert and then a read, two statements of one transaction, so two claims
// registering one new name at once cannot both take it: the second's insert waits on the first's
// row, finds the conflict once the first commits, and its read, a statement of its own, sees the row
// the first committed. The names are taken in one order, kind then name, in every transaction, so
// two registrations sharing several names wait on each other one way and never deadlock. The bound
// is counted after the inserts, over the rows the claim holds then, so it is exact: the claim's own
// registrations are one machine's decisions, one after another.
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
		added := int64(0)
		for _, i := range order {
			h := holds[i]
			tag, err := tx.Exec(ctx, `insert into claim_sessions (project, kind, name, claim) values ($1, $2, $3, $4)
				on conflict (project, kind, name) do nothing`, project, string(h.Kind), h.Name, string(token))
			if err != nil {
				return err
			}
			added += tag.RowsAffected()
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
		if added == 0 {
			return nil
		}
		var held int
		if err := tx.QueryRow(ctx, `select count(*) from claim_sessions where project = $1 and claim = $2`,
			project, string(token)).Scan(&held); err != nil {
			return err
		}
		if held > supervise.MaxHeldNamesPerClaim {
			return fmt.Errorf("%d names held with this call's %d new ones, past %d: %w", held, added, supervise.MaxHeldNamesPerClaim, supervise.ErrHeldNamesCap)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errHeldElsewhere) {
		return nil, fmt.Errorf("hold the session of %s: %w", token, err)
	}
	return holders, nil
}

// SessionConflict is one name of a session (supervise.Hold) that claims of a project record without
// holding it: a name the table holds for no claim (Holder empty), or one it holds for another claim,
// Holder. Claims is every claim not holding it whose current session records it, which the daemon
// suspends at boot; Abandoned is every claim not holding it that records it only in the session it
// last abandoned, which nothing resumes or enrolls, so the boot suspends none of them.
type SessionConflict struct {
	Hold      supervise.Hold
	Holder    claim.Token
	Claims    []claim.Token
	Abandoned []claim.Token
}

// recordedNames is every name a claim of project records (supervise.RecordedHolds), each with the
// claims recording it, in token order: current is the names of each claim's current session, and
// abandoned the names only the session it last abandoned gives it.
func recordedNames(ctx context.Context, q pgx.Tx, project string) (current, abandoned map[supervise.Hold][]claim.Token, err error) {
	rows, err := q.Query(ctx, `select token, session, session_file,
			coalesce(abandoned_session ->> 'session', ''), coalesce(abandoned_session ->> 'sessionFile', '')
		from claims where project = $1 order by token collate "C"`, project)
	if err != nil {
		return nil, nil, err
	}
	current, abandoned = map[supervise.Hold][]claim.Token{}, map[supervise.Hold][]claim.Token{}
	var token, session, file, abandonedSession, abandonedFile string
	if _, err := pgx.ForEachRow(rows, []any{&token, &session, &file, &abandonedSession, &abandonedFile}, func() error {
		seen := map[supervise.Hold]bool{}
		for _, h := range supervise.RecordedHolds(session, file) {
			if !seen[h] {
				seen[h] = true
				current[h] = append(current[h], claim.Token(token))
			}
		}
		for _, h := range supervise.RecordedHolds(abandonedSession, abandonedFile) {
			if !seen[h] {
				seen[h] = true
				abandoned[h] = append(abandoned[h], claim.Token(token))
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return current, abandoned, nil
}

// sortedHolds is the names of every map in the one order every transaction that holds several
// takes them in (HoldSessions): kind, then name.
func sortedHolds(recorders ...map[supervise.Hold][]claim.Token) []supervise.Hold {
	var holds []supervise.Hold
	seen := map[supervise.Hold]bool{}
	for _, r := range recorders {
		for h := range r {
			if !seen[h] {
				seen[h] = true
				holds = append(holds, h)
			}
		}
	}
	slices.SortFunc(holds, func(a, b supervise.Hold) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return holds
}

// HoldRecordedSessions holds every name a claim of project records that the table holds for no
// one yet, as the claim_sessions migration's backfill held every name recorded before the table
// existed: for the one claim recording it, of its current session or the one it abandoned, or for
// none where several do. It answers how many names it held. The daemon runs it at every boot
// (internal/daemon's suspendSessionConflicts), because a claim records a name without holding it
// whenever a daemon from before the hold runs on this database, as one rolled back to an earlier
// release does: such a daemon applies only the migrations it has and never writes claim_sessions,
// and the migration's own backfill has run by then. On a database only daemons with the hold have
// run, every recorded name is held already and this holds nothing.
//
// It is one read of the claims and one insert of every name, whatever the project's size, both
// inside the boot's budget: a statement per name was one round trip per name, which at a project
// of 7,508 claims and 33,786 names took 4.2 s on a loopback and would take the whole 30 s budget at
// Cloud SQL's round trip (review R2-1 of #4220). The insert takes the names in kind and name order,
// the order HoldSessions takes them in, so it never deadlocks with a registration.
func (s *Store) HoldRecordedSessions(ctx context.Context, project string) (int, error) {
	held := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		current, abandoned, err := recordedNames(ctx, tx, project)
		if err != nil {
			return err
		}
		holds := sortedHolds(current, abandoned)
		kinds, names, holders := make([]string, len(holds)), make([]string, len(holds)), make([]string, len(holds))
		for i, h := range holds {
			kinds[i], names[i] = string(h.Kind), h.Name
			if recorders := append(slices.Clone(current[h]), abandoned[h]...); len(recorders) == 1 {
				holders[i] = string(recorders[0])
			}
		}
		tag, err := tx.Exec(ctx, `insert into claim_sessions (project, kind, name, claim)
			select $1, kind, name, claim from unnest($2::text[], $3::text[], $4::text[]) with ordinality as n(kind, name, claim, i)
			order by i
			on conflict (project, kind, name) do nothing`, project, kinds, names, holders)
		if err != nil {
			return err
		}
		held = int(tag.RowsAffected())
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
// HoldRecordedSessions holds it first. The daemon logs each at boot and suspends the claims whose
// current session records it, for an operator to settle by hand (the claim_sessions migration gives
// the steps).
//
// It is two reads whatever the project's size, and the second reads only the rows of names the
// claims record now, through the primary key, never the whole table: a claim may hold up to
// supervise.MaxHeldNamesPerClaim names, most of them of sessions it no longer records, so the
// table can hold far more rows than the claims name, and the boot's cost must follow what the
// claims record, about 4.5 names a claim, not what the table has gathered (review N2 of #4220).
func (s *Store) SessionConflicts(ctx context.Context, project string) ([]SessionConflict, error) {
	var conflicts []SessionConflict
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		current, abandoned, err := recordedNames(ctx, tx, project)
		if err != nil {
			return err
		}
		recorded := sortedHolds(current, abandoned)
		kinds, names := make([]string, len(recorded)), make([]string, len(recorded))
		for i, h := range recorded {
			kinds[i], names[i] = string(h.Kind), h.Name
		}
		rows, err := tx.Query(ctx, `select h.kind, h.name, h.claim
			from unnest($2::text[], $3::text[]) as r(kind, name)
			join claim_sessions h on h.project = $1 and h.kind = r.kind and h.name = r.name`, project, kinds, names)
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
		others := func(tokens []claim.Token, holder claim.Token) []claim.Token {
			var out []claim.Token
			for _, token := range tokens {
				if token != holder {
					out = append(out, token)
				}
			}
			return out
		}
		for _, h := range recorded {
			holder, held := holders[h]
			if !held {
				continue
			}
			c := SessionConflict{Hold: h, Holder: holder, Claims: others(current[h], holder), Abandoned: others(abandoned[h], holder)}
			if len(c.Claims) > 0 || len(c.Abandoned) > 0 {
				conflicts = append(conflicts, c)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the session names claims of %s record without holding them: %w", project, err)
	}
	return conflicts, nil
}
