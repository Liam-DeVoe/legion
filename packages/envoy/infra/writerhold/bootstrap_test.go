package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// A hold row lost in the middle of a hold is not bootstrapped back released: with the row held and
// then deleted from Postgres (the log keeps its gens), `hold restore --bootstrap` is refused,
// naming the log's next gen, writes no row, and a writer stays refused, since the row cannot be
// read. `hold restore --hold` of the hold's own id is the way back: the row returns held, and the
// writer is refused under it. The control is the bootstrap as it stood (RestoreRow at gen 1
// released, following the 409's restore_gen): it wrote the next gen released with no hold id, so
// the hold ended and the writer was admitted (red at 36e8cf250e: "gen 4, phase released").
func TestHoldABootstrapAfterALossMidHoldIsRefused(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.s.HoldWithoutTheSteps(t, id)
	_, err := h.s.Broker.Pool.Exec(ctx, `delete from nats_hold`)
	require.NoError(t, err, "the test's admin deletes the row")
	rows := func() int {
		t.Helper()
		var n int
		require.NoError(t, h.s.Broker.Pool.QueryRow(ctx, `select count(*) from nats_hold`).Scan(&n))
		return n
	}
	code, _, stderr := h.run(actionRestore, "--bootstrap", "--reason", "the row is gone")
	require.Equal(t, 1, code, "a bootstrap after a loss mid-hold")
	require.Contains(t, stderr, "the log's next gen is", "the refusal names the log's next gen")
	require.Contains(t, stderr, "--hold", "the refusal names the way back")
	require.Zero(t, rows(), "the refused bootstrap wrote a row")
	_, err = connectListener(h.s)
	require.Error(t, err, "a writer was admitted after the refused bootstrap")

	code, _, _ = h.run(actionRestore, "--hold", id, "--reason", "the row is gone")
	require.Zero(t, code, "the restore of the hold's own row")
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhaseHeld, row.Phase)
	require.Equal(t, id, row.HoldID)
	_, err = connectListener(h.s)
	require.Error(t, err, "a writer was admitted under the restored held row")
}
