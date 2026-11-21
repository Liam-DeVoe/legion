package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// linger_hours and production_check_hours reach the workflow engine as the durations they name, a
// decimal included: the Stage 4b proof lingers 0.3 hours so a tree closes inside one run, and an
// hour count truncated to zero would reach the engine as no linger at all, closing a finished tree
// the moment it ends, or as no check bound, which the engine's default would then stand in for.
func TestLingerHoursReachesTheEngineAsItsDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legion.yaml")
	body := "project: demo\nstate_dir: /var/lib/legion\npostgres_dsn: postgres://legion@127.0.0.1:5432/legion\nlinger_hours: 0.3\nproduction_check_hours: 0.1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForValidation(path, func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadForValidation: %v", err)
	}

	engine := engineConfig(cfg, "legion-reviewer[bot]", nil)
	if engine.Linger != 18*time.Minute {
		t.Errorf("the engine's linger = %s, want 18m0s", engine.Linger)
	}
	if engine.ProductionCheckBound != 6*time.Minute {
		t.Errorf("the engine's production check bound = %s, want 6m0s", engine.ProductionCheckBound)
	}
}

// loginTokens answers every set's lease, and the App's login, with its one bot login.
type loginTokens struct{ login string }

func (t loginTokens) Token(context.Context, appauth.TokenSet, string) (appauth.Lease, error) {
	return appauth.Lease{Token: "token", ExpiresAt: time.Now().Add(time.Hour), Identity: appauth.GitIdentity{Name: t.login}}, nil
}

func (t loginTokens) VerdictToken(ctx context.Context, owner string) (appauth.Lease, error) {
	return t.Token(ctx, appauth.SetVerdict, owner)
}

func (t loginTokens) AppLogin(context.Context) (string, error) {
	return t.login, nil
}

func (loginTokens) InstalledOn(context.Context, ghrepo.Repository) (bool, error) {
	return true, nil
}

// InstalledOwners answers no owner list: opening the workflow builds no gh bundle, and a test
// that builds one names its owners itself.
func (loginTokens) InstalledOwners(context.Context) ([]string, error) {
	return nil, errors.New("loginTokens lists no owner accounts, which a gh bundle holds one directory per owner of")
}

// Forget drops nothing: every lease is minted afresh.
func (loginTokens) Forget(appauth.TokenSet, string) {}

func (t loginTokens) ReadToken(ctx context.Context, _ string) (appauth.Lease, error) {
	lease, err := t.Token(ctx, "", "")
	lease.Token += "-read"
	return lease, err
}

// The engine judges a push by its pusher against the App's bot login, so boot refuses an App that
// names no login before anything else opens.
func TestOpenWorkflowRefusesAnAppThatNamesNoLogin(t *testing.T) {
	dispatchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !servedBootCall(t, w, r, "demo") {
			t.Errorf("Dispatch request = %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(dispatchServer.Close)
	cfg := config.Config{
		Project: "demo", DispatchURL: dispatchServer.URL,
		Projects: map[string]config.Project{"demo": {}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := openWorkflow(context.Background(), cfg, nil, "project-id", "dispatch-token", nil, newRepositories(cfg.Project, cfg.RepositoryRenames), log, loginTokens{}, repositoriesGitHub(t), nil, time.Now)
	if err == nil || !strings.Contains(err.Error(), "the GitHub App names no bot login") {
		t.Fatalf("openWorkflow = %v, want the refusal of an App with no login", err)
	}
}

// countedLogins is the one App's bot login, counting each read and failing a read for each error
// it still holds, in order.
type countedLogins struct {
	login string
	errs  []error
	reads int
}

func (c *countedLogins) AppLogin(context.Context) (string, error) {
	c.reads++
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		return "", err
	}
	return c.login, nil
}

// The boot reads the one App's bot login once, under the App's own JWT with no owner to mint for,
// and answers it: the engine judges a push's pusher against that login. One App serves every role,
// so no second login is read and none is compared. GitHub's trouble is read again; any other
// failure, and an App that names no login, is refused at once.
func TestOneAppLoginAtBoot(t *testing.T) {
	quickAppMints(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	const want = "legion-implementer[bot]"

	app := &countedLogins{login: want}
	login, err := appLoginAtBoot(context.Background(), app, log)
	if err != nil || login != want || app.reads != 1 {
		t.Fatalf("appLoginAtBoot = %q, %v after %d reads; want the App's login %s, read once", login, err, app.reads, want)
	}

	trouble := &countedLogins{login: want, errs: []error{&appauth.TransientError{Err: errors.New("GitHub answered 502")}}}
	if login, err := appLoginAtBoot(context.Background(), trouble, log); err != nil || login != want || trouble.reads != 2 {
		t.Fatalf("appLoginAtBoot after GitHub's trouble = %q, %v after %d reads; want the login read again", login, err, trouble.reads)
	}

	refused := &countedLogins{login: want, errs: []error{errors.New("GitHub answered 401")}}
	if _, err := appLoginAtBoot(context.Background(), refused, log); err == nil || !strings.Contains(err.Error(), "401") || refused.reads != 1 {
		t.Fatalf("appLoginAtBoot of a refused read = %v after %d reads; want the refusal at once", err, refused.reads)
	}

	if _, err := appLoginAtBoot(context.Background(), &countedLogins{}, log); err == nil || !strings.Contains(err.Error(), "the GitHub App names no bot login") {
		t.Fatalf("appLoginAtBoot of an App with no login = %v, want the refusal", err)
	}
}
