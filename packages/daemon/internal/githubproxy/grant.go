// Package githubproxy is a Legion role's GitHub forwarder (`legion forwarder`): a loopback HTTP
// CONNECT proxy that the role's environment names in HTTPS_PROXY and https_proxy, and the keeper
// of the grant it forwards under.
//
// The role holds its own key (the shim makes it in the role's key directory) and the daemon
// enrolls it; the forwarder asks the secrets broker, with that key, for a grant of the role's
// GitHub secrets, which the deployment names by role (runtime.kubernetes.agent_secrets.github).
// The grant lasts an hour, names no repository, and holds one placeholder for each account the
// role's App is installed on, and no token. The forwarder writes each placeholder where the daemon
// writes a token for a role whose secrets are not behind the proxy: one gh directory per account
// (ghconfig.WriteBundle), whose hosts.yml `legion gh` and `legion git-credential` read. A CONNECT
// to a host the grant binds goes to the broker's proxy port with the grant's proxy password; the
// broker swaps each placeholder a request carries for its account's token. Every other CONNECT
// the forwarder opens itself, so a broker outage stops only the calls that need a credential.
//
// The forwarder asks for the next grant with ten minutes of the hour left, and asks again at once
// when the broker's proxy answers 407, which it does for a grant the broker ended early; it then
// retries the connection with the new grant. It revokes the grants it holds when it stops, at the
// end of the role's generation, and the daemon's revocation of the generation's enrollment ends
// any it missed.
package githubproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/cipher"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// Lifetime is a role grant's lifetime: an hour, the life of an App installation token, so a
// placeholder a missed revocation leaves works no longer than the token it replaced did.
const Lifetime = time.Hour

// RenewBefore is how long before a grant expires the forwarder asks for the next one: ten minutes
// of the hour, which leaves room for retries. The broker hands an automatic grant back only in the
// first half of its life (requests' reuseLiveGrant), so the ask gets a new grant, and the old one
// lives on until its own expiry, which lets calls made under it finish.
const RenewBefore = 10 * time.Minute

// githubSlotPrefix opens the slot of one installation of a github-app secret's release, followed
// by the account's login, lowercased (the broker's requests.SlotGitHubPrefix).
const githubSlotPrefix = "github:"

// Broker is the role's session at the secrets broker, as the forwarder uses it
// (*cipher.Session).
type Broker interface {
	Request(ctx context.Context, names []string, reason string, lifetime int) (cipher.RequestAnswer, error)
	WaitRequest(ctx context.Context, id string, wait time.Duration) (cipher.RequestAnswer, error)
	Values(ctx context.Context, id string) (cipher.Release, error)
	Revoke(ctx context.Context, id string) error
}

// Grant is one grant of the role's secrets as the forwarder holds it: its id and expiry, the
// broker's proxy and the password a connection to it opens with, the hosts the grant binds, and
// each account's placeholder, by the account's lowercased login.
type Grant struct {
	ID        string
	ExpiresAt time.Time
	ProxyURL  *url.URL
	Password  string
	CA        string
	Hosts     []string
	Accounts  map[string]string
}

// Binds reports whether the grant sends a CONNECT to host:port through the broker: port 443 and a
// host the grant binds, by its exact name or, for an entry `*.<domain>`, under that domain, as
// the broker's proxy matches them.
func (g *Grant) Binds(host, port string) bool {
	if port != "443" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, bound := range g.Hosts {
		bound = strings.ToLower(bound)
		if suffix, wildcard := strings.CutPrefix(bound, "*."); wildcard {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == bound {
			return true
		}
	}
	return false
}

// grantOf reads a release into a Grant, refusing one the forwarder cannot stand behind: a name of
// the role's released as a value, which would put a credential in the role's files; a release
// with no proxy object, no proxy password or a proxy URL that is no http(s) URL; and two names
// holding a placeholder for one account, since a gh directory holds one token. A name of the
// role's with no github slot is no refusal: its hosts go through the proxy all the same.
func grantOf(id string, names []string, release cipher.Release) (*Grant, error) {
	for _, name := range names {
		if _, released := release.Values[name]; released {
			return nil, fmt.Errorf("the broker released %s as its value, not behind its proxy: a role's GitHub secret must have a kind", name)
		}
	}
	if release.Proxy == nil {
		return nil, errors.New("the release holds no proxy object")
	}
	if release.ExpiresAt == nil {
		return nil, errors.New("the grant has no expiry: a role's grant lasts an hour")
	}
	proxy, err := url.Parse(release.Proxy.ProxyURL)
	if err != nil || (proxy.Scheme != "http" && proxy.Scheme != "https") || proxy.Host == "" {
		return nil, errors.New("the release's proxy URL is no http(s) URL with a host")
	}
	if release.Proxy.ProxyPassword == "" {
		return nil, errors.New("the release names no proxy password")
	}
	grant := &Grant{ID: id, ExpiresAt: *release.ExpiresAt, ProxyURL: proxy, Password: release.Proxy.ProxyPassword,
		CA: release.Proxy.CACertificate, Accounts: map[string]string{}}
	owner := map[string]string{}
	for _, name := range names {
		held, ok := release.Proxy.Names[name]
		if !ok {
			return nil, fmt.Errorf("the release holds no placeholder for %s", name)
		}
		for _, host := range held.Hosts {
			if !slices.Contains(grant.Hosts, host) {
				grant.Hosts = append(grant.Hosts, host)
			}
		}
		for slot, placeholder := range held.Slots {
			account, ok := strings.CutPrefix(slot, githubSlotPrefix)
			if !ok {
				continue
			}
			account = strings.ToLower(account)
			if other, clash := owner[account]; clash {
				return nil, fmt.Errorf("%s and %s both hold a placeholder for account %s, and a gh directory holds one token", other, name, account)
			}
			owner[account] = name
			grant.Accounts[account] = placeholder
		}
	}
	slices.Sort(grant.Hosts)
	return grant, nil
}

// keeper holds the role's current grant, asks for the next one when it nears its expiry or when
// the broker ended it early, and writes each grant's placeholders into the role's gh bundle.
type keeper struct {
	open         func() (Broker, error)
	names        []string
	reason       string
	ghDir        string
	defaultOwner string
	renames      []ghrepo.Rename
	renewBefore  time.Duration
	log          *slog.Logger

	mu      sync.Mutex
	current *Grant
	held    map[string]time.Time // every grant asked for and not yet revoked, by id, with its expiry
	ready   chan struct{}        // closed once the first grant is held
	asking  *ask                 // the ask in flight, which every caller joins
	// saidNotEnrolled is whether the wait for the role's enrollment was logged, which is said
	// once.
	saidNotEnrolled bool
}

// ask is one ask for a grant, which every caller that needs one while it runs joins.
type ask struct {
	done  chan struct{}
	grant *Grant
	err   error
}

func newKeeper(open func() (Broker, error), names []string, reason, ghDir, defaultOwner string, renames []ghrepo.Rename, renewBefore time.Duration, log *slog.Logger) *keeper {
	return &keeper{open: open, names: names, reason: reason, ghDir: ghDir, defaultOwner: defaultOwner, renames: renames,
		renewBefore: renewBefore, log: log, held: map[string]time.Time{}, ready: make(chan struct{})}
}

// Current is the grant held now, nil before the first.
func (k *keeper) Current() *Grant {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.current
}

// awaitFirst waits for the first grant, until ctx ends.
func (k *keeper) awaitFirst(ctx context.Context) (*Grant, error) {
	select {
	case <-k.ready:
		return k.Current(), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("no grant of the role's GitHub secrets yet: %w", context.Cause(ctx))
	}
}

// Replace asks for a new grant at once in place of failed, the grant whose password the broker's
// proxy refused, and answers the grant held once that ask ends. A caller whose failed grant was
// already replaced gets the current one without asking; callers that come while an ask runs join
// it.
func (k *keeper) Replace(ctx context.Context, failed *Grant) (*Grant, error) {
	k.mu.Lock()
	if k.current != nil && failed != nil && k.current.ID != failed.ID {
		current := k.current
		k.mu.Unlock()
		return current, nil
	}
	k.mu.Unlock()
	return k.obtain(ctx, "the broker's proxy refused the grant's password (407)")
}

// obtain makes one ask for a grant, or joins the one in flight, and on success makes it current
// and writes its placeholders into the bundle.
func (k *keeper) obtain(ctx context.Context, why string) (*Grant, error) {
	k.mu.Lock()
	if k.asking == nil {
		a := &ask{done: make(chan struct{})}
		k.asking = a
		go k.run(a, why)
	}
	a := k.asking
	k.mu.Unlock()
	select {
	case <-a.done:
		return a.grant, a.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// askBound bounds one ask: the request, its wait for a decision, the release and the bundle's
// write. An agent-tier secret is granted at once, so most asks take one round trip.
const askBound = 50 * time.Second

func (k *keeper) run(a *ask, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), askBound)
	defer cancel()
	grant, err := k.ask(ctx)
	k.mu.Lock()
	k.asking = nil
	if err == nil {
		k.held[grant.ID] = grant.ExpiresAt
		first := k.current == nil
		k.current = grant
		if first {
			close(k.ready)
		}
	}
	k.mu.Unlock()
	switch {
	case errors.Is(err, cipher.ErrNotEnrolled):
		// The daemon enrolls the role's key once its agent has registered, so a forwarder started
		// with the generation waits for it: said once, not at every second's look.
		k.mu.Lock()
		said := k.saidNotEnrolled
		k.saidNotEnrolled = true
		k.mu.Unlock()
		if !said {
			k.log.Info("forwarder: waiting for the daemon to enroll the role's key", "names", k.names)
		}
	case err != nil:
		k.log.Warn("forwarder: no grant of the role's GitHub secrets", "why", why, "names", k.names, "err", err)
	default:
		k.log.Info("forwarder: holds a grant of the role's GitHub secrets", "why", why, "grant", grant.ID,
			"expires_at", grant.ExpiresAt.UTC().Format(time.RFC3339), "accounts", slices.Sorted(maps.Keys(grant.Accounts)), "hosts", grant.Hosts)
	}
	a.grant, a.err = grant, err
	close(a.done)
}

// ask is one request of the role's names, its wait while it is pending, its release and the
// bundle's write. A release the forwarder refuses is revoked before the refusal is answered, so
// no grant it will not use stays live.
func (k *keeper) ask(ctx context.Context) (*Grant, error) {
	broker, err := k.open()
	if err != nil {
		return nil, err
	}
	answer, err := broker.Request(ctx, k.names, k.reason, int(Lifetime/time.Second))
	if err != nil {
		return nil, fmt.Errorf("ask for %v: %w", k.names, err)
	}
	for answer.State == "pending" {
		id := answer.RequestID
		if answer, err = broker.WaitRequest(ctx, id, 20*time.Second); err != nil {
			return nil, fmt.Errorf("wait for request %s: %w", id, err)
		}
	}
	if answer.State != "granted" || answer.GrantID == nil {
		detail := ""
		if answer.Detail != nil {
			detail = ": " + *answer.Detail
		}
		return nil, fmt.Errorf("request %s for %v is %s%s", answer.RequestID, k.names, answer.State, detail)
	}
	release, err := broker.Values(ctx, *answer.GrantID)
	if err != nil {
		return nil, fmt.Errorf("release grant %s: %w", *answer.GrantID, err)
	}
	grant, err := grantOf(*answer.GrantID, k.names, release)
	if err == nil {
		err = k.write(grant)
	}
	if err != nil {
		if revoked := broker.Revoke(ctx, *answer.GrantID); revoked != nil {
			k.log.Warn("forwarder: could not revoke the grant it refused", "grant", *answer.GrantID, "err", revoked)
		}
		return nil, fmt.Errorf("grant %s: %w", *answer.GrantID, err)
	}
	return grant, nil
}

// write brings the bundle to grant: one gh directory per account holding that account's
// placeholder (ghconfig.Render, with the grant's expiry), the configured renames, and the default
// directory GH_CONFIG_DIR names. A default the grant holds no account for is logged, and the
// bundle's own default is then its first account: GH_CONFIG_DIR's directory holds no token, as a
// role whose App is not installed on its issue's owner holds none today.
func (k *keeper) write(grant *Grant) error {
	if len(grant.Accounts) == 0 {
		return errors.New("the grant holds a placeholder for no GitHub account")
	}
	bundle := ghconfig.Bundle{Default: k.defaultOwner, Dirs: map[string]ghconfig.Rendered{}, Pairs: k.renames}
	for account, placeholder := range grant.Accounts {
		bundle.Dirs[account] = ghconfig.Render(placeholder, "proxy", grant.ExpiresAt)
	}
	if _, held := bundle.Dirs[bundle.Default]; !held {
		accounts := slices.Sorted(maps.Keys(bundle.Dirs))
		if k.defaultOwner != "" {
			k.log.Warn("forwarder: the grant holds no account for GH_CONFIG_DIR's directory", "default", k.defaultOwner, "accounts", accounts)
		}
		bundle.Default = accounts[0]
	}
	_, err := ghconfig.WriteBundle(k.ghDir, bundle)
	return err
}

// keep asks for the first grant, then the next one each time the current one nears its expiry,
// until ctx ends; a failed ask is retried with a backoff from one second to thirty. An ask that
// gets the current grant back — a broker from before it handed an automatic grant back only in
// the first half of its life — is no new grant, and is asked again a minute later, or just past
// that grant's expiry where that comes first, rather than at once.
func (k *keeper) keep(ctx context.Context) {
	wait := time.Second
	for {
		var next time.Duration
		previous := k.Current()
		if previous != nil {
			next = time.Until(previous.ExpiresAt.Add(-k.renewBefore))
		}
		if next > 0 && !sleep(ctx, next) {
			return
		}
		why := "the role's generation started"
		if previous != nil {
			why = "the grant nears its expiry"
		}
		grant, err := k.obtain(ctx, why)
		switch {
		case errors.Is(err, cipher.ErrNotEnrolled):
			// The enrollment comes seconds after the agent registers; it is looked for each
			// second, so the first grant follows it closely, and no backoff grows meanwhile.
			if !sleep(ctx, time.Second) {
				return
			}
		case err != nil:
			if ctx.Err() != nil || !sleep(ctx, wait) {
				return
			}
			wait = min(2*wait, 30*time.Second)
		case previous != nil && grant.ID == previous.ID:
			k.log.Warn("forwarder: the broker handed the expiring grant back rather than a new one", "grant", grant.ID)
			if !sleep(ctx, min(time.Minute, time.Until(grant.ExpiresAt)+time.Second)) {
				return
			}
		default:
			wait = time.Second
		}
	}
}

// sleep waits d, and reports false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// revokeHeld ends every grant the forwarder asked for that has not expired, within ctx: the
// generation's end, when no call of the role's needs them.
func (k *keeper) revokeHeld(ctx context.Context) {
	k.mu.Lock()
	held := maps.Clone(k.held)
	k.held = map[string]time.Time{}
	k.mu.Unlock()
	if len(held) == 0 {
		return
	}
	broker, err := k.open()
	if err != nil {
		k.log.Warn("forwarder: could not revoke its grants at its stop", "grants", slices.Sorted(maps.Keys(held)), "err", err)
		return
	}
	for _, id := range slices.Sorted(maps.Keys(held)) {
		if time.Now().After(held[id]) {
			continue
		}
		if err := broker.Revoke(ctx, id); err != nil {
			k.log.Warn("forwarder: could not revoke a grant at its stop", "grant", id, "err", err)
			continue
		}
		k.log.Info("forwarder: revoked a grant at its stop", "grant", id)
	}
}
