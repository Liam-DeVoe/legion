package holdtest

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/cipher/natscallout"
	"github.com/sjawhar/envoy/internal/cipher/natshold"
	"github.com/sjawhar/envoy/internal/metrics"
	"github.com/sjawhar/envoy/internal/testnats"
)

// HoldRead is a reading of the hold row, natscallout.Deps.HoldRead's type.
type HoldRead = func(ctx context.Context) (natshold.Reading, bool, error)

// ReplicaConfig is how a replica StartReplica starts differs from the stack's own (Stack.Auth):
// each field's zero value is the stack's.
type ReplicaConfig struct {
	// NoHoldRead starts the replica with no HoldRead, a broker whose BROKER_NATS_HOLD_READ is
	// false: it reads no row and answers every decision as if the row held released.
	NoHoldRead bool
	// Wrap, when set, is given the replica's own read of the row (natshold.Read on the replica's
	// pool) and answers the read the replica decides on: a test's model of an older view.
	Wrap func(read HoldRead) HoldRead
	// HoldKeys are the replica's BROKER_NATS_HOLD_SIGNING_KEYS; nil is the broker's one key.
	HoldKeys []ed25519.PublicKey
	// Now is the replica's clock (natscallout.Deps.Now); nil is time.Now.
	Now func() time.Time
}

// Replica is one more replica of the stack's auth service, in the replicas' queue group beside
// any other that runs: the same hub, keys, services and platform tokens as Stack.Auth, and its own
// metrics and its own Postgres pool, which reaches the stack's Postgres through Postgres, a relay
// a test can freeze (every byte held both ways, as a network partition makes it) or cut (every
// connection closed and every new one refused, as a stopped Postgres looks to this replica alone),
// and restore. Its every reading of the row, as the replica decided on it, is kept (Readings).
type Replica struct {
	Service *natscallout.Service
	// Registry holds the replica's broker_nats_callout_decisions_total and
	// broker_nats_callout_hold_unknown (natscallout.NewMetrics).
	Registry *metrics.Registry
	// Postgres is the relay from the replica's pool to the stack's Postgres
	// (testnats.StartProxy); nil with NoHoldRead.
	Postgres *testnats.Proxy

	mu       sync.Mutex
	readings []Reading
}

// Reading is one reading of the hold row a replica decided on: the phase the read answered, or
// that it found no row, or its error.
type Reading struct {
	Phase string
	Found bool
	Err   error
}

// StartReplica starts a replica of the auth service on the stack's hub, closed when t ends, and
// returns it once its subscription reaches the hub.
func (s *Stack) StartReplica(t testing.TB, cfg ReplicaConfig) *Replica {
	t.Helper()
	r := &Replica{Registry: metrics.New()}
	deps := natscallout.Deps{ServiceToken: s.tokens.verify, Metrics: natscallout.NewMetrics(r.Registry), Now: cfg.Now}
	if !cfg.NoHoldRead {
		upstream := s.Broker.Pool.Config()
		r.Postgres = testnats.StartProxy(t, "tcp://"+net.JoinHostPort(upstream.ConnConfig.Host, strconv.Itoa(int(upstream.ConnConfig.Port))))
		relay, err := url.Parse(r.Postgres.URL())
		if err != nil {
			t.Fatalf("holdtest: read the Postgres relay's address: %v", err)
		}
		port, err := strconv.Atoi(relay.Port())
		if err != nil {
			t.Fatalf("holdtest: read the Postgres relay's port: %v", err)
		}
		poolConfig := s.Broker.Pool.Config()
		poolConfig.ConnConfig.Host, poolConfig.ConnConfig.Port = relay.Hostname(), uint16(port)
		poolConfig.ConnConfig.Fallbacks = nil
		pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
		if err != nil {
			t.Fatalf("holdtest: open the replica's pool: %v", err)
		}
		t.Cleanup(pool.Close)
		read := HoldRead(func(ctx context.Context) (natshold.Reading, bool, error) {
			return natshold.Read(ctx, pool, s.schema)
		})
		if cfg.Wrap != nil {
			read = cfg.Wrap(read)
		}
		keys := cfg.HoldKeys
		if keys == nil {
			keys = []ed25519.PublicKey{s.Broker.VerifyKey}
		}
		// A replica of the auth service alone, beside no routes of its own, keeps a floor of its own
		// (nil), as natscallout's own replica tests do.
		deps.Hold = natscallout.NewHoldView(func(ctx context.Context) (natshold.Reading, bool, error) {
			reading, found, err := read(ctx)
			r.mu.Lock()
			r.readings = append(r.readings, Reading{Phase: reading.Phase, Found: found, Err: err})
			r.mu.Unlock()
			return reading, found, err
		}, keys, nil, deps.Metrics)
	}
	r.Service = s.startAuth(t, "a replica of the auth service", deps)
	return r
}

// Close closes the replica: it leaves the queue group, and the hub sends its requests to the
// replicas that remain.
func (r *Replica) Close() { r.Service.Close() }

// Readings are the replica's readings of the row so far, oldest first.
func (r *Replica) Readings() []Reading {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Reading(nil), r.readings...)
}

// Decisions is how many decisions the replica counted of credential kind (session, service or
// none) with outcome (admitted or refused) and reason (the refusal's label, "" for an admission),
// read from its broker_nats_callout_decisions_total.
func (r *Replica) Decisions(kind, outcome, reason string) int64 {
	want := []string{`kind="` + kind + `"`, `outcome="` + outcome + `"`, `reason="` + reason + `"`}
	var total int64
	for _, line := range r.scrape() {
		series, value, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(series, "broker_nats_callout_decisions_total{") {
			continue
		}
		matches := true
		for _, label := range want {
			matches = matches && strings.Contains(series, label)
		}
		if n, err := strconv.ParseInt(value, 10, 64); matches && err == nil {
			total += n
		}
	}
	return total
}

// HoldUnknown is the replica's broker_nats_callout_hold_unknown as its metrics port serves it, ""
// when the gauge is not served.
func (r *Replica) HoldUnknown() string {
	for _, line := range r.scrape() {
		if value, ok := strings.CutPrefix(line, "broker_nats_callout_hold_unknown "); ok {
			return value
		}
	}
	return ""
}

// scrape is the replica's metrics as the broker's metrics port serves them, line by line.
func (r *Replica) scrape() []string {
	recorder := httptest.NewRecorder()
	r.Registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	var lines []string
	scanner := bufio.NewScanner(recorder.Body)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}
