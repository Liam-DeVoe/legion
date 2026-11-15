package oidc

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// twoIssuers starts two issuers, each publishing one key, and the set naming them first and
// second, with no key-set spacing.
func twoIssuers(t *testing.T) (set *IssuerSet, first, second *oidctest.Issuer, firstKey, secondKey *oidctest.Key) {
	t.Helper()
	first, second = oidctest.New(t), oidctest.New(t)
	firstKey, secondKey = first.PublishKey(t, "signing-key"), second.PublishKey(t, "signing-key")
	set = NewIssuerSet([]NamedIssuer{{Name: "first", URL: first.URL()}, {Name: "second", URL: second.URL()}}, testAudience, 0)
	return set, first, second, firstKey, secondKey
}

// reads is how often issuer's discovery document and key set have been read.
func reads(issuer *oidctest.Issuer) [2]int {
	return [2]int{issuer.DiscoveryRequests(), issuer.JWKSFetches()}
}

// An issuer set checks each token against the issuer its iss names and answers that issuer, and
// a token of one issuer reads nothing of the other: neither its discovery document nor its keys.
func TestAnIssuerSetChecksATokenByTheIssuerItNames(t *testing.T) {
	set, first, second, firstKey, secondKey := twoIssuers(t)
	ctx := context.Background()

	claims, issuer, err := set.Verify(ctx, first.Mint(t, firstKey, first.Claims(testSubject, testAudience)))
	if err != nil || claims.Subject != testSubject || issuer != (NamedIssuer{Name: "first", URL: first.URL()}) {
		t.Fatalf("Verify(the first issuer's token) = %+v, %+v, %v; want %s from the first issuer", claims, issuer, err, testSubject)
	}
	if got := reads(second); got != [2]int{} {
		t.Fatalf("the first issuer's token read the second issuer's discovery document and key set %v times, want none", got)
	}

	claims, issuer, err = set.Verify(ctx, second.Mint(t, secondKey, second.Claims(testSubject, testAudience)))
	if err != nil || claims.Subject != testSubject || issuer != (NamedIssuer{Name: "second", URL: second.URL()}) {
		t.Fatalf("Verify(the second issuer's token) = %+v, %+v, %v; want %s from the second issuer", claims, issuer, err, testSubject)
	}

	// The second issuer's key under the first issuer's iss: the token goes to the first issuer,
	// whose keys do not verify it, and the second issuer's keys are not read for it.
	before := reads(second)
	_, issuer, err = set.Verify(ctx, second.Mint(t, secondKey, first.Claims(testSubject, testAudience)))
	if !errors.Is(err, ErrSignature) || issuer.Name != "first" {
		t.Fatalf("Verify(a token naming the first issuer, signed by the second) = %+v, %v; want ErrSignature from the first issuer", issuer, err)
	}
	if got := reads(second); got != before {
		t.Fatalf("a token naming the first issuer read the second issuer: reads went from %v to %v", before, got)
	}

	if _, _, err := set.Verify(ctx, first.Mint(t, firstKey, first.Claims(testSubject, "another-audience"))); !errors.Is(err, ErrAudience) {
		t.Fatalf("Verify(a token for another audience) = %v, want ErrAudience", err)
	}
}

// A token whose iss no issuer of the set names is ErrIssuer, the error of a token of another
// issuer, and reads nothing: no issuer of the set is asked anything for it.
func TestAnIssuerSetRefusesATokenOfAnIssuerItDoesNotName(t *testing.T) {
	set, first, second, _, _ := twoIssuers(t)
	third := oidctest.New(t)
	thirdKey := third.PublishKey(t, "signing-key")

	_, issuer, err := set.Verify(context.Background(), third.Mint(t, thirdKey, third.Claims(testSubject, testAudience)))
	if !errors.Is(err, ErrIssuer) || issuer != (NamedIssuer{}) {
		t.Fatalf("Verify(a token of an issuer the set does not name) = %+v, %v; want ErrIssuer and no issuer", issuer, err)
	}
	for name, issuer := range map[string]*oidctest.Issuer{"first": first, "second": second, "third": third} {
		if got := reads(issuer); got != [2]int{} {
			t.Errorf("the %s issuer's discovery document and key set were read %v times for that token, want none", name, got)
		}
	}
}

// A token the set cannot decode is ErrMalformed, as Verifier.Verify classifies it, and reads
// nothing.
func TestAnIssuerSetRefusesAMalformedToken(t *testing.T) {
	set, first, second, _, _ := twoIssuers(t)
	for name, raw := range map[string]string{
		"not a JWT":               "not-a-token",
		"a payload not base64url": "e30.!!!.c2ln",
		"a payload not JSON":      "e30.bm90IGpzb24.c2ln",
	} {
		t.Run(name, func(t *testing.T) {
			if _, issuer, err := set.Verify(context.Background(), raw); !errors.Is(err, ErrMalformed) || issuer != (NamedIssuer{}) {
				t.Fatalf("Verify(%q) = %+v, %v; want ErrMalformed and no issuer", raw, issuer, err)
			}
		})
	}
	if got := [2][2]int{reads(first), reads(second)}; got != [2][2]int{} {
		t.Fatalf("malformed tokens read the issuers %v times, want none", got)
	}
}

// While an issuer of the set has not been read, its tokens are ErrIssuerUnavailable, naming that
// issuer, and the other issuer's tokens still verify.
func TestAnIssuerSetAnswersAnIssuerItCannotReadUnavailable(t *testing.T) {
	set, first, second, firstKey, secondKey := twoIssuers(t)
	first.FailDiscovery(http.StatusServiceUnavailable)
	ctx := context.Background()

	_, issuer, err := set.Verify(ctx, first.Mint(t, firstKey, first.Claims(testSubject, testAudience)))
	if !errors.Is(err, ErrIssuerUnavailable) || issuer.Name != "first" {
		t.Fatalf("Verify(a token of an issuer that cannot be read) = %+v, %v; want ErrIssuerUnavailable from the first issuer", issuer, err)
	}
	if _, issuer, err := set.Verify(ctx, second.Mint(t, secondKey, second.Claims(testSubject, testAudience))); err != nil || issuer.Name != "second" {
		t.Fatalf("Verify(the other issuer's token) = %+v, %v; want it verified", issuer, err)
	}
}

// Boot tries every issuer once and answers which it read, in the set's order; Run then reads the
// one that failed once it answers and reports it once, and never reports one Boot read.
func TestAnIssuerSetBootsAndReadsTheRestInTheBackground(t *testing.T) {
	set, first, second, _, _ := twoIssuers(t)
	first.FailDiscovery(http.StatusServiceUnavailable)
	// A failed read holds the next one back for discoveryRetrySpacing; on a clock of its own, the
	// first issuer can be moved past it so Run reads at once rather than after its wait.
	clock := newTestClock()
	set.entries[0].verifier.discovery.now = clock.Now
	ctx := context.Background()

	want := []IssuerRead{{Issuer: NamedIssuer{Name: "first", URL: first.URL()}}, {Issuer: NamedIssuer{Name: "second", URL: second.URL()}, Read: true}}
	if got := set.Boot(ctx); !slices.Equal(got, want) {
		t.Fatalf("Boot = %+v, want %+v", got, want)
	}

	first.FailDiscovery(0)
	clock.advance(discoveryRetrySpacing)

	var mu sync.Mutex
	var reported []NamedIssuer
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	set.Run(runCtx, func(issuer NamedIssuer) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, issuer)
	})
	if runCtx.Err() != nil {
		t.Fatal("Run did not return once the first issuer was read")
	}
	if want := []NamedIssuer{{Name: "first", URL: first.URL()}}; !slices.Equal(reported, want) {
		t.Fatalf("Run reported %+v, want the first issuer alone, once", reported)
	}
}
