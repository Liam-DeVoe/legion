package oidc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// Review of PR #4202 round 3, proposed guards. A set past its Lifetime that holds the key answers
// with no wait at all, even where FetchTimeout is shorter than FetchWait, which is the case the
// existing test cannot tell from a wait (its bound, FetchWait/2, is longer than its FetchTimeout).
func TestReviewStaleSetAnswersWithoutWaiting(t *testing.T) {
	issuer := oidctest.New(t)
	keys, _ := keysFor(t, issuer, "exchange-key")
	keys.FetchTimeout = time.Second
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(the published key): %v", err)
	}
	keys.Lifetime = 0
	issuer.DelayKeys(time.Minute)
	start := time.Now()
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key(a key the stale set holds) while the issuer hangs: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Key(a key the stale set holds) took %s while the issuer hangs, want no wait", elapsed)
	}
}

// A read outlives the caller that started it: the caller's context ends during its wait, the
// issuer answers after that, and the next caller is served from the set that read filled, with
// one key-set request in all. On a real server the request's context ends when its handler
// returns, so a read tied to it would never fill the cache from an issuer slower than FetchWait.
func TestReviewReadOutlivesTheCallerThatStartedIt(t *testing.T) {
	issuer := oidctest.New(t)
	keys, _ := keysFor(t, issuer, "exchange-key")
	keys.FetchWait = 100 * time.Millisecond
	issuer.DelayKeys(400 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := keys.Key(ctx, "exchange-key", testAlgorithm)
	cancel()
	if !errors.Is(err, ErrKeySet) {
		t.Fatalf("Key on a context that ends during its wait = %v, want ErrKeySet", err)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := keys.Key(context.Background(), "exchange-key", testAlgorithm); err != nil {
		t.Fatalf("Key once the first caller's read has landed = %v, want the key from that read", err)
	}
	if fetches := issuer.JWKSFetches(); fetches != 1 {
		t.Errorf("key set read %d times, want 1: the first caller's read outlives it", fetches)
	}
}
