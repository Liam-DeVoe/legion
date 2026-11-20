package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/githubtest"
)

// issue_links_attribution_test.go holds R19 of the spec of several open pull requests as a change
// of an issue's links meets it: a pull request that an issue links by its URL is that issue's
// pull request, in any repository, while the issue links it, and one whose head is in a fork is
// no issue's, linked or not. The file builds at a commit before the PATCH judged a linked pull
// request again, and fails there on its own assertion.

// changedLinkPull is one pull request changedLinkGitHub answers: its title, body, head branch, the
// repository its head branch is in, its head commit, and whether it merged. A refused one is
// answered with the 403 GitHub refuses an installation token a resource with, which is no rate
// limit: GitHub says nothing of it.
type changedLinkPull struct {
	title, body, headRef, headRepo, sha string
	merged, refused                     bool
}

// changedLinkGitHub is GitHub for the pull requests a change of an issue's links names: each
// pull request of pulls, keyed `owner/repo#n`, answered whole as GET /repos/{o}/{r}/pulls/{n}
// answers it, with no commit messages, and every other one GitHub's 404. reads counts the reads
// of each.
type changedLinkGitHub struct {
	mu    sync.Mutex
	pulls map[string]changedLinkPull
	reads map[string]int
}

// readsOf is how often GitHub was asked for pull, so far.
func (g *changedLinkGitHub) readsOf(pull string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reads[pull]
}

// changedLinkServer is a Dispatch holding an App key, installed on every repository, whose GitHub
// answers from github.
func changedLinkServer(t *testing.T, github *changedLinkGitHub) (http.Handler, *store.Store) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate fixture key: %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	github.reads = map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":77,"app_slug":"dispatch-test","permissions":{"contents":"read","pull_requests":"read"}}`)
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_fake_77","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}", func(w http.ResponseWriter, r *http.Request) {
		repo, number := r.PathValue("owner")+"/"+r.PathValue("repo"), r.PathValue("number")
		github.mu.Lock()
		github.reads[repo+"#"+number]++
		pull, known := github.pulls[repo+"#"+number]
		github.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		if pull.refused {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
			return
		}
		state, mergedAt := "open", "null"
		if pull.merged {
			state, mergedAt = "closed", `"2026-11-16T00:00:00Z"`
		}
		fmt.Fprintf(w, `{"number":%s,"title":%q,"body":%q,"html_url":"https://github.com/%s/pull/%s",
			"state":%q,"merged":%t,"merged_at":%s,"updated_at":"2026-11-16T00:00:00Z",
			"head":{"ref":%q,"sha":%q,"repo":{"full_name":%q}},"base":{"ref":"main"}}`,
			number, pull.title, pull.body, repo, number, state, pull.merged, mergedAt, pull.headRef, pull.sha, pull.headRepo)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}/commits", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	upstream := githubtest.Server(t, githubtest.Spec{Handle: mux.ServeHTTP})
	handler, database, _, _ := newRegisteredTestServer(t, testServerOptions{
		app:           &auth.AppConfig{ClientID: "Iv1.test", PEM: pemText},
		githubAPIBase: upstream,
	})
	return handler, database
}

// storeMerge stores repo#number merged at sha, with the issue the attribution gave it when a
// writer last judged it (nil for nobody) and the source that named it, as a read of GitHub before
// the test leaves the row.
func storeMerge(t *testing.T, database *store.Store, repo string, number int, sha string, issue *string, source string) {
	t.Helper()
	eventAt := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	read := time.Now().Add(-time.Minute)
	if err := delivery.UpsertPullRequestHead(context.Background(), database.Pool, nil, delivery.PullRequestHead{
		Repo: repo, Number: number, HeadSHA: sha, HeadRef: "work/deploy-job", BaseRef: "main",
		State: "merged", IssueKey: issue, AttributionSource: source, AttributionComplete: true,
		Title: "Move the deploy job", URL: "https://github.com/" + repo + "/pull/" + strconv.Itoa(number),
		EventAt: &eventAt, CheckedAt: &read,
	}); err != nil {
		t.Fatalf("store the merged head of %s#%d: %v", repo, number, err)
	}
}

// headNumbersOf is the number of each pull request the results read compares issue's
// `before_merge` checks with (delivery.HeadsAttributedTo), in order.
func headNumbersOf(t *testing.T, database *store.Store, issue string) []int {
	t.Helper()
	heads, err := delivery.HeadsAttributedTo(context.Background(), database.Pool, issue)
	if err != nil {
		t.Fatalf("read the heads of %s: %v", issue, err)
	}
	numbers := []int{}
	for _, head := range heads {
		numbers = append(numbers, head.Number)
	}
	slices.Sort(numbers)
	return numbers
}

// attributionMovesOf counts the issue.pull_request_attribution_changed events on issue that move
// repo#number away from it.
func attributionMovesOf(t *testing.T, database *store.Store, issue, repo string, number int) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) from events
		where issue_key = $1 and type = $2 and payload->>'repo' = $3
			and (payload->>'number')::int = $4 and payload->>'from' = $1 and payload->>'to' is null
	`, issue, delivery.AttributionEventType, repo, number).Scan(&count); err != nil {
		t.Fatalf("count the moves of %s#%d off %s: %v", repo, number, issue, err)
	}
	return count
}

// R19: removing a link takes the pull request it names from the issue at the removal, merged or
// not, in a repository the issue does not resolve to as much as in its own: no read of the
// issue's results counts its head once the PATCH has answered, and the move is on the issue's
// timeline. Before, the issue kept a merged linked pull request until the attribution rule
// changed, since nothing reads a merged row again. GitHub is asked about that pull request again
// at once, so its other sources give it to whichever issue they name, and the removal stands
// where GitHub says nothing of it. A pull request the issue still links stays the issue's, and
// GitHub is not asked about it.
func TestRemovingALinkTakesThePullRequestFromTheIssueAtOnce(t *testing.T) {
	const (
		removedHead = "12c12c12c12c12c12c12c12c12c12c12c12c12c1"
		keptHead    = "13d13d13d13d13d13d13d13d13d13d13d13d13d1"
		refusedHead = "16a16a16a16a16a16a16a16a16a16a16a16a16a1"
		removedURL  = "https://github.com/acme/tools/pull/12"
		keptURL     = "https://github.com/acme/tools/pull/13"
		refusedURL  = "https://github.com/acme/tools/pull/16"
	)
	github := &changedLinkGitHub{pulls: map[string]changedLinkPull{
		"acme/tools#12": {title: "Move the deploy job", headRef: "work/deploy-job", headRepo: "acme/tools", sha: removedHead, merged: true},
		"acme/tools#13": {title: "Move the build job", headRef: "work/build-job", headRepo: "acme/tools", sha: keptHead, merged: true},
		"acme/tools#16": {refused: true},
	}}
	handler, database := changedLinkServer(t, github)
	issue := issueInRepository(t, handler, "RELR", "acme/widgets", "Work in two repositories")
	linkPullRequests(t, handler, issue.Key, removedURL, keptURL, refusedURL)
	// The three merged pull requests as the attribution leaves them once a writer judged them:
	// the issue's, by its links.
	storeMerge(t, database, "acme/tools", 12, removedHead, &issue.Key, "external_link")
	storeMerge(t, database, "acme/tools", 13, keptHead, &issue.Key, "external_link")
	storeMerge(t, database, "acme/tools", 16, refusedHead, &issue.Key, "external_link")
	if heads := headNumbersOf(t, database, issue.Key); !slices.Equal(heads, []int{12, 13, 16}) {
		t.Fatalf("the heads of %s while it links #12, #13 and #16 = %v, want [12 13 16]", issue.Key, heads)
	}

	linkPullRequests(t, handler, issue.Key, keptURL)

	if heads := headNumbersOf(t, database, issue.Key); !slices.Equal(heads, []int{13}) {
		t.Fatalf("the heads of %s once its links to acme/tools#12 and #16 are removed = %v, want [13]: a pull request is the issue's while the issue links it, whatever GitHub says of it after",
			issue.Key, heads)
	}
	for _, number := range []int{12, 16} {
		if moves := attributionMovesOf(t, database, issue.Key, "acme/tools", number); moves != 1 {
			t.Fatalf("%s's timeline holds %d moves of acme/tools#%d off it, want 1", issue.Key, moves, number)
		}
	}
	for _, pull := range []string{"acme/tools#12", "acme/tools#16"} {
		if reads := github.readsOf(pull); reads != 1 {
			t.Fatalf("GitHub was asked for %s %d times after its link was removed, want once", pull, reads)
		}
	}
	if reads := github.readsOf("acme/tools#13"); reads != 0 {
		t.Fatalf("GitHub was asked for acme/tools#13 %d times, want none: its link did not change", reads)
	}
}

// R19: linking a pull request makes it the issue's at the link, merged or not. A merged pull
// request of another repository whose row a writer judged before the issue linked it, and so
// gave no issue, is one of the issue's heads once the PATCH that links it has answered: GitHub,
// asked about it at once, says its head is a branch of its own repository. One whose head is in
// a fork is no head, linked or not.
func TestLinkingAMergedPullRequestMakesItTheIssuesAtOnce(t *testing.T) {
	const (
		ownHead  = "14e14e14e14e14e14e14e14e14e14e14e14e14e1"
		forkHead = "15f15f15f15f15f15f15f15f15f15f15f15f15f1"
	)
	github := &changedLinkGitHub{pulls: map[string]changedLinkPull{
		"acme/tools#14": {title: "Move the deploy job", headRef: "work/deploy-job", headRepo: "acme/tools", sha: ownHead, merged: true},
		"acme/tools#15": {title: "Move the deploy job", headRef: "work/deploy-job", headRepo: "contributor/tools", sha: forkHead, merged: true},
	}}
	handler, database := changedLinkServer(t, github)
	issue := issueInRepository(t, handler, "RELA", "acme/widgets", "Work linked after its merge")
	storeMerge(t, database, "acme/tools", 14, ownHead, nil, "none")
	storeMerge(t, database, "acme/tools", 15, forkHead, nil, "none")

	linkPullRequests(t, handler, issue.Key,
		"https://github.com/acme/tools/pull/14", "https://github.com/acme/tools/pull/15")

	if heads := headNumbersOf(t, database, issue.Key); !slices.Equal(heads, []int{14}) {
		t.Fatalf("the heads of %s once it links acme/tools#14 and the fork's #15 = %v, want [14]", issue.Key, heads)
	}
}

// The attribution's second source is a link of the issue too: a pull request whose body cites a
// GitHub issue the issue links is the issue's, where the issue resolves to the pull request's
// repository. Removing that link takes the pull request from the issue at the removal, merged or
// not, and the move is on the issue's timeline; before, the issue kept it until a read of the
// pull request, never for a merged one. No stored row says which GitHub issue a body cites, so
// every pull request the issue held by a citation is taken and read again from GitHub at once: one
// whose body cites a GitHub issue the issue still links is the issue's again, and the removal
// stands where GitHub says nothing of the pull request. A pull request the issue holds by another
// source is left as it is, and GitHub is not asked about it.
func TestRemovingAnIssueLinkTakesThePullRequestsItGaveAtOnce(t *testing.T) {
	const (
		removedLink = "https://github.com/acme/widgets/issues/71"
		keptLink    = "https://github.com/acme/widgets/issues/82"
		citedHead   = "31a31a31a31a31a31a31a31a31a31a31a31a31a1"
		keptHead    = "32b32b32b32b32b32b32b32b32b32b32b32b32b1"
		branchHead  = "33c33c33c33c33c33c33c33c33c33c33c33c33c1"
		refusedHead = "34d34d34d34d34d34d34d34d34d34d34d34d34d1"
	)
	github := &changedLinkGitHub{pulls: map[string]changedLinkPull{
		"acme/widgets#31": {title: "Move the deploy job", body: "Fixes #71.", headRef: "work/deploy-job", headRepo: "acme/widgets", sha: citedHead, merged: true},
		"acme/widgets#32": {title: "Move the build job", body: "Follows #82.", headRef: "work/build-job", headRepo: "acme/widgets", sha: keptHead, merged: true},
		"acme/widgets#34": {refused: true},
	}}
	handler, database := changedLinkServer(t, github)
	issue := issueInRepository(t, handler, "RELC", "acme/widgets", "Work its GitHub issues name")
	linkPullRequests(t, handler, issue.Key, removedLink, keptLink)
	// Four merged pull requests of the issue's own repository as the attribution leaves them: #31,
	// #32 and #34 the issue's because each body cites a GitHub issue it links, #33 the issue's by
	// its branch.
	storeMerge(t, database, "acme/widgets", 31, citedHead, &issue.Key, "github_issue_link")
	storeMerge(t, database, "acme/widgets", 32, keptHead, &issue.Key, "github_issue_link")
	storeMerge(t, database, "acme/widgets", 33, branchHead, &issue.Key, "branch")
	storeMerge(t, database, "acme/widgets", 34, refusedHead, &issue.Key, "github_issue_link")
	if heads := headNumbersOf(t, database, issue.Key); !slices.Equal(heads, []int{31, 32, 33, 34}) {
		t.Fatalf("the heads of %s while it links both GitHub issues = %v, want [31 32 33 34]", issue.Key, heads)
	}

	linkPullRequests(t, handler, issue.Key, keptLink)

	if heads := headNumbersOf(t, database, issue.Key); !slices.Equal(heads, []int{32, 33}) {
		t.Fatalf("the heads of %s once its link to acme/widgets/issues/71 is removed = %v, want [32 33]: #31 and #34 were the issue's by a link it no longer has",
			issue.Key, heads)
	}
	for _, number := range []int{31, 34} {
		if moves := attributionMovesOf(t, database, issue.Key, "acme/widgets", number); moves != 1 {
			t.Fatalf("%s's timeline holds %d moves of acme/widgets#%d off it, want 1", issue.Key, moves, number)
		}
	}
	for _, pull := range []string{"acme/widgets#31", "acme/widgets#32", "acme/widgets#34"} {
		if reads := github.readsOf(pull); reads != 1 {
			t.Fatalf("GitHub was asked for %s %d times after the link was removed, want once", pull, reads)
		}
	}
	if reads := github.readsOf("acme/widgets#33"); reads != 0 {
		t.Fatalf("GitHub was asked for acme/widgets#33 %d times, want none: no link gave it the issue", reads)
	}
}
