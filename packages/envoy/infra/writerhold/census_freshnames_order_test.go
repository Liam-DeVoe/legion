package writerhold

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/internal/bus"
)

// The census's reader of the recorded fresh names runs after the census's checks and its barrier on
// the selection bucket (streams.Census calls it, CensusInput.RecordedFreshNames), so a record
// written before the barrier is read and its consumer is not removed as unrecorded. The reader here
// reads the barrier's own record, $KV.envoy_session_consumers.hold.barrier.<hold id>, on the
// census's connection when it is called: present, so the barrier came first. A census refused at
// its convert check calls the reader not at all. The control is the census as it stood: it read the
// fresh names before streams.Census ran, before the barrier, and the reader found no barrier record.
func TestHoldTheCensusReadsTheFreshNamesAfterItsBarrier(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	stub := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }
	var holdID string
	var calls int
	var barrier error
	reader := func(ctx context.Context, conn *nats.Conn, _ DaemonDeployments, _ []string, _ string, _ []string) (FreshNamesRead, error) {
		calls++
		_, barrier = lastBySubject(ctx, conn, bus.SessionConsumersBucket, "hold.barrier."+holdID)
		return FreshNamesRead{Names: map[string][]string{"session": {}}}, nil
	}
	readers := CensusReaders{Recorded: stub, FreshNames: reader, Stores: noStores}

	code, _, _ := h.streamsRunReaders(readers, "census")
	require.Equal(t, 1, code, "the census before the family is converted")
	require.Zero(t, calls, "a census refused at its convert check read the fresh names")
	holdID = h.row().HoldID
	_, err := h.s.Records.PutCutover(ctx, "session", "done", holdID)
	require.NoError(t, err)

	code, _, stderr := h.streamsRunReaders(readers, "census", "--hold", holdID, "--family", "session")
	require.Zero(t, code, "the census: %s", stderr)
	require.Equal(t, 1, calls)
	require.NoError(t, barrier, "the reader ran before the census's barrier on %s", bus.SessionConsumersBucket)
}
