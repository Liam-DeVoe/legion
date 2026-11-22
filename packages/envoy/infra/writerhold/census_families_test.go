package writerhold

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nuid"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/streams"
	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/bus"
)

// The census takes the daemon families it reads and reconciles from the broker's cutover rows, as
// hub-restore's abort does: every daemon/<P> row's family, whatever the --family values name. A
// --family daemon/<P> value is a check of the operator's intent against those rows: when any is
// given and the two sets differ, the census is UNKNOWN (exit 2) before it reads or removes
// anything, naming each family a row holds that the flags leave out and each flag no row holds.
// So it never removes a consumer of a daemon family whose fresh names it did not read and whose
// store it did not reconcile. The control is the census as it stood: it read and reconciled only
// the --family values, so with daemon/B left out it removed daemon B's fresh consumer as an orphan,
// left B's store on the old stream id, and was done (CLEAN, exit 0).
func TestHoldTheCensusReadsTheDaemonFamiliesFromTheCutoverRows(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	stub := func(context.Context, *nats.Conn) ([]streams.ConsumerShape, error) { return nil, nil }
	current := h.notificationsID()
	require.NotEmpty(t, current)

	// Daemon B's consumer, in its fresh form, which B's store records: made by a listener entry
	// before any hold, as the daemon's own create would leave it.
	fresh := "legion-b~" + nuid.Next()
	listener, err := nats.Connect(h.s.Cluster.URL(), h.s.ListenerCredential(holdtest.ListenerMachines[0]), nats.NoReconnect(), nats.Timeout(10*time.Second))
	require.NoError(t, err)
	ljs, err := jetstream.New(listener)
	require.NoError(t, err)
	_, err = ljs.CreateOrUpdateConsumer(ctx, bus.Stream, jetstream.ConsumerConfig{Durable: fresh, FilterSubject: "notifications.b.>", AckPolicy: jetstream.AckExplicitPolicy})
	require.NoError(t, err)
	listener.Close()
	present := func() bool {
		t.Helper()
		nc, err := h.s.ConnectOperator(ctx)
		require.NoError(t, err)
		defer nc.Close()
		js, err := jetstream.New(nc)
		require.NoError(t, err)
		_, err = js.Consumer(ctx, bus.Stream, fresh)
		return err == nil
	}
	require.True(t, present(), "setup: daemon B's consumer")

	var asked [][]string
	names := func(_ context.Context, _ *nats.Conn, _ DaemonDeployments, families []string, _ string, _ []string) (FreshNamesRead, error) {
		asked = append(asked, append([]string(nil), families...))
		read := FreshNamesRead{Names: map[string][]string{}}
		for _, f := range families {
			read.Names[f] = []string{}
			if f == "daemon/B" {
				read.Names[f] = []string{fresh}
			}
		}
		return read, nil
	}
	stores := &fakeStores{ids: map[string]string{"A": "stream-0", "B": "stream-0"}}
	h.daemonDeployments("A", "B")
	census := func(families ...string) (int, string, string) {
		args := []string{"census", "--daemon-consumer", "legion-b=notifications.b.>", "--daemon-database", "A=/dev/null", "--daemon-database", "B=/dev/null"}
		for _, f := range families {
			args = append(args, "--family", f)
		}
		return h.streamsRunReaders(CensusReaders{Recorded: stub, FreshNames: names, Stores: stores.reconcile}, args...)
	}

	// The first census sets its hold and stops at its convert check (no cutover row, so no family);
	// the cutover rows of session, daemon/A and daemon/B are then written done under that hold.
	code, _, _ := census()
	require.Equal(t, 1, code, "the census before any family is converted")
	id := h.row().HoldID
	for _, family := range []string{"session", "daemon/A", "daemon/B"} {
		_, err := h.s.Records.PutCutover(ctx, family, "done", id)
		require.NoError(t, err)
	}

	for _, row := range []struct {
		name, named string
		families    []string
	}{
		{"a cutover row's family the flags leave out", "daemon/B", []string{"session", "daemon/A"}},
		{"a flag no cutover row holds", "daemon/C", []string{"session", "daemon/A", "daemon/B", "daemon/C"}},
	} {
		asked = nil
		args := []string{"census", "--hold", id, "--daemon-consumer", "legion-b=notifications.b.>", "--daemon-database", "A=/dev/null", "--daemon-database", "B=/dev/null"}
		for _, f := range row.families {
			args = append(args, "--family", f)
		}
		code, stdout, stderr := h.streamsRunReaders(CensusReaders{Recorded: stub, FreshNames: names, Stores: stores.reconcile}, args...)
		require.Equal(t, 2, code, "%s: the census", row.name)
		require.Contains(t, stderr, row.named, row.name)
		require.NotContains(t, stdout, "CLEAN", row.name)
		require.Empty(t, asked, "%s: the census read the fresh names", row.name)
		require.True(t, present(), "%s: daemon B's consumer was removed", row.name)
		require.Equal(t, map[string]string{"A": "stream-0", "B": "stream-0"}, stores.snapshot(), "%s: a store was written", row.name)
		require.NotContains(t, h.journalCommands(id), CommandStreamsCensus+" done", row.name)
	}

	// No --family: the census reads and reconciles every family the rows hold.
	args := []string{"census", "--hold", id, "--daemon-consumer", "legion-b=notifications.b.>",
		"--daemon-database", "A=/dev/null", "--daemon-database", "B=/dev/null"}
	code, stdout, stderr := h.streamsRunReaders(CensusReaders{Recorded: stub, FreshNames: names, Stores: stores.reconcile}, args...)
	require.Zero(t, code, "the census of the rows' daemon families: %s", stderr)
	require.Contains(t, stdout, "CLEAN")
	require.Len(t, asked, 1)
	require.True(t, slices.Contains(asked[0], "daemon/A") && slices.Contains(asked[0], "daemon/B"), "the fresh names were read for %v", asked[0])
	require.True(t, present(), "daemon B's recorded consumer was removed")
	require.Equal(t, map[string]string{"A": current, "B": current}, stores.snapshot(), "a daemon store the rows hold was not reconciled")
	require.True(t, strings.Contains(strings.Join(h.journalCommands(id), "|"), CommandStreamsCensus+" done"))
}
