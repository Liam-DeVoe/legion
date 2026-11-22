package main

import (
	"context"
	"crypto"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/sjawhar/envoy/internal/oidc"
)

// The lines googleKeySet writes about Google's key set, as loggedIssuers writes them about a
// cluster issuer's: each says that the key set was found other than the line before it said, so a
// key set that cannot be read for a day writes one line, not one for each token.
const (
	// googleKeysUnreadableMessage is logged, at WARN, when a read of Google's key set fails and
	// the key set's last line, if it has one, did not say that it could not be read, followed by
	// issuer= and error=, which names the key set's address and the key id asked for, never a
	// token.
	googleKeysUnreadableMessage = "broker: google key set cannot be read"
	// googleKeysReadableMessage is logged, at INFO, once when a key of Google's key set is read
	// after its last line said that it could not be, followed by issuer=.
	googleKeysReadableMessage = "broker: google key set can be read again"
)

// googleKeySet is the key set of BROKER_GOOGLE_OIDC_ISSUER as every reader of it in the broker
// reads it: a listed caller's and an edge listener's Google verifier, the token exchange's Google,
// person and machine-actor tokens, and the NATS auth service's Google verifier. It is keys,
// logging each change of the key set between read and not read once (googleKeysUnreadableMessage,
// googleKeysReadableMessage). Every route answers a token whose key it could not read 503
// OIDC_ISSUER_UNAVAILABLE and logs nothing, and package oidc logs no read, so these two lines are
// the log of Google's key set the broker cannot read.
type googleKeySet struct {
	keys   *oidc.Keys
	issuer string
	// unreadable is whether the key set's last line said that it could not be read.
	unreadable atomic.Bool
}

// newGoogleKeySet is keys, Google's key set at issuer, logging its changes.
func newGoogleKeySet(keys *oidc.Keys, issuer string) *googleKeySet {
	return &googleKeySet{keys: keys, issuer: issuer}
}

// Key is Keys.Key, logging the change its answer finds in the key set: it cannot be read where
// the answer is oidc.ErrKeySet, and can be read where a key was read. An answer whose caller's
// context ended says nothing of the key set, and neither does a key id the set does not hold
// (oidc.ErrNoKey) or holds twice: neither logs.
func (g *googleKeySet) Key(ctx context.Context, kid, alg string) (crypto.PublicKey, error) {
	key, err := g.keys.Key(ctx, kid, alg)
	switch {
	case ctx.Err() != nil:
	case errors.Is(err, oidc.ErrKeySet):
		// Of the tokens a burst answers at once, one alone swaps false for true, so the burst
		// writes one line.
		if !g.unreadable.Swap(true) {
			slog.Warn(googleKeysUnreadableMessage, "issuer", g.issuer, "error", err)
		}
	case err == nil:
		// Loaded before it is swapped, so a key read while the set can be read writes nothing.
		if g.unreadable.Load() && g.unreadable.Swap(false) {
			slog.Info(googleKeysReadableMessage, "issuer", g.issuer)
		}
	}
	return key, err
}
