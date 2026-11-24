package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The record keeps how many review requests GitHub holds open on a pull request beside its merge
// state, since a `blocked` head with one open waits for that review rather than ending its round
// (classify.RoundWaitsOnMergeState). A GET /pulls/{number} answers them as `requested_reviewers`
// and `requested_teams`, people, Apps and teams alike, and both count.
func TestAReadOfOnePullRequestCountsItsOpenReviewRequests(t *testing.T) {
	var pull githubPullRequest
	if err := json.Unmarshal([]byte(`{"number":86,"state":"open","mergeable":true,"mergeable_state":"blocked",
		"requested_reviewers":[{"login":"octocat"},{"login":"legion-reviewer[bot]"}],"requested_teams":[{"slug":"engineering"}]}`), &pull); err != nil {
		t.Fatal(err)
	}
	if got := pull.reviewsRequested(); got != 3 {
		t.Fatalf("the read counts %d review requests, want the two reviewers and the team", got)
	}
	var none githubPullRequest
	if err := json.Unmarshal([]byte(`{"number":87,"state":"open","mergeable":true,"mergeable_state":"blocked"}`), &none); err != nil {
		t.Fatal(err)
	}
	if got := none.reviewsRequested(); got != 0 {
		t.Fatalf("an answer with no review request counts %d, want 0", got)
	}
}

// A base's batch reads the same count from GraphQL, which answers it as `reviewRequests
// { totalCount }`: the query asks for it beside the merge state, and the record takes it from the
// answer, so a base push never writes 0 over a review GitHub still waits for.
func TestABasePushRecordsTheReviewRequestsGitHubHoldsOpen(t *testing.T) {
	t.Parallel()
	if query := chunkQuery([]int{86}); !strings.Contains(query, "reviewRequests { totalCount }") {
		t.Fatalf("the batch query asks %q, want each pull request's reviewRequests { totalCount }", query)
	}
	pool := isolatedOutboxPool(t)
	seedOnMain(t, pool, "CAPTURE-1", 86, phase.Testing, "testing", "U")
	rig := newBatchRig(t, pool, func(_ int32, asked []int, _ http.Header) (int, string) {
		node := batchNode(86, "head-86", "MERGEABLE", "BLOCKED")
		node["reviewRequests"] = map[string]any{"totalCount": 2}
		return http.StatusOK, batchAnswer(t, asked, node)
	})

	pushOntoMain(t, pool, rig.w, "main-2")
	testwait.Eventually(t, "the batch's answer for 86 to be recorded", func() bool {
		return recordedPull(t, pool, "CAPTURE-1").MergeState == record.MergeStateBlocked && rig.ended()
	})
	if got := recordedPull(t, pool, "CAPTURE-1").ReviewsRequested; got != 2 {
		t.Fatalf("the record holds %d review requests for 86, want the 2 GitHub's answer holds open", got)
	}
}
