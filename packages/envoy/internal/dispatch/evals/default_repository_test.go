package evals

import (
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
)

// The attribution of what a repository holds to the projects whose default repository it is
// (dispatch://INFRA-2916, D1 and R15 after the person's answer A; plan T7): a pull request with no
// known issue, a CI job of such a pull request or of none, and a deploy run that ships such a pull
// request or none count for each project whose default repository (projects.default_repo) is
// their repository; a deploy counts once in each project, through an issue where any of the pull
// requests it ships reached that project through one.

// defaultTally is what one eval's series counts for a project over a read: its rows' n and how many
// of them came through a default repository.
type defaultTally struct{ n, via int }

// readTallies reads, through the read the eval routes fold (DayWindow, over the deploy
// repository the rows stand recorded under), project's rows of each eval of series (by slug, the
// series to tally) on day, and answers each eval's tally and how many rows the read answered.
func (r *refresherRig) readTallies(t *testing.T, project string, series map[string]string, day time.Time) (map[string]defaultTally, int) {
	t.Helper()
	ctx := t.Context()
	page, err := r.registry.Current(ctx, []string{project}, "", 200, 0)
	if err != nil {
		t.Fatalf("read %s's evals: %v", project, err)
	}
	var entries []Entry
	for _, entry := range page.Items {
		if _, held := series[entry.Definition.Slug]; held {
			entries = append(entries, entry)
		}
	}
	if len(entries) != len(series) {
		t.Fatalf("%s holds %d of the %d evals the read tallies", project, len(entries), len(series))
	}
	deployRepos, err := DeployReposIn(ctx, r.database.Pool)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := r.registry.DayWindow(ctx, []string{project}, entries, deployRepos, nil, Range{From: day, To: day.AddDate(0, 0, 1)}, nil)
	if err != nil {
		t.Fatalf("read %s's day rows of %s: %v", project, day.Format(time.DateOnly), err)
	}
	tallies, held := map[string]defaultTally{}, 0
	for _, row := range rows {
		slug := slugOf(project, row.EvalID)
		if row.Part.Series != series[slug] {
			continue
		}
		held++
		tallies[slug] = defaultTally{n: tallies[slug].n + row.Part.N, via: tallies[slug].via + row.Part.ViaRepository}
	}
	return tallies, held
}

// One day in acme/alpha, the deploy repository and the default repository of both ALPHA and BETA:
// deploy X ships PR1 (ALPHA-1's) and PR2 (no issue), deploy Y ships PR3 (no issue), and deploy Z
// ships only PR4 (ALPHA-1's). deploy-frequency counts X, Y and Z for ALPHA (X and Z through
// ALPHA-1, Y through the default repository) and X and Y for BETA (each through the default
// repository: X ships PR2, and BETA is no project of X's issues), each deploy once in each; and
// production-deploy counts each deploy's production job once for each project that counts its
// deploy. Red under the rule that counts a row with no issue for no project: ALPHA counts X and Z
// and BETA nothing.
func TestADeployCountsOnceInEachProjectWhoseIssueOrDefaultRepositoryItReaches(t *testing.T) {
	r := newRefresherRig(t)
	ctx, pool := t.Context(), r.database.Pool
	day := refresherToday.AddDate(0, 0, -1)
	at := func(hour, minute int) time.Time {
		return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}
	r.exec(t, "the projects", `insert into projects (key, name, default_assignee, next_number, default_repo)
		values ('ALPHA', 'Alpha', 'alice', 2, $1), ('BETA', 'Beta', 'alice', 1, $1)`, alphaRepo)
	r.exec(t, "the issue", `insert into issues (key, project_key, number, title, status, rank, repo, created_by, created_at)
		values ('ALPHA-1', 'ALPHA', 1, 'Alpha work', 'in_progress', '00000001', $1, '{"kind":"system","id":"default-repository-test"}', $2)`,
		alphaRepo, day.AddDate(0, 0, -1))
	production := "production / apply"
	putSources(t, pool, "ALPHA", []string{"dev-ana"}, sourceOf(delivery.DeliverySettings{
		DeployWorkflowPath: ".github/workflows/deploy.yml", ProductionJobNames: []string{production},
		PRChecksWorkflowPath: ".github/workflows/pr-checks.yml",
	}, nil))
	for _, pr := range []struct {
		number int
		issue  *string
		merged time.Time
	}{
		{1, new("ALPHA-1"), at(9, 0)},
		{2, nil, at(9, 10)},
		{3, nil, at(11, 0)},
		{4, new("ALPHA-1"), at(13, 0)},
	} {
		if err := delivery.UpsertPullRequest(ctx, pool, delivery.DeliveryPullRequest{
			Repo: alphaRepo, Number: pr.number, Title: fmt.Sprintf("Change %d", pr.number),
			URL: fmt.Sprintf("https://github.com/%s/pull/%d", alphaRepo, pr.number), Author: "dev-ana",
			CreatedAt: new(pr.merged.Add(-time.Hour)), MergedAt: new(pr.merged), IssueKey: pr.issue, Sessions: []string{},
		}); err != nil {
			t.Fatalf("write %s#%d: %v", alphaRepo, pr.number, err)
		}
	}
	// X is created after PR1 and PR2 merge, Y after PR3, Z after PR4, each reaching production
	// before the next merge, so each ships the pull requests merged since the one before it.
	for _, deploy := range []struct {
		id      int64
		created time.Time
	}{{301, at(10, 0)}, {302, at(12, 0)}, {303, at(14, 0)}} {
		started, completed := deploy.created.Add(time.Minute), deploy.created.Add(20*time.Minute)
		if err := delivery.UpsertRun(ctx, pool, delivery.DeliveryRun{
			Repo: alphaRepo, RunID: deploy.id, Kind: delivery.DeliveryRunKindDeploy, HeadSHA: fmt.Sprintf("%040d", deploy.id),
			HeadCommitAt: deploy.created.Add(-time.Minute), StartedAt: started, CreatedAt: new(deploy.created), CompletedAt: &completed,
			Conclusion: new(delivery.DeliveryRunConclusionSuccess), URL: fmt.Sprintf("https://github.com/%s/actions/runs/%d", alphaRepo, deploy.id),
			HeadBranch: new("main"), Event: new("push"),
		}); err != nil {
			t.Fatalf("write deploy run %d: %v", deploy.id, err)
		}
		job := delivery.DeliveryRunJob{Name: production, StartedAt: new(started.Add(time.Minute)), CompletedAt: new(completed.Add(-time.Minute)),
			Conclusion: new(delivery.DeliveryJobConclusionSuccess)}
		if err := delivery.UpsertRunJobs(ctx, pool, alphaRepo, deploy.id, []delivery.DeliveryRunJob{job}, true); err != nil {
			t.Fatalf("write the job of deploy run %d: %v", deploy.id, err)
		}
	}
	r.settle(t)

	series := map[string]string{SlugDeployFrequency: "deploys", SlugProductionDeploy: "minutes"}
	want := map[string]map[string]defaultTally{
		"ALPHA": {SlugDeployFrequency: {n: 3, via: 1}, SlugProductionDeploy: {n: 3, via: 1}},
		"BETA":  {SlugDeployFrequency: {n: 2, via: 2}, SlugProductionDeploy: {n: 2, via: 2}},
	}
	for _, project := range []string{"ALPHA", "BETA"} {
		got, rows := r.readTallies(t, project, series, day)
		t.Logf("%s reads %d day rows of %s: %v", project, rows, day.Format(time.DateOnly), got)
		for slug, name := range series {
			if got[slug] != want[project][slug] {
				t.Errorf("%s's %s.%s counts %d, %d through a default repository; want %d and %d",
					project, slug, name, got[slug].n, got[slug].via, want[project][slug].n, want[project][slug].via)
			}
		}
	}
}
