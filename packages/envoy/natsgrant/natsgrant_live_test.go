//go:build natslive

package natsgrant_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// grantedConnection connects to uri as the nkey user whose seed is seed and returns the
// connection with the asynchronous errors the server sends it, a refused publish's among them.
func grantedConnection(t *testing.T, uri, seed string) (*natsgo.Conn, <-chan error) {
	t.Helper()
	errs := make(chan error, 16)
	conn, err := testnats.ConnectSeed(t, uri, seed, natsgo.ErrorHandler(func(_ *natsgo.Conn, _ *natsgo.Subscription, err error) {
		select {
		case errs <- err:
		default:
		}
	}))
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	return conn, errs
}

// A publish of a Dispatch event on the edge listener's connection is refused by the server: the
// event never reaches a subscriber, the connection is told of the violation naming the subject,
// and the server logs it. The same publish under the listener's grant is accepted and reaches the
// subscriber, which shows the subscriber would have seen the edge's had the server let it through.
func TestTheEdgeCannotPublishADispatchEvent(t *testing.T) {
	const subject = "notifications.dispatch.issue.PRIV.PRIV-1.comment.created"
	edgeSeed, edgeKey := testnats.User(t)
	listenerSeed, listenerKey := testnats.User(t)
	readerSeed, readerKey := testnats.User(t)
	edge, listener := natsgrant.Edge("example-edge-a"), natsgrant.Listener("example-hub")
	server, uri := testnats.StartImageConfig(t, testnats.HubImage, testnats.GrantedUsersConfig(
		testnats.StaticUser{NKey: edgeKey, Publish: edge.Publish, Subscribe: edge.Subscribe},
		testnats.StaticUser{NKey: listenerKey, Publish: listener.Publish, Subscribe: listener.Subscribe},
		testnats.StaticUser{NKey: readerKey, Publish: []string{"example.unused"}, Subscribe: []string{"notifications.dispatch.>"}},
	), []string{"-js"}, false)

	reader, _ := grantedConnection(t, uri, readerSeed)
	events, err := reader.SubscribeSync(subject)
	require.NoError(t, err)
	require.NoError(t, reader.Flush())

	accepted, acceptedErrs := grantedConnection(t, uri, listenerSeed)
	require.NoError(t, accepted.Publish(subject, []byte("from the listener")))
	require.NoError(t, accepted.Flush())
	msg, err := events.NextMsg(5 * time.Second)
	require.NoError(t, err, "the listener's publish of %s did not reach the subscriber", subject)
	require.Equal(t, "from the listener", string(msg.Data))
	select {
	case err := <-acceptedErrs:
		t.Fatalf("the server refused the listener's publish of %s: %v", subject, err)
	default:
	}

	refused, refusedErrs := grantedConnection(t, uri, edgeSeed)
	require.NoError(t, refused.Publish(subject, []byte("from the edge")))
	require.NoError(t, refused.Flush())
	select {
	case err := <-refusedErrs:
		require.True(t, errors.Is(err, natsgo.ErrPermissionViolation), "the edge's connection was told %v, want a permissions violation", err)
		require.Contains(t, err.Error(), `Publish to "`+subject+`"`)
	case <-time.After(5 * time.Second):
		t.Fatalf("the server did not refuse the edge's publish of %s within 5s", subject)
	}
	if msg, err := events.NextMsg(time.Second); err == nil {
		t.Fatalf("the edge's publish reached the subscriber: %q", msg.Data)
	}
	logs, err := server.Logs(context.Background())
	require.NoError(t, err)
	defer logs.Close()
	logged, err := io.ReadAll(logs)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(logged), `Publish Violation`) && strings.Contains(string(logged), `Subject "`+subject+`"`),
		"the server logged no publish violation naming %s:\n%s", subject, logged)
}
