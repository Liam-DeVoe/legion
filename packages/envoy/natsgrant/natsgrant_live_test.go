//go:build natslive

package natsgrant_test

import (
	"context"
	"errors"
	"io"
	"slices"
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
	edge, listener := natsgrant.Edge("m", []string{"PRIV"}), natsgrant.Listener("example-hub")
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

// edgeOwnLanes is one subject of each lane natsgrant.Edge("m", ["PRIV"]) publishes among the
// notification topics: the agent subjects, project PRIV's role subjects, the relay subjects, the
// relay receipts, the resume lane, and the exception lanes of the agent subjects and of PRIV's role
// subjects.
var edgeOwnLanes = []string{
	"notifications.agent.x",
	"notifications.role.PRIV.x",
	"notifications.envoy.deliver.x",
	"notifications.envoy.relayreply.x",
	"notifications.envoy.resume.x",
	"notifications.envoy.exceptions.notifications.agent.x",
	"notifications.envoy.exceptions.notifications.role.PRIV.x",
}

// edgeRefusedLanes are notification topics the edge's grant does not publish: the kept role lane
// and its drain cursors, a role of another project, a role in the form that names no project, the
// Legion wake topic, a Slack topic, and the exception lane of a GitHub topic.
var edgeRefusedLanes = []string{
	"notifications.envoy.kept.role.x",
	"notifications.envoy.kept.cursor.x",
	"notifications.role.INFRA.x",
	"notifications.role.legion-infra-controller",
	"notifications.legion.wake",
	"notifications.slack.x",
	"notifications.envoy.exceptions.notifications.github.acme.pub.pr.1.opened",
}

// publishOutcome publishes once on each of subjects, in order, as the nkey user whose seed is
// seed, and then once on a sentinel, and returns the subjects reader received before the sentinel
// and the subjects the server refused naming them. A connection's messages reach a subscriber in
// the order they were published, so a publish the reader has not received by the time the
// sentinel arrives was not accepted; each such subject must then be named by a permissions
// violation, and one that is neither received nor refused fails the test naming it.
func publishOutcome(t *testing.T, uri, seed string, reader *natsgo.Subscription, subjects []string) (accepted, refused []string) {
	t.Helper()
	const sentinel = "notifications.agent.sentinel"
	conn, errs := grantedConnection(t, uri, seed)
	for _, subject := range append(slices.Clone(subjects), sentinel) {
		require.NoError(t, conn.Publish(subject, []byte(subject)))
	}
	require.NoError(t, conn.Flush())
	for {
		msg, err := reader.NextMsg(10 * time.Second)
		require.NoError(t, err, "the sentinel %s did not reach the reader within 10s, so no publish could be read as accepted", sentinel)
		if msg.Subject == sentinel {
			break
		}
		accepted = append(accepted, msg.Subject)
	}
	pending := map[string]bool{}
	for _, subject := range subjects {
		if !slices.Contains(accepted, subject) {
			pending[subject] = true
		}
	}
	deadline := time.After(10 * time.Second)
	for len(pending) > 0 {
		select {
		case err := <-errs:
			_, quoted, found := strings.Cut(err.Error(), `Publish to "`)
			subject, _, closed := strings.Cut(quoted, `"`)
			if errors.Is(err, natsgo.ErrPermissionViolation) && found && closed && pending[subject] {
				delete(pending, subject)
				refused = append(refused, subject)
			}
		case <-deadline:
			t.Fatalf("the server neither delivered nor refused these publishes within 10s: %v", pending)
		}
	}
	return accepted, refused
}

// An edge listener's user, rendered natsgrant.Edge("m", ["PRIV"]) on the hub's image, publishes
// each of its own lanes and is refused every other notification topic named here, each refusal
// naming its subject. The same publishes as a listener's user (natsgrant.Listener) are each
// accepted and received, which shows the reader sees an accepted publish on every one of these
// subjects: a refusal of the edge's is its grant's, not a subject the reader cannot see.
func TestTheEdgeGrantPublishesOnlyItsOwnLanes(t *testing.T) {
	edgeSeed, edgeKey := testnats.User(t)
	listenerSeed, listenerKey := testnats.User(t)
	readerSeed, readerKey := testnats.User(t)
	edge, listener := natsgrant.Edge("m", []string{"PRIV"}), natsgrant.Listener("m")
	_, uri := testnats.StartImageConfig(t, testnats.HubImage, testnats.GrantedUsersConfig(
		testnats.StaticUser{NKey: edgeKey, Publish: edge.Publish, Subscribe: edge.Subscribe},
		testnats.StaticUser{NKey: listenerKey, Publish: listener.Publish, Subscribe: listener.Subscribe},
		testnats.StaticUser{NKey: readerKey, Publish: []string{"example.unused"}, Subscribe: []string{"notifications.>"}},
	), []string{"-js"}, false)
	reader, _ := grantedConnection(t, uri, readerSeed)
	received, err := reader.SubscribeSync("notifications.>")
	require.NoError(t, err)
	require.NoError(t, reader.Flush())
	subjects := slices.Concat(edgeOwnLanes, edgeRefusedLanes)

	accepted, refused := publishOutcome(t, uri, listenerSeed, received, subjects)
	require.Empty(t, refused, "the listener's grant was refused publishes, so the reader cannot show an accepted one")
	require.Equal(t, subjects, accepted, "the reader did not receive every publish the listener's grant accepted")

	accepted, refused = publishOutcome(t, uri, edgeSeed, received, subjects)
	var wrongly []string
	for _, subject := range edgeRefusedLanes {
		if slices.Contains(accepted, subject) {
			wrongly = append(wrongly, subject)
		}
	}
	if len(wrongly) > 0 {
		t.Errorf("the edge's grant accepted the publishes on %s, which it must refuse", strings.Join(wrongly, ", "))
	}
	var lost []string
	for _, subject := range edgeOwnLanes {
		if slices.Contains(refused, subject) {
			lost = append(lost, subject)
		}
	}
	if len(lost) > 0 {
		t.Errorf("the edge's grant refused the publishes on %s, its own lanes", strings.Join(lost, ", "))
	}
	require.Equal(t, edgeOwnLanes, accepted, "the edge's accepted publishes, in order")
}
