package delivery

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/explaintest"
)

// The readers of the timeline plan the statement for the pull requests of each call
// (attributedIssues). A connection that has run the statement prepares it, and from its sixth run
// Postgres may run it by a generic plan, priced for an array of 10 keys, whose joins over the
// keys' rows are nested loops: a window of 3,000 pull requests took 1.50 s by it and 0.23 s by a
// plan of its own keys. On a pool of one connection, ten reads of ten pull requests run the
// prepared statement; the window's read and the waiting read of 300 pull requests after them run
// no plan of it: its counts of generic and custom plans in pg_prepared_statements stay where the
// ten reads left them. The control in the same test: the ten reads move those counts by ten, so
// the counts read are the statement's.
func TestTheTimelinesReadersPlanTheStatementForTheirOwnPullRequests(t *testing.T) {
	pool, ctx := deliveryTestPool(t)
	storeTextsRows(t, ctx, pool, 1, 300)
	merged := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		insert into delivery_pull_requests (repo, number, title, url, author, created_at, merged_at)
		select h.repo, h.number, h.title, h.url, 'octocat', $1::timestamptz - interval '1 hour', $1::timestamptz + make_interval(secs => h.number)
		from pull_request_heads h where h.repo = 'acme/widgets'
	`, merged); err != nil {
		t.Fatalf("store the timeline rows: %v", err)
	}
	config := pool.Config()
	config.MaxConns, config.MinConns, config.MinIdleConns = 1, 0, 0
	opened, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open a pool of one connection: %v", err)
	}
	t.Cleanup(opened.Close)
	single := store.NewPool(opened)
	statement := attributionStatementsFor(readingSchema(t, ctx, pool)).pullRequestIssues
	plans := func() (generic, custom int64) {
		t.Helper()
		if err := single.QueryRow(ctx, `select coalesce(sum(generic_plans), 0), coalesce(sum(custom_plans), 0)
			from pg_prepared_statements where statement = $1`, statement).Scan(&generic, &custom); err != nil {
			t.Fatalf("read the plans of the prepared statement: %v", err)
		}
		return generic, custom
	}

	ten := make([]PullRequestKey, 10)
	for i := range ten {
		ten[i] = PullRequestKey{Repo: "acme/widgets", Number: i + 1}
	}
	for range 10 {
		if _, err := PullRequestIssues(ctx, single, ten); err != nil {
			t.Fatal(err)
		}
	}
	generic, custom := plans()
	t.Logf("ten reads of ten pull requests: %d generic and %d custom plans of the prepared statement", generic, custom)
	if generic+custom != 10 {
		t.Fatalf("control: ten reads of ten pull requests ran %d generic and %d custom plans of the prepared statement, want ten in all: the counts read are not the statement's", generic, custom)
	}

	at := merged.Add(time.Hour)
	window, err := ListPullRequestsInWindow(ctx, single, merged, at)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := ListPullRequestsWaitingAt(ctx, single, "acme/widgets", Production{JobNames: []string{"deploy"}}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 300 || len(waiting) != 300 {
		t.Fatalf("the window lists %d pull requests and the waiting read %d, want 300 each", len(window), len(waiting))
	}
	afterGeneric, afterCustom := plans()
	if afterGeneric != generic || afterCustom != custom {
		t.Errorf("the window's read and the waiting read of 300 pull requests ran %d generic and %d custom plans of the connection's prepared statement, want none: each is planned for its own pull requests",
			afterGeneric-generic, afterCustom-custom)
	}
}

// The statement reads the renamed repositories once for each pull request it judges, however many
// there are (PullRequestLinksSQL). Over 20 pull requests with texts, the loops of the nodes that
// read repo_projects add up to the same count with 2 renamed repositories and with 41. Where each
// renamed name's canonical name was compared for every pull request, that count grew by 20 for
// each renamed repository, and the statement over 10 pull requests took 996 ms with 500 of them.
// The controls in the same test: the plan reads repo_projects, the statement answers each of the
// 20, and the links of a pull request still hold its forms under the name renamed to its
// repository.
func TestTheStatementReadsTheRenamesOnceForEachPullRequestWhateverTheirNumber(t *testing.T) {
	pool, ctx := deliveryTestPool(t)
	storeTextsRows(t, ctx, pool, 1, 20)
	exec := func(what, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	exec("project", `insert into projects (key, name, default_assignee) values ('RNMD', 'RNMD', 'alice') on conflict do nothing`)
	exec("the old name of acme/widgets", `insert into repo_projects (repo, project, created_by, renamed_to)
		values ('oldco/widgets', 'RNMD', '{"kind":"system","id":"test"}', 'acme/widgets')`)
	renames := func(first, last int) {
		t.Helper()
		exec("renamed repositories", `insert into repo_projects (repo, project, created_by, renamed_to)
			select 'gone/r' || g, 'RNMD', '{"kind":"system","id":"test"}', 'kept/r' || g from generate_series($1::int, $2::int) g`, first, last)
	}
	statement := attributionStatementsFor(readingSchema(t, ctx, pool)).pullRequestIssues
	repos := make([]string, 20)
	numbers := make([]int, 20)
	for i := range repos {
		repos[i], numbers[i] = "acme/widgets", i+1
	}
	loops := func(what string) float64 {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(ctx, `explain (analyze, format json) `+statement, repos, numbers).Scan(&raw); err != nil {
			t.Fatalf("explain the statement %s: %v", what, err)
		}
		plan := explaintest.OnePlan(t, raw)
		var sum float64
		for _, node := range explaintest.Nodes(t, plan) {
			if node.Relation == "repo_projects" {
				sum += node.Loops
			}
		}
		if root := explaintest.Nodes(t, plan)[0]; root.Rows != 20 {
			t.Fatalf("the statement %s answers %v pull requests, want the 20", what, root.Rows)
		}
		return sum
	}

	renames(1, 1)
	few := loops("with 2 renamed repositories")
	renames(2, 40)
	many := loops("with 41 renamed repositories")
	t.Logf("loops of the nodes that read repo_projects: %v with 2 renamed repositories, %v with 41", few, many)
	if few == 0 {
		t.Fatalf("control: the plan reads repo_projects in no node, so its loops say nothing")
	}
	if many != few {
		t.Errorf("the nodes that read repo_projects ran %v loops with 41 renamed repositories and %v with 2, want the same: the statement reads the renames once for each pull request", many, few)
	}

	var links []string
	if err := pool.QueryRow(ctx, `select `+PullRequestLinksSQL("$1::text", "$2::int"), "acme/widgets", 7).Scan(&links); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprint([]string{"https://github.com/acme/widgets/issues/7", "https://github.com/acme/widgets/pull/7",
		"https://github.com/oldco/widgets/issues/7", "https://github.com/oldco/widgets/pull/7"})
	if got := fmt.Sprint(slices.Sorted(slices.Values(links))); got != want {
		t.Errorf("the links of acme/widgets#7 are %s, want %s", got, want)
	}
}
