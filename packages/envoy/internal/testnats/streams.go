package testnats

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testwait"
)

// jetStreamWait bounds each JetStream request of a context Cluster.JetStream opens, main's bound:
// internal/bus's TestRaisingTheStreamWhileAPublisherSendsKeepsEveryAcknowledgedMessage counts a
// publish that draws no answer across the stream's raise as failed once this wait ends, and with
// the test binary's deadline in its place that publish waits out the whole run.
// A2's callers still use this bound; A2 deletes it with its last caller. A create of a stream of
// more than one copy on a cluster is answered from the stream's first leader, once its members
// have elected one (processStreamLeaderChange), and on a loaded machine that election can come
// after jetStreamWait, or the answer never come (sendCreate). Such a create goes through
// Cluster.CreateStream.
const jetStreamWait = 10 * time.Second

// JetStream is a JetStream context, each request of it bounded by jetStreamWait, on whichever
// running member of the cluster answers (Connect), closed when the test ends. A test creates a
// stream of more than one copy with Cluster.CreateStream, not through it.
func (c *Cluster) JetStream(t testing.TB) natsgo.JetStreamContext {
	t.Helper()
	conn := Connect(t, c.URL())
	t.Cleanup(conn.Close)
	js, err := conn.JetStream(natsgo.MaxWait(jetStreamWait))
	if err != nil {
		t.Fatalf("open JetStream: %v", err)
	}
	return js
}

// createConnectOptions are the options of CreateStream's own connection, which retries its first
// dial and reconnects after a drop for as long as it is open: nats.go's handshake timeout for one
// dial (a second) and its pause between two dials (100 ms). Neither bounds the create, which
// sendCreate waits for through the shared wait.
var createConnectOptions = []natsgo.Option{
	natsgo.Timeout(time.Second), natsgo.RetryOnFailedConnect(true), natsgo.MaxReconnects(-1),
	natsgo.ReconnectWait(100 * time.Millisecond),
}

// CreateStream creates the stream config names on a connection of its own to the cluster, sending
// the create until the cluster answers it (sendCreate), and fails the test with the cluster's
// refusal. The connection retries its first dial as it reconnects after a drop
// (createConnectOptions): a create sent before it has connected waits in it, as on a loaded
// machine whose members take longer than a dial's handshake timeout to answer a dial, and a create
// whose answer a drop lost is sent again on it. A stream the cluster already holds under the name
// with another configuration is refused as "stream name already in use", and the failure then
// names each field config sets that the stream the cluster holds sets otherwise
// (streamDifference): a send again of this create is answered with the stream it made, so that
// refusal is never this create's own stream.
func (c *Cluster) CreateStream(t testing.TB, config natsgo.StreamConfig) {
	t.Helper()
	conn, err := natsgo.Connect(c.URL(), createConnectOptions...)
	if err != nil {
		t.Fatalf("connect to the cluster to create stream %s: %v", config.Name, err)
	}
	defer conn.Close()
	js, err := conn.JetStream(natsgo.MaxWait(createSendWait))
	if err != nil {
		t.Fatalf("open JetStream: %v", err)
	}
	err = sendCreate(t, conn, "create stream "+config.Name, []string{"$JS.API.STREAM.CREATE." + config.Name}, func() error {
		_, err := js.AddStream(&config)
		return err
	})
	if errors.Is(err, natsgo.ErrStreamNameAlreadyInUse) {
		info, readErr := js.StreamInfo(config.Name)
		if readErr != nil {
			t.Fatalf("create stream %s: the cluster holds a stream of that name with another configuration, and its read failed: %v", config.Name, readErr)
		}
		t.Fatalf("create stream %s: the cluster holds a stream of that name with another configuration: %s", config.Name, streamDifference(config, info.Config))
	}
	if err != nil {
		t.Fatalf("create stream %s: %v", config.Name, err)
	}
}

// streamDifference names each field asked sets that held sets otherwise, as the stream API writes
// the field, held's value first, asked's as the server stores it (asStored). Of the fields the API
// leaves out when they are unset, asked's unset ones are left out, as the server fills them in,
// and of asked's metadata only the keys asked names, as the server adds keys of its own. When no
// field asked sets differs, the server judged a field asked leaves unset, and the whole of held
// is named.
func streamDifference(asked, held natsgo.StreamConfig) string {
	askedFields, heldFields := apiFields(asStored(asked)), apiFields(held)
	if len(asked.Metadata) > 0 {
		kept := make(map[string]string, len(asked.Metadata))
		for key := range asked.Metadata {
			if value, ok := held.Metadata[key]; ok {
				kept[key] = value
			}
		}
		heldFields["metadata"], _ = json.Marshal(kept)
	}
	var differences []string
	for _, field := range slices.Sorted(maps.Keys(askedFields)) {
		if heldValue, ok := heldFields[field]; !ok || !bytes.Equal(heldValue, askedFields[field]) {
			if !ok {
				heldValue = []byte("unset")
			}
			differences = append(differences, fmt.Sprintf("%s holds %s, asked %s", field, heldValue, askedFields[field]))
		}
	}
	if len(differences) == 0 {
		whole, _ := json.Marshal(held)
		return "every field the create sets matches the stream held, which is " + string(whole)
	}
	return strings.Join(differences, "; ")
}

// asStored is config with each field that the stream API writes even when config leaves it
// unset, and that the server stores otherwise when it is unset, set as the server stores it.
// nats.go v1.55.0 writes max_consumers, max_msgs, max_bytes, max_msgs_per_subject and
// num_replicas with no omitempty (jsm.go:130, 135, 140, 156 and 167), so an unset one goes out as
// 0, and nats-server v2.15.1 stores one copy for num_replicas and -1, no limit, for the other four
// (checkStreamCfgLocked, server/stream.go:1959-1997). Every other field the API writes when it is
// unset goes out as the value the server stores for it, but two the server sets beside another
// field, which no caller of CreateStream sets: allow_msg_ttl beside a subject delete marker TTL
// (:2067-2076), and mirror_direct beside a mirror (:2221-2228).
func asStored(config natsgo.StreamConfig) natsgo.StreamConfig {
	if config.Replicas == 0 {
		config.Replicas = 1
	}
	if config.MaxConsumers == 0 {
		config.MaxConsumers = -1
	}
	for _, limit := range []*int64{&config.MaxMsgs, &config.MaxBytes, &config.MaxMsgsPerSubject} {
		if *limit == 0 {
			*limit = -1
		}
	}
	return config
}

// apiFields is config as the stream API writes it, field by field: a field the API marks
// omitempty is absent at its zero value.
func apiFields(config natsgo.StreamConfig) map[string]json.RawMessage {
	encoded, _ := json.Marshal(config)
	fields := map[string]json.RawMessage{}
	_ = json.Unmarshal(encoded, &fields)
	return fields
}

// heldStream is the configuration the cluster holds for stream, as the stream API writes it, or
// why it could not be read.
func heldStream(js natsgo.JetStreamContext, stream string) string {
	info, err := js.StreamInfo(stream)
	if err != nil {
		return fmt.Sprintf("a stream %s whose read failed: %v", stream, err)
	}
	encoded, _ := json.Marshal(info.Config)
	return string(encoded)
}

// Copies is how many copies of a stream or a consumer a cluster holds, its leader's and its
// replicas', as its info's Cluster reports them: 0 for none reported.
func Copies(cluster *natsgo.ClusterInfo) int {
	if cluster == nil {
		return 0
	}
	return 1 + len(cluster.Replicas)
}

// settled reports whether cluster shows n settled copies: a leader and n-1 replicas, every one
// current with the leader and none offline. Every wait below settles on it.
func settled(cluster *natsgo.ClusterInfo, n int) bool {
	return cluster != nil && cluster.Leader != "" && len(cluster.Replicas) == n-1 &&
		!slices.ContainsFunc(cluster.Replicas, func(peer *natsgo.PeerInfo) bool { return !peer.Current || peer.Offline })
}

// AwaitSettledStream waits until stream holds as many settled copies as its configuration asks
// (settled), and returns its info. A test that asserts the copies a write left reads them from the
// info it returns.
func AwaitSettledStream(t testing.TB, js natsgo.JetStreamContext, stream string) *natsgo.StreamInfo {
	t.Helper()
	return awaitInfo(t, fmt.Sprintf("stream %s to settle", stream),
		func() (*natsgo.StreamInfo, error) { return js.StreamInfo(stream, natsgo.MaxWait(readWait)) },
		func(info *natsgo.StreamInfo) bool { return settled(info.Cluster, info.Config.Replicas) },
		describeStream)
}

// AwaitStreamCopies waits until stream is configured with n copies and holds n settled ones
// (settled), and returns its info. A member that was killed or lame-ducked mid-write rejoins and
// catches its streams up, which this waits out however long it takes.
func AwaitStreamCopies(t testing.TB, js natsgo.JetStreamContext, stream string, n int) *natsgo.StreamInfo {
	t.Helper()
	return awaitInfo(t, fmt.Sprintf("stream %s to hold %d settled copies", stream, n),
		func() (*natsgo.StreamInfo, error) { return js.StreamInfo(stream, natsgo.MaxWait(readWait)) },
		func(info *natsgo.StreamInfo) bool { return info.Config.Replicas == n && settled(info.Cluster, n) },
		describeStream)
}

// AwaitStreamCopiesWithin is AwaitStreamCopies under main's name and signature, from when it took
// a bound of its own; within is no longer read, and the wait is AwaitStreamCopies's.
// A2's callers still use this; A2 deletes it with its last caller.
func AwaitStreamCopiesWithin(t testing.TB, js natsgo.JetStreamContext, stream string, n int, _ time.Duration) *natsgo.StreamInfo {
	t.Helper()
	return AwaitStreamCopies(t, js, stream, n)
}

// AwaitConsumerCopies waits until stream's consumer holds n settled copies (settled), as a durable
// that sets no replica count of its own does once it has followed its stream to n, and returns its
// info.
func AwaitConsumerCopies(t testing.TB, js natsgo.JetStreamContext, stream, consumer string, n int) *natsgo.ConsumerInfo {
	t.Helper()
	return awaitInfo(t, fmt.Sprintf("consumer %s of %s to hold %d settled copies", consumer, stream, n),
		func() (*natsgo.ConsumerInfo, error) {
			return js.ConsumerInfo(stream, consumer, natsgo.MaxWait(readWait))
		},
		func(info *natsgo.ConsumerInfo) bool { return settled(info.Cluster, n) },
		describeConsumer)
}

// AwaitStreamLeader waits until stream has a leader other than not, the server_name of a member
// the test stopped or "" for any, and returns its info.
func AwaitStreamLeader(t testing.TB, js natsgo.JetStreamContext, stream, not string) *natsgo.StreamInfo {
	t.Helper()
	return awaitInfo(t, fmt.Sprintf("stream %s to have a leader other than %q", stream, not),
		func() (*natsgo.StreamInfo, error) { return js.StreamInfo(stream, natsgo.MaxWait(readWait)) },
		func(info *natsgo.StreamInfo) bool {
			return info.Cluster != nil && info.Cluster.Leader != "" && info.Cluster.Leader != not
		},
		describeStream)
}

// describeStream is what a stream wait prints of the info it read last when it ran out: the copies
// the stream's configuration asks for, and every copy the cluster reports (describeCopies).
func describeStream(info *natsgo.StreamInfo) string {
	if info == nil {
		return "no stream info was read"
	}
	return describeCopies(info.Config.Replicas, info.Cluster)
}

// describeConsumer is describeStream for a consumer wait. A durable that sets no replica count of
// its own reports none configured and follows its stream's count.
func describeConsumer(info *natsgo.ConsumerInfo) string {
	if info == nil {
		return "no consumer info was read"
	}
	return describeCopies(info.Config.Replicas, info.Cluster)
}

// describeCopies is the line both describers print: the copies the configuration asks for, and
// the state of every copy the cluster reports (describeCluster). Go gives a type parameter no way
// to reach those two fields, so the two describers stay and share this.
func describeCopies(configured int, cluster *natsgo.ClusterInfo) string {
	return fmt.Sprintf("%d copies configured, %s", configured, describeCluster(cluster))
}

// describeCluster names every peer the cluster reports and the state settled reads: the leader,
// and each replica's name, whether it is current with the leader, whether it is offline, how long
// since it was last active and how far behind it is. A wait that ran out prints this because
// printing the info itself prints the ClusterInfo as an address, so no CI log of a failed wait
// could name the peer that was behind.
func describeCluster(cluster *natsgo.ClusterInfo) string {
	if cluster == nil {
		return "the server reported no cluster for it"
	}
	peers := make([]string, 0, len(cluster.Replicas))
	for _, peer := range cluster.Replicas {
		if peer == nil {
			peers = append(peers, "a nil peer")
			continue
		}
		peers = append(peers, fmt.Sprintf("%s current=%t offline=%t active=%s lag=%d",
			peer.Name, peer.Current, peer.Offline, peer.Active, peer.Lag))
	}
	return fmt.Sprintf("%d copies reported, leader %q, replicas [%s]",
		Copies(cluster), cluster.Leader, strings.Join(peers, "; "))
}

// infoRead is one read of awaitInfo's wait: what the describer says of the info it read, and the
// server's answer, which is what the failure of a wait that ran out prints.
type infoRead[T any] struct {
	info     *T
	err      error
	describe func(*T) string
}

func (r infoRead[T]) String() string { return fmt.Sprintf("%s (%v)", r.describe(r.info), r.err) }

// awaitInfo reads an info through the shared wait until a read succeeds and done holds for it, and
// returns that info. Each caller bounds its read by readWait, whatever the wait of the context it
// was given, so a read sent across a leader change that draws no answer is sent again: a member
// that still counts a stream's or a consumer's leader that is gone answers nothing for it
// (nats-server v2.15.1, jsStreamInfoRequest, jsConsumerInfoRequest, isGroupLeaderless). The
// failure of a wait that ran out prints what describe says of the info it read last and the last
// error.
func awaitInfo[T any](t testing.TB, what string, read func() (*T, error), done func(*T) bool, describe func(*T) string) *T {
	t.Helper()
	return testwait.Until(t, what, func() (infoRead[T], bool) {
		info, err := read()
		return infoRead[T]{info: info, err: err, describe: describe}, err == nil && done(info)
	}).info
}

// StreamWrites records every stream create and update a JetStream account's clients ask for: a
// request on the account's $JS.API.STREAM.CREATE or $JS.API.STREAM.UPDATE subject, a bucket's
// stream (KV_<bucket>) among them, reaches its subscriber as it reaches the server.
type StreamWrites struct {
	conn *natsgo.Conn
	sub  *natsgo.Subscription
}

// WatchStreamWrites starts recording the stream writes asked of the account uri connects to, a
// lone server's (Start) or a test's own (URL), until the test ends.
func WatchStreamWrites(t testing.TB, uri string) *StreamWrites {
	t.Helper()
	conn := Connect(t, uri)
	t.Cleanup(conn.Close)
	sub, err := conn.SubscribeSync("$JS.API.STREAM.*.*")
	if err != nil {
		t.Fatalf("watch stream writes: %v", err)
	}
	if err := conn.FlushWithContext(Context(t)); err != nil {
		t.Fatalf("watch stream writes: %v", err)
	}
	return &StreamWrites{conn: conn, sub: sub}
}

// Take returns the subjects of the creates and updates asked since the last Take. Each request
// reached the subscription before the server answered it, so the flush, a round trip after the
// writer's answer, leaves every one queued here.
func (w *StreamWrites) Take(t testing.TB) []string {
	t.Helper()
	if err := w.conn.FlushWithContext(Context(t)); err != nil {
		t.Fatalf("flush the stream-write watch: %v", err)
	}
	var subjects []string
	for {
		msg, err := w.sub.NextMsg(50 * time.Millisecond)
		if err != nil {
			return subjects
		}
		if strings.HasPrefix(msg.Subject, "$JS.API.STREAM.CREATE.") || strings.HasPrefix(msg.Subject, "$JS.API.STREAM.UPDATE.") {
			subjects = append(subjects, msg.Subject)
		}
	}
}
