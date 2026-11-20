package eval

import (
	"context"
	"fmt"
)

// CheckPersonWrites is the rule c13 holds every try's messages and comments to: each message
// and comment the person wrote during the try (each message.created and comment.created by the
// person) belongs to a task that asks for it (the task allows it, or a status task messages a
// session whose reply it relies on) and to a row of an approved plan of the person's run: a row
// the person or Auto-review approved, whose landed request (going_landings) answered the item's
// id. It answers one line for each that breaks the rule, saying why, and none for a try that
// keeps it. q reaches the try's store, which before and after were read from.
func CheckPersonWrites(ctx context.Context, q Querier, seed *Seed, task Task, before, after *Store, person string) ([]string, error) {
	var broken []string
	for _, c := range Diff(seed, before, after, person) {
		if c.ID == "" || (c.Field != FieldMessage && c.Field != FieldComment && c.Field != FieldSuggestion) {
			continue
		}
		if !allowed(task, c, nil) {
			broken = append(broken, fmt.Sprintf("%s: task %s does not ask for it", c.What, task.ID))
			continue
		}
		var approved bool
		if err := q.QueryRow(ctx, `
			select exists (
			  select 1
			  from going_landings l
			  join going_runs r on r.id = l.run_id and lower(r.author_login) = lower($2)
			  join going_plan_rows p on p.run_id = l.run_id
			    and (p.result->>'step')::int = l.step and (p.result->>'call')::int = l.call
			  where l.phase = 'do' and p.approval is not null and strpos(l.answer::text, $1) > 0
			)
		`, c.ID, person).Scan(&approved); err != nil {
			return nil, fmt.Errorf("eval: read the plan row that made %s: %w", c.ID, err)
		}
		if !approved {
			broken = append(broken, fmt.Sprintf("%s: no approved row of a plan of the task made it", c.What))
		}
	}
	return broken, nil
}
