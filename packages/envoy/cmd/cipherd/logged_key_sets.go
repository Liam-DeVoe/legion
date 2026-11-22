package main

import (
	"context"
	"crypto"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/sjawhar/envoy/internal/cipher/config"
	"github.com/sjawhar/envoy/internal/cipher/oauth"
	"github.com/sjawhar/envoy/internal/oidc"
)

// The lines a loggedKeySet writes about the key set of an issuer the broker reads directly,
// Google's and the token exchange's GitHub issuer, as loggedIssuers writes them about a cluster
// issuer's: each says that the key set was found other than the line before it said, so a key set
// that cannot be read for a day writes one line, not one for each token. A line that a key set
// cannot be read is logged at WARN, when a read of it fails and its last line, if it has one, did
// not say so, followed by issuer= and error=, which names the key set's address and the key id
// asked for, never a token. A line that it can be read again is logged at INFO, once, when a key of
// it is read after its last line said that it could not be, followed by issuer=.
const (
	googleKeysUnreadableMessage = "broker: google key set cannot be read"
	googleKeysReadableMessage   = "broker: google key set can be read again"
	githubKeysUnreadableMessage = "broker: github key set cannot be read"
	githubKeysReadableMessage   = "broker: github key set can be read again"
)

// loggedKeySet is the key set of one issuer as every reader of it in the broker reads it, logging
// each change of the key set between read and not read once (unreadableMessage, readableMessage).
// Every route answers a token whose key it could not read 503 OIDC_ISSUER_UNAVAILABLE and logs
// nothing, and package oidc logs no read, so these two lines are the log of a key set the broker
// cannot read. Google's (newGoogleKeySet) is read by a listed caller's and an edge listener's Google
// verifier, the token exchange's Google, person and machine-actor tokens, and the NATS auth
// service's Google verifier; the GitHub issuer's (newGitHubKeySet) by the token exchange's
// workflow tokens alone.
type loggedKeySet struct {
	keys                               *oidc.Keys
	issuer                             string
	unreadableMessage, readableMessage string
	// unreadable is whether the key set's last line said that it could not be read.
	unreadable atomic.Bool
}

// newGoogleKeySet is keys, Google's key set at issuer (BROKER_GOOGLE_OIDC_ISSUER), logging its
// changes.
func newGoogleKeySet(keys *oidc.Keys, issuer string) *loggedKeySet {
	return &loggedKeySet{keys: keys, issuer: issuer, unreadableMessage: googleKeysUnreadableMessage, readableMessage: googleKeysReadableMessage}
}

// newGitHubKeySet is keys, the GitHub issuer's key set at issuer (BROKER_EXCHANGE_GITHUB_ISSUER),
// logging its changes.
func newGitHubKeySet(keys *oidc.Keys, issuer string) *loggedKeySet {
	return &loggedKeySet{keys: keys, issuer: issuer, unreadableMessage: githubKeysUnreadableMessage, readableMessage: githubKeysReadableMessage}
}

// Key is Keys.Key, logging the change its answer finds in the key set: it cannot be read where
// the answer is oidc.ErrKeySet, and can be read where a key was read. An answer whose caller's
// context ended says nothing of the key set, and neither does a key id the set does not hold
// (oidc.ErrNoKey) or holds twice: neither logs.
func (l *loggedKeySet) Key(ctx context.Context, kid, alg string) (crypto.PublicKey, error) {
	key, err := l.keys.Key(ctx, kid, alg)
	switch {
	case ctx.Err() != nil:
	case errors.Is(err, oidc.ErrKeySet):
		// Of the tokens a burst answers at once, one alone swaps false for true, so the burst
		// writes one line.
		if !l.unreadable.Swap(true) {
			slog.Warn(l.unreadableMessage, "issuer", l.issuer, "error", err)
		}
	case err == nil:
		// Loaded before it is swapped, so a key read while the set can be read writes nothing.
		if l.unreadable.Load() && l.unreadable.Swap(false) {
			slog.Info(l.readableMessage, "issuer", l.issuer)
		}
	}
	return key, err
}

// githubExchangeIssuer is the token exchange's GitHub issuer: BROKER_EXCHANGE_GITHUB_ISSUER, for
// BROKER_EXCHANGE_GITHUB_AUDIENCE, with its key set read through keysFor (issuerKeys in
// production) and logged (newGitHubKeySet).
func githubExchangeIssuer(ctx context.Context, cfg config.Config, keysFor keysFunc) (oauth.IssuerConfig, error) {
	keys, err := keysFor(ctx, cfg.ExchangeGitHubIssuer)
	if err != nil {
		return oauth.IssuerConfig{}, err
	}
	return oauth.IssuerConfig{
		Issuer: cfg.ExchangeGitHubIssuer, Audience: cfg.ExchangeGitHubAudience,
		Keys: newGitHubKeySet(keys, cfg.ExchangeGitHubIssuer),
	}, nil
}
