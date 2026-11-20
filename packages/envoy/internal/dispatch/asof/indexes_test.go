package asof_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/asof"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// Three reads at a cut each have an index of their own on events (decision 15 of the plan),
// measured on a copy of production's event log: the issue listing reads each issue's newest
// state through events_issue_state, the asks read each ask's newest state through
// events_ask_state, and the bound of an event log's page reads the ids at the cut through
// events_created_at. A partial index applies only when the planner can prove that the read's
// condition implies the index's predicate. So a shadow whose type list or expression drifts from
// its index still answers right, but reads each event of each issue or ask again: on the copy,
// the issue listing took 1,215 ms without its index and 33 ms with it. A bound written as
// `order by id desc limit 1` reads each event after the cut. The plan of each read, bound to a
// cut amid the seeded events, is what this test holds.
func TestTheShadowsUseTheirIndexes(t *testing.T) {
	w := newWorld(t)
	seedIndexedReads(t, w)
	cut := mustCut(t, "2026-11-01T01:40:00Z")
	keys := explaintest.NewIndexKeys(w.database.Pool)

	for _, read := range []struct {
		name, statement string
		arguments       []any
		index           string
	}{
		{
			// The listing's own filters and order (api/issue_queries.go, issueListFilters and
			// issueListOrder), reduced to the ones this seed decides.
			name: "the issue listing",
			statement: `select i.key, i.status, i.rank from issues i
				where ($1 = '' or i.project_key = $1) and (not $2::boolean or i.closed_at is null)
				order by i.rank asc, i.created_at asc, i.key asc`,
			arguments: []any{"IDX", false},
			index:     "events_issue_state",
		},
		{
			// Every open ask of every project, as the inbox and the open asks read them.
			name:      "the open asks",
			statement: `select a.id, a.issue_key, a.question from asks a where a.state = 'open' order by a.created_at, a.id`,
			index:     "events_ask_state",
		},
		{
			name:      "the bound of an event log's page",
			statement: asof.NewestEventIDStatement,
			index:     "events_created_at",
		},
	} {
		t.Run(read.name, func(t *testing.T) {
			bound, err := asof.Bind(read.statement, cut)
			if err != nil {
				t.Fatalf("Bind(%q): %v", read.statement, err)
			}
			plan := planWithoutSequentialScans(t, w, bound, read.arguments...)
			keys.RequireIndexKeyed(t, plan, read.index, read.name+" at a cut", explaintest.CondRequired)
		})
	}
}

// The ask-state index's predicate names the event types the asks shadow reads, in its order. The
// migration writes the list by hand, since a migration is SQL that never changes once applied: a
// type the shadow reads later would leave the index unused, and a type the shadow no longer
// reads would keep its events in the index for nothing.
func TestTheAskStateIndexNamesTheTypesTheAsksShadowReads(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "store", "migrations", "*_events_ask_state.up.sql"))
	if err != nil || len(files) != 1 {
		t.Fatalf("the migrations of events_ask_state: %v, %v; want one file", files, err)
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var statement []string
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			statement = append(statement, line)
		}
	}
	predicate := regexp.MustCompile(`(?s)\bwhere\s+type\s+in\s+\(([^)]*)\)`).FindStringSubmatch(strings.Join(statement, "\n"))
	if predicate == nil {
		t.Fatalf("%s holds no `where type in (...)`:\n%s", files[0], content)
	}
	var types []string
	for _, literal := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(predicate[1], -1) {
		types = append(types, literal[1])
	}
	if want := asof.AskStateTypes(); !slices.Equal(types, want) {
		t.Errorf("%s indexes the types %v, want the asks shadow's %v", files[0], types, want)
	}
}

// seedIndexedReads writes 400 issues of project IDX, each with one ask, and the events their
// writers append: per issue an issue.created, 20 comment.created, an issue.updated, an ask.opened
// and an ask.answered, one a second from n × 30 seconds after 2026-11-01T00:00Z for issue n. So
// most of an issue's events are not the ones its state is read from, as in production, and the
// events run to 03:20, past the test's cut. It vacuums and analyzes the tables, so the planner
// reads these counts.
func seedIndexedReads(t *testing.T, w world) {
	t.Helper()
	w.exec(`insert into projects (key, name, default_assignee, created_at) values ('IDX', 'IDX', 'carol', '2026-11-01T00:00:00Z')`)
	w.exec(`
		insert into issues (key, project_key, number, title, rank, created_by, created_at, updated_at)
		select 'IDX-' || n, 'IDX', n, 'Issue ' || n, lpad(n::text, 4, '0'), '{"kind":"user","id":"alice"}',
			timestamptz '2026-11-01T00:00:00Z' + make_interval(secs => n * 30),
			timestamptz '2026-11-01T00:00:00Z' + make_interval(secs => n * 30)
		from generate_series(1, 400) n`)
	w.exec(`
		insert into asks (id, issue_key, author, question, options, created_at)
		select md5('ask-' || n)::uuid, 'IDX-' || n, '{"kind":"user","id":"alice"}', 'Ship it?', '[]',
			timestamptz '2026-11-01T00:00:00Z' + make_interval(secs => n * 30 + 23)
		from generate_series(1, 400) n`)
	w.exec(`
		insert into events (issue_key, seq, type, actor, payload, notify, created_at)
		select 'IDX-' || n, s,
			case s when 1 then 'issue.created' when 22 then 'issue.updated' when 23 then 'ask.opened'
				when 24 then 'ask.answered' else 'comment.created' end,
			'{"kind":"user","id":"alice"}',
			case when s >= 23 then jsonb_build_object('id', md5('ask-' || n)::uuid) else '{}'::jsonb end,
			false,
			timestamptz '2026-11-01T00:00:00Z' + make_interval(secs => n * 30 + s)
		from generate_series(1, 400) n, generate_series(1, 24) s`)
	w.exec(`update issues set last_seq = 24`)
	w.exec(`vacuum analyze events, issues, asks`)
}

// planWithoutSequentialScans is the plan PostgreSQL makes for statement in a transaction of its
// own with sequential scans off. The seed is far smaller than production's event log, and a
// table this small is read fastest whole however a statement is written; with sequential scans
// off, the plan shows which index the statement's shape lets the planner read.
func planWithoutSequentialScans(t *testing.T, w world, statement string, arguments ...any) json.RawMessage {
	t.Helper()
	ctx := context.Background()
	tx, err := w.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the plan's transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "set local enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scans: %v", err)
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "explain (format json) "+statement, arguments...).Scan(&raw); err != nil {
		t.Fatalf("explain %q: %v", statement, err)
	}
	return explaintest.OnePlan(t, raw)
}
