package writerhold

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// deleteRow is a database admin deleting the hold row; the log keeps its gens.
func (h *heldStack) deleteRow() {
	h.t.Helper()
	_, err := h.s.Broker.Pool.Exec(context.Background(), `delete from nats_hold`)
	require.NoError(h.t, err)
}

// A row lost after `hold set` moved it to held and before set's done entry comes back held: `hold
// restore --hold` restores held whenever the hold's journal holds a `hold set` entry, done or not,
// and the hold has not ended, since the journal cannot tell whether the move happened and held
// fails closed. Writers stay refused, and the hold's way out works again: after a set that stopped
// at its probe, `hold abort`; after one that stopped past its restart limit, `hold release`; after
// one that crashed after its move, set's rerun and then release. The control is the restore as it
// stood: it restored held only after a done `hold set`, so it sent released, which the route
// refuses after a held log entry (400 bad_transition), and it exited 1 naming `hold restore --hold`,
// the command just run, with no row written.
func TestHoldARowLostAfterSetsMoveIsRestoredHeld(t *testing.T) {
	restoredHeld := func(t *testing.T, h *heldStack, id string) {
		t.Helper()
		before := h.row()
		h.deleteRow()
		code, stdout, stderr := h.run(actionRestore, "--hold", id, "--reason", "the lost row of a hold set moved")
		require.Zero(t, code, "hold restore --hold of a hold whose set moved the row: %s", stderr)
		row := h.row()
		require.Equal(t, natsgrant.OperatorPhaseHeld, row.Phase)
		require.Equal(t, id, row.HoldID)
		require.Greater(t, row.Gen, before.Gen, "the restored row's gen")
		require.Contains(t, stdout, "phase held")
		require.False(t, h.listenerAdmitted(), "a writer was admitted under the restored row")
	}
	t.Run("set stopped at its probe, then abort", func(t *testing.T) {
		h := newHeldStack(t)
		h.seedProtectedStreams()
		id := h.stoppedInHeld(holdrecord.WorkflowBucketsCreate)
		restoredHeld(t, h, id)
		code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", "the way out after the restore")
		require.Zero(t, code, "abort after the restore: %s", stderr)
		require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
		require.True(t, h.listenerAdmitted(), "a writer was refused after the abort")
	})
	t.Run("set stopped past its restart limit, then release", func(t *testing.T) {
		h := newHeldStack(t)
		h.seedProtectedStreams()
		h.op.RestartLimit = time.Millisecond
		code, _, _ := h.run(actionSet, "--workflow", string(holdrecord.WorkflowBucketsCreate), "--reason", "a set that stops at its restart")
		h.op.RestartLimit = 3 * time.Minute
		require.NotZero(t, code)
		id := h.row().HoldID
		require.Equal(t, holdrecord.StateStop, latestState(h.journal(id), holdrecord.CommandSet))
		restoredHeld(t, h, id)
		h.awaitStreamLeaders()
		code, _, stderr := h.run(actionRelease, "--hold", id, "--reason", "nothing changed")
		require.Zero(t, code, "release after the restore: %s", stderr)
		require.True(t, h.listenerAdmitted(), "a writer was refused after the release")
	})
	t.Run("set crashed after its move, then its rerun and release", func(t *testing.T) {
		h := newHeldStack(t)
		h.seedProtectedStreams()
		ctx := context.Background()
		id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
		h.crashedSetIntent(id)
		_, err := h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhaseHeld, id, "hold set's move, then its crash")
		require.NoError(t, err)
		restoredHeld(t, h, id)
		h.awaitStreamLeaders()
		code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the rerun after the restore")
		require.Zero(t, code, "hold set's rerun after the restore: %s", stderr)
		code, _, stderr = h.run(actionRelease, "--hold", id, "--reason", "nothing changed")
		require.Zero(t, code, "release after the rerun: %s", stderr)
		require.True(t, h.listenerAdmitted(), "a writer was refused after the release")
	})
}
