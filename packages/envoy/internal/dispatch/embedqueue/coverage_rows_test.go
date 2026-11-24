package embedqueue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/embed"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// fillCounts fills the coverage counts as the counts loop's first run does (FillCoverage),
// failing the test on an error.
func fillCounts(t *testing.T, database *store.Store) {
	t.Helper()
	if err := FillCoverage(context.Background(), database); err != nil {
		t.Fatalf("fill the coverage counts: %v", err)
	}
}

// foldCounts runs one fold (foldCoverage), failing the test on an error.
func foldCounts(t *testing.T, database *store.Store) {
	t.Helper()
	if err := foldCoverage(context.Background(), database); err != nil {
		t.Fatalf("fold the coverage counts: %v", err)
	}
}

// failFill runs a fill that fails at its last group, after every group before it has written its
// base rows: search_coverage_filled refuses the row the fill records itself with, and takes it
// again once the fill has failed. The counts are then filled in every group but the record that
// says so, the state a fill a stop or a database error cut off at its end leaves.
func failFill(t *testing.T, database *store.Store) {
	t.Helper()
	mustExec(t, database.Pool, `alter table search_coverage_filled add constraint fill_refused check (false)`)
	if err := FillCoverage(context.Background(), database); err == nil {
		t.Fatal("a fill whose record of its end is refused succeeded")
	}
	mustExec(t, database.Pool, `alter table search_coverage_filled drop constraint fill_refused`)
}

// topUpDeltas adds delta rows until search_coverage_deltas holds total, each of the component's
// one class and summing to nothing (+1 and -1 in turn, and a 0 last for an odd count), so the
// counts answer what they answered and the report reads exactly total delta rows.
func topUpDeltas(t *testing.T, database *store.Store, total int) {
	t.Helper()
	ctx := context.Background()
	var held int
	if err := database.Pool.QueryRow(ctx, `select count(*) from search_coverage_deltas`).Scan(&held); err != nil {
		t.Fatalf("count the delta rows: %v", err)
	}
	if held > total {
		t.Fatalf("the database holds %d delta rows already, more than the %d the test reads", held, total)
	}
	mustExec(t, database.Pool, `
		insert into search_coverage_deltas (kind, model, matched, dead, items)
		select 'component', '', false, false,
		       case when g = $1 and $1 % 2 = 1 then 0 when g % 2 = 0 then 1 else -1 end
		from generate_series(1, $1) g`, total-held)
}

// readSixColumns is the report's statement (coverageRead) narrowed to the six columns main's
// coverage statement answers, for a comparison of the two row for row.
var readSixColumns = `select kind, indexed, items, current, failed, oldest
	from (` + coverageRead + `) read (delta_rows, filled, kind, indexed, items, current, failed, oldest)`

// readRowsOf reads the report's statement for identity in tx as Coverage runs it, under the
// settings planCoverage sets, inside a savepoint it rolls back, as headRowsOf reads main's.
func readRowsOf(t *testing.T, tx pgx.Tx, identity string) []string {
	t.Helper()
	ctx := context.Background()
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("open a savepoint for the report's statement: %v", err)
	}
	defer func() { _ = savepoint.Rollback(ctx) }()
	if err := planCoverage(ctx, savepoint); err != nil {
		t.Fatalf("plan the report's statement as Coverage does: %v", err)
	}
	rows, _ := coverageRowsOf(t, savepoint, readSixColumns, identity)
	return rows
}

// requireReadIsMains fails the test unless the report's statement answers what main's statement
// (mainCoverageQuery) answers, every row whole, for the configured identity, for none and for
// another, all in one REPEATABLE READ snapshot and so one now(): R7's "the same members, kinds and
// counts as main's statement at the same snapshot", the oldest lacking ages included.
func requireReadIsMains(t *testing.T, database *store.Store, stage string) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("%s: begin the snapshot both statements read: %v", stage, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, identity := range []string{configuredIdentity, "", foreignIdentity} {
		mains, _ := coverageRowsOf(t, tx, mainCoverageQuery, identity)
		reads := readRowsOf(t, tx, identity)
		if !slices.Equal(mains, reads) {
			t.Errorf("%s, identity %q: the report's statement answers\n  %s\nwhere main's statement answers\n  %s",
				stage, identity, strings.Join(reads, "\n  "), strings.Join(mains, "\n  "))
		}
	}
}

// requireCoverageIsMains fails the test unless Coverage, the route's read, answers for identity
// the members, kinds and counts main's statement answers read just after it, with each oldest
// lacking age within a second of main's (the two read two snapshots, a moment apart).
func requireCoverageIsMains(t *testing.T, database *store.Store, identity, stage string) {
	t.Helper()
	ctx := context.Background()
	report, err := Coverage(ctx, database, identity)
	if err != nil {
		t.Fatalf("%s: Coverage: %v", stage, err)
	}
	tx, err := database.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("%s: begin main's read: %v", stage, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, mains := coverageRowsOf(t, tx, mainCoverageQuery, identity)
	for kind, counts := range map[string]struct {
		items, current, failed, lacking int64
		oldest                          *int64
	}{
		"issue":          {report.Kinds.Issue.Items, report.Kinds.Issue.Current, report.Kinds.Issue.Failed, report.Kinds.Issue.Lacking, report.Kinds.Issue.OldestLackingSeconds},
		"document_block": {report.Kinds.DocumentBlock.Items, report.Kinds.DocumentBlock.Current, report.Kinds.DocumentBlock.Failed, report.Kinds.DocumentBlock.Lacking, report.Kinds.DocumentBlock.OldestLackingSeconds},
		"comment_block":  {report.Kinds.CommentBlock.Items, report.Kinds.CommentBlock.Current, report.Kinds.CommentBlock.Failed, report.Kinds.CommentBlock.Lacking, report.Kinds.CommentBlock.OldestLackingSeconds},
		"message_block":  {report.Kinds.MessageBlock.Items, report.Kinds.MessageBlock.Current, report.Kinds.MessageBlock.Failed, report.Kinds.MessageBlock.Lacking, report.Kinds.MessageBlock.OldestLackingSeconds},
		"ask_part":       {report.Kinds.AskPart.Items, report.Kinds.AskPart.Current, report.Kinds.AskPart.Failed, report.Kinds.AskPart.Lacking, report.Kinds.AskPart.OldestLackingSeconds},
	} {
		main := mains[kind]
		if counts.items != main.items || counts.current != main.current || counts.failed != main.failed ||
			counts.lacking != main.items-main.current-main.failed {
			t.Errorf("%s, identity %q, %s: the route answers items %d, current %d, failed %d, lacking %d; main's statement %s",
				stage, identity, kind, counts.items, counts.current, counts.failed, counts.lacking, main)
		}
		if (counts.oldest == nil) != (main.oldest == nil) ||
			(counts.oldest != nil && (*counts.oldest-max(*main.oldest, 0) > 1 || max(*main.oldest, 0)-*counts.oldest > 1)) {
			t.Errorf("%s, identity %q, %s: the route answers the oldest lacking age %v; main's statement %s",
				stage, identity, kind, counts.oldest, main)
		}
	}
	for _, kind := range report.NotIndexed {
		if main := mains[kind.Kind]; main.indexed || kind.Items != main.items {
			t.Errorf("%s, identity %q: the route counts %d of %s; main's statement %s", stage, identity, kind.Items, kind.Kind, main)
		}
	}
}

// seedRowsCorpus writes items numbered from to to of every kind the report counts under project,
// each kind in every class main's statement tells apart: issues waiting for a vector, current,
// embedded by another identity and dead; windows of each window kind the same, and white space
// alone; components, versions of a project document, results and handoffs. Each kind is written in
// one statement over generate_series, so a corpus of twice the items differs from one of the items
// in its rows alone, and its triggers keep the counts as any write does. The first call (from 1)
// writes what the rest hang from: the project, an architecture source and snapshot, and the
// document.
func seedRowsCorpus(t *testing.T, database *store.Store, project string, from, to int) {
	t.Helper()
	q := database.Pool
	vector, foreign := embed.Literal(oneVector()), foreignIdentity
	if from == 1 {
		mustExec(t, q, `insert into projects (key, name, default_assignee) values ($1, 'Rows', 'alice')`, project)
		mustExec(t, q, `insert into architecture_sources (project_key, id, repo, branch, installation_id, created_by)
			values ($1, 1, 'rows/arch', 'main', 1, '{"kind":"system","id":"test"}')`, project)
		mustExec(t, q, `insert into architecture_snapshots (id, project_key, commit, files) values (900, $1, 'c1', '{}')`, project)
		mustExec(t, q, `insert into artifacts (project_key, slug, name, kind, created_by, ref_key)
			values ($1, 'rows-md', 'rows.md', 'doc', '{"kind":"user","id":"alice"}', $1 || '/rows-md')`, project)
	}
	mustExec(t, q, `
		insert into issues (key, project_key, number, title, created_by, rank)
		select $1 || '-' || g, $1, g, 'An issue of the rows corpus ' || g, '{"kind":"user","id":"alice"}', 'A'
		from generate_series($2::int, $3::int) g`, project, from, to)
	issues := `kind = 'issue' and id in (select $1 || '-' || g from generate_series($2::int, $3::int) g where g % 4 = `
	mustExec(t, q, `update embeddings set embedding = $4::vector, model = $5, embedded_hash = content_hash, embedded_at = now()
		where `+issues+`1)`, project, from, to, vector, configuredIdentity)
	mustExec(t, q, `update embeddings set embedding = $4::vector, model = $5, embedded_hash = content_hash, embedded_at = now()
		where `+issues+`2)`, project, from, to, vector, foreign)
	mustExec(t, q, `update embeddings set dead = true, confirmed_failures = 10, dead_model = $4
		where `+issues+`3)`, project, from, to, configuredIdentity)
	for _, kind := range windowCoverageKinds {
		mustExec(t, q, `
			select embeddings_enqueue_window($1, $2 || '/' || $1 || '#' || g,
			                                 case when g % 5 = 4 then ' ' else 'A window of the rows corpus ' || g end, array['b1'])
			from generate_series($3::int, $4::int) g`, kind, project, from, to)
		windows := `kind = $1 and id in (select $2 || '/' || $1 || '#' || g from generate_series($3::int, $4::int) g where g % 5 = `
		mustExec(t, q, `update embeddings set embedding = $5::vector, model = $6, embedded_hash = content_hash, embedded_at = now()
			where `+windows+`1)`, kind, project, from, to, vector, configuredIdentity)
		mustExec(t, q, `update embeddings set embedding = $5::vector, model = $6, embedded_hash = content_hash, embedded_at = now()
			where `+windows+`2)`, kind, project, from, to, vector, foreign)
		mustExec(t, q, `update embeddings set dead = true, confirmed_failures = 10, dead_model = $5
			where `+windows+`3)`, kind, project, from, to, configuredIdentity)
	}
	mustExec(t, q, `
		insert into components (project_key, id, title, prose, snapshot_id, source_id)
		select $1, 'component-' || g, 'Component ' || g, '', 900, 1 from generate_series($2::int, $3::int) g`, project, from, to)
	mustExec(t, q, `
		insert into artifact_versions (artifact_id, number, markdown, authors)
		select (select id from artifacts where ref_key = $1 || '/rows-md'), g, '# Version ' || g, '[]'
		from generate_series($2::int, $3::int) g`, project, from, to)
	mustExec(t, q, `
		insert into check_results (issue_key, check_issue_key, check_artifact_id, check_block_id,
		                           check_version, check_sha256, role, commit_sha, verdict, actor)
		select $1 || '-' || g, $1 || '-' || g, (select id from artifacts where ref_key = $1 || '/rows-md'), 'k' || g,
		       1, 'a-check-sha', 'tester', repeat('a', 40), 'pass', '{"kind":"user","id":"alice"}'::jsonb
		from generate_series($2::int, $3::int) g`, project, from, to)
	mustExec(t, q, `
		insert into handoffs (issue_key, phase, version, body, author)
		select $1 || '-' || g, 'implement', 1, '{}', '{"kind":"user","id":"alice"}'::jsonb
		from generate_series($2::int, $3::int) g`, project, from, to)
}

// coverageRouteState is one state the route reads the kept counts in (R7a, R7b): what brings a
// seeded database to it, how many delta rows it then holds unfolded, and what Coverage answers
// there.
type coverageRouteState struct {
	name    string
	prepare func(t *testing.T, database *store.Store)
	deltas  int
	answers error
}

// coverageRouteStates is every state the route reads the kept counts in: filled; not filled, with
// the delta rows the triggers wrote from the migration on; after a fill that failed at its end,
// which wrote every base row but the record that it ended; at the delta bound; and above it. Each
// holds coverageDeltaBound delta rows unfolded, which is as many as any but the last allows, and
// the last one more. The filled state's base has had its fold; the state at the bound has had
// none since its fill.
var coverageRouteStates = []coverageRouteState{
	{"filled", func(t *testing.T, database *store.Store) { fillCounts(t, database); foldCounts(t, database) }, coverageDeltaBound, nil},
	{"not filled", func(*testing.T, *store.Store) {}, coverageDeltaBound, ErrCoverageNotFilled},
	{"after a failed fill", failFill, coverageDeltaBound, ErrCoverageNotFilled},
	{"at the delta bound", fillCounts, coverageDeltaBound, nil},
	{"above the delta bound", fillCounts, coverageDeltaBound + 1, ErrCoverageFoldBehind},
}

// seedRouteState writes the rows corpus of size items of every kind to a database of its own,
// brings it to state and tops its delta rows up to the state's count.
func seedRouteState(t *testing.T, state coverageRouteState, size int) *store.Store {
	t.Helper()
	database := storetest.Open(t)
	seedRowsCorpus(t, database, "ROWS", 1, size)
	state.prepare(t, database)
	topUpDeltas(t, database, state.deltas)
	return database
}

// explainRead explains the report's statement for identity as Coverage runs it - in a read-only
// transaction, under its statement_timeout and the settings planCoverage sets - with ANALYZE and
// BUFFERS when analyze, and answers its plan.
func explainRead(t *testing.T, database *store.Store, statement string, args []any, analyze bool) []byte {
	t.Helper()
	ctx := context.Background()
	tx, err := database.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin the explain: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	mustExec(t, tx, "set local statement_timeout = '"+coverageStatementTimeout+"'")
	if err := planCoverage(ctx, tx); err != nil {
		t.Fatalf("plan the statement as Coverage does: %v", err)
	}
	options := "format json"
	if analyze {
		options = "analyze, buffers, format json"
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "explain ("+options+") "+statement, args...).Scan(&raw); err != nil {
		t.Fatalf("explain the statement: %v", err)
	}
	return explaintest.OnePlan(t, raw)
}

// rowsRead is the sum over every scan of a relation in plan, an analyzed plan, of the rows it
// answered times the times it ran, and those of them that read search_coverage_deltas.
func rowsRead(t *testing.T, plan []byte) (read, deltas float64, scans []string) {
	t.Helper()
	for _, scan := range explaintest.Scans(t, plan) {
		rows := scan.Loops * scan.Rows
		read += rows
		if scan.Relation == "search_coverage_deltas" {
			deltas += rows
		}
		scans = append(scans, fmt.Sprintf("%s using %s: %v x %v", scan.Relation, scan.Index, scan.Loops, scan.Rows))
	}
	return read, deltas, scans
}

// requireAnswers fails the test unless Coverage answers want for identity: no error when want is
// nil, and otherwise an error that is want.
func requireAnswers(t *testing.T, database *store.Store, identity string, want error, stage string) {
	t.Helper()
	_, err := Coverage(context.Background(), database, identity)
	if want == nil && err != nil || want != nil && !errors.Is(err, want) {
		t.Errorf("%s: Coverage answers %v, want %v", stage, err, want)
	}
}

// TestCoverageReadsTheSameRowsAtTwiceTheCorpus holds R7a: the route reads a number of rows that
// does not grow with the corpus, in every state it reads in (coverageRouteStates). In each, at N
// and at 2N items of every kind (seedRowsCorpus), the report's one statement is explained with
// ANALYZE for the configured identity and for none, and the rows every scan of a relation answered,
// times the times it ran, add up to the same at both sizes; no state reads more than
// coverageDeltaBound + 1 delta rows, and each reads every delta row it holds up to that.
func TestCoverageReadsTheSameRowsAtTwiceTheCorpus(t *testing.T) {
	const n = 20
	for _, state := range coverageRouteStates {
		t.Run(state.name, func(t *testing.T) {
			read := map[string]float64{}
			scans := map[string][]string{}
			for _, size := range []int{n, 2 * n} {
				database := seedRouteState(t, state, size)
				for _, identity := range []string{configuredIdentity, ""} {
					stage := fmt.Sprintf("%d items of every kind, identity %q", size, identity)
					requireAnswers(t, database, identity, state.answers, stage)
					rows, deltas, named := rowsRead(t, explainRead(t, database, coverageRead, []any{identity}, true))
					if deltas > coverageDeltaBound+1 || deltas != float64(min(state.deltas, coverageDeltaBound+1)) {
						t.Errorf("%s: the report read %v delta rows of the %d it holds, want them all and at most %d",
							stage, deltas, state.deltas, coverageDeltaBound+1)
					}
					key := identity + "@" + fmt.Sprint(size)
					read[key], scans[key] = rows, named
				}
			}
			for _, identity := range []string{configuredIdentity, ""} {
				small, large := identity+"@"+fmt.Sprint(n), identity+"@"+fmt.Sprint(2*n)
				if read[small] != read[large] {
					t.Errorf("identity %q: the report read %v rows at %d items of every kind and %v at %d:\n  at %d: %s\n  at %d: %s",
						identity, read[small], n, read[large], 2*n,
						n, strings.Join(scans[small], "; "), 2*n, strings.Join(scans[large], "; "))
				}
			}
		})
	}
}

// coverageReadUnkeyedTables are the tables the report may read with no index condition: the base
// rows and the delta rows, which it reads whole (the delta rows up to coverageDeltaBound + 1), and
// the two tables of one row. Every other table it reads grows with the corpus and is read through
// an index condition.
var coverageReadUnkeyedTables = []string{"search_coverage_counts", "search_coverage_deltas", "search_coverage_filled", "route_roster"}

// indexTables is every index of the database's public schema, with the table it is on.
func indexTables(t *testing.T, database *store.Store) map[string]string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `select indexname, tablename from pg_indexes where schemaname = 'public'`)
	if err != nil {
		t.Fatalf("read the indexes: %v", err)
	}
	tables := map[string]string{}
	for rows.Next() {
		var index, table string
		if err := rows.Scan(&index, &table); err != nil {
			t.Fatalf("read an index: %v", err)
		}
		tables[index] = table
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the indexes: %v", err)
	}
	return tables
}

// unkeyedScansOfGrowingTables is every scan of plan with no index condition
// (explaintest.ScansWithoutIndexCond) of a table other than coverageReadUnkeyedTables; a Bitmap
// Index Scan, which names its index alone, is judged by its index's table.
func unkeyedScansOfGrowingTables(t *testing.T, plan []byte, tables map[string]string) []string {
	t.Helper()
	var growing []string
	for _, scan := range explaintest.ScansWithoutIndexCond(t, plan) {
		table := ""
		if _, on, found := strings.Cut(scan, " on "); found {
			table, _, _ = strings.Cut(on, " ")
		} else if _, index, found := strings.Cut(scan, " using "); found {
			table = tables[index]
		}
		if !slices.Contains(coverageReadUnkeyedTables, table) {
			growing = append(growing, scan)
		}
	}
	return growing
}

// TestTheCoverageReadScansNoTableWithoutAnIndexCondition holds R7b's plan: in every state the
// route reads in, the report's statement reads every table but the base rows, the delta rows and
// the two tables of one row through an index condition (explaintest.ScansWithoutIndexCond), for
// the configured identity and for none.
func TestTheCoverageReadScansNoTableWithoutAnIndexCondition(t *testing.T) {
	for _, state := range coverageRouteStates {
		t.Run(state.name, func(t *testing.T) {
			database := seedRouteState(t, state, 10)
			tables := indexTables(t, database)
			for _, identity := range []string{configuredIdentity, ""} {
				plan := explainRead(t, database, coverageRead, []any{identity}, false)
				if growing := unkeyedScansOfGrowingTables(t, plan, tables); len(growing) > 0 {
					t.Errorf("identity %q: the report's statement reads a table that grows with the corpus by a scan with no index condition: %s",
						identity, strings.Join(growing, "; "))
				}
			}
		})
	}
}

// sentStatement is one statement a traced pool sent, with its arguments.
type sentStatement struct {
	sql  string
	args []any
}

// sentRecorder records every statement started on the pool it traces (storetest.Traced), with its
// arguments, so a test can explain each one as it was sent.
type sentRecorder struct {
	mu   sync.Mutex
	sent []sentStatement
}

func (r *sentRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, sentStatement{data.SQL, data.Args})
	return ctx
}

func (r *sentRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take answers the statements recorded since the last take, and forgets them.
func (r *sentRecorder) take() []sentStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	sent := r.sent
	r.sent = nil
	return sent
}

// TestTheCoverageRouteSendsNoStatementOfMains holds R7b's statements: a tracer records every
// statement Coverage sends in every state the route reads in, for the configured identity and for
// none. None is main's coverage statement, neither the one main's route ran
// (mainRouteCoverageStatement) nor the one before it (mainCoverageQuery); every statement but the
// transaction's own (its begin, its bound, planCoverage's settings, its rollback) reads, and each
// such read, explained with the arguments it was sent with, reads no table that grows with the
// corpus by a scan with no index condition. Coverage answers each state's answer.
func TestTheCoverageRouteSendsNoStatementOfMains(t *testing.T) {
	for _, state := range coverageRouteStates {
		t.Run(state.name, func(t *testing.T) {
			database := seedRouteState(t, state, 10)
			tables := indexTables(t, database)
			recorder := &sentRecorder{}
			traced := &store.Store{Pool: storetest.Traced(t, database.Pool, recorder, nil)}
			for _, identity := range []string{configuredIdentity, ""} {
				requireAnswers(t, traced, identity, state.answers, fmt.Sprintf("identity %q", identity))
				reads := 0
				for _, sent := range recorder.take() {
					text := strings.ToLower(strings.TrimSpace(sent.sql))
					switch {
					case sent.sql == mainCoverageQuery || sent.sql == mainRouteCoverageStatement:
						t.Errorf("identity %q: the route sent main's coverage statement", identity)
					case strings.HasPrefix(text, "begin"), strings.HasPrefix(text, "set local"), text == "rollback":
					case strings.HasPrefix(text, "select"), strings.HasPrefix(text, "with"):
						reads++
						plan := explainRead(t, database, sent.sql, sent.args, false)
						if growing := unkeyedScansOfGrowingTables(t, plan, tables); len(growing) > 0 {
							t.Errorf("identity %q: a statement the route sent reads a table that grows with the corpus by a scan with no index condition: %s\n%s",
								identity, strings.Join(growing, "; "), sent.sql)
						}
					default:
						t.Errorf("identity %q: the route sent a statement that is neither a read nor its transaction's: %s", identity, sent.sql)
					}
				}
				if reads != 1 {
					t.Errorf("identity %q: the route sent %d reads, want its one statement", identity, reads)
				}
			}
		})
	}
}

// TestCoverageReadsAtMostTheDeltaBound holds R7j's read: with coverageDeltaBound delta rows
// unfolded Coverage answers the counts, which equal main's statement's, row for row in one
// snapshot and as Coverage answers them; with one more it answers ErrCoverageFoldBehind, the bound
// the route names being 100,000.
func TestCoverageReadsAtMostTheDeltaBound(t *testing.T) {
	database := storetest.Open(t)
	seedEveryCoverageState(t, database)
	fillCounts(t, database)
	topUpDeltas(t, database, coverageDeltaBound)

	requireReadIsMains(t, database, "100,000 delta rows unfolded")
	for _, identity := range []string{configuredIdentity, "", foreignIdentity} {
		requireCoverageIsMains(t, database, identity, "100,000 delta rows unfolded")
	}

	topUpDeltas(t, database, coverageDeltaBound+1)
	if _, err := Coverage(context.Background(), database, configuredIdentity); !errors.Is(err, ErrCoverageFoldBehind) {
		t.Fatalf("with 100,001 delta rows unfolded Coverage answers %v, want ErrCoverageFoldBehind", err)
	}
	if CoverageDeltaBound() != 100000 {
		t.Errorf("the bound the route names is %d, want spec version 27's 100,000", CoverageDeltaBound())
	}
}
