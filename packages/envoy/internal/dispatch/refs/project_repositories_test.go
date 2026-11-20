package refs

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// projectRepositoriesWant is each project's answer over seedProjectRepositories' rows, lower-cased,
// sorted byte-wise and each once.
var projectRepositoriesWant = map[string][]string{
	"PRIV":  {"acme/done", "acme/oldname", "acme/privdefault", "acme/pub", "acme/two", "acme/upper"},
	"OTHER": {"acme/priv", "acme/two"},
}

// seedProjectRepositories writes PRIV, private, and OTHER, public, with an issue of PRIV for each
// way an issue resolves to a repository: PRIV-1 names acme/pub, its own; PRIV-2 names none and is a
// child of PRIV-1, so it resolves to its parent's acme/pub; PRIV-3 names none and is a child of
// OTHER-2, which names acme/two, so only the parent chain gives PRIV acme/two, since no issue of
// PRIV names it; PRIV-4 names none and is a root, so it resolves to PRIV's default,
// acme/privdefault; PRIV-5 names acme/done and is done; PRIV-6 names acme/oldname, which a
// mapping of PRIV records as renamed to acme/newname; and PRIV-7 names Acme/Upper, in capitals.
// OTHER-1 names acme/priv. PRIV maps acme/privmapped, which no issue names. The default, the
// rename and that mapping are stored as this build stores them, in repo_projects. A parent in
// another project and a repository stored in capitals are rows only a direct write makes (the API
// refuses the one, api/issue_parent.go, and lower-cases the other, api/issue_repo.go); refs' other
// seeds hold such a parent too (seedRepositoryIssues). It is the one way the parent chain adds a
// repository that the project's own issues do not name.
func seedProjectRepositories(t *testing.T) *store.Store {
	t.Helper()
	database := storetest.Open(t)
	if _, err := database.Pool.Exec(context.Background(), `
		insert into projects (key, name, default_assignee, visibility) values
			('PRIV', 'Private', 'alice', 'private'), ('OTHER', 'Other', 'alice', 'public');
		insert into issues (key, project_key, number, title, created_by, rank, parent_key, repo, status) values
			('OTHER-1', 'OTHER', 1, 'Names acme/priv', '{"kind":"user","id":"alice"}', 'a', null, 'acme/priv', 'triage'),
			('OTHER-2', 'OTHER', 2, 'Names acme/two', '{"kind":"user","id":"alice"}', 'b', null, 'acme/two', 'triage'),
			('PRIV-1', 'PRIV', 1, 'Names acme/pub', '{"kind":"user","id":"alice"}', 'a', null, 'acme/pub', 'triage'),
			('PRIV-2', 'PRIV', 2, 'Under PRIV-1', '{"kind":"user","id":"alice"}', 'b', 'PRIV-1', null, 'triage'),
			('PRIV-3', 'PRIV', 3, 'Under OTHER-2', '{"kind":"user","id":"alice"}', 'c', 'OTHER-2', null, 'triage'),
			('PRIV-4', 'PRIV', 4, 'Names none', '{"kind":"user","id":"alice"}', 'd', null, null, 'triage'),
			('PRIV-5', 'PRIV', 5, 'Names acme/done', '{"kind":"user","id":"alice"}', 'e', null, 'acme/done', 'done'),
			('PRIV-6', 'PRIV', 6, 'Names acme/oldname', '{"kind":"user","id":"alice"}', 'f', null, 'acme/oldname', 'triage'),
			('PRIV-7', 'PRIV', 7, 'Names Acme/Upper', '{"kind":"user","id":"alice"}', 'g', null, 'Acme/Upper', 'triage');
		insert into repo_projects (repo, project, is_default, renamed_to, created_by) values
			('acme/privdefault', 'PRIV', true, null, '{"kind":"user","id":"alice"}'),
			('acme/oldname', 'PRIV', false, 'acme/newname', '{"kind":"user","id":"alice"}'),
			('acme/privmapped', 'PRIV', false, null, '{"kind":"user","id":"alice"}');
	`); err != nil {
		t.Fatalf("seed the projects' issues: %v", err)
	}
	return database
}

// TestAProjectsRepositoriesAreWhereItsIssuesResolve: Dispatch's repository answer (GET
// /api/v1/access/projects/{key}/repositories, ProjectRepositories) is the repository each of a
// project's issues resolves to, open or done: its own, its parent's, or its project's default,
// lower-cased, sorted and each once. PRIV's answer holds acme/pub (PRIV-1's own, and PRIV-2's
// through its parent), acme/two (PRIV-3's, only through its parent OTHER-2), acme/privdefault
// (PRIV-4's default), acme/done (the done PRIV-5's), acme/oldname (PRIV-6's, as its issue names
// it: the rule reads no rename from repo_projects, so acme/newname, the name a mapping records for
// it, is in no answer) and acme/upper (PRIV-7's Acme/Upper, lower-cased). OTHER-1's acme/priv adds
// nothing to PRIV's answer, and acme/privmapped, which PRIV maps and no issue names, is in no
// answer. Controls, each on ProjectRepositoriesSQL, the one rule the answer reads: without its
// parent-chain step PRIV-3 resolves to PRIV's default, the answer misses acme/two, and the test
// fails naming it; reading renames (CanonicalRepoSQL) answers acme/newname in place of
// acme/oldname, and the test fails naming both; without lower() the answer holds Acme/Upper in
// place of acme/upper, and the test fails naming both.
func TestAProjectsRepositoriesAreWhereItsIssuesResolve(t *testing.T) {
	database := seedProjectRepositories(t)
	for _, project := range []string{"PRIV", "OTHER"} {
		got, err := ProjectRepositories(context.Background(), database.Pool, project)
		if err != nil {
			t.Fatalf("ProjectRepositories(%s): %v", project, err)
		}
		want := projectRepositoriesWant[project]
		if !slices.Equal(got, want) {
			t.Errorf("ProjectRepositories(%s) = %q: it misses %q and adds %q, want %q",
				project, got, notIn(want, got), notIn(got, want), want)
		}
	}
}

// notIn is each of these that from does not hold, in these' order.
func notIn(these, from []string) []string {
	missing := []string{}
	for _, repository := range these {
		if !slices.Contains(from, repository) {
			missing = append(missing, repository)
		}
	}
	return missing
}

// fixtureIssue is one issue of the fixture as the one-issue read resolves it: its key, its
// project and the repository IssueRepository answers, lower-cased as the answer names it.
type fixtureIssue struct {
	key, project, resolved string
}

// resolveFixtureIssues reads every issue the database holds, whatever seeded it, and resolves
// each through IssueRepository, the one-issue read of the same terms. IssueRepository answers a
// name as the issue stores it and the repository answer names it lower-cased, so each is
// lower-cased here. An issue that resolves to none fails the test, since every issue of the
// fixture resolves to one, and so does a database that holds no issue: a walk over nothing would
// pass every check below it.
func resolveFixtureIssues(t *testing.T, database *store.Store) []fixtureIssue {
	t.Helper()
	ctx := context.Background()
	rows, err := database.Pool.Query(ctx, `select key, project_key from issues order by key`)
	if err != nil {
		t.Fatalf("list the fixture's issues: %v", err)
	}
	var issues []fixtureIssue
	for rows.Next() {
		var issue fixtureIssue
		if err := rows.Scan(&issue.key, &issue.project); err != nil {
			t.Fatalf("read a fixture issue: %v", err)
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list the fixture's issues: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("the fixture holds no issue, so no check below could fail")
	}
	for i := range issues {
		resolved, err := IssueRepository(ctx, database.Pool, issues[i].key)
		if err != nil {
			t.Fatalf("IssueRepository(%s): %v", issues[i].key, err)
		}
		if resolved == nil {
			t.Fatalf("IssueRepository(%s) resolves to no repository, and every issue of the fixture resolves to one", issues[i].key)
		}
		issues[i].resolved = strings.ToLower(*resolved)
	}
	return issues
}

// TestEveryIssueResolvesIntoItsProjectsRepositories: for every issue of the fixture, the
// repository the one-issue read resolves it to (IssueRepository), lower-cased, is in its project's
// repository answer, and the union of them over a project's issues, open and done, is that
// project's answer, no more: no repository the project only maps. Control: answer a project from
// its open issues alone, and PRIV-5's acme/done is missing from PRIV's answer, and the test fails
// naming it.
//
// Its row "the GitHub proxy" holds the proxy's gate (RepositoryClause) to the answer, since both
// read the one rule, ProjectRepositoriesSQL: for every fixture issue's repository, the projects
// that rule counts as its owners, embedded as RepositoryClause embeds it, are exactly the
// projects whose answer lists it; a restricted member who sees only PRIV is shown exactly the
// repositories PRIV's answer lists; and alice, open and seeing OTHER, is shown exactly those that
// OTHER's answer lists or PRIV's does not, PRIV being the one private project she does not see.
// Those are the spec's rules for a viewer that is not open (a repository an issue the viewer sees
// resolves to) and for an open one (that, or a repository no private project hidden from the
// viewer owns). Control: drop repositorySeen's question for a project's default, and the member
// is not shown acme/privdefault, which PRIV's answer lists; the row fails naming it.
func TestEveryIssueResolvesIntoItsProjectsRepositories(t *testing.T) {
	database := seedProjectRepositories(t)
	issues := resolveFixtureIssues(t, database)
	answers := map[string][]string{}
	union := map[string][]string{}
	for _, issue := range issues {
		answer, read := answers[issue.project]
		if !read {
			var err error
			if answer, err = ProjectRepositories(context.Background(), database.Pool, issue.project); err != nil {
				t.Fatalf("ProjectRepositories(%s): %v", issue.project, err)
			}
			answers[issue.project] = answer
		}
		if !slices.Contains(answer, issue.resolved) {
			t.Errorf("IssueRepository(%s) = %s, which ProjectRepositories(%s) = %q does not hold",
				issue.key, issue.resolved, issue.project, answer)
		}
		if !slices.Contains(union[issue.project], issue.resolved) {
			union[issue.project] = append(union[issue.project], issue.resolved)
		}
	}
	for project, answer := range answers {
		slices.Sort(union[project])
		if !slices.Equal(union[project], answer) {
			t.Errorf("the union of IssueRepository over %s's issues is %q, and ProjectRepositories(%s) = %q, which misses %q and adds %q: the two are one list",
				project, union[project], project, answer, notIn(union[project], answer), notIn(answer, union[project]))
		}
	}

	t.Run("the GitHub proxy", func(t *testing.T) {
		repositories := make([]string, 0, len(issues))
		for _, issue := range issues {
			repositories = append(repositories, issue.resolved)
		}
		owners := projectOwners(t, database, "true", repositories)
		projects := slices.Sorted(maps.Keys(answers))
		member := model.Actor{Viewer: &model.Viewer{Person: "bob", Restricted: true, Sees: []string{"PRIV"}, AnyPrivate: true}}
		open := model.Actor{Viewer: &model.Viewer{Person: "alice", Open: true, Sees: []string{"OTHER"}, AnyPrivate: true}}
		for _, issue := range issues {
			listed := []string{}
			for _, project := range projects {
				if slices.Contains(answers[project], issue.resolved) {
					listed = append(listed, project)
				}
			}
			if got := owners[issue.resolved]; !slices.Equal(got, listed) {
				t.Errorf("the proxy's rule counts %q as the owners of %s, %s's repository, and the answers of %q list it: the two must name the same projects",
					got, issue.resolved, issue.key, listed)
			}
			inPRIV := slices.Contains(answers["PRIV"], issue.resolved)
			if shown := repositoryPasses(t, database, member, issue.resolved); shown != inPRIV {
				t.Errorf("the proxy shows %s, %s's repository, to a person who sees only PRIV: %t; PRIV's answer holds it: %t",
					issue.resolved, issue.key, shown, inPRIV)
			}
			inOTHER := slices.Contains(answers["OTHER"], issue.resolved)
			if shown := repositoryPasses(t, database, open, issue.resolved); shown != (inOTHER || !inPRIV) {
				t.Errorf("the proxy shows %s, %s's repository, to an open viewer who sees OTHER: %t; OTHER's answer holds it: %t, and PRIV's, hidden from that viewer: %t",
					issue.resolved, issue.key, shown, inOTHER, inPRIV)
			}
		}
	})
}
