package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// The plan TestScanRowsReadsEveryScanNodeOfAPlan reads (testdata/explain-scan-kinds.json) was
// captured from the pod's PostgreSQL 18. The database was one that seedDismissedNarrowing made,
// kept for the capture. The command was:
//
//	psql "<the seeded test database>" -X -A -t -q -c "set enable_seqscan = off" -c "explain (analyze, format json) with marks as materialized (select thread_id, read_through from user_threads where login = 'alice' and dismissed_at > now() - interval '30 minutes') select m.thread_id, (select count(*) from comments c where c.reply_to = m.thread_id) as replies, (select count(*) from marks) as total from marks m"
//
// The two plans of the Dismissed page's statement are not kept as files. The narrowing's page
// test explains both live, with 1,000 dismissed threads and with 250, and sums them with
// scanRowsOf.

// The seed of the narrowing's page test: a pile of each kind of dismissed thread, built with the
// scale seed's own helpers (inbox_dismissed_scale_seed_test.go). 960 Reply threads are dismissed a
// minute apart and 40 Agent roots ten minutes apart, so a page holds both kinds. The ten threads
// at places 196 to 205 of the order are then given one moment, as one bulk Dismiss gives every
// thread it takes, so that tie lies across the first page's edge.
const (
	dismissedNarrowingReplies  = 960
	dismissedNarrowingAgents   = 40
	dismissedNarrowingTieFirst = 196
	dismissedNarrowingTieLast  = 205
	// dismissedNarrowingFewer is the pile the plan's rows are read again at: all 40 Agent roots and
	// the 210 newest Reply threads, so each arm of the cut takes as many rows as it took at 1,000 -
	// the Reply arm the page and the one past it, the Agent arm its 40 - and a statement that reads
	// its page and no more reads no fewer rows with 250 than with 1,000.
	dismissedNarrowingFewer = 250
)

// c-dismissed-page: GET /api/v1/me/inbox?state=dismissed answers at most one page of 200 however
// many threads the person has dismissed. With no limit it answers 200, as it does with limit=200,
// newest dismissal first, with a next_cursor; a limit above 200 is refused by its key; the cursor
// walks every dismissed thread of both kinds once, in order, a tie across the first page's edge
// included, and the last page names no cursor; each page's total is the page's own entries. The
// statement of the first page reads its pile through user_threads_dismissed and never scans
// user_threads, and it reads no more rows with 1,000 dismissed threads than with 250, because each
// arm is cut to the page before the walk (meInboxDismissedCut) and R2's gate is read for the page's
// entries alone (meInboxGateReads). The rows it reads are the actual rows of every scan node of its
// plan (scanRowsOf).
func TestTheDismissedNarrowingAnswersAtMostOnePage(t *testing.T) {
	shape := seedDismissedNarrowing(t)
	want := dismissedNarrowingOrder(t, shape)
	if len(want) != dismissedNarrowingReplies+dismissedNarrowingAgents {
		t.Fatalf("the seed dismissed %d threads, want %d", len(want), dismissedNarrowingReplies+dismissedNarrowingAgents)
	}

	dismissed := url.Values{"state": {myRepliesStateDismissed}}
	for _, query := range []url.Values{dismissed, {"state": {myRepliesStateDismissed}, "limit": {"200"}}} {
		answer := readMeInboxAs(t, shape.handler, dismissedScaleLogin, query)
		got := meInboxNodes(answer)
		if len(got) != myRepliesDismissedPage || answer.Limit != myRepliesDismissedPage {
			t.Fatalf("?%s answers %d entries under limit %d, want %d under %d",
				query.Encode(), len(got), answer.Limit, myRepliesDismissedPage, myRepliesDismissedPage)
		}
		if !slices.Equal(got, want[:myRepliesDismissedPage]) {
			t.Fatalf("?%s answers %v, want the %d newest dismissals %v", query.Encode(), got, myRepliesDismissedPage, want[:myRepliesDismissedPage])
		}
		if answer.Total != len(got) || answer.NextCursor == "" {
			t.Fatalf("?%s answers total %d and cursor %q, want the page's %d and a cursor", query.Encode(), answer.Total, answer.NextCursor, len(got))
		}
	}

	refused := meInboxRequestAs(t, shape.handler, context.Background(), dismissedScaleLogin,
		url.Values{"state": {myRepliesStateDismissed}, "limit": {"500"}})
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), `"code":"INVALID_QUERY"`) ||
		!strings.Contains(refused.Body.String(), "limit") {
		t.Fatalf("?state=dismissed&limit=500: status=%d body=%s, want 400 INVALID_QUERY naming limit", refused.Code, refused.Body.String())
	}

	read := []string{}
	query := url.Values{"state": {myRepliesStateDismissed}}
	for page := 1; ; page++ {
		if page > len(want) {
			t.Fatalf("the Dismissed view read %d threads and still names a cursor", len(read))
		}
		answer := readMeInboxAs(t, shape.handler, dismissedScaleLogin, query)
		if answer.Total != len(answer.Entries) || answer.Total == len(want) {
			t.Fatalf("page %d answers total %d over %d entries, want the page it answers and never the pile of %d",
				page, answer.Total, len(answer.Entries), len(want))
		}
		read = append(read, meInboxNodes(answer)...)
		if answer.NextCursor == "" {
			break
		}
		query.Set("cursor", answer.NextCursor)
	}
	if !slices.Equal(read, want) {
		t.Fatalf("the cursor walk read %d threads, want every one of the %d once, newest dismissal first and then by id: %v",
			len(read), len(want), read)
	}

	all := explainDismissedNarrowing(t, shape, "the first page with 1,000 dismissed")
	putBackOldestReplies(t, shape, len(want)-dismissedNarrowingFewer)
	if fewer := dismissedNarrowingOrder(t, shape); len(fewer) != dismissedNarrowingFewer {
		t.Fatalf("after the put-back %d threads stand dismissed, want %d", len(fewer), dismissedNarrowingFewer)
	}
	few := explainDismissedNarrowing(t, shape, "the first page with 250 dismissed")
	t.Logf("the first page's statement reads %.0f rows with %d dismissed and %.0f with %d", all, len(want), few, dismissedNarrowingFewer)
	if all > few {
		t.Fatalf("the first page's statement reads %.0f rows with %d dismissed threads and %.0f with %d, want no more with the larger pile: the arms meet the walk before they are cut",
			all, len(want), few, dismissedNarrowingFewer)
	}
}

// seedDismissedNarrowing seeds the person's two piles, puts the tie across the first page's edge,
// and gives the planner its statistics, as the scale seed does.
func seedDismissedNarrowing(t *testing.T) *dismissedScaleShape {
	t.Helper()
	handler, database, _ := newTestServer(t, testServerOptions{})
	issue := createInteractionIssue(t, handler, "SCALE", "A pile of dismissed threads", "A spec")
	shape := &dismissedScaleShape{
		name: "one page", handler: handler, database: database, issue: issue.Key,
		dismissedReplies: dismissedNarrowingReplies, dismissedAgents: dismissedNarrowingAgents,
	}
	dismissedScaleCommentPairs(t, shape, dismissedScalePileLabel, dismissedNarrowingReplies, dismissedScalePileAge)
	markDismissedScaleThreads(t, shape, dismissedNarrowingReplies)
	dismissedScaleMessagePairs(t, shape, dismissedScaleAgentPileLabel, dismissedNarrowingAgents, dismissedScaleAgentAge)
	markDismissedScaleAgentThreads(t, shape, dismissedNarrowingAgents)

	ctx := context.Background()
	var tied, agents int
	if err := database.Pool.QueryRow(ctx, `
		with ordered as (
			select kind, id, at, row_number() over (order by at desc, id::uuid desc) as n
			from (`+dismissedNarrowingMarks+`) marked
		), tie as (
			select at from ordered where n = $2
		), threads as (
			update user_threads u set dismissed_at = (select at from tie)
			from ordered o
			where o.n between $2 and $3 and o.kind <> 'message' and u.login = $1 and u.thread_id = o.id::uuid
			returning 1
		), roots as (
			update user_agent_thread_dismissed d set dismissed_at = (select at from tie)
			from ordered o
			where o.n between $2 and $3 and o.kind = 'message' and d.login = $1 and d.message_id = o.id::uuid
			returning 1
		)
		select (select count(*) from threads) + (select count(*) from roots), (select count(*) from roots)
	`, dismissedScaleLogin, dismissedNarrowingTieFirst, dismissedNarrowingTieLast).Scan(&tied, &agents); err != nil {
		t.Fatalf("tie the threads across the first page's edge: %v", err)
	}
	if tied != dismissedNarrowingTieLast-dismissedNarrowingTieFirst+1 || agents == 0 || agents == tied {
		t.Fatalf("tied %d threads, %d of them Agent roots, want %d of both kinds", tied, agents, dismissedNarrowingTieLast-dismissedNarrowingTieFirst+1)
	}
	if _, err := database.Pool.Exec(ctx, "vacuum analyze"); err != nil {
		t.Fatalf("analyze the seed: %v", err)
	}
	return shape
}

// dismissedNarrowingMarks is every dismissal of the person $1 holds, of both kinds, as the node the
// one read answers for it: a Reply thread's root by its own kind, an Agent root as a message.
const dismissedNarrowingMarks = `
	select dismissed_at as at, kind, thread_id::text as id from user_threads
	where login = $1 and dismissed_at is not null
	union all
	select dismissed_at, 'message', message_id::text from user_agent_thread_dismissed where login = $1`

// dismissedNarrowingOrder is every thread the person has dismissed, as "kind:id", in the order the
// Dismissed view answers them: newest dismissal first, then the root's id as a uuid, descending.
func dismissedNarrowingOrder(t *testing.T, shape *dismissedScaleShape) []string {
	t.Helper()
	return dismissedScaleStrings(t, shape, `
		select kind || ':' || id from (`+dismissedNarrowingMarks+`) marked
		order by at desc, id::uuid desc`, dismissedScaleLogin)
}

// putBackOldestReplies takes the dismissal off the count oldest dismissed Reply threads.
func putBackOldestReplies(t *testing.T, shape *dismissedScaleShape, count int) {
	t.Helper()
	tag, err := shape.database.Pool.Exec(context.Background(), `
		update user_threads set dismissed_at = null
		where login = $1 and thread_id in (
			select thread_id from user_threads where login = $1 and dismissed_at is not null
			order by dismissed_at, thread_id limit $2
		)`, dismissedScaleLogin, count)
	if err != nil {
		t.Fatalf("put back %d threads: %v", count, err)
	}
	if tag.RowsAffected() != int64(count) {
		t.Fatalf("put back %d threads, want %d", tag.RowsAffected(), count)
	}
}

// explainDismissedNarrowing explains the first page's statement as the handler builds it for
// GET /api/v1/me/inbox?state=dismissed with no limit, under the read's own settings and so its
// generic plan (meInboxSettings), holds the plan to reading the pile through user_threads_dismissed
// and never scanning user_threads, and answers the rows it reads (scanRowsOf).
func explainDismissedNarrowing(t *testing.T, shape *dismissedScaleShape, name string) float64 {
	t.Helper()
	ctx := context.Background()
	request, err := parseMeInboxRequest(httptest.NewRequest(http.MethodGet, "/api/v1/me/inbox?state="+myRepliesStateDismissed, nil))
	if err != nil {
		t.Fatalf("read the first page's request: %v", err)
	}
	var horizon int64
	if err := shape.database.Pool.QueryRow(ctx, `select coalesce(max(id), 0) from events`).Scan(&horizon); err != nil {
		t.Fatalf("read the event log's horizon: %v", err)
	}
	statement, args, err := meInboxStatement(model.Actor{Kind: "user", ID: dismissedScaleLogin}, horizon, request, false, false, nil)
	if err != nil {
		t.Fatalf("build %s: %v", name, err)
	}
	connection, err := shape.database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection for %s: %v", name, err)
	}
	defer connection.Release()
	plan, text := explainDismissedScaleGeneric(t, connection.Conn(), name, statement, args, meInboxSettings...)
	if !planReadsIndex(t, plan, "user_threads_dismissed") {
		t.Errorf("%s does not read user_threads_dismissed:\n%s", name, text)
	}
	if explaintest.SeqScansRelation(t, plan, "user_threads") {
		t.Errorf("%s scans user_threads:\n%s", name, text)
	}
	return scanRowsOf(t, plan)
}

// scanRowsOf is how many rows the scan nodes of an analysed plan read: the actual rows of every
// node whose type is a scan - of a table, an index, a bitmap, a CTE, a subquery, a function - over
// every loop of each, at any depth, an InitPlan's and a SubPlan's included.
func scanRowsOf(t testing.TB, plan json.RawMessage) float64 {
	t.Helper()
	rows := 0.0
	for _, node := range explaintest.Nodes(t, plan) {
		if strings.HasSuffix(node.Type, " Scan") {
			rows += node.Rows * node.Loops
		}
	}
	return math.Round(rows)
}

// F1: scanRowsOf over a plan PostgreSQL 18 wrote (the command at the top of this file). The plan
// holds every kind of node the walk must reach: an InitPlan and a SubPlan under the top node, a
// CTE scan, a bitmap heap scan over a bitmap index scan, and an index-only scan. The SubPlan runs
// once a row, so its loops count. The count was read off the fixture by hand, scan by scan:
//
//	CTE Scan on marks m                                28 rows x 1 loop    28
//	Bitmap Heap Scan on user_threads (CTE marks)       28 rows x 1 loop    28
//	Bitmap Index Scan on user_threads_dismissed        28 rows x 1 loop    28
//	CTE Scan on marks (InitPlan 3)                     28 rows x 1 loop    28
//	Index Only Scan using comments_reply_to (SubPlan)   1 row  x 28 loops  28
//
// The two Aggregate nodes read no rows of their own, so they are not counted.
func TestScanRowsReadsEveryScanNodeOfAPlan(t *testing.T) {
	for _, fixture := range []struct {
		file string
		rows float64
	}{
		{"explain-scan-kinds.json", 140},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", fixture.file))
		if err != nil {
			t.Fatalf("read %s: %v", fixture.file, err)
		}
		if rows := scanRowsOf(t, explaintest.OnePlan(t, raw)); rows != fixture.rows {
			t.Errorf("%s: scanRowsOf = %.0f, want %.0f", fixture.file, rows, fixture.rows)
		}
	}
}
