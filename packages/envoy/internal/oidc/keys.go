package oidc

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

// The class of every Key failure. A key the issuer does not publish and an issuer that cannot be
// read are different faults — the first is a token signed by something the issuer never had, the
// second is an outage — and only the second is worth an alarm, so a caller tells them apart by
// sentinel rather than by error text.
var (
	ErrNoKey        = errors.New("oidc: issuer publishes no such signing key")
	ErrAmbiguousKey = errors.New("oidc: issuer publishes several keys and the token names none")
	ErrKeySet       = errors.New("oidc: key set could not be read")
)

// The timings a Keys starts with. A caller that wants others sets the fields before its first Key.
const (
	// DefaultKeysLifetime is how long a fetched set answers before the next Key reads the issuer
	// again. It is the far side of the trade a cache makes: a key the issuer has withdrawn still
	// verifies a signature for this long, and in exchange every verification in the window costs
	// no network read.
	DefaultKeysLifetime = 15 * time.Minute
	// DefaultKeysMinRefresh is the shortest interval between two fetches made because a token
	// named a key id the cached set does not hold. It is what keeps a rotation visible within a
	// minute while a token naming a key nobody ever published, replayed in a loop, costs the
	// issuer one read per minute and no more.
	DefaultKeysMinRefresh = time.Minute
	// DefaultKeysFetchTimeout bounds one key-set read. An issuer that accepts the connection and
	// never answers would otherwise hold a read open for ever.
	DefaultKeysFetchTimeout = 10 * time.Second
	// DefaultKeysFetchWait is the longest a Key waits on a key-set read before it answers that the
	// set could not be read (ErrKeySet); the read goes on, under FetchTimeout, for the next Key. It
	// is what keeps an issuer that hangs, rather than fails, from holding a verification past its
	// caller's own window: the Envoy listener answers each of Dispatch's calls within the 5 s
	// Dispatch's client gives it (internal/dispatch/envoy), and a key-set outage there is a 503
	// Dispatch retries, which an answer that came too late would not be.
	DefaultKeysFetchWait = 3 * time.Second
)

// maxKeySetBytes is the most of an issuer's key-set response that is read. A real set is a few
// kilobytes; the limit is what stops a misbehaving or hostile endpoint from being decoded into
// the broker's memory in full.
const maxKeySetBytes = 1 << 20

// Keys is an issuer's published signing keys as a key source: it answers the public key a token's
// `kid` and `alg` name, where a Verifier only answers whether a whole token verifies. The RFC 8693
// token exchange needs the key itself, because the OAuth provider validates a subject token
// through a jwt.Keyfunc — a function handed the parsed token that returns the key the library then
// checks the signature with — and go-oidc's RemoteKeySet verifies signatures without ever handing
// its keys out. So the exchange reads the issuer's set through this, beside the Verifier rather
// than through it.
//
// A Keys holds the set in memory and is safe for concurrent use: callers that find it stale or
// missing share one fetch rather than each making their own. The timing fields are read without
// the lock, so a caller changes them before its first Key and not after.
type Keys struct {
	// Lifetime is how long a fetched set is answered from before a Key reads the issuer again
	// (DefaultKeysLifetime).
	Lifetime time.Duration
	// MinRefresh is the shortest interval between two fetches prompted by a key id the cached set
	// does not hold (DefaultKeysMinRefresh).
	MinRefresh time.Duration
	// FetchTimeout bounds one key-set read, which no caller's context ends
	// (DefaultKeysFetchTimeout).
	FetchTimeout time.Duration
	// FetchWait is the longest a Key waits on a read (DefaultKeysFetchWait).
	FetchWait time.Duration
	// HTTPClient reads the key set. A caller replaces it to route the read through a proxy or an
	// instrumented transport; it is never nil.
	HTTPClient *http.Client

	jwksURI string

	mu       sync.Mutex
	keys     []jose.JSONWebKey
	fetched  time.Time
	missed   time.Time
	inflight *keysFetch
	// failure is why the last read reached no set, or why the last wait on one gave up while it
	// was still in flight; nil once a read succeeds.
	failure error
}

// keysFetch is one key-set read in progress, which every caller that arrives while it runs waits
// on instead of starting its own.
type keysFetch struct {
	done chan struct{}
	keys []jose.JSONWebKey
	err  error
}

// NewKeys reads issuer's OIDC discovery document now and returns the key source for the keys it
// names, so a caller configures an issuer and nothing else. It refuses, naming the issuer, one
// whose discovery document cannot be read or names no jwks_uri. No key is fetched here: the first
// Key reads the set, and ctx bounds discovery alone.
func NewKeys(ctx context.Context, issuer string) (*Keys, error) {
	provider, err := gooidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovering issuer %s: %w", issuer, err)
	}
	var discovery struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := provider.Claims(&discovery); err != nil {
		return nil, fmt.Errorf("oidc: reading issuer %s discovery document: %w", issuer, err)
	}
	if discovery.JWKSURI == "" {
		return nil, fmt.Errorf("oidc: issuer %s names no jwks_uri in its discovery document", issuer)
	}
	return &Keys{
		Lifetime:     DefaultKeysLifetime,
		MinRefresh:   DefaultKeysMinRefresh,
		FetchTimeout: DefaultKeysFetchTimeout,
		FetchWait:    DefaultKeysFetchWait,
		HTTPClient:   &http.Client{},
		jwksURI:      discovery.JWKSURI,
	}, nil
}

// Key answers the public key a token naming kid and signed with alg is to be checked against. An
// empty kid is answered only where one key of the set is usable for alg, since choosing among
// several would settle a signature's fate by position in a JSON array; an empty alg asks for no
// particular algorithm and narrows nothing.
//
// A set older than Lifetime that holds the key answers at once and is read again behind the
// answer; one that does not hold it is read again first. A kid the set does not hold is a rotation
// the cache has not seen yet, so it prompts one more read, at most once per MinRefresh. A read
// that fails leaves the last good set serving and is returned only when that set does not hold
// the key asked for — an issuer that is down, or hangs, stops no verification it does not have
// to. While the MinRefresh window is closed, a kid the set does not hold waits on a read still in
// flight, and after a read that failed gets that read's error, not ErrNoKey, until a read
// succeeds: the issuer has not said it publishes no such key, so no verdict was reached.
//
// Every error wraps ErrNoKey, ErrAmbiguousKey or ErrKeySet. The text names the key id asked for,
// which is a public identifier, shortened so an oversized one cannot flood a log, and no other
// part of the token.
func (k *Keys) Key(ctx context.Context, kid, alg string) (crypto.PublicKey, error) {
	set, fetched, inflight, failure := k.state()
	stale := fetched.IsZero() || time.Since(fetched) >= k.Lifetime
	if stale && !fetched.IsZero() {
		if key, err := selectKey(set, kid, alg); err == nil {
			k.begin(ctx)
			return key, nil
		}
	}
	var fetchErr error
	if stale {
		fresh, err := k.fetch(ctx)
		switch {
		case err == nil:
			set = fresh
		case fetched.IsZero():
			return nil, err
		default:
			fetchErr = err
		}
	}
	key, err := selectKey(set, kid, alg)
	if err == nil {
		return key, nil
	}
	if stale || !errors.Is(err, ErrNoKey) {
		if fetchErr != nil {
			return nil, fetchErr
		}
		return nil, err
	}
	// The miss reads the set again when its window opens; inside the window it waits on the read in
	// flight, or answers the last read's failure, and only after a read that succeeded ErrNoKey.
	var read *keysFetch
	switch {
	case k.openMissWindow():
		read = k.begin(ctx)
	case inflight != nil:
		read = inflight
	case failure != nil:
		return nil, failure
	default:
		return nil, err
	}
	fresh, err := k.await(ctx, read)
	if err != nil {
		return nil, err
	}
	return selectKey(fresh, kid, alg)
}

// state is the cached set, when it was fetched, the zero time standing for a set never read, the
// read in flight, if any, and the last read's failure.
func (k *Keys) state() ([]jose.JSONWebKey, time.Time, *keysFetch, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys, k.fetched, k.inflight, k.failure
}

// openMissWindow reports whether a key id the cached set does not hold may prompt a read now, and
// opens the next MinRefresh window when it does. The window opens on the attempt rather than on
// the answer, so an issuer that is failing is asked no more often than one that is rotating.
func (k *Keys) openMissWindow() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if !k.missed.IsZero() && now.Sub(k.missed) < k.MinRefresh {
		return false
	}
	k.missed = now
	return true
}

// fetch starts a key-set read, or joins the one already in flight (begin), and waits on it
// (await). One read serves every caller waiting on it, so a key set that is slow to answer costs
// the issuer one request however many tokens arrive meanwhile. A read that fails keeps the last
// good set.
func (k *Keys) fetch(ctx context.Context) ([]jose.JSONWebKey, error) {
	return k.await(ctx, k.begin(ctx))
}

// await waits at most FetchWait for inflight. A caller whose own context ends first, or whose
// wait runs out, leaves with that error and the read carries on for the others; a wait that runs
// out is kept as the read's failure until the read ends.
func (k *Keys) await(ctx context.Context, inflight *keysFetch) ([]jose.JSONWebKey, error) {
	wait := time.NewTimer(k.FetchWait)
	defer wait.Stop()
	select {
	case <-inflight.done:
		return inflight.keys, inflight.err
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: waiting on %s: %w", ErrKeySet, k.jwksURI, ctx.Err())
	case <-wait.C:
		err := fmt.Errorf("%w: %s has not answered in %s", ErrKeySet, k.jwksURI, k.FetchWait)
		k.mu.Lock()
		if k.inflight == inflight {
			k.failure = err
		}
		k.mu.Unlock()
		return nil, err
	}
}

// begin returns the key-set read in flight, starting one when none is. The read runs on ctx's
// values but not its cancellation, under FetchTimeout alone, so no caller that leaves ends it for
// the others, and its result is kept when it ends: the set and no failure, or the failure.
func (k *Keys) begin(ctx context.Context) *keysFetch {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.inflight != nil {
		return k.inflight
	}
	inflight := &keysFetch{done: make(chan struct{})}
	k.inflight = inflight
	go func() {
		inflight.keys, inflight.err = k.read(context.WithoutCancel(ctx))
		k.mu.Lock()
		k.inflight = nil
		if inflight.err == nil {
			k.keys, k.fetched, k.failure = inflight.keys, time.Now(), nil
		} else {
			k.failure = inflight.err
		}
		k.mu.Unlock()
		close(inflight.done)
	}()
	return inflight
}

// read fetches and parses the issuer's key set once, under FetchTimeout.
func (k *Keys) read(ctx context.Context) ([]jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(ctx, k.FetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.jwksURI, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: requesting %s: %w", ErrKeySet, k.jwksURI, err)
	}
	resp, err := k.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrKeySet, k.jwksURI, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s answered %s", ErrKeySet, k.jwksURI, resp.Status)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKeySetBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("%w: parsing %s: %w", ErrKeySet, k.jwksURI, err)
	}
	return set.Keys, nil
}

// selectKey answers the one key of set a token naming kid and signed with alg may be checked
// against. A key the set marks for encryption, or for another algorithm, is not that key and is
// passed over rather than refused, so a set that carries an encryption key beside the signing one
// still resolves.
func selectKey(set []jose.JSONWebKey, kid, alg string) (crypto.PublicKey, error) {
	var match jose.JSONWebKey
	usable := 0
	for _, key := range set {
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		if alg != "" && key.Algorithm != "" && key.Algorithm != alg {
			continue
		}
		if kid != "" && key.KeyID != kid {
			continue
		}
		if usable == 0 {
			match = key
		}
		usable++
	}
	switch {
	case usable == 1:
		return match.Key, nil
	case usable == 0 && kid == "":
		return nil, fmt.Errorf("%w: the token names no key id and the issuer publishes none for %s", ErrNoKey, algorithmName(alg))
	case usable == 0:
		return nil, fmt.Errorf("%w: key id %s for %s", ErrNoKey, keyIDName(kid), algorithmName(alg))
	case kid == "":
		return nil, fmt.Errorf("%w: the token names no key id and the issuer publishes %d for %s", ErrAmbiguousKey, usable, algorithmName(alg))
	default:
		return nil, fmt.Errorf("%w: the issuer publishes %d keys under key id %s", ErrAmbiguousKey, usable, keyIDName(kid))
	}
}

// algorithmName names alg in an error, an empty one being the ask that narrows nothing.
func algorithmName(alg string) string {
	if alg == "" {
		return "any algorithm"
	}
	return alg
}

// maxKeyIDInError is how much of a key id an error repeats. A key id comes from a header no one
// has verified yet, so its length is whatever the caller sent.
const maxKeyIDInError = 64

// keyIDName quotes kid for an error, shortened to maxKeyIDInError bytes.
func keyIDName(kid string) string {
	if len(kid) > maxKeyIDInError {
		return fmt.Sprintf("%q (%d bytes, shortened)", kid[:maxKeyIDInError], len(kid))
	}
	return fmt.Sprintf("%q", kid)
}
