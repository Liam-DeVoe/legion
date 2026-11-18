// Package brokertest is a public re-export of internal/broker/brokertest's real-broker test rig,
// existing solely so a module outside github.com/sjawhar/envoy — packages/daemon's
// agentsecrets contract test — can drive it: Go's internal-package rule refuses to let any
// package whose own import path does not start with github.com/sjawhar/envoy import a package
// under github.com/sjawhar/envoy/internal/..., which blocks a foreign module from importing
// internal/broker/brokertest directly, however workspace-linked the two modules are. This facade
// changes no behavior — Rig and Option are type aliases for the internal package's own types, and
// NewRig and WithAccessLifetime simply forward to it — so every exported field and method
// internal/broker/brokertest.Rig carries (Req, UI, MintPodToken, Store, Operator, ...) stays
// reachable exactly as documented there; that file remains the one place the rig's own behavior
// is described and changed.
package brokertest

import (
	"testing"
	"time"

	internal "github.com/sjawhar/envoy/internal/broker/brokertest"
)

// Rig is internal/broker/brokertest.Rig under a name a foreign module may import.
type Rig = internal.Rig

// Option is internal/broker/brokertest.Option: one change to how NewRig wires its broker.
type Option = internal.Option

// DaemonAccount is internal/broker/brokertest.DaemonAccount: the ServiceAccount every Rig lets log
// in as legion-daemon with its token.
const DaemonAccount = internal.DaemonAccount

// ExchangeRolesAccount is internal/broker/brokertest.ExchangeRolesAccount: the account the Rig's
// exchange lists with no fixed session, whose pods trade only for a session registered for them.
// Rig.MintAccountPodToken mints the token a registration presents for one of its pods, and
// Rig.MintSubjectToken the token that pod trades.
const ExchangeRolesAccount = internal.ExchangeRolesAccount

// DaemonExchangeAccount is internal/broker/brokertest.DaemonExchangeAccount: the account the
// Rig's exchange lists for a Legion daemon's own pod, whose entry fixes the session
// legion-daemon:EXAMPLE for project EXAMPLE and names the Rig's Operator as its person.
// Rig.MintSubjectToken mints the token that pod trades.
const DaemonExchangeAccount = internal.DaemonExchangeAccount

// OtherProjectAccount is internal/broker/brokertest.OtherProjectAccount: the account the Rig's
// exchange lists for a pod of a second project, whose entry fixes the session
// other-project-worker for project OTHER and names the Rig's Operator as its person.
const OtherProjectAccount = internal.OtherProjectAccount

// NewRig forwards to internal/broker/brokertest.NewRig, including its own skip when
// BROKER_TEST_DATABASE_URL is unset.
func NewRig(t *testing.T, opts ...Option) *Rig {
	return internal.NewRig(t, opts...)
}

// WithAccessLifetime forwards to internal/broker/brokertest.WithAccessLifetime: how long an
// access token the Rig's broker issues lasts, fifteen minutes unless a test says otherwise.
func WithAccessLifetime(d time.Duration) Option {
	return internal.WithAccessLifetime(d)
}
