package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// What a14 holds the plans to: every statement its six operations run, the index each must read,
// the table each may not scan and the sort no plan may make, the EXPLAIN runs themselves, and the
// negative control - the shape this change replaced, run at the pile's own size and held to being
// far outside the bound. The measurement and its time bounds are
// api/inbox_dismissed_scale_test.go's, and the seed they run over
// api/inbox_dismissed_scale_seed_test.go's.
//
// It is a file of its own because a plan is held to different things than a time is: a number out
// of its bound is read here, where the statement, its index and its plan are, and the file that
// takes the readings stays short enough to read in one sitting.

// dismissedScaleExplained is one statement an operation runs, with what its plan is held to: the
// index it must read, the table it may not scan, and the column no Sort key may name. An empty
// noSortOn holds the plan to no Sort node at all, which is the rule replies_plan_test.go holds
// every Reply-half read to; an Agent-half read carries sorts of the list's own that are older
// than the dismissal - the roots' newest reply, the lateral that reads it, the ordered array_agg -
// so what it is held to is that no sort stands in for the dismissal, named by its column. The two
// write routes read their rows and then sort them by thread id to take their locks in one order
// (markInboxReadThreads), which is the route's own sort and not the index's, so each is held the
// same way, by the column its state is ordered by.
type dismissedScaleExplained struct {
	operation string
	half      string
	sql       string
	args      []any
	index     string
	// relation is the table the plan may not scan, empty for a read whose cheapest way to take a
	// whole set at this seed is a scan of it, and onceOver a table the plan must read once rather
	// than once per row: the Inbox's own three reads take the person's dismissal marks as one set,
	// and whether the planner takes that set off the index or off the table is its own business,
	// while taking it one root at a time is what made those reads grow with the pile.
	relation string
	noSortOn string
	onceOver string
}

// explainDismissedScaleReads explains every statement the six operations run, under
// EXPLAIN (ANALYZE, BUFFERS) with enable_seqscan and enable_bitmapscan left on, so the plans are
// what the planner really does at this size rather than what a pin makes it do. On the loaded
// shape each plan is held to its index and its sort rule; on the baselines they are printed and
// held to nothing, since a scan of an empty table is the right plan for an empty table.
func explainDismissedScaleReads(t *testing.T, shape *dismissedScaleShape, hold bool) {
	t.Helper()
	ctx := context.Background()
	connection, err := shape.database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection for the plans: %v", err)
	}
	defer connection.Release()
	for _, explained := range dismissedScaleStatements(shape) {
		name := explained.operation + " / " + explained.half + " @ " + shape.name
		plan, text := explainDismissedScale(t, connection.Conn(), name, explained.sql, explained.args)
		t.Logf("%s:\n%s", name, text)
		if !hold {
			continue
		}
		if explained.relation != "" && explaintest.SeqScansRelation(t, plan, explained.relation) {
			t.Errorf("%s scans %s:\n%s", name, explained.relation, text)
		}
		if !planReadsIndex(t, plan, explained.index) {
			t.Errorf("%s does not read %s:\n%s", name, explained.index, text)
		}
		if explained.onceOver != "" {
			if loops := planLoopsOver(t, plan, explained.onceOver); loops > 1 {
				t.Errorf("%s reads %s %.0f times over, want once: the Inbox's own reads take the person's marks as one set rather than one read a root, which is what stops them growing with the pile:\n%s",
					name, explained.onceOver, loops, text)
			}
		}
		for _, key := range planSortKeys(t, plan) {
			if explained.noSortOn == "" {
				t.Errorf("%s sorts on %s, which its index already answers in:\n%s", name, key, text)
				continue
			}
			if strings.Contains(key, explained.noSortOn) {
				t.Errorf("%s sorts on %s, which %s already answers in:\n%s", name, key, explained.index, text)
			}
		}
	}
	explainDismissedScaleOneRead(t, connection.Conn(), shape, hold)
}

// explainDismissedScaleOneRead explains the one read's Dismissed page as the handler builds it
// (GET /api/v1/me/inbox?state=dismissed&limit=200, meInboxStatement) and as it runs it, under the
// settings of the read's own transaction (meInboxSettings, readMeInbox): the first page and, where
// the shape holds one, the page 50,000 deep, each under the plan of its own arguments and under the
// generic plan a prepared statement settles on from its sixth execution, which knows nothing of the
// cursor or the page and is the plan the measurement mostly times. On the loaded shape each plan is
// held to the cut (meInboxDismissedCut): each of the two arms reads its own index, from the cursor
// and in that index's order, and takes no more of it than the page and the one row past it, so no
// node reading either index answers more than 201 rows and no sort orders anything by the
// dismissal. Before the cut each arm read the whole pile - 100,000 and 10,000 rows - into the walk.
func explainDismissedScaleOneRead(t *testing.T, connection *pgx.Conn, shape *dismissedScaleShape, hold bool) {
	t.Helper()
	ctx := context.Background()
	var horizon int64
	if err := connection.QueryRow(ctx, `select coalesce(max(id), 0) from events`).Scan(&horizon); err != nil {
		t.Fatalf("read the event log's horizon: %v", err)
	}
	pages := []struct{ operation, cursor string }{{"dismissed page 1", ""}}
	if shape.deepCursor != "" {
		pages = append(pages, struct{ operation, cursor string }{"dismissed page 50,000 deep", shape.deepCursor})
	}
	limit := dismissedScalePage
	for _, page := range pages {
		request := meInboxRequest{state: myRepliesStateDismissed, limit: &limit}
		if page.cursor != "" {
			raw, err := url.QueryUnescape(page.cursor)
			if err != nil {
				t.Fatalf("unescape the cursor %d deep: %v", dismissedScaleCursorDepth, err)
			}
			if request.cursor, err = parseMeInboxCursor(raw, myRepliesStateDismissed); err != nil {
				t.Fatalf("read the cursor %d deep: %v", dismissedScaleCursorDepth, err)
			}
		}
		statement, args, err := meInboxStatement(model.Actor{Kind: "user", ID: dismissedScaleLogin}, horizon, request, false, false, nil)
		if err != nil {
			t.Fatalf("build the one read's %s: %v", page.operation, err)
		}
		for _, generic := range []bool{false, true} {
			name := page.operation + " / the one read @ " + shape.name
			plan, text := json.RawMessage(nil), ""
			if generic {
				name = page.operation + " / the one read, generic plan @ " + shape.name
				plan, text = explainDismissedScaleGeneric(t, connection, name, statement, args, meInboxSettings)
			} else {
				plan, text = explainDismissedScale(t, connection, name, statement, args, meInboxSettings)
			}
			t.Logf("%s:\n%s", name, text)
			if !hold {
				continue
			}
			for _, index := range []string{"user_threads_dismissed", "user_agent_thread_dismissed_login_dismissed_at"} {
				if !planReadsIndex(t, plan, index) {
					t.Errorf("%s does not read %s:\n%s", name, index, text)
				}
				if read := planRowsOver(t, plan, index); read > float64(limit+1) {
					t.Errorf("%s takes %.0f rows of %s, want at most the page and the one past it, %d: the arm meets the walk before it is cut:\n%s",
						name, read, index, limit+1, text)
				}
			}
			for _, key := range planSortKeys(t, plan) {
				if strings.Contains(key, "dismissed_at") {
					t.Errorf("%s sorts on %s, which the state's indexes already answer in:\n%s", name, key, text)
				}
			}
		}
	}
}

// planRowsOver is how many rows the nodes of an analysed plan that read index answered, over every
// loop of each.
func planRowsOver(t *testing.T, planJSON json.RawMessage, index string) float64 {
	t.Helper()
	rows := float64(0)
	for _, node := range explaintest.Nodes(t, planJSON) {
		if node.Index == index {
			rows += node.Rows * max(node.Loops, 1)
		}
	}
	return rows
}

// explainDismissedScaleGeneric is explainDismissedScale under the generic plan: statement prepared
// on connection, and its EXECUTE explained under plan_cache_mode force_generic_plan and settings,
// each a statement the transaction runs first, inside a transaction that is rolled back. EXECUTE
// takes no bind parameter (explaintest.GenericPlan), so args go as literals pgx writes into it (the
// simple protocol), each cast to the type the prepare gave that parameter. The prepared statement
// is deallocated afterwards.
func explainDismissedScaleGeneric(
	t *testing.T, connection *pgx.Conn, name, statement string, args []any, settings ...string,
) (json.RawMessage, string) {
	t.Helper()
	ctx := context.Background()
	const prepared = "dismissed_scale_generic"
	described, err := connection.Prepare(ctx, prepared, statement)
	if err != nil {
		t.Fatalf("prepare %s: %v", name, err)
	}
	defer func() {
		if err := connection.Deallocate(ctx, prepared); err != nil {
			t.Errorf("deallocate %s: %v", name, err)
		}
	}()
	placeholders := make([]string, len(described.ParamOIDs))
	for index, oid := range described.ParamOIDs {
		var typed string
		if err := connection.QueryRow(ctx, `select $1::oid::regtype::text`, oid).Scan(&typed); err != nil {
			t.Fatalf("name the type of %s's $%d: %v", name, index+1, err)
		}
		placeholders[index] = fmt.Sprintf("$%d::%s", index+1, typed)
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the explain of %s: %v", name, err)
	}
	defer tx.Rollback(ctx)
	for _, setting := range append([]string{"set local plan_cache_mode = force_generic_plan"}, settings...) {
		if _, err := tx.Exec(ctx, setting); err != nil {
			t.Fatalf("%s before the explain of %s: %v", setting, name, err)
		}
	}
	execute := "execute " + prepared + "(" + strings.Join(placeholders, ", ") + ")"
	// A []byte argument - the acting credential, JSON (refs.VisibilityArg) - goes as its text: pgx
	// writes bytes as a bytea literal, which no json cast reads.
	literals := []any{pgx.QueryExecModeSimpleProtocol}
	for _, arg := range args {
		if raw, isBytes := arg.([]byte); isBytes {
			arg = string(raw)
		}
		literals = append(literals, arg)
	}
	var planJSON []byte
	if err := tx.QueryRow(ctx, "explain (analyze, buffers, format json) "+execute, literals...).Scan(&planJSON); err != nil {
		t.Fatalf("explain %s: %v", name, err)
	}
	rows, err := tx.Query(ctx, "explain (analyze, buffers) "+execute, literals...)
	if err != nil {
		t.Fatalf("explain %s as text: %v", name, err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain %s as text: %v", name, err)
	}
	return explaintest.OnePlan(t, planJSON), strings.Join(lines, "\n")
}

// planLoopsOver is the greatest number of times any one node of an analysed plan read relation:
// one for a table the read visits once, and a row count for one it visits per row.
func planLoopsOver(t *testing.T, planJSON json.RawMessage, relation string) float64 {
	t.Helper()
	var node struct {
		Relation string            `json:"Relation Name"`
		Loops    float64           `json:"Actual Loops"`
		Plans    []json.RawMessage `json:"Plans"`
	}
	if err := json.Unmarshal(planJSON, &node); err != nil {
		t.Fatalf("decode plan node: %v", err)
	}
	most := float64(0)
	if node.Relation == relation {
		most = node.Loops
	}
	for _, child := range node.Plans {
		if loops := planLoopsOver(t, child, relation); loops > most {
			most = loops
		}
	}
	return most
}

// explainDismissedScale runs one statement under EXPLAIN (ANALYZE, BUFFERS) twice: once as JSON,
// which the checks read, and once as text, which the report prints. Both run inside a transaction
// that is rolled back, so a write route's read takes no lock past the measurement, after settings,
// the statements the read's own transaction runs before it where it runs any (meInboxSettings).
func explainDismissedScale(
	t *testing.T, connection *pgx.Conn, name, statement string, args []any, settings ...string,
) (json.RawMessage, string) {
	t.Helper()
	ctx := context.Background()
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the explain of %s: %v", name, err)
	}
	defer tx.Rollback(ctx)
	for _, setting := range settings {
		if _, err := tx.Exec(ctx, setting); err != nil {
			t.Fatalf("%s before the explain of %s: %v", setting, name, err)
		}
	}
	var planJSON []byte
	if err := tx.QueryRow(ctx, "explain (analyze, buffers, format json) "+statement, args...).Scan(&planJSON); err != nil {
		t.Fatalf("explain %s: %v", name, err)
	}
	rows, err := tx.Query(ctx, "explain (analyze, buffers) "+statement, args...)
	if err != nil {
		t.Fatalf("explain %s as text: %v", name, err)
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan a plan line of %s: %v", name, err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain %s as text: %v", name, err)
	}
	return explaintest.OnePlan(t, planJSON), strings.Join(lines, "\n")
}

// dismissedScaleStatements is every statement the two write operations run, with the index each is
// held to: the dismissal's read of the rows it was given (api/inbox_dismiss.go) and the mark-all's
// two reads (api/inbox_read.go). The one read's Dismissed page is explained apart, as the handler
// builds it (explainDismissedScaleOneRead).
func dismissedScaleStatements(shape *dismissedScaleShape) []dismissedScaleExplained {
	agentArgs := func(extra ...any) []any {
		return append([]any{dismissedScaleLogin, dismissedScaleLogin, nil}, extra...)
	}
	mark := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []dismissedScaleExplained{
		// The two write routes sort the rows they matched by thread id, which is the lock order
		// markInboxReadThreads writes down and not an order any index owes them, so what each is
		// held to is that nothing sorts by the state's own order. The dismissal's read of the
		// rows a request named binds the ids alone and answers each row's own version: what the
		// page drew in them is the request's, written over that answer in the server rather
		// than sent to this read (readInboxDismissThreads).
		{"120-thread dismiss", "reply rows", inboxDismissNamedThreads,
			[]any{dismissedScaleLogin, shape.dismissRows},
			"user_threads_pkey", "user_threads", "replied_at", ""},
		{"mark all read", "reply rows", inboxReadReplyThreads,
			[]any{dismissedScaleLogin, "", nil, mark, nil, true, nil}, "user_threads_unread", "user_threads", "replied_at", ""},
		{"mark all read", "agent rows", inboxReadAgentThreads,
			append(agentArgs(), mark), "messages_direct_roots", "", "dismissed_at", "user_agent_thread_dismissed"},
	}
	return statements
}

// dismissedScaleControlAgentCount is the shape this change replaced, kept here and nowhere else:
// the Dismissed view's Agent count read over one recursive CTE of every one-off message the person
// ever sent, with one lateral per dismissed root scanning that whole CTE again. It is a14's
// negative control - put the per-root Agent count back and the Dismissed pages go far above their
// bound - so it is spelled out rather than built from the live constants, which no longer hold it.
var dismissedScaleControlAgentCount = `
	with recursive` + unreadDirectRepliesCTE + `,
	one_off_replies as (
		select dt.root_id, dt.session_id, dt.id as reply_id, dt.created_at, dt.author
		from direct_thread dt
		where dt.author->>'kind' = 'session'
		  and not exists (
			select 1 from message_deliveries d
			where d.message_id = dt.root_id and d.accepted_as = 'user_turn'
		  )
	),
	dismissed_roots as (
		select d.message_id as root_id, d.dismissed_at
		from user_agent_thread_dismissed d
		where d.login = $1
		order by d.dismissed_at desc, d.message_id desc
	),
	roots as (
		select dr.root_id, newest.session_id, newest.replied_at, dr.dismissed_at
		from dismissed_roots dr
		join lateral (
			select o.session_id, max(o.created_at) as replied_at
			from one_off_replies o
			where o.root_id = dr.root_id
			group by o.session_id
		) newest on true
		where dr.dismissed_at >= newest.replied_at
	)
	select count(*) from roots`

// dismissedScaleNegativeControl runs that control at the pile's own size and holds it to being
// red: a read the Dismissed page would have to make before it could answer, far above the 250 ms
// every one of those pages is held to. A control that came in under the bound would mean this
// measurement cannot tell the two shapes apart, and so proves nothing about the one it passed.
func dismissedScaleNegativeControl(t *testing.T, shape *dismissedScaleShape) {
	t.Helper()
	ctx := context.Background()
	var planJSON []byte
	if err := shape.database.Pool.QueryRow(ctx,
		"explain (analyze, buffers, format json) "+dismissedScaleControlAgentCount,
		dismissedScaleLogin, dismissedScaleLogin, nil).Scan(&planJSON); err != nil {
		t.Fatalf("explain the negative control: %v", err)
	}
	var answered []struct {
		Execution float64         `json:"Execution Time"`
		Plan      json.RawMessage `json:"Plan"`
	}
	if err := json.Unmarshal(planJSON, &answered); err != nil || len(answered) != 1 {
		t.Fatalf("decode the control's plan: %v\n%s", err, planJSON)
	}
	took := time.Duration(answered[0].Execution * float64(time.Millisecond))
	t.Logf("negative control (the per-root Agent count over the recursive thread query) @ %s: %.1f ms\n%s",
		shape.name, msOf(took), planJSON)
	if took <= dismissedScaleTimeBound {
		t.Errorf("the control answered in %.1f ms, inside the %s bound: the measurement cannot tell the shape it replaced from the one it holds, so the bounds above prove nothing",
			msOf(took), dismissedScaleTimeBound)
	}
}
