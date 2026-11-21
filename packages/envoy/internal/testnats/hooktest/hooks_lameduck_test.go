//go:build odatrace

package hooktest

import (
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A lame duck is a shutdown too: nats-server v2.15.1's lameDuckMode shuts JetStream down, which
// waits for each clustered stream's monitor goroutine (stream.go stop, stopMonitoring), before it
// calls Shutdown (server.go:4516, 4608). A committed entry held at stream.apply parks that
// goroutine, so a hold the hook releases only at server.shutdown is never released and
// testnats.Server.LameDuck gives up after its 30 s connectTimeout, forces a Shutdown and returns
// an error. The lame duck of a server with a hold parked must return, as its Stop does.
func TestForcedOrderALameDuckReleasesTheHoldOnItsServer(t *testing.T) {
	cluster := testnats.StartCluster(t, 3)
	hooks := testnats.Hook(t)
	conn := testnats.Connect(t, cluster.URL())
	t.Cleanup(conn.Close)
	js, err := conn.JetStream(natsgo.MaxWait(reachWithin))
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{Name: "LAME", Subjects: []string{"lame.>"}, Replicas: 3}); err != nil {
		t.Fatalf("create the stream: %v", err)
	}
	if _, err := js.Publish("lame.first", nil); err != nil {
		t.Fatalf("the unheld publish: %v", err)
	}
	info, err := js.StreamInfo("LAME")
	if err != nil || info.Cluster == nil || cluster.Member(info.Cluster.Leader) < 0 {
		t.Fatalf("the stream's leader: %+v, %v", info, err)
	}
	leader := cluster.Servers[cluster.Member(info.Cluster.Leader)]

	hold := hooks.Hold(testnats.StreamApply, testnats.OnServer(leader), testnats.Stream("LAME"), testnats.Subject("lame.second"))
	if _, err := js.PublishAsync("lame.second", nil); err != nil {
		t.Fatalf("publish the held message: %v", err)
	}
	t.Logf("held %s", hold.Reached(t))
	start := time.Now()
	if err := leader.LameDuck(); err != nil {
		t.Fatalf("the lame duck of a server with a hold parked failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
	if took := time.Since(start); took > stopWithin {
		t.Fatalf("the lame duck of a server with a hold parked took %s, more than %s", took.Round(time.Millisecond), stopWithin)
	}
	if !hold.Released() {
		t.Fatal("the lame duck returned and the hold is not released")
	}
}
