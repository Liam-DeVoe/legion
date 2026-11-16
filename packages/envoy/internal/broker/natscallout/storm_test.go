package natscallout

import (
	"context"
	"crypto/ecdsa"
	"fmt"
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
// Service), as a session here is; giveUpOnRefusal leaves it off, which is the control.
func reconnectingSession(t *testing.T, url string, s *session, giveUpOnRefusal bool, options ...nats.Option) *nats.Conn {
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
	require.NoError(t, err, "session %s was not admitted", s.id)
	t.Cleanup(nc.Close)
	return nc
}

// After the server restarts with the service held off the bus for three seconds, every one of
// five hundred open session connections is admitted again, each presenting a proof over its new
// connection's nonce, and every decision the service makes is inside its budget.
//
// Three seconds is not arbitrary: the service is a client of the same server, so a restart drops
// its own connection too, and every client that comes back before it is refused. A client that is
// refused twice in a row by the same server closes for good unless it was told otherwise, so
// without that option a hub restart would leave the fleet with dead clients and no bus. The
// control below is exactly that client, and it closes.
func TestACalloutStormAfterAServerRestartAdmitsEveryConnection(t *testing.T) {
	const (
		connections = 500
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
	var wait sync.WaitGroup
	for index, s := range sessions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			conns[index] = reconnectingSession(t, url, s, false, nats.ReconnectHandler(func(*nats.Conn) { reconnected.Add(1) }))
		}()
	}
	wait.Wait()
	require.EqualValues(t, connections, decided.Load(), "every first connection was decided")

	// The service is off the bus for heldOff, so every client is refused for that long and must
	// still be there when it returns.
	service.Close()
	server.Restart(t, testnats.CalloutConfig(keys))
	time.Sleep(heldOff)
	back := startService(t, url, keys, deps, timed)
	t.Cleanup(back.Close)

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
	// "not received in time" is the server giving up on the service: a request it was sent and did
	// not answer inside the server's authorization timeout, which the budget exists to prevent.
	require.NotContains(t, server.Log(), "not received in time", "the server gave up on a request the service was sent")

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
			conns[index] = reconnectingSession(t, url, s, true)
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
