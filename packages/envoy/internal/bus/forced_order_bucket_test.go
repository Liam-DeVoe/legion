//go:build odatrace

package bus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/forcedorder"
	"github.com/sjawhar/envoy/internal/testnats"
)

// forcedMetaParked is how long the bucket row keeps the meta leader's meta layer parked after the
// bucket's sentinel is written: long past the rig's own steps from the create to the first read.
const forcedMetaParked = 10 * time.Second

// The first read of a selection bucket a rig made waits until the meta leader has applied the
// bucket (forcedRig.makeSelection). Held here, on R3: the meta leader's meta layer is parked at a
// consumer assignment of the notification stream, so it applies nothing after it, the bucket's
// assignment among them, while the other two members apply it, elect the bucket's leader and answer
// the create and the sentinel's write. Until the meta leader applies the bucket it answers any
// request for it "stream not found" (10059), and the other members' API workers are held at the
// first message get of the bucket after the rig is made, so that answer is the only one the first
// registration's read can take. The meta leader is released forcedMetaParked after the sentinel's
// write is applied, and the API workers once it is. TestForcedOrderLongSessionIDsAreKeptApartAndRefusedAsToday's
// R3 topology failed so once at a load of 702 to 862: "a 253-byte id: consumer names: the selection
// store is missing: KV_envoy_session_consumers (... err_code=10059 stream not found)". The control
// in the row: the meta leader still led the meta group, with its meta layer parked, when the
// sentinel's write was applied.
func TestForcedOrderASelectionBucketIsReadOnlyOnceTheMetaLeaderHasAppliedIt(t *testing.T) {
	skipOnARefusedServer(t)
	r := startForcedRig(t, forcedorder.Topologies[2])
	leader := r.Cluster.MetaLeader()
	if leader < 0 {
		t.Fatal("no member of the ready cluster leads its meta group")
	}
	const parked = "forced-meta-parked"
	meta := r.Hooks.Hold(testnats.MetaApply, testnats.OnServer(r.Servers.Servers[leader]), testnats.Stream(Stream), testnats.Consumer(parked))
	js, err := r.conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	// The create is answered by the consumer's leader among the other two members; the row needs
	// only the meta leader's apply of it, held.
	go func() {
		_, _ = js.AddConsumer(Stream, &natsgo.ConsumerConfig{Durable: parked, AckPolicy: natsgo.AckExplicitPolicy})
	}()
	t.Logf("held %s", meta.Reached(t))

	// released lets the meta leader go on, then every API worker held before; one held after it is
	// let go as it is reached.
	var mu sync.Mutex
	var released bool
	var api []*testnats.Hold
	release := func() {
		mu.Lock()
		released = true
		held := api
		mu.Unlock()
		meta.Release()
		for _, hold := range held {
			hold.Release()
		}
	}
	var leaderAtSentinel atomic.Int64
	var parkedAtSentinel atomic.Bool
	leaderAtSentinel.Store(-1)
	sentinel := r.Hooks.Hold(testnats.StreamApply, testnats.Stream(forcedSelectionStream))
	sentinel.Release() // reached and recorded, never parked
	// The release runs on its own after the test too, harmlessly: a Release only lets a hold go.
	testnats.SetOnReached(sentinel, func() {
		leaderAtSentinel.Store(int64(r.Cluster.MetaLeader()))
		parkedAtSentinel.Store(!meta.Released())
		time.AfterFunc(forcedMetaParked, release)
	})
	r.makeSelection(t)
	if got := leaderAtSentinel.Load(); got != int64(leader) || !parkedAtSentinel.Load() {
		t.Fatalf("control: when the sentinel's write was applied the meta leader was member %d with its meta layer parked %t; want member %d, parked",
			got, parkedAtSentinel.Load(), leader)
	}

	mark := r.Hooks.Mark()
	read := "$JS.API.STREAM.MSG.GET." + forcedSelectionStream
	for i, srv := range r.Servers.Servers {
		if i == leader {
			continue
		}
		hold := r.Hooks.Hold(testnats.APIWorker, testnats.OnServer(srv), testnats.Subject(read), testnats.After(mark))
		testnats.SetOnReached(hold, func() {
			mu.Lock()
			defer mu.Unlock()
			if released {
				hold.Release()
			}
		})
		mu.Lock()
		api = append(api, hold)
		mu.Unlock()
	}
	ctx := context.Background()
	id := "ses_forced_bucket_applied"
	agent := contracts.LegacyAgentSubject(id)
	seqs := r.publish(t, agent, 2)
	start, err := r.consumers.Ensure(ctx, id, []string{agent}, seqs[0])
	if err != nil {
		t.Fatalf("the first registration after the rig was made: %v", err)
	}
	mu.Lock()
	held := append([]*testnats.Hold(nil), api...)
	mu.Unlock()
	for _, hold := range held {
		t.Logf("held %s", hold.Reached(t))
	}
	got := delivered(t, seqs, func(ctx context.Context) (*SessionPull, error) { return forcedPull(ctx, r.consumers, start, id) })
	requireDelivered(t, got, seqs, "the first registration's consumer")
}
