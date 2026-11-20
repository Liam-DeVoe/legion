package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// heldLinkLock is the advisory lock a test holds to keep a link write between its check and its
// insert: the trigger the test installs waits on it in the insert of a /pull/7 link.
const heldLinkLock = 4209012

// Two link writes at once cannot store one pull request in its two forms on two issues. Write A
// links acme/tools/pull/7 to the first issue, and a trigger holds A in its insert of that row,
// after its link check, until the test lets it go. While A is held, write B links the /issues/7
// form of the same pull request to the second issue, by a PATCH and, in the second case, by the
// create of an issue for owner/repo#7. B's check reads committed rows alone, so without a lock
// between the check and the write it finds nothing, and both links are stored: the pull request
// then has two issues, and the statement gives it to the lesser key. With the lock of the pull
// request (lockLinkedPullRequests), B waits for A to commit, and its check then reads A's row:
// the PATCH is refused with 409 EXTERNAL_LINK_TAKEN naming the first issue, and the create
// answers the first issue as the issue of owner/repo#7, as it does for a link stored before it.
func TestTwoLinkWritesAtOnceStoreOnePullRequestOnOneIssue(t *testing.T) {
	for _, second := range []string{"patch", "create"} {
		t.Run(second, func(t *testing.T) {
			handler, database := newTestHandlerWithStore(t)
			ctx := context.Background()
			if response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/projects", map[string]string{"key": "CORE", "name": "Core"}, "alice"); response.Code != http.StatusCreated {
				t.Fatalf("create project: status=%d body=%s", response.Code, response.Body.String())
			}
			// The create for acme/tools#7 takes its project from the repository's mapping.
			if response := dispatchRequest(t, handler, http.MethodPut, "/api/v1/settings/repo-projects/acme/tools", map[string]any{"project": "CORE"}, "alice"); response.Code != http.StatusOK {
				t.Fatalf("map acme/tools to CORE: status=%d body=%s", response.Code, response.Body.String())
			}
			keys := []string{}
			for _, title := range []string{"One", "Two"} {
				response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]string{"project": "CORE", "title": title}, "alice")
				if response.Code != http.StatusCreated {
					t.Fatalf("create %q: status=%d body=%s", title, response.Code, response.Body.String())
				}
				keys = append(keys, decodeBody[model.Issue](t, response).Key)
			}
			one, two := keys[0], keys[1]
			link := func(key, url string) *httptest.ResponseRecorder {
				return sessionRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+key,
					map[string]any{"external_links": []map[string]string{{"url": url, "kind": "github"}}, "actor": sessionActor()})
			}
			exec := func(statement string) {
				t.Helper()
				if _, err := database.Pool.Exec(ctx, statement); err != nil {
					t.Fatalf("%s: %v", statement, err)
				}
			}
			exec(`create function hold_pull_7_link() returns trigger language plpgsql as $$
				begin
					if new.url like '%/pull/7' then
						perform pg_advisory_lock(4209012);
						perform pg_advisory_unlock(4209012);
					end if;
					return new;
				end $$`)
			exec(`create trigger hold_pull_7_link before insert on issue_external_links
				for each row execute function hold_pull_7_link()`)
			held, err := database.Pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			if _, err := held.Exec(ctx, `select pg_advisory_lock($1)`, heldLinkLock); err != nil {
				t.Fatal(err)
			}
			released := false
			release := func() {
				if !released {
					released = true
					if _, err := held.Exec(ctx, `select pg_advisory_unlock($1)`, heldLinkLock); err != nil {
						t.Fatal(err)
					}
				}
			}
			defer release()
			// waiting is how many backends of this database wait on an advisory lock.
			waiting := func() int {
				var count int
				if err := database.Pool.QueryRow(ctx, `
					select count(*) from pg_stat_activity
					where datname = current_database() and wait_event_type = 'Lock' and wait_event = 'advisory'
				`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}

			first := make(chan *httptest.ResponseRecorder, 1)
			go func() { first <- link(one, "https://github.com/acme/tools/pull/7") }()
			deadline := time.Now().Add(30 * time.Second)
			for waiting() < 1 {
				if time.Now().After(deadline) {
					t.Fatal("write A never reached its held insert")
				}
				time.Sleep(20 * time.Millisecond)
			}

			later := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				if second == "patch" {
					later <- link(two, "https://github.com/acme/tools/issues/7")
					return
				}
				later <- dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues",
					map[string]string{"project": "CORE", "external": "acme/tools#7"}, "alice")
			}()
			// B either answers while A is held, which is the race, or waits on a lock behind A.
			var answered *httptest.ResponseRecorder
			deadline = time.Now().Add(30 * time.Second)
			for answered == nil && waiting() < 2 {
				select {
				case answered = <-later:
				case <-time.After(20 * time.Millisecond):
				}
				if answered == nil && time.Now().After(deadline) {
					t.Fatal("write B neither answered nor waited on a lock while A was held")
				}
			}
			waitedForA := answered == nil
			release()
			a := <-first
			b := answered
			if b == nil {
				b = <-later
			}
			t.Logf("second=%s b_waited_for_a=%v a=%d b=%d b_body=%s", second, waitedForA, a.Code, b.Code, strings.TrimSpace(b.Body.String()))

			if a.Code != http.StatusOK {
				t.Fatalf("write A of /pull/7 to %s: status=%d body=%s, want 200", one, a.Code, a.Body.String())
			}
			var holders []string
			if err := database.Pool.QueryRow(ctx, `
				select coalesce(array_agg(distinct issue_key order by issue_key), '{}') from issue_external_links
				where lower(url) in ('https://github.com/acme/tools/pull/7', 'https://github.com/acme/tools/issues/7')
			`).Scan(&holders); err != nil {
				t.Fatal(err)
			}
			if len(holders) != 1 || holders[0] != one {
				t.Fatalf("the two forms of acme/tools#7 are held by %v after two writes at once, want %s alone: B's check read no row of A's write in flight", holders, one)
			}
			switch second {
			case "patch":
				if b.Code != http.StatusConflict || !strings.Contains(b.Body.String(), `"code":"EXTERNAL_LINK_TAKEN"`) || !strings.Contains(b.Body.String(), one) {
					t.Fatalf("write B of /issues/7 to %s: status=%d body=%s, want 409 EXTERNAL_LINK_TAKEN naming %s", two, b.Code, b.Body.String(), one)
				}
			case "create":
				if b.Code != http.StatusOK || !strings.Contains(b.Body.String(), one) {
					t.Fatalf("the create for acme/tools#7: status=%d body=%s, want 200 naming %s, the issue that holds it", b.Code, b.Body.String(), one)
				}
			}
		})
	}
}
