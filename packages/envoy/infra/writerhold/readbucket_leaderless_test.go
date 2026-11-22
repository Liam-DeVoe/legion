package writerhold

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// ReadBucket on a stream whose group has no leader waits for one within barrierWithin and then is
// UNKNOWN, naming its first step, the stream's leader before the barrier, and runs neither the
// barrier nor the read. A responder on a server with no JetStream answers STREAM.INFO with a
// cluster and no leader, as a leaderless group's members do. The forced-order row waits for a
// leader since 43126cebd4, so this row holds the wait (round 6's coverage note). Control: a go
// test -overlay copy of barrier.go whose streamLeader reads a cluster with no leader as a lone
// server's stream; the row then fails, since ReadBucket goes on to the barrier.
func TestReadBucketOnALeaderlessStreamIsUnknownNamingTheLeaderStep(t *testing.T) {
	ctx := t.Context()
	_, url := testnats.StartConfig(t, "", nil, true)
	stream := kvStreamPrefix + bus.SessionConsumersBucket
	leaderless, err := json.Marshal(jetstream.StreamInfo{Config: jetstream.StreamConfig{Name: stream},
		Cluster: &jetstream.ClusterInfo{Name: "hub", Leader: ""}})
	require.NoError(t, err)
	members, err := testnats.ConnectSeed(t, url, "")
	require.NoError(t, err)
	t.Cleanup(members.Close)
	_, err = members.Subscribe(strings.Replace(natsserver.JSApiStreamInfoT, "%s", "*", 1), func(msg *nats.Msg) {
		_ = msg.Respond(leaderless)
	})
	require.NoError(t, err)
	require.NoError(t, members.Flush())
	nc, err := testnats.ConnectSeed(t, url, "")
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	read := false
	start := time.Now()
	err = ReadBucket(ctx, nc, bus.SessionConsumersBucket, "hold1", func(context.Context) error {
		read = true
		return nil
	})
	took := time.Since(start)
	t.Logf("ReadBucket on a leaderless stream answered in %s: %v", took, err)
	requireUnknown(t, err, StepBarrier, stream+"'s leader before the barrier (attempt 1 of 3)", "did not answer within", "answers no leader")
	require.False(t, read, "ReadBucket read a leaderless stream")
	require.GreaterOrEqual(t, took, barrierWithin, "ReadBucket did not wait for a leader")
	require.Less(t, took, barrierWithin+jsRequest, "ReadBucket waited past its bound")
}
