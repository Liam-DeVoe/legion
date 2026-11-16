package projectaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// repoProjectsPath is Dispatch's repository mapping route, which any caller Dispatch
// authenticates may read, the broker's own account token among them.
const repoProjectsPath = "/api/v1/settings/repo-projects"

// maxMappingBytes caps the repository mapping read from Dispatch. The mapping is every row
// Dispatch holds, about 160 bytes a row, so maxAnswerBytes, sized for one project's answer, would
// refuse a deployment past about 400 rows, and with it every daemon the NATS auth service holds to
// its exchange projects. 8 MiB holds about 50,000 rows and still refuses an answer too large to
// be Dispatch's.
const maxMappingBytes = 8 << 20

// RepoProject is one row of Dispatch's repository mapping, as GET /api/v1/settings/repo-projects
// writes it (Dispatch's model.RepoProject): a repository "owner/name", the project it maps to, and
// the "owner/name" it is called today, nil for a repository never renamed.
type RepoProject struct {
	Repo      string  `json:"repo"`
	Project   string  `json:"project"`
	RenamedTo *string `json:"renamed_to"`
}

// RepoProjects is Dispatch's whole repository mapping, asked afresh on every call and never
// cached: its one reader, the NATS auth service, asks it on a schedule of its own. It is asked as
// every route here is (ask), read to maxMappingBytes: a 401 or a 403 is ErrBrokerRefused, and every
// other status but a 200, like every other failure, is an error naming what failed and never the
// token.
func (c *Client) RepoProjects(ctx context.Context) ([]RepoProject, error) {
	status, body, err := c.ask(ctx, repoProjectsPath, nil, maxMappingBytes)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("projectaccess: dispatch answered %s %d with the code %q, which is no repository mapping", repoProjectsPath, status, refusalCode(bytes.NewReader(body)))
	}
	var rows []RepoProject
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("projectaccess: decode dispatch's answer to %s: %w", repoProjectsPath, err)
	}
	return rows, nil
}
