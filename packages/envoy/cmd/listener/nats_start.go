package main

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
)

// listenerCredentialWait is how long the listener's start waits out a refusal of its NATS
// credential (bus.WithCredentialWait), from its first try to connect until the stream's reconcile
// on its first connection succeeds: main passes it to runListener, which starts its client with it
// (newNATSStart, connectListenerBus). A server refuses a new nkey until it reloads a configuration
// that names it, and a hub's servers reload only once the kubelet has refreshed each one's mounted
// configuration. On 2026-11-22 the last of a hub's servers reloaded a session listener's new nkey
// 64 s after its configuration changed and 71.4 s after the listener's Deployment was created, and
// the listener, which then exited at the first refusal, exited three times meanwhile. 180 s holds
// that reading two and a half times over, and an estimate of a kubelet refresh of up to 90 s per
// server, through one Service address, with about 70 s to spare. It is a chosen tolerance, not a
// proved bound. While the start waits, /livez answers 204 and /healthz 503 (registerProbeRoutes),
// so a Deployment's probes must not restart the listener inside it: one whose startup and liveness
// probes read /livez restarts none.
const listenerCredentialWait = 180 * time.Second

// natsStart is the listener's start on NATS (Phase 5, connectListenerBus) as its probe routes
// report it. wait is the credential wait its main client is started with (option), an owning
// client's or, on an edge listener, the client bus.Connect returns, and refusal the last refusal of
// the credential the start is waiting out: nil before the first refusal and once the start has
// succeeded.
type natsStart struct {
	wait    time.Duration
	refusal atomic.Pointer[string]
}

// newNATSStart is a start that waits out a refusal of the credential for up to wait.
func newNATSStart(wait time.Duration) *natsStart { return &natsStart{wait: wait} }

// option is the option the main client's start takes, an owning client's or an edge listener's: the
// start's wait, handing each refusal to refused as the bus meets it.
func (s *natsStart) option() bus.ConnectOption { return bus.WithCredentialWait(s.wait, s.refused) }

// refused records err as the last refusal of the credential.
func (s *natsStart) refused(err error) {
	text := err.Error()
	s.refusal.Store(&text)
}

// succeeded forgets the start's refusals: the start has connected and reconciled the stream, so
// /healthz answers as it does for a start that met none.
func (s *natsStart) succeeded() { s.refusal.Store(nil) }

// healthz is next, answering /healthz, behind the start's own answer while it waits out a refusal
// of its credential: 503, "unhealthy", with an error naming the last refusal and, as every answer
// does, the fields issuers adds (writeHealth). A readiness probe that runs curl -f against /healthz
// so keeps a listener whose credential the hub refuses out of its Service, which a 200 "starting"
// would not. Before the first refusal and once the start has succeeded, next answers.
func (s *natsStart) healthz(next http.Handler, issuers func() map[string]any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refusal := s.refusal.Load()
		if refusal == nil {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeHealth(w, http.StatusServiceUnavailable, map[string]any{"status": "unhealthy", "error": "nats credential refused: " + *refusal}, issuers)
	})
}

// registerProbeRoutes registers the two routes a Deployment probes the listener on, which main
// serves from Phase 4 on, before any NATS work, and which the tests of the start serve the same
// way.
//
// /healthz is always reachable, before NATS setup as well as after it: start's answer while the
// start waits out a refusal of the credential (natsStart.healthz), and healthzHandler's otherwise,
// over deps. Every answer carries what issuers adds, the identity token issuers' fields
// (issuerHealthFields), which never decide its status.
//
// /livez answers 204 and touches nothing: no flush to NATS, no cache ping and no read of the
// durable consumer. It says only that the process answers, for the startup and liveness probes,
// which must not wait on or fail with NATS: a listener whose bus is down repairs itself, a start
// whose credential is refused waits for the hub to admit it, and a restart would only drop its
// warm caches. /healthz stays the readiness route, the one that reads the bus, because its HTTP
// status is what tells a starting pod from a serving one.
func registerProbeRoutes(mux *http.ServeMux, deps *atomic.Pointer[listenerDeps], start *natsStart, issuers func() map[string]any) {
	mux.Handle("/healthz", start.healthz(healthzHandler(deps, issuers), issuers))
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}
