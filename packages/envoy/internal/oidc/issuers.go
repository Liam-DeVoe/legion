package oidc

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// NamedIssuer is one issuer of an IssuerSet: the name a deployment's configuration gives it, empty
// for the one issuer of a set written as a bare URL, and the URL a token of it names as its iss.
type NamedIssuer struct {
	Name, URL string
}

// IssuerSet verifies tokens of several issuers for one audience, such as the projected
// ServiceAccount tokens of several Kubernetes clusters, each its own issuer. It reads a token's iss
// without trusting it and gives the token to that issuer's verifier alone, so a token of one
// issuer never makes another's verifier read its keys. Each issuer is a LazyVerifier, read when
// first needed or in the background, so an issuer that cannot be read stops no other.
type IssuerSet struct {
	entries []issuerEntry
}

// issuerEntry is one issuer of a set and its verifier.
type issuerEntry struct {
	issuer   NamedIssuer
	verifier *LazyVerifier
}

// NewIssuerSet returns a set verifying tokens each of issuers issues to audience, and reads
// nothing (NewLazyVerifier). keySpacing is the FailureSpacing of each issuer's key set
// (WithKeyFailureSpacing); zero spaces nothing. The issuers' URLs are distinct, as the
// configuration that names them refuses a repeated one; a token naming a URL twice listed goes to
// the first.
func NewIssuerSet(issuers []NamedIssuer, audience string, keySpacing time.Duration) *IssuerSet {
	set := &IssuerSet{entries: make([]issuerEntry, 0, len(issuers))}
	for _, issuer := range issuers {
		set.entries = append(set.entries, issuerEntry{
			issuer:   issuer,
			verifier: NewLazyVerifier(issuer.URL, audience, WithKeyFailureSpacing(keySpacing)),
		})
	}
	return set
}

// Verify checks raw against the issuer its iss names, and answers the claims and that issuer. A
// token that cannot be decoded is ErrMalformed, and one whose iss no issuer of the set names is
// ErrIssuer, as Verifier.Verify classifies each; neither reads anything. Every other answer is
// that issuer's LazyVerifier's, ErrIssuerUnavailable while its discovery document has not been
// read or its key set cannot be, and it carries the issuer even when it is an error, so a caller
// can say which issuer could not be read.
func (s *IssuerSet) Verify(ctx context.Context, raw string) (Claims, NamedIssuer, error) {
	claims, err := decodeClaims(raw)
	if err != nil {
		return Claims{}, NamedIssuer{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	for _, entry := range s.entries {
		if entry.issuer.URL == claims.Issuer {
			verified, err := entry.verifier.Verify(ctx, raw)
			return verified, entry.issuer, err
		}
	}
	return Claims{}, NamedIssuer{}, fmt.Errorf("%w: %q, which no issuer of the set names", ErrIssuer, claims.Issuer)
}

// IssuerRead is one issuer of a set and whether its discovery document has been read.
type IssuerRead struct {
	Issuer NamedIssuer
	Read   bool
}

// Boot tries every issuer's discovery at once, each try bounded by discoveryAttemptTimeout, and
// answers, in the set's order, each issuer and whether it was read. A failed try is no error of
// Boot's: Run reads that issuer in the background, and until then its tokens are
// ErrIssuerUnavailable.
func (s *IssuerSet) Boot(ctx context.Context) []IssuerRead {
	reads := make([]IssuerRead, len(s.entries))
	var wg sync.WaitGroup
	for i, entry := range s.entries {
		reads[i].Issuer = entry.issuer
		wg.Go(func() {
			attempt, cancel := context.WithTimeout(ctx, discoveryAttemptTimeout)
			defer cancel()
			reads[i].Read = entry.verifier.Discover(attempt) == nil
		})
	}
	wg.Wait()
	return reads
}

// Run reads in the background each issuer not read when it is called (LazyVerifier.Run), and
// calls read once for each of them when its read succeeds, from that issuer's own goroutine. It
// returns once every one of them is read or ctx ends.
func (s *IssuerSet) Run(ctx context.Context, read func(NamedIssuer)) {
	var wg sync.WaitGroup
	for _, entry := range s.entries {
		if entry.verifier.Discovered() {
			continue
		}
		wg.Go(func() {
			entry.verifier.Run(ctx)
			if entry.verifier.Discovered() {
				read(entry.issuer)
			}
		})
	}
	wg.Wait()
}
