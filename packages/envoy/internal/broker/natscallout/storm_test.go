package natscallout

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// sessionOption is a session's credential as natscred.Session presents it, on a token source of
// the test's own: each CONNECT takes the token current answers once, signs the nonce the server
// sent that connection for it, and sends that same token.
func sessionOption(key *ecdsa.PrivateKey, current func() string) nats.Option {
	var mu sync.Mutex
	presented := ""
	sign := func(nonce []byte) ([]byte, error) {
		token := current()
		proof, err := brokerclient.ConnectionProof(key, token, string(nonce))
		mu.Lock()
		presented = token
		mu.Unlock()
		return []byte(proof), err
	}
	return func(o *nats.Options) error {
		if err := nats.UserJWT(func() (string, error) { return "-", nil }, sign)(o); err != nil {
			return err
		}
		return nats.TokenHandler(func() string { mu.Lock(); defer mu.Unlock(); return presented })(o)
	}
}

// reconnectingSession connects to url as s, as a bus client does: it keeps reconnecting, and it
// never gives up over a refused credential. That second part is what the storm below is about.
// nats.go closes a connection for good when the same server refuses it twice in a row with the
// same authorization error (v1.53.1, nats.go:4086: `if nc.current.lastErr == err &&
// !nc.Opts.IgnoreAuthErrorAbort { nc.ar = true }`), and a hub whose connections this service
// admits refuses every one of them while the service is away, which a restart of the hub itself
// causes. natscred sets the option on exactly the connections the auth service decides (Session,
// Service), as a session here is; giveUpOnRefusal leaves it off, which is the control. It answers
// the connect's error rather than failing the test itself, so a caller may run it on a goroutine
// of its own.
func reconnectingSession(t *testing.T, url string, s *session, giveUpOnRefusal bool, options ...nats.Option) (*nats.Conn, error) {
	t.Helper()
	settings := []nats.Option{
		sessionOption(s.key, func() string { return s.token }),
		nats.Timeout(10 * time.Second), nats.MaxReconnects(-1),
		nats.ReconnectWait(100 * time.Millisecond), nats.ReconnectJitter(200*time.Millisecond, 0),
	}
	if !giveUpOnRefusal {
		settings = append(settings, nats.IgnoreAuthErrorAbort())
	}
	nc, err := nats.Connect(url, append(settings, options...)...)
	if err != nil {
		return nil, fmt.Errorf("session %s was not admitted: %w", s.id, err)
	}
	t.Cleanup(nc.Close)
	return nc, nil
}

// After the server restarts with the service held off the bus for three seconds, every one of
// five hundred open session connections is admitted again, each presenting a proof over its new
// connection's nonce, every decision the service makes is inside its budget, and the server gives
// up on none of the requests it sends the service.
//
// Three seconds is not arbitrary: the service is a client of the same server, so a restart drops
// its own connection too, and every client that comes back before it is refused. A client that is
// refused twice in a row by the same server closes for good unless it was told otherwise, so
// without that option a hub restart would leave the fleet with dead clients and no bus. The
// control below is exactly that client, and it closes.
//
// Five hundred is the size dispatch://INFRA-840's check c-callout-available names, and more than
// the service's decision slots, so the reconnects that reach it together queue for a slot. Under
// the race detector it is two hundred (raceEnabled): the clients and the hub run in the test's own
// process, and at five hundred under -race on a loaded machine the first connects failed before
// anything under test had started. envoy-go's plain step runs the five hundred. The first connects
// are made twenty at a time for the same reason: they set the storm up and are not the storm.
func TestACalloutStormAfterAServerRestartAdmitsEveryConnection(t *testing.T) {
	connections := 500
	if raceEnabled {
		connections = 200
	}
	const (
		// How long the service is held off the bus. Each refused attempt costs the client the
		// server's whole authorization timeout, two seconds, because nobody answers
		// (nats-server server/auth_callout.go waits it out when no service is subscribed), so a
		// window of three seconds refuses every client at least once. The control below needs two
		// refusals in a row, which is what makes a client that does not set the option give up, so
		// it holds the service off for controlHeldOff instead.
		heldOff        = 3 * time.Second
		controlHeldOff = 9 * time.Second
	)
	keys := testnats.NewCalloutKeys(t)
	server, url := testnats.StartWithCalloutServer(t, keys)
	sessions := make([]*session, connections)
	for index := range sessions {
		sessions[index] = newSession(t, fmt.Sprintf("ses_storm_%03d", index), 15*time.Minute)
	}
	var decided, slowest atomic.Int64
	introspect := introspector(sessions...)
	timed := func(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error) {
		began := time.Now()
		decision, err := Decide(ctx, deps, services, req)
		for took := int64(time.Since(began)); took > slowest.Load(); {
			if slowest.CompareAndSwap(slowest.Load(), took) {
				break
			}
		}
		return decision, err
	}
	deps := Deps{Introspect: func(ctx context.Context, token string) (oauthwire.Introspection, error) {
		decided.Add(1)
		return introspect(ctx, token)
	}}
	service := startService(t, url, keys, deps, timed)

	var reconnected atomic.Int64
	conns := make([]*nats.Conn, connections)
	errs := make([]error, connections)
	connecting := make(chan struct{}, 20)
	var wait sync.WaitGroup
	for index, s := range sessions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			connecting <- struct{}{}
			defer func() { <-connecting }()
			conns[index], errs[index] = reconnectingSession(t, url, s, false, nats.ReconnectHandler(func(*nats.Conn) { reconnected.Add(1) }))
		}()
	}
	wait.Wait()
	require.NoError(t, errors.Join(errs...), "a first connection was not admitted")
	require.EqualValues(t, connections, decided.Load(), "every first connection was decided")

	// The service is off the bus for heldOff, so every client is refused for that long and must
	// still be there when it returns. The restart turns on the server's debug log, where the
	// server says it gave up on a request (hubGaveUp).
	service.Close()
	server.Restart(t, debugCalloutConfig(keys))
	time.Sleep(heldOff)
	back := startService(t, url, keys, deps, timed)
	t.Cleanup(back.Close)
	// A request the server sent before back subscribed went to no one, and the server logs giving
	// up on it one authorization timeout after it sent it; so what the server logs from a timeout
	// after back is up is about requests back was sent, and the log before that is the control:
	// the server giving up on the requests no one was there for.
	hubTimeout := testnats.CalloutAuthTimeout * time.Second
	backUp := make(chan int, 1)
	time.AfterFunc(hubTimeout, func() { backUp <- len(server.Log()) })
	testwait.WithinPolling(t, 60*time.Second, 50*time.Millisecond, "every connection admitted again", func() bool {
		for _, nc := range conns {
			if !nc.IsConnected() {
				return false
			}
		}
		return true
	})
	closed := 0
	for _, nc := range conns {
		if nc.IsClosed() {
			closed++
		}
	}
	require.Zero(t, closed, "%d connections closed for good while the service was away", closed)
	require.GreaterOrEqual(t, decided.Load(), int64(2*connections), "each reconnect was decided afresh")
	require.Less(t, time.Duration(slowest.Load()), decideBudget,
		"the slowest of %d decisions took %s, at or past the %s budget", decided.Load(), time.Duration(slowest.Load()), decideBudget)
	t.Logf("reconnects=%d decided=%d slowest-decision=%s; clients refused while the service was away: %d",
		reconnected.Load(), decided.Load(), time.Duration(slowest.Load()), strings.Count(server.Log(), "authentication error"))
	// Every request back was sent went out before the last connection was admitted, so a timeout
	// on from there, the server has logged giving up on any of them it gave up on.
	mark := <-backUp
	time.Sleep(hubTimeout)
	require.Positive(t, strings.Count(server.Log()[:mark], hubGaveUp),
		"the server logged giving up on none of the requests it sent while the service was away: the check below cannot see one")
	gaveUp := strings.Count(server.Log()[mark:], hubGaveUp)
	require.Zero(t, gaveUp, "the server gave up on %d requests the service was sent", gaveUp)

	t.Run("control: clients that give up on a refused credential", func(t *testing.T) {
		keys := testnats.NewCalloutKeys(t)
		server, url := testnats.StartWithCalloutServer(t, keys)
		const control = 20
		controls := make([]*session, control)
		for index := range controls {
			controls[index] = newSession(t, fmt.Sprintf("ses_control_%02d", index), 15*time.Minute)
		}
		deps := Deps{Introspect: introspector(controls...)}
		first := startService(t, url, keys, deps, Decide)
		conns := make([]*nats.Conn, control)
		for index, s := range controls {
			nc, err := reconnectingSession(t, url, s, true)
			require.NoError(t, err)
			conns[index] = nc
		}
		first.Close()
		server.Restart(t, testnats.CalloutConfig(keys))
		time.Sleep(controlHeldOff)
		back := startService(t, url, keys, deps, Decide)
		t.Cleanup(back.Close)

		testwait.WithinPolling(t, 30*time.Second, 50*time.Millisecond, "the clients that give up to close", func() bool {
			for _, nc := range conns {
				if !nc.IsClosed() {
					return false
				}
			}
			return true
		})
	})
}

// A connection already open stays open while the auth service is stopped, and receives on its
// own channel, until the user it was admitted as expires; the server then closes it. Once the
// service answers again it reconnects with the live token its client holds by then, and receives
// again.
func TestConnectionsSurviveTheAuthServiceStoppingUntilTheirUserExpires(t *testing.T) {
	keys := testnats.NewCalloutKeys(t)
	static, staticSeed := publisher(t)
	url := testnats.StartWithCallout(t, keys, static)
	a := newSession(t, "ses_a", 4*time.Second)
	renewed := *a
	renewed.token = "renewed-access-token-of-ses_a"
	renewed.answer.ExpiresAt = time.Now().Add(15 * time.Minute).Unix()
	deps := Deps{Introspect: introspector(a, &renewed)}
	service := startService(t, url, keys, deps, Decide)

	var current atomic.Value
	current.Store(a.token)
	option := sessionOption(a.key, func() string { return current.Load().(string) })
	var disconnected, reconnected atomic.Int64
	nc, err := nats.Connect(url, option, nats.IgnoreAuthErrorAbort(), nats.Timeout(10*time.Second), nats.MaxReconnects(-1),
		nats.ReconnectWait(200*time.Millisecond),
		nats.DisconnectErrHandler(func(*nats.Conn, error) { disconnected.Add(1) }),
		nats.ReconnectHandler(func(*nats.Conn) { reconnected.Add(1) }))
	require.NoError(t, err)
	defer nc.Close()
	own, err := nc.SubscribeSync(bus.DeliverSubject(a.id))
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	pub, err := testnats.ConnectSeed(t, url, staticSeed)
	require.NoError(t, err)
	defer pub.Close()
	receives := func(payload string) {
		t.Helper()
		// A round trip on nc first: after a reconnect nats.go has written the subscription again,
		// but the server reads nc's socket and pub's on goroutines of their own, so without it the
		// publish can reach the server before the subscription does and go to no one.
		require.NoError(t, nc.Flush())
		require.NoError(t, pub.Publish(bus.DeliverSubject(a.id), []byte(payload)))
		require.NoError(t, pub.Flush())
		msg, err := own.NextMsg(5 * time.Second)
		require.NoError(t, err, "ses_a received nothing on its own channel")
		require.Equal(t, payload, string(msg.Data))
	}

	service.Close()
	receives("while the service is stopped")
	require.True(t, nc.IsConnected())
	require.Zero(t, disconnected.Load(), "the open connection was dropped when the service stopped")

	testwait.WithinPolling(t, 15*time.Second, 50*time.Millisecond, "the server closing the connection when its user expires", func() bool {
		return disconnected.Load() > 0
	})
	require.False(t, nc.IsConnected(), "the connection outlived its user")

	current.Store(renewed.token)
	startService(t, url, keys, deps, Decide)
	testwait.WithinPolling(t, 30*time.Second, 50*time.Millisecond, "the reconnect once the service answers again", func() bool {
		return reconnected.Load() > 0 && nc.IsConnected()
	})
	receives("after the service is back")
}

// hubGaveUp is what nats-server logs, at debug level only, for an authorization request no service
// answered inside its authorization timeout (server/auth_callout.go:462-463). The server then
// refuses that client itself, and an answer arriving after it is read by no one.
const hubGaveUp = "not received in time"

// debugCalloutConfig is testnats.CalloutConfig with nats-server's debug log on, so that hubGaveUp
// reaches Server.Log: without it a test that reads the server's log for one reads nothing,
// whatever the service does.
func debugCalloutConfig(keys testnats.CalloutKeys) string {
	return testnats.CalloutConfig(keys) + "\ndebug: true\n"
}

// startDebugCalloutServer is testnats.StartWithCalloutServer with the server's debug log on: the
// fixture names no debug, so the server is restarted with the same configuration and debug added.
func startDebugCalloutServer(t *testing.T, keys testnats.CalloutKeys) (*testnats.Server, string) {
	t.Helper()
	server, url := testnats.StartWithCalloutServer(t, keys)
	server.Restart(t, debugCalloutConfig(keys))
	return server, url
}

// refusalCost is what each record costlyLog writes takes: a stand-in, scaled up, for the work of
// answering one request, which for a refusal is about 0.3 ms of CPU to open, read, sign and seal
// it (3.6 ms under -race, measured with the service's own keys).
const refusalCost = 50 * time.Millisecond

// costlyHandler is a text handler each record of which takes cost before it is written, outside
// any lock: records logged on many goroutines take it together, as work on goroutines of their own
// runs together across a replica's cores, and records logged on one goroutine take it in turn.
type costlyHandler struct {
	slog.Handler
	cost time.Duration
}

func (h costlyHandler) Handle(ctx context.Context, record slog.Record) error {
	time.Sleep(h.cost)
	return h.Handler.Handle(ctx, record)
}

func (h costlyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return costlyHandler{h.Handler.WithAttrs(attrs), h.cost}
}

func (h costlyHandler) WithGroup(name string) slog.Handler {
	return costlyHandler{h.Handler.WithGroup(name), h.cost}
}

// costlyLog is a log whose every record takes refusalCost (costlyHandler), and the buffer it is
// written to. Every refusal the service answers writes one record, so each refusal costs at least
// that: made one after another, as the delivery goroutine once made them, the hundred or so
// refusals of a storm take about five seconds, past the hub's two-second authorization timeout in
// either build, and made side by side they take about one refusal's time.
func costlyLog() (*slog.Logger, *logtest.Buffer) {
	buffer := &logtest.Buffer{}
	return slog.New(costlyHandler{slog.NewTextHandler(buffer, nil), refusalCost}), buffer
}

// stormRequests is how many connections each storm below sends one replica at once. The storms set
// the bounds to a handful, sixteen waiting beside one or two slots, so almost every request goes
// past the waiting bound, which is the regime a storm of more than about 1,100 requests is for the
// deployed bounds (64 slots, 1,024 waiting), and refusalCost puts what those refusals cost one
// after another past the hub's timeout. The count is kept that small because the clients and the
// hub run in the test's own process: at 400, under -race on a loaded machine, they fell behind
// themselves, and clients failed their connects with the service's fix in place.
const stormRequests = 120

// requireEveryClientHeardInsideTheHubsTimeout fails unless the hub gave up on no request of a
// storm, every client heard its answer before the hub's authorization timeout, admitted (a nil
// error) or refused, and the storm reached what it is for: at least half its requests past the
// waiting bound, read from the refusals in log. It answers how many were admitted.
func requireEveryClientHeardInsideTheHubsTimeout(t *testing.T, server *testnats.Server, log *logtest.Buffer, errs []error, took []time.Duration) int {
	t.Helper()
	hubTimeout := testnats.CalloutAuthTimeout * time.Second
	// Every request of the storm went out before its client returned, and the hub logs giving up on
	// one a timeout after it sent it, so a timeout from now its log names every one it gave up on.
	time.Sleep(hubTimeout)
	admitted, late, failed := 0, 0, 0
	var slowest time.Duration
	var other error
	for index, err := range errs {
		switch {
		case err == nil:
			admitted++
		case !errors.Is(err, nats.ErrAuthorization):
			failed++
			other = err
		}
		slowest = max(slowest, took[index])
		if took[index] >= hubTimeout {
			late++
		}
	}
	pastBound, gaveUp := strings.Count(log.String(), "label="+labelWaitingBound), strings.Count(server.Log(), hubGaveUp)
	t.Logf("%d requests: %d admitted, %d past the waiting bound, %d heard at or after %s (slowest %s), %d failed otherwise (%v); the hub gave up on %d",
		len(errs), admitted, pastBound, late, hubTimeout, slowest, failed, other, gaveUp)
	require.Zero(t, gaveUp, "the hub gave up on %d requests it sent the service", gaveUp)
	require.Zero(t, late, "%d clients heard at or after the hub's %s timeout; the slowest after %s", late, hubTimeout, slowest)
	require.Zero(t, failed, "%d clients failed their connect neither admitted nor refused: %v", failed, other)
	require.GreaterOrEqual(t, pastBound, len(errs)/2, "the storm did not go past the waiting bound, which is the regime it is for")
	return admitted
}

// A storm of connections far past what one replica holds waiting is answered inside the hub's
// authorization timeout, every connection admitted or refused, and the hub gives up on none of
// them. Past the waiting bound a request is refused off the goroutine nats.go delivers every
// request on (deliver); refused on it, as each once was, every refusal held back the delivery of
// every request behind it, and the last of a storm were delivered after the hub had refused their
// clients, given a fresh budget, and answered to no one. The bounds here are two slots and sixteen
// waiting, so the storm is the regime a storm past the deployed bounds is, and each refusal's work
// is scaled up (costlyLog).
func TestAStormPastTheWaitingBoundIsAnsweredInsideTheHubsTimeout(t *testing.T) {
	withDecisionSlots(t, 2)
	withWaitingBound(t, 16)
	keys := testnats.NewCalloutKeys(t)
	server, url := startDebugCalloutServer(t, keys)
	sessions := sessionsNamed(t, "ses_storm", stormRequests)
	deps := slowIntrospect(100*time.Millisecond, sessions...)
	logger, log := costlyLog()
	deps.Log = logger
	startService(t, url, keys, deps, Decide)

	errs, took := connectAll(t, url, sessions)
	admitted := requireEveryClientHeardInsideTheHubsTimeout(t, server, log, errs, took)
	require.Positive(t, admitted, "no connection of the storm was admitted")
}

// A flood of connections that hold no credential at all, which any client that reaches the hub can
// send one of per TCP connect, is answered the same way: every connection refused inside the hub's
// timeout, and the hub gives up on none of them. Such a request is refused without a broker call,
// so its decision costs what a refusal does, but the flood still outruns one slot and sixteen
// waiting, as a flood outruns the cores of a replica, and what goes past them is refused off the
// delivery goroutine.
func TestAFloodOfConnectionsHoldingNoCredentialIsAnsweredInsideTheHubsTimeout(t *testing.T) {
	withDecisionSlots(t, 1)
	withWaitingBound(t, 16)
	keys := testnats.NewCalloutKeys(t)
	server, url := startDebugCalloutServer(t, keys)
	logger, log := costlyLog()
	startService(t, url, keys, Deps{Log: logger}, Decide)

	errs := make([]error, stormRequests)
	took := make([]time.Duration, stormRequests)
	var wait sync.WaitGroup
	for index := range errs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			began := time.Now()
			nc, err := nats.Connect(url, nats.NoReconnect(), nats.Timeout(10*time.Second))
			took[index], errs[index] = time.Since(began), err
			if err == nil {
				nc.Close()
			}
		}()
	}
	wait.Wait()
	admitted := requireEveryClientHeardInsideTheHubsTimeout(t, server, log, errs, took)
	require.Zero(t, admitted, "a connection holding no credential was admitted")
}
