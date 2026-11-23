package migrations

// RunningFixSQL is the number of the fix of an issue's task that runs at the issue's generation, 0
// for none, as one expression over a row of issues: a row of task_fixes in state running
// (20261123134547_fix_beside_steps). A fix that still reads its failed job is none yet, since its
// entry is not raised. Package record's issue read selects it as record.Issue.RunningFix, and a
// read of package store over issues reads it by the same text. It lives beside the schema it reads
// because record and store cannot import each other and both import this package. A generation
// holds one task (plan.Supported) and a task one fix in state running (task_fixes_one_open), so the
// subquery answers one row at most.
const RunningFixSQL = `coalesce((select f.fix from task_fixes f where f.issue = issues.key and f.generation = issues.generation and f.state = 'running'), 0)`
