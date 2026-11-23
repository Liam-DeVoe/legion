package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/access"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// nearCopyPlanStatement is one statement of the near-copy gate as a plan test prepares it: the
// placeholder the creator's viewer binds, and the parameter types and literals its prepared form
// is explained with.
type nearCopyPlanStatement struct {
	name, sql, viewer    string
	parameters, literals []string
}

// nearCopyPlanStatements are the gate's two statements, each bound to an open full member who
// sees one project (planViewer), so no arm of the project clause is folded away.
func nearCopyPlanStatements() []nearCopyPlanStatement {
	viewer := "'" + string(planViewer) + "'"
	options := "'" + duplicateHeadlineOptions + "'"
	return []nearCopyPlanStatement{{
		name: "the scoring statement", sql: duplicateScoreQuery, viewer: "$8",
		parameters: []string{"text", "text[]", "float8[]", "text[]", "float8", "text", "text", "jsonb"},
		literals:   []string{"'TEST'", "array['astrolab']", "array[1]::float8[]", "'{}'", "0.6", options, "'Astrolabe readings'", viewer},
	}, {
		name: "the equal-lexemes statement", sql: duplicateEqualLexemesQuery, viewer: "$6",
		parameters: []string{"text", "text", "text", "text[]", "text", "jsonb"},
		literals:   []string{"'TEST'", "'Astrolabe readings'", "''", "'{}'", options, viewer},
	}}
}

// The near-copy gate reads one project, $1, so each of its statements decides once whether the
// creator's viewer sees that project, never once per row: its project clause tests the parameter
// rather than a row's project_key. A prepared statement runs, from a connection's sixth run on,
// the generic plan Postgres makes without its arguments, and there a clause over the row's column
// is a filter of every issue whose share Postgres guesses, the viewer being a parameter it cannot
// read; over the parameter alone it is a One-Time Filter, decided before any row is read and
// counted in no estimate, so the generic plan reads issues as it would with no viewer at all.
//
// The test reads the generic plan of each statement and holds every condition that tests a row to
// naming no viewer parameter, and requires a One-Time Filter that does, so a plan that bound no
// viewer at all cannot pass. Its control is each statement with the clause over the row's project,
// as the gate wrote it before: the same check must find the viewer in a row's filter there, or it
// is a check that cannot fail.
func TestTheNearCopyGateDecidesItsViewerOncePerStatement(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	connection, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection for the plans: %v", err)
	}
	defer connection.Release()
	for number, statement := range nearCopyPlanStatements() {
		t.Run(statement.name, func(t *testing.T) {
			plan := explaintest.GenericPlan(t, ctx, connection, fmt.Sprintf("near_copy_%d", number),
				statement.sql, statement.parameters, statement.literals)
			perRow, once := viewerConditions(t, plan, statement.viewer)
			if len(perRow) > 0 {
				t.Errorf("%s's generic plan tests rows by the viewer %s: %q\n%s", statement.name, statement.viewer, perRow, plan)
			}
			if once == 0 {
				t.Errorf("%s's generic plan decides the viewer %s in no One-Time Filter:\n%s", statement.name, statement.viewer, plan)
			}

			rowClause := access.ProjectClause("i.project_key", statement.viewer)
			control := strings.ReplaceAll(statement.sql, access.ProjectClause("$1", statement.viewer), rowClause)
			if control == statement.sql {
				t.Fatalf("the control of %s is the statement itself: it holds no project clause over $1", statement.name)
			}
			controlPlan := explaintest.GenericPlan(t, ctx, connection, fmt.Sprintf("near_copy_control_%d", number),
				control, statement.parameters, statement.literals)
			if perRow, _ := viewerConditions(t, controlPlan, statement.viewer); len(perRow) == 0 {
				t.Errorf("the control of %s, its clause over each row's project, tests no row by the viewer %s, so the check cannot fail:\n%s",
					statement.name, statement.viewer, controlPlan)
			}
		})
	}
}

// viewerConditions reads plan, one "Plan" node of `EXPLAIN (FORMAT JSON)`, for the conditions that
// name viewer, the viewer's placeholder: every one a node tests rows by (a Filter, a Join Filter,
// and an index, hash, merge or recheck condition), each as its key and text, and the count of
// One-Time Filters, which a node tests once before it reads a row.
func viewerConditions(t *testing.T, plan json.RawMessage, viewer string) ([]string, int) {
	t.Helper()
	named := regexp.MustCompile(regexp.QuoteMeta(viewer) + `\b`)
	var decoded any
	if err := json.Unmarshal(plan, &decoded); err != nil {
		t.Fatalf("decode the plan: %v\n%s", err, plan)
	}
	var perRow []string
	once := 0
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, field := range typed {
				if text, ok := field.(string); ok && (strings.HasSuffix(key, "Filter") || strings.HasSuffix(key, "Cond")) && named.MatchString(text) {
					if key == "One-Time Filter" {
						once++
					} else {
						perRow = append(perRow, key+": "+text)
					}
				}
				walk(field)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(decoded)
	return perRow, once
}
