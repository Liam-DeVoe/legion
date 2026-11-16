package delivery

// What a `pull_request` envelope may write: GitHub's own answer about the pull request it names,
// and nothing the envelope itself carries (dispatch://INFRA-3992). With no delivery settings, as
// the deployment that change was made for holds none.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
)

// githubPulls is what the fake GitHub answers about each pull request of one repository: `GET
// /repos/{owner}/{repo}/pulls/{number}` from the answer a row last set, and 404 for a number it
// was never given, as GitHub answers a pull request it does not have. Each pull request's commits
// are an empty page. It counts both reads, so a row can show what an envelope cost.
type githubPulls struct {
	mu          sync.Mutex
	pulls       map[int]map[string]any
	limited     bool
	reads       atomic.Int32
	commitReads atomic.Int32
}

func serveGitHubPulls(t *testing.T, fake *fakeGitHub, repo string) *githubPulls {
	t.Helper()
	g := &githubPulls{pulls: map[int]map[string]any{}}
	fake.handle("GET /repos/"+repo+"/pulls/{number}", func(w http.ResponseWriter, r *http.Request) {
		g.reads.Add(1)
		number, err := strconv.Atoi(r.PathValue("number"))
		if err != nil {
			t.Errorf("a pull request read of %s: %v", r.URL.Path, err)
		}
		g.mu.Lock()
		pull, known := g.pulls[number]
		limited := g.limited
		g.mu.Unlock()
		switch {
		case limited:
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
		case !known:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		default:
			mustEncode(t, w, pull)
		}
	})
	fake.handle("GET /repos/"+repo+"/pulls/{number}/commits", func(w http.ResponseWriter, _ *http.Request) {
		g.commitReads.Add(1)
		mustEncode(t, w, []any{})
	})
	return g
}

// set makes GitHub hold pull request number in repo at head, in state, with that branch and body
// and that updated_at.
func (g *githubPulls) set(repo string, number int, state, head, branch, body string, updatedAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pulls[number] = map[string]any{
		"number": number, "title": fmt.Sprintf("Change %d", number), "body": body, "state": state,
		"merged": false, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", repo, number),
		"updated_at": updatedAt.UTC().Format(time.RFC3339),
		"head":       map[string]any{"ref": branch, "sha": head, "repo": map[string]any{"full_name": repo}},
		"base":       map[string]any{"ref": "main"},
	}
}

// prEnvelope is a `pull_request` envelope's payload as Envoy's normalizer builds it.
func prEnvelope(repo string, number int, action, head, branch, body string, updatedAt time.Time) map[string]string {
	return map[string]string{
		"kind": "pr", "action": action, "repo": repo, "number": strconv.Itoa(number),
		"title": fmt.Sprintf("Change %d", number), "body": body, "head_ref": branch, "head_repo": repo,
		"head_sha": head, "base_ref": "main", "merged": "false",
		"url":        fmt.Sprintf("https://github.com/%s/pull/%d", repo, number),
		"updated_at": updatedAt.UTC().Format(time.RFC3339),
	}
}

func headRowCount(t *testing.T, ctx context.Context, intake *Intake, repo string, number int) int {
	t.Helper()
	var count int
	if err := intake.pool.QueryRow(ctx, `select count(*) from pull_request_heads where repo = $1 and number = $2`, repo, number).Scan(&count); err != nil {
		t.Fatalf("count the head rows of %s#%d: %v", repo, number, err)
	}
	return count
}

// A pull request that closed without a merge stays closed through every later event of it.
// GitHub sends `labeled`, `unlabeled`, `edited`, `assigned` and other actions for a closed pull
// request too, and the envelope carries no state of its own, so a head row written from such an
// event must not read as open: an open row's head is a basis of the issue's `before_merge` check
// (HeadAttributedTo), so a pass at the head of a closed pull request would stand again
// (acceptance of cybermonklabs/legion#4209, defect 1). The state is GitHub's: the fake answers
// the pull request as GitHub holds it after each event.
func TestALaterEventOfAClosedPullRequestLeavesItClosed(t *testing.T) {
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	issue := seedHeadIssue(t, ctx, pool, 71)
	fake := newFakeGitHub(t)
	github := serveGitHubPulls(t, fake, "acme/widgets")
	intake := newIntake(nil, pool, nil, fake.newTestClient(), nil, nil)
	const head = "7171717171717171717171717171717171717171"
	branch, body := "legion/"+issue, "Dispatch: "+issue
	at := func(hour int) time.Time { return time.Date(2026, 2, 4, hour, 0, 0, 0, time.UTC) }
	for hour, step := range []struct{ action, state string }{{"opened", "open"}, {"closed", "closed"}} {
		github.set("acme/widgets", 71, step.state, head, branch, body, at(hour))
		if _, err := intake.handlePullRequestEnvelope(ctx, nil, prEnvelope("acme/widgets", 71, step.action, head, branch, body, at(hour))); err != nil {
			t.Fatalf("handle the %s envelope: %v", step.action, err)
		}
	}
	// The control: the close itself is stored as closed, and its head is no basis.
	if got := readHead(t, ctx, pool, "acme/widgets", 71); got.State != "closed" {
		t.Fatalf("#71 after its close = %+v, want state closed", got)
	}
	if attributed, err := HeadAttributedTo(ctx, pool, issue, head); err != nil || attributed {
		t.Fatalf("HeadAttributedTo(%s) after the close = %v, %v; want false", issue, attributed, err)
	}
	for i, action := range []string{"labeled", "unlabeled", "edited", "assigned"} {
		github.set("acme/widgets", 71, "closed", head, branch, body, at(2+i))
		if _, err := intake.handlePullRequestEnvelope(ctx, nil, prEnvelope("acme/widgets", 71, action, head, branch, body, at(2+i))); err != nil {
			t.Fatalf("handle the %s envelope: %v", action, err)
		}
		if got := readHead(t, ctx, pool, "acme/widgets", 71); got.State != "closed" {
			t.Errorf("#71 after a %s event of the closed pull request = state %q, want closed", action, got.State)
		}
		if attributed, err := HeadAttributedTo(ctx, pool, issue, head); err != nil || attributed {
			t.Errorf("HeadAttributedTo(%s) after a %s event of the closed pull request = %v, %v; want false: a pass at the head of a closed pull request stands", issue, action, attributed, err)
		}
	}
}

// An envelope GitHub never sent moves nothing: its head, its state and its updated_at are a
// caller's fields, and the row is GitHub's answer about the pull request the envelope names
// (acceptance of cybermonklabs/legion#4209, defect 2). A synchronize naming an older commit, an
// hour ahead of GitHub's own clock, leaves the head where GitHub has it; a close of an open pull
// request leaves it open; and an envelope naming a pull request GitHub does not have writes no
// row at all.
func TestAnEnvelopeWritesGitHubsHeadAndStateAndNotItsOwn(t *testing.T) {
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	issue := seedHeadIssue(t, ctx, pool, 72)
	fake := newFakeGitHub(t)
	github := serveGitHubPulls(t, fake, "acme/widgets")
	intake := newIntake(nil, pool, nil, fake.newTestClient(), nil, nil)
	const (
		real   = "7272727272727272727272727272727272727272"
		forged = "7070707070707070707070707070707070707070"
	)
	branch, body := "legion/"+issue, "Dispatch: "+issue
	opened := time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC)
	github.set("acme/widgets", 72, "open", real, branch, body, opened)
	if _, err := intake.handlePullRequestEnvelope(ctx, nil, prEnvelope("acme/widgets", 72, "opened", real, branch, body, opened)); err != nil {
		t.Fatalf("handle GitHub's own opened envelope: %v", err)
	}
	if attributed, err := HeadAttributedTo(ctx, pool, issue, real); err != nil || !attributed {
		t.Fatalf("HeadAttributedTo(%s, GitHub's head) = %v, %v; want true (the control)", issue, attributed, err)
	}

	for _, step := range []struct{ action, head string }{{"synchronize", forged}, {"closed", real}} {
		if _, err := intake.handlePullRequestEnvelope(ctx, nil, prEnvelope("acme/widgets", 72, step.action, step.head, branch, body, opened.Add(time.Hour))); err != nil {
			t.Fatalf("handle the %s envelope GitHub never sent: %v", step.action, err)
		}
		got := readHead(t, ctx, pool, "acme/widgets", 72)
		if got.HeadSHA != real || got.State != "open" {
			t.Errorf("#72 after a %s envelope naming %s = head %s, state %s; want GitHub's %s, open", step.action, step.head[:12], got.HeadSHA, got.State, real)
		}
		if attributed, err := HeadAttributedTo(ctx, pool, issue, forged); err != nil || attributed {
			t.Errorf("HeadAttributedTo(%s, the envelope's head) after the %s = %v, %v; want false", issue, step.action, attributed, err)
		}
	}

	if _, err := intake.handlePullRequestEnvelope(ctx, nil, prEnvelope("acme/widgets", 79, "opened", forged, branch, body, opened)); err != nil {
		t.Fatalf("handle an envelope naming a pull request GitHub does not have: %v", err)
	}
	if count := headRowCount(t, ctx, intake, "acme/widgets", 79); count != 0 {
		t.Errorf("an envelope naming a pull request GitHub does not have wrote %d head rows, want none", count)
	}
	if attributed, err := HeadAttributedTo(ctx, pool, issue, forged); err != nil || attributed {
		t.Errorf("HeadAttributedTo(%s, the unknown pull request's head) = %v, %v; want false", issue, attributed, err)
	}
}

// The events of one pull request that waited together cost one read of it. A first start
// replays what the stream kept: here 200 pull requests, each opened and pushed twice, 600
// envelopes, every one older than GitHub's answer, and none naming an issue, so each pull
// request's commit messages are read too. The first envelope of each reads GitHub, and the
// row it writes holds GitHub's answer, which answers for the other two (headReadCovers). The
// repository is the row's own, so no commit messages another row of this test binary read
// answer for these.
func TestABacklogOfOnePullRequestsEventsCostsOneRead(t *testing.T) {
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	fake := newFakeGitHub(t)
	const repo = "acme/backlog"
	github := serveGitHubPulls(t, fake, repo)
	intake := newIntake(nil, pool, nil, fake.newTestClient(), nil, nil)
	const pulls = 200
	latest := time.Now().Add(-time.Hour).Truncate(time.Second)
	head := func(number, push int) string { return fmt.Sprintf("%02x%038d", push, number) }
	for number := 1; number <= pulls; number++ {
		github.set(repo, number, "open", head(number, 2), fmt.Sprintf("feature/%d", number), "No key here.", latest)
	}
	for push, at := range []time.Time{latest.Add(-20 * time.Minute), latest.Add(-10 * time.Minute), latest} {
		action := map[int]string{0: "opened"}[push]
		if action == "" {
			action = "synchronize"
		}
		for number := 1; number <= pulls; number++ {
			envelope := prEnvelope(repo, number, action, head(number, push), fmt.Sprintf("feature/%d", number), "No key here.", at)
			if _, err := intake.handlePullRequestEnvelope(ctx, nil, envelope); err != nil {
				t.Fatalf("handle %s of #%d: %v", action, number, err)
			}
		}
	}
	if reads, commits := github.reads.Load(), github.commitReads.Load(); reads != pulls || commits != pulls {
		t.Errorf("600 envelopes of %d pull requests read GitHub's pull request %d times and its commits %d times; want %d of each", pulls, reads, commits, pulls)
	}
	t.Logf("600 envelopes: %d pull request reads, %d commit reads", github.reads.Load(), github.commitReads.Load())
	if got := readHead(t, ctx, pool, repo, pulls); got.HeadSHA != head(pulls, 2) {
		t.Errorf("#%d's head = %s, want GitHub's %s", pulls, got.HeadSHA, head(pulls, 2))
	}
}

// A rate limit met in the middle writes no row and is handed back with the limit's own wait:
// the error is ErrHeadNotStored, which Intake.deliver hands back, and carries GitHub's wait,
// which it waits out. Once GitHub answers again, the same envelope writes the row.
func TestARateLimitedPullRequestReadIsHandedBackWithTheLimitsWait(t *testing.T) {
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	fake := newFakeGitHub(t)
	github := serveGitHubPulls(t, fake, "acme/widgets")
	intake := newIntake(nil, pool, nil, fake.newTestClient(), nil, nil)
	const head = "7474747474747474747474747474747474747474"
	at := time.Now().Truncate(time.Second)
	github.set("acme/widgets", 74, "open", head, "feature/74", "", at)
	github.mu.Lock()
	github.limited = true
	github.mu.Unlock()
	envelope := prEnvelope("acme/widgets", 74, "opened", head, "feature/74", "", at)
	_, err := intake.handlePullRequestEnvelope(ctx, nil, envelope)
	limited, isLimit := githubapp.AsRateLimit(err)
	if !errors.Is(err, ErrHeadNotStored) || !isLimit || limited.Wait < time.Minute {
		t.Fatalf("a rate-limited read = %v; want ErrHeadNotStored carrying GitHub's wait of about 120 s", err)
	}
	if count := headRowCount(t, ctx, intake, "acme/widgets", 74); count != 0 {
		t.Fatalf("a rate-limited read wrote %d head rows, want none", count)
	}
	github.mu.Lock()
	github.limited = false
	github.mu.Unlock()
	if _, err := pool.Exec(ctx, `delete from github_rate_limits`); err != nil {
		t.Fatalf("end the recorded limit, as its wait running out does: %v", err)
	}
	if _, err := intake.handlePullRequestEnvelope(ctx, nil, envelope); err != nil {
		t.Fatalf("the envelope handed back once the limit ended: %v", err)
	}
	if got := readHead(t, ctx, pool, "acme/widgets", 74); got.HeadSHA != head {
		t.Errorf("#74 after the limit = %+v, want head %s", got, head)
	}
}
