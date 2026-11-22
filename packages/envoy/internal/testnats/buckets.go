package testnats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testwait"
)

// A cluster member answers a stream lookup out of the JetStream metadata it has applied itself.
// nats-server 2.10.29 routes a KV create to the meta leader, which answers the client as soon as
// the bucket's stream is made, while every member applies the meta entry that carries the new
// assignment on its own clock; until a member has applied it, that member answers "stream not
// found" for the bucket that was just created, the member the create was sent through included.
// Measured here with three in-process members on two CPUs, 67 of 75 member reads taken the moment
// a create was answered still said so, the slowest for 206 ms.
//
// Whatever reaches the bucket next reaches it through whichever member its own connection is on:
// bus.EnsureKeyValue opens the jetstream handle its KeyValue carries, a rebuild calls
// bus.OpenKeyValue, a second program looks the bucket up before it raises it, and a first put
// writes to it. So a test that creates a bucket on a cluster and then has the code under test
// open it, raise it, look it up or write to it creates it with CreateKeyValue, and one whose
// code under test creates the bucket itself calls AwaitBucket after it, on the connection and
// the JetStream context that code holds.

// readWait bounds one read that a wait of this package sends again until it holds: Connect's
// account info, a member's lookup of a bucket's stream (awaitBucketOn), and a read of a stream's or
// a consumer's info (awaitInfo). It is nats.go's own request timer, a hang guard and no budget:
// nats-server v2.15.1 leaves such a read unanswered rather than late in two states a cluster
// passes through. Only the meta group's leader answers an account info, and a member that is not
// it answers nothing (jsAccountInfoRequest); and a member that is not a stream's or a consumer's
// leader answers nothing for it while it still counts a leader that is gone (jsStreamInfoRequest,
// jsConsumerInfoRequest, isGroupLeaderless). So a read sent across a leader change or a member's
// restart can draw no answer at all, and one that runs out is sent again by the shared wait,
// whose one bound is the test binary's deadline.
const readWait = 10 * time.Second

// createSendWait bounds each request of a test fixture's own create of a stream or a bucket
// (sendCreate), as readWait bounds a read: a hang guard and no budget, for nats-server leaves a
// create unanswered on several paths (sendCreate), and a send whose requests drew no answer within
// it is sent again by the shared wait, whose one bound is the test binary's deadline. A create on a
// cluster is proposed through the meta group and answered once the stream, a bucket's included,
// is made, and a stream of more than one copy answers it from its first leader, once its members
// have elected one: 25 bucket creates on an idle two-CPU machine took from 60 ms to 1.03 s. A send
// it ends decides nothing: the next send is answered with the stream the first one made. A
// stream's create is one request (nats.go v1.55.0 jsm.go:924), and a bucket's makes up to four,
// each bounded by it: nats.go v1.55.0's CreateKeyValue reads the account's JetStream information,
// then creates the bucket's stream, and on "stream name already in use" reads that stream and may
// update it (kv.go:432, 525, 535 and 542).
const createSendWait = 10 * time.Second

// sendCreate sends a test fixture's own create of a stream or a bucket on conn until the cluster
// answers it, and returns the answer: nil once the stream is made, or the cluster's refusal. send
// makes one create through a JetStream context on conn whose requests createSendWait bounds,
// publishing to subjects.
//
// nats-server v2.15.1 leaves a stream create unanswered on several paths, which internal/bus's
// sentAgain lists with their source lines, and sends no answer later: a fixture that waited two
// minutes for one failed TestADispatchStartKeepsAThreeCopyStreamsCopiesByteLimitAndIDAsItCorrectsTheStream
// at its first create, in a whole-package run on a loaded machine. The answer to a create the
// cluster took is also lost with the connection it was sent on when that connection drops before
// the stream's first leader answers. A send again recovers that answer only on a connection that
// reconnects, as CreateStream's own does; CreateKeyValue sends on its caller's, and none of its
// callers' connections reconnects (Connect, ConnectSeed, internal/bus's clusterConn), so there the
// drop ends the create with the connection's error.
//
// A second identical create is answered with the stream the first one made: the meta leader
// proposes the equal assignment again (jsClusteredStreamRequest) and each member that holds the
// stream answers it (processClusterCreateStream). So a create that landed costs one more send,
// never a refusal. A publish conn's grant does not cover draws no answer either, as the server
// reports it on the connection and never in the answer; the refusal conn records during the send
// (refusalOf) is then the answer, so a refused create is sent once. The sends end otherwise only on
// an answer, or where the shared wait gives up, whose failure names the sends that drew none.
func sendCreate(t testing.TB, conn *natsgo.Conn, what string, subjects []string, send func() error) error {
	t.Helper()
	sends := 0
	var answer error
	testwait.Until(t, what+": an answer to one of its sends", func() (string, bool) {
		sends++
		before := conn.LastError()
		err := send()
		if errors.Is(err, natsgo.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			if refusal := refusalOf(conn, before, subjects); refusal != nil {
				answer = refusal
				return "", true
			}
			return fmt.Sprintf("%d sends drew no answer, the last %v", sends, err), false
		}
		answer = err
		return "", true
	})
	return answer
}

// refusalOf is the server's refusal of a publish to one of subjects that conn recorded after
// before, its last error as the send began, or nil. nats.go records the server's report of a
// publish the connection's grant does not cover as the connection's last error
// (processTransientError, nats.go v1.55.0 nats.go:4173-4176), each report a new error value.
func refusalOf(conn *natsgo.Conn, before error, subjects []string) error {
	err := conn.LastError()
	if !errors.Is(err, natsgo.ErrPermissionViolation) {
		return nil
	}
	for _, subject := range subjects {
		if strings.Contains(err.Error(), `Publish to "`+subject+`"`) && err != before {
			return err
		}
	}
	return nil
}

// CreateKeyValue creates the KV bucket config names on conn, sent until the cluster answers it
// (sendCreate), and returns its handle once every running member of the cluster answers for the
// bucket's stream (AwaitBucket). conn is the connection whose grant the create is made under, so a
// test of a client's permissions creates the bucket as that client. A bucket whose stream the
// cluster already holds with another configuration fails the test with both: the stream the
// cluster holds, and the bucket the create asked for.
func (c *Cluster) CreateKeyValue(t testing.TB, conn *natsgo.Conn, config *natsgo.KeyValueConfig) natsgo.KeyValue {
	t.Helper()
	js, err := conn.JetStream(natsgo.MaxWait(createSendWait))
	if err != nil {
		t.Fatalf("open JetStream: %v", err)
	}
	var kv natsgo.KeyValue
	stream := "KV_" + config.Bucket
	// The subjects CreateKeyValue publishes to: the account read, the create, and on "already in
	// use" the update (its read of the stream held ignores a refusal).
	subjects := []string{"$JS.API.INFO", "$JS.API.STREAM.CREATE." + stream, "$JS.API.STREAM.UPDATE." + stream}
	err = sendCreate(t, conn, "create KV bucket "+config.Bucket, subjects, func() error {
		var err error
		kv, err = js.CreateKeyValue(config)
		return err
	})
	if errors.Is(err, natsgo.ErrStreamNameAlreadyInUse) {
		asked, _ := json.Marshal(config)
		t.Fatalf("create KV bucket %s: the cluster holds its stream with another configuration: it holds %s, the create asked for the bucket %s",
			config.Bucket, heldStream(js, stream), asked)
	}
	if err != nil {
		t.Fatalf("create KV bucket %s: %v", config.Bucket, err)
	}
	c.AwaitBucket(t, config.Bucket)
	return kv
}

// AwaitBucket waits until every running member of the cluster both answers for the stream of
// bucket and holds the interest a write to the bucket is routed by. A member answers a lookup out
// of the metadata it has applied, and routes a write by the subscription the bucket's stream holds
// on its subjects, which reaches a member that does not host the stream over the cluster's routes;
// the two arrive on their own clocks, and a write through a member that has neither yet is
// answered "no responders", which nats.go gives a caller as `nats: no response from stream`.
func (c *Cluster) AwaitBucket(t testing.TB, bucket string) {
	t.Helper()
	stream := "KV_" + bucket
	// A subject of the bucket, which the stream's own `$KV.<bucket>.>` subscription matches: the
	// interest a put, a delete or a watch of any key is routed by.
	subject := "$KV." + bucket + ".a"
	for i, server := range c.Servers {
		if !server.running() {
			continue
		}
		c.awaitBucketOn(t, i, server, stream, subject)
	}
}

// awaitBucketOn waits for member i to answer for stream and to hold the interest subject is
// routed by, the member's own account read in this process.
func (c *Cluster) awaitBucketOn(t testing.TB, i int, server *Server, stream, subject string) {
	t.Helper()
	conn := Connect(t, server.URL())
	defer conn.Close()
	js, err := conn.JetStream(natsgo.MaxWait(readWait))
	if err != nil {
		t.Fatalf("open JetStream on member %s: %v", c.Name(i), err)
	}
	testwait.Until(t, fmt.Sprintf("member %s to answer for stream %s and route %s", c.Name(i), stream, subject),
		func() (string, bool) {
			_, err := js.StreamInfo(stream)
			routed := false
			if srv := server.natsServer(); srv != nil {
				routed = srv.GlobalAccount().SubscriptionInterest(subject)
			}
			return fmt.Sprintf("%v (routed %t)", err, routed), err == nil && routed
		})
}
