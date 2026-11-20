package delivery

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// linkBackfillMigration is the migration that owes, once, the read of each pull request a link
// names whose head row Dispatch does not hold (20261120171308_pull_request_reads_owed_link_backfill).
const linkBackfillMigration = 20261120171308

// runLinkBackfill applies the backfill as the runner applies it: its version is forgotten and the
// store migrates, which applies that version alone, in one transaction with its record in
// schema_migrations (store.applyMigrationOnce), as a deployment's boot applies it once.
func runLinkBackfill(t *testing.T, ctx context.Context, database *store.Store) {
	t.Helper()
	if _, err := database.Pool.Exec(ctx, `delete from schema_migrations where version = $1`, linkBackfillMigration); err != nil {
		t.Fatalf("forget version %d: %v", linkBackfillMigration, err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("apply version %d: %v", linkBackfillMigration, err)
	}
}

// owedLinkReads is every owed read of the table as `repo#number reason`, with ` link` where the
// row's linked_revision is its revision, in order.
func owedLinkReads(t *testing.T, ctx context.Context, pool *store.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		select repo || '#' || number || ' ' || reason || case when linked_revision = revision then ' link' else '' end
		from pull_request_reads_owed order by repo, number`)
	if err != nil {
		t.Fatalf("read the owed reads: %v", err)
	}
	defer rows.Close()
	var owed []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		owed = append(owed, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(owed)
	return owed
}

// refusalReads is how many times the reconcile has read the refusal owed for acme/widgets#5071.
func refusalReads(t *testing.T, ctx context.Context, pool *store.Pool) int {
	t.Helper()
	var reads int
	if err := pool.QueryRow(ctx, `select reads from pull_request_reads_owed where repo = 'acme/widgets' and number = 5071`).Scan(&reads); err != nil {
		t.Fatalf("read the refusal of acme/widgets#5071: %v", err)
	}
	return reads
}

// headRows is how many head rows of repo's pull request number are stored.
func headRows(t *testing.T, ctx context.Context, pool *store.Pool, repo string, number int) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `select count(*) from pull_request_heads where repo = $1 and number = $2`, repo, number).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// backfillLinks seeds the issues and links every case below reads, and the rows they stand
// beside: a head row of #5064, of #5065 (the link spells it in other letters) and of #5066 (the
// link names the repository by the name it had before a rename to acme/widgets).
func backfillLinks(t *testing.T, ctx context.Context, pool *store.Pool) map[string][]string {
	t.Helper()
	links := map[string][]string{
		"ATTR-901": {"https://github.com/acme/widgets/pull/5063"},
		"ATTR-902": {"https://github.com/acme/widgets/pull/5064"},
		"ATTR-903": {"https://GITHUB.com/ACME/Widgets/PULL/5065"},
		"ATTR-904": {"https://github.com/oldco/widgets/pull/5066", "https://github.com/oldco/widgets/pull/5067"},
		"ATTR-905": {"https://github.com/acme/widgets/issues/5068"},
		"ATTR-906": {"https://github.com/../widgets/pull/5069", "https://github.com/acme/widgets/pull/05070", "https://example.com/acme/widgets/pull/5073"},
		"ATTR-907": {"https://github.com/acme/widgets/pull/5071"},
		"ATTR-908": {"https://github.com/acme/widgets/pull/5072"},
		"ATTR-909": {"https://github.com/Acme/Widgets/pull/5072"},
	}
	keys := make([]string, 0, len(links))
	for key := range links {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	seedAttributionIssues(t, ctx, pool, keys, links)
	if _, err := pool.Exec(ctx, `
		insert into repo_projects (repo, project, created_by, is_default, renamed_to)
		values ('oldco/widgets', $1, '{"kind":"system","id":"delivery-test"}'::jsonb, false, 'acme/widgets')
		on conflict (repo) do update set renamed_to = excluded.renamed_to
	`, deliveryProject); err != nil {
		t.Fatalf("record oldco/widgets as renamed to acme/widgets: %v", err)
	}
	for _, number := range []int{5064, 5065, 5066} {
		if err := UpsertPullRequestHead(ctx, pool, PullRequestHead{
			Repo: "acme/widgets", Number: number, HeadSHA: fakeSHA("5", number), HeadRef: "topic", BaseRef: "main", State: "merged",
		}); err != nil {
			t.Fatalf("store the head of acme/widgets#%d: %v", number, err)
		}
	}
	return links
}

// oweRefusal5071 owes a refusal of acme/widgets#5071, read three times: a read the backfill finds
// owed already. It is owed after any pass the test runs before the backfill, since a pass would
// read it and settle it, GitHub answering that no such pull request is there.
func oweRefusal5071(t *testing.T, ctx context.Context, pool *store.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		insert into pull_request_reads_owed (repo, number, reason, reads, refused_as, refused_since)
		values ('acme/widgets', 5071, 'refusal', 3, 'pull_request_not_found', now() - interval '1 hour')
	`); err != nil {
		t.Fatalf("owe a refusal of acme/widgets#5071: %v", err)
	}
}

// goRuleOwes is what a link write of this release owes for links, through the Go rule the
// migration's SQL spells out (linkedPullRequest, owedName and oweLinkedRead's test of a stored
// head), for the /pull/ form alone, with no read owed where one is owed already, and one read of
// two links of one pull request, under the spelling first in byte order: the control that the
// migration and the link write hold a link to one rule.
func goRuleOwes(t *testing.T, ctx context.Context, pool *store.Pool, links map[string][]string) []string {
	t.Helper()
	type pull struct {
		repo   string
		number int
	}
	first := map[string]pull{}
	for _, urls := range links {
		for _, url := range urls {
			repo, number, ok := linkedPullRequest(url)
			if !ok || !strings.Contains(strings.ToLower(url), "/pull/") {
				continue
			}
			name, err := owedName(ctx, pool, repo, number)
			if err != nil {
				t.Fatalf("the owed name of %s: %v", url, err)
			}
			key := fmt.Sprintf("%s#%d", strings.ToLower(name), number)
			if seen, ok := first[key]; !ok || name < seen.repo {
				first[key] = pull{repo: name, number: number}
			}
		}
	}
	var owed []string
	for _, linked := range first {
		var held, owedAlready bool
		if err := pool.QueryRow(ctx, `
			select exists (select 1 from pull_request_heads h where h.number = $2 and lower(`+refs.CanonicalRepoSQL("h.repo")+`) = lower($1)),
				exists (select 1 from pull_request_reads_owed o where lower(o.repo) = lower($1) and o.number = $2)
		`, linked.repo, linked.number).Scan(&held, &owedAlready); err != nil {
			t.Fatal(err)
		}
		if !held && !owedAlready {
			owed = append(owed, fmt.Sprintf("%s#%d failure link", linked.repo, linked.number))
		}
	}
	slices.Sort(owed)
	return owed
}

// A link written before this release owes no read of the pull request it links, so a merged pull
// request with no event since, acme/widgets#5063 here, keeps no head row however many passes run,
// and the issue's checks read no head (red). The backfill owes, once, the read of each such link
// as a link write of this release owes it, and the reconcile's next pass reads it from GitHub and
// writes its head (green): #5063 as GitHub holds it, merged, and #5067, which the link names by
// the repository's name before its rename, under acme/widgets. A link whose pull request has a
// head row owes nothing (control): #5064, #5065 spelled in other letters, and #5066 named by the
// old name; nor does an /issues/ link, a link of no repository GitHub can have, of another host or
// with a zero before its number, nor a pull request whose read is owed already, which keeps its
// refusal and its three reads. Two links of #5072 in two letter cases owe one read. The set the
// migration owes is the set the Go rule of a link write owes for the same links, read before the
// backfill (goRuleOwes).
func TestTheLinkBackfillOwesTheReadOfEachLinkedPullRequestWithNoHead(t *testing.T) {
	fastPageRetries(t)
	database := storetest.Open(t)
	pool, ctx := database.Pool, t.Context()
	if err := PrepareAttribution(ctx, pool); err != nil {
		t.Fatal(err)
	}
	links := backfillLinks(t, ctx, pool)
	fake := newFakeGitHub(t)
	github := serveGitHubPulls(t, fake, "acme/widgets")
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for _, number := range []int{5063, 5067, 5072} {
		github.set("acme/widgets", number, "open", fakeSHA("6", number), "topic", "", at)
	}
	github.merge(5063)
	client := fake.newTestClient()

	NewReconcile(pool, client, events.NewBroker()).runOnce(ctx)
	if rows := headRows(t, ctx, pool, "acme/widgets", 5063); rows != 0 {
		t.Fatalf("red: before the backfill a pass stored %d head rows of acme/widgets#5063, which a link written before this release names; want none", rows)
	}

	oweRefusal5071(t, ctx, pool)
	want := goRuleOwes(t, ctx, pool, links)
	readsBefore := refusalReads(t, ctx, pool)
	runLinkBackfill(t, ctx, database)
	owed := owedLinkReads(t, ctx, pool)
	backfilled := slices.DeleteFunc(slices.Clone(owed), func(row string) bool { return !strings.HasSuffix(row, " link") })
	t.Logf("owed after the backfill: %v", owed)
	if !slices.Equal(backfilled, want) {
		t.Fatalf("the backfill owes %v; the Go rule of a link write owes %v for the same links", backfilled, want)
	}
	if !slices.Equal(backfilled, []string{"Acme/Widgets#5072 failure link", "acme/widgets#5063 failure link", "acme/widgets#5067 failure link"}) {
		t.Fatalf("the backfill owes %v, want acme/widgets#5063, #5067 and #5072 (as Acme/Widgets, first in byte order) alone", backfilled)
	}
	if !slices.Contains(owed, "acme/widgets#5071 refusal") {
		t.Fatalf("the owed refusal of acme/widgets#5071 is %v after the backfill, want it left as it was", owed)
	}
	if reads := refusalReads(t, ctx, pool); reads != readsBefore {
		t.Fatalf("the refusal of #5071 has %d reads after the backfill, want the %d it had before", reads, readsBefore)
	}

	NewReconcile(pool, client, events.NewBroker()).runOnce(ctx)
	got := readHead(t, ctx, pool, "acme/widgets", 5063)
	if got.HeadSHA != fakeSHA("6", 5063) || got.State != "merged" {
		t.Fatalf("green: after the backfill's pass acme/widgets#5063 = head %s state %s, want GitHub's %s merged", got.HeadSHA, got.State, fakeSHA("6", 5063))
	}
	if rows := headRows(t, ctx, pool, "acme/widgets", 5067); rows != 1 {
		t.Fatalf("after the pass acme/widgets#5067, linked by the name before the rename, has %d head rows, want 1", rows)
	}
	for _, number := range []int{5063, 5067} {
		if n := owedCount(t, ctx, pool, "acme/widgets", number); n != 0 {
			t.Errorf("acme/widgets#%d is still owed after a pass GitHub answered", number)
		}
	}
}

// The way back deletes each owe the backfill wrote that no later owe changed, and nothing else:
// #5063's owe as the backfill left it goes; #5067's, which a link write of this release owed
// again after the backfill, stays, and so does #5072's, which an event owed again; the refusal of
// #5071 owed before the backfill stays. The control is the way back run with no record of the
// migration in schema_migrations, which deletes nothing.
func TestTheLinkBackfillsWayBackDeletesOnlyTheOwesItWrote(t *testing.T) {
	database := storetest.Open(t)
	pool, ctx := database.Pool, t.Context()
	if err := PrepareAttribution(ctx, pool); err != nil {
		t.Fatal(err)
	}
	backfillLinks(t, ctx, pool)
	oweRefusal5071(t, ctx, pool)
	runLinkBackfill(t, ctx, database)
	down, err := os.ReadFile("../store/migrations/20261120171308_pull_request_reads_owed_link_backfill.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := OweLinkedPullRequestReads(ctx, tx, []string{"https://github.com/acme/widgets/pull/5067"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owePullRequestRead(ctx, pool, "Acme/Widgets", 5072, owedFailure, ""); err != nil {
		t.Fatal(err)
	}
	before := owedLinkReads(t, ctx, pool)

	recorded, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorded.Exec(ctx, `delete from schema_migrations where version = $1`, linkBackfillMigration); err != nil {
		t.Fatal(err)
	}
	if _, err := recorded.Exec(ctx, string(down)); err != nil {
		t.Fatalf("the way back with no record: %v", err)
	}
	var left int
	if err := recorded.QueryRow(ctx, `select count(*) from pull_request_reads_owed`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if err := recorded.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if left != len(before) {
		t.Fatalf("control: the way back with no record of the migration left %d of %d owed reads, want every one", left, len(before))
	}

	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("the way back: %v", err)
	}
	after := owedLinkReads(t, ctx, pool)
	t.Logf("owed before the way back: %v; after: %v", before, after)
	if want := []string{"Acme/Widgets#5072 failure", "acme/widgets#5067 failure link", "acme/widgets#5071 refusal"}; !slices.Equal(after, want) {
		t.Fatalf("after the way back the owed reads are %v, want %v", after, want)
	}
}
