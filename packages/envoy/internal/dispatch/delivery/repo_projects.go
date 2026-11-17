package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// RepoProjectColumns are the repo_projects columns ScanRepoProject reads, in its order.
const RepoProjectColumns = `repo, project, is_default, created_by, created_at, renamed_to`

// ScanRepoProject decodes one RepoProjectColumns row into a repository's mapping.
func ScanRepoProject(row pgx.Row) (model.RepoProject, error) {
	var mapping model.RepoProject
	var createdBy []byte
	if err := row.Scan(&mapping.Repo, &mapping.Project, &mapping.Default, &createdBy, &mapping.CreatedAt, &mapping.RenamedTo); err != nil {
		return model.RepoProject{}, err
	}
	if err := json.Unmarshal(createdBy, &mapping.CreatedBy); err != nil {
		return model.RepoProject{}, fmt.Errorf("decode repository project author: %w", err)
	}
	return mapping, nil
}

// LockRepoProjectWrite takes, in this order, every lock a write to repo's mapping in repo_projects
// needs, and returns the mapping as it stands under them (nil when repo has none). projects are
// the projects the write may put the mapping in.
//
//   - the change log's lock (LockChanges): the mapping's trigger logs a settings change under it
//     (migration 20261117100020), and the write's later statements, a default switch of other
//     mappings and the events, would otherwise run after it, so it is taken first, before any row
//     lock: repo_projects' one position for the lock;
//   - repo's own advisory lock, which every writer of the mapping takes whether or not its row
//     exists yet, so the mapping's project and its existence hold still from here on (a default
//     switch in its project may still clear its is_default until the project lock below is held,
//     and the row read after that lock sees it);
//   - the event owners of the mapping's current project and of projects, in one LockOwners call
//     before the first event, which serializes each project's default switch and orders two
//     moves between the same projects;
//   - the mapping's row, for update, which conflicts with the key-share lock an issue write naming
//     it takes (api's checkIssueRepoMapped and the issues foreign key), so a count of the issues
//     naming it read after it holds until the write commits.
//
// An issue create locks its project before the mapping too (api's createIssue), so the two never
// wait on each other in a circle, and no issue write takes the change log's lock. Every writer of
// a mapping goes through here, so none takes the change log's lock after a row of the table: the
// PUT and the DELETE of api's mapping routes, the boot seed (api.SeedRepoProjects), cmd/dispatch-seed's
// projects, and the delivery fixtures that tests and the eval bench write
// (deliverytest.WriteRepoProjects).
func LockRepoProjectWrite(ctx context.Context, tx pgx.Tx, broker *events.Broker, repo string, projects ...string) (*model.RepoProject, error) {
	if err := LockChanges(ctx, tx); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		select pg_advisory_xact_lock(hashtext('dispatch-repo-project'), hashtext($1))
	`, repo); err != nil {
		return nil, fmt.Errorf("lock repository mapping %s: %w", repo, err)
	}
	var current string
	err := tx.QueryRow(ctx, `select project from repo_projects where repo = $1`, repo).Scan(&current)
	switch {
	case err == nil:
		projects = append(projects, current)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	if len(projects) == 0 {
		return nil, nil
	}
	owners := make([]model.Event, 0, len(projects))
	for _, project := range projects {
		owners = append(owners, model.Event{ProjectKey: &project})
	}
	if err := broker.LockOwners(ctx, tx, owners...); err != nil {
		return nil, err
	}
	mapping, err := ScanRepoProject(tx.QueryRow(ctx, `
		select `+RepoProjectColumns+` from repo_projects where repo = $1 for update
	`, repo))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mapping, nil
}
