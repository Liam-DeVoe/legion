package workflowstore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// IssueSteps answers every run of every step of one issue, each with its attempt, in the order
// the runs happened: by generation, then by start, a run whose start never arrived first, then by
// id. The attempt is the place of a run among the issue's runs of the same task, step and kind in
// that order, from 1, so the retro and the merge, both at step 0, each count from 1. An issue
// Legion never ran answers none.
//
// It reads the `part_of` tuples into the issue through workflow_tuples_inbound, whose predicate
// the literal to_kind implies, and joins each to its node row, so it costs the issue's own runs
// and nothing else.
func IssueSteps(ctx context.Context, q Queryer, issue string) ([]StepRead, error) {
	rows, err := q.Query(ctx, `
		select n.id, n.attributes
		from workflow_tuples t
		join workflow_nodes n on n.kind = t.from_kind and n.id = t.from_id
		where t.to_kind = '`+KindIssue+`' and t.to_id = $1 and t.kind = '`+RelationPartOf+`'
			and t.from_kind = '`+KindStep+`' and t.asserted
	`, issue)
	if err != nil {
		return nil, fmt.Errorf("read the steps of %s: %w", issue, err)
	}
	defer rows.Close()
	var steps []StepRead
	for rows.Next() {
		var id string
		var attributes []byte
		if err := rows.Scan(&id, &attributes); err != nil {
			return nil, fmt.Errorf("read the steps of %s: %w", issue, err)
		}
		step, err := stepFrom(id, attributes)
		if err != nil {
			return nil, err
		}
		steps = append(steps, StepRead{Step: step})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the steps of %s: %w", issue, err)
	}

	slices.SortFunc(steps, func(left, right StepRead) int { return compareRuns(left.Step, right.Step) })
	type place struct {
		task, kind string
		step       int
	}
	attempts := map[place]int{}
	for index := range steps {
		key := place{steps[index].Task, steps[index].Kind, steps[index].Place}
		attempts[key]++
		steps[index].Attempt = attempts[key]
	}
	return steps, nil
}

// compareRuns is the order the runs of an issue happened in: by generation, then by start, a run
// whose start never arrived first, then by id.
func compareRuns(left, right Step) int {
	if order := cmp.Compare(left.Generation, right.Generation); order != 0 {
		return order
	}
	switch {
	case left.StartedAt == nil && right.StartedAt != nil:
		return -1
	case left.StartedAt != nil && right.StartedAt == nil:
		return 1
	case left.StartedAt != nil:
		if order := left.StartedAt.Compare(*right.StartedAt); order != 0 {
			return order
		}
	}
	return cmp.Compare(left.ID, right.ID)
}

// ProjectPlans answers the finished issues of project that filter keeps, an issue whose status is
// done and whose closed_at is set, newest close first, its close within window ([Since, Until),
// each nil for no bound, each rounded up to the microsecond). Each issue carries its plan (the node
// of its newest generation, nil where it has none), the rating of each retro in the order the
// retros ran, the earliest start and the latest end its runs hold (nil where none holds one), and
// its count of runs of kind step that failed. A finished issue Legion never ran is listed with none
// of these. Beside the issues it counts every one the window and PlannedOrRated keep, and every
// finished issue of the window.
//
// It is one statement for the whole project, whatever the number of issues, and the statement
// keeps and bounds the issues itself, so it answers the rows of the issues it lists and no others:
// the project's finished issues, those of them with a plan node or a run whose rating is not 0 when
// PlannedOrRated asks (stepFrom's reading of a rating), the newest Limit of those, each left-joined
// through the `part_of` tuples into it (workflow_tuples_inbound, whose predicate the literal
// to_kind implies) to the node rows of its runs and plans. Its one row of counts stands alone when
// it lists no issue.
func ProjectPlans(ctx context.Context, q Queryer, project string, window Window, filter PlansFilter) (PlansRead, error) {
	// A null limit, which a Limit of 0 sends, keeps every issue.
	var limit *int
	if filter.Limit > 0 {
		limit = &filter.Limit
	}
	partOf := `t.to_kind = '` + KindIssue + `' and t.kind = '` + RelationPartOf + `' and t.asserted`
	rows, err := q.Query(ctx, `
		with finished as (
			select i.key, i.title, i.closed_at
			from issues i
			where i.project_key = $1 and i.status = 'done' and i.closed_at is not null
				and ($2::timestamptz is null or i.closed_at >= $2)
				and ($3::timestamptz is null or i.closed_at < $3)
		), kept as (
			select f.key, f.title, f.closed_at
			from finished f
			where not $4::boolean or exists (
				select 1
				from workflow_tuples t join workflow_nodes n on n.kind = t.from_kind and n.id = t.from_id
				where `+partOf+` and t.to_id = f.key
					and (t.from_kind = '`+KindPlan+`'
						or (t.from_kind = '`+KindStep+`' and (n.attributes->>'`+AttributeRating+`')::int <> 0))
			)
		), listed as (
			select k.key, k.title, k.closed_at
			from kept k
			order by k.closed_at desc, k.key
			limit $5::int
		)
		select (select count(*) from kept), (select count(*) from finished),
			l.key, l.title, l.closed_at, n.kind, n.id, n.attributes, n.made_at
		from (values (1)) as answer (one)
		left join (listed l
			left join (workflow_tuples t join workflow_nodes n on n.kind = t.from_kind and n.id = t.from_id)
				on `+partOf+` and t.to_id = l.key and t.from_kind in ('`+KindStep+`', '`+KindPlan+`'))
			on true
		order by l.closed_at desc, l.key
	`, project, store.MicrosecondCeiling(window.Since), store.MicrosecondCeiling(window.Until), filter.PlannedOrRated, limit)
	if err != nil {
		return PlansRead{}, fmt.Errorf("read the plans of %s: %w", project, err)
	}
	defer rows.Close()

	// gathering is one finished issue as its rows build it: its answer, its runs and its newest
	// plan with the moment that plan's node was made, the two folded into the answer once every row
	// is read.
	type gathering struct {
		issue IssuePlan
		runs  []Step
		plan  *madePlan
	}
	var issues []gathering
	var read PlansRead
	for rows.Next() {
		var key, title, kind, id *string
		var closed, made *time.Time
		var attributes []byte
		if err := rows.Scan(&read.Total, &read.FinishedTotal, &key, &title, &closed, &kind, &id, &attributes, &made); err != nil {
			return PlansRead{}, fmt.Errorf("read the plans of %s: %w", project, err)
		}
		if key == nil {
			continue
		}
		if len(issues) == 0 || issues[len(issues)-1].issue.Issue != *key {
			issues = append(issues, gathering{issue: IssuePlan{Issue: *key, Title: *title, ClosedAt: closed.UTC()}})
		}
		last := &issues[len(issues)-1]
		switch {
		case kind == nil:
		case *kind == KindStep:
			step, err := stepFrom(*id, attributes)
			if err != nil {
				return PlansRead{}, err
			}
			last.runs = append(last.runs, step)
		case *kind == KindPlan:
			plan, err := planFrom(*id, attributes)
			if err != nil {
				return PlansRead{}, err
			}
			if candidate := (madePlan{plan: plan, made: *made}); candidate.newerThan(last.plan) {
				last.plan = &candidate
			}
		}
	}
	if err := rows.Err(); err != nil {
		return PlansRead{}, fmt.Errorf("read the plans of %s: %w", project, err)
	}

	read.Issues = make([]IssuePlan, 0, len(issues))
	for _, gathered := range issues {
		issue := gathered.issue
		if gathered.plan != nil {
			issue.Plan = &gathered.plan.plan
		}
		slices.SortFunc(gathered.runs, compareRuns)
		for _, step := range gathered.runs {
			if step.StartedAt != nil && (issue.StartedAt == nil || step.StartedAt.Before(*issue.StartedAt)) {
				issue.StartedAt = step.StartedAt
			}
			if step.EndedAt != nil && (issue.EndedAt == nil || step.EndedAt.After(*issue.EndedAt)) {
				issue.EndedAt = step.EndedAt
			}
			if step.Kind == StepKindStep && step.Result == ResultFailed {
				issue.FailedSteps++
			}
			if step.Rating != nil {
				issue.Ratings = append(issue.Ratings, Rating{Rating: step.Rating.Rating, Reason: step.Rating.Reason, StepID: step.ID, EndedAt: step.EndedAt})
			}
		}
		read.Issues = append(read.Issues, issue)
	}
	return read, nil
}

// madePlan is a plan with the moment its node was made, which picks between two plans of one
// generation.
type madePlan struct {
	plan Plan
	made time.Time
}

// newerThan answers whether candidate is newer than held (nil for none): of a higher generation,
// or of the same generation and made later, or made at the same moment with the greater id, so the
// read picks one plan whatever order the rows came in.
func (candidate madePlan) newerThan(held *madePlan) bool {
	if held == nil {
		return true
	}
	if order := cmp.Compare(candidate.plan.Generation, held.plan.Generation); order != 0 {
		return order > 0
	}
	if order := candidate.made.Compare(held.made); order != 0 {
		return order > 0
	}
	return candidate.plan.ID > held.plan.ID
}
