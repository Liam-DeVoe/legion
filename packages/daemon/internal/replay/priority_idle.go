package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// priorityBook is each issue's Dispatch priority over a window, by key: the moments an event of
// the issue set its priority, in order, with the priority each set (0 for P0). The daemon stores no
// priority (record.Issue has none), so a rule that reads one (priority-first, p0-lane) reads it from
// the window's own Dispatch events, the copy of Dispatch the replay applies, as the moment of the
// pass sees it. An event that sets the priority to none sets it to none.
type priorityBook map[string][]prioritySet

// prioritySet is one event's priority: the event's moment and the priority it set, nil for none.
type prioritySet struct {
	at       time.Time
	priority *int
}

// priorityBookOf reads each issue's priority changes from w's Dispatch events (Window.Events): an
// event whose payload carries the field `priority` sets it, at the event's moment.
func priorityBookOf(w Window) (priorityBook, error) {
	book := make(priorityBook)
	for key, events := range w.Events {
		for _, raw := range events {
			var event struct {
				CreatedAt time.Time                  `json:"created_at"`
				Payload   map[string]json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return nil, fmt.Errorf("read the priority of an event of %s: %w", key, err)
			}
			field, sets := event.Payload["priority"]
			if !sets {
				continue
			}
			var priority *int
			if err := json.Unmarshal(field, &priority); err != nil {
				return nil, fmt.Errorf("read the priority %s of an event of %s: %w", field, key, err)
			}
			book[key] = append(book[key], prioritySet{at: event.CreatedAt, priority: priority})
		}
		sort.SliceStable(book[key], func(i, j int) bool { return book[key][i].at.Before(book[key][j].at) })
	}
	return book, nil
}

// at is the priority each of issues stands at, at the moment now, by key: the last event at or
// before now that set it. An issue with none set by then has no entry.
func (b priorityBook) at(now time.Time, issues []record.Issue) map[string]int {
	out := make(map[string]int)
	for _, issue := range issues {
		var current *int
		for _, set := range b[issue.Key] {
			if set.at.After(now) {
				break
			}
			current = set.priority
		}
		if current != nil {
			out[issue.Key] = *current
		}
	}
	return out
}

// waitsIdle says whether issue stands where, idle, it waits on a person rather than on work of its
// own, the stands the rule idle-return-1h reads idleness at: a root waiting on a person it has not
// heard from (phase.AwaitingPerson, unanswered), and a tree's root at admitted whose design gate has
// never opened (record.AtWork at admitted), whose architect writes the design or, idle, waits on a
// person's approval of it. A parent waiting on its open children is not read here: under every
// rule this comparison runs, that wait already gives its slot back (blockers.waitsOf), and a root
// whose gate has opened stands where its first phase may start, so idleness there would hold it out
// of the line after its children end.
func waitsIdle(issue record.Issue, gate record.TreeGate) bool {
	switch issue.Phase {
	case phase.AwaitingPerson:
		return !issue.Answered()
	case phase.Admitted:
		return record.AtWork(issue, gate)
	default:
		return false
	}
}

// readIdle is, under a rule that gives an idle waiting holder's slot back after idleness, each of
// issues that stands where it waits on a person (waitsIdle), that holds a slot
// or carries the mark of one it gave back (record.Issue.ReleasedForWaitAt), and none of whose
// claims has a turn open or has begun or ended one within idleness before now, by key, with the
// moment it took its slot (the slot's admitted_at), or its mark where it holds none. A holder that
// took its slot within idleness before now is not idle yet: the moment it took the slot counts as
// its last activity.
func readIdle(ctx context.Context, tx pgx.Tx, issues []record.Issue, facts record.LineFacts, idleness time.Duration,
	now time.Time,
) (map[string]time.Time, error) {
	candidates := make(map[string]record.Issue)
	for _, issue := range issues {
		if record.InWorkflow(issue) && waitsIdle(issue, facts.Gates[issue.Tree]) {
			candidates[issue.Key] = issue
		}
	}
	idle := make(map[string]time.Time)
	if len(candidates) == 0 {
		return idle, nil
	}
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	since := make(map[string]time.Time)
	marks := make(map[string]time.Time)
	rows, err := tx.Query(ctx, `select issue, admitted_at from slots where issue = any($1)`, keys)
	if err != nil {
		return nil, fmt.Errorf("read the slots of the idle candidates: %w", err)
	}
	for rows.Next() {
		var key string
		var at time.Time
		if err := rows.Scan(&key, &at); err != nil {
			rows.Close()
			return nil, err
		}
		since[key], marks[key] = at.UTC(), at.UTC()
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for key, issue := range candidates {
		if _, holds := marks[key]; !holds && issue.ReleasedForWaitAt != nil {
			marks[key] = issue.ReleasedForWaitAt.UTC()
		}
	}
	open := make(map[string]bool)
	rows, err = tx.Query(ctx, `select issue, state, last_turn_start, last_turn_end from claims where issue = any($1)`, keys)
	if err != nil {
		return nil, fmt.Errorf("read the claims of the idle candidates: %w", err)
	}
	for rows.Next() {
		var key, state string
		var start, end *time.Time
		if err := rows.Scan(&key, &state, &start, &end); err != nil {
			rows.Close()
			return nil, err
		}
		if state == "working" || (start != nil && (end == nil || start.After(*end))) {
			open[key] = true
		}
		for _, at := range []*time.Time{start, end} {
			if at != nil && at.After(since[key]) {
				since[key] = at.UTC()
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for key, mark := range marks {
		if open[key] || now.Sub(since[key]) < idleness {
			continue
		}
		idle[key] = mark
	}
	return idle, nil
}
