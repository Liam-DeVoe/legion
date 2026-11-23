// project_deploy_run.go serves GET /api/v1/projects/{key}/deploy-runs/{id}: the drill-down's run
// detail under the project whose timeline row opened it (dispatch://INFRA-2918, D14). A run id is
// one repository's, and a project's deploy sources read several, so the route names the repository
// the run is in (repo, which each deploy and failed_run row of the project timeline carries). It
// answers the run where that repository is the source of a delivery-on project and the run counts
// for the project by the rule the project timeline keeps its run rows by (evals.Attribution).
package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/delivery"
)

// getProjectDeployRun answers GET /api/v1/projects/{key}/deploy-runs/{id}?repo=<owner/name>: the
// run's jobs as GET /api/v1/delivery/runs/{id} answers them, read from repo (readProjectDeployRun).
// 400 INVALID_PROJECT for a key that is not a project key, 400 RUN_ID_INPUT for an id that is not a
// positive integer, 400 INVALID_QUERY for a query naming no repo or naming it twice.
func (s *server) getProjectDeployRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuthenticated(w, r) {
		return
	}
	project := strings.TrimSpace(r.PathValue("key"))
	if !projectKeyPattern.MatchString(project) {
		writeError(w, "INVALID_PROJECT", http.StatusBadRequest, projectKeyProblem("project", project))
		return
	}
	runID, err := parseDeliveryRunID(r.PathValue("id"))
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	query := r.URL.Query()
	if err := namedOnce(query, "repo"); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	repo := strings.TrimSpace(query.Get("repo"))
	if repo == "" {
		writeError(w, "INVALID_QUERY", http.StatusBadRequest, "repo is required: the owner/name of the repository the run is in, as the project timeline's run rows name it")
		return
	}
	detail, err := s.readProjectDeployRun(r.Context(), project, repo, runID)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, detail)
}

// readProjectDeployRun is run runID of repo with every job it ran (deliveryRunDetail), for project:
// the project found (loadProject, 404 PROJECT_NOT_FOUND for a project no row holds); repo the
// repository of a github_workflow source of some delivery-on project (delivery.DeployRules over
// every project's settings, each source's repository by delivery.SourceRepoOf), read under that
// source's rule; the run one of that repository (readDeliveryRun); and the run counting for project
// (deployRunCountsFor). Each of the last three that fails is 404 NOT_FOUND.
func (s *server) readProjectDeployRun(ctx context.Context, project, repo string, runID int64) (delivery.DeliveryRunDetailView, error) {
	pool := s.deps.Store.Pool
	if _, err := s.loadProject(ctx, pool, project); err != nil {
		return delivery.DeliveryRunDetailView{}, err
	}
	projects, err := delivery.ListProjectSettings(ctx, pool)
	if err != nil {
		return delivery.DeliveryRunDetailView{}, err
	}
	rules := delivery.DeployRules(projects)
	index := slices.IndexFunc(rules, func(rule delivery.DeliverySettings) bool { return strings.EqualFold(rule.DeployRepo, repo) })
	if index < 0 {
		return delivery.DeliveryRunDetailView{}, errorf(http.StatusNotFound, "NOT_FOUND", "%s is the deploy source of no project with delivery on", repo)
	}
	rule := rules[index]
	run, err := readDeliveryRun(ctx, pool, rule.DeployRepo, runID)
	if err != nil {
		return delivery.DeliveryRunDetailView{}, err
	}
	counts, err := s.deployRunCountsFor(ctx, rule, run, project)
	if err != nil {
		return delivery.DeliveryRunDetailView{}, err
	}
	if !counts {
		return delivery.DeliveryRunDetailView{}, errorf(http.StatusNotFound, "NOT_FOUND",
			"run %d of %s ships first no pull request project %s counts, so no row of its timeline lists it", runID, rule.DeployRepo, project)
	}
	return deliveryRunDetail(ctx, pool, run)
}

// deployRunCountsFor is whether run, a run of rule's repository, counts for project as the project
// timeline keeps its run rows (projectTimelinePopulation): a deploy run on main counts by the pull
// requests it ships first, through the rule the eval registry's day rows count a deploy by
// (evals.Attribution.CountsDeploy). What it ships first is delivery.Deploys.ShippedBy over the
// rows delivery.ReadWindowRows reads for a window holding the run's start alone, which reads the
// deploys before it and the pull requests they leave to it as a wider window's read does. Any other
// run is no deploy of a window and ships nothing, so it counts for no project.
func (s *server) deployRunCountsFor(ctx context.Context, rule delivery.DeliverySettings, run delivery.DeliveryRun, project string) (bool, error) {
	// Postgres keeps microseconds, so the window ends a microsecond past the start it holds.
	merged, deploys, err := delivery.ReadWindowRows(ctx, s.deps.Store.Pool, rule, run.StartedAt, run.StartedAt.Add(time.Microsecond), false, true)
	if err != nil {
		return false, fmt.Errorf("read what run %d of %s ships: %w", run.RunID, run.Repo, err)
	}
	shipped := deploys.ShippedBy(merged, rule)[run.RunID]
	var keys []string
	for _, pr := range shipped {
		if pr.IssueKey != nil {
			keys = append(keys, *pr.IssueKey)
		}
	}
	attribution, err := s.registry().ReadAttribution(ctx, keys)
	if err != nil {
		return false, fmt.Errorf("read the projects run %d of %s counts for: %w", run.RunID, run.Repo, err)
	}
	return attribution.CountsDeploy(shipped, project), nil
}
