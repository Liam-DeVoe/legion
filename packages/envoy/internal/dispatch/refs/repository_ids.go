package refs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A repository name a person writes is recorded with the GitHub id GitHub answered for it then:
// an issue's own repository (issues.repo_id) and a project's default (repo_projects.repo_id on the
// is_default row). A name stored before Dispatch recorded ids holds none, and so does one whose
// write could not read GitHub while its project was public. Dispatch never reads an id for a name
// already stored: GitHub answers an old name with the repository it moved to only until another
// repository takes the name, so a later read can answer a repository the writer never meant. The
// one exception is the project owner's confirmation before the project turns private
// (ConfirmRepository), where the owner sees what GitHub answers and says it is the repository
// they mean.

// heldRepositoriesSQL is every repository name project $1's own rows hold, with the id recorded
// beside each: one row (repo, repo_id, issue) for each issue of the project whose own repository is
// set, open or done, issue true, and one for the project's default, issue false. A repository an
// issue takes from its parent is its parent's row, so it is not here. repo is lower-cased.
const heldRepositoriesSQL = `
	select lower(i.repo) as repo, i.repo_id, true as issue from issues i where i.project_key = $1 and i.repo is not null
	union all
	select lower(m.repo), m.repo_id, false from repo_projects m where m.project = $1 and m.is_default`

// unconfirmedRepositoriesQuery is UnconfirmedRepositories' one statement: the names of
// heldRepositoriesSQL whose row holds no id, each once, sorted byte-wise.
var unconfirmedRepositoriesQuery = `
	select coalesce(array_agg(distinct held.repo collate "C" order by held.repo collate "C"), '{}')
	from (` + heldRepositoriesSQL + `) held
	where held.repo_id is null`

// UnconfirmedRepositories is each repository name an issue of project key, or its default,
// holds with no GitHub id recorded beside it, read through q: each name once, lower-cased and
// sorted byte-wise, [] when every name holds an id. A switch to private waits while it names any
// (api's switch, 409 PRIVATE_PROJECT_NOT_PROVISIONED), since the GitHub proxy can tell a private
// project's repository from one that took its name later only by the id. A repository an issue of
// the project takes from a parent of another project is that parent's, and is not listed: the
// parent resolves to it too, so the proxy already shows it wherever the parent is seen, and a
// private parent's project confirmed it at its own switch.
//
// It reads every row of the project whoever runs it: it answers only the project's owner, who
// sees the project, and the switch, which holds the project's owner lock while it reads.
func UnconfirmedRepositories(ctx context.Context, q Queryer, key string) ([]string, error) {
	names := []string{}
	if err := q.QueryRow(ctx, unconfirmedRepositoriesQuery, key).Scan(&names); err != nil {
		return nil, fmt.Errorf("read the unconfirmed repositories of project %s: %w", key, err)
	}
	return names, nil
}

// HeldRepository is one repository a project's own rows name, as recorded: Repo, lower-cased, with
// ID, the GitHub id recorded beside it, nil for the rows that hold the name with none; Issues, how
// many issues of the project, open or done, hold it as their own repository with that id; and
// Default, whether the project's default holds it.
type HeldRepository struct {
	Repo    string
	ID      *int64
	Issues  int
	Default bool
}

// heldRepositoriesQuery is HeldRepositories' one statement.
var heldRepositoriesQuery = `
	select held.repo, held.repo_id, (count(*) filter (where held.issue))::int, bool_or(not held.issue)
	from (` + heldRepositoriesSQL + `) held
	group by held.repo, held.repo_id
	order by held.repo collate "C", held.repo_id nulls first`

// HeldRepositories is every repository project key's own issues and default hold, one for each
// name and recorded id, by name byte-wise and then the rows with no id first, read through q. A
// name some rows hold with an id and others with none is two of them, and so is a name rows hold
// with two ids: a repository that took the name after the first was renamed. [] for a project
// whose rows name none. It reads as UnconfirmedRepositories does, for the project's owner.
func HeldRepositories(ctx context.Context, q Queryer, key string) ([]HeldRepository, error) {
	rows, err := q.Query(ctx, heldRepositoriesQuery, key)
	if err != nil {
		return nil, fmt.Errorf("read the repositories of project %s: %w", key, err)
	}
	held, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (HeldRepository, error) {
		var repository HeldRepository
		err := row.Scan(&repository.Repo, &repository.ID, &repository.Issues, &repository.Default)
		return repository, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the repositories of project %s: %w", key, err)
	}
	if held == nil {
		held = []HeldRepository{}
	}
	return held, nil
}

// RepositoryElsewhere is what the other projects hold of one repository of a project (Elsewhere):
// Named, how many of their rows, issues' own repositories and defaults, hold its stored name or
// the name GitHub gives it now with no id, so the project's confirmation may speak for them
// (ConfirmRepository's shared); and Shared, how many of their issues, open or done, resolve to its
// id (ProjectRepositoriesSQL, a default included), so their people read it through the GitHub
// proxy already. Each is a count and names no project or issue: a repository's name is not a
// project's content.
type RepositoryElsewhere struct {
	Named, Shared int
}

// AskedRepository is one repository Elsewhere counts for: its stored name, the name GitHub gives
// it now ("" when GitHub did not answer), and its id (0 for none known).
type AskedRepository struct {
	Stored, Current string
	ID              int64
}

// elsewhereQuery is Elsewhere's one statement: project $1, and the asked repositories' stored
// names, current names and ids as three arrays of one length, $2, $3 and $4, "" and 0 for none.
// Only the issues of other projects are walked, once, for every id together.
var elsewhereQuery = `
	with shared as (
		select owned.repo_id, count(*)::int as issues
		from (` + ProjectRepositoriesSQL(`project_key <> $1`) + `) owned
		where owned.repo_id = any($4::bigint[])
		group by owned.repo_id
	), unrecorded as (
		select lower(i.repo) as repo from issues i where i.project_key <> $1 and i.repo is not null and i.repo_id is null
		union all
		select lower(m.repo) from repo_projects m where m.project <> $1 and m.is_default and m.repo_id is null
	)
	select (select count(*) from unrecorded where unrecorded.repo in (lower(asked.stored), lower(nullif(asked.current, ''))))::int,
		coalesce((select shared.issues from shared where shared.repo_id = nullif(asked.id, 0)), 0)
	from unnest($2::text[], $3::text[], $4::bigint[]) with ordinality as asked(stored, current, id, position)
	order by asked.position`

// Elsewhere is, for each of asked in order, what the projects other than project hold of it
// (RepositoryElsewhere), read through q in one statement. It reads every other project's rows
// whoever runs it, and answers counts alone, for project's owner.
func Elsewhere(ctx context.Context, q Queryer, project string, asked []AskedRepository) ([]RepositoryElsewhere, error) {
	answers := make([]RepositoryElsewhere, 0, len(asked))
	if len(asked) == 0 {
		return answers, nil
	}
	stored, current, ids := make([]string, len(asked)), make([]string, len(asked)), make([]int64, len(asked))
	for index, repository := range asked {
		stored[index], current[index], ids[index] = repository.Stored, repository.Current, repository.ID
	}
	rows, err := q.Query(ctx, elsewhereQuery, project, stored, current, ids)
	if err != nil {
		return nil, fmt.Errorf("count what other projects hold of the repositories of %s: %w", project, err)
	}
	answers, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RepositoryElsewhere, error) {
		var answer RepositoryElsewhere
		err := row.Scan(&answer.Named, &answer.Shared)
		return answer, err
	})
	if err != nil {
		return nil, fmt.Errorf("count what other projects hold of the repositories of %s: %w", project, err)
	}
	return answers, nil
}

// Confirmed is what ConfirmRepository recorded: Own, the rows of the project that took the id, and
// Others, the rows of other projects that did.
type Confirmed struct {
	Own, Others int
}

// confirmRepositoryStatement is ConfirmRepository's one statement: project $1, the stored name $2,
// the id $3, the name GitHub gives the repository now $4, and whether the other projects' rows take
// the id too, $5.
const confirmRepositoryStatement = `
	with own_issues as (
		update issues set repo_id = $3 where project_key = $1 and lower(repo) = lower($2) and repo_id is null returning 1
	), own_default as (
		update repo_projects set repo_id = $3 where project = $1 and is_default and lower(repo) = lower($2) and repo_id is null returning 1
	), other_issues as (
		update issues set repo_id = $3
		where $5 and project_key <> $1 and lower(repo) in (lower($2), lower($4)) and repo_id is null returning 1
	), other_defaults as (
		update repo_projects set repo_id = $3
		where $5 and project <> $1 and is_default and lower(repo) in (lower($2), lower($4)) and repo_id is null returning 1
	)
	select ((select count(*) from own_issues) + (select count(*) from own_default))::int,
		((select count(*) from other_issues) + (select count(*) from other_defaults))::int`

// ConfirmRepository records id, the GitHub id the project's owner confirmed for the name stored,
// on every row of project key that holds that name with no id: each issue whose own repository it
// is, and the default when it is that name. With shared it records id too on every row of the
// other projects that holds the stored name, or current, the name GitHub gives the repository
// now, with no id: the owner says those rows mean this repository, as a repository the project
// shares with another project, and their people read it through the GitHub proxy from then on.
// Without shared those rows keep no id, and the proxy shows the repository through them only to a
// viewer who sees a project that holds its id (RepositoryClause). A row that holds an id already
// keeps it. It writes through q, the confirm's transaction, and answers what it recorded.
func ConfirmRepository(ctx context.Context, q Queryer, key, stored, current string, id int64, shared bool) (Confirmed, error) {
	var confirmed Confirmed
	if err := q.QueryRow(ctx, confirmRepositoryStatement, key, stored, id, current, shared).Scan(&confirmed.Own, &confirmed.Others); err != nil {
		return Confirmed{}, fmt.Errorf("record the GitHub id %d of %s for project %s: %w", id, stored, key, err)
	}
	return confirmed, nil
}
