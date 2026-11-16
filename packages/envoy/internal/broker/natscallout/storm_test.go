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

// reconnectingSession connects to url as s, as a client that keeps reconnecting does: every
// CONNECT signs the nonce of that connection, so a reconnect presents a proof of its own.
func reconnectingSession(t *testing.T, url string, s *session, options ...nats.Option) *nats.Conn {
	t.Helper()
	option := sessionOption(s.key, func() string { return s.token })
	nc, err := nats.Connect(url, append([]nats.Option{
		option, nats.Timeout(10 * time.Second), nats.MaxReconnects(-1),
		nats.ReconnectWait(100 * time.Millisecond), nats.ReconnectJitter(200*time.Millisecond, 0),
	}, options...)...)
	require.NoError(t, err, "session %s was not admitted", s.id)
	t.Cleanup(nc.Close)
	return nc
}

// After the server restarts, every one of five hundred open session connections reconnects at
// once, each presenting a proof over its new connection's nonce, and every one is admitted: none
// waits out the server's authorization timeout, which the server would log.
func TestACalloutStormAfterAServerRestartAdmitsEveryConnection(t *testing.T) {
	const connections = 500
	keys := testnats.NewCalloutKeys(t)
	server, url := testnats.StartWithCalloutServer(t, keys)
	sessions := make([]*session, connections)
	for index := range sessions {
		sessions[index] = newSession(t, fmt.Sprintf("ses_storm_%03d", index), 15*time.Minute)
	}
	var decided atomic.Int64
	introspect := introspector(sessions...)
	startService(t, url, keys, Deps{Introspect: func(ctx context.Context, token string) (oauthwire.Introspection, error) {
		decided.Add(1)
		return introspect(ctx, token)
	}}, Decide)

	var reconnected atomic.Int64
	conns := make([]*nats.Conn, connections)
	var wait sync.WaitGroup
	for index, s := range sessions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			conns[index] = reconnectingSession(t, url, s, nats.ReconnectHandler(func(*nats.Conn) { reconnected.Add(1) }))
		}()
	}
	wait.Wait()
	require.EqualValues(t, connections, decided.Load(), "every first connection was decided")

	server.Restart(t, testnats.CalloutConfig(keys))
	testwait.WithinPolling(t, 60*time.Second, 50*time.Millisecond, "every connection reconnected after the restart", func() bool {
		return reconnected.Load() == connections
	})
	for index, nc := range conns {
		require.True(t, nc.IsConnected(), "connection %d is not connected", index)
	}
	require.GreaterOrEqual(t, decided.Load(), int64(2*connections), "each reconnect was decided afresh")
	// A connection the callout does not admit in time is refused like any other, and the server
	// logs that refusal as "authentication error" (nats-server server/client.go, authViolation);
	// its own "not received in time" is a debug line.
	log := server.Log()
	require.NotContains(t, log, "authentication error", "a reconnect was refused, the authorization timeout's refusal among them")
	require.NotContains(t, log, "not received in time")

	// The control: the same log, read the same way, names a refusal when one happens.
	_, err := connectSession(t, url, &session{id: "ses_unknown", key: sessions[0].key, token: "a-token-no-broker-issued"}, nil)
	require.Error(t, err)
	testwait.WithinPolling(t, 5*time.Second, 50*time.Millisecond, "the control's refusal in the server's log", func() bool {
		return strings.Contains(server.Log(), "authentication error")
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
	nc, err := nats.Connect(url, option, nats.Timeout(10*time.Second), nats.MaxReconnects(-1),
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
