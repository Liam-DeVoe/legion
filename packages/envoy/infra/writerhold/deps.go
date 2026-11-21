package writerhold

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// ErrNoKeyStore is a `hold keygen` run whose Deps name no key store.
var ErrNoKeyStore = errors.New("no key store: Deps.Keys is nil")

// Deps are what the held commands, `envoy-infra hold`, `envoy-infra streams update|census` and
// `envoy-infra buckets`, reach outside the process. RealDeps gives a real run's: the broker's
// routes and the hub that HubFlags names. A test, of this package or another (writerhold/holdtest,
// t-7's), gives its own and runs the command through CommandOn, StreamsCommandOn or
// buckets.CommandOn. The hold's flags are required and checked whatever Deps a run takes; a test's
// functions need not read them.
type Deps struct {
	// Records opens the operator's client of the broker's hold routes h names, with the signing key
	// when signing.
	Records func(ctx context.Context, h HubFlags, signing bool) (*holdrecord.Client, error)
	// Operator is the operator's reach of the hub h names: its Kubernetes objects, its NATS
	// connections and the probe.
	Operator func(ctx context.Context, h HubFlags) (*Operator, error)
	// Keys opens the signing key's store, which `hold keygen` writes; with none, keygen fails with
	// ErrNoKeyStore.
	Keys func(ctx context.Context) (KeyStore, error)
	// Now is the clock the minter report is checked by; time.Now when nil.
	Now func() time.Time
	// Readers are the census's readers. Readers.Stores is the daemon stores' stream-id writer, which
	// hub-restore's abort runs too (consumernames.ReconcileDaemonStreamIDs, which cmd/envoy-infra
	// passes in); nil for none.
	Readers CensusReaders
}

// RealDeps are a real run's Deps: the broker's routes with the operator's token and the hold key
// from Secret Manager (HubFlags.Records), the hub through HubFlags.Operator, and readers.
func RealDeps(readers CensusReaders) Deps {
	return Deps{
		Records: func(ctx context.Context, h HubFlags, signing bool) (*holdrecord.Client, error) {
			return h.Records(ctx, signing)
		},
		Operator: func(ctx context.Context, h HubFlags) (*Operator, error) { return h.Operator(ctx) },
		Keys:     openKeys,
		Now:      time.Now,
		Readers:  readers,
	}
}

// CommandOn is Command with deps in place of the broker, Secret Manager and hub its flags name.
func CommandOn(ctx context.Context, args []string, stdout, stderr io.Writer, deps Deps) int {
	return run(ctx, args, stdout, stderr, deps)
}

// Clock is d.Now, or time.Now when nil.
func (d Deps) Clock() func() time.Time {
	if d.Now == nil {
		return time.Now
	}
	return d.Now
}

// keyStore opens d's key store, or answers ErrNoKeyStore when it names none.
func (d Deps) keyStore(ctx context.Context) (KeyStore, error) {
	if d.Keys == nil {
		return nil, ErrNoKeyStore
	}
	return d.Keys(ctx)
}
