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

// While the deployment holds a private project, each write that sets a repository name, an issue's
// own repository (POST /api/v1/issues, PATCH /api/v1/issues/{key}) and a project's default (PUT
// /api/v1/settings/repo-projects/{owner}/{repo} leaving the mapping a default), records beside it
// the GitHub id GitHub answers for that name as the write runs (writtenRepository): the name a
// person writes now names the repository they mean now. Nothing is stored for a write whose id the
// read does not give: where GitHub answers that Dispatch's App cannot read the repository, it is 403
// REPOSITORY_NOT_AVAILABLE in the words a repository hidden from the writer is refused in, so a
// writer cannot tell the two apart; where GitHub gives no answer, it is 503
// REPOSITORY_ID_UNAVAILABLE in words that name no project. With no project private, no id is read
// and the name is stored with none, as before any project could be private (R9); the switch to
// private then waits for its owner to confirm each such name (refs.UnconfirmedRepositories). A write
// that carries the name its row already stores keeps the row as it is, with its id or with none,
// and reads nothing: a read of a stored name can answer a repository that took the name later, and
// a new id on a row the project's owner left unconfirmed would undo the owner's confirmation
// (refs.ConfirmRepository).

// codeRepositoryIDUnavailable is the code a repository write is refused with when GitHub does not
// answer the id it reads, or when its project turned private beside a write that read none.
const codeRepositoryIDUnavailable = "REPOSITORY_ID_UNAVAILABLE"

// repositoryIDUnavailable is the refusal of a repository write whose id GitHub did not answer: 503
// REPOSITORY_ID_UNAVAILABLE, naming no project and no repository.
func repositoryIDUnavailable() error {
	return errorf(http.StatusServiceUnavailable, codeRepositoryIDUnavailable, "GitHub did not answer; try again")
}

// writtenRepository is the repository name one write sets, with the GitHub id the write records
// beside it, read from GitHub at most once (id), and only while the deployment holds a private
// project (reads).
type writtenRepository struct {
	github *githubapp.Client
	name   string
	reads  bool
	read   bool
	found  *int64
	err    error
}

// writtenRepository is the repository canonical, the name a write by who sets, read through the
// server's GitHub App client while the deployment holds a private project, the flag who's viewer
// carries (Viewer.AnyPrivate, the one the GitHub proxy's R9 arm reads).
func (s *server) writtenRepository(canonical string, who model.Actor) *writtenRepository {
	return &writtenRepository{github: s.deps.GitHub, name: canonical, reads: who.Viewer != nil && who.Viewer.AnyPrivate}
}

// id is the GitHub id of the repository the written name reaches now (githubapp.CurrentRepository,
// which follows GitHub's redirect from an old name), read the first time it is asked for and kept.
// With no project private it reads nothing and answers nil. Where GitHub answers that Dispatch's
// App cannot read the repository, no installation covering it (githubapp.ErrNoInstallation) or a
// 403 or 404 for the repository itself (githubapp.ErrRepositoryUnreadable), it is
// repositoryNotAvailable; any other failure, which is no answer from GitHub about the repository (a
// rate limit, a 5xx, a timeout or a transport failure, and a deployment with no App key, which
// cannot ask), is repositoryIDUnavailable. ctx is the request's, never one held to a repository.
func (w *writtenRepository) id(ctx context.Context) (*int64, error) {
	if !w.reads {
		return nil, nil
	}
	if w.read {
		return w.found, w.err
	}
	w.read = true
	owner, name, _ := strings.Cut(w.name, "/")
	current, err := w.github.CurrentRepository(ctx, owner, name)
	switch {
	case errors.Is(err, githubapp.ErrNoInstallation) || errors.Is(err, githubapp.ErrRepositoryUnreadable):
		slog.Info("dispatch: a repository write names a repository Dispatch's GitHub App cannot read while a project is private; the write is refused", "repository", w.name, "error", err)
		w.err = repositoryNotAvailable()
	case err != nil:
		slog.Warn("dispatch: a repository write could not read the repository's GitHub id while a project is private; the write is refused", "repository", w.name, "error", err)
		w.err = repositoryIDUnavailable()
	default:
		w.found = &current.ID
	}
	return w.found, w.err
}

// sameRepositoryName reports whether a write that sets repo carries the name stored already, as
// GitHub compares names: without case.
func sameRepositoryName(stored *string, repo string) bool {
	return stored != nil && strings.EqualFold(*stored, repo)
}

// refuseUnidentifiedRepository refuses, with repositoryIDUnavailable and nothing written, a write
// that would leave a row of project, an issue of it or its default, holding the repository name
// repo with no GitHub id while project is private: a write that began with no project private
// read no id, and its project turned private beside it. It takes the project's owner lock shared
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
	return repositoryIDUnavailable()
}
