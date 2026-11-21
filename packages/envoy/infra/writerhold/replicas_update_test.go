package writerhold

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// updateResponder answers STREAM.INFO and STREAM.UPDATE of one stream on a plain server: each read
// answers the stream at its copies now; the first drop updates go unanswered, as a clustered
// stream's update is while its group elects a leader (only the leader answers it), and each one
// after sets the copies and answers.
type updateResponder struct {
	mu       sync.Mutex
	replicas int
	drop     int
	updates  int
}

func newUpdateResponder(t *testing.T, url, stream string, replicas, drop int) *updateResponder {
	t.Helper()
	r := &updateResponder{replicas: replicas, drop: drop}
	conn, err := testnats.ConnectSeed(t, url, "")
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	info := func(replicas int) []byte {
		body, err := json.Marshal(jetstream.StreamInfo{Config: jetstream.StreamConfig{Name: stream, Replicas: replicas}})
		require.NoError(t, err)
		return body
	}
	_, err = conn.Subscribe(fmt.Sprintf(natsserver.JSApiStreamInfoT, stream), func(msg *nats.Msg) {
		r.mu.Lock()
		replicas := r.replicas
		r.mu.Unlock()
		_ = msg.Respond(info(replicas))
	})
	require.NoError(t, err)
	_, err = conn.Subscribe(fmt.Sprintf(natsserver.JSApiStreamUpdateT, stream), func(msg *nats.Msg) {
		var config jetstream.StreamConfig
		if json.Unmarshal(msg.Data, &config) != nil {
			return
		}
		r.mu.Lock()
		r.updates++
		answer := r.updates > r.drop
		if answer {
			r.replicas = config.Replicas
		}
		r.mu.Unlock()
		if answer {
			_ = msg.Respond(info(config.Replicas))
		}
	})
	require.NoError(t, err)
	require.NoError(t, conn.Flush())
	return r
}

func (r *updateResponder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updates
}

// The streams rows' setup takes a copied bucket from 3 copies to 2 (setReplicas), and its one
// update once drew no answer within nats.go's 5 s under load: the seed had just made the stream,
// its group was still electing, and only a group's leader answers an update. updateReplicas bounds
// each request, sends it again until answered, and reads the stream back at the new count. Scripted
// on a plain server: the first update goes unanswered and the second is answered, so the update is
// done at its second send and the stream reads 2 copies. The control is the setup's one update,
// which fails against the same leaderless answer.
func TestTheReplicasUpdateIsSentAgainUntilAnswered(t *testing.T) {
	_, url := testnats.StartConfig(t, "", nil, true)
	nc, err := testnats.ConnectSeed(t, url, "")
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	// The control: one update, as the setup sent it.
	interests := kvStreamPrefix + bus.InterestBucket
	newUpdateResponder(t, url, interests, 3, 1)
	once, cancelOnce := context.WithTimeout(ctx, 2*time.Second)
	defer cancelOnce()
	s, err := js.Stream(once, interests)
	require.NoError(t, err)
	config := s.CachedInfo().Config
	config.Replicas = 2
	_, err = js.UpdateStream(once, config)
	require.Error(t, err, "control: the one update of a group that answers no update")
	t.Logf("control, the one update: %v", err)

	roles := kvStreamPrefix + bus.RoleClaimBucket
	r := newUpdateResponder(t, url, roles, 3, 1)
	require.NoError(t, updateReplicas(ctx, js, roles, 2, 500*time.Millisecond))
	require.Equal(t, 2, r.count(), "the update was not sent again after its unanswered first send")
	s, err = js.Stream(ctx, roles)
	require.NoError(t, err)
	require.Equal(t, 2, s.CachedInfo().Config.Replicas)
}
