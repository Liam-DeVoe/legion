package cireconcile

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/cistore"
	"github.com/sjawhar/envoy/internal/contracts"
)

// The head the tests below read is cybermonklabs/legion#3513 at a368687e, read on 2026-11-21.
//
// testdata/graphql-head-3513.json is that commit as headsQuery asks for it, workflowRun { event }
// of each check suite included, captured with `gh api graphql` against the real API. The commit
// was read by its oid, since the pull request's head has moved on since, and its answer is set
// under the one pull request node of headsQuery's own envelope; it was read again at 06:57Z and
// answered byte for byte the same. Its suite 121419707580 is the pull_request run 44860489650,
// whose ci-ok failed in check run 136722779787. Its suite 121419789924 is a workflow_dispatch run
// of the pull request's branch, run 44860519340, whose ci-ok passed in check run 136723497213, a
// higher id, and whose jobs share every name of the pull_request run's.
//
// testdata/graphql-rollup-3513.json is GitHub's own rollup of the pull request at that commit,
// captured the same way at 06:57Z: statusCheckRollup with each check run's id, name, conclusion,
// whether the pull request requires it, and its suite's workflow run event. It answers FAILURE,
// ci-ok is check run 136722779787, and each of its 74 check runs is of a pull_request run or of
// an App other than GitHub Actions.
const (
	countedNumber        = 3513
	countedNumberText    = "3513"
	countedSHA           = "a368687e3a5a316f04533f1e83715eaf5e5663f7"
	countedCheck         = "ci-ok"
	countedPullCheck     = 136722779787
	countedDispatchCheck = 136723497213
	countedDispatchSuite = "121419789924"
	eventlessLine        = "ci reconcile counted check suites whose workflow run event it could not read"
)

// rollupCheck is one check run of GitHub's rollup of a pull request.
type rollupCheck struct {
	ID         uint64
	Conclusion string
}

// countedRollup is GitHub's rollup of #3513 at countedSHA (testdata/graphql-rollup-3513.json): the
// check runs GitHub counts for the pull request, by name.
func countedRollup(t *testing.T) map[string]rollupCheck {
	t.Helper()
	var answer struct {
		Data struct {
			Repository struct {
				Object struct {
					StatusCheckRollup struct {
						State    string `json:"state"`
						Contexts struct {
							TotalCount int `json:"totalCount"`
							Nodes      []struct {
								Typename   string `json:"__typename"`
								DatabaseID uint64 `json:"databaseId"`
								Name       string `json:"name"`
								Conclusion string `json:"conclusion"`
							} `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"object"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(captured(t, "graphql-rollup-3513.json")), &answer); err != nil {
		t.Fatalf("the captured rollup is not GitHub's envelope: %v", err)
	}
	rollup := answer.Data.Repository.Object.StatusCheckRollup
	if rollup.State != "FAILURE" || rollup.Contexts.TotalCount != len(rollup.Contexts.Nodes) {
		t.Fatalf("the captured rollup answers %s with %d of %d contexts, want FAILURE with every context",
			rollup.State, len(rollup.Contexts.Nodes), rollup.Contexts.TotalCount)
	}
	checks := map[string]rollupCheck{}
	for _, node := range rollup.Contexts.Nodes {
		if node.Typename != "CheckRun" {
			continue
		}
		if _, twice := checks[node.Name]; twice {
			t.Fatalf("the captured rollup names %q twice", node.Name)
		}
		checks[node.Name] = rollupCheck{ID: node.DatabaseID, Conclusion: strings.ToLower(node.Conclusion)}
	}
	if got := checks[countedCheck]; got.ID != countedPullCheck || got.Conclusion != "failure" {
		t.Fatalf("the captured rollup's ci-ok is %+v, want check run %d's failure", got, uint64(countedPullCheck))
	}
	return checks
}

// countedCiOKRuns is each ci-ok check run of the captured answer for #3513's head, as the REST
// listing and a check_run webhook carry one, with no App and no suite: what a check of a record
// written by a listener of an earlier release holds, which kept neither.
func countedCiOKRuns(t *testing.T) []object {
	t.Helper()
	var answer graphQLAnswer
	if err := json.Unmarshal([]byte(captured(t, "graphql-head-3513.json")), &answer); err != nil {
		t.Fatalf("the captured answer is not GitHub's envelope: %v", err)
	}
	var runs []object
	for _, suite := range answer.Data.Repository.PullRequests.Nodes[0].Commits.Nodes[0].Commit.CheckSuites.Nodes {
		for _, run := range suite.CheckRuns.Nodes {
			if run.Name != countedCheck {
				continue
			}
			runs = append(runs, object{
				"id": run.DatabaseID, "name": run.Name, "head_sha": countedSHA,
				"status": strings.ToLower(run.Status), "conclusion": strings.ToLower(run.Conclusion),
				"started_at": run.StartedAt, "completed_at": run.CompletedAt, "html_url": run.Permalink,
				"pull_requests": []any{object{"number": countedNumber}},
			})
		}
	}
	if len(runs) != 2 {
		t.Fatalf("the captured answer holds %d ci-ok runs, want the pull_request run's and the dispatched run's", len(runs))
	}
	return runs
}

// countedWebhook is the check_run webhook of #3513 carrying run.
func countedWebhook(t *testing.T, run object) string {
	t.Helper()
	body, err := json.Marshal(object{
		"action":    "completed",
		"check_run": run,
		"sender":    object{"login": "github-actions[bot]", "type": "Bot"},
		"repository": object{
			"name": capturedName, "owner": object{"login": capturedOwner},
			"full_name": capturedRepository, "default_branch": "main",
		},
	})
	if err != nil {
		t.Fatalf("build the check_run webhook: %v", err)
	}
	return string(body)
}

// countedSettled is #3513's record once the summary loop has settled it and stopped writing it.
func countedSettled(t *testing.T, h *harness) cistore.State {
	t.Helper()
	var revision uint64
	var state *cistore.State
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		next, nextRevision := h.recordAt(capturedRepository, countedNumber, countedSHA)
		if next != nil && next.SettledEmitted && next.Claim == nil && nextRevision == revision {
			return *next
		}
		state, revision = next, nextRevision
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the head's record did not settle: %+v", state)
	return cistore.State{}
}

// lastSettlement is the last settlement of the head published to sub within a short wait, which
// must be one at least.
func lastSettlement(t *testing.T, sub *nats.Subscription, why string) cistore.Summary {
	t.Helper()
	var last *nats.Msg
	for {
		message, err := sub.NextMsg(time.Second)
		if err != nil {
			break
		}
		last = message
	}
	if last == nil {
		t.Fatalf("%s: no settlement was published", why)
	}
	var envelope contracts.Envelope
	if err := json.Unmarshal(last.Data, &envelope); err != nil {
		t.Fatalf("%s: the settlement is not an envelope: %v", why, err)
	}
	var summary cistore.Summary
	if err := json.Unmarshal([]byte(envelope.Payload), &summary); err != nil {
		t.Fatalf("%s: the settlement's payload is not JSON: %v", why, err)
	}
	return summary
}

// settledCiOK is the ci-ok run a settlement names, and whether it names ci-ok failing.
func settledCiOK(summary cistore.Summary) (id uint64, failing bool) {
	for _, run := range summary.CheckRuns {
		if run.Name == countedCheck {
			id = run.ID
		}
	}
	return id, slices.Contains(summary.Failed.Checks, countedCheck)
}

// A record a listener of an earlier release wrote -- it kept the newest check run of each name
// whatever started its run, so the workflow_dispatch run's ci-ok success stands for ci-ok -- is
// corrected by a pass over GitHub's answer for the head, which carries the event that started each
// suite's workflow run. The head's checks are then GitHub's own rollup of the pull request, check
// run for check run, ci-ok the pull_request run's failure among them; the dispatched run's suite
// is not in the record; no suite is named as one whose event the pass could not read; and the
// settlement the listener publishes next names ci-ok failing.
func TestAPassCountsAHeadsCheckRunsAsGitHubsRollupOfThePullRequestDoes(t *testing.T) {
	rollup := countedRollup(t)
	h := newHarness(t, reviewerAppID)
	h.mapRepository(capturedRepository)
	h.github.setRepository(capturedRepository, capturedRepository, 555)
	h.github.setPullRequests(capturedRepository, fakePull{number: countedNumber, sha: countedSHA})
	h.github.answerQueryWith(capturedRepository, captured(t, "graphql-head-3513.json"))

	subject := "notifications.github." + capturedOwner + "." + capturedName + ".pr." + countedNumberText + ".checks"
	settlements, err := h.bus.Conn.SubscribeSync(subject)
	if err != nil {
		t.Fatalf("subscribe to %s: %v", subject, err)
	}
	t.Cleanup(func() { _ = settlements.Unsubscribe() })

	for i, run := range countedCiOKRuns(t) {
		h.post("check_run", countedWebhook(t, run), fmt.Sprintf("earlier-release-%d", i))
	}
	countedSettled(t, h)
	if id, failing := settledCiOK(lastSettlement(t, settlements, "the earlier release's record")); id != countedDispatchCheck || failing {
		t.Fatalf("before the pass the settlement names ci-ok %d (failing %v), want the dispatched run's success %d, which a record of an earlier release holds",
			id, failing, uint64(countedDispatchCheck))
	}

	h.pass()

	state := countedSettled(t, h)
	var differ []string
	for name, want := range rollup {
		got, held := state.Checks[name]
		if !held || uint64(got.CheckRunID) != want.ID || got.Conclusion != want.Conclusion {
			differ = append(differ, fmt.Sprintf("%s: record %d %q (held %v), rollup %d %q", name, uint64(got.CheckRunID), got.Conclusion, held, want.ID, want.Conclusion))
		}
	}
	for name, got := range state.Checks {
		if _, counted := rollup[name]; !counted {
			differ = append(differ, fmt.Sprintf("%s: record %d %q, not in the rollup", name, uint64(got.CheckRunID), got.Conclusion))
		}
	}
	if len(differ) > 0 {
		slices.Sort(differ)
		t.Errorf("after the pass %d of the record's %d checks differ from GitHub's rollup of %d:\n  %s",
			len(differ), len(state.Checks), len(rollup), strings.Join(differ, "\n  "))
	}
	if check := state.Checks[countedCheck]; uint64(check.CheckRunID) != countedPullCheck || check.Conclusion != "failure" {
		t.Errorf("after the pass the head's ci-ok is %+v, want check run %d, the pull_request run's failure, as GitHub's rollup answers",
			check, uint64(countedPullCheck))
	}
	if suite, held := state.Suites[countedDispatchSuite]; held {
		t.Errorf("the record holds the workflow_dispatch run's suite %+v", suite)
	}
	if lines := h.lines(eventlessLine); len(lines) != 0 {
		t.Errorf("the pass named suites whose event it could not read, in an answer that carries every GitHub Actions suite's event: %v", lines)
	}
	if id, failing := settledCiOK(lastSettlement(t, settlements, "after the pass")); id != countedPullCheck || !failing {
		t.Errorf("after the pass the settlement names ci-ok %d (failing %v), want the pull_request run's failure %d",
			id, failing, uint64(countedPullCheck))
	}
}

// A GitHub Actions suite whose workflow run's event the answer does not carry is counted, as
// every suite was before the store read events, and the pass names it, once for the head. The
// captured page of #1827 was read before the query asked for workflowRun, so it carries no event:
// the pass names its Actions suite 101684831035, whose run "test" stands for its name, and not
// suite 101684758258, which is another App's and has no workflow run.
func TestAPassNamesTheSuitesWhoseEventItCouldNotRead(t *testing.T) {
	h := newHarness(t, reviewerAppID)
	h.mapRepository(capturedRepository)
	h.github.setRepository(capturedRepository, capturedRepository, 555)
	h.github.setPullRequests(capturedRepository, fakePull{number: capturedNumber, sha: capturedSHA})
	h.github.answerQueryWith(capturedRepository, captured(t, "graphql-page.json"))

	h.pass()

	lines := h.lines(eventlessLine)
	if len(lines) != 1 {
		t.Fatalf("the pass logged %d lines naming suites whose event it could not read, want one:\n%s", len(lines), h.logs.String())
	}
	line := lines[0]
	if line["level"] != "WARN" || line["repository"] != capturedRepository || line["pr"] != capturedNumberText || line["sha"] != capturedSHA {
		t.Errorf("the line names %v %v#%v at %v, want WARN for %s#%s at %s", line["level"], line["repository"], line["pr"], line["sha"],
			capturedRepository, capturedNumberText, capturedSHA)
	}
	var suites []string
	if listed, ok := line["suites"].([]any); ok {
		for _, suite := range listed {
			suites = append(suites, fmt.Sprint(suite))
		}
	}
	if !slices.Contains(suites, strconv.FormatInt(capturedSuiteID, 10)) || slices.Contains(suites, "101684758258") {
		t.Errorf("the line names suites %v, want GitHub Actions' suite %d and not another App's suite 101684758258", suites, int64(capturedSuiteID))
	}
	state := h.stateAt(capturedRepository, capturedNumber, capturedSHA)
	if state == nil {
		t.Fatal("the head has no record after the pass")
	}
	if check, held := state.Checks[capturedCheckName]; !held || uint64(check.CheckRunID) != uint64(capturedRunID) {
		t.Errorf("the record holds %s as %+v (held %v), want run %d of the suite whose event is unknown, counted", capturedCheckName, check, held, uint64(capturedRunID))
	}
}
