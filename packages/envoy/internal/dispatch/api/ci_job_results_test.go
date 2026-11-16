// ci_job_results_test.go holds POST /api/v1/issues/{key}/results to R12 of the spec for a check
// CI proves: the CI job that its ci_check names records the check's result there with its own
// results token, and Dispatch takes who recorded it, the commit and the branch from that token
// and from GitHub, never from the body. A role still records no result for such a check, and the
// job's result never takes the check away from CI.
package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/docs"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp/githubapptest"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/githubtest"
)

const (
	// ciJobProject is the project of the job's results token and of the issue it records on.
	ciJobProject = "ACME"
	// ciJobRepository is the repository of the job's run, which the project maps as its default.
	ciJobRepository = "example-org/example-repo"
	// ciJobCommit is the commit the job's run tested, the commit of its token.
	ciJobCommit = "0123456789abcdef0123456789abcdef01234567"
	// ciJobOtherCommit is a commit the token does not name.
	ciJobOtherCommit = "89abcdef0123456789abcdef0123456789abcdef"
	// ciJobHead is the head of the issue's open pull request, which the job did not test.
	ciJobHead = "fedcba9876543210fedcba9876543210fedcba98"
	// ciJobCheckRun is the check run of the job the default token was minted in.
	ciJobCheckRun = 97
	// ciJobName is the name of that check run, and the ci_check of the check c1.
	ciJobName = "go-checks"
	// ciJobRef is the branch push the job ran on, and ciJobBranch that branch.
	ciJobRef    = "refs/heads/legion/ACME-1"
	ciJobBranch = "legion/ACME-1"
	// ciJobNegativeControl is the negative control both checks name.
	ciJobNegativeControl = "A build whose server does not build."
)

// ciJobVerification is the Verification document of the job's issue: c1, a check CI proves whose
// ci_check names the job, and k1, an ordinary check.
var ciJobVerification = ciCheckBlock("c1", `negative_control="`+ciJobNegativeControl+`" ci_check="`+ciJobName+`"`) +
	"\n" + ciCheckBlock("k1", `negative_control="`+ciJobNegativeControl+`"`)

// ciJobSession is the session the job's results token proves (resultsOnlyToken).
var ciJobSession = resultsOnlyToken("", ciJobProject).Session

// ciJobGitHub is the GitHub the route reads for a job's result: the installation of every
// repository, the check runs of a job by id, the commits a repository holds (every one), and the
// check runs listed at a pull request head, which the derived result of the read is made from.
type ciJobGitHub struct {
	mu        sync.Mutex
	checkRuns map[int64]specCheckRunAnswer
	listings  *checkRunsFake
}

// setCheckRun has GitHub name check run id name, at commit headSHA.
func (g *ciJobGitHub) setCheckRun(id int64, name, headSHA string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checkRuns[id] = specCheckRunAnswer{name: name, headSHA: headSHA}
}

func (g *ciJobGitHub) serve(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":77,"app_slug":"dispatch-test","permissions":{"contents":"read","pull_requests":"read","checks":"read"}}`)
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"ghs_fake_77","expires_at":"2099-01-01T00:00:00Z"}`)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"full_name":%q,"default_branch":"main"}`, r.PathValue("owner")+"/"+r.PathValue("repo"))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"sha":%q}`, r.PathValue("sha"))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/compare/{basehead}", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":"diverged"}`)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/check-runs", g.listings.serve)
	mux.HandleFunc("GET /repos/{owner}/{repo}/check-runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		var id int64
		fmt.Sscan(r.PathValue("id"), &id)
		run, ok := g.checkRuns[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprintf(w, `{"id":%d,"name":%q,"head_sha":%q,"status":"in_progress"}`, id, run.name, run.headSHA)
	})
	return githubtest.Server(t, githubtest.Spec{Handle: mux.ServeHTTP})
}

// ciJobRig is a Dispatch whose verifier serves results tokens as cmd/dispatch's does, a GitHub
// that holds the job's check run, and issue ACME-1 of project ACME, whose repository is the run's
// and whose Verification document, verification, is ciJobVerification.
type ciJobRig struct {
	*brokerTokenRig
	github       *ciJobGitHub
	caller       *brokerCaller
	tokens       int
	issue        string
	verification string
}

func newCIJobRig(t *testing.T) *ciJobRig {
	t.Helper()
	github := &ciJobGitHub{
		checkRuns: map[int64]specCheckRunAnswer{ciJobCheckRun: {name: ciJobName, headSHA: ciJobCommit}},
		listings:  newCheckRunsFake(),
	}
	_, pemText := githubapptest.Key(t)
	rig := &ciJobRig{
		brokerTokenRig: newBrokerTokenRigOn(t, testServerOptions{
			app:           &auth.AppConfig{ClientID: "Iv1.test", PEM: pemText},
			githubAPIBase: github.serve(t),
		}),
		github: github,
		caller: newBrokerCaller(t),
	}
	rig.issue, rig.verification = rig.issueOf(t, ciJobProject, ciJobRepository)
	return rig
}

// issueOf creates project, maps repository to it as its default, and files one issue of it whose
// Verification document is ciJobVerification. It answers the issue and that document.
func (r *ciJobRig) issueOf(t *testing.T, project, repository string) (string, string) {
	t.Helper()
	if created := r.asPerson(t, http.MethodPost, "/api/v1/projects", map[string]any{"key": project, "name": project}); created.Code != http.StatusCreated {
		t.Fatalf("create project %s: status=%d body=%s", project, created.Code, created.Body.String())
	}
	if mapped := r.asPerson(t, http.MethodPut, "/api/v1/settings/repo-projects/"+repository, map[string]any{"project": project, "default": true}); mapped.Code != http.StatusOK {
		t.Fatalf("map %s to %s: status=%d body=%s", repository, project, mapped.Code, mapped.Body.String())
	}
	filed := r.asPerson(t, http.MethodPost, "/api/v1/issues", map[string]any{"project": project, "title": "Proved by a CI job", "spec": "A spec\n"})
	if filed.Code != http.StatusCreated {
		t.Fatalf("create an issue of %s: status=%d body=%s", project, filed.Code, filed.Body.String())
	}
	key := decodeBody[model.Issue](t, filed).Key
	return key, uploadDocument(t, r.handler, "/api/v1/issues/"+key, "verification.md", ciJobVerification).Artifact.ID
}

// jobToken has the broker answer a results token of project ACME for the job's run on a push of
// ciJobRef, with change applied to the answer, and answers the token.
func (r *ciJobRig) jobToken(change func(*brokertoken.Introspection)) string {
	answer := resultsOnlyToken(r.caller.jkt, ciJobProject)
	answer.CI.Ref = ciJobRef
	answer.CI.WorkflowRef = ciJobRepository + "/.github/workflows/ci-results.yaml@" + ciJobRef
	if change != nil {
		change(&answer)
	}
	return r.says(answer)
}

// sessionToken has the broker answer a session's own token of project ACME, and answers it.
func (r *ciJobRig) sessionToken() string {
	answer := liveNamedToken(r.caller.jkt)
	answer.Project = ciJobProject
	return r.says(answer)
}

func (r *ciJobRig) says(answer brokertoken.Introspection) string {
	r.tokens++
	token := fmt.Sprintf("a-token-of-the-ci-job-rig-%d", r.tokens)
	r.broker.Says(token, answer)
	return token
}

// post records body on issue's results route under token.
func (r *ciJobRig) post(t *testing.T, issue, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return r.serve(r.caller.presenting(t, http.MethodPost, "/api/v1/issues/"+issue+"/results", token, body))
}

// jobResultOf is the body a job writes for check: its verdict at the commit it tested.
func jobResultOf(check, verdict string) map[string]any {
	return map[string]any{"check": check, "commit": ciJobCommit, "verdict": verdict, "observed": "ok"}
}

// stored is how many rows check_results holds.
func (r *ciJobRig) stored(t *testing.T) int {
	t.Helper()
	var count int
	if err := r.database.Pool.QueryRow(context.Background(), `select count(*) from check_results`).Scan(&count); err != nil {
		t.Fatalf("count check_results: %v", err)
	}
	return count
}

// refusedWith fails the test unless response is status with code, and no result and no
// result.recorded event were stored. It answers the refusal's text.
func (r *ciJobRig) refusedWith(t *testing.T, what string, response *httptest.ResponseRecorder, status int, code string) string {
	t.Helper()
	got, text := refusalOf(t, response)
	if response.Code != status || got != code {
		t.Fatalf("%s: status=%d body=%s, want %d %s", what, response.Code, response.Body.String(), status, code)
	}
	if held := r.stored(t); held != 0 {
		t.Fatalf("%s left %d results, want none", what, held)
	}
	if events := resultEventsOn(t, r.database, r.issue); events != 0 {
		t.Fatalf("%s left %d result.recorded events on %s, want none", what, events, r.issue)
	}
	return text
}

// jobResult is one result as the wire carries it, read apart from the server's own type, so
// each test reads the names a client reads.
type jobResult struct {
	ID              string      `json:"id"`
	IssueKey        string      `json:"issue_key"`
	CheckIssueKey   string      `json:"check_issue_key"`
	CheckBlockID    string      `json:"check_block_id"`
	Role            string      `json:"role"`
	Run             string      `json:"run"`
	Surface         string      `json:"surface"`
	NegativeControl string      `json:"negative_control"`
	Branch          string      `json:"branch"`
	Session         string      `json:"session"`
	Actor           model.Actor `json:"actor"`
	CommitSHA       string      `json:"commit_sha"`
	Verdict         string      `json:"verdict"`
	Observed        string      `json:"observed"`
	CreatedAt       time.Time   `json:"created_at"`
}

// jobRecorded decodes a 201 answer of the record route, failing the test on any other.
func jobRecorded(t *testing.T, what string, response *httptest.ResponseRecorder) jobResult {
	t.Helper()
	if response.Code != http.StatusCreated {
		t.Fatalf("%s: status=%d body=%s, want 201", what, response.Code, response.Body.String())
	}
	return decodeBody[struct {
		Result jobResult `json:"result"`
	}](t, response).Result
}

// jobCheckRead is one check of the results read as the wire carries it.
type jobCheckRead struct {
	BlockID string         `json:"block_id"`
	CICheck string         `json:"ci_check"`
	CI      *model.CheckCI `json:"ci"`
	Results []jobResult    `json:"results"`
}

// readJobCheck is check blockID of the issue's results read, as a person reads it.
func (r *ciJobRig) readJobCheck(t *testing.T, blockID string) jobCheckRead {
	t.Helper()
	response := dispatchRequest(t, r.handler, http.MethodGet, "/api/v1/issues/"+r.issue+"/results", nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the results of %s: status=%d body=%s", r.issue, response.Code, response.Body.String())
	}
	for _, check := range decodeBody[struct {
		Checks []jobCheckRead `json:"checks"`
	}](t, response).Checks {
		if check.BlockID == blockID {
			return check
		}
	}
	t.Fatalf("the results of %s hold no check %s", r.issue, blockID)
	return jobCheckRead{}
}

// TestTheJobACheckNamesRecordsItsResult: the job that c1's ci_check names records c1's result
// with its own token. The row names role ci and the job's check run as its run, the branch the
// job ran on, the session the token proved and the commit of the token; the body named none of
// them. One result.recorded event goes on the issue.
func TestTheJobACheckNamesRecordsItsResult(t *testing.T) {
	rig := newCIJobRig(t)
	result := jobRecorded(t, "the job's result for c1", rig.post(t, rig.issue, rig.jobToken(nil), jobResultOf("c1", "pass")))

	want := jobResult{
		ID: result.ID, IssueKey: rig.issue, CheckIssueKey: rig.issue, CheckBlockID: "c1",
		Role: "ci", Run: ciJobName, Surface: "", NegativeControl: ciJobNegativeControl,
		Branch: ciJobBranch, Session: ciJobSession, CommitSHA: ciJobCommit, Verdict: "pass", Observed: "ok",
	}
	got := result
	got.Actor, got.CreatedAt = model.Actor{}, time.Time{}
	if got != want {
		t.Fatalf("the stored result = %+v, want %+v", got, want)
	}
	if result.Actor.Kind != "session" || result.Actor.ID != ciJobSession || result.Actor.Owner == nil || *result.Actor.Owner != "ada@example.com" {
		t.Fatalf("the result's actor = %+v, want session %s acting for ada@example.com", result.Actor, ciJobSession)
	}

	var role, run, branch, session, commit string
	if err := rig.database.Pool.QueryRow(context.Background(), `
		select role, run, coalesce(branch, ''), session, commit_sha from check_results where id = $1::uuid
	`, result.ID).Scan(&role, &run, &branch, &session, &commit); err != nil {
		t.Fatalf("read the stored row: %v", err)
	}
	if role != "ci" || run != ciJobName || branch != ciJobBranch || session != ciJobSession || commit != ciJobCommit {
		t.Fatalf("the row holds role %q, run %q, branch %q, session %q, commit %s; want ci, %s, %s, %s, %s",
			role, run, branch, session, commit, ciJobName, ciJobBranch, ciJobSession, ciJobCommit)
	}
	if events := resultEventsOn(t, rig.database, rig.issue); events != 1 {
		t.Fatalf("%s holds %d result.recorded events, want one", rig.issue, events)
	}
}

// TestARoleStillCannotRecordACheckOfACIJob: a session's tester and implementer results against
// c1 are refused, 409 CHECK_PROVED_BY_CI, saying that only a run of the job records its result.
func TestARoleStillCannotRecordACheckOfACIJob(t *testing.T) {
	rig := newCIJobRig(t)
	want := `check c1 names CI job "` + ciJobName + `": only a run of that job records its result, through the secrets broker`
	for _, role := range []string{"tester", "implementer"} {
		text := rig.refusedWith(t, "the "+role+"'s result for c1",
			rig.post(t, rig.issue, rig.sessionToken(), resultBody("c1", "pass", ciJobCommit, map[string]any{"role": role})),
			http.StatusConflict, "CHECK_PROVED_BY_CI")
		if text != want {
			t.Fatalf("the %s's refusal reads %q, want %q", role, text, want)
		}
	}
}

// TestAResultsTokenOfAnotherJobIsRefused: a job records only the result of a check whose ci_check
// names its own check run. The run of envoy-go is refused c1, and the run of go-checks is
// refused k1, which names no ci_check.
func TestAResultsTokenOfAnotherJobIsRefused(t *testing.T) {
	rig := newCIJobRig(t)
	rig.github.setCheckRun(98, "envoy-go", ciJobCommit)
	other := rig.jobToken(func(answer *brokertoken.Introspection) { answer.CI.CheckRunID = "98" })
	text := rig.refusedWith(t, "the envoy-go run's result for c1", rig.post(t, rig.issue, other, jobResultOf("c1", "pass")),
		http.StatusConflict, "CHECK_NOT_THIS_JOBS")
	for _, name := range []string{"c1", `"` + ciJobName + `"`, `"envoy-go"`} {
		if !strings.Contains(text, name) {
			t.Fatalf("the refusal %q does not name %s", text, name)
		}
	}

	text = rig.refusedWith(t, "the go-checks run's result for k1", rig.post(t, rig.issue, rig.jobToken(nil), jobResultOf("k1", "pass")),
		http.StatusConflict, "CHECK_NOT_THIS_JOBS")
	for _, name := range []string{"k1", "no ci_check", `"` + ciJobName + `"`} {
		if !strings.Contains(text, name) {
			t.Fatalf("the refusal %q does not name %s", text, name)
		}
	}
}

// TestAJobsResultAtAnotherCommitIsRefused: a job records a result only for the commit its run
// tested, and only through a check run that GitHub names at that commit. Each refusal names both
// commits.
func TestAJobsResultAtAnotherCommitIsRefused(t *testing.T) {
	rig := newCIJobRig(t)
	body := jobResultOf("c1", "pass")
	body["commit"] = ciJobOtherCommit
	text := rig.refusedWith(t, "a result for another commit", rig.post(t, rig.issue, rig.jobToken(nil), body),
		http.StatusForbidden, "COMMIT_NOT_THE_RUNS")
	if !strings.Contains(text, ciJobOtherCommit) || !strings.Contains(text, ciJobCommit) {
		t.Fatalf("the refusal %q names not both commits", text)
	}

	rig.github.setCheckRun(99, ciJobName, ciJobOtherCommit)
	moved := rig.jobToken(func(answer *brokertoken.Introspection) { answer.CI.CheckRunID = "99" })
	text = rig.refusedWith(t, "a check run of another commit", rig.post(t, rig.issue, moved, jobResultOf("c1", "pass")),
		http.StatusForbidden, "CHECK_RUN_NOT_THE_RUNS")
	if !strings.Contains(text, ciJobOtherCommit) || !strings.Contains(text, ciJobCommit) {
		t.Fatalf("the refusal %q names not both commits", text)
	}
}

// TestAJobsResultOfARunOfNoBranchIsRefused: a job records a result for the push of a branch, and
// its row names that branch, which is what keeps the row out of the fourth rule of its check. A
// run of a tag, and a ref that names an empty branch, are refused, and nothing is stored.
func TestAJobsResultOfARunOfNoBranchIsRefused(t *testing.T) {
	rig := newCIJobRig(t)
	for _, ref := range []string{"refs/tags/v1.0.0", "refs/heads/"} {
		token := rig.jobToken(func(answer *brokertoken.Introspection) {
			answer.CI.Ref = ref
			answer.CI.WorkflowRef = ciJobRepository + "/.github/workflows/ci-results.yaml@" + ref
		})
		text := rig.refusedWith(t, "a run of "+ref, rig.post(t, rig.issue, token, jobResultOf("c1", "pass")),
			http.StatusForbidden, "REF_NOT_A_BRANCH")
		if !strings.Contains(text, ref) {
			t.Fatalf("the refusal %q does not name the ref %s", text, ref)
		}
	}
}

// TestAJobOfAnotherRepositoryIsRefused: the token's run is of a repository that the issue does not
// resolve to, so its commit is no commit of the issue.
func TestAJobOfAnotherRepositoryIsRefused(t *testing.T) {
	rig := newCIJobRig(t)
	const other = "example-org/other-repo"
	token := rig.jobToken(func(answer *brokertoken.Introspection) {
		answer.Session = "ci-results:" + other + "/.github/workflows/ci-results.yaml"
		answer.CI.Repository = other
		answer.CI.WorkflowRef = other + "/.github/workflows/ci-results.yaml@" + ciJobRef
	})
	text := rig.refusedWith(t, "a run of another repository", rig.post(t, rig.issue, token, jobResultOf("c1", "pass")),
		http.StatusForbidden, "REPOSITORY_NOT_THE_ISSUES")
	if !strings.Contains(text, other) || !strings.Contains(text, ciJobRepository) {
		t.Fatalf("the refusal %q names not both repositories", text)
	}
}

// TestAJobsResultCarriesNoFieldOfARole: the identity on a result is the one the token proved,
// never a field the caller set, so a job's body that names any field of a role's result is
// refused by name.
func TestAJobsResultCarriesNoFieldOfARole(t *testing.T) {
	rig := newCIJobRig(t)
	for name, value := range map[string]any{
		"role":             "tester",
		"actor":            map[string]any{"kind": "session", "id": ciJobSession},
		"run":              "go test ./...",
		"surface":          "api",
		"negative_control": ciJobNegativeControl,
		"failed":           1,
		"skipped":          1,
		"excluded":         1,
		"screenshots":      []string{"dispatch://" + rig.issue + "/artifact/screen"},
	} {
		body := jobResultOf("c1", "pass")
		body[name] = value
		text := rig.refusedWith(t, "a job's result naming "+name, rig.post(t, rig.issue, rig.jobToken(nil), body),
			http.StatusBadRequest, "RESULT_INPUT")
		if !strings.Contains(text, name) {
			t.Fatalf("the refusal of a body naming %s reads %q, which does not name it", name, text)
		}
	}
}

// TestAResultsTokenOfAnotherProjectIsRefused: a results token records on the issues of its own
// project alone.
func TestAResultsTokenOfAnotherProjectIsRefused(t *testing.T) {
	rig := newCIJobRig(t)
	other, _ := rig.issueOf(t, "OTHER", "example-org/other-repo")
	response := rig.post(t, other, rig.jobToken(nil), jobResultOf("c1", "pass"))
	if code, _ := refusalOf(t, response); response.Code != http.StatusForbidden || code != codeProjectScope {
		t.Fatalf("a result for %s under a token of %s: status=%d body=%s, want 403 %s",
			other, ciJobProject, response.Code, response.Body.String(), codeProjectScope)
	}
	if held := rig.stored(t); held != 0 {
		t.Fatalf("the refused result left %d results, want none", held)
	}
}

// TestAJobsResultLeavesTheCheckCIs: the job's result is no result a role recorded, so c1 stays a
// check CI proves. An edit elsewhere in the document leaves it a settlement to owe, which holds
// every check block of the document to the four rules (reconcileCICheckBlocks); that settlement
// keeps c1's ci_check and appends no block.invalid. A write that changes c1's negative control,
// which holds c1 to the four rules again (refuseCICheckBlocks), is stored. A settlement with
// nothing owed judges no block, so the edit is what makes the first half able to fail.
func TestAJobsResultLeavesTheCheckCIs(t *testing.T) {
	rig := newCIJobRig(t)
	jobRecorded(t, "the job's result for c1", rig.post(t, rig.issue, rig.jobToken(nil), jobResultOf("c1", "pass")))
	documents := rig.deps.Docs.(*docs.Service)

	if edited := applyOp(t, rig.handler, rig.verification, map[string]any{
		"op": "insert", "after": "end", "markdown": "A note below the checks.\n",
	}); edited.Code != http.StatusOK {
		t.Fatalf("an edit beside c1: status=%d body=%s, want 200", edited.Code, edited.Body.String())
	}
	if err := documents.SettleIssueDocuments(context.Background(), rig.issue); err != nil {
		t.Fatalf("settle the issue's documents: %v", err)
	}
	if check := rig.readJobCheck(t, "c1"); check.CICheck != ciJobName {
		t.Errorf("c1 once settled reads ci_check %q, want %q kept", check.CICheck, ciJobName)
	}
	if events := ciCheckRefusalEvents(t, rig.handler, rig.issue); len(events) != 0 {
		t.Errorf("settlement appended block.invalid %+v, want none", events)
	}

	changed := ciCheckBlock("c1", `negative_control="A build whose server fails to start." ci_check="`+ciJobName+`"`) +
		"\n" + ciCheckBlock("k1", `negative_control="`+ciJobNegativeControl+`"`) + "\nA note below the checks.\n"
	if written := uploadVerification(t, rig.handler, rig.issue, changed); written.Code != http.StatusCreated {
		t.Fatalf("a write changing c1's negative control: status=%d body=%s, want 201", written.Code, written.Body.String())
	}
	if err := documents.SettleIssueDocuments(context.Background(), rig.issue); err != nil {
		t.Fatalf("settle the issue's documents again: %v", err)
	}
	if check := rig.readJobCheck(t, "c1"); check.CICheck != ciJobName {
		t.Fatalf("c1 after the write reads ci_check %q, want %q kept", check.CICheck, ciJobName)
	}
	if events := ciCheckRefusalEvents(t, rig.handler, rig.issue); len(events) != 0 {
		t.Fatalf("the write left block.invalid %+v, want none", events)
	}
}

// TestTheReadShowsTheJobsResultBesideTheDerivedOne: c1's read carries its ci member, read at the
// head of the issue's pull request, and every result: the one derived from the run at that head
// and each one the job recorded at the commit it tested, newest first. The derived run concluded
// between the job's two results, so a read that put it first or last would be out of order.
func TestTheReadShowsTheJobsResultBesideTheDerivedOne(t *testing.T) {
	rig := newCIJobRig(t)
	writeHead(t, rig.database, ciJobRepository, 70, ciJobHead, "open", rig.issue)

	older := jobRecorded(t, "the job's first result", rig.post(t, rig.issue, rig.jobToken(nil), jobResultOf("c1", "pass")))
	time.Sleep(50 * time.Millisecond)
	concluded := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)
	newer := jobRecorded(t, "the job's second result", rig.post(t, rig.issue, rig.jobToken(nil), jobResultOf("c1", "fail")))

	run := completedRun(11, ciJobName, "success")
	run["completed_at"] = concluded.Format(time.RFC3339Nano)
	rig.github.listings.set(ciJobHead, run)

	check := rig.readJobCheck(t, "c1")
	if check.CI == nil || check.CI.Run != ciJobName || check.CI.State != model.CIStatePass {
		t.Fatalf("c1 carries ci %+v, want run %s passed", check.CI, ciJobName)
	}
	var order []string
	for _, result := range check.Results {
		order = append(order, result.ID)
	}
	derived := ciResultIDPrefix + "11"
	if want := []string{newer.ID, derived, older.ID}; strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("c1 holds the results %v, want %v: the job's newer result, the derived one, the job's older one", order, want)
	}
	for _, result := range check.Results {
		wantBranch, wantCommit := ciJobBranch, ciJobCommit
		if result.ID == derived {
			wantBranch, wantCommit = "", ciJobHead
		}
		if result.Role != "ci" || result.Branch != wantBranch || result.CommitSHA != wantCommit {
			t.Fatalf("result %s reads role %q, branch %q, commit %s; want ci, %q, %s",
				result.ID, result.Role, result.Branch, result.CommitSHA, wantBranch, wantCommit)
		}
	}
}
