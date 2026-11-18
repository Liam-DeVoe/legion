// results_ci_each_head_test.go holds GET /api/v1/issues/{key}/results to R8: for a check CI
// proves, Dispatch derives one result at the head of each open pull request of the issue, from the
// newest check run of the named job at that head, read in the repository of that pull request's own
// row. A read that does not end within the deadline makes that head not known. The test reads the
// route's answer as JSON into the types of this file, so it builds at a commit before the rule and
// fails there on its own assertion.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The heads of this test: H1 and H2 of #70 and #71 in the issue's repository, and H3 of #9 of
// another repository the issue links.
const (
	ciEachHeadH1 = "c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1"
	ciEachHeadH2 = "c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2"
	ciEachHeadH3 = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
)

// ciEachHeadVerification is c1, a check whose ci_check names the job go-checks.
const ciEachHeadVerification = ":::check{#c1 negative_control=\"A build whose server does not build.\" ci_check=\"go-checks\"}\n" +
	"The server builds at the head.\n" +
	":::\n"

// ciEachHeadGitHub is GitHub's listing of check runs, keyed by repository and commit, with the
// count of the requests for each, and a hold that keeps the reads of one key waiting until their
// caller gives up.
type ciEachHeadGitHub struct {
	mu    sync.Mutex
	runs  map[string][]map[string]any
	asked map[string]int
	held  map[string]bool
}

func ciEachHeadKey(repo, sha string) string { return repo + "@" + sha }

// set is the runs GitHub lists for sha in repo. A key it holds no runs for answers 422, as GitHub
// does for a commit the repository does not hold.
func (g *ciEachHeadGitHub) set(repo, sha string, runs ...map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runs[ciEachHeadKey(repo, sha)] = runs
}

func (g *ciEachHeadGitHub) hold(repo, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held[ciEachHeadKey(repo, sha)] = true
}

func (g *ciEachHeadGitHub) requests(repo, sha string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.asked[ciEachHeadKey(repo, sha)]
}

func (g *ciEachHeadGitHub) serve(w http.ResponseWriter, r *http.Request) {
	key := ciEachHeadKey(r.PathValue("owner")+"/"+r.PathValue("repo"), r.PathValue("sha"))
	g.mu.Lock()
	g.asked[key]++
	runs, listed := g.runs[key]
	held := g.held[key]
	g.mu.Unlock()
	if held {
		<-r.Context().Done()
		return
	}
	if !listed {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprintf(w, `{"message":"No commit found for SHA: %s"}`, r.PathValue("sha"))
		return
	}
	if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page > 1 {
		runs = nil
	}
	body, _ := json.Marshal(map[string]any{"total_count": len(runs), "check_runs": runs})
	_, _ = w.Write(body)
}

// ciEachHeadSubject is one subject of c1's pass_stands, with only the members this test reads.
type ciEachHeadSubject struct {
	Basis struct {
		Commit string `json:"commit"`
	} `json:"basis"`
	Answer string `json:"answer"`
	Reason string `json:"reason"`
}

// ciEachHeadRead is c1's subjects as the results read answers them, each by its commit.
func ciEachHeadRead(t *testing.T, handler http.Handler, issue string) map[string]ciEachHeadSubject {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/issues/"+issue+"/results", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the results of %s: status=%d body=%s", issue, response.Code, response.Body.String())
	}
	var read struct {
		Checks []struct {
			BlockID    string `json:"block_id"`
			PassStands struct {
				Subjects []ciEachHeadSubject `json:"subjects"`
			} `json:"pass_stands"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode the results of %s: %v", issue, err)
	}
	for _, check := range read.Checks {
		if check.BlockID == "c1" {
			subjects := map[string]ciEachHeadSubject{}
			for _, subject := range check.PassStands.Subjects {
				subjects[subject.Basis.Commit] = subject
			}
			return subjects
		}
	}
	t.Fatalf("the read of %s holds no check c1: %s", issue, response.Body.String())
	return nil
}

// ciEachHeadWants fails the test unless subjects names head with answer for reason.
func ciEachHeadWants(t *testing.T, what string, subjects map[string]ciEachHeadSubject, head, answer, reason string) {
	t.Helper()
	subject, named := subjects[head]
	if !named {
		t.Fatalf("%s: c1 names no subject at %s, only %+v", what, head, subjects)
	}
	if subject.Answer != answer || subject.Reason != reason {
		t.Fatalf("%s: the head at %s reads %q (reason %q), want %q (reason %q)", what, head, subject.Answer, subject.Reason, answer, reason)
	}
}

// TestACheckCIProvesDerivesAResultAtEachOpenHead: R8. Dispatch reads the named job's newest check
// run at each open head, each in its own pull request's repository, and a read that does not end
// within the deadline makes that head alone not known.
func TestACheckCIProvesDerivesAResultAtEachOpenHead(t *testing.T) {
	github := &ciEachHeadGitHub{runs: map[string][]map[string]any{}, asked: map[string]int{}, held: map[string]bool{}}
	handler, database, _ := resultsGitHubServed(t, map[string]bool{"acme/widgets": true, "acme/infra": true}, nil, "main",
		func(string, string) (int, string) { return http.StatusOK, "diverged" }, github.serve)
	issue := issueInRepository(t, handler, "RESEH", "acme/widgets", "A check CI proves at each head")
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", ciEachHeadVerification)
	writeHead(t, database, "acme/widgets", 70, ciEachHeadH1, "open", issue.Key)
	writeHead(t, database, "acme/widgets", 71, ciEachHeadH2, "open", issue.Key)

	github.set("acme/widgets", ciEachHeadH1, completedRun(51, "go-checks", "success"))
	github.set("acme/widgets", ciEachHeadH2, completedRun(52, "go-checks", "success"))
	subjects := ciEachHeadRead(t, handler, issue.Key)
	ciEachHeadWants(t, "a success at H1", subjects, ciEachHeadH1, "yes", "")
	ciEachHeadWants(t, "a success at H2", subjects, ciEachHeadH2, "yes", "")
	if h1, h2 := github.requests("acme/widgets", ciEachHeadH1), github.requests("acme/widgets", ciEachHeadH2); h1 != 1 || h2 != 1 {
		t.Fatalf("one read asked GitHub %d times at H1 and %d at H2, want once at each", h1, h2)
	}

	github.set("acme/widgets", ciEachHeadH1, completedRun(53, "go-checks", "failure"))
	ciEachHeadWants(t, "a failure at H1", ciEachHeadRead(t, handler, issue.Key), ciEachHeadH1, "no", "fail")

	github.set("acme/widgets", ciEachHeadH2, completedRun(54, "go-checks", "failure"), completedRun(55, "go-checks", "success"))
	ciEachHeadWants(t, "the newer run's success at H2", ciEachHeadRead(t, handler, issue.Key), ciEachHeadH2, "yes", "")

	t.Run("each head in its own repository", func(t *testing.T) {
		writeHead(t, database, "acme/infra", 9, ciEachHeadH3, "open", issue.Key)
		github.set("acme/infra", ciEachHeadH3, completedRun(56, "go-checks", "success"))
		ciEachHeadWants(t, "a success at H3 in acme/infra", ciEachHeadRead(t, handler, issue.Key), ciEachHeadH3, "yes", "")
		if infra, widgets := github.requests("acme/infra", ciEachHeadH3), github.requests("acme/widgets", ciEachHeadH3); infra != 1 || widgets != 0 {
			t.Fatalf("H3 was read %d times in acme/infra and %d in acme/widgets, want once in its own repository alone", infra, widgets)
		}
	})

	t.Run("a read that does not end", func(t *testing.T) {
		shortenCIReadDeadline(t, 300*time.Millisecond)
		github.hold("acme/widgets", ciEachHeadH2)
		started := time.Now()
		subjects := ciEachHeadRead(t, handler, issue.Key)
		if took := time.Since(started); took > 5*time.Second {
			t.Fatalf("the read with H2 held took %s, want it bounded by the deadline", took)
		}
		ciEachHeadWants(t, "H2 held past the deadline", subjects, ciEachHeadH2, "unknown", "read_failed")
		ciEachHeadWants(t, "H1 beside the held H2", subjects, ciEachHeadH1, "no", "fail")
	})
}
