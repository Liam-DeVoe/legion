// Package appauth mints GitHub installation tokens for Legion's one GitHub App: each role's write
// token with that role's set (TokenSet), the read lease, and the daemon's own verdict token.
package appauth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// refreshWindow is how long before a lease's expiry Token stops serving it from the cache and
// mints a fresh one. The daemon rewrites each role's gh token file from the cached lease, so a
// container sees a new token only after that rewrite reaches it, and a Kubernetes Secret volume
// reaches a running pod within about the kubelet's one-minute sync period. Twenty minutes leaves
// a container the new token at least some fifteen minutes before the old one expires, while GitHub
// keeps the old token valid until its own expiry, so a `gh` or `git` that started on the old one
// finishes on it.
const refreshWindow = 20 * time.Minute

// narrowingRefusalWindow is how long a Manager serves a narrowing GitHub already refused
// (ErrNarrowingRefused) from its own memory, asking GitHub nothing (lease). The refusal is
// GitHub's definitive answer about what the account grants the App's installation, and nothing
// the daemon does changes it: only an account approving the grant does, which the next ask past
// this window finds. Without the memory every refresh of every role of every live pod asks the
// refused scope again, each exchange preceded by an uncached grant read — for one refused scope
// of an issue pod, six role bundles times two exchanges a minute — and a rate limit then fails
// every bundle of the project, the write leases included. Five minutes keeps that at one
// exchange per scope per five minutes, while leaving an account's fix to reach the daemon well
// inside the hour an installation token lasts.
const narrowingRefusalWindow = 5 * time.Minute

// ErrNoVerdictToken is VerdictToken's answer in token mode: the daemon holds no key, and no
// profile a token_command names holds checks or statuses at write, so a daemon in token mode
// mints no verdict token and writes no check.
var ErrNoVerdictToken = errors.New("the App is in token mode, which mints no verdict token")

// errVerdictSet is Token's refusal of SetVerdict: the verdict token is the daemon's own, minted by
// VerdictToken alone, so no caller that hands a role a token can hand it that one.
var errVerdictSet = errors.New("the verdict set is minted only as the daemon's own verdict token (VerdictToken)")

// GitIdentity is the bot identity that jj and git must use for work a role performs.
type GitIdentity = runtime.GitIdentity

// Lease is one installation-token lease, including its expiry, when it was minted and the App bot
// identity: the git identity work is done under, and the bot user's id.
type Lease struct {
	Token     string
	ExpiresAt time.Time
	// Minted is when GitHub issued the token: one moments old may not be accepted yet.
	Minted   time.Time
	Identity GitIdentity
	// BotUserID is the id of the App's bot user, as GET /users/<login> answered it: the creator.id
	// of each commit status the bot creates. A rename of the App changes the bot's login and not
	// this id, so the writer of legion-verdict tells the bot's statuses by it. 0 where GitHub
	// answered no whole positive number, which tells no status as the bot's.
	BotUserID int64
}

// TransientError is a GitHub answer that says nothing about the App or its installation, so the
// same request may pass when it is made again: no answer at all (a transport failure, including a
// request or a body read cut off by its deadline), a 5xx, or a rate limit (a 429, or a 403 that
// says it is one). Every other failure is GitHub's definitive answer, or one about the
// configuration, and asking again changes nothing.
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// AnswerError is the failure of a GitHub call that GitHub answered with a status other than 2xx,
// with GitHub's answer beside it (githubrest.AnswerOf): the request's method and its path under the
// Manager's base URL, the status, the body, and the rate-limit headers as GitHub sent them. Its
// message is Err's, and it unwraps to Err alone: the answer is a field and no link of the chain, so
// errors.As finds no *githubrest.Answer in an error of this package, and every caller that reads
// one in the errors of its own GitHub reads (internal/daemon's defaultBranchLimit and
// githubrest.RetryAfter among them) reads a mint's error as it did before. Only a caller that asks
// for an *AnswerError reads the answer: the daemon's start, which waits out GitHub's rate limit on
// its App calls, the App logins read and the key proof (internal/daemon's bootLimit). Every answer
// other than 2xx this package reads carries one, inside the TransientError where one is made.
type AnswerError struct {
	Err    error
	Answer *githubrest.Answer
}

func (e *AnswerError) Error() string { return e.Err.Error() }
func (e *AnswerError) Unwrap() error { return e.Err }

// answered is err, the failure of a call GitHub answered with response, whose body is body,
// carrying that answer (AnswerError), its path the request's under the Manager's base URL
// (githubrest.PathUnder).
func (m *Manager) answered(response *http.Response, body []byte, err error) *AnswerError {
	path := githubrest.PathUnder(m.baseURL, response.Request.URL)
	return &AnswerError{Err: err, Answer: githubrest.AnswerOf(response.Request.Method, path, response, body)}
}

// transient marks a response, whose body is body, as GitHub's trouble rather than an answer: a
// 5xx, or a rate limit (the answer's RateLimited, githubrest.RateLimited, which reads the body for
// a secondary limit GitHub names there alone). Either way err carries GitHub's answer (answered).
func (m *Manager) transient(response *http.Response, body []byte, err error) error {
	refused := m.answered(response, body, err)
	if response.StatusCode >= http.StatusInternalServerError || refused.Answer.RateLimited {
		return &TransientError{Err: refused}
	}
	return refused
}

// refusalBody is the body GitHub sent with a refusal, read whole, and the response closed. One
// that cannot be read is empty: the status and the headers still say what the answer is.
func refusalBody(response *http.Response) []byte {
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	return body
}

// responseBound caps what the daemon reads of every GitHub answer but a token exchange's, which
// is longer and has a bound of its own (exchangeAnswerBound): the installation discovery, the
// grant read, the two identity lookups, token mode's viewer and App reads (viewerLogin,
// checkAppID) and one page of an installation's repositories. GitHub has already acted by the
// time the answer arrives, so the bound is above any answer it makes rather than a limit the
// daemon expects to meet. The longest of them is a page of the listing (listInstallation, 100
// repositories a page), which a read lease now fills since it names no repository: about 530 KB
// at the size exchangeAnswerBound cites, half this bound.
const responseBound = 1 << 20

// exchangeAnswerBound caps what a token exchange reads of GitHub's answer, the longest answer
// the daemon reads: GitHub lists one object for every repository the installation holds,
// whatever the request named, and the request names none at all (accessTokensBody), so the list
// is the installation's whole breadth. Those objects measure 4.9 to 5.3 KB each, which is what
// the secrets broker bounds one of them at 64 KB on
// (packages/envoy/internal/cipher/secrets/githubapp.go, githubRepositoryAnswerBound), and this
// deployment's example-org installation held 216 repositories at 15:45Z on 2026-11-10, about
// 1.1 MB of answer. 16 MiB holds about 3,000 repositories at the measured size, far above any
// installation a Legion deployment mints on, and an answer past it is refused rather than read
// into the daemon's memory.
const exchangeAnswerBound = 16 << 20

// readBody reads a successful response's body whole, up to limit: a read that fails is a
// transport failure, and transient, where what the body then fails to parse as is GitHub's
// answer. A body past limit is no answer GitHub makes, and is refused naming the bound rather
// than parsed, so a body that never ends cannot take the daemon's memory with it; the refusal
// names the bound and never what the body held, which for a token exchange is the token.
func readBody(response *http.Response, what string, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	response.Body.Close()
	if err != nil {
		return nil, &TransientError{Err: fmt.Errorf("%s: read the response: %w", what, err)}
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: the response is longer than the %d bytes an answer holds", what, limit)
	}
	return body, nil
}

// Tokens mints the App's installation tokens on one owner account. Every token reaches its
// owner's whole installation at the permissions asked for, and names no repository at all: the
// owner alone, beside the token's set, keys its lease, its command run and its refusal memory.
type Tokens interface {
	// Token is set's write token for owner: the App's installation token on that account,
	// holding the set's permissions (writePermissions). It reaches every repository the
	// account's installation holds, which the account chooses when it installs the App and the
	// daemon narrows no further. An owner with no name is refused before anything is minted,
	// since there is no account to mint on, and so is SetVerdict, which only VerdictToken mints.
	Token(ctx context.Context, set TokenSet, owner string) (Lease, error)
	// ReadToken is the App's installation token for owner narrowed to reading: readPermissions,
	// which is contents, metadata, pull requests, issues, Actions, checks, commit statuses and
	// attestations, each at read. It is the owner's one read lease, whichever set's caller asks:
	// a controller, which works no repository and holds one for every owner the App is
	// installed on (ControllerCredential), the Kubernetes runtime's provisioning token for the
	// issue's own owner, which every issue pod's workspace-fetch and every repository cache
	// clones with (implementTokens, internal/daemon), and the daemon's own reads of an issue's
	// repository and its pull requests. A workflow role's directories hold that owner's own lease at the role's
	// permissions instead (Token, GitHubCredential). An App on token_command gets what its
	// command's release of the read profile holds instead, which the deployment's profiles file
	// decides and the broker cuts to what the installation holds; the read profile
	// docs/deployment.md gives names readPermissions. With a key of its own the mint asks for what
	// the installation is granted of readPermissions and leaves the rest out (exchangeGranted), so
	// an installation whose account has not granted the App issues, Actions, checks, commit
	// statuses or attestations gets a token without the ones it is not granted rather than
	// nothing. An owner with no name is refused before anything is minted, as Token's is.
	ReadToken(ctx context.Context, owner string) (Lease, error)
	// VerdictToken is the daemon's own token for owner, of SetVerdict: the one token that writes
	// check runs and commit statuses, which no role is handed. It is minted with a key of the
	// App's own alone; in token mode it is ErrNoVerdictToken, minting nothing.
	VerdictToken(ctx context.Context, owner string) (Lease, error)
}

// Options makes a token manager testable. The zero value uses api.github.com, http.DefaultClient,
// the wall clock, the daemon's own stderr for what a token_command writes there, and
// slog.Default for what the manager logs: a grant shortfall, at warn (sayShortfalls).
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
	Stderr     io.Writer
	Log        *slog.Logger
}

// Manager caches installation tokens by set and owner, the owner's read lease by owner alone, and
// the App's installations by owner. An App configured with a token_command is in token mode
// (tokencommand.go): the Manager holds no key and signs no JWT for it, and every lease comes from
// the command's release.
type Manager struct {
	githubApp config.GitHubApp
	baseURL   string
	client    *http.Client
	// noRedirect is client following no redirect, so a 3xx is the answer the caller reads
	// (InstalledUnder).
	noRedirect *http.Client
	now        func() time.Time
	stderr     io.Writer
	log        *slog.Logger

	mu      sync.Mutex
	leases  map[scope]Lease
	pending map[scope]*pendingLease
	// installations holds the App's installation on each owner, by lowercased login, as the last
	// discovery or release of every installation found them; nil until one has.
	installations     map[string]string
	identities        map[string]botUser
	pendingIdentities map[string]*pendingIdentity
	// pendingInstallations is the installation read running now under the App's JWT, so
	// overlapping reads share one (InstalledOwners), nil while none runs. It is dropped the moment
	// the read ends: nothing of the answer is kept, since the answer is a bundle's whole reach and
	// must be GitHub's own at the build (credential.go's GitHubCredential). An App in token mode
	// leaves it nil: its read is the token_command's run, which runTokenCommand shares.
	pendingInstallations *pendingInstallations
	commands             map[scope]*commandRun
	// keys holds each App private key parsed, by its PEM text (generateJWT).
	keys map[string]*rsa.PrivateKey

	// refusals holds, per scope, the narrowing GitHub refused and when it did (lease,
	// narrowingRefusalWindow): while the window holds, that scope is answered from here and
	// GitHub is asked nothing. A mint of the scope that succeeds drops its entry, as does a
	// Forget of its owner.
	refusals map[scope]refusedNarrowingAt

	// saidShortfalls holds every shortfall already logged, each by the owner whose installation
	// is granted less than a set asks for and the shortfall itself (sayShortfalls).
	saidShortfalls map[saidShortfall]bool

	// root ends every token_command run on Close (stop), runs counts the runs still going, and
	// closed, set under mu, refuses a run after Close.
	root   context.Context
	stop   context.CancelFunc
	runs   sync.WaitGroup
	closed bool
}

type pendingLease struct {
	done  chan struct{}
	lease Lease
	err   error
}

// refusedNarrowingAt is one remembered narrowing refusal: GitHub's own refusal, served again to
// every caller of the scope while the window holds, and the Manager's clock reading when it was
// met.
type refusedNarrowingAt struct {
	err error
	at  time.Time
}

type pendingIdentity struct {
	done     chan struct{}
	identity botUser
	err      error
}

// botUser is what the Manager reads once of the App's bot user and keeps by App id: the git
// identity work is done under (Lease.Identity), and the bot user's id (Lease.BotUserID).
type botUser struct {
	identity GitIdentity
	id       int64
}

// onto is lease with the bot user's identity and id set on it.
func (b botUser) onto(lease Lease) Lease {
	lease.Identity, lease.BotUserID = b.identity, b.id
	return lease
}

// pendingInstallations is the App's installation read in flight under its JWT, and what it
// answered, for every caller that joined it rather than reading GitHub beside it
// (InstalledOwners).
type pendingInstallations struct {
	done   chan struct{}
	owners []string
	err    error
}

// A Manager answers every interface a caller reaches it through. The daemon's wiring takes it
// as a Source, which the build holds it to wherever it is handed over. Forgetter is taken by
// type assertion instead (internal/daemon's openWorkflow and leakCheckSource), so a Forget
// renamed without its interface would fail the daemon's boot, or leave the leak check serving
// a token GitHub refused, rather than fail the build.
var (
	_ Source    = (*Manager)(nil)
	_ Forgetter = (*Manager)(nil)
)

// New makes an App token manager. Config.Load resolves command and secret key sources before
// constructing this manager; callers using LoadForValidation must resolve them themselves.
func New(app config.GitHubApp, options Options) *Manager {
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := options.Now
	if now == nil {
		now = time.Now
	}
	stderr := options.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	log := options.Log
	if log == nil {
		log = slog.Default()
	}
	root, stop := context.WithCancel(context.Background())
	return &Manager{
		root:              root,
		stop:              stop,
		githubApp:         app,
		baseURL:           baseURL,
		client:            client,
		noRedirect:        &noRedirect,
		now:               now,
		stderr:            stderr,
		log:               log,
		leases:            map[scope]Lease{},
		pending:           map[scope]*pendingLease{},
		identities:        map[string]botUser{},
		pendingIdentities: map[string]*pendingIdentity{},
		commands:          map[scope]*commandRun{},
		keys:              map[string]*rsa.PrivateKey{},
		refusals:          map[scope]refusedNarrowingAt{},
		saidShortfalls:    map[saidShortfall]bool{},
	}
}

// Token returns the cached write lease of set for owner until fewer than twenty minutes remain
// (refreshWindow), otherwise minting a fresh installation token on owner's installation with the
// set's permissions (writePermissions). Concurrent requests for the same set and owner share that
// one exchange. The lease reaches every repository that installation holds: the account chooses
// those when it installs the App, and the daemon asks for no narrowing of them. An owner with no
// name is no account to mint on: it is refused, and nothing is minted. SetVerdict is refused
// before anything is asked (errVerdictSet), since only VerdictToken mints it, and so is a set no
// role has.
func (m *Manager) Token(ctx context.Context, set TokenSet, owner string) (Lease, error) {
	_, known := writePermissions[set]
	switch {
	case set == SetVerdict:
		return Lease{}, errVerdictSet
	case !known:
		return Lease{}, fmt.Errorf("no role's token is of the set %q", set)
	}
	return m.lease(ctx, mintRequest{set: set, owner: owner})
}

// ReadToken is Token for a token narrowed to readPermissions, the owner's one read lease, cached
// under a scope of its own that no set names, so an owner's write and read tokens never stand in
// for each other. Each permission of the set is asked for at read where the installation is
// granted it, and one it is granted none of is left out (exchangeGranted). In token mode it is
// the command's release of the read profile (TokenCommandReadOnly) instead, which the
// deployment's profiles file decides; the read profile docs/deployment.md gives names this same
// set. A write request of the same owner is a run of its own either way: it asks for no read-only
// release, so the two never stand in for each other. An owner with no name is refused, and
// nothing is minted.
func (m *Manager) ReadToken(ctx context.Context, owner string) (Lease, error) {
	return m.lease(ctx, mintRequest{owner: owner, read: true})
}

// VerdictToken is the write lease of SetVerdict for owner, served and minted as Token serves a
// role's: the daemon's own token for the check runs and commit statuses it writes. A daemon in
// token mode is answered ErrNoVerdictToken and runs no command for it: the command's profiles are
// the roles' and the read lease's, and no profile holds checks or statuses at write.
func (m *Manager) VerdictToken(ctx context.Context, owner string) (Lease, error) {
	if m.githubApp.TokenCommand != "" {
		return Lease{}, ErrNoVerdictToken
	}
	return m.lease(ctx, mintRequest{set: SetVerdict, owner: owner})
}

// Forgetter drops the leases a token source holds, so the next one is minted: a token GitHub
// refused (401) is not served again from the cache. Forget is for a caller that met the refusal
// on a write lease of set, ForgetRead for one that met it on the owner's read lease.
type Forgetter interface {
	Forget(set TokenSet, owner string)
	ForgetRead(owner string)
}

// Forget drops both leases a caller of set may hold for owner, set's write token and the owner's
// read token, and every narrowing refusal remembered for them (refusals): the owner is what a
// refused token is known by, and a caller that met one holds no account of which of the two
// scopes it was, so the next ask of either reaches GitHub. Another set's lease of the owner is
// kept: its caller meets its own refusal, and forgets it then.
func (m *Manager) Forget(set TokenSet, owner string) {
	m.forget(leaseScope(false, set, owner), leaseScope(true, "", owner))
}

// ForgetRead drops the owner's read lease and the narrowing refusal remembered for it, and no
// write lease: a caller that met a refusal on the read lease (the daemon's read of a review
// author's permission) knows which scope it was, so every role's write lease of the owner is kept.
func (m *Manager) ForgetRead(owner string) {
	m.forget(leaseScope(true, "", owner))
}

// forget drops the lease and the narrowing refusal each of scopes holds.
func (m *Manager) forget(scopes ...scope) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, forgotten := range scopes {
		delete(m.leases, forgotten)
		delete(m.refusals, forgotten)
	}
}

// forgetNarrowingRefusal drops the narrowing refusal remembered for owner's read lease (lease,
// narrowingRefusalWindow), and nothing else: no lease, and no other scope's refusal. It is for
// the caller that knows that one memory is stale — the token-mode installation check of a
// repository's preparation, which asks whether the App's installation holds a repository and must
// have GitHub's answer rather than the memory of a refusal met before an operator fixed the grant
// (listInstallation) — where forgetting the lease too would throw away a token that is good and
// make it mint again.
func (m *Manager) forgetNarrowingRefusal(owner string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.refusals, leaseScope(true, "", owner))
}

// scope is one lease's identity, and the key every lease is cached, awaited and warned under:
// which kind of token it is, the set a write token is minted with, and the owner whose
// installation mints it. Those three are the whole of what a lease may do, since a token reaches
// every repository its own installation holds: a read lease must never be served to a caller
// that asked for a write one, no set's token may stand in for another set's, and no owner's
// token may stand in for another owner's. A read lease names no set: it is the owner's one.
type scope struct {
	read  bool
	set   TokenSet
	owner string
}

// leaseScope is the scope of set's lease on owner, or of owner's read lease, whose set it drops.
// The owner is compared trimmed and lowercased, as GitHub matches an account's name, so two
// callers that spell one account differently share the one lease rather than mint twice.
func leaseScope(read bool, set TokenSet, owner string) scope {
	if read {
		set = ""
	}
	return scope{read: read, set: set, owner: strings.ToLower(strings.TrimSpace(owner))}
}

// mintRequest is one ask for a lease, built by Token, ReadToken and VerdictToken and carried
// whole through the mint: the set a write token is minted with (none for the read token), the
// owner whose installation mints it, and whether it is the read token. What GitHub is asked for
// follows from those two (permissions) rather than riding beside them, so no request can name
// one set and ask for another's.
type mintRequest struct {
	set   TokenSet
	owner string
	read  bool
}

// scope is the key the request's lease is cached and awaited under (leaseScope).
func (r mintRequest) scope() scope { return leaseScope(r.read, r.set, r.owner) }

// permissions is what the request asks GitHub for: readPermissions for the read token, else its
// set's (writePermissions). Every mint shares the map, so a caller copies before it changes one
// (narrowToGranted).
func (r mintRequest) permissions() map[string]string {
	if r.read {
		return readPermissions
	}
	return writePermissions[r.set]
}

// token is the request's token as a message names it: the read token, or the set's.
func (r mintRequest) token() string {
	if r.read {
		return "read"
	}
	return string(r.set)
}

// lease serves the cached lease of the request's scope while more than refreshWindow remains,
// and otherwise mints one with the permissions the request names, sharing one exchange among
// concurrent callers. A caller that waited on another's mint, which ended only because that
// caller's own deadline did (a context error, or ErrCommandPending), asks again under its own
// context rather than take that caller's failure as its own.
//
// A narrowing GitHub refused is remembered per scope for narrowingRefusalWindow and served from
// that memory while it holds, GitHub asked nothing: it is GitHub's definitive answer about what
// the account grants the App's installation, where every refresh of every role of every live pod
// would otherwise ask it again. Only ErrNarrowingRefused is remembered — GitHub's trouble
// (TransientError) and every other failure keep asking as they always did — and a mint of the
// scope that succeeds drops what was remembered of it.
//
// The memory answers the ask that would repeat a refusal, never one that already knows better.
// A caller holding what the window cannot know — the token-mode installation check of a
// repository's preparation, which asks whether an installation holds a repository after an
// operator may have changed the grant (listInstallation) — drops that one scope's memory before it
// asks (forgetNarrowingRefusal), so what it is served is GitHub's answer now.
//
// What a cached answer is worth is worth saying, since a caller may read such an ask as what
// its installation does: a lease served from here says a token of that scope was minted within
// the hour, not that GitHub mints one now. An App an account uninstalls leaves every lease of
// that owner cached and good-looking until its expiry nears (refreshWindow), so a bundle built
// in that hour may hold a directory whose token GitHub would no longer mint; what the bundle
// does not do is guess, since the owners it holds a directory for at all are read at every build
// (InstalledOwners, credential.go): from GitHub each time under the App's JWT, and in token mode
// from the command's release of every installation, which may be that of a run that finished,
// with no caller waiting for it, up to finishedRunKeep before the build (runTokenCommand).
//
// A mint that succeeds also drops every lease the cache holds that has expired. The cache is
// keyed by the owner a lease is minted on, so the owners a long-lived daemon works under change
// over its life and leave entries no caller asks for again, and an expired one can never be
// served: a lease is served only while more than refreshWindow remains.
func (m *Manager) lease(ctx context.Context, request mintRequest) (Lease, error) {
	request.owner = strings.TrimSpace(request.owner)
	if request.owner == "" {
		return Lease{}, fmt.Errorf("the %s token names no owner account to mint on", request.token())
	}
	cacheKey := request.scope()
	for {
		now := m.now()
		m.mu.Lock()
		if lease, ok := m.leases[cacheKey]; ok && lease.ExpiresAt.Sub(now) > refreshWindow {
			m.mu.Unlock()
			return lease, nil
		}
		if refused, ok := m.refusals[cacheKey]; ok {
			if now.Sub(refused.at) < narrowingRefusalWindow {
				m.mu.Unlock()
				return Lease{}, refused.err
			}
			delete(m.refusals, cacheKey)
		}
		if waiting, ok := m.pending[cacheKey]; ok {
			m.mu.Unlock()
			select {
			case <-waiting.done:
				if callerEnded(waiting.err) && ctx.Err() == nil {
					continue
				}
				return waiting.lease, waiting.err
			case <-ctx.Done():
				return Lease{}, ctx.Err()
			}
		}
		waiting := &pendingLease{done: make(chan struct{})}
		m.pending[cacheKey] = waiting
		m.mu.Unlock()

		lease, err := m.mint(ctx, request)
		minted := m.now()
		m.mu.Lock()
		switch {
		case err == nil:
			m.leases[cacheKey] = lease
			delete(m.refusals, cacheKey)
			maps.DeleteFunc(m.leases, func(_ scope, held Lease) bool { return !held.ExpiresAt.After(minted) })
		case NarrowingRefused(err):
			m.refusals[cacheKey] = refusedNarrowingAt{err: err, at: minted}
		}
		waiting.lease, waiting.err = lease, err
		delete(m.pending, cacheKey)
		close(waiting.done)
		m.mu.Unlock()
		return lease, err
	}
}

// NarrowingRefused reports GitHub refusing the narrowing a request named, and not GitHub's own
// trouble, which the same request may pass through a moment later (TransientError). An error
// that carries both is the second. No mint builds one today: the exchange returns a rate-limited
// 422 as a TransientError alone (exchange.go), and token mode returns its trouble before it
// builds the refusal (mintFromCommand), so excluding it here is a guard for a later producer. It
// is the one predicate every caller that acts on a refusal reads — a Manager deciding what to
// remember (refusals), a bundle deciding which directories it leaves out (credential.go's
// leaveOutReason), the leak check's token (internal/daemon's leakCheckToken) and the controller
// route's answer (internal/api) — so that none of them reads a rate limit as an answer about what
// an installation grants.
// The daemon's provisioning adapter reads it no longer: it returns every error of its mint as it
// came (internal/daemon's implementTokens).
func NarrowingRefused(err error) bool {
	var trouble *TransientError
	return errors.Is(err, ErrNarrowingRefused) && !errors.As(err, &trouble)
}

// callerEnded reports a failure that says only that the calling context ended: the context's own
// errors, or a token_command still running past it (ErrCommandPending).
func callerEnded(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCommandPending)
}

// mint mints the request's installation token. An installation the daemon found that answers the
// permissions read or the exchange 404 was removed since (errInstallationGone): it is forgotten
// and looked for once more, so a removed App answers ErrNotInstalled and an App installed there
// again mints on its new installation. A configured installation answering 404 is not installed
// either.
func (m *Manager) mint(ctx context.Context, request mintRequest) (Lease, error) {
	app := m.githubApp
	if app.TokenCommand != "" {
		return m.mintFromCommand(ctx, app, request)
	}
	installation, err := m.installation(ctx, app, request.owner)
	if err != nil {
		return Lease{}, err
	}
	jwt, err := m.generateJWT(app.AppID, app.PrivateKey, m.now())
	if err != nil {
		return Lease{}, err
	}
	lease, err := m.exchangeGranted(ctx, jwt, installation, request)
	if errors.Is(err, errInstallationGone) && configuredInstallation(app, request.owner) == "" {
		m.forgetInstallation(request.owner)
		if installation, err = m.installation(ctx, app, request.owner); err != nil {
			return Lease{}, err
		}
		lease, err = m.exchangeGranted(ctx, jwt, installation, request)
	}
	if errors.Is(err, errInstallationGone) {
		m.forgetInstallation(request.owner)
		return Lease{}, fmt.Errorf("%w: the App's installation %s on %s answers %w", ErrNotInstalled, installation, request.owner, err)
	}
	if err != nil {
		return Lease{}, err
	}
	bot, err := m.identity(ctx, app.AppID, func(ctx context.Context) (botUser, error) {
		return m.fetchIdentity(ctx, jwt, lease.Token)
	})
	if err != nil {
		return Lease{}, err
	}
	return bot.onto(lease), nil
}

// installation is app's installation on owner: the one its configuration names, else the one
// the Manager last found, else the one a discovery of every installation finds now. It is the
// owner's alone, whichever set's token is minted on it.
func (m *Manager) installation(ctx context.Context, app config.GitHubApp, owner string) (string, error) {
	ownerKey := strings.ToLower(owner)
	if installation := configuredInstallation(app, owner); installation != "" {
		return installation, nil
	}
	m.mu.Lock()
	if installation := m.installations[ownerKey]; installation != "" {
		m.mu.Unlock()
		return installation, nil
	}
	m.mu.Unlock()

	found, err := m.discoverInstallations(ctx, app)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if m.installations == nil {
		m.installations = map[string]string{}
	}
	for discoveredOwner, installation := range found {
		m.installations[strings.ToLower(discoveredOwner)] = installation
	}
	installation := m.installations[ownerKey]
	m.mu.Unlock()
	if installation == "" {
		return "", fmt.Errorf("%w: the App is not installed on %s", ErrNotInstalled, owner)
	}
	return installation, nil
}

// configuredInstallation is the installation app's configuration names for owner, if any.
func configuredInstallation(app config.GitHubApp, owner string) string {
	for configuredOwner, installation := range app.Installations {
		if strings.EqualFold(configuredOwner, owner) && installation != "" {
			return installation
		}
	}
	return ""
}

// forgetInstallation drops the installation found on owner, for the next mint to look for it
// again.
func (m *Manager) forgetInstallation(owner string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.installations, strings.ToLower(owner))
}

// errInstallationGone is a permissions read or a token exchange GitHub answers 404: the
// installation is no longer there.
var errInstallationGone = errors.New("the installation is gone")

// ErrNarrowingRefused is a token exchange GitHub answers 422, every one of them that is not a
// rate limit: a permission the installation is not granted at the access the request asked for,
// which GitHub refuses whole rather than leaving that one permission out. A mint asks only for
// what the installation was granted a moment before (exchangeGranted), so a refusal here is a
// grant that changed between those two calls. A repository cannot cause it any longer: no
// request names one (exchange), so there is none for GitHub to refuse. In token mode it is the
// secrets broker refusing the narrowing its own release names (brokerNarrowingRefused).
//
// It is exported because its callers read it rather than fail on it: a controller's gh bundle
// that cannot mint its own default owner's lease fails wrapping it, which the API's controller
// route answers as a refusal of its own (internal/api's controllerGitHubCredential), while a
// bundle leaves out every other owner GitHub answers this way, as it does for the few other
// answers that are about the installation (mintBundle, leaveOutReason).
// The Manager remembers it per scope and serves it without asking GitHub again while the window
// holds (lease, narrowingRefusalWindow), which it does for no other refusal. No other refusal
// is read any of these ways. The daemon's provisioning adapter once fell back on it, serving
// the owner's own write token where the reading lease was refused; it does so no longer, and
// returns every error of its mint as it came (implementTokens in internal/daemon).
var ErrNarrowingRefused = errors.New("the installation does not hold the permissions asked for")

// ErrNotInstalled is a mint refused because the App has no installation on the owner: an answer
// about the owner, not GitHub's trouble, which a retry would only repeat.
var ErrNotInstalled = errors.New("github_app_not_installed")

// OwnerInstallations reads the owner accounts an App is installed on. It is what decides the
// directories of a role's gh bundle (credential.go's GitHubCredential and ControllerCredential):
// the installation is the whole of a role's reach, since every token this package mints reaches
// every repository its installation holds and names no repository at all (INFRA-2543), so the
// accounts the App is installed on are the owners a bundle can hold a directory for. It is an
// interface of its own, as Tokens is, so that a test fake answers one of the two and a Source
// is put together from the pair.
type OwnerInstallations interface {
	InstalledOwners(ctx context.Context) ([]string, error)
}

// Source is a daemon's GitHub App token source whole: the leases it mints (Tokens) and the
// owner accounts the App is installed on (OwnerInstallations). A gh bundle is built from both,
// one directory per installed owner holding that owner's lease, so the two credentials and
// every place the daemon hands its token source on take a Source (GitHubCredential,
// ControllerCredential, internal/daemon's runtime factories, internal/api's Options.Tokens).
// The type is what says a token source answers its installations: a value that does not is
// refused by the build, where a type assertion at each of those places would find it only as
// the daemon ran. The Manager is the daemon's one Source.
type Source interface {
	Tokens
	OwnerInstallations
}

// InstalledOwners is the owner accounts the App is installed on, lowercased and sorted, read for
// the call: GET /app/installations under the App's own JWT, every page of it
// (discoverInstallations), or, in token mode, the accounts the command's release of every
// installation names (runTokenCommand with no owner, which records them as the App's
// installations itself). An installation GitHub lists as suspended is none of them: GitHub
// refuses every token of a suspended installation, so it is its own statement that the
// installation serves nothing (discoverInstallations), and the secrets broker leaves one out of
// every release for the same reason (its GitHubApp.Mint), which is why a release names none.
//
// It answers from no installations the Manager has found before, because its caller builds a
// bundle from the answer: an owner the account removed the App from must stop minting at the
// next bundle, and one that installed it must mint at that same bundle, with no cached set
// between the account's change and the role's reach. Under the App's JWT it asks GitHub every
// time. In token mode the one answer that outlives a call is a finished run of the command no
// caller took, which runTokenCommand keeps for finishedRunKeep for a caller that comes back, so a
// call within that time may be answered with that run's release. What it read replaces the
// installation ids the Manager holds, so a mint that follows reaches the
// installation the read names and an owner no longer listed is looked for again rather than
// minted on a stale id.
//
// Reads that overlap in time share one read, as concurrent reads of the App's identity do
// (identity): the answer is a fact about the App and the moment, so the second caller is given
// what the first is reading rather than asking GitHub the same question beside it. Under the
// App's JWT that is the shared read (pendingInstallations), and nothing of it is held once it
// ends, so the next caller reads GitHub afresh. In token mode it is the command's run, which
// runTokenCommand shares among every caller of the scope, and it is what keeps the token-mode
// cost of a bundle bearable: the read is the release of every installation the App has, which
// is a subprocess, a request to the secrets broker and a whole set of tokens thrown away but for
// the owner names.
//
// Under the App's JWT the shared read belongs to no caller (readSharedInstallations): it runs on
// a context detached from the one that started it, bounded by installationReadLimit, and every
// caller, the first among them, waits for it under its own context. Run under the first caller's
// context, an API request that a client cancelled would end the read and hand `context canceled`
// to a launch that joined it, failing a bundle over nothing that happened to the launch or to
// GitHub. A caller whose own context ends before the read does gets the context's error alone:
// GitHub has not answered yet, and no person is waited on.
//
// In token mode each call waits for the command's run under its own context for at most
// installationReadLimit, and runTokenCommand's doc holds every rule of that run.
//
// AppLogin in token mode reads the same scope, so a login read and a call here share one run and
// what is kept of it; anyInstallation says what that means for the boot's key proof.
func (m *Manager) InstalledOwners(ctx context.Context) ([]string, error) {
	if m.githubApp.TokenCommand != "" {
		bounded, cancel := context.WithTimeout(ctx, installationReadLimit)
		defer cancel()
		return m.readInstalledOwners(bounded)
	}
	m.mu.Lock()
	read := m.pendingInstallations
	if read == nil {
		read = &pendingInstallations{done: make(chan struct{})}
		m.pendingInstallations = read
		go m.readSharedInstallations(context.WithoutCancel(ctx), read)
	}
	m.mu.Unlock()
	select {
	case <-read.done:
		return slices.Clone(read.owners), read.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// installationReadLimit bounds the one installation read every overlapping caller shares under
// the App's JWT (InstalledOwners), which no caller's own deadline bounds: longer than the thirty
// seconds a runtime gives a call (sandbox's apiTimeout), so a caller that joins late is still
// answered, and short enough that a read GitHub never answers is dropped and the next caller asks
// afresh rather than joining it for ever. In token mode it bounds each caller's wait for the
// command's run, which goes on past it (runTokenCommand).
const installationReadLimit = time.Minute

// readSharedInstallations makes the read the callers of InstalledOwners share under the App's
// JWT and hands its answer to every one of them through read. detached carries the values of the
// context that started it and none of its cancellation.
func (m *Manager) readSharedInstallations(detached context.Context, read *pendingInstallations) {
	bounded, cancel := context.WithTimeout(detached, installationReadLimit)
	defer cancel()
	owners, err := m.readInstalledOwners(bounded)
	m.mu.Lock()
	read.owners, read.err = owners, err
	m.pendingInstallations = nil
	close(read.done)
	m.mu.Unlock()
}

// readInstalledOwners is the read itself: under the App's JWT made once for every caller whose
// call overlaps it (readSharedInstallations), and in token mode made by each caller under its
// own context, runTokenCommand sharing the command's run among them (InstalledOwners).
func (m *Manager) readInstalledOwners(ctx context.Context) ([]string, error) {
	app := m.githubApp
	var found map[string]string
	if app.TokenCommand != "" {
		released, err := m.runTokenCommand(ctx, app, everyInstallation)
		if err != nil {
			return nil, err
		}
		found = make(map[string]string, len(released))
		for owner := range released {
			found[owner] = owner
		}
	} else {
		discovered, err := m.discoverInstallations(ctx, app)
		if err != nil {
			return nil, err
		}
		found = discovered
	}
	installations := make(map[string]string, len(found))
	owners := make([]string, 0, len(found))
	for owner, installation := range found {
		key := strings.ToLower(owner)
		installations[key] = installation
		owners = append(owners, key)
	}
	m.mu.Lock()
	m.installations = installations
	m.mu.Unlock()
	slices.Sort(owners)
	return owners, nil
}

func (m *Manager) discoverInstallations(ctx context.Context, app config.GitHubApp) (map[string]string, error) {
	jwt, err := m.generateJWT(app.AppID, app.PrivateKey, m.now())
	if err != nil {
		return nil, err
	}
	installations := map[string]string{}
	for page := 1; ; page++ {
		endpoint, err := url.Parse(m.baseURL + "/app/installations")
		if err != nil {
			return nil, fmt.Errorf("GitHub App installation URL: %w", err)
		}
		query := endpoint.Query()
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		endpoint.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		m.appHeaders(request, jwt)
		response, err := m.client.Do(request)
		if err != nil {
			return nil, &TransientError{Err: fmt.Errorf("GitHub App installation discovery: %w", err)}
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, m.transient(response, refusalBody(response), fmt.Errorf("GitHub App installation discovery failed (%d)", response.StatusCode))
		}
		body, err := readBody(response, "GitHub App installation discovery", responseBound)
		if err != nil {
			return nil, err
		}
		var payload []struct {
			ID      json.RawMessage `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
			SuspendedAt *string `json:"suspended_at"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("GitHub App installation discovery returned an invalid response")
		}
		for _, installation := range payload {
			// A suspended installation is GitHub's own statement that it serves nothing: every
			// token asked of it is refused 403. Listed, its owner would be a directory of every
			// role's bundle whose mint can only fail, and an owner nothing can be minted on is
			// what not being installed means.
			if installation.SuspendedAt != nil {
				continue
			}
			id, err := installationID(installation.ID)
			if err != nil || installation.Account.Login == "" {
				return nil, fmt.Errorf("GitHub App installation discovery returned an invalid installation account")
			}
			installations[installation.Account.Login] = id
		}
		if len(payload) < 100 {
			return installations, nil
		}
	}
}

func installationID(raw json.RawMessage) (string, error) {
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil && stringID != "" {
		return stringID, nil
	}
	var numberID int64
	if err := json.Unmarshal(raw, &numberID); err == nil && numberID > 0 {
		return strconv.FormatInt(numberID, 10), nil
	}
	return "", fmt.Errorf("invalid installation id")
}

// identity is the bot user of the App appID, read once by fetch and cached; concurrent callers
// share the one read.
func (m *Manager) identity(ctx context.Context, appID string, fetch func(context.Context) (botUser, error)) (botUser, error) {
	m.mu.Lock()
	if identity, ok := m.identities[appID]; ok {
		m.mu.Unlock()
		return identity, nil
	}
	if waiting, ok := m.pendingIdentities[appID]; ok {
		m.mu.Unlock()
		select {
		case <-waiting.done:
			return waiting.identity, waiting.err
		case <-ctx.Done():
			return botUser{}, ctx.Err()
		}
	}
	waiting := &pendingIdentity{done: make(chan struct{})}
	m.pendingIdentities[appID] = waiting
	m.mu.Unlock()

	identity, err := fetch(ctx)
	m.mu.Lock()
	if err == nil {
		m.identities[appID] = identity
	}
	waiting.identity, waiting.err = identity, err
	delete(m.pendingIdentities, appID)
	close(waiting.done)
	m.mu.Unlock()
	return identity, err
}

func (m *Manager) fetchIdentity(ctx context.Context, jwt, installationToken string) (botUser, error) {
	slug, err := m.appSlug(ctx, jwt)
	if err != nil {
		return botUser{}, err
	}
	return m.botIdentity(ctx, slug+"[bot]", installationToken)
}

// botIdentity is the App bot user of login, read from GET /users/<login> under installationToken:
// its git identity, whose noreply email names the bot user's id, and that id as a number, 0 where
// GitHub answered no whole positive number.
func (m *Manager) botIdentity(ctx context.Context, login, installationToken string) (botUser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+"/users/"+url.PathEscape(login), nil)
	if err != nil {
		return botUser{}, err
	}
	request.Header.Set("Authorization", "token "+installationToken)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "legion-daemon")
	response, err := m.client.Do(request)
	if err != nil {
		return botUser{}, &TransientError{Err: fmt.Errorf("GitHub App bot identity lookup: %w", err)}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return botUser{}, m.transient(response, refusalBody(response), fmt.Errorf("GitHub App bot identity lookup failed (%d)", response.StatusCode))
	}
	body, err := readBody(response, "GitHub App bot identity lookup", responseBound)
	if err != nil {
		return botUser{}, err
	}
	var bot struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &bot); err != nil {
		return botUser{}, fmt.Errorf("GitHub App bot identity lookup returned an invalid bot user id")
	}
	id, err := installationID(bot.ID)
	if err != nil {
		return botUser{}, fmt.Errorf("GitHub App bot identity lookup returned an invalid bot user id")
	}
	userID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || userID <= 0 {
		userID = 0
	}
	return botUser{
		identity: GitIdentity{Name: login, Email: id + "+" + login + "@users.noreply.github.com"},
		id:       userID,
	}, nil
}

// appSlug is the slug of the App jwt signs for, read from GET /app: its bot login is the slug's,
// "<slug>[bot]".
func (m *Manager) appSlug(ctx context.Context, jwt string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+"/app", nil)
	if err != nil {
		return "", err
	}
	m.appHeaders(request, jwt)
	response, err := m.client.Do(request)
	if err != nil {
		return "", &TransientError{Err: fmt.Errorf("GitHub App identity lookup: %w", err)}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", m.transient(response, refusalBody(response), fmt.Errorf("GitHub App identity lookup failed (%d)", response.StatusCode))
	}
	body, err := readBody(response, "GitHub App identity lookup", responseBound)
	if err != nil {
		return "", err
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(body, &app); err != nil || app.Slug == "" {
		return "", fmt.Errorf("GitHub App identity lookup returned an invalid app slug")
	}
	return app.Slug, nil
}

func (m *Manager) appHeaders(request *http.Request, jwt string) {
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "legion-daemon")
}
