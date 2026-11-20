package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The broker keeps the last answer Dispatch gave it for each person, whether Dispatch holds them,
// in 0040's person_standing, so that while Dispatch cannot be asked the hold gate
// (internal/broker/hold) answers from it: a person last answered held stays held, and a restart or
// a second broker process answers as this one does. Every process writes the answers its own asks
// fetched, so the row keeps the latest by Dispatch's own clock, the answer's as_of, and never by
// the order the writes arrive in.

// PersonStanding is the last answer kept for person: whether Dispatch held them and the time its
// answer rests on (its as_of). found is false when no answer is kept, which is no error.
func (s *Store) PersonStanding(ctx context.Context, person string) (held bool, answeredAt time.Time, found bool, err error) {
	err = s.Pool.QueryRow(ctx, `select held, answered_at from person_standing where person = $1`, person).Scan(&held, &answeredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, time.Time{}, false, nil
	}
	if err != nil {
		return false, time.Time{}, false, err
	}
	return held, answeredAt, true, nil
}

// KeepPersonStanding writes Dispatch's answer for person, held as of asOf, unless the row already
// holds an answer Dispatch gave later. person is a login as record.CanonicalLogin spells it, which
// the table's check refuses any other spelling of.
func (s *Store) KeepPersonStanding(ctx context.Context, person string, held bool, asOf time.Time) error {
	_, err := s.Pool.Exec(ctx, `insert into person_standing values ($1, $2, $3)
		on conflict (person) do update set held = excluded.held, answered_at = excluded.answered_at
		where person_standing.answered_at < excluded.answered_at`, person, held, asOf)
	return err
}
