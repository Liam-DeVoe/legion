package writerhold

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nuid"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// The census takes every family from the broker's cutover rows, session and dispatch as well as
// each daemon/<P>, and reads the fresh names of each. --family values are optional; given, they must
// be exactly the rows' families, or the census is UNKNOWN, naming each missing or extra family,
// before it reads or removes anything. So it never removes a consumer of a family whose fresh names
// it did not read. The reader here models t-7's (consumernames.ReadRecordedNames), which this row
// uses until t-7's head is merged: it reads Dispatch's store only when the families name dispatch,
// and then only from --dispatch-database-file, refusing without one. With the session and dispatch
// rows done:
//   - --family session, dispatch left out: UNKNOWN, naming dispatch; Dispatch's fresh consumer kept;
//   - no --family and no --dispatch-database-file: the reader is asked for dispatch and refuses, so
//     UNKNOWN; the consumer kept;
//   - no --family, with --dispatch-database-file: the reader reads Dispatch's names, the consumer is
//     kept as recorded, and the census is done.
//
// The control is the census at 599c9838f2: with --family session it read no dispatch names, removed
// Dispatch's fresh consumer as an orphan (exit 1, CHANGED ... removed), and its rerun was done.
func TestHoldTheCensusTakesEveryFamilyFromTheCutoverRows(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	stub := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }

	// Dispatch's live consumer in its fresh form, as Dispatch's own create leaves it.
	fresh := bus.DispatchDeliveryDurable + "~" + nuid.Next()
	listener, err := nats.Connect(h.s.Cluster.URL(), h.s.ListenerCredential(holdtest.ListenerMachines[0]), nats.NoReconnect(), nats.Timeout(10*time.Second))
	require.NoError(t, err)
	ljs, err := listener.JetStream()
	require.NoError(t, err)
	config := bus.DispatchDeliveryConsumer()
	config.Durable = fresh
	_, err = ljs.AddConsumer(bus.Stream, &config)
	require.NoError(t, err, "setup: Dispatch's fresh consumer")
	listener.Close()
	present := func() bool {
		t.Helper()
		nc, err := h.s.ConnectOperator(ctx)
		require.NoError(t, err)
		defer nc.Close()
		js, err := nc.JetStream()
		require.NoError(t, err)
		_, err = js.ConsumerInfo(bus.Stream, fresh)
		return err == nil
	}
	require.True(t, present(), "setup: the consumer")

	var asked [][]string
	reader := func(_ context.Context, _ *nats.Conn, _ DaemonDeployments, families []string, dispatchFile string, _ []string) (FreshNamesRead, error) {
		asked = append(asked, slices.Clone(families))
		read := FreshNamesRead{Names: map[string][]string{}}
		if slices.Contains(families, "dispatch") {
			if dispatchFile == "" {
				return FreshNamesRead{}, errors.New("the recorded names: dispatch's store: no --dispatch-database-file names it")
			}
			read.Names["dispatch"] = []string{fresh}
		}
		for _, f := range families {
			if _, ok := read.Names[f]; !ok {
				read.Names[f] = []string{}
			}
		}
		return read, nil
	}
	census := func(args ...string) (int, string, string) {
		return h.streamsRunReaders(CensusReaders{Recorded: stub, FreshNames: reader, Stores: noStores}, append([]string{"census"}, args...)...)
	}

	// A streams-census hold set without its steps, then the session and dispatch rows done in it.
	id := h.s.Prepare(t, holdrecord.WorkflowStreamsCensus)
	h.s.HoldWithoutTheSteps(t, id)
	for _, family := range []string{"session", "dispatch"} {
		_, err := h.s.Records.PutCutover(ctx, family, "done", id)
		require.NoError(t, err)
	}

	code, stdout, stderr := census("--hold", id, "--family", "session")
	require.Equal(t, 2, code, "--family session with the dispatch row done: %s%s", stdout, stderr)
	require.Contains(t, stderr, "dispatch has a cutover row and no --family")
	require.Empty(t, asked, "the census read fresh names before it refused")
	require.True(t, present(), "Dispatch's fresh consumer was removed")

	code, stdout, stderr = census("--hold", id)
	require.Equal(t, 2, code, "no --family and no --dispatch-database-file: %s%s", stdout, stderr)
	require.Contains(t, stderr, "dispatch's store")
	require.Equal(t, [][]string{{"dispatch", "session"}}, asked, "the families the reader was asked for")
	require.True(t, present(), "Dispatch's fresh consumer was removed")

	code, stdout, stderr = census("--hold", id, "--dispatch-database-file", "/run/dispatch-db")
	require.Zero(t, code, "no --family, with --dispatch-database-file: %s%s", stdout, stderr)
	require.True(t, present(), "Dispatch's recorded fresh consumer was removed")
	require.Contains(t, h.journalCommands(id), CommandStreamsCensus+" done")
}
