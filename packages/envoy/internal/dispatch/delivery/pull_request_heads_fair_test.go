package delivery

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestAHealthyRowBehindAFullBatchOfFailingRowsIsReadWithinTwoPasses: a full batch of open pull
// requests (PullRequestHeadBatch) whose rows no read has reached sit ahead of #70 in the
// reconcile's order, and GitHub answers each of their reads 502 at every pass. #70 is a row of
// the release before this one: read by that release an hour ago, with no base revision stored,
// so the check states settler compares none of its candidates until a read stores one. GitHub
// answers #70's read. The reconcile tries each due row within ceil(n/PullRequestHeadBatch) passes
// of n due rows, the least lately tried first, whatever earlier tries answered: so the second
// pass reads #70 and stores its base revision, and the failed reads leave each failing row as it
// was, unread and with no base revision, since a failed read is evidence of nothing. The third
// pass tries the one failing row the second left, before any row the second pass tried.
func TestAHealthyRowBehindAFullBatchOfFailingRowsIsReadWithinTwoPasses(t *testing.T) {
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	issue := seedHeadIssue(t, ctx, pool, 11)
	const (
		healthy  = 70
		first    = 1000
		head     = "7070707070707070707070707070707070707070"
		baseSHA  = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
		failures = PullRequestHeadBatch
	)
	var mu sync.Mutex
	pass := 0
	triedIn := map[int]map[int]bool{}
	fake := newFakeGitHub(t)
	fake.handle("GET /repos/acme/widgets/pulls/{number}", func(w http.ResponseWriter, r *http.Request) {
		number, err := strconv.Atoi(r.PathValue("number"))
		if err != nil {
			t.Errorf("a read of pull request %q", r.PathValue("number"))
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		if triedIn[number] == nil {
			triedIn[number] = map[int]bool{}
		}
		triedIn[number][pass] = true
		mu.Unlock()
		if number != healthy {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"message":"Server Error"}`)
			return
		}
		mustEncode(t, w, map[string]any{
			"number": healthy, "title": issue + " the work", "body": "",
			"html_url": fmt.Sprintf("https://github.com/acme/widgets/pull/%d", healthy),
			"state":    "open", "merged": false, "updated_at": "2026-02-01T00:00:00Z",
			"head": map[string]any{"ref": "legion/" + issue, "sha": head, "repo": map[string]any{"full_name": "acme/widgets"}},
			"base": map[string]any{"ref": "main", "sha": baseSHA},
		})
	})
	fake.handle("GET /repos/acme/widgets/pulls/{number}/commits", func(w http.ResponseWriter, _ *http.Request) {
		mustEncode(t, w, []any{})
	})
	if _, err := pool.Exec(ctx, `
		insert into pull_request_heads (repo, number, head_sha, state)
		select 'acme/widgets', n, lpad(to_hex(n), 40, '0'), 'open' from generate_series($1::integer, $2::integer) n`,
		first, first+failures-1); err != nil {
		t.Fatalf("store the pull requests whose reads fail: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		insert into pull_request_heads (repo, number, head_sha, head_ref, base_ref, state, issue_key, attribution_source, checked_at)
		values ('acme/widgets', $1, $2, 'legion/' || $3, 'main', 'open', $3, 'title', now() - interval '1 hour')`,
		healthy, head, issue); err != nil {
		t.Fatalf("store #%d as the release before stored it: %v", healthy, err)
	}
	stale, err := ListStalePullRequestHeads(ctx, pool, PullRequestHeadBatch, time.Now())
	if err != nil {
		t.Fatalf("list the stale heads: %v", err)
	}
	for _, row := range stale {
		if row.Number == healthy {
			t.Fatalf("the first batch already holds #%d: this test holds no row behind a full batch", healthy)
		}
	}

	reconcile := NewReconcile(pool, fake.newTestClient(), nil)
	baseOf := func() string {
		t.Helper()
		var stored *string
		if err := pool.QueryRow(ctx, `select base_sha from pull_request_heads where repo = 'acme/widgets' and number = $1`, healthy).Scan(&stored); err != nil {
			t.Fatalf("read #%d's base revision: %v", healthy, err)
		}
		if stored == nil {
			return ""
		}
		return *stored
	}
	for _, at := range []int{1, 2} {
		mu.Lock()
		pass = at
		mu.Unlock()
		if err := reconcile.reconcilePullRequestHeads(ctx, time.Now()); err != nil {
			t.Fatalf("reconcile pass %d: %v", at, err)
		}
	}
	if got := baseOf(); got != baseSHA {
		t.Fatalf("#%d's row stores the base revision %q after two passes, want %s: of %d due rows, each is tried within two passes of %d, whatever the reads of the others answer (tried in passes %v)",
			healthy, got, baseSHA, failures+1, PullRequestHeadBatch, triedIn[healthy])
	}
	var untried []int
	for number := first; number < first+failures; number++ {
		if len(triedIn[number]) == 0 {
			untried = append(untried, number)
		}
	}
	if len(untried) > 0 {
		t.Fatalf("after two passes %d failing rows were never tried (%v), want each tried", len(untried), untried)
	}
	var unchanged int
	if err := pool.QueryRow(ctx, `
		select count(*) from pull_request_heads
		where repo = 'acme/widgets' and number between $1 and $2 and checked_at is null and base_sha is null and head_sha = lpad(to_hex(number), 40, '0')`,
		first, first+failures-1).Scan(&unchanged); err != nil {
		t.Fatalf("read the failing rows: %v", err)
	}
	if unchanged != failures {
		t.Fatalf("%d of the %d rows whose reads failed are as they were, want all: a failed read is no read of the pull request", unchanged, failures)
	}

	var left []int
	for number := first; number < first+failures; number++ {
		if !triedIn[number][2] {
			left = append(left, number)
		}
	}
	if len(left) != 1 {
		t.Fatalf("the second pass left %v of the failing rows untried, want exactly one: it tries #%d and %d of them", left, healthy, PullRequestHeadBatch-1)
	}
	mu.Lock()
	pass = 3
	mu.Unlock()
	stale, err = ListStalePullRequestHeads(ctx, pool, PullRequestHeadBatch, time.Now())
	if err != nil {
		t.Fatalf("list the stale heads before the third pass: %v", err)
	}
	if len(stale) == 0 || stale[0].Number != left[0] {
		t.Fatalf("the third pass's batch begins %v, want #%d, the one row the second pass left untried", stale[:min(len(stale), 3)], left[0])
	}
}
