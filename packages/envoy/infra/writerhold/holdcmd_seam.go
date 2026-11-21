package writerhold

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// ErrNoKeyStore is keygen run through CommandOn with no HoldDeps.Keys: there is no store to write
// the signing key's private half to, so nothing is generated.
var ErrNoKeyStore = errors.New("this run has no store of the hold's signing key")

// HoldDeps are what CommandOn reaches outside the process, in place of the broker, Secret Manager
// and the hub its flags name: a test of another package runs the hold's own words on its own stack
// with them (writerhold/holdtest), as StreamsCommandOn runs the held streams words. The hold's
// flags are still required and checked, and their values are not read.
type HoldDeps struct {
	// Records opens the operator's client of the broker's hold routes, signing when signing.
	Records func(ctx context.Context, signing bool) (*holdrecord.Client, error)
	// Keys opens the signing key's store, which keygen writes; nil refuses keygen with
	// ErrNoKeyStore.
	Keys func(ctx context.Context) (KeyStore, error)
	// Operator is the operator's reach of the hub.
	Operator func(ctx context.Context) (*Operator, error)
	// Now is the clock the minter report is checked by; time.Now when nil.
	Now func() time.Time
}

// CommandOn is Command with deps in place of the broker, Secret Manager and hub its flags name:
// the caller-stack seam of `envoy-infra hold`, as StreamsCommandOn is of `envoy-infra streams`.
func CommandOn(ctx context.Context, args []string, stdout, stderr io.Writer, deps HoldDeps) int {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	keys := deps.Keys
	if keys == nil {
		keys = func(context.Context) (KeyStore, error) { return nil, ErrNoKeyStore }
	}
	e := env{
		records: func(ctx context.Context, _ options, signing bool) (*holdrecord.Client, error) {
			return deps.Records(ctx, signing)
		},
		keys: keys,
		hub:  func(ctx context.Context, _ options) (*Operator, error) { return deps.Operator(ctx) },
		now:  now,
	}
	return run(ctx, args, stdout, stderr, e)
}
