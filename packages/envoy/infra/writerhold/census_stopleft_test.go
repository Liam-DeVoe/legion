package writerhold

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// A stop's record of what its command left names every change its runs made and did not undo:
// each consumer the census removed, and this hold's barrier key in each bucket the census put it
// in, which nothing the census does deletes. So `hold release` is refused over that stop, and
// `hold abort` ends the hold. The census here removes an undeclared consumer, which it then reports
// as a failure, so it stops (exit 1). An exit after an UNKNOWN records `unknown: read …; unread …;
// <what the run changed>`, never `unchanged`: TestHoldReleaseAfterAStreamsCensus (no reader) and
// TestHoldTheCensusReportsGoneAndUnreconciledStores (a store unreconciled) show it. The control is
// the stop at aa66766bef, which named the removal alone, without the barrier key; at 599c9838f2
// those two UNKNOWN stops recorded `unchanged`.
func TestHoldACensusStopNamesEveryChangeItLeft(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	stub := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }

	// An undeclared consumer, which the census removes.
	const stray = "stray-consumer"
	listener, err := nats.Connect(h.s.Cluster.URL(), h.s.ListenerCredential(holdtest.ListenerMachines[0]), nats.NoReconnect(), nats.Timeout(10*time.Second))
	require.NoError(t, err)
	ljs, err := listener.JetStream()
	require.NoError(t, err)
	_, err = ljs.AddConsumer(bus.Stream, &nats.ConsumerConfig{Durable: stray, FilterSubject: "notifications.stray.>", AckPolicy: nats.AckExplicitPolicy})
	require.NoError(t, err, "setup: the stray consumer")
	listener.Close()

	id := h.s.Prepare(t, holdrecord.WorkflowStreamsCensus)
	h.s.HoldWithoutTheSteps(t, id)
	_, err = h.s.Records.PutCutover(ctx, "session", "done", id)
	require.NoError(t, err)
	names := func(context.Context, *nats.Conn, DaemonDeployments, []string, string, []string) (FreshNamesRead, error) {
		return FreshNamesRead{Names: map[string][]string{"session": {}}}, nil
	}
	code, stdout, stderr := h.streamsRunReaders(CensusReaders{Recorded: stub, FreshNames: names, Stores: noStores}, "census", "--hold", id)
	require.Equal(t, 1, code, "the census that removed an undeclared consumer: %s%s", stdout, stderr)
	require.Contains(t, stdout, "consumer "+stray+": removed")

	last := h.lastEntry(id)
	require.Equal(t, CommandStreamsCensus+" "+holdrecord.StateStop, last.Command+" "+last.State)
	left := last.Outputs["left"]
	require.False(t, strings.HasPrefix(left, "unknown: "), "a refusal's stop read as an UNKNOWN's: %q", left)
	require.Contains(t, left, "consumer "+stray+": removed")
	require.Contains(t, left, "this hold's barrier key in KV_"+bus.SessionConsumersBucket)

	code, _, stderr = h.run(actionRelease, "--hold", id, "--reason", "over a census that removed a consumer")
	require.Equal(t, 1, code, "release over the census's stop: %s", stderr)
	code, _, stderr = h.run(actionAbort, "--hold", id, "--reason", "the way out")
	require.Zero(t, code, "abort over the census's stop: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
}
