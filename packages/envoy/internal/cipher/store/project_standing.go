package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The broker keeps the last answer Dispatch's project route gave it for each person and project in
// 0047's project_standing, so that while Dispatch cannot be asked the request machine's person rule
// answers from it (projectaccess.OrKept): a project Dispatch last answered public or visible to the
// person shows them the row, and every other answer, or none, refuses as before. Every process
// writes the answers its own asks fetched, and the row keeps the one asked latest by the clock of
// the process that asked, since Dispatch's project answer names no time of its own.

// KeepProjectStanding writes Dispatch's answer for person and project, asked at askedAt, unless the
// row already holds an answer asked later: visibility as Dispatch answered it, "public" or
// "private", and whether person may see the project; or, for visibility "", that Dispatch holds no
// such project, which is written for no person (”), since it is the same answer for everyone.
// person is a login as record.CanonicalLogin spells it, or ” for a question naming no person.
func (s *Store) KeepProjectStanding(ctx context.Context, person, project, visibility string, visible bool, askedAt time.Time) error {
	if visibility == "" {
		person, visible = "", false
	}
	_, err := s.Pool.Exec(ctx, `insert into project_standing (person, project, visibility, visible, asked_at) values ($1, $2, nullif($3, ''), $4, $5)
		on conflict (person, project) do update set visibility = excluded.visibility, visible = excluded.visible, asked_at = excluded.asked_at
		where project_standing.asked_at < excluded.asked_at`, person, project, visibility, visible, askedAt)
	return err
}

// KeptProjectStanding is the last answer kept for person and project: the later, by askedAt, of
// the answer kept for that person and an answer that Dispatch holds no such project, which is
// kept for no person. visibility is "public" or "private", or "" for a project Dispatch does not
// hold. found is false when no answer is kept, which is no error.
func (s *Store) KeptProjectStanding(ctx context.Context, person, project string) (visibility string, visible bool, askedAt time.Time, found bool, err error) {
	err = s.Pool.QueryRow(ctx, `select coalesce(visibility, ''), visible, asked_at from project_standing
		where project = $2 and (person = $1 or (person = '' and visibility is null))
		order by asked_at desc limit 1`, person, project).Scan(&visibility, &visible, &askedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, time.Time{}, false, nil
	}
	if err != nil {
		return "", false, time.Time{}, false, err
	}
	return visibility, visible, askedAt, true, nil
}
