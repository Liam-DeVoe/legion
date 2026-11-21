package delivery

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// TestDeliveryHoldsARenamedPrivateRepositoryByItsID: a pull request that names no issue, and a run
// that ships none, is held to the repository rule by the GitHub id GitHub's answer about it carries
// (delivery_pull_requests.repo_id, delivery_runs.repo_id), as the GitHub proxy holds a repository,
// so a private project's repository GitHub renamed is hidden under its new name too.
//
// PRIV, private, has PRIV-1 naming acme/privold with id 9001; GitHub renamed it acme/privnew. PUB is
// public. Issue-less pull requests: acme/privnew (9001), acme/privold (9010, a new repository that
// took the old name), acme/pub (9100) and acme/legacy (no id, stored before ids; PRIV-2 names
// acme/legacy with no id either). olga, an open viewer who sees PUB, is hidden acme/privnew, by its
// id, and acme/legacy, by its name, and shown acme/privold and acme/pub; carol, who sees PRIV too, is
// shown all four. A partial row of acme/privnew written with no id keeps the 9001 stored. A deploy
// run of acme/privnew (9001) that shipped nothing is no run olga reads (ReadRunShown answers
// pgx.ErrNoRows, as for a run nobody holds), and carol reads it.
//
// Control: judge rows by their name (the clause's null id), and olga is shown acme/privnew and
// hidden acme/privold; the test fails naming both.
func TestDeliveryHoldsARenamedPrivateRepositoryByItsID(t *testing.T) {
	pool, ctx := deliveryTestPool(t)
	for _, statement := range []string{
		`insert into projects (key, name, default_assignee, visibility) values ('PUB', 'PUB', 'olga', 'public'), ('PRIV', 'PRIV', 'olga', 'private')`,
		`insert into issues (key, project_key, number, title, created_by, rank, repo, repo_id, status) values
			('PRIV-1', 'PRIV', 1, 'a', '{"kind":"user","id":"olga"}', 'a', 'acme/privold', 9001, 'triage'),
			('PRIV-2', 'PRIV', 2, 'b', '{"kind":"user","id":"olga"}', 'b', 'acme/legacy', null, 'triage')`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	merged := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	opened := merged.Add(-time.Hour)
	id := func(value int64) *int64 { return &value }
	for _, pr := range []DeliveryPullRequest{
		{Repo: "acme/privnew", RepoID: id(9001)},
		{Repo: "acme/privold", RepoID: id(9010)},
		{Repo: "acme/pub", RepoID: id(9100)},
		{Repo: "acme/legacy"},
	} {
		pr.Number, pr.Title, pr.Author, pr.URL, pr.CreatedAt, pr.MergedAt = 1, "change", "octocat", "https://github.com/"+pr.Repo+"/pull/1", &opened, &merged
		if err := UpsertPullRequest(ctx, pool, pr); err != nil {
			t.Fatalf("store the pull request of %s: %v", pr.Repo, err)
		}
	}
	if err := UpsertPullRequest(ctx, pool, DeliveryPullRequest{
		Repo: "acme/privnew", Number: 1, Title: "change", Author: "octocat", URL: "https://github.com/acme/privnew/pull/1",
		CreatedAt: &opened, MergedAt: &merged, Partial: true,
	}); err != nil {
		t.Fatalf("store a partial row of acme/privnew: %v", err)
	}
	var kept *int64
	if err := pool.QueryRow(ctx, `select repo_id from delivery_pull_requests where repo = 'acme/privnew'`).Scan(&kept); err != nil || kept == nil || *kept != 9001 {
		t.Fatalf("after a partial write with no id, acme/privnew's row holds %v (%v), want the 9001 stored", kept, err)
	}

	olga := refs.VisibilityArg(model.Actor{Viewer: &model.Viewer{Person: "olga", Open: true, Sees: []string{"PUB"}, AnyPrivate: true}})
	carol := refs.VisibilityArg(model.Actor{Viewer: &model.Viewer{Person: "carol", Open: true, Sees: []string{"PUB", "PRIV"}, AnyPrivate: true}})
	if got, want := oneRuleRead(t, pool, olga, `not `+pullRequestShownSQL("pr", "i", "$1")), []string{"acme/legacy", "acme/privnew"}; !slices.Equal(got, want) {
		t.Errorf("olga, who does not see PRIV, is hidden the pull requests of %q, want %q: acme/privnew by its id 9001, acme/legacy by its name", got, want)
	}
	if got := oneRuleRead(t, pool, carol, `not `+pullRequestShownSQL("pr", "i", "$1")); len(got) != 0 {
		t.Errorf("carol, who sees PRIV, is hidden the pull requests of %q, want none", got)
	}

	completed := merged.Add(time.Hour)
	if err := UpsertRun(ctx, pool, DeliveryRun{
		Repo: "acme/privnew", RepoID: id(9001), RunID: 77, Kind: DeliveryRunKindDeploy, HeadSHA: "0123abcd",
		HeadCommitAt: merged, StartedAt: merged, CreatedAt: &merged, CompletedAt: &completed, URL: "https://github.com/acme/privnew/actions/runs/77",
	}); err != nil {
		t.Fatalf("store the run of acme/privnew: %v", err)
	}
	if _, err := ReadRunShown(ctx, pool, "acme/privnew", 77, Production{}, olga); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("olga reads acme/privnew's run 77, which shipped nothing, with error %v, want pgx.ErrNoRows: its repository 9001 is PRIV's", err)
	}
	if run, err := ReadRunShown(ctx, pool, "acme/privnew", 77, Production{}, carol); err != nil || run.RepoID == nil || *run.RepoID != 9001 {
		t.Errorf("carol reads acme/privnew's run 77 as %+v, %v, want it, with its id 9001", run, err)
	}
}

// TestGitHubsAnswersGiveDeliveryTheRepositoryID: every GitHub answer Delivery stores a pull
// request or a run from carries the repository's id, and Delivery keeps it: a REST pull request's
// base.repo.id, a search result's baseRepository.databaseId, and a workflow run's repository.id,
// which StoredRun records. Control: drop any one of the three fields, and its row fails naming it.
func TestGitHubsAnswersGiveDeliveryTheRepositoryID(t *testing.T) {
	var rest pullRequestPayload
	if err := json.Unmarshal([]byte(`{"number":1,"base":{"ref":"main","repo":{"id":9001,"full_name":"acme/privnew"}}}`), &rest); err != nil || rest.Base.Repo.ID != 9001 {
		t.Errorf("a REST pull request's base.repo.id decodes as %d (%v), want 9001", rest.Base.Repo.ID, err)
	}
	var node searchPullRequestNode
	if err := json.Unmarshal([]byte(`{"number":1,"baseRepository":{"databaseId":9001}}`), &node); err != nil {
		t.Fatalf("decode a search result: %v", err)
	}
	if got := fetchedPullRequestFromSearchNode(node).RepoID; got != 9001 {
		t.Errorf("a search result's baseRepository.databaseId gives the pull request %d, want 9001", got)
	}
	if !strings.Contains(searchPullRequestsQuery, "baseRepository { databaseId }") {
		t.Errorf("the search query does not ask baseRepository { databaseId }:\n%s", searchPullRequestsQuery)
	}
	var item workflowRunItem
	if err := json.Unmarshal([]byte(`{"id":77,"head_sha":"0123abcd","repository":{"id":9001,"full_name":"acme/privnew"}}`), &item); err != nil {
		t.Fatalf("decode a workflow run: %v", err)
	}
	if run := StoredRun("acme/privold", DeliveryRunKindDeploy, fetchedRunFromItem(item)); run.RepoID == nil || *run.RepoID != 9001 {
		t.Errorf("a workflow run's repository.id is stored as %v, want 9001", run.RepoID)
	}
	if run := StoredRun("acme/privold", DeliveryRunKindDeploy, FetchedRun{RunID: 78}); run.RepoID != nil {
		t.Errorf("a run whose answer carries no repository id is stored with %d, want none", *run.RepoID)
	}
}
