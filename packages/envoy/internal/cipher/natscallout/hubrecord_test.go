package natscallout

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// hubRefusals is the subject a callout server publishes the auth error event of each connection it
// refuses on, in the callout's account (nats-server server/events.go:65, authErrorAccountEventSubj,
// sent for a connection the callout decides at server/auth.go:784).
const hubRefusals = "$SYS.ACCOUNT.CLIENT.AUTH.ERR"

// hubRecordBuffer is how many requests, and how many refusals, a hubRecord holds unread: far more
// than the requests and refusals of the largest storm here, a few thousand.
const hubRecordBuffer = 1 << 16

// hubRecord reads a callout server's own record of what became of the authorization requests it
// sends, as a static user of the server's configuration (newHubRecordUser), which the server asks
// no service about. The server writes two things about a request: the request itself, which names
// the connection it is for (its client ID, the cid of the server's log) and the nonce it sent that
// connection; and, when it refuses that connection, an auth error event naming the connection and
// the callout's reason.
//
// The reason is what tells the server's give-up from every other refusal, and the server's log
// cannot: it logs its give-up (hubGaveUp) naming no connection, and every refusal with the same
// "authentication error" line. The callout ends with no reason only when it read no answer inside
// its authorization timeout (server/auth_callout.go:457-466); every other way out of it names one,
// an answer that refuses, one the server cannot open or will not take, or a request it could not
// send, and the server sends that string as the event's reason (server/auth.go:762-785). So a
// refusal event with no reason is a request the server gave up on.
//
// The server queues both, the request and the event, on its one queue of internal messages, which
// one goroutine sends in order (server/events.go:713-751, internalSendLoop :499-673). So read
// reads the record up to an event of its own making: once that has arrived, every request and
// every refusal the server queued before it has arrived too.
type hubRecord struct {
	url      string
	public   string
	xkey     nkeys.KeyPair
	conn     *nats.Conn
	requests *nats.Subscription
	refusals *nats.Subscription
	sent     chan *nats.Msg
	refused  chan *nats.Msg
	marks    int
}

// newHubRecordUser is the static user a hubRecord reads as, and its seed: an nkey user the server's
// configuration lists, which may read the requests the server sends and its auth error events.
func newHubRecordUser(t *testing.T) (testnats.StaticUser, string) {
	t.Helper()
	seed, public := testnats.User(t)
	return testnats.StaticUser{NKey: public, Publish: []string{"_INBOX.>"}, Subscribe: []string{authSubject, hubRefusals}}, seed
}

// recordHub reads the record of the server at url as the static user whose seed is seed, from the
// moment it returns, across a restart of the server: it reconnects as a bus client does, and sends
// its subscriptions again (awaitResubscribed). keys' curve key opens the requests the server seals
// to the service.
func recordHub(t *testing.T, url, seed string, keys testnats.CalloutKeys) *hubRecord {
	t.Helper()
	user, err := nkeys.FromSeed([]byte(seed))
	require.NoError(t, err)
	public, err := user.PublicKey()
	require.NoError(t, err)
	xkey, err := nkeys.FromSeed([]byte(keys.XKeySeed))
	require.NoError(t, err)
	r := &hubRecord{url: url, public: public, xkey: xkey,
		sent: make(chan *nats.Msg, hubRecordBuffer), refused: make(chan *nats.Msg, hubRecordBuffer)}
	r.conn, err = nats.Connect(url, nats.Nkey(public, user.Sign), nats.MaxReconnects(-1),
		nats.ReconnectWait(100*time.Millisecond), nats.Timeout(10*time.Second))
	require.NoError(t, err, "the record's user is a static auth user, admitted without a callout")
	t.Cleanup(r.conn.Close)
	r.requests, err = r.conn.ChanSubscribe(authSubject, r.sent)
	require.NoError(t, err)
	r.refusals, err = r.conn.ChanSubscribe(hubRefusals, r.refused)
	require.NoError(t, err)
	require.NoError(t, r.conn.Flush())
	return r
}

// awaitResubscribed waits until the record has reconnected to a restarted server and its
// subscriptions have reached it: nats.go sends them again as it reconnects, before anything the
// flush queues behind them.
func (r *hubRecord) awaitResubscribed(t *testing.T) {
	t.Helper()
	testwait.UntilPolling(t, 50*time.Millisecond, "the server's record read again after the restart", func() (uint64, bool) {
		reconnects := r.conn.Stats().Reconnects
		return reconnects, reconnects > 0 && r.conn.FlushTimeout(time.Second) == nil
	})
}

// hubConnection is one connection of one server: a server numbers its connections from one, and a
// restarted server, which comes back under a new ID, numbers them afresh.
type hubConnection struct {
	server string
	id     uint64
}

// hubVerdicts is what the server's record says became of its requests: the connection it sent the
// request on each nonce for, and the reason it refused each connection it refused with.
type hubVerdicts struct {
	connection map[string]hubConnection
	refused    map[hubConnection]string
}

// read reads the server's record up to an auth error event of its own making: it connects as the
// record's own user signing with another key, under a name of its own, which the server refuses by
// itself, asking no service, and publishes an event for like any other.
func (r *hubRecord) read(t *testing.T) hubVerdicts {
	t.Helper()
	r.marks++
	mark := fmt.Sprintf("hub record mark %d", r.marks)
	stranger, err := nkeys.CreateUser()
	require.NoError(t, err)
	refused, err := nats.Connect(r.url, nats.Nkey(r.public, stranger.Sign), nats.Name(mark), nats.NoReconnect(),
		nats.Timeout(testwait.Bounded(t, time.Minute, time.Second)))
	if err == nil {
		refused.Close()
	}
	require.ErrorIs(t, err, nats.ErrAuthorization, "the server did not refuse the record's user signing with another key")

	verdicts := hubVerdicts{connection: map[string]hubConnection{}, refused: map[hubConnection]string{}}
	testwait.Until(t, "the server's auth error event for "+mark, func() (int, bool) {
		for {
			select {
			case msg := <-r.refused:
				var event natsserver.DisconnectEventMsg
				require.NoError(t, json.Unmarshal(msg.Data, &event), "an auth error event the server published")
				if event.Client.Name == mark {
					return len(verdicts.refused), true
				}
				verdicts.refused[hubConnection{event.Server.ID, event.Client.ID}] = event.Reason
			default:
				return len(verdicts.refused), false
			}
		}
	})
	for len(r.sent) > 0 {
		msg := <-r.sent
		data := msg.Data
		if serverXKey := msg.Header.Get(serverXKeyHeader); serverXKey != "" {
			data, err = r.xkey.Open(data, serverXKey)
			require.NoError(t, err, "a request the server sealed to the service's curve key")
		}
		claims, err := jwt.DecodeAuthorizationRequestClaims(string(data))
		require.NoError(t, err, "an authorization request the server sent")
		verdicts.connection[claims.ClientInformation.Nonce] = hubConnection{claims.Server.ID, claims.ClientInformation.ID}
	}
	for _, sub := range []*nats.Subscription{r.requests, r.refusals} {
		dropped, err := sub.Dropped()
		require.NoError(t, err)
		require.Zero(t, dropped, "the record dropped %d of the server's messages on %s", dropped, sub.Subject)
	}
	return verdicts
}

// gaveUpOn splits nonces into those whose request the server gave up on, by its own record, and
// every other, each with what the record says of it: that the server refused its connection for a
// reason, that it refused it not at all, so it took the request's answer, or that it holds no
// request on that nonce.
func (v hubVerdicts) gaveUpOn(nonces []string) (gaveUp, other []string) {
	for _, nonce := range nonces {
		connection, sent := v.connection[nonce]
		reason, refused := v.refused[connection]
		switch {
		case !sent:
			other = append(other, nonce+": the server's record holds no request on it")
		case !refused:
			other = append(other, fmt.Sprintf("%s (connection %d): the server took its answer and refused nothing on it", nonce, connection.id))
		case reason != "":
			other = append(other, fmt.Sprintf("%s (connection %d): the server refused it: %s", nonce, connection.id, reason))
		default:
			gaveUp = append(gaveUp, nonce)
		}
	}
	return gaveUp, other
}
