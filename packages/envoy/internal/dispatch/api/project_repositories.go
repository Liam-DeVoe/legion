package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/access"
	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Before a project turns private its owner confirms which GitHub repository each name its own
// issues and its default hold with no id means (refs.UnconfirmedRepositories): the switch waits
// until each is confirmed (patchProjectAccess, 409 PRIVATE_PROJECT_NOT_PROVISIONED naming them).
// GET /api/v1/projects/{key}/repositories lists every name with the id recorded beside it and the
// repository GitHub answers for it now, and POST /api/v1/projects/{key}/repositories/confirm
// records the id the owner confirms (refs.ConfirmRepository). Both are the owner's alone, as every
// write of who may see the project is (ownerWrite).

// projectRepositories is the answer of both routes: every repository project's own issues and
// its default name, one for each name and recorded id, sorted by name and then id, the rows with no
// id first (refs.HeldRepositories).
type projectRepositories struct {
	Repositories []projectRepository `json:"repositories"`
}

// projectRepository is one repository a project's own rows name.
type projectRepository struct {
	// Repo is the name as stored, lower-cased.
	Repo string `json:"repo"`
	// ID is the GitHub id recorded beside it, null for the rows that hold the name with none: those
	// are the ones the switch to private waits for.
	ID *int64 `json:"id"`
	// GitHubID and GitHubFullName are the repository the name reaches on GitHub now, read as
	// Dispatch's App, following GitHub's redirect from an old name; both null when GitHub did not
	// answer. A full name that is not Repo is a repository renamed or moved since, or one that took
	// the name after another left it.
	GitHubID       *int64  `json:"github_id"`
	GitHubFullName *string `json:"github_full_name"`
	// NamedElsewhere is how many rows of the other projects the owner sees, issues' own
	// repositories and defaults, hold Repo or the full name GitHub gives it now with no id: a
	// confirmation with shared true records the id on them too.
	NamedElsewhere int `json:"named_elsewhere"`
	// SharedElsewhere is how many issues of the other projects the owner sees, open or done,
	// resolve to the repository's id, the recorded one or else GitHub's (refs.ProjectRepositoriesSQL,
	// a default included): their people read it through the GitHub proxy whatever this project's
	// visibility. It informs and holds nothing. A private project the owner does not see counts
	// toward neither (refs.RepositoryElsewhere).
	SharedElsewhere int `json:"shared_elsewhere"`
}

// confirmRepositoryInput is the body of POST /api/v1/projects/{key}/repositories/confirm.
type confirmRepositoryInput struct {
	// Repo is a name the project's own issues or its default hold, as GET answers it.
	Repo string `json:"repo" required:"true"`
	// ID is the GitHub id the owner confirms that name means: the github_id GET answered.
	ID int64 `json:"id" required:"true"`
	// Shared says the rows of other projects that hold the name, or the full name GitHub gives it
	// now, with no id mean this repository too, and records the id on them.
	Shared bool `json:"shared"`
}

// codeRepositoryChanged is a confirmation whose id is not the one GitHub answers for the name now.
const codeRepositoryChanged = "REPOSITORY_CHANGED"

// githubReadConcurrency bounds the GitHub reads one list makes at once, so a project of many
// names spends its installation's rate limit no faster than a few readers would.
const githubReadConcurrency = 4

// getProjectRepositories answers the repositories of project {key} for its owner.
func (s *server) getProjectRepositories(w http.ResponseWriter, r *http.Request) {
	key, who, _, ok := s.projectOwnerRead(w, r)
	if !ok {
		return
	}
	pool, err := s.readyPool()
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	answer, err := s.listProjectRepositories(r.Context(), pool, key, who)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, answer)
}

// confirmProjectRepository records the GitHub id the owner of project {key} confirms for a name
// its rows hold (refs.ConfirmRepository), and answers the project's repositories as the
// confirmation left them. The id must be the one GitHub answers for the name now (409
// REPOSITORY_CHANGED otherwise, naming what GitHub answers), read as the route runs (503
// REPOSITORY_ID_UNAVAILABLE when GitHub does not answer); a name no row of the project holds is
// 404 REPOSITORY_NOT_FOUND. Nothing is written by any refusal.
func (s *server) confirmProjectRepository(w http.ResponseWriter, r *http.Request) {
	var input confirmRepositoryInput
	if err := decodeJSON(r, &input); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	repo, _, err := externalRef(strings.TrimSpace(input.Repo) + "#1")
	if err != nil || !githubCouldHoldRepo(repo) {
		writeError(w, "REPOSITORY_INPUT", http.StatusBadRequest, fmt.Sprintf(`repo must be "owner/name" (got %q)`, input.Repo))
		return
	}
	if input.ID <= 0 {
		writeError(w, "REPOSITORY_INPUT", http.StatusBadRequest, "id must be the repository's GitHub id, a positive number")
		return
	}
	key, who, actor, ok := s.projectOwnerRead(w, r)
	if !ok {
		return
	}
	// GitHub is read before the transaction, so no lock waits on it.
	owner, name, _ := strings.Cut(repo, "/")
	current, err := s.deps.GitHub.CurrentRepository(r.Context(), owner, name)
	if err != nil {
		writeError(w, codeRepositoryIDUnavailable, http.StatusServiceUnavailable,
			fmt.Sprintf("GitHub did not answer which repository %s is now, so nothing was confirmed: try again", repo))
		return
	}
	if current.ID != input.ID {
		writeError(w, codeRepositoryChanged, http.StatusConflict, fmt.Sprintf(
			"%s is GitHub's %s (id %d) now, not id %d: nothing was confirmed; read the project's repositories again and confirm what the name is now",
			repo, current.FullName, current.ID, input.ID))
		return
	}
	tx, err := s.begin(r.Context())
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// The owner is read again under the project's owner lock, taken shared: a change of owner, which
	// takes it exclusively (access.LockForAccessWrite), is read rather than raced, and the issue
	// writes that take it shared go on beside the confirmation.
	if err := store.LockOwnersShared(r.Context(), tx, store.ProjectOwner(key)); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if !s.ownerOf(r.Context(), w, tx, key, who, actor) {
		return
	}
	var held bool
	if err := tx.QueryRow(r.Context(), `select exists (select 1 from issues where project_key = $1 and lower(repo) = $2)
		or exists (select 1 from repo_projects where project = $1 and is_default and lower(repo) = $2)`, key, repo).Scan(&held); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if !held {
		writeError(w, "REPOSITORY_NOT_FOUND", http.StatusNotFound,
			fmt.Sprintf("no issue of %s, and not its default, names %s: nothing was confirmed", key, repo))
		return
	}
	if _, err := refs.ConfirmRepository(r.Context(), tx, key, repo, current.FullName, current.ID, input.Shared); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeHandlerError(w, err)
		return
	}
	answer, err := s.listProjectRepositories(r.Context(), s.deps.Store.Pool, key, who)
	if err != nil {
		s.writeHandlerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, answer)
}

// projectOwnerRead holds a request about project {key}'s repositories to the project's owner: the
// caller acts as a person, sees the project (404 PROJECT_NOT_FOUND otherwise, exactly as a key no
// project holds), and is its owner (403 PROJECT_OWNER_REQUIRED naming the owner, to a caller who
// sees the project). It answers the key, the caller as it reads (who) and as the person it acts for
// (actor), and whether the request may go on.
func (s *server) projectOwnerRead(w http.ResponseWriter, r *http.Request) (key string, who, actor model.Actor, ok bool) {
	if actor, ok = s.actingPerson(w, r); !ok {
		return "", model.Actor{}, model.Actor{}, false
	}
	if who, ok = s.readerOf(w, r); !ok {
		return "", model.Actor{}, model.Actor{}, false
	}
	key = r.PathValue("key")
	if !viewerSees(who.Viewer, key) {
		s.writeAccessError(w, fmt.Errorf("%w: %s", access.ErrProjectNotFound, key))
		return "", model.Actor{}, model.Actor{}, false
	}
	pool, err := s.readyPool()
	if err != nil {
		s.writeHandlerError(w, err)
		return "", model.Actor{}, model.Actor{}, false
	}
	return key, who, actor, s.ownerOf(r.Context(), w, pool, key, who, actor)
}

// ownerOf reports whether actor owns project key as q reads it, as who sees it, and writes the
// refusal where not: 404 PROJECT_NOT_FOUND for a project who does not see, and 403
// PROJECT_OWNER_REQUIRED naming the owner for one it sees and does not own.
func (s *server) ownerOf(ctx context.Context, w http.ResponseWriter, q queryer, key string, who, actor model.Actor) bool {
	var owner *string
	err := q.QueryRow(ctx, `select owner from projects where key = $1 and `+access.ProjectClause("key", "$2"),
		key, refs.VisibilityArg(who)).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeAccessError(w, fmt.Errorf("%w: %s", access.ErrProjectNotFound, key))
		return false
	}
	if err != nil {
		s.writeHandlerError(w, err)
		return false
	}
	if owner == nil || *owner != actor.ID {
		writeError(w, "PROJECT_OWNER_REQUIRED", http.StatusForbidden,
			"only the project's owner confirms its repositories: ask its owner, "+ownerName(owner))
		return false
	}
	return true
}

// listProjectRepositories is project key's repositories as its owner, who, reads them
// (projectRepositories): its rows' names and ids, read through q under who's clause; GitHub's
// answer for each name, at most githubReadConcurrency read at once; and what the other projects
// who sees hold of each (refs.Elsewhere).
func (s *server) listProjectRepositories(ctx context.Context, q queryer, key string, who model.Actor) (projectRepositories, error) {
	held, err := refs.HeldRepositories(ctx, q, key, refs.VisibilityArg(who))
	if err != nil {
		return projectRepositories{}, err
	}
	current := make(map[string]*githubapp.RepositoryAnswer, len(held))
	for _, repository := range held {
		current[repository.Repo] = nil
	}
	var (
		mu      sync.Mutex
		readers sync.WaitGroup
		slots   = make(chan struct{}, githubReadConcurrency)
	)
	for name := range current {
		readers.Add(1)
		slots <- struct{}{}
		go func() {
			defer readers.Done()
			defer func() { <-slots }()
			owner, repo, _ := strings.Cut(name, "/")
			answer, err := s.deps.GitHub.CurrentRepository(ctx, owner, repo)
			if err != nil {
				return
			}
			mu.Lock()
			current[name] = &answer
			mu.Unlock()
		}()
	}
	readers.Wait()
	asked := make([]refs.AskedRepository, len(held))
	answer := projectRepositories{Repositories: make([]projectRepository, len(held))}
	for index, repository := range held {
		row := projectRepository{Repo: repository.Repo, ID: repository.ID}
		ask := refs.AskedRepository{Stored: repository.Repo}
		if repository.ID != nil {
			ask.ID = *repository.ID
		}
		if github := current[repository.Repo]; github != nil {
			row.GitHubID, row.GitHubFullName = &github.ID, &github.FullName
			ask.Current = github.FullName
			if ask.ID == 0 {
				ask.ID = github.ID
			}
		}
		answer.Repositories[index], asked[index] = row, ask
	}
	elsewhere, err := refs.Elsewhere(ctx, q, key, refs.VisibilityArg(who), asked)
	if err != nil {
		return projectRepositories{}, err
	}
	for index := range answer.Repositories {
		answer.Repositories[index].NamedElsewhere = elsewhere[index].Named
		answer.Repositories[index].SharedElsewhere = elsewhere[index].Shared
	}
	return answer, nil
}

// unconfirmedRepositoriesRefusal is the 409 PRIVATE_PROJECT_NOT_PROVISIONED a switch to private
// answers while its project's own rows hold a repository name with no GitHub id
// (refs.UnconfirmedRepositories): the refusal's code and words, and those names, for the owner to
// confirm (POST /api/v1/projects/{key}/repositories/confirm). A switch refused because the
// deployment does not name the project carries no such field (errorBody).
type unconfirmedRepositoriesRefusal struct {
	Code                    string   `json:"code"`
	Error                   string   `json:"error"`
	UnconfirmedRepositories []string `json:"unconfirmed_repositories"`
}

// unconfirmedRepositoriesError is a switch to private of project while names, each held by its
// own issues or its default with no GitHub id, wait for its owner's confirmation. It is
// errPrivateProjectNotProvisioned for every reader of the code, and writeAccessError answers it
// with the names (unconfirmedRepositoriesRefusal).
type unconfirmedRepositoriesError struct {
	project string
	names   []string
}

func (e *unconfirmedRepositoriesError) Error() string {
	return fmt.Sprintf("%s stays public until its owner confirms which GitHub repository each of these names its issues or its default hold means: %s (GET /api/v1/projects/%s/repositories, then POST /api/v1/projects/%s/repositories/confirm for each)",
		e.project, strings.Join(e.names, ", "), e.project, e.project)
}

func (e *unconfirmedRepositoriesError) Unwrap() error { return errPrivateProjectNotProvisioned }

// refuseUnconfirmedRepositories is a switch to private's wait for its owner's confirmations: an
// unconfirmedRepositoriesError while refs.UnconfirmedRepositories names a name of key's, read
// through the switch's transaction, which holds the project's owner lock, so a repository write of
// the project's either committed before it, and is read, or waits for the switch and then reads the
// project private (refuseUnidentifiedRepository).
func refuseUnconfirmedRepositories(ctx context.Context, q refs.Queryer, key string) error {
	names, err := refs.UnconfirmedRepositories(ctx, q, key)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return &unconfirmedRepositoriesError{project: key, names: names}
	}
	return nil
}
