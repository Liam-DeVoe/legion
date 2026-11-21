//go:build natslive

package natsgrant_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// grantedConnection connects to uri as the nkey user whose seed is seed, with options, and
// returns the connection with the asynchronous errors the server sends it, a refused publish's
// among them.
func grantedConnection(t *testing.T, uri, seed string, options ...natsgo.Option) (*natsgo.Conn, <-chan error) {
	t.Helper()
	errs := make(chan error, 16)
	conn, err := testnats.ConnectSeed(t, uri, seed, append(options, natsgo.ErrorHandler(func(_ *natsgo.Conn, _ *natsgo.Subscription, err error) {
		select {
		case errs <- err:
		default:
		}
	}))...)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	return conn, errs
}

// refusedNaming waits up to 10 s for the server's permissions violation naming what, a fragment of
// its text such as `Publish to "x"`, among errs, and fails the test without one.
func refusedNaming(t *testing.T, errs <-chan error, what string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-errs:
			if errors.Is(err, natsgo.ErrPermissionViolation) && strings.Contains(err.Error(), what) {
				return
			}
		case <-deadline:
			t.Fatalf("the server sent no permissions violation for %s within 10s", what)
		}
	}
}

// The edge's consumer calls reach past its publish list, and this row pins how far, on the hub's
// image: a narrowing of that reach fails it, and so does a widening of the publish list it reads
// against. On a stream with the production subjects, which a shared listener's user owns:
//   - a direct publish of the edge's on another project's role subject is refused, and a push
//     consumer the edge creates over a message it stored delivers that message on the same subject;
//   - a consumer the edge creates on another project's Dispatch event, which the shared user
//     stored, delivers that event to the edge's own inbox;
//   - a push consumer whose deliver subject the stream carries is refused as a cycle;
//   - the edge updates another listener's durable to deliver to its own inbox, and deletes it;
//   - a subscription to the relay subjects under deliver.> is admitted and delivered a subject of
//     two tokens past the prefix, where a subscription to that subject itself is refused.
func TestTheEdgeGrantsConsumerCallsReachPastItsPublishList(t *testing.T) {
	edgeSeed, edgeKey := testnats.User(t)
	sharedSeed, sharedKey := testnats.User(t)
	readerSeed, readerKey := testnats.User(t)
	edge, shared := edge(t, "m", "PRIV"), natsgrant.Listener("shared")
	_, uri := testnats.StartImageConfig(t, testnats.HubImage, testnats.GrantedUsersConfig(
		testnats.StaticUser{NKey: edgeKey, Publish: edge.Publish, Subscribe: edge.Subscribe},
		testnats.StaticUser{NKey: sharedKey, Publish: shared.Publish, Subscribe: shared.Subscribe},
		testnats.StaticUser{NKey: readerKey, Publish: []string{"example.unused"}, Subscribe: []string{">"}},
	), []string{"-js"}, false)
	owner, err := bus.ConnectOwningStream([]string{uri}, bus.WithName("shared"), bus.WithEnvironment(func(name string) (string, bool) {
		if name == bus.NkeySeedEnvVar {
			return sharedSeed, true
		}
		return "", false
	}))
	require.NoError(t, err, "the shared listener's user creates the stream")
	t.Cleanup(owner.Close)
	const durable = "listener-shared"
	_, err = owner.JS().AddConsumer(bus.Stream, &natsgo.ConsumerConfig{
		Durable: durable, DeliverSubject: owner.Conn.NewInbox(), FilterSubjects: []string{"notifications.dispatch.>"}, AckPolicy: natsgo.AckExplicitPolicy,
	})
	require.NoError(t, err, "the shared listener's user creates its durable")
	reader, _ := grantedConnection(t, uri, readerSeed)
	edgeConn, edgeErrs := grantedConnection(t, uri, edgeSeed, natsgo.CustomInboxPrefix(natsgrant.EdgeInboxPrefix("m")))
	edgeJS, err := edgeConn.JetStream()
	require.NoError(t, err)

	t.Run("a push consumer delivers on a subject the grant does not publish", func(t *testing.T) {
		const target, own = "notifications.role.INFRA.x", "notifications.agent.ses_edge_own"
		received, err := reader.SubscribeSync(target)
		require.NoError(t, err)
		require.NoError(t, reader.Flush())
		require.NoError(t, edgeConn.Publish(target, []byte("published directly")))
		require.NoError(t, edgeConn.Flush())
		refusedNaming(t, edgeErrs, `Publish to "`+target+`"`)
		_, err = edgeJS.Publish(own, []byte("stored by the edge"))
		require.NoError(t, err, "the edge stores a message on its own agent subject")
		_, err = edgeJS.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{
			Name: "edge-reach", FilterSubject: own, DeliverSubject: target, AckPolicy: natsgo.AckNonePolicy,
		})
		require.NoError(t, err, "the edge creates a push consumer delivering on %s", target)
		msg, err := received.NextMsg(10 * time.Second)
		require.NoError(t, err, "the edge's push consumer delivered nothing on %s", target)
		require.Equal(t, "stored by the edge", string(msg.Data), "the reader received the edge's direct publish, which the server refused")
	})
	t.Run("it reads another project's stored Dispatch event", func(t *testing.T) {
		const event = "notifications.dispatch.issue.INFRA.INFRA-1.comment.created"
		_, err := owner.JS().Publish(event, []byte("another project's event"))
		require.NoError(t, err, "the shared user stores another project's Dispatch event")
		inbox := natsgrant.EdgeInboxPrefix("m") + ".read"
		read, err := edgeConn.SubscribeSync(inbox)
		require.NoError(t, err)
		require.NoError(t, edgeConn.Flush())
		_, err = edgeJS.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{
			Name: "edge-read", FilterSubject: event, DeliverSubject: inbox, AckPolicy: natsgo.AckNonePolicy,
		})
		require.NoError(t, err, "the edge creates a consumer on %s", event)
		msg, err := read.NextMsg(10 * time.Second)
		require.NoError(t, err, "the edge's consumer on %s delivered nothing to its inbox", event)
		require.Equal(t, "another project's event", string(msg.Data))
	})
	t.Run("a deliver subject the stream carries is refused as a cycle", func(t *testing.T) {
		_, err := edgeJS.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{
			Name: "edge-cycle", FilterSubject: "notifications.agent.ses_edge_own", DeliverSubject: "notifications.dispatch.issue.INFRA.INFRA-1.comment.created", AckPolicy: natsgo.AckNonePolicy,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "cycle")
	})
	t.Run("it updates and deletes another listener's durable", func(t *testing.T) {
		stolen := natsgrant.EdgeInboxPrefix("m") + ".stolen"
		_, err := edgeJS.UpdateConsumer(bus.Stream, &natsgo.ConsumerConfig{
			Durable: durable, DeliverSubject: stolen, FilterSubjects: []string{"notifications.dispatch.>"}, AckPolicy: natsgo.AckExplicitPolicy,
		})
		require.NoError(t, err, "the edge updates %s", durable)
		info, err := owner.JS().ConsumerInfo(bus.Stream, durable)
		require.NoError(t, err)
		require.Equal(t, stolen, info.Config.DeliverSubject, "%s delivers to the edge's inbox", durable)
		require.NoError(t, edgeJS.DeleteConsumer(bus.Stream, durable), "the edge deletes %s", durable)
		_, err = owner.JS().ConsumerInfo(bus.Stream, durable)
		require.ErrorIs(t, err, natsgo.ErrConsumerNotFound, "%s is still there", durable)
	})
	t.Run("a subscription under deliver.> is admitted and gets two tokens past the prefix", func(t *testing.T) {
		const twoTokens = "notifications.envoy.deliver.x.y"
		wide, err := edgeConn.SubscribeSync(bus.DeliverSubjectPrefix + ">")
		require.NoError(t, err)
		_, err = edgeConn.SubscribeSync(twoTokens)
		require.NoError(t, err)
		require.NoError(t, edgeConn.Flush())
		refusedNaming(t, edgeErrs, `Subscription to "`+twoTokens+`"`)
		require.NoError(t, owner.Conn.Publish(twoTokens, []byte("two tokens")))
		require.NoError(t, owner.Conn.Flush())
		msg, err := wide.NextMsg(10 * time.Second)
		require.NoError(t, err, "the edge's subscription under deliver.> was delivered nothing")
		require.Equal(t, twoTokens, msg.Subject)
	})
}

// One edge machine can neither publish nor read another machine's relay receipts
// (natsgrant.EdgeRelayReplyPrefix): on the hub's image, edge-a's publish on edge-b's receipt
// family and its subscription to that family are each refused naming the subject. The control is
// in the same run: each machine publishes on its own family and reads what it published, edge-b
// first, so a forged receipt of edge-a's that the server let through would be the first edge-b
// reads.
func TestAnEdgeMachineCannotReadOrPublishAnotherMachinesReceipts(t *testing.T) {
	aSeed, aKey := testnats.User(t)
	bSeed, bKey := testnats.User(t)
	a, b := edge(t, "edge-a", "PRIV"), edge(t, "edge-b", "PRIV")
	_, uri := testnats.StartImageConfig(t, testnats.HubImage, testnats.GrantedUsersConfig(
		testnats.StaticUser{NKey: aKey, Publish: a.Publish, Subscribe: a.Subscribe},
		testnats.StaticUser{NKey: bKey, Publish: b.Publish, Subscribe: b.Subscribe},
	), []string{"-js"}, false)
	aConn, aErrs := grantedConnection(t, uri, aSeed)
	bConn, _ := grantedConnection(t, uri, bSeed)
	familyOf := func(machine string) string { return natsgrant.EdgeRelayReplyPrefix(machine) + ".>" }
	receiptOf := func(machine string) string { return natsgrant.EdgeRelayReplyPrefix(machine) + ".t1" }
	ofB, err := bConn.SubscribeSync(familyOf("edge-b"))
	require.NoError(t, err)
	ofA, err := aConn.SubscribeSync(familyOf("edge-a"))
	require.NoError(t, err)
	require.NoError(t, bConn.Flush())
	require.NoError(t, aConn.Flush())

	require.NoError(t, aConn.Publish(receiptOf("edge-b"), []byte("forged by edge-a")))
	require.NoError(t, aConn.Flush())
	refusedNaming(t, aErrs, `Publish to "`+receiptOf("edge-b")+`"`)
	_, err = aConn.SubscribeSync(familyOf("edge-b"))
	require.NoError(t, err)
	require.NoError(t, aConn.Flush())
	refusedNaming(t, aErrs, `Subscription to "`+familyOf("edge-b")+`"`)

	for _, own := range []struct {
		conn     *natsgo.Conn
		sub      *natsgo.Subscription
		machine  string
		received string
	}{{bConn, ofB, "edge-b", "from edge-b"}, {aConn, ofA, "edge-a", "from edge-a"}} {
		require.NoError(t, own.conn.Publish(receiptOf(own.machine), []byte(own.received)))
		require.NoError(t, own.conn.Flush())
		msg, err := own.sub.NextMsg(10 * time.Second)
		require.NoError(t, err, "%s did not read its own receipt", own.machine)
		require.Equal(t, own.received, string(msg.Data), "%s read a receipt it did not publish first", own.machine)
	}
}

// accountUserEntry is one nkey user of an account in a server configuration, held to its allow
// lists, as a hub renders a static user.
func accountUserEntry(user testnats.StaticUser) string {
	quoted := func(entries []string) string {
		out := make([]string, len(entries))
		for i, entry := range entries {
			out[i] = strconv.Quote(entry)
		}
		return strings.Join(out, ", ")
	}
	return fmt.Sprintf("{ nkey: %q, permissions: { publish: { allow: [%s] }, subscribe: { allow: [%s] } } }", user.NKey, quoted(user.Publish), quoted(user.Subscribe))
}

// twoAccountsConfig is a server configuration of two JetStream accounts, A and B, holding the
// users given, and no other client.
func twoAccountsConfig(a, b []testnats.StaticUser) string {
	entries := func(users []testnats.StaticUser) string {
		out := make([]string, len(users))
		for i, user := range users {
			out[i] = accountUserEntry(user)
		}
		return strings.Join(out, ",\n      ")
	}
	return fmt.Sprintf("jetstream {}\naccounts {\n  A: {\n    jetstream: enabled\n    users: [\n      %s\n    ]\n  }\n  B: {\n    jetstream: enabled\n    users: [\n      %s\n    ]\n  }\n}\n", entries(a), entries(b))
}

// A push consumer's deliver subject is checked against no permission, and the stream delivers
// through its account's own JetStream client, so the edge can aim a push consumer at a subject
// under $JS.API. or $KV. or at another stream's subjects. This row pins, on the hub's image
// (nats-server 2.15.1), what such a consumer of the edge's does to a scratch stream or bucket of
// its account, over a message the edge stored on its own agent subject:
//   - a deliver subject no client subscribes gives the consumer no interest: it is not push bound
//     and delivers nothing, the JetStream API's import and a stream's capture being no interest;
//   - with a client also subscribed to it, the consumer delivers, and still a stream delete,
//     purge or update and a bucket's stream delete leave the stream as it was, and a $KV write
//     leaves no entry: the API does not act on a delivery;
//   - with a client subscribed to a subject another stream of the account captures, that stream
//     stores the edge's message: the one write this route has.
//
// Controls in the same run: a subscribed subject receives the delivery; the edge's direct request
// of a stream delete is refused and the stream stays; the account's administrator deleting a
// stream is seen as deleted; and a stream of the same name in another account stays.
func TestTheEdgeGrantsPushConsumerReachesItsAccountsJetStreamAPI(t *testing.T) {
	edgeSeed, edgeKey := testnats.User(t)
	sharedSeed, sharedKey := testnats.User(t)
	adminSeed, adminKey := testnats.User(t)
	otherSeed, otherKey := testnats.User(t)
	edge, shared := edge(t, "m", "PRIV"), natsgrant.Listener("shared")
	everything := []string{">"}
	_, uri := testnats.StartImageConfig(t, testnats.HubImage, twoAccountsConfig(
		[]testnats.StaticUser{
			{NKey: edgeKey, Publish: edge.Publish, Subscribe: edge.Subscribe},
			{NKey: sharedKey, Publish: shared.Publish, Subscribe: shared.Subscribe},
			{NKey: adminKey, Publish: everything, Subscribe: everything},
		},
		[]testnats.StaticUser{{NKey: otherKey, Publish: everything, Subscribe: everything}},
	), []string{"-js"}, false)
	owner, err := bus.ConnectOwningStream([]string{uri}, bus.WithName("shared"), bus.WithEnvironment(func(name string) (string, bool) {
		if name == bus.NkeySeedEnvVar {
			return sharedSeed, true
		}
		return "", false
	}))
	require.NoError(t, err, "the shared listener's user creates the stream")
	t.Cleanup(owner.Close)
	adminConn, _ := grantedConnection(t, uri, adminSeed)
	admin, err := adminConn.JetStream()
	require.NoError(t, err)
	otherConn, _ := grantedConnection(t, uri, otherSeed)
	other, err := otherConn.JetStream()
	require.NoError(t, err)
	scratch := func(js natsgo.JetStreamContext, name string) {
		t.Helper()
		_, err := js.AddStream(&natsgo.StreamConfig{Name: name, Subjects: []string{"scratch." + strings.ToLower(name) + ".>"}, Storage: natsgo.MemoryStorage})
		require.NoError(t, err, "make the scratch stream %s", name)
		_, err = js.Publish("scratch."+strings.ToLower(name)+".m", []byte("kept"))
		require.NoError(t, err)
	}
	for _, name := range []string{"SCRATCH_DELETE", "SCRATCH_DELETE_SUB", "SCRATCH_PURGE", "SCRATCH_UPDATE", "SCRATCH_CAPTURE", "SCRATCH_CAPTURE_SUB", "SCRATCH_CONTROL", "SCRATCH_ADMIN"} {
		scratch(admin, name)
	}
	scratch(other, "SCRATCH_DELETE")
	_, err = admin.CreateKeyValue(&natsgo.KeyValueConfig{Bucket: "scratch_kv_delete", Storage: natsgo.MemoryStorage})
	require.NoError(t, err)
	written, err := admin.CreateKeyValue(&natsgo.KeyValueConfig{Bucket: "scratch_kv_write", Storage: natsgo.MemoryStorage})
	require.NoError(t, err)
	update, err := admin.StreamInfo("SCRATCH_UPDATE")
	require.NoError(t, err)
	changed := update.Config
	changed.MaxMsgs = 7
	updateBody, err := json.Marshal(changed)
	require.NoError(t, err)
	streamState := func(name string) string {
		info, err := admin.StreamInfo(name)
		switch {
		case errors.Is(err, natsgo.ErrStreamNotFound):
			return "deleted"
		case err != nil:
			return "unread: " + err.Error()
		}
		return fmt.Sprintf("kept, %d messages, max_msgs %d", info.State.Msgs, info.Config.MaxMsgs)
	}
	var received atomic.Int64
	const kept, keptEmpty = "kept, 1 messages, max_msgs -1", "kept, 0 messages, max_msgs -1"
	edgeConn, edgeErrs := grantedConnection(t, uri, edgeSeed, natsgo.CustomInboxPrefix(natsgrant.EdgeInboxPrefix("m")))
	edgeJS, err := edgeConn.JetStream()
	require.NoError(t, err)

	for i, delivery := range []struct {
		target, payload string
		// subscribed: a client subscribes the deliver subject, which gives the consumer interest.
		subscribed bool
		state      func() string
		want       string
	}{
		{target: "$JS.API.STREAM.DELETE.SCRATCH_DELETE", state: func() string { return streamState("SCRATCH_DELETE") }, want: kept},
		{target: "scratch.scratch_capture.m", payload: "captured", state: func() string { return streamState("SCRATCH_CAPTURE") }, want: kept},
		{target: "$JS.API.STREAM.DELETE.SCRATCH_DELETE_SUB", subscribed: true, state: func() string { return streamState("SCRATCH_DELETE_SUB") }, want: kept},
		{target: "$JS.API.STREAM.PURGE.SCRATCH_PURGE", payload: "{}", subscribed: true, state: func() string { return streamState("SCRATCH_PURGE") }, want: kept},
		{target: "$JS.API.STREAM.UPDATE.SCRATCH_UPDATE", payload: string(updateBody), subscribed: true, state: func() string { return streamState("SCRATCH_UPDATE") }, want: kept},
		{target: "$JS.API.STREAM.DELETE.KV_scratch_kv_delete", subscribed: true, state: func() string { return streamState("KV_scratch_kv_delete") }, want: keptEmpty},
		{target: "$KV.scratch_kv_write.k1", payload: "written by the edge", subscribed: true, state: func() string {
			if entry, err := written.Get("k1"); err == nil {
				return fmt.Sprintf("entry %q", entry.Value())
			}
			return "no entry"
		}, want: "no entry"},
		{target: "scratch.scratch_capture_sub.m", payload: "captured", subscribed: true, state: func() string { return streamState("SCRATCH_CAPTURE_SUB") }, want: "kept, 2 messages, max_msgs -1"},
		{target: "scratch.subscribed", payload: "to a subscriber", subscribed: true, state: func() string { return fmt.Sprintf("%d received", received.Load()) }, want: "1 received"},
	} {
		t.Run(delivery.target, func(t *testing.T) {
			if delivery.subscribed {
				sub, err := adminConn.Subscribe(delivery.target, func(*natsgo.Msg) {
					if delivery.target == "scratch.subscribed" {
						received.Add(1)
					}
				})
				require.NoError(t, err)
				t.Cleanup(func() { _ = sub.Unsubscribe() })
				require.NoError(t, adminConn.Flush())
			}
			before := delivery.state()
			own := fmt.Sprintf("notifications.agent.ses_edge_api_%d", i)
			_, err := edgeJS.Publish(own, []byte(delivery.payload))
			require.NoError(t, err, "the edge stores the message on its own agent subject")
			name := fmt.Sprintf("edge-api-%d", i)
			_, err = edgeJS.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{Name: name, FilterSubject: own, DeliverSubject: delivery.target, AckPolicy: natsgo.AckNonePolicy})
			require.NoError(t, err, "the edge creates a push consumer delivering on %s", delivery.target)
			if !delivery.subscribed {
				info, err := admin.ConsumerInfo(bus.Stream, name)
				require.NoError(t, err)
				require.False(t, info.PushBound, "the consumer on %s has interest no client gave it", delivery.target)
				require.Zero(t, info.Delivered.Consumer, "the consumer delivered on %s with no interest", delivery.target)
				require.Equal(t, delivery.want, delivery.state())
				return
			}
			delivered := false
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !delivered; time.Sleep(50 * time.Millisecond) {
				info, err := admin.ConsumerInfo(bus.Stream, name)
				delivered = err == nil && info.Delivered.Consumer >= 1
			}
			require.True(t, delivered, "the consumer on %s delivered nothing within 10s, though a client subscribes it", delivery.target)
			// A change lands within the bound (the capture case is seen changed inside it); one
			// judged unchanged has had the whole bound to land.
			after := delivery.state()
			for deadline := time.Now().Add(2 * time.Second); after == before && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				after = delivery.state()
			}
			require.Equal(t, delivery.want, after, "%s after the edge's delivery (before: %s)", delivery.target, before)
		})
	}

	t.Run("controls", func(t *testing.T) {
		_, err := edgeConn.Request("$JS.API.STREAM.DELETE.SCRATCH_CONTROL", nil, 2*time.Second)
		require.Error(t, err, "the edge's direct request of a stream delete was answered")
		refusedNaming(t, edgeErrs, `Publish to "$JS.API.STREAM.DELETE.SCRATCH_CONTROL"`)
		require.Equal(t, kept, streamState("SCRATCH_CONTROL"), "the edge's direct request changed SCRATCH_CONTROL")
		require.NoError(t, admin.DeleteStream("SCRATCH_ADMIN"))
		require.Equal(t, "deleted", streamState("SCRATCH_ADMIN"), "the probe does not see a stream the administrator deleted")
		info, err := other.StreamInfo("SCRATCH_DELETE")
		require.NoError(t, err, "account B's SCRATCH_DELETE is gone")
		require.Equal(t, uint64(1), info.State.Msgs, "account B's SCRATCH_DELETE changed")
	})
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
	edge, listener := edge(t, "m", "PRIV"), natsgrant.Listener("example-hub")
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
// notification topics: the agent subjects, project PRIV's role subjects, the relay subjects,
// machine m's relay receipts, the resume lane, and the exception lanes of the agent subjects and of
// PRIV's role subjects.
var edgeOwnLanes = []string{
	"notifications.agent.x",
	"notifications.role.PRIV.x",
	"notifications.envoy.deliver.x",
	"notifications.envoy.relayreply.m.x",
	"notifications.envoy.resume.x",
	"notifications.envoy.exceptions.notifications.agent.x",
	"notifications.envoy.exceptions.notifications.role.PRIV.x",
}

// edgeRefusedLanes are notification topics the edge's grant does not publish: the kept role lane
// and its drain cursors, a role of another project, a role in the form that names no project, the
// Legion wake topic, a Slack topic, the exception lane of a GitHub topic, and another machine's
// relay receipts.
var edgeRefusedLanes = []string{
	"notifications.envoy.kept.role.x",
	"notifications.envoy.kept.cursor.x",
	"notifications.role.INFRA.x",
	"notifications.role.legion-infra-controller",
	"notifications.legion.wake",
	"notifications.slack.x",
	"notifications.envoy.exceptions.notifications.github.acme.pub.pr.1.opened",
	"notifications.envoy.relayreply.other.x",
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
	edge, listener := edge(t, "m", "PRIV"), natsgrant.Listener("m")
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
