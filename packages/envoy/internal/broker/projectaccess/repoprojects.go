package projectaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
// cached: its one reader, the NATS auth service, asks it on a schedule of its own. It asks as get
// does (projectaccess.go): a 401 or a 403 is ErrBrokerRefused, decided by the status before any
// of the body is read, and every other failure, an answer past maxMappingBytes among them, is an
// error naming what failed and never the token. It repeats get's request only for its larger
// bound, since get reads at most maxAnswerBytes; once #4119 has landed, get can take the bound
// and this repeat goes.
func (c *Client) RepoProjects(ctx context.Context) ([]RepoProject, error) {
	bearer, err := c.account.Read()
	if err != nil {
		return nil, fmt.Errorf("projectaccess: read the broker's account token: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+repoProjectsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("projectaccess: build the request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("projectaccess: ask dispatch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: dispatch answered %s %d with the code %q", ErrBrokerRefused, repoProjectsPath, response.StatusCode, refusalCode(response.Body))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMappingBytes+1))
	if err != nil {
		return nil, fmt.Errorf("projectaccess: read dispatch's answer: %w", err)
	}
	if len(body) > maxMappingBytes {
		return nil, fmt.Errorf("projectaccess: dispatch's answer to %s is over %d bytes", repoProjectsPath, maxMappingBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("projectaccess: dispatch answered %s %d with the code %q, which is no repository mapping", repoProjectsPath, response.StatusCode, refusalCode(bytes.NewReader(body)))
	}
	var rows []RepoProject
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("projectaccess: decode dispatch's answer to %s: %w", repoProjectsPath, err)
	}
	return rows, nil
}
