//go:build odatrace

package consumernames

import (
	"context"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsversion"
)

// prepare runs while every client still runs, so a creator's sweep, deregistration or reaper can
// delete a fixed name between prepare's read of it and its threshold raise. The row holds
// prepare's update of s-gone (CONSUMER.CREATE with action update) at the meta leader's api.worker,
// deletes s-gone as its creator would, and releases the update, which the server answers "consumer
// does not exist" (10149): prepare completes, and its cohort keeps s-gone with the floor it read,
// a safe bound since a floor never goes down. It fails when prepare fails the step on a fixed name
// that needs no raise any more, or drops its floor.
func TestForcedOrderPrepareKeepsAFixedNameDeletedBeforeItsRaise(t *testing.T) {
	rig := newConvertRig(t, 0)
	ctx := context.Background()
	seqs := rig.publish("notifications.agent.gone", 3)
	rig.fixed("s-gone", "notifications.agent.gone", time.Hour)
	consumer, err := rig.writer.Consumer(ctx, notifications, "s-gone")
	require.NoError(t, err)
	require.NoError(t, rig.ackUpTo(consumer, seqs[1]))
	id := rig.prepared(holdrecord.WorkflowCutover)

	hooks := testnats.Hook(t)
	leader := rig.metaLeaderPod(ctx)
	member := -1
	for i := range rig.s.Cluster.Servers {
		if holdtest.Pod(i) == leader {
			member = i
		}
	}
	require.GreaterOrEqual(t, member, 0)
	hold := hooks.Hold(testnats.APIWorker, testnats.OnServer(rig.s.Cluster.Servers[member]),
		testnats.Subject("$JS.API.CONSUMER.CREATE."+notifications+".s-gone"))
	type result struct {
		code           int
		stdout, stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, stdout, stderr := rig.run(actionPrepare, "--hold", id)
		done <- result{code, stdout, stderr}
	}()
	t.Logf("held %s", hold.Reached(t))
	require.NoError(t, rig.writer.DeleteConsumer(ctx, notifications, "s-gone"), "the creator's delete between prepare's read and its raise")
	hold.Release()
	var got result
	select {
	case got = <-done:
	case <-time.After(time.Minute):
		t.Fatal("prepare did not end within a minute of the release")
	}
	require.Zero(t, got.code, "prepare failed on a fixed name deleted before its raise: %s", got.stderr)
	cohort, found, err := readPrepareCohort(ctx, rig.records(), id)
	require.NoError(t, err)
	require.True(t, found)
	gone := cohort.member("s-gone")
	require.NotNil(t, gone, "the cohort dropped the fixed name deleted before its raise")
	require.Equal(t, seqs[1], gone.Floor)
}

// The cutover's race regressions and its full-cap row on the hooked nats-server
// (packages/envoy/scripts/forced-order.sh -C packages/envoy/infra ./consumernames/), on 2.15.1 and
// 2.10.29: each runs its row's body (prepare_test.go, fullcap_test.go, metaleader_test.go) on the
// server this build links.

// sessionFamilyRow runs row, a row of the session family, which registers a session in the fresh
// mode after the window. The session consumers and the listener refuse every 2.10 server by design
// (packages/envoy/natsversion: every 2.10 has the filter-scan bug), so on 2.10.29 the row proves
// that refusal on the stack's own servers and skips: a skip is no pass.
func sessionFamilyRow(t *testing.T, row func(*testing.T)) {
	t.Helper()
	if strings.HasPrefix(natsserver.VERSION, "2.10.") {
		h := newStack(t)
		nc, _ := h.operator()
		err := natsversion.ConnectedRefusal(context.Background(), nc)
		require.Error(t, err, "nats-server %s is not refused by the session family", natsserver.VERSION)
		t.Skipf("the session family refuses nats-server %s by design: %v", natsserver.VERSION, err)
	}
	row(t)
}

func TestForcedOrderTheCutoverBoundaryExpiryBeforeTheListing(t *testing.T) {
	sessionFamilyRow(t, boundaryExpiryRow)
}

func TestForcedOrderTheCutoverBoundaryPreBoundaryStart(t *testing.T) {
	sessionFamilyRow(t, boundaryPreBoundaryRow)
}

func TestForcedOrderTheCutoverBoundaryHasNoEnd(t *testing.T) {
	sessionFamilyRow(t, boundaryNoEndRow)
}

func TestForcedOrderTheCutoverBoundaryLongAfterTheRelease(t *testing.T) {
	sessionFamilyRow(t, boundaryLongAfterRow)
}

func TestForcedOrderTheCutoverBoundaryOnAnEmptyStream(t *testing.T) {
	sessionFamilyRow(t, boundaryEmptyStreamRow)
}

// The full-cap window converts whatever fixed names the cap holds, the session family's here, by
// the operator alone: no session registers, so it runs on both servers.
func TestForcedOrderAFullCapWindowReachesConversion(t *testing.T) {
	fullCapWindowRow(t)
}

// The meta-leader row's term check is the operator's alone: it runs on both servers.
func TestForcedOrderAMetaLeaderChangeAfterTheBarrierDiscardsTheListing(t *testing.T) {
	metaLeaderRow(t)
}
