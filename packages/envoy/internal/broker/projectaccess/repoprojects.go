package projectaccess

import (
	"context"
	"fmt"
	"net/http"
)

// repoProjectsPath is Dispatch's repository mapping route, which any caller Dispatch
// authenticates may read, the broker's own account token among them.
const repoProjectsPath = "/api/v1/settings/repo-projects"

// RepoProject is one row of Dispatch's repository mapping, as GET /api/v1/settings/repo-projects
// writes it (Dispatch's model.RepoProject): a repository "owner/name", the project it maps to, and
// the "owner/name" it is called today, nil for a repository never renamed.
type RepoProject struct {
	Repo      string  `json:"repo"`
	Project   string  `json:"project"`
	RenamedTo *string `json:"renamed_to"`
}

// RepoProjects is Dispatch's whole repository mapping, asked afresh on every call and never
// cached: its one reader, the NATS auth service, asks it on a schedule of its own. A 401 or a 403
// is ErrBrokerRefused; any other failure, an answer past the 64 KiB the client reads of one among
// them, is an error naming what failed and never the token.
func (c *Client) RepoProjects(ctx context.Context) ([]RepoProject, error) {
	var rows []RepoProject
	status, err := c.get(ctx, repoProjectsPath, nil, &rows)
	switch {
	case err != nil:
		return nil, err
	case status != http.StatusOK:
		return nil, fmt.Errorf("projectaccess: dispatch answered %s %d, which is no repository mapping", repoProjectsPath, status)
	}
	return rows, nil
}
