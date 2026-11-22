// results_cited_pull_request_test.go holds GET /api/v1/issues/{key}/results to the attribution of
// a pull request whose body names another pull request by number. GitHub numbers a repository's
// issues and pull requests in one sequence, and Dispatch stores a link to the reference
// owner/repo#n as both .../issues/n and .../pull/n, so "#53 renamed the helper" in a body reads as
// a citation of a GitHub issue that another issue links. Where Dispatch holds pull request #53,
// that mention cites no issue, and the pull request stays the issue its branch names. The file
// drives the routes alone, with helpers main holds and no writer of a build's own, so it runs
// unchanged at any build that serves the read-through, and fails on its own assertions where the
// rule is dropped.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// The pull requests of acme/widgets these cases read: the issue's own, one stacked on its branch,
// the other issue's pull request a body names, and the GitHub issue a body cites.
const (
	citedOwn    = 51
	citedOnTop  = 52
	citedPull   = 53
	citedIssue  = 54
	citedOwnSHA = "5151515151515151515151515151515151515151"
	citedTopSHA = "5252525252525252525252525252525252525252"
	citedPRSHA  = "5353535353535353535353535353535353535353"
)

// citedWorld is a Dispatch whose GitHub names, for each head commit, one pull request of
// acme/widgets: the issue's own #51 (branch legion/<issue>, base main, body ownBody), #52 stacked
// on its branch (legion/<issue>-snippet-sentinels), and the other issue's #53 (legion/<other>).
// Both issues carry siblingChecks; the other issue links acme/widgets#linked as Dispatch stores a
// reference, in both URL forms.
type citedWorld struct {
	t       *testing.T
	handler http.Handler
	issue   string
	other   string
}

func newCitedWorld(t *testing.T, project, ownBody string, linked int) citedWorld {
	t.Helper()
	handler, database, _, fake := readThroughGitHub(t, "acme/widgets")
	issue := issueInRepository(t, handler, project, "acme/widgets", "A pull request that names another")
	response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues",
		map[string]any{"project": project, "title": "The other issue's work", "spec": "A spec\n"}, "alice")
	if response.Code != http.StatusCreated {
		t.Fatalf("create the other issue: status=%d body=%s", response.Code, response.Body.String())
	}
	other := decodeBody[model.Issue](t, response).Key
	for _, key := range []string{issue.Key, other} {
		uploadDocument(t, handler, "/api/v1/issues/"+key, "verification.md", siblingChecks)
	}
	if _, err := database.Pool.Exec(context.Background(), `
		insert into issue_external_links (issue_key, url, kind) values
			($1, 'https://github.com/acme/widgets/issues/' || $2, 'github_issue'),
			($1, 'https://github.com/acme/widgets/pull/' || $2, 'url')`, other, strconv.Itoa(linked)); err != nil {
		t.Fatalf("link acme/widgets#%d to %s: %v", linked, other, err)
	}
	pulls := map[string]struct {
		number     int
		head, base string
		body       string
	}{
		citedOwnSHA: {citedOwn, "legion/" + issue.Key, "main", ownBody},
		citedTopSHA: {citedOnTop, "legion/" + issue.Key + "-snippet-sentinels", "legion/" + issue.Key, ""},
		citedPRSHA:  {citedPull, "legion/" + other, "main", ""},
	}
	fake.answers(func(repo, sha string) string {
		pull, named := pulls[sha]
		if !named {
			return `[]`
		}
		body, err := json.Marshal(pull.body)
		if err != nil {
			t.Errorf("encode the body of #%d: %v", pull.number, err)
		}
		return fmt.Sprintf(`[{"number":%d,"title":"A change","body":%s,"html_url":"https://github.com/%s/pull/%d",
			"state":"open","merged":false,"merged_at":null,"updated_at":"2026-11-22T07:00:00Z",
			"head":{"ref":%q,"sha":%q,"repo":{"full_name":%q}},"base":{"ref":%q}}]`,
			pull.number, body, repo, pull.number, pull.head, sha, repo, pull.base)
	})
	return citedWorld{t: t, handler: handler, issue: issue.Key, other: other}
}

// pass records the tester's pass of k1 of key at commit. The record route reads the pull requests
// GitHub names for a commit that heads none Dispatch holds for key, and stores each, so a pass is
// how a case makes Dispatch hold a pull request, as production's results do.
func (w citedWorld) pass(key, commit string) {
	w.t.Helper()
	response := recordResult(w.t, w.handler, key, resultBody("k1", "pass", commit, map[string]any{"role": "tester"}))
	if response.Code != http.StatusCreated {
		w.t.Fatalf("record the tester's pass of %s k1 at %s: status=%d body=%s", key, commit, response.Code, response.Body.String())
	}
}

// comparedWith is the commits each check of key compared its results with, sorted, by check.
func (w citedWorld) comparedWith(key string) map[string][]string {
	w.t.Helper()
	read := readResults(w.t, w.handler, key)
	if len(read.Checks) != 2 {
		w.t.Fatalf("the results of %s hold %d checks, want siblingChecks' two: %+v", key, len(read.Checks), read)
	}
	by := map[string][]string{}
	for _, check := range read.Checks {
		commits := []string{}
		for _, basis := range check.ComparedWith {
			commits = append(commits, basis.Commit)
		}
		slices.Sort(commits)
		by[check.BlockID] = commits
	}
	return by
}

// requireCompared fails the test unless every check of key compared with want alone.
func (w citedWorld) requireCompared(key string, why string, want ...string) {
	w.t.Helper()
	slices.Sort(want)
	for check, got := range w.comparedWith(key) {
		if !slices.Equal(got, want) {
			w.t.Errorf("%s's %s compared with %v, want %v: %s", key, check, got, want, why)
		}
	}
}

// The issue's own pull request #51 names the other issue's pull request #53 by number in its body,
// and the other issue links acme/widgets#53 as .../issues/53 and .../pull/53. Dispatch holds #53
// (a pass at its head is recorded first), so "#53" cites no GitHub issue: #51 stays the issue's by
// its branch, beside #52, whose branch names the issue too, and every check of the issue compares
// with both heads. Where the citation of #53 still counted as source 2, which ranks above the
// branch, #51 went to the other issue, and the issue's checks compared with #52's head alone. The
// other issue's checks compare with #53's head alone either way.
//
// Control: a body citing #54, which the other issue links and which is no pull request Dispatch
// holds, still gives #51 to the other issue by the citation, which outranks the branch: the rule
// leaves a real citation of a GitHub issue standing.
func TestABodyNamingAPullRequestByNumberLeavesThePullRequestItsBranchsIssue(t *testing.T) {
	t.Run("the body names a pull request Dispatch holds", func(t *testing.T) {
		world := newCitedWorld(t, "CITEPR",
			fmt.Sprintf("#%d renamed the helper, so this change calls it by its new name.", citedPull), citedPull)
		world.pass(world.other, citedPRSHA)
		world.pass(world.issue, citedOwnSHA)
		world.pass(world.issue, citedTopSHA)
		world.requireCompared(world.issue,
			fmt.Sprintf("#%d is a pull request Dispatch holds, so #%d's body cites no GitHub issue and its branch names the issue", citedPull, citedOwn),
			citedOwnSHA, citedTopSHA)
		world.requireCompared(world.other, fmt.Sprintf("the other issue's one pull request is #%d", citedPull), citedPRSHA)
	})
	t.Run("control: the body cites a GitHub issue the other issue links", func(t *testing.T) {
		world := newCitedWorld(t, "CITEIS", fmt.Sprintf("Fixes #%d.", citedIssue), citedIssue)
		world.pass(world.issue, citedOwnSHA)
		world.pass(world.issue, citedTopSHA)
		world.requireCompared(world.issue,
			fmt.Sprintf("#%d is no pull request Dispatch holds, so #%d's body cites the GitHub issue the other issue links, which ranks above the branch", citedIssue, citedOwn),
			citedTopSHA)
	})
}
