package store

import (
	"context"
	"fmt"
	"time"
)

// WritingCheckRetention is how long a writing_check_events row is kept. The daily sweep
// (SweepWritingCheckEvents) deletes every row older than this.
const WritingCheckRetention = 90 * 24 * time.Hour

// WritingCheckEvent is one row of writing_check_events: what the writing check, or the reviewer,
// decided for one write. Every string the write did not carry is "", stored as null; ReaderMS is
// nil where the model reader was not called.
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
	// the record; "" where no person reads it. A Rewrite request (rewrite_requested) names the
	// person who asked for it, who reads the text. The counts give each row to its reader.
	Reader string
	// Project is the project of the text's issue or document, "" where the writer did not record
	// it; the counts give each row to its project.
	Project string
}

// InsertWritingCheckEvent records one decision of the writing check. It runs in a transaction of
// its own, never inside the write's own transaction, so the caller logs a failure and lets the
// write go on.
//
// The row is the check's record of a write, and no part of the write: the check records it
// before the write opens its transaction, and records it for a write that may never commit. So
// its transaction takes no commit step a keyed request's context carries
// (WithoutBeforeCommit). A mark taken here would answer a repeat after a crash KEY_COMMITTED for
// a write nobody made; without it the repeat runs the write, and its check records a row of its
// own.
func (s *Store) InsertWritingCheckEvent(ctx context.Context, event WritingCheckEvent) error {
	rules, fields := event.Rules, event.Fields
	if rules == nil {
		rules = []string{}
	}
	if fields == nil {
		fields = []string{}
	}
	tx, err := s.Pool.Begin(WithoutBeforeCommit(ctx))
	if err != nil {
		return fmt.Errorf("record writing check event: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		insert into writing_check_events (
			route, method, actor_kind, actor_id, act_session, token_id, outcome,
			rules, fields, candidates, reader_ms, detail, reader, project
		) values ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), $7, $8, $9, $10, $11, nullif($12, ''), nullif($13, ''), nullif($14, ''))
	`, event.Route, event.Method, event.ActorKind, event.ActorID, event.ActSession, event.TokenID, event.Outcome,
		rules, fields, event.Candidates, event.ReaderMS, event.Detail, event.Reader, event.Project); err != nil {
		return fmt.Errorf("record writing check event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
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

// WritingCheckCounts is R8's counts of the UTC days First to Last, both YYYY-MM-DD: Days, each day
// of writing_check_events counted by outcome and by route, every day of the range listed; and Rows,
// the counts of each day, project and person that has any.
type WritingCheckCounts struct {
	First string            `json:"first"`
	Last  string            `json:"last"`
	Days  []WritingCheckDay `json:"days"`
	Rows  []WritingCheckRow `json:"rows"`
}

// WritingCheckDay is one UTC day of writing_check_events, counted two ways: by outcome and by route
// ("<method> <pattern>").
type WritingCheckDay struct {
	Day      string         `json:"day"`
	Total    int            `json:"total"`
	Outcomes map[string]int `json:"outcomes"`
	Routes   map[string]int `json:"routes"`
}

// WritingCheckRow is R8's counts for one UTC day, one project ("" for a text on no project, or one
// whose writer recorded none) and one person ("" for a person the record does not name: a text a
// person typed in a session whose credential names nobody, or a reviewer's row with no reader).
// The texts are those people wrote, as the records job read them (word_questions): WordQuestions
// those whose verdict is about words, Texts every one, TextsUnread those still waiting for a
// verdict, and DefineUses the person's Define uses. AsksAnswered is the asks the person answered
// that day, and AsksRepliedBeforeAnswer those of them a person had commented on in the ask's thread
// before the answer, read from the event log. The rest are writing_check_events rows given to their
// reader: RewriteUses the Rewrites the person asked for (rewrite_requested), the reviewer's held,
// rewrote (rewrite_posted), passed and posted-unreviewed texts, and PostedOverFinding the texts that
// posted over a finding (findings, posted_as_written, and reviewed_after with findings).
type WritingCheckRow struct {
	Day                      string `json:"day"`
	Project                  string `json:"project"`
	Person                   string `json:"person"`
	WordQuestions            int    `json:"word_questions"`
	Texts                    int    `json:"texts"`
	TextsUnread              int    `json:"texts_unread"`
	AsksAnswered             int    `json:"asks_answered"`
	AsksRepliedBeforeAnswer  int    `json:"asks_replied_before_answer"`
	RewriteUses              int    `json:"rewrite_uses"`
	DefineUses               int    `json:"define_uses"`
	ReviewerHeld             int    `json:"reviewer_held"`
	ReviewerRewrote          int    `json:"reviewer_rewrote"`
	ReviewerPassed           int    `json:"reviewer_passed"`
	ReviewerPostedUnreviewed int    `json:"reviewer_posted_unreviewed"`
	PostedOverFinding        int    `json:"posted_over_finding"`
}

// countWritingCheckRows is WritingCheckRow's counts, each from the rows of its own source over the
// days $1 to $2 (dates) and the times $3 to $4 (their first midnight and the midnight after the
// last), summed by day, project and person. An ask answered and an ask commented on are a person's
// own act: an actor of kind user with no session acting for them, as a record's person is.
const countWritingCheckRows = `
select day::text, project, person,
	sum(word_questions)::int, sum(texts)::int, sum(texts_unread)::int, sum(asks_answered)::int,
	sum(asks_replied)::int, sum(rewrite_uses)::int, sum(define_uses)::int, sum(held)::int,
	sum(rewrote)::int, sum(passed)::int, sum(unreviewed)::int, sum(over_finding)::int
from (
	select day, coalesce(project, '') as project, coalesce(person, '') as person,
		count(*) filter (where source = 'event' and about_words) as word_questions,
		count(*) filter (where source = 'event') as texts,
		count(*) filter (where source = 'event' and state <> 'done') as texts_unread,
		0 as asks_answered, 0 as asks_replied, 0 as rewrite_uses,
		count(*) filter (where source = 'define') as define_uses,
		0 as held, 0 as rewrote, 0 as passed, 0 as unreviewed, 0 as over_finding
	from word_questions
	where day >= $1::date and day <= $2::date
	group by 1, 2, 3
	union all
	select (e.created_at at time zone 'UTC')::date,
		coalesce(e.project_key, i.project_key, ar.project_key, ai.project_key, ''),
		lower(btrim(e.actor->>'id')),
		0, 0, 0,
		count(distinct e.payload->>'id'),
		count(distinct e.payload->>'id') filter (where exists (
			select 1 from comments c
			where c.ask_id = (e.payload->>'id')::uuid and c.created_at < e.created_at
				and c.author->>'kind' = 'user' and coalesce(c.author->'act'->>'session', '') = '')),
		0, 0, 0, 0, 0, 0, 0
	from events e
	left join issues i on i.key = e.issue_key
	left join artifacts ar on ar.id = e.artifact_id
	left join issues ai on ai.key = ar.issue_key
	where e.type = 'ask.answered' and e.actor->>'kind' = 'user'
		and coalesce(e.actor->'act'->>'session', '') = ''
		and e.created_at >= $3 and e.created_at < $4
	group by 1, 2, 3
	union all
	select (created_at at time zone 'UTC')::date, coalesce(project, ''), coalesce(reader, ''),
		0, 0, 0, 0, 0,
		count(*) filter (where outcome = 'rewrite_requested'),
		0,
		count(*) filter (where outcome = 'held'),
		count(*) filter (where outcome = 'rewrite_posted'),
		count(*) filter (where outcome = 'passed'),
		count(*) filter (where outcome = 'posted_unreviewed'),
		count(*) filter (where outcome in ('findings', 'posted_as_written') or outcome = 'reviewed_after')
	from writing_check_events
	where created_at >= $3 and created_at < $4
		and (outcome in ('rewrite_requested', 'held', 'rewrite_posted', 'passed', 'posted_unreviewed',
			'findings', 'posted_as_written') or (outcome = 'reviewed_after' and cardinality(rules) > 0))
	group by 1, 2, 3
) counted
group by 1, 2, 3
order by 1, 2, 3`

// CountWritingCheck counts the UTC calendar days first to last, both included: from first's
// midnight to the midnight after last.
func (s *Store) CountWritingCheck(ctx context.Context, first, last time.Time) (WritingCheckCounts, error) {
	from := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, time.UTC)
	through := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
	until := through.AddDate(0, 0, 1)
	counts := WritingCheckCounts{First: from.Format(time.DateOnly), Last: through.Format(time.DateOnly), Rows: []WritingCheckRow{}}
	byDay := map[string]*WritingCheckDay{}
	for day := from; day.Before(until); day = day.AddDate(0, 0, 1) {
		counts.Days = append(counts.Days, WritingCheckDay{Day: day.Format(time.DateOnly), Outcomes: map[string]int{}, Routes: map[string]int{}})
	}
	for i := range counts.Days {
		byDay[counts.Days[i].Day] = &counts.Days[i]
	}
	rows, err := s.Pool.Query(ctx, `
		select (created_at at time zone 'UTC')::date::text, outcome, method || ' ' || route, count(*)::int
		from writing_check_events
		where created_at >= $1 and created_at < $2
		group by 1, 2, 3
	`, from, until)
	if err != nil {
		return WritingCheckCounts{}, fmt.Errorf("count writing check events: %w", err)
	}
	for rows.Next() {
		var day, outcome, route string
		var n int
		if err := rows.Scan(&day, &outcome, &route, &n); err != nil {
			rows.Close()
			return WritingCheckCounts{}, fmt.Errorf("count writing check events: %w", err)
		}
		if counted := byDay[day]; counted != nil {
			counted.Total += n
			counted.Outcomes[outcome] += n
			counted.Routes[route] += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WritingCheckCounts{}, fmt.Errorf("count writing check events: %w", err)
	}
	rows, err = s.Pool.Query(ctx, countWritingCheckRows, from, through, from, until)
	if err != nil {
		return WritingCheckCounts{}, fmt.Errorf("count the records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row WritingCheckRow
		if err := rows.Scan(&row.Day, &row.Project, &row.Person, &row.WordQuestions, &row.Texts, &row.TextsUnread,
			&row.AsksAnswered, &row.AsksRepliedBeforeAnswer, &row.RewriteUses, &row.DefineUses, &row.ReviewerHeld,
			&row.ReviewerRewrote, &row.ReviewerPassed, &row.ReviewerPostedUnreviewed, &row.PostedOverFinding); err != nil {
			return WritingCheckCounts{}, fmt.Errorf("count the records: %w", err)
		}
		counts.Rows = append(counts.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return WritingCheckCounts{}, fmt.Errorf("count the records: %w", err)
	}
	return counts, nil
}
