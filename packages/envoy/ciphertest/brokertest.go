// Package ciphertest is a public re-export of internal/cipher/ciphertest's real-broker test rig,
// existing solely so a module outside github.com/sjawhar/envoy — packages/daemon's
// cipher contract test and its sandbox runtime's kind test — can drive it: Go's
// internal-package rule refuses to let any package whose own import path does not start with
// github.com/sjawhar/envoy import a package under github.com/sjawhar/envoy/internal/..., which
// blocks a foreign module from importing internal/cipher/ciphertest directly, however
// workspace-linked the two modules are. This facade changes no behavior — Rig is a type alias for
// the internal package's own type, and NewRig simply forwards to it, with its options — so every
// exported field and method internal/cipher/ciphertest.Rig
// carries (Req, UI, MintPodToken, Store, Operator, ...) stays reachable exactly as documented
// there; that file remains the one place the rig's own behavior is described and changed.
package ciphertest

import (
	"net/http"
	"testing"

	internal "github.com/sjawhar/envoy/internal/cipher/ciphertest"
	"github.com/sjawhar/envoy/internal/cipher/policy"
)

// Option is internal/cipher/ciphertest.Option: how NewRig wires its broker.
type Option = internal.Option

// The owner and tiers a secret PutSecret writes is tagged with (internal/cipher/policy):
// OwnerShared for a secret nobody owns alone, TierAgent for one its owner's agents use without
// asking, TierHuman for one a person approves every request for.
const (
	OwnerShared = policy.OwnerShared
	TierAgent   = policy.TierAgent
	TierHuman   = policy.TierHuman
)

// WithListen is internal/cipher/ciphertest.WithListen: the broker listens on address, which a
// process outside the test's own reaches.
func WithListen(address string) Option { return internal.WithListen(address) }

// WithClusterIssuer is internal/cipher/ciphertest.WithClusterIssuer: the broker verifies pod,
// login and caller tokens against a real cluster's service-account issuer for audience.
func WithClusterIssuer(issuer, audience string, client *http.Client, logins, services map[string]string) Option {
	return internal.WithClusterIssuer(issuer, audience, client, logins, services)
}

// Rig is internal/cipher/ciphertest.Rig under a name a foreign module may import.
type Rig = internal.Rig

// DaemonAccount is internal/cipher/ciphertest.DaemonAccount: the ServiceAccount every Rig lets log
// in as legion-daemon with its token.
const DaemonAccount = internal.DaemonAccount

// ExchangeRolesAccount is internal/cipher/ciphertest.ExchangeRolesAccount: the account the Rig's
// exchange lists with no fixed session, whose pods trade only for a session registered for them.
// Rig.MintAccountPodToken mints the token a registration presents for one of its pods, and
// Rig.MintSubjectToken the token that pod trades.
const ExchangeRolesAccount = internal.ExchangeRolesAccount

// NewRig forwards to internal/cipher/ciphertest.NewRig, including its own skip when
// BROKER_TEST_DATABASE_URL is unset.
func NewRig(t *testing.T, options ...Option) *Rig {
	return internal.NewRig(t, options...)
}
