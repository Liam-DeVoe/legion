package api

// writtenColumn is one of the columns the repository rule reads (refs.IssueRepoTerms) that a
// write route changes: which repository an issue names, which issue it sits under, which project
// it belongs to, and which repository a project's issues fall back to. A route that changes one
// can change the repository an issue resolves to, so it is one of the writes that record the
// repository's GitHub id (writtenRepository) and that the switch to private waits against
// (refuseUnidentifiedRepository).
type writtenColumn string

const (
	// writesIssueRepo is issues.repo, an issue's own repository.
	writesIssueRepo writtenColumn = "issues.repo"
	// writesIssueParent is issues.parent_key, the issue above it on child_of.
	writesIssueParent writtenColumn = "issues.parent"
	// writesIssueProject is issues.project_key, the project an issue belongs to: written by an
	// issue's create alone, since no route moves an issue to another project.
	writesIssueProject writtenColumn = "issues.project"
	// writesDefaultRepository is a project's default repository: the repo_projects row is_default
	// marks, set, moved or removed.
	writesDefaultRepository writtenColumn = "repo_projects.default"
)

// writeColumns names, by "<method> <pattern>", every write route that changes a writtenColumn,
// and each column it changes; routes() stamps it onto apiRoute.Writes. A route this map leaves out
// changes none of them. TestEveryWriteDeclaresItsColumns holds the map to what each route writes,
// running every write route under triggers that record each of these columns a statement writes,
// and TestEveryRepositoryWriteRecordsItsID holds every route named here to recording the GitHub id
// of a repository it sets.
var writeColumns = map[string][]writtenColumn{
	"POST /api/v1/issues":                                  {writesIssueRepo, writesIssueParent, writesIssueProject},
	"PATCH /api/v1/issues/{key}":                           {writesIssueRepo, writesIssueParent},
	"PUT /api/v1/settings/repo-projects/{owner}/{repo}":    {writesDefaultRepository},
	"DELETE /api/v1/settings/repo-projects/{owner}/{repo}": {writesDefaultRepository},
}
