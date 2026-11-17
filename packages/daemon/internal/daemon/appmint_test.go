package daemon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/bootprobe"
	"github.com/sjawhar/legion/daemon/internal/clock"
	"github.com/sjawhar/legion/daemon/internal/clock/clocktest"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// gitHubStandIn serves the GitHub App endpoints a boot's token mint calls, for the owner acme:
// installation discovery answers through discover, the installation is granted every permission
// a role's set names, the exchange and identity lookups succeed, and each App's slug is
// legion-test-<its id>, so the two bot logins differ as two real Apps' do.
// It counts each App's installation discoveries by the App id its JWT names.
type gitHubStandIn struct {
	mu          sync.Mutex
	discoveries map[string]int
}

func (g *gitHubStandIn) Discoveries() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return maps.Clone(g.discoveries)
}

// jwtIssuer is the App id a request's App JWT names.
func jwtIssuer(t *testing.T, r *http.Request) string {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		t.Errorf("installation discovery without an App JWT: %q", r.Header.Get("Authorization"))
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err == nil {
		err = json.Unmarshal(payload, &claims)
	}
	if err != nil {
		t.Errorf("decode the App JWT: %v", err)
	}
	return claims.Issuer
}

// appTokens is a real App token manager whose GitHub is a stand-in, the implement App id 1 and the
// review App id 2; discover answers each installation discovery, with its App and its number, from 1,
// among that App's. A request without the manager's credential (anAppCredential) is another
// process's: it is answered 404 and logged, and counts as no discovery.
func appTokens(t *testing.T, discover func(w http.ResponseWriter, r *http.Request, app string, n int)) (appauth.Tokens, *gitHubStandIn) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	standIn := &gitHubStandIn{discoveries: map[string]int{}}
	server := githubtest.Serve(t, githubtest.Spec{Credential: anAppCredential(&key.PublicKey), Handle: func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations":
			app := jwtIssuer(t, r)
			standIn.mu.Lock()
			standIn.discoveries[app]++
			n := standIn.discoveries[app]
			standIn.mu.Unlock()
			discover(w, r, app, n)
		case strings.HasSuffix(r.URL.Path, "/installation"):
			fmt.Fprint(w, `{"id":77}`)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			// A mint names the permissions its token holds, and refuses an answer wider than it
			// asked for (appauth), so the stand-in answers with them as the request named them,
			// as GitHub does.
			repositories, permissions := exchangeBody(t, r)
			mintedToken(w, "installation-token", repositories, permissions)
		case strings.HasPrefix(r.URL.Path, "/app/installations/"):
			// A mint reads what the installation is granted before it asks for a token and cuts
			// its request down to it (appauth's exchangeGranted), so the stand-in's installation
			// is granted every permission a role's set names.
			fmt.Fprint(w, `{"id":77,"permissions":{"actions":"write","checks":"write","contents":"write","metadata":"read","pull_requests":"write","statuses":"write","workflows":"write"}}`)
		case r.URL.Path == "/app":
			fmt.Fprintf(w, `{"slug":"legion-test-%s"}`, jwtIssuer(t, r))
		case strings.HasPrefix(r.URL.Path, "/users/"):
			fmt.Fprint(w, `{"id":42}`)
		default:
			githubtest.Unexpected(t, w, r)
		}
	}})
	return appauth.New(config.GitHubApps{
		Implement: config.GitHubApp{AppID: "1", PrivateKey: privatePEM},
		Review:    config.GitHubApp{AppID: "2", PrivateKey: privatePEM},
	}, appauth.Options{BaseURL: server.URL, HTTPClient: server.Client()}), standIn
}

func installed(w http.ResponseWriter) {
	fmt.Fprint(w, `[{"id":77,"account":{"login":"acme"}}]`)
}

// booting is a daemon a test boots through run, whose boot it may drive before it waits for the
// daemon to be ready or for run to return (readyOrExit).
type booting struct {
	obs  *observer
	done chan error
	// returned is set once a wait took run's result, so the cleanup waits for no second one.
	returned bool
}

// bootOn runs the daemon on cfg until the test ends, its App tokens from tokens and its time from
// clk, and returns at once, so the test can drive clk while the boot waits on it.
func bootOn(t *testing.T, cfg config.Config, tokens appauth.Tokens, clk clock.Clock, log *slog.Logger) *booting {
	t.Helper()
	return bootOnGitHub(t, cfg, tokens, repositoriesGitHub(t), clk, log)
}

// bootOnGitHub is bootOn with the daemon reading GitHub's REST API at githubAPI.
func bootOnGitHub(t *testing.T, cfg config.Config, tokens appauth.Tokens, githubAPI string, clk clock.Clock, log *slog.Logger) *booting {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b := &booting{done: make(chan error, 1)}
	o := overrides{
		listen:         heldListen,
		runtime:        fakeRuntime(fake.NewRuntime(), &built{}).runtime,
		clock:          clk,
		workflowTokens: tokens,
		githubAPI:      githubAPI,
	}
	b.obs = observing(&o)
	go func() { b.done <- run(ctx, cfg, log, o) }()
	t.Cleanup(func() {
		cancel()
		if b.returned {
			return
		}
		if err := testwait.Receive(t, "run to return once the test ended", b.done); err != nil {
			t.Errorf("run after the test: %v", err)
		}
	})
	return b
}

// readyOrExit waits until the daemon reports itself ready to serve or run returns, and says which:
// nil for the first, and run's error for the second, which must be one.
func (b *booting) readyOrExit(t *testing.T) error {
	t.Helper()
	returned, err := b.obs.readyOrReturned(t, b.done)
	if !returned {
		return nil
	}
	b.returned = true
	if err == nil {
		t.Fatal("run returned nil before the daemon was ready to serve")
	}
	return err
}

// bootsOrExits runs the daemon until it reports itself ready to serve or run returns, and says
// which: nil for the first, and run's error for the second, which must be one. Its clock is the
// still one, whose waits between a boot's attempts end at once.
func bootsOrExits(t *testing.T, cfg config.Config, tokens appauth.Tokens) error {
	t.Helper()
	return bootsOrExitsLogging(t, cfg, tokens, quietLogger())
}

// bootsOrExitsLogging is bootsOrExits with the daemon logging to log.
func bootsOrExitsLogging(t *testing.T, cfg config.Config, tokens appauth.Tokens, log *slog.Logger) error {
	t.Helper()
	return bootOn(t, cfg, tokens, newStillClock(), log).readyOrExit(t)
}

// fireArmed waits until the daemon has armed a timer on clk for d past clk's present, what names
// it, and moves clk to that moment, which fires it and any timer due no later. A fake made by
// clocktest.New stands still between moves, so the deadline a wait of d arms is known exactly.
func fireArmed(t *testing.T, clk *clocktest.Fake, what string, d time.Duration) {
	t.Helper()
	due := clk.Now().Add(d)
	testwait.Until(t, fmt.Sprintf("%s armed for %s on the fake clock", what, d), func() ([]time.Time, bool) {
		armed := clk.Armed()
		return armed, slices.ContainsFunc(armed, due.Equal)
	})
	clk.Set(due)
}

// fireBackoff fires, one after another, the first n waits of retry's backoff a boot probe arms on
// clk (bootprobe.Run): retry.Initial, doubling, capped at retry.Max.
func fireBackoff(t *testing.T, clk *clocktest.Fake, what string, retry bootprobe.Retry, n int) {
	t.Helper()
	wait := retry.Initial
	for attempt := range n {
		fireArmed(t, clk, fmt.Sprintf("%s (wait %d of %d)", what, attempt+1, n), wait)
		wait = min(2*wait, retry.Max)
	}
}

// A GitHub request at boot that never answers is GitHub's trouble, not the App's: the boot mints
// again and comes up. Before, the one request ran until the boot budget ran out, and the daemon
// exited (a Stage 3 restart died exactly so: "mint implement GitHub App token at boot: GitHub App
// installation discovery: ... context deadline exceeded", #1347). The attempt's bound and the wait
// before the next attempt are the daemon's clock's, which the test moves past each.
func TestABootWaitsOutAGitHubRequestThatNeverAnswers(t *testing.T) {
	hung := make(chan struct{})
	tokens, standIn := appTokens(t, func(w http.ResponseWriter, r *http.Request, app string, n int) {
		if app == "1" && n == 1 {
			close(hung)
			<-r.Context().Done()
			return
		}
		installed(w)
	})
	clk := clocktest.New(time.Now())
	boot := bootOn(t, workflowConfig(t, workflowNATS(t)), tokens, clk, quietLogger())
	testwait.Receive(t, "the implement App's first installation discovery", hung)
	fireArmed(t, clk, "the mint attempt's bound", appMintAttempt)
	fireArmed(t, clk, "the wait before the mint's next attempt", appMintRetry.Initial)
	if err := boot.readyOrExit(t); err != nil {
		t.Fatalf("boot = %v, want it to mint again after one GitHub request that never answered", err)
	}
	if got := standIn.Discoveries(); !maps.Equal(got, map[string]int{"1": 2, "2": 1}) {
		t.Errorf("installation discoveries by App = %v, want the implement App's that never answered and one more, and the review App's one", got)
	}
}

// A definitive answer (the App's key refused) is refused at once, naming it and the repository the
// boot minted for (workflowConfig's mapping, acme/widgets): no retry changes it.
func TestABootRefusesADefinitiveGitHubAnswerAtOnce(t *testing.T) {
	tokens, standIn := appTokens(t, func(w http.ResponseWriter, r *http.Request, _ string, _ int) {
		http.Error(w, `{"message":"A JSON web token could not be decoded"}`, http.StatusUnauthorized)
	})
	err := bootsOrExits(t, workflowConfig(t, workflowNATS(t)), tokens)
	if err == nil || !strings.Contains(err.Error(), "mint implement GitHub App token at boot for acme/widgets: ") || !strings.Contains(err.Error(), "(401)") {
		t.Fatalf("boot = %v, want the refusal naming the implement mint, the repository acme/widgets and GitHub's 401", err)
	}
	if got := standIn.Discoveries(); !maps.Equal(got, map[string]int{"1": 1}) {
		t.Errorf("installation discoveries by App = %v, want the implement App's one: a 401 is not asked again", got)
	}
}

// Both Apps' tokens share one retry budget, so a boot waits at most appMintRetry before it refuses,
// however the failures fall between the two mints. Here the implement App's discovery fails twice
// and then answers, and the review App's never does: the budget's attempts are spent with the
// review token asked only for the attempts the implement App's two failures left. With a budget per
// App the review App would be asked the whole budget's number of times, and the boot would wait
// both out, twice as long, which is what outlasted Stage 3's wait for /healthz. Each wait between
// attempts is the daemon's clock's, which the test moves past.
func TestABootGivesBothAppTokensOneRetryBudget(t *testing.T) {
	tokens, standIn := appTokens(t, func(w http.ResponseWriter, r *http.Request, app string, n int) {
		if app == "1" && n > 2 {
			installed(w)
			return
		}
		http.Error(w, "{}", http.StatusBadGateway)
	})
	clk := clocktest.New(time.Now())
	boot := bootOn(t, workflowConfig(t, workflowNATS(t)), tokens, clk, quietLogger())
	fireBackoff(t, clk, "the wait before the mint's next attempt", appMintRetry, appMintRetry.Attempts-1)
	err := boot.readyOrExit(t)
	budget := fmt.Sprintf("retry budget (%d attempts)", appMintRetry.Attempts)
	if err == nil || !strings.Contains(err.Error(), budget) || !strings.Contains(err.Error(), "mint review GitHub App token at boot") {
		t.Fatalf("boot = %v, want the refusal after the one budget's %d attempts, naming the review mint", err, appMintRetry.Attempts)
	}
	if got, want := standIn.Discoveries(), map[string]int{"1": 3, "2": appMintRetry.Attempts - 2}; !maps.Equal(got, want) {
		t.Errorf("installation discoveries by App = %v, want %v: the implement App's three and the review App's in the attempts left of one budget", got, want)
	}
}
