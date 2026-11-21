package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// appInstallationResponse is GET /api/v1/github/app-installation/{owner}/{repo}'s answer.
type appInstallationResponse struct {
	Installed bool `json:"installed"`
}

// getAppInstallation answers whether Dispatch's GitHub App, the one GitHub sends the webhook
// events of its repositories through, is installed on the path's repository: GitHub's
// installation lookup under the App's JWT, its 404 being false. Any authenticated caller may ask:
// the Legion daemon asks it for every repository it serves, since on a repository the App is not
// installed on GitHub sends Legion none of its events and nothing redelivers them. Dispatch with no
// App key answers 503 GITHUB_APP_UNCONFIGURED; any other failed lookup 502 GITHUB_UNREADABLE,
// naming the repository and GitHub's error.
//
// A repository the caller may not see by the GitHub proxy's rule (repositoriesHidden, over
// refs.RepositoryClause) answers exactly what one the App is not installed on answers, as the
// proxy answers it (githubapi.ProxyREST). As the proxy does, it judges the repository by the GitHub
// id the name reaches now (githubapp.Client.CurrentRepository: the installation lookup, the token
// mint and the read of the repository itself, following GitHub's redirect from an old name), so a
// private project's repository GitHub renamed or moved answers as not installed under every name
// it has had; GitHub is asked nothing more about a hidden repository. That read's own answers are
// the lookup's: a repository the App is not installed on is false, and a rate limit or any other
// failure 502 GITHUB_UNREADABLE, whatever the repository, so they tell a caller nothing of whose
// it is. No App key answers every repository alike, so it is answered before the repository is
// read. A reader that sees every object, as every open viewer of a deployment with no private
// project does, reads no id and no statement for it and is answered as before any project could
// be private (R9).
func (s *server) getAppInstallation(w http.ResponseWriter, r *http.Request) {
	reader, ok := s.readerOf(w, r)
	if !ok {
		return
	}
	repo, err := repoProjectPath(r)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	owner, name, _ := strings.Cut(repo, "/")
	if s.deps.GitHub == nil {
		writeError(w, "GITHUB_APP_UNCONFIGURED", http.StatusServiceUnavailable, githubapp.ErrNoAppKey.Error())
		return
	}
	var installed bool
	if seesEveryObject(reader) {
		installed, err = s.deps.GitHub.Installed(r.Context(), owner, name)
	} else {
		installed, err = s.installedAndShown(r.Context(), reader, owner, name)
	}
	if err != nil {
		writeError(w, "GITHUB_UNREADABLE", http.StatusBadGateway, fmt.Sprintf("read the GitHub App's installation on %s: %v", repo, err))
		return
	}
	WriteJSON(w, http.StatusOK, appInstallationResponse{Installed: installed})
}

// installedAndShown is whether the App is installed on owner/name and reader may see the
// repository the name reaches: the repository's GitHub id read as the App
// (githubapp.Client.CurrentRepository), which succeeds only where the App is installed, then
// refs.RepositoryClause over that id. A repository the App is not installed on is false with no
// error, as is a hidden one; any other failure of the read is its error. The installation lookup
// a reader that sees every object is answered from is the App's own request, which a rate limit in
// force on the App's scope holds unsent, so that limit is answered here first, whatever this
// client remembers of the repository's installation and token, and a repository is never answered
// differently under it for being installed, hidden or remembered; a rate limit the read meets is
// answered as the limit too.
func (s *server) installedAndShown(ctx context.Context, reader model.Actor, owner, name string) (bool, error) {
	if limited := s.deps.GitHub.HeldLimit(ctx, githubapp.AppScope); limited != nil {
		return false, limited
	}
	current, err := s.deps.GitHub.CurrentRepository(ctx, owner, name)
	if limited, ok := githubapp.AsRateLimit(err); ok {
		return false, limited
	}
	if errors.Is(err, githubapp.ErrNoInstallation) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	asked := askedRepository{Repo: owner + "/" + name, ID: current.ID}
	hidden, err := s.repositoriesHidden(ctx, reader, []askedRepository{asked})
	if err != nil {
		return false, err
	}
	return !hidden[asked], nil
}

// askedRepository is one repository whose visibility a read asks (repositoriesHidden): its name,
// owner/name, and the GitHub id recorded beside it or read for it, 0 for none.
type askedRepository struct {
	Repo string
	ID   int64
}

// askedOf is the repository repo, whose recorded GitHub id is id (nil for none), as
// repositoriesHidden asks it.
func askedOf(repo string, id *int64) askedRepository {
	if id == nil {
		return askedRepository{Repo: repo}
	}
	return askedRepository{Repo: repo, ID: *id}
}

// repositoriesHiddenQuery is refs.RepositoryClause over each repository of two arrays, the
// reader's visibility argument bound at $1, the repositories' names at $2 and their GitHub ids at
// $3 (0 for none), each answered with whether it is hidden. A repository is judged by its id, as
// the GitHub proxy judges one, so a private project's repository GitHub renamed or moved is hidden
// under its new name too; one asked with no id is judged by its name, which hides every name a
// private project the reader does not see holds, whatever id it recorded. No placeholder differs
// between reads, so the text is built once.
var repositoriesHiddenQuery = `select asked.repo, asked.id, not ` + refs.RepositoryClause("asked.repo", "nullif(asked.id, 0)", "$1") +
	` from unnest($2::text[], $3::bigint[]) as asked(repo, id)`

// repositoriesHidden answers, for each of asked, whether reader may not see it: whether it fails
// refs.RepositoryClause, read in one statement and keyed by the repository as asked. A reader that
// sees every object (seesEveryObject) sees every repository, and nothing is read for it (R9);
// nothing is read for no repository either. The App-installation route, and delivery's pull
// requests that name no issue and its runs that ship none (deliveryPopulation.cutTo), hold a
// repository to it, as the GitHub proxy does (routes' repositoryHidden).
func (s *server) repositoriesHidden(ctx context.Context, reader model.Actor, asked []askedRepository) (map[askedRepository]bool, error) {
	hidden := make(map[askedRepository]bool, len(asked))
	if len(asked) == 0 || seesEveryObject(reader) {
		return hidden, nil
	}
	names, ids := make([]string, len(asked)), make([]int64, len(asked))
	for index, repository := range asked {
		names[index], ids[index] = repository.Repo, repository.ID
	}
	rows, err := s.deps.Store.Pool.Query(ctx, repositoriesHiddenQuery, refs.VisibilityArg(reader), names, ids)
	if err != nil {
		return nil, fmt.Errorf("read whether the caller sees %d repositories: %w", len(asked), err)
	}
	defer rows.Close()
	for rows.Next() {
		var repository askedRepository
		var concealed bool
		if err := rows.Scan(&repository.Repo, &repository.ID, &concealed); err != nil {
			return nil, fmt.Errorf("read whether the caller sees a repository: %w", err)
		}
		hidden[repository] = concealed
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read whether the caller sees %d repositories: %w", len(asked), err)
	}
	return hidden, nil
}
