package oidc

import (
	"context"
	"crypto"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// testAlgorithm is the algorithm oidctest signs and publishes with, the one a caller asks Key for.
const testAlgorithm = string(jose.RS256)

// keysFor publishes one key as kid and returns the key source for the issuer serving it, with
// nothing fetched yet: NewKeys reads discovery alone, so a test may count the first key-set read.
func keysFor(t *testing.T, issuer *oidctest.Issuer, kid string) (*Keys, *oidctest.Key) {
	t.Helper()
	signing := issuer.PublishKey(t, kid)
	keys, err := NewKeys(context.Background(), issuer.URL())
	if err != nil {
		t.Fatalf("NewKeys(%s): %v", issuer.URL(), err)
	}
	if fetches := issuer.JWKSFetches(); fetches != 0 {
		t.Fatalf("NewKeys read the key set %d times, want 0: the first Key reads it", fetches)
	}
	return keys, signing
}

// signs reports whether raw, a token the test minted, verifies against key: the one way to show
// that Key answered the material the issuer signs with rather than some other published key.
func signs(t *testing.T, raw string, key crypto.PublicKey) bool {
	t.Helper()
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("parse the minted token: %v", err)
	}
	_, err = signed.Verify(key)
	return err == nil
}

func TestKeyAnswersThePublishedKey(t *testing.T) {
	issuer := oidctest.New(t)
	keys, signing := keysFor(t, issuer, "exchange-key")
	token := issuer.Mint(t, signing, issuer.Claims("system:serviceaccount:example-ns:example-worker", "broker"))

	for _, kid := range []string{"exchange-key", ""} {
		key, err := keys.Key(context.Background(), kid, testAlgorithm)
		if err != nil {
			t.Fatalf("Key(%q): %v", kid, err)
		}
		if !signs(t, token, key) {
			t.Errorf("Key(%q) answered a key the issuer's token does not verify against", kid)
		}
	}
	if fetches := issuer.JWKSFetches(); fetches != 1 {
		t.Errorf("key set read %d times for two answers, want 1: the set is cached", fetches)
	}
}

func TestKeyReadsTheSetOnceForAnUnknownKeyIDInTheWindow(t *testing.T) {
	issuer := oidctest.New(t)
	keys, _ := keysFor(t, issuer, "exchange-key")
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(the published key): %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := keys.Key(context.Background(), "never-published", testAlgorithm)
		if !errors.Is(err, ErrNoKey) {
			t.Fatalf("Key(an unpublished key id), attempt %d = %v, want ErrNoKey", attempt, err)
		}
		if !strings.Contains(err.Error(), "never-published") {
			t.Errorf("Key error %q names no key id", err)
		}
		if fetches := issuer.JWKSFetches(); fetches != 2 {
			t.Errorf("key set read %d times after attempt %d, want 2: the first attempt refetches, the second is inside MinRefresh", fetches, attempt)
		}
	}
}

func TestKeyFindsAKeyPublishedAfterTheWindowHasPassed(t *testing.T) {
	const window = 200 * time.Millisecond
	issuer := oidctest.New(t)
	keys, _ := keysFor(t, issuer, "exchange-key")
	keys.MinRefresh = window
	// Generated before the window opens: generating an RSA key takes long enough to pass a short
	// window on its own, and this test is about the window, not about how slow that is.
	rotated := oidctest.NewKey(t, "rotated-in")
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(the published key): %v", err)
	}
	if _, err := keys.Key(context.Background(), "rotated-in", testAlgorithm); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Key(a key id nobody has published yet) = %v, want ErrNoKey", err)
	}

	issuer.Publish(rotated)
	if _, err := keys.Key(context.Background(), "rotated-in", testAlgorithm); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Key(the new key) inside the window = %v, want ErrNoKey: the window holds the read back", err)
	}
	if fetches := issuer.JWKSFetches(); fetches != 2 {
		t.Fatalf("key set read %d times inside the window, want 2", fetches)
	}

	time.Sleep(window)
	key, err := keys.Key(context.Background(), "rotated-in", testAlgorithm)
	if err != nil {
		t.Fatalf("Key(the new key) once the window has passed: %v", err)
	}
	token := issuer.Mint(t, rotated, issuer.Claims("system:serviceaccount:example-ns:example-worker", "broker"))
	if !signs(t, token, key) {
		t.Error("Key answered a key the newly published key's token does not verify against")
	}
}

func TestKeyKeepsServingTheCachedSetWhenAReadFails(t *testing.T) {
	issuer := oidctest.New(t)
	keys, signing := keysFor(t, issuer, "exchange-key")
	// Every Key reads the issuer again, so the cached set is only ever the fallback.
	keys.Lifetime = 0
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(the published key): %v", err)
	}
	issuer.FailKeys(http.StatusInternalServerError)

	key, err := keys.Key(context.Background(), "exchange-key", testAlgorithm)
	if err != nil {
		t.Fatalf("Key(a cached key) with the read failing = %v, want the cached key", err)
	}
	token := issuer.Mint(t, signing, issuer.Claims("system:serviceaccount:example-ns:example-worker", "broker"))
	if !signs(t, token, key) {
		t.Error("Key answered a key the issuer's token does not verify against")
	}

	if _, err := keys.Key(context.Background(), "never-published", testAlgorithm); !errors.Is(err, ErrKeySet) {
		t.Errorf("Key(a key the cached set does not hold) with the read failing = %v, want ErrKeySet", err)
	}
}

// A set past its Lifetime that holds the key answers at once, from the cache, and the issuer is
// read again behind the answer: an issuer that hangs costs a token the cache can judge nothing.
func TestKeyAnswersAStaleSetAtOnceAndReadsItAgainBehind(t *testing.T) {
	issuer := oidctest.New(t)
	keys, _ := keysFor(t, issuer, "exchange-key")
	keys.FetchTimeout = 500 * time.Millisecond
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(the published key): %v", err)
	}
	keys.Lifetime = 0
	issuer.DelayKeys(time.Minute)

	start := time.Now()
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(a key the stale set holds) while the issuer hangs: %v", err)
	}
	if elapsed := time.Since(start); elapsed > keys.FetchWait/2 {
		t.Errorf("Key(a key the stale set holds) took %s while the issuer hangs, want an answer at once", elapsed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for issuer.JWKSFetches() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fetches := issuer.JWKSFetches(); fetches != 2 {
		t.Errorf("key set read %d times, want 2: the stale set is read again behind its answer", fetches)
	}
}

// An issuer that accepts the connection and never answers holds no Key past FetchWait: the caller
// gets ErrKeySet, the issuer unavailable, inside its own window, while the read goes on under
// FetchTimeout. A key rotated in meanwhile gets the same answer for the rest of the MinRefresh
// window, while the read is in flight and after it has failed, and never ErrNoKey, which would
// call a token the issuer may well have signed a forgery.
func TestKeyWaitsAtMostFetchWaitOnAnIssuerThatHangs(t *testing.T) {
	issuer := oidctest.New(t)
	keys, _ := keysFor(t, issuer, "exchange-key")
	keys.FetchWait, keys.FetchTimeout = 200*time.Millisecond, time.Second
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(the published key): %v", err)
	}
	issuer.DelayKeys(time.Minute)
	for call := 1; call <= 3; call++ {
		start := time.Now()
		_, err := keys.Key(context.Background(), "rotated-in", testAlgorithm)
		elapsed := time.Since(start)
		if !errors.Is(err, ErrKeySet) || errors.Is(err, ErrNoKey) {
			t.Errorf("call %d, a key id the set does not hold while the issuer hangs = %v, want ErrKeySet alone", call, err)
		}
		if elapsed > keys.FetchWait+100*time.Millisecond {
			t.Errorf("call %d waited %s, want at most FetchWait, %s", call, elapsed, keys.FetchWait)
		}
		if call == 2 {
			// Past FetchTimeout the read has failed, and its failure answers the third call.
			time.Sleep(keys.FetchTimeout)
		}
	}
	if fetches := issuer.JWKSFetches(); fetches != 2 {
		t.Errorf("key set read %d times, want 2: one read per MinRefresh window", fetches)
	}
}

// A key rotated in while the key set answers 500 reaches no verdict on any call of the MinRefresh
// window, not the first alone: each gets the failed read's ErrKeySet until a read succeeds.
func TestKeyAnswersARotationDuringAnOutageUnavailableUntilAReadSucceeds(t *testing.T) {
	issuer := oidctest.New(t)
	keys, _ := keysFor(t, issuer, "exchange-key")
	keys.MinRefresh = 300 * time.Millisecond
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(the published key): %v", err)
	}
	issuer.FailKeys(http.StatusInternalServerError)
	rotated := oidctest.NewKey(t, "rotated-in")
	issuer.Publish(rotated)
	for call := 1; call <= 3; call++ {
		if _, err := keys.Key(context.Background(), "rotated-in", testAlgorithm); !errors.Is(err, ErrKeySet) || errors.Is(err, ErrNoKey) {
			t.Fatalf("call %d with the key set answering 500 = %v, want ErrKeySet alone", call, err)
		}
	}
	issuer.FailKeys(0)
	time.Sleep(keys.MinRefresh)
	key, err := keys.Key(context.Background(), "rotated-in", testAlgorithm)
	if err != nil {
		t.Fatalf("Key(the rotated key) once the key set reads again: %v", err)
	}
	if !signs(t, issuer.Mint(t, rotated, issuer.Claims("system:serviceaccount:example-ns:example-worker", "broker")), key) {
		t.Error("Key answered a key the rotated key's token does not verify against")
	}
	if _, err := keys.Key(context.Background(), "never-published", testAlgorithm); !errors.Is(err, ErrNoKey) {
		t.Errorf("Key(a key id nobody publishes) after a good read = %v, want ErrNoKey", err)
	}
}

func TestKeyReadsTheSetOnceForConcurrentCallers(t *testing.T) {
	issuer := oidctest.New(t)
	// The read is slow enough that every caller arrives while the first one is still waiting,
	// which is the case this test is about.
	issuer.DelayKeys(100 * time.Millisecond)
	keys, _ := keysFor(t, issuer, "exchange-key")

	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = keys.Key(context.Background(), "exchange-key", testAlgorithm)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: Key: %v", i, err)
		}
	}
	if fetches := issuer.JWKSFetches(); fetches != 1 {
		t.Errorf("key set read %d times for %d concurrent callers, want 1", fetches, callers)
	}
}

func TestNewKeysRefusesAnIssuerThatDoesNotDiscover(t *testing.T) {
	issuer := oidctest.New(t)
	issuer.FailDiscovery(http.StatusInternalServerError)
	keys, err := NewKeys(context.Background(), issuer.URL())
	if err == nil {
		t.Fatalf("NewKeys(an issuer whose discovery fails) = %v, want an error", keys)
	}
	if !strings.Contains(err.Error(), issuer.URL()) {
		t.Errorf("NewKeys error %q names no issuer", err)
	}
}

func TestSelectKeyChoosesTheOneUsableKey(t *testing.T) {
	signing := jose.JSONWebKey{Key: "signing", KeyID: "exchange-key", Algorithm: testAlgorithm, Use: "sig"}
	encryption := jose.JSONWebKey{Key: "encryption", KeyID: "exchange-key", Algorithm: testAlgorithm, Use: "enc"}
	elliptic := jose.JSONWebKey{Key: "elliptic", KeyID: "elliptic-key", Algorithm: string(jose.ES256), Use: "sig"}
	unmarked := jose.JSONWebKey{Key: "unmarked", KeyID: "unmarked-key"}
	second := jose.JSONWebKey{Key: "second", KeyID: "second-key", Algorithm: testAlgorithm, Use: "sig"}

	for _, c := range []struct {
		name string
		set  []jose.JSONWebKey
		kid  string
		alg  string
		want any
		err  error
	}{
		{name: "the key the token names", set: []jose.JSONWebKey{signing, second}, kid: "exchange-key", alg: testAlgorithm, want: "signing"},
		{name: "an encryption key under the same id is passed over", set: []jose.JSONWebKey{encryption, signing}, kid: "exchange-key", alg: testAlgorithm, want: "signing"},
		{name: "a key marked for no use is usable", set: []jose.JSONWebKey{unmarked}, kid: "unmarked-key", alg: testAlgorithm, want: "unmarked"},
		{name: "no key id, one usable key", set: []jose.JSONWebKey{signing, elliptic}, alg: testAlgorithm, want: "signing"},
		{name: "a key for another algorithm", set: []jose.JSONWebKey{elliptic}, kid: "elliptic-key", alg: testAlgorithm, err: ErrNoKey},
		{name: "a key id the set does not hold", set: []jose.JSONWebKey{signing}, kid: "never-published", alg: testAlgorithm, err: ErrNoKey},
		{name: "an empty set", kid: "exchange-key", alg: testAlgorithm, err: ErrNoKey},
		{name: "no key id, no usable key", set: []jose.JSONWebKey{elliptic}, alg: testAlgorithm, err: ErrNoKey},
		{name: "no key id, two usable keys", set: []jose.JSONWebKey{signing, second}, alg: testAlgorithm, err: ErrAmbiguousKey},
		{name: "one key id, two keys under it", set: []jose.JSONWebKey{signing, {Key: "twin", KeyID: "exchange-key", Use: "sig"}}, kid: "exchange-key", alg: testAlgorithm, err: ErrAmbiguousKey},
		{name: "no algorithm asked for narrows nothing", set: []jose.JSONWebKey{elliptic}, kid: "elliptic-key", want: "elliptic"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := selectKey(c.set, c.kid, c.alg)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("selectKey(%q, %q) = %v, %v; want %v", c.kid, c.alg, got, err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("selectKey(%q, %q) = %v, %v; want %v", c.kid, c.alg, got, err, c.want)
			}
		})
	}
}
