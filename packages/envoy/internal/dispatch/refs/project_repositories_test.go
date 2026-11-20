package refs

import (
	"context"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// projectRepositoriesWant is each project's answer over seedProjectRepositories' rows, sorted
// byte-wise and each once.
var projectRepositoriesWant = map[string][]string{
	"PRIV":  {"acme/done", "acme/newname", "acme/privdefault", "acme/pub", "acme/two"},
	"OTHER": {"acme/priv", "acme/two"},
}

// seedProjectRepositories writes PRIV, private, and OTHER, public, with an issue of PRIV for each
// way an issue resolves to a repository: PRIV-1 names acme/pub, its own; PRIV-2 names none and is a
// child of PRIV-1, so it resolves to its parent's acme/pub; PRIV-3 names none and is a child of
// OTHER-2, which names acme/two, so only the parent chain gives PRIV acme/two, since no issue of
// PRIV names it; PRIV-4 names none and is a root, so it resolves to PRIV's default,
// acme/privdefault; PRIV-5 names acme/done and is done; and PRIV-6 names acme/oldname, which a
// recorded rename gives the name acme/newname. OTHER-1 names acme/priv. PRIV maps acme/privmapped,
// which no issue names. The default, the rename and that mapping are stored as this build stores
// them, in repo_projects. A parent in another project is a row only a direct write makes (the API
// refuses one), as refs' other seeds hold one too (seedRepositoryIssues): it is the one way the
// parent chain adds a repository that the project's own issues do not name.
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
			('PRIV-6', 'PRIV', 6, 'Names acme/oldname', '{"kind":"user","id":"alice"}', 'f', null, 'acme/oldname', 'triage');
		insert into repo_projects (repo, project, is_default, renamed_to, created_by) values
			('acme/privdefault', 'PRIV', true, null, '{"kind":"user","id":"alice"}'),
			('acme/oldname', 'PRIV', false, 'acme/newname', '{"kind":"user","id":"alice"}'),
			('acme/privmapped', 'PRIV', false, null, '{"kind":"user","id":"alice"}');
	`); err != nil {
		t.Fatalf("seed the projects' issues: %v", err)
	}
	return database
}

// TestAProjectsRepositoriesAreWhereItsIssuesResolve (K-E5): a project's answer is the repository
// each of its issues resolves to, open or done: its own, its parent's, or its project's default,
// under the name a recorded rename gives it, sorted and each once. PRIV's answer holds acme/pub
// (PRIV-1's own, and PRIV-2's through its parent), acme/two (PRIV-3's, only through its parent
// OTHER-2), acme/privdefault (PRIV-4's default), acme/done (the done PRIV-5's) and acme/newname
// (PRIV-6's acme/oldname under its new name). OTHER-1's acme/priv adds nothing to PRIV's answer,
// and acme/privmapped, which PRIV maps and no issue names, is in no answer. Control: the statement
// without its parent-chain step resolves PRIV-3 to PRIV's default, misses acme/two, and the test
// fails naming it.
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
// project, the repository IssueRepository answers, and that repository under its recorded rename.
type fixtureIssue struct {
	key, project, resolved, canonical string
}

// resolveFixtureIssues reads every issue the database holds, whatever seeded it, and resolves
// each through IssueRepository, the one-issue read of the same rule. An issue that resolves to
// none fails the test, since every issue of the fixture resolves to one, and so does a database
// that holds no issue: a walk over nothing would pass every check below it.
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
		canonical, err := CanonicalRepo(ctx, database.Pool, *resolved)
		if err != nil {
			t.Fatalf("CanonicalRepo(%s): %v", *resolved, err)
		}
		issues[i].resolved, issues[i].canonical = *resolved, canonical
	}
	return issues
}

// TestEveryIssueResolvesIntoItsProjectsRepositories (K-E5): for every issue of the fixture, the
// repository the one-issue read resolves it to (IssueRepository), under its recorded rename, is in
// its project's answer, and the union of them over a project's issues, open and done, is that
// project's answer, no more: no repository the project only maps. Its row "the GitHub proxy" holds
// the proxy's own check (RepositoryClause) to the same answer for every issue's repository: a
// person who sees only PRIV is shown exactly the ones in PRIV's answer, and an open viewer who does
// not see PRIV, private, is shown exactly the others. Control: answer a project from its open
// issues alone, and PRIV-5's acme/done is missing from PRIV's answer, and the test fails naming it.
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
		if !slices.Contains(answer, issue.canonical) {
			t.Errorf("IssueRepository(%s) = %s (%s under its recorded rename), which ProjectRepositories(%s) = %q does not hold",
				issue.key, issue.resolved, issue.canonical, issue.project, answer)
		}
		if !slices.Contains(union[issue.project], issue.canonical) {
			union[issue.project] = append(union[issue.project], issue.canonical)
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
		member := model.Actor{Viewer: &model.Viewer{Person: "bob", Restricted: true, Sees: []string{"PRIV"}, AnyPrivate: true}}
		open := model.Actor{Viewer: &model.Viewer{Person: "alice", Open: true, Sees: []string{"OTHER"}, AnyPrivate: true}}
		for _, issue := range issues {
			inPRIV := slices.Contains(answers["PRIV"], issue.canonical)
			if shown := repositoryPasses(t, database, member, issue.resolved); shown != inPRIV {
				t.Errorf("the proxy shows %s, %s's repository, to a person who sees only PRIV: %t; PRIV's answer holds it: %t",
					issue.resolved, issue.key, shown, inPRIV)
			}
			if shown := repositoryPasses(t, database, open, issue.resolved); shown == inPRIV {
				t.Errorf("the proxy shows %s, %s's repository, to an open viewer who does not see PRIV: %t; PRIV's answer holds it: %t",
					issue.resolved, issue.key, shown, inPRIV)
			}
		}
	})
}
