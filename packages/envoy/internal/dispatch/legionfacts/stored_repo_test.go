package legionfacts

import (
	"maps"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// TestEachStoredRowNamesTheRepositoryTheIssuePageResolves: a stored row's repo is the repository
// the issue resolves to by the server's one rule, which the issue page reads and
// refs.IssueRepository reads for one key: the issue's own, the nearest one up its parent chain,
// else its project's default repository (projects.default_repo). The history's project has a
// default repository; the first tree's implemented child names another as its own, so it reads
// that one while the rest of its tree reads the default.
func TestEachStoredRowNamesTheRepositoryTheIssuePageResolves(t *testing.T) {
	seeded := seed(t, 2)
	project := seeded.fixture.Project
	own := seeded.fixture.Keys[1]
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if _, err := seeded.database.Pool.Exec(t.Context(), statement, args...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	exec("give the project a default repository", `update projects set default_repo = 'acme/widgets' where key = $1`, project)
	exec("give one issue a repository of its own", `update issues set repo = 'acme/gadgets' where key = $1`, own)

	rows, _, err := ComputeProject(t.Context(), seeded.database.Pool, project, seeded.now)
	if err != nil {
		t.Fatalf("compute the facts of %s: %v", project, err)
	}
	if _, err := WriteProject(t.Context(), seeded.database.Pool, ProjectWrite{Project: project, Rows: rows, Whole: true}); err != nil {
		t.Fatalf("write the facts of %s: %v", project, err)
	}
	stored, err := readWhole(t.Context(), seeded.database.Pool, project)
	if err != nil {
		t.Fatalf("read the facts of %s: %v", project, err)
	}
	byRepo := map[string]int{}
	for _, key := range slices.Sorted(maps.Keys(stored)) {
		resolved, err := refs.IssueRepository(t.Context(), seeded.database.Pool, key)
		if err != nil {
			t.Fatalf("resolve the repository of %s: %v", key, err)
		}
		want := ""
		if resolved != nil {
			want = *resolved
		}
		if got := stored[key].Repo; got != want {
			t.Errorf("%s stores the repository %q, want the %q the issue page resolves", key, got, want)
		}
		byRepo[stored[key].Repo]++
	}
	if stored[own].Repo != "acme/gadgets" || byRepo["acme/widgets"] == 0 {
		t.Errorf("%s stores %q and %d rows store the default, want its own acme/gadgets and the rest the default",
			own, stored[own].Repo, byRepo["acme/widgets"])
	}
	t.Logf("%s: %d stored rows read against refs.IssueRepository, by repository %v", project, len(stored), byRepo)
}

// TestADefaultRepositoryChangeReResolvesEveryStoredRepository: a stored row's repository falls
// back to its project's default repository, so the pass that reads a write of the project's row
// (the project.updated event PUT /api/v1/projects/{key}/default-repository appends) resolves the
// stored rows of the project's issues again: once the project gains a default repository, the
// closed issues' rows, which no feed names, store what refs.IssueRepository answers, as the open
// ones do.
func TestADefaultRepositoryChangeReResolvesEveryStoredRepository(t *testing.T) {
	c := newConsumer(t, 3)
	c.pass(t)
	project := c.fixture.Project
	closed := 0
	for key, row := range c.rows(t) {
		if row.Repo != "" {
			t.Fatalf("%s stores %q before the project has a default repository", key, row.Repo)
		}
		if row.ClosedAt != nil {
			closed++
		}
	}
	if closed == 0 {
		t.Fatal("the history holds no closed issue's row, so this test checks no row only the re-resolve reaches")
	}
	// The route's write: the project's row, then its project.updated event, in one transaction.
	c.exec(t, "give the project a default repository", `
		with written as (update projects set default_repo = 'acme/widgets' where key = $1 returning key)
		insert into events (project_key, seq, type, actor, payload, notify)
		select key, (select coalesce(max(seq), 0) + 1 from events where project_key = $1), 'project.updated',
		       '{"kind":"user","id":"alice"}'::jsonb, jsonb_build_object('key', key, 'default_repo', 'acme/widgets'), true
		from written
	`, project)
	changed := c.pass(t)
	byRepo := map[string]int{}
	for key, row := range c.rows(t) {
		resolved, err := refs.IssueRepository(t.Context(), c.database.Pool, key)
		if err != nil {
			t.Fatalf("resolve the repository of %s: %v", key, err)
		}
		if resolved == nil || row.Repo != *resolved || row.Repo != "acme/widgets" {
			shown := "none"
			if resolved != nil {
				shown = *resolved
			}
			t.Errorf("%s (closed %t) stores %q after its project's default repository became acme/widgets; the issue page resolves %s", key, row.ClosedAt != nil, row.Repo, shown)
		}
		if row.ClosedAt != nil {
			byRepo[row.Repo]++
		}
	}
	if len(changed) == 0 {
		t.Error("the pass after the default repository's write changed no row")
	}
	t.Logf("the pass after the default repository's write changed %d rows; the %d closed rows by repository: %v", len(changed), closed, byRepo)
}
