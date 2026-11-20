package appauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// Installations answers whether the GitHub App is installed on a repository. The daemon serves a
// repository of its project's mapping only once it is.
type Installations interface {
	InstalledOn(ctx context.Context, repo ghrepo.Repository) (bool, error)
}

var _ Installations = (*Manager)(nil)

// Logins answers the GitHub App's bot login under the App's own JWT, which needs no installation
// on any repository: it is the login the workflow judges a push's pusher and a review's author
// by, known even while the daemon serves no repository. An App on token_command has no JWT: its
// login is read under a token of any installation its command releases.
type Logins interface {
	AppLogin(ctx context.Context) (string, error)
}

var _ Logins = (*Manager)(nil)

// AppLogin is the App's bot login, "<slug>[bot]", read from GET /app under the App's JWT, or in
// token mode from GraphQL's viewer under a token of any installation, checked against the
// configured app_id (tokenIdentity).
func (m *Manager) AppLogin(ctx context.Context) (string, error) {
	app := m.githubApp
	if app.TokenCommand != "" {
		installation, err := m.anyInstallation(ctx, app)
		if err != nil {
			return "", fmt.Errorf("read the App's login: %w", err)
		}
		identity, err := m.tokenIdentity(ctx, app.AppID, installation.token)
		if err != nil {
			return "", fmt.Errorf("read the App's login: %w", err)
		}
		return identity.Name, nil
	}
	jwt, err := m.generateJWT(app.AppID, app.PrivateKey, m.now())
	if err != nil {
		return "", err
	}
	slug, err := m.appSlug(ctx, jwt)
	if err != nil {
		return "", fmt.Errorf("read the App's login: %w", err)
	}
	return slug + "[bot]", nil
}

// InstalledOn reads GET /repos/{owner}/{repo}/installation under the App's JWT: GitHub answers
// the installation that covers the repository, 2xx, or 404 when the App has none there. An owner's
// installation may cover only some of its repositories, so the read is per repository. Any other
// answer is an error naming the repository, a TransientError when it is GitHub's trouble. In
// token mode it reads the owner's installation's repositories instead (installedFromCommand).
func (m *Manager) InstalledOn(ctx context.Context, repo ghrepo.Repository) (bool, error) {
	app := m.githubApp
	if app.TokenCommand != "" {
		return m.installedFromCommand(ctx, repo)
	}
	jwt, err := m.generateJWT(app.AppID, app.PrivateKey, m.now())
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		m.baseURL+"/repos/"+url.PathEscape(repo.Owner())+"/"+url.PathEscape(repo.Name())+"/installation", nil)
	if err != nil {
		return false, err
	}
	m.appHeaders(request, jwt)
	response, err := m.client.Do(request)
	if err != nil {
		return false, &TransientError{Err: fmt.Errorf("read whether the App is installed on %s: %w", repo, err)}
	}
	body := refusalBody(response)
	switch {
	case response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices:
		return true, nil
	case response.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		return false, transient(response, body, fmt.Errorf("read whether the App is installed on %s failed (%d)", repo, response.StatusCode))
	}
}
