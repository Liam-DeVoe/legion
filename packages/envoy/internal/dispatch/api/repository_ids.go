package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Each write that sets a repository name, an issue's own repository (POST /api/v1/issues, PATCH
// /api/v1/issues/{key}) and a project's default (PUT /api/v1/settings/repo-projects/{owner}/{repo}
// leaving the mapping a default), records beside it the GitHub id GitHub answers for that name as
// the write runs (writtenRepository): the name a person writes now names the repository they mean
// now. A write that carries the name its row already stores keeps the row as it is, with its id or
// with none, and reads nothing: a read of a stored name can answer a repository that took the name
// later, and a new id on a row the project's owner left unconfirmed would undo the owner's
// confirmation (refs.ConfirmRepository). A read that does not answer stores the name with no id,
// except where the row belongs to a private project: there the write is refused 503
// REPOSITORY_ID_UNAVAILABLE and writes nothing (refuseUnidentifiedRepository), since every name a
// private project's own rows hold carries an id from its switch on (refs.UnconfirmedRepositories).

// codeRepositoryIDUnavailable is the code refuseUnidentifiedRepository refuses a write with.
const codeRepositoryIDUnavailable = "REPOSITORY_ID_UNAVAILABLE"

// writtenRepository is the repository name one write sets, with the GitHub id the write records
// beside it, read from GitHub at most once (id).
type writtenRepository struct {
	github *githubapp.Client
	name   string
	read   bool
	found  *int64
}

// writtenRepository is the repository canonical, the name a write sets, read through the
// server's GitHub App client.
func (s *server) writtenRepository(canonical string) *writtenRepository {
	return &writtenRepository{github: s.deps.GitHub, name: canonical}
}

// id is the GitHub id of the repository the written name reaches now (githubapp.CurrentRepository,
// which follows GitHub's redirect from an old name), read the first time it is asked for and kept:
// nil where GitHub did not answer, a deployment with no App key's included. ctx is the request's,
// never one held to a repository.
func (w *writtenRepository) id(ctx context.Context) *int64 {
	if w.read {
		return w.found
	}
	w.read = true
	owner, name, _ := strings.Cut(w.name, "/")
	current, err := w.github.CurrentRepository(ctx, owner, name)
	switch {
	case errors.Is(err, githubapp.ErrNoAppKey):
		slog.Debug("dispatch: a repository write records no GitHub id: no App key", "repository", w.name)
	case err != nil:
		slog.Warn("dispatch: a repository write could not read the repository's GitHub id; the name is stored with none", "repository", w.name, "error", err)
	default:
		w.found = &current.ID
	}
	return w.found
}

// sameRepositoryName reports whether a write that sets repo carries the name stored already, as
// GitHub compares names: without case.
func sameRepositoryName(stored *string, repo string) bool {
	return stored != nil && strings.EqualFold(*stored, repo)
}

// refuseUnidentifiedRepository refuses, with 503 REPOSITORY_ID_UNAVAILABLE and nothing written, a
// write that would leave a row of project, an issue of it or its default, holding the repository
// name repo with no GitHub id while project is private. It takes the project's owner lock shared
// before it reads the project's visibility, so it either reads the visibility a switch to private
// committed, which holds that lock exclusively, or the switch waits for this write and then sees
// its row (refs.UnconfirmedRepositories); a transaction that already holds the lock exclusively,
// an issue create's, takes nothing more. A write that clears the name (repo nil) or stores an id
// is refused nothing and takes nothing.
func refuseUnidentifiedRepository(ctx context.Context, tx pgx.Tx, project string, repo *string, id *int64) error {
	if repo == nil || id != nil {
		return nil
	}
	if err := store.LockOwnersShared(ctx, tx, store.ProjectOwner(project)); err != nil {
		return err
	}
	var visibility string
	if err := tx.QueryRow(ctx, `select visibility from projects where key = $1`, project).Scan(&visibility); err != nil {
		return fmt.Errorf("read the visibility of project %s: %w", project, err)
	}
	if visibility != model.ProjectPrivate {
		return nil
	}
	return errorf(http.StatusServiceUnavailable, codeRepositoryIDUnavailable,
		"GitHub did not answer the id of %s, and every repository a private project names records its id: nothing was written; try again", *repo)
}
