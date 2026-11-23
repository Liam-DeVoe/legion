package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// The tests of GET /api/v1/projects/{key}/deploy-runs/{id} (dispatch://INFRA-2918, D14): the
// drill-down's run detail under the project whose timeline row opened it, read from the repository
// the row names.

// deployRunSource is a github_workflow source reading repo, whose deploys its job release judges.
func deployRunSource(repo string) delivery.SourceInput {
	return delivery.SourceInput{
		Kind: delivery.SourceKindGitHubWorkflow, Repo: &repo, DeployWorkflowPath: new(".github/workflows/deploy.yml"),
		ProductionJobNames: []string{"release"}, PRChecksWorkflowPath: new(".github/workflows/pr-checks.yml"),
	}
}

// putDeploySources saves project's delivery settings, delivery on or off, with sources.
func putDeploySources(t *testing.T, database *store.Store, project string, on bool, sources ...delivery.SourceInput) {
	t.Helper()
	ctx := t.Context()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the settings of %s: %v", project, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := delivery.PutProjectSettings(ctx, tx, delivery.ProjectSettingsInput{Project: project, Delivery: on, Sources: sources},
		model.Actor{Kind: "system", ID: "test"}); err != nil {
		t.Fatalf("save the delivery settings of %s: %v", project, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the settings of %s: %v", project, err)
	}
}

// openDeployRunIssue opens an issue of project titled title, which no other issue's shares a word
// of, so the create's duplicate check passes it, and answers its key.
func openDeployRunIssue(t *testing.T, handler http.Handler, project, title string) string {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodPost, "/api/v1/issues", map[string]any{"project": project, "title": title}, "alice")
	if response.Code != http.StatusCreated {
		t.Fatalf("open an issue of %s: status %d, body %s", project, response.Code, response.Body.String())
	}
	return decodeBody[struct {
		Key string `json:"key"`
	}](t, response).Key
}

// deployRunPR writes repo#n by octocat, merged at merged, naming issue.
func deployRunPR(t *testing.T, database *store.Store, repo string, n int, merged time.Time, issue string) {
	t.Helper()
	opened := merged.Add(-2 * time.Hour)
	upsertPR(t, database, delivery.DeliveryPullRequest{
		Repo: repo, Number: n, Title: "feat: change " + strconv.Itoa(n), URL: "https://github.com/" + repo + "/pull/" + strconv.Itoa(n),
		Author: "octocat", MergedAt: &merged, CreatedAt: &opened, IssueKey: &issue,
	})
}

// deployRunIn writes deploy run id of repo on main, created at created and started a minute later:
// a job named build of repo, then the production job release, each succeeding five minutes after
// it starts.
func deployRunIn(t *testing.T, database *store.Store, repo string, id int64, created time.Time) {
	t.Helper()
	ctx := t.Context()
	started := created.Add(time.Minute)
	built, released, completed := started.Add(5*time.Minute), started.Add(6*time.Minute), started.Add(11*time.Minute)
	success, jobSuccess := delivery.DeliveryRunConclusionSuccess, delivery.DeliveryJobConclusionSuccess
	url := "https://github.com/" + repo + "/actions/runs/" + strconv.FormatInt(id, 10)
	if err := delivery.UpsertRun(ctx, database.Pool, delivery.DeliveryRun{
		Repo: repo, RunID: id, Kind: delivery.DeliveryRunKindDeploy, HeadSHA: "sha" + strconv.FormatInt(id, 10),
		HeadCommitAt: created.Add(-time.Minute), StartedAt: started, CreatedAt: &created, CompletedAt: &completed, Conclusion: &success,
		URL: url, HeadBranch: new("main"), Event: new("push"),
	}); err != nil {
		t.Fatalf("upsert deploy run %d of %s: %v", id, repo, err)
	}
	if err := delivery.UpsertRunJobs(ctx, database.Pool, repo, id, []delivery.DeliveryRunJob{
		{Repo: repo, RunID: id, Name: "build " + repo, StartedAt: &started, CompletedAt: &built, Conclusion: &jobSuccess},
		{Repo: repo, RunID: id, Name: "release", StartedAt: &released, CompletedAt: &completed, Conclusion: &jobSuccess},
	}, true); err != nil {
		t.Fatalf("upsert the jobs of deploy run %d of %s: %v", id, repo, err)
	}
}

// TestADeployRunAnswersItsJobsFromTheNamedSourceRepository: project SHIP deploys from two sources in
// two repositories, OTHR from a third, and IDLE, with delivery off, names a fourth. A run id is one
// repository's: run 100 of each of SHIP's repositories ships a SHIP pull request, and each answers
// its own jobs and address on SHIP's route with its repository, the first byte for byte what
// GET /api/v1/delivery/runs/{id} answers of a deployment deploying from it. A deploy in OTHR's
// repository that shipped a SHIP pull request answers its jobs on SHIP's route, as its timeline row
// is SHIP's; one there that ships only OTHR's pull request answers 404 on SHIP's route and its jobs
// on OTHR's. A deploy of IDLE's repository shipping a SHIP pull request answers 404, as no
// delivery-on project's source reads it, and so does a run id the named repository holds no run
// of. A query naming no repo, or naming it twice, answers 400 INVALID_QUERY naming repo.
func TestADeployRunAnswersItsJobsFromTheNamedSourceRepository(t *testing.T) {
	handler, database, _ := newTestServer(t, testServerOptions{})
	createTestProject(t, handler, "SHIP", "OTHR", "IDLE")
	putDeploySources(t, database, "SHIP", true, deployRunSource("acme/alpha"), deployRunSource("acme/beta"))
	putDeploySources(t, database, "OTHR", true, deployRunSource("acme/gamma"))
	putDeploySources(t, database, "IDLE", false, deployRunSource("acme/delta"))
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, pr := range []struct {
		repo    string
		number  int
		merged  time.Time
		project string
		title   string
	}{
		{"acme/alpha", 1, at, "SHIP", "Widgets"}, {"acme/beta", 1, at, "SHIP", "Gadgets"}, {"acme/gamma", 5, at, "SHIP", "Sprockets"},
		{"acme/gamma", 6, at.Add(3 * time.Hour), "OTHR", "Cogwheels"}, {"acme/delta", 1, at, "SHIP", "Levers"},
	} {
		deployRunPR(t, database, pr.repo, pr.number, pr.merged, openDeployRunIssue(t, handler, pr.project, pr.title))
	}
	deployRunIn(t, database, "acme/alpha", 100, at.Add(time.Hour))
	deployRunIn(t, database, "acme/beta", 100, at.Add(time.Hour))
	deployRunIn(t, database, "acme/gamma", 300, at.Add(time.Hour))
	deployRunIn(t, database, "acme/gamma", 301, at.Add(4*time.Hour))
	deployRunIn(t, database, "acme/delta", 400, at.Add(time.Hour))

	target := func(project string, id int64, query string) string {
		return "/api/v1/projects/" + project + "/deploy-runs/" + strconv.FormatInt(id, 10) + query
	}
	answers := []struct {
		project string
		id      int64
		repo    string
	}{
		{"SHIP", 100, "acme/alpha"}, {"SHIP", 100, "acme/beta"}, {"SHIP", 300, "acme/gamma"}, {"OTHR", 301, "acme/gamma"},
	}
	for _, want := range answers {
		path := target(want.project, want.id, "?repo="+want.repo)
		response := dispatchRequest(t, handler, http.MethodGet, path, nil, "alice")
		if response.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, body %s; want the run's jobs", path, response.Code, response.Body.String())
			continue
		}
		detail := decodeBody[delivery.DeliveryRunDetailView](t, response)
		names := make([]string, len(detail.Jobs))
		for i, job := range detail.Jobs {
			names[i] = job.Name
		}
		wantURL := "https://github.com/" + want.repo + "/actions/runs/" + strconv.FormatInt(want.id, 10)
		if detail.ID != want.id || detail.URL != wantURL || !slices.Equal(names, []string{"build " + want.repo, "release"}) {
			t.Errorf("GET %s = run %d at %s with jobs %v; want run %d at %s with build %s and release, its repository's", path,
				detail.ID, detail.URL, names, want.id, wantURL, want.repo)
		}
	}

	if _, err := delivery.PutSettings(t.Context(), database.Pool, delivery.DeliverySettings{
		DeployRepo: "acme/alpha", DeployWorkflowPath: ".github/workflows/deploy.yml", ProductionJobNames: []string{"release"},
		PRChecksWorkflowPath: ".github/workflows/pr-checks.yml", PopulationAuthors: []string{"octocat"},
	}, model.Actor{Kind: "system", ID: "test"}); err != nil {
		t.Fatalf("seed the deployment's delivery settings: %v", err)
	}
	if base, project := string(getBody(t, handler, "/api/v1/delivery/runs/100")), string(getBody(t, handler, target("SHIP", 100, "?repo=acme/alpha"))); project != base {
		t.Errorf("SHIP's run 100 of acme/alpha answers %s, GET /api/v1/delivery/runs/100 from acme/alpha %s; want the same body", project, base)
	}

	for _, refused := range []struct {
		what, path string
		status     int
		code       string
	}{
		{"a deploy of OTHR's repository shipping no SHIP pull request", target("SHIP", 301, "?repo=acme/gamma"), http.StatusNotFound, "NOT_FOUND"},
		{"a repository only a delivery-off project's source reads", target("SHIP", 400, "?repo=acme/delta"), http.StatusNotFound, "NOT_FOUND"},
		{"a run id the named repository holds no run of", target("SHIP", 300, "?repo=acme/alpha"), http.StatusNotFound, "NOT_FOUND"},
		{"no repo", target("SHIP", 100, ""), http.StatusBadRequest, "INVALID_QUERY"},
		{"repo named twice", target("SHIP", 100, "?repo=acme/alpha&repo=acme/beta"), http.StatusBadRequest, "INVALID_QUERY"},
	} {
		response := dispatchRequest(t, handler, http.MethodGet, refused.path, nil, "alice")
		answer := decodeBody[refusalAnswer](t, response)
		if response.Code != refused.status || answer.Code != refused.code {
			t.Errorf("%s: GET %s = %d %s; want %d %s", refused.what, refused.path, response.Code, response.Body.String(), refused.status, refused.code)
		}
		if refused.status == http.StatusBadRequest && !strings.Contains(answer.Error, "repo") {
			t.Errorf("%s: the refusal %q does not name repo", refused.what, answer.Error)
		}
	}
}
