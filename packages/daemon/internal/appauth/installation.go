package appauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// Installations answers whether the GitHub App is installed on a repository. The daemon records
// an issue on a repository only once the App is installed on it.
//
// InstalledOn is GitHub's answer wherever GitHub holds the repository now, a redirect followed;
// InstalledUnder is its answer for the name asked alone, a name GitHub redirects answering that
// the App is not installed under it. The daemon reads each name a configured rename holds by the
// second, since a name GitHub redirects is no repository of its own (ghrepo.Renames), and every
// other name by the first.
type Installations interface {
	InstalledOn(ctx context.Context, repo ghrepo.Repository) (bool, error)
	InstalledUnder(ctx context.Context, repo ghrepo.Repository) (bool, error)
}

var _ Installations = (*Manager)(nil)

// NotInstalledOn is the sentence saying GitHub answered the App is not installed on repo, in the
// words the daemon refuses an admission with and legion state shows: "the App is not installed on
// acme/gadgets" (AppIs).
func NotInstalledOn(repo string) string {
	return AppIs("not installed on " + repo)
}

// AppIs is the sentence saying of the App what predicate says of it: "the App is <predicate>". It
// builds NotInstalledOn's sentence and the daemon's rename pass's of the App still installed on a
// configured pair's old name ("still installed on sjawhar/legion").
func AppIs(predicate string) string {
	return "the App is " + predicate
}

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
// configured app_id (tokenIdentity). In token mode the installation is kept until the login is
// read under it (loginRelease), so a call after one that failed past its release runs the
// command again only once that installation is within refreshWindow of its expiry.
func (m *Manager) AppLogin(ctx context.Context) (string, error) {
	app := m.githubApp
	if app.TokenCommand != "" {
		installation, err := m.loginRelease(ctx, app)
		if err != nil {
			return "", fmt.Errorf("read the App's login: %w", err)
		}
		bot, err := m.tokenIdentity(ctx, app.AppID, installation.token)
		if err != nil {
			return "", fmt.Errorf("read the App's login: %w", err)
		}
		m.forgetLoginRelease()
		return bot.identity.Name, nil
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
// installation may cover only some of its repositories, so the read is per repository. A redirect
// is followed, as the Manager's client follows it (net/http keeps the App JWT on a redirect within
// one host), so a name GitHub moved is answered for the repository where it is now. Any other
// answer is an error naming the repository, a TransientError when it is GitHub's trouble. In
// token mode it reads the owner's installation's repositories instead (installedFromCommand).
func (m *Manager) InstalledOn(ctx context.Context, repo ghrepo.Repository) (bool, error) {
	return m.installedOn(ctx, repo, false)
}

// InstalledUnder is InstalledOn of repo's own name, read without following a redirect. GitHub
// answers a moved repository's API path with a 301 to where the repository is now (GET
// /repos/sjawhar/legion answered 301 to /repositories/1146927990 on 2026-11-19), and a read that
// follows it reads the installation that covers the repository there, so the name it moved away
// from would answer installed. Here a 3xx is the answer instead: the App is not installed under
// that name. In token mode it is InstalledOn, whose read of the owner's own installation's
// repositories follows no name to another (installedFromCommand).
func (m *Manager) InstalledUnder(ctx context.Context, repo ghrepo.Repository) (bool, error) {
	return m.installedOn(ctx, repo, true)
}

// installedOn is InstalledOn, and with own set InstalledUnder: the read made through the Manager's
// client that follows no redirect (noRedirect), a 3xx answering not installed.
func (m *Manager) installedOn(ctx context.Context, repo ghrepo.Repository, own bool) (bool, error) {
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
	client := m.client
	if own {
		client = m.noRedirect
	}
	response, err := client.Do(request)
	if err != nil {
		return false, &TransientError{Err: fmt.Errorf("read whether the App is installed on %s: %w", repo, err)}
	}
	body := refusalBody(response)
	switch {
	case response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices:
		return true, nil
	case response.StatusCode == http.StatusNotFound:
		return false, nil
	case own && response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest:
		return false, nil
	default:
		return false, m.transient(response, body, fmt.Errorf("read whether the App is installed on %s failed (%d)", repo, response.StatusCode))
	}
}
