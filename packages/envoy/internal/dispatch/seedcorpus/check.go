package seedcorpus

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
)

// corpusHeld is the key of the seeded issue where the project holds it already, and "" where not.
func corpusHeld(ctx context.Context, q graphstore.Queryer, project string) (string, error) {
	var key string
	err := q.QueryRow(ctx, `select coalesce(min(key), '') from issues where project_key = $1 and title = $2`, project, modelTitle).Scan(&key)
	if err != nil {
		return "", fmt.Errorf("read whether %s holds the corpus: %w", project, err)
	}
	return key, nil
}

// CheckOptions are one self-check: the project the corpus was written into and the directory of
// the captures, whose asks and sections the copied issues are counted against.
type CheckOptions struct {
	Project  string
	Fixtures string
}

// Report is what one self-check read: one line for each bullet, row and replayed fixture, and a
// last line counting them.
type Report struct {
	lines []string
}

// Print writes the report's lines.
func (r Report) Print(out io.Writer) {
	for _, line := range r.lines {
		fmt.Fprintln(out, line)
	}
}

// CheckFailed is a self-check that found a bullet or a row missing, or holding another count than
// the one written, naming each.
type CheckFailed struct{ Failed []string }

func (e *CheckFailed) Error() string {
	return fmt.Sprintf("%d of the corpus's reads failed: %s", len(e.Failed), strings.Join(e.Failed, "; "))
}

// read is one count a clause reads from the store: what it counts, the statement that counts it,
// its arguments and the count the seed wrote.
type read struct {
	what string
	sql  string
	args []any
	want int
}

// clause is one bullet, named row or replayed fixture, and the reads it is held to. left, where
// set, names the separate part the bullet is left to, and the clause reads nothing.
type clause struct {
	name  string
	reads []read
	left  string
}

// current is the relations the store asserts now among the tuples where holds, the latest
// tuple of each (from, kind, to) by its seq being an assertion: a relation a later tuple retracted
// is not current.
func current(where string) string {
	return `(select * from (select distinct on (from_kind, from_id, kind, to_kind, to_id) from_kind, from_id, kind, to_kind, to_id, asserted
		from graph_tuples where to_kind <> 'value' and (` + where + `)
		order by from_kind, from_id, kind, to_kind, to_id, seq desc) latest where asserted)`
}

// Check reads every bullet of the third fixture, the three rows the two checks name and the
// replayed fixtures back from the store, by statements of its own over the tables, never through
// the code that wrote them. Each line names the clause, every count it read and the count the
// seed wrote, and the run fails where a count is zero or differs from it, naming the clause and
// the read; a bullet left to a separate part is printed as such, with the head that adds its kind.
func Check(ctx context.Context, q graphstore.Queryer, options CheckOptions) (Report, error) {
	if options.Project == "" {
		options.Project = DefaultProject
	}
	var report Report
	key, err := issueTitled(ctx, q, options.Project, modelTitle)
	if err != nil {
		return report, err
	}
	handover, err := issueTitled(ctx, q, options.Project, handoverTitle)
	if err != nil {
		return report, err
	}
	premortemIssue, err := issueTitled(ctx, q, options.Project, premortemTitle)
	if err != nil {
		return report, err
	}
	captured, err := readCaptures(options.Fixtures)
	if err != nil {
		return report, err
	}
	clauses := modelClauses(key, handover)
	for _, capture := range captured.copied {
		copiedKey, err := issueTitled(ctx, q, options.Project, capture.Title)
		if err != nil {
			return report, err
		}
		clauses = append(clauses, copiedClause(capture, copiedKey))
	}
	clauses = append(clauses, premortemClause(premortemIssue))

	var failed []string
	passed, left := 0, 0
	for _, c := range clauses {
		if c.left != "" {
			left++
			report.lines = append(report.lines, "LEFT: "+c.name+": "+c.left)
			continue
		}
		var counts, problems []string
		for _, r := range c.reads {
			var got int
			if err := q.QueryRow(ctx, r.sql, r.args...).Scan(&got); err != nil {
				return report, fmt.Errorf("%s: read %s: %w", c.name, r.what, err)
			}
			counts = append(counts, fmt.Sprintf("%d %s (wrote %d)", got, r.what, r.want))
			if got != r.want {
				problems = append(problems, fmt.Sprintf("read %d %s, want %d", got, r.what, r.want))
			}
		}
		if hasPositive(c.reads) == 0 {
			problems = append(problems, "no read of the clause counts a population the seed wrote")
		}
		if len(problems) > 0 {
			failed = append(failed, c.name+": "+strings.Join(problems, ", "))
			report.lines = append(report.lines, "FAIL: "+c.name+": examined "+strings.Join(counts, ", ")+": "+strings.Join(problems, ", "))
			continue
		}
		passed++
		report.lines = append(report.lines, "PASS: "+c.name+": examined "+strings.Join(counts, ", "))
	}
	report.lines = append(report.lines, fmt.Sprintf("check: %d clauses on %s (%s the seeded issue): %d passed, %d left to a separate part, %d failed",
		len(clauses), options.Project, key, passed, left, len(failed)))
	if len(failed) > 0 {
		return report, &CheckFailed{Failed: failed}
	}
	return report, nil
}

// hasPositive is how many of reads count a population the seed wrote, which every clause needs at
// least one of: a read that wants zero, an absence, stands beside a read of the same kind that
// finds what the seed planted.
func hasPositive(reads []read) int {
	positive := 0
	for _, r := range reads {
		if r.want > 0 {
			positive++
		}
	}
	return positive
}

// issueTitled is the key of the one issue of project titled title, refusing none or more than one.
func issueTitled(ctx context.Context, q graphstore.Queryer, project, title string) (string, error) {
	var keys []string
	rows, err := q.Query(ctx, `select key from issues where project_key = $1 and title = $2 order by number`, project, title)
	if err != nil {
		return "", fmt.Errorf("find the issue %q: %w", title, err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return "", err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(keys) != 1 {
		return "", &CheckFailed{Failed: []string{fmt.Sprintf("project %s holds %d issues titled %q, want one: was the corpus seeded here once?", project, len(keys), title)}}
	}
	return keys[0], nil
}
