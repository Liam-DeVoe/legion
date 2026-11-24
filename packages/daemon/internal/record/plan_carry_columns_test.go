package record

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A carried plan holds every column of plan_steps that a step's start reads: CarryPlan copies each
// step to the new generation with every column but the generation, so a column added to the table
// and left out of the copy fails here, whatever its name. The case that found it is
// leave_session: the default steps' agent that took a key's name is written with the key's session
// to leave, and a plan carried to the next generation before that agent's first start must carry
// it, or the start resumes the key's session. The columns are read from the table itself, not from
// a list the test keeps, so the census cannot go stale. The control: the same comparison names the
// generation, which the copy does change, and finds every row different.
func TestACarriedPlanHoldsEveryColumnOfItsSteps(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	inTx(t, st, func(tx pgx.Tx) {
		for _, statement := range []string{
			`insert into issue_plans (issue, generation, version, source, artifact_id, given_by, given_at)
				values ('LEGION-208', 1, 0, 'default', '', '', now())`,
			`insert into plan_tasks (issue, generation, task, plan_version, block_text, steps_by_default, state)
				values ('LEGION-208', 1, 'default', 0, 'The project''s default steps.', true, 'waiting')`,
			`insert into plan_steps (issue, generation, task, position, role, type, agent_key, agent, leave_session)
				values ('LEGION-208', 1, 'default', 1, 'tester', 'testing', '', 'tester', 'ses-key-k'),
				('LEGION-208', 1, 'default', 2, 'reviewer', 'review', '', 'reviewer', '')`,
		} {
			_, err := tx.Exec(ctx, statement)
			must(t, err)
		}
		must(t, NewStore().CarryPlan(ctx, tx, "LEGION-208", 1, 2, false))

		rows, err := tx.Query(ctx, `select column_name from information_schema.columns
			where table_schema = 'public' and table_name = 'plan_steps' and column_name <> 'generation' order by column_name`)
		must(t, err)
		columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
		must(t, err)
		if len(columns) < 8 {
			t.Fatalf("plan_steps has the columns %q, want at least the eight a step is written with: the census read nothing", columns)
		}
		differ := func(list string) int {
			t.Helper()
			var n int
			must(t, tx.QueryRow(ctx, fmt.Sprintf(`select count(*) from (
				select %[1]s from plan_steps where issue = 'LEGION-208' and generation = 1
				except select %[1]s from plan_steps where issue = 'LEGION-208' and generation = 2) d`, list)).Scan(&n))
			return n
		}
		if n := differ(strings.Join(columns, ", ")); n != 0 {
			t.Fatalf("%d steps of generation 1 have no copy at generation 2 over the columns %q: CarryPlan leaves a column of plan_steps behind", n, columns)
		}
		if n := differ(strings.Join(append(columns, "generation"), ", ")); n != 2 {
			t.Fatalf("with the generation named, %d steps differ, want both: the comparison cannot fail", n)
		}
	})
}
