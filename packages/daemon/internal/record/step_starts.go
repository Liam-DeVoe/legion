package record

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// StepPlace is one place of a task where a step runs: the task, the kind of step (step, retro or
// merge) and its place in the task, from 1 for a step and 0 for a retro or a merge, which no plan
// names (workflow's stepOf).
type StepPlace struct {
	Task  string
	Kind  string
	Place int
}

// StepStart is the count of starts of one place of an issue, and the agent and the moment its
// latest start wrote: what the end of the step running there computes the record's id from and
// writes again (workflow's endStep), so the start and the end agree whichever reaches Dispatch
// first. The zero StepStart is a place no start of this release has raised: a step that was
// running when the release deployed.
type StepStart struct {
	Starts    int64
	Agent     string
	StartedAt time.Time
}

// RaiseStepStarts counts one more start of place on issue, keeps the agent and the moment that
// start writes, and answers the count with that start in it. It is called in the transaction that
// writes the start's record.
func (s *Postgres) RaiseStepStarts(ctx context.Context, tx pgx.Tx, issue string, place StepPlace, agent string, at time.Time) (int64, error) {
	var starts int64
	if err := tx.QueryRow(ctx, `insert into step_starts (issue, task, kind, place, starts, agent, started_at) values ($1, $2, $3, $4, 1, $5, $6)
		on conflict (issue, task, kind, place) do update set starts = step_starts.starts + 1, agent = excluded.agent, started_at = excluded.started_at
		returning starts`, issue, place.Task, place.Kind, place.Place, agent, at).Scan(&starts); err != nil {
		return 0, fmt.Errorf("count a start of %s %s/%s/%d: %w", issue, place.Task, place.Kind, place.Place, err)
	}
	return starts, nil
}

// StepStarts is the count of starts of place on issue, with the agent and the moment of the
// latest, unchanged: the zero StepStart where no start was counted.
func (s *Postgres) StepStarts(ctx context.Context, tx pgx.Tx, issue string, place StepPlace) (StepStart, error) {
	var start StepStart
	err := tx.QueryRow(ctx, `select starts, agent, started_at from step_starts where issue = $1 and task = $2 and kind = $3 and place = $4`,
		issue, place.Task, place.Kind, place.Place).Scan(&start.Starts, &start.Agent, &start.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return StepStart{}, nil
	}
	if err != nil {
		return StepStart{}, fmt.Errorf("read the starts of %s %s/%s/%d: %w", issue, place.Task, place.Kind, place.Place, err)
	}
	start.StartedAt = start.StartedAt.UTC()
	return start, nil
}
