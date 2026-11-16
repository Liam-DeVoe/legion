package natscred_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
	"github.com/sjawhar/envoy/natscred"
)

// stubBroker answers every trade with a fresh DPoP-bound token, tok-1, tok-2 and so on.
func stubBroker(t *testing.T) (url string, trades *atomic.Int64) {
	t.Helper()
	trades = &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != brokerclient.TokenPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("tok-%d", trades.Add(1)), "token_type": "DPoP",
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "expires_in": 900,
		})
	}))
	t.Cleanup(server.Close)
	return server.URL, trades
}

func connect(t *testing.T, url string, options ...nats.Option) (*nats.Conn, error) {
	t.Helper()
	nc, err := nats.Connect(url, append([]nats.Option{nats.Timeout(10 * time.Second), nats.NoReconnect()}, options...)...)
	if err == nil {
		t.Cleanup(nc.Close)
	}
	return nc, err
}

// The session credential's proof is the one brokertoken checks: a session trading at its broker
// connects through the auth service and is admitted to its own channel. One token is taken per
// CONNECT, so a token that rotates between the signature and the token handler is not what the
// connection presents: the proof's 'ath' names the token sent, and the connection is admitted.
// The control reads the token again in its token handler, sends the rotated one beside a proof of
// the first, and is refused for the proof's 'ath'.
func TestConnectionProofVerifiesWithBrokertoken(t *testing.T) {
	key, err := proof.NewKey()
	require.NoError(t, err)
	tokens := func(token string) bool {
		return strings.HasPrefix(token, "tok-") || strings.HasPrefix(token, "rotated-") || strings.HasPrefix(token, "control-")
	}
	rig := newRig(t, liveFor(thumbprint(t, key), tokens), nil)

	t.Run("a session trading at its broker", func(t *testing.T) {
		brokerURL, trades := stubBroker(t)
		identity := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(identity, []byte("pod-token"), 0o600))
		client, err := brokerclient.New(brokerclient.Config{BrokerURL: brokerURL, Key: key, Subject: brokerclient.NewProjectedToken(identity), Session: "ses_a"})
		require.NoError(t, err)
		option, _ := natscred.Session(client)
		nc, err := connect(t, rig.url, option)
		require.NoError(t, err, "the session's credential was refused:\n%s", rig.log.String())
		_, err = nc.SubscribeSync(bus.DeliverSubject("ses_a"))
		require.NoError(t, err)
		require.NoError(t, nc.Flush())
		require.EqualValues(t, 1, trades.Load())
	})

	// rotating answers first, then second for every later read: the rotation lands between the
	// one read a CONNECT makes for its signature and anything read after it.
	rotating := func(first, second string) (func(context.Context) (string, error), *atomic.Int64) {
		reads := &atomic.Int64{}
		return func(context.Context) (string, error) {
			if reads.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		}, reads
	}
	t.Run("a token rotated between the signature and the token", func(t *testing.T) {
		current, reads := rotating("rotated-1", "rotated-2")
		option, _ := natscred.SessionFrom(key, current)
		_, err := connect(t, rig.url, option)
		require.NoError(t, err, "the snapshot's connection was refused:\n%s", rig.log.String())
		require.EqualValues(t, 1, reads.Load(), "the CONNECT read the token more than once")
		rotated, _ := current(t.Context())
		require.Equal(t, "rotated-2", rotated, "the token did rotate after the signature")
	})
	t.Run("control: a token handler that reads the token again", func(t *testing.T) {
		current, _ := rotating("control-1", "control-2")
		_, sign := natscred.SessionFrom(key, current)
		_, err := connect(t, rig.url,
			nats.UserJWT(func() (string, error) { return natscred.PlaceholderJWT, nil }, sign),
			nats.TokenHandler(func() string { token, _ := current(context.Background()); return token }))
		require.ErrorIs(t, err, nats.ErrAuthorization)
		require.Eventually(t, func() bool { return strings.Contains(rig.log.String(), `reason="proof invalid: ath"`) },
			5*time.Second, 50*time.Millisecond, "the service did not refuse the rotated token for its proof's 'ath':\n%s", rig.log.String())
	})
}

// A session's keeper reconnects on each token its renewal loop lands after the first: the loop
// trades on its first pass (brokerclient.Client.Run) just after the connection presented a live
// token, so the first is no renewal, and a reconnect on it would cut the connection at once. A
// later token the connection did not present is a renewal; one it did present is not. The control
// is the keeper without the skip of the first, which reconnects on it.
func TestSessionKeeperReconnectsOnEachTokenAfterTheFirst(t *testing.T) {
	_, url := testnats.Start(t)
	var reconnected atomic.Int64
	nc, err := nats.Connect(url, nats.Timeout(10*time.Second), nats.ReconnectWait(50*time.Millisecond),
		nats.ReconnectHandler(func(*nats.Conn) { reconnected.Add(1) }))
	require.NoError(t, err)
	defer nc.Close()

	// run lands each token the test sends it, as brokerclient.Client.Run lands each it trades.
	landed, handled := make(chan string), make(chan struct{})
	run := func(ctx context.Context, onToken func(context.Context, *brokerclient.Token) error) error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case token := <-landed:
				if err := onToken(ctx, &brokerclient.Token{AccessToken: token}); err != nil {
					return err
				}
				handled <- struct{}{}
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	kept := make(chan struct{})
	go func() {
		defer close(kept)
		natscred.SessionKeeper(run, "tok-presented").Keep(ctx, nc)
	}()
	land := func(token string) {
		t.Helper()
		select {
		case landed <- token:
		case <-kept:
			t.Fatalf("the keeper stopped before %s landed", token)
		}
		select {
		case <-handled:
		case <-kept:
			t.Fatalf("the keeper stopped on %s", token)
		}
	}

	land("tok-first")
	require.Never(t, func() bool { return reconnected.Load() > 0 }, 2*time.Second, 50*time.Millisecond,
		"the keeper reconnected on the first token its loop landed")
	land("tok-second")
	testwait.WithinPolling(t, 5*time.Second, 50*time.Millisecond, "the keeper's reconnect with the second token", func() bool {
		return reconnected.Load() == 1
	})
	land("tok-presented")
	require.Never(t, func() bool { return reconnected.Load() > 1 }, time.Second, 50*time.Millisecond,
		"the keeper reconnected on the token the connection presented")
}
