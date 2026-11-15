package store

import (
	"context"
	"fmt"
	"time"
)

// WritingCheckRetention is how long a writing_check_events row is kept. The daily sweep
// (SweepWritingCheckEvents) deletes every row older than this.
const WritingCheckRetention = 90 * 24 * time.Hour

// WritingCheckEvent is one row of writing_check_events: what the writing check decided for one
// write. Every string the write did not carry is "", stored as null; ReaderMS is nil where the
// model reader was not called.
type WritingCheckEvent struct {
	Route      string
	Method     string
	ActorKind  string
	ActorID    string
	ActSession string
	TokenID    string
	Outcome    string
	Rules      []string
	Fields     []string
	Candidates int
	ReaderMS   *int
	Detail     string
	// Reader is the login of the person who reads the write's text, as the server decided it from
	// the record; "" where no person reads it.
	Reader string
}

// InsertWritingCheckEvent records one decision of the writing check. It runs on the pool and
// never inside the write's own transaction, so the caller logs a failure and lets the write go
// on.
func (s *Store) InsertWritingCheckEvent(ctx context.Context, event WritingCheckEvent) error {
	rules, fields := event.Rules, event.Fields
	if rules == nil {
		rules = []string{}
	}
	if fields == nil {
		fields = []string{}
	}
	if _, err := s.Pool.Exec(ctx, `
		insert into writing_check_events (
			route, method, actor_kind, actor_id, act_session, token_id, outcome,
			rules, fields, candidates, reader_ms, detail, reader
		) values ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), $7, $8, $9, $10, $11, nullif($12, ''), nullif($13, ''))
	`, event.Route, event.Method, event.ActorKind, event.ActorID, event.ActSession, event.TokenID, event.Outcome,
		rules, fields, event.Candidates, event.ReaderMS, event.Detail, event.Reader); err != nil {
		return fmt.Errorf("record writing check event: %w", err)
	}
	return nil
}

// SweepWritingCheckEvents deletes every writing_check_events row older than
// WritingCheckRetention, on the created_at index, and answers how many it deleted.
func (s *Store) SweepWritingCheckEvents(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from writing_check_events where created_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int64(WritingCheckRetention/time.Second)))
	if err != nil {
		return 0, fmt.Errorf("sweep writing check events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// WritingCheckCounts is one UTC day of writing_check_events, counted two ways: by outcome and by
// route ("<method> <pattern>").
type WritingCheckCounts struct {
	Day      string         `json:"day"`
	Total    int            `json:"total"`
	Outcomes map[string]int `json:"outcomes"`
	Routes   map[string]int `json:"routes"`
}

// CountWritingCheckEvents counts the rows created on day, a UTC calendar day: from its midnight
// to the next.
func (s *Store) CountWritingCheckEvents(ctx context.Context, day time.Time) (WritingCheckCounts, error) {
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	counts := WritingCheckCounts{
		Day:      from.Format(time.DateOnly),
		Outcomes: map[string]int{},
		Routes:   map[string]int{},
	}
	rows, err := s.Pool.Query(ctx, `
		select outcome, method || ' ' || route, count(*)::int
		from writing_check_events
		where created_at >= $1 and created_at < $2
		group by 1, 2
	`, from, from.AddDate(0, 0, 1))
	if err != nil {
		return WritingCheckCounts{}, fmt.Errorf("count writing check events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var outcome, route string
		var n int
		if err := rows.Scan(&outcome, &route, &n); err != nil {
			return WritingCheckCounts{}, fmt.Errorf("count writing check events: %w", err)
		}
		counts.Total += n
		counts.Outcomes[outcome] += n
		counts.Routes[route] += n
	}
	if err := rows.Err(); err != nil {
		return WritingCheckCounts{}, fmt.Errorf("count writing check events: %w", err)
	}
	return counts, nil
}
